package tuner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"dvb-web/internal/db"
	"dvb-web/internal/epg"
	"dvb-web/internal/transcoder"
)

var (
	ErrAllTunersBusy = errors.New("all tuners are currently in use on different frequencies")
	ErrTunerNotFound = errors.New("tuner adapter not found")
)

type ViewerSession struct {
	ID          string      `json:"id"`
	IP          string      `json:"ip"`
	UserAgent   string      `json:"user_agent"`
	ChannelID   int64       `json:"channel_id"`
	ChannelName string      `json:"channel_name"`
	StreamKey   string      `json:"stream_key,omitempty"`
	ConnectedAt time.Time   `json:"connected_at"`
	KickChan    chan string `json:"-"`
}

type ActiveRecordingInfo struct {
	RecordingID int64     `json:"recording_id"`
	ChannelID   int64     `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	Title       string    `json:"title"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	FilePath    string    `json:"file_path"`
}

type TrackInfo struct {
	Index    int    `json:"index"`
	Type     string `json:"type"` // "audio" or "subtitle"
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

type ChannelTracks struct {
	AudioTracks    []TrackInfo `json:"audio_tracks"`
	SubtitleTracks []TrackInfo `json:"subtitle_tracks"`
	HasTeletext    bool        `json:"has_teletext"`
	HbbtvURL       string      `json:"hbbtv_url,omitempty"`
}

type TunerStatus struct {
	ID               int                  `json:"id"`
	Name             string               `json:"name"`
	FrontendPath     string               `json:"frontend_path"`
	Status           string               `json:"status"` // "idle", "active", "scanning"
	CurrentFrequency int64                `json:"current_frequency"`
	CurrentChannel   string               `json:"current_channel"`
	ActiveChannels   []string             `json:"active_channels,omitempty"`
	Enabled          bool                 `json:"enabled"`
	Viewers          []*ViewerSession     `json:"viewers"`
	IsMock           bool                 `json:"is_mock"`
	Signal           *SignalInfo          `json:"signal,omitempty"`
	ActiveRecording  *ActiveRecordingInfo `json:"active_recording,omitempty"`
}

type TunerConflictError struct {
	ActiveTuners       []TunerStatus         `json:"active_tuners"`
	HasActiveRecording bool                  `json:"has_active_recording"`
	ActiveRecordings   []ActiveRecordingInfo `json:"active_recordings,omitempty"`
}

func (e *TunerConflictError) Error() string {
	if e.HasActiveRecording {
		return "tuner conflict: an active recording is in progress on this or another frequency"
	}
	return "tuner conflict: all tuners are active on different frequencies"
}

// ChannelStream represents an active transcoded stream for a specific channel within a multiplex
type ChannelStream struct {
	ChannelID   int64
	ServiceID   int
	ChannelName string
	pipeline    *transcoder.Pipeline
	pipeWriter  *io.PipeWriter
	tsQueue     chan []byte
	subscribers map[string]chan []byte
	cancel      context.CancelFunc
	mu          sync.Mutex
}

func (cs *ChannelStream) WriteTS(chunk []byte) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.tsQueue != nil {
		select {
		case cs.tsQueue <- chunk:
		default:
		}
	}
}

func (cs *ChannelStream) Subscribe(sessionID string) chan []byte {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.subscribers == nil {
		cs.subscribers = make(map[string]chan []byte)
	}
	ch := make(chan []byte, 512)
	cs.subscribers[sessionID] = ch
	return ch
}

func (cs *ChannelStream) Unsubscribe(sessionID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if ch, ok := cs.subscribers[sessionID]; ok {
		delete(cs.subscribers, sessionID)
		close(ch)
	}
}

func (cs *ChannelStream) ViewerCount() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.subscribers)
}

func (cs *ChannelStream) Stop() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.cancel != nil {
		cs.cancel()
		cs.cancel = nil
	}
	if cs.tsQueue != nil {
		close(cs.tsQueue)
		cs.tsQueue = nil
	}
	if cs.pipeWriter != nil {
		_ = cs.pipeWriter.Close()
		cs.pipeWriter = nil
	}
	for id, ch := range cs.subscribers {
		close(ch)
		delete(cs.subscribers, id)
	}
	if cs.pipeline != nil {
		cs.pipeline.Stop()
		cs.pipeline = nil
	}
	log.Printf("[ChannelStream] Stopped stream for channel %s (ID: %d)", cs.ChannelName, cs.ChannelID)
}

func GetStreamKey(channelID int64, audioTrack, subTrack, subOffset int) string {
	if audioTrack < 0 {
		audioTrack = 0
	}
	if subTrack < 0 {
		return fmt.Sprintf("%d_a%d_s-1", channelID, audioTrack)
	}
	return fmt.Sprintf("%d_a%d_s%d_o%d", channelID, audioTrack, subTrack, subOffset)
}

func getStreamKey(channelID int64, audioTrack, subTrack, subOffset int) string {
	return GetStreamKey(channelID, audioTrack, subTrack, subOffset)
}

type Tuner struct {
	ID               int
	Name             string
	FrontendPath     string
	DVRPath          string
	DemuxPath        string
	IsMock           bool
	Enabled          bool
	mu               sync.Mutex
	status           string // "idle", "active", "scanning"
	currentFrequency int64
	currentChannelID int64
	currentChannel   string
	viewers          map[string]*ViewerSession
	channels         map[string]*ChannelStream
	activeRecording  *ActiveRecordingInfo
	videoUDPPort     int
	audioUDPPort     int
	frontendFD       int
	demuxFD          int
	dvrFD            int
	tsCancel         context.CancelFunc
	epgParser        *epg.Parser
	idleTimer        *time.Timer
	detectedTracks   map[int]ChannelTracks // serviceID -> detected audio & subtitle tracks
	pmtPIDs          map[int]int           // pmtPID -> serviceID
}

type Pool struct {
	mu         sync.RWMutex
	tuners     []*Tuner
	db         *db.Database
	transcoder *transcoder.Detector
	epgParser  *epg.Parser
	basePort   int
	isMockMode bool
}

func NewPool(database *db.Database, detector *transcoder.Detector, epgP *epg.Parser, forceMock bool) *Pool {
	p := &Pool{
		tuners:     make([]*Tuner, 0),
		db:         database,
		transcoder: detector,
		epgParser:  epgP,
		basePort:   20000,
		isMockMode: forceMock,
	}

	p.discover()
	return p
}

func (p *Pool) discover() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.tuners = nil

	if !p.isMockMode {
		matches, _ := filepath.Glob("/dev/dvb/adapter*")
		re := regexp.MustCompile(`/dev/dvb/adapter(\d+)`)

		portCounter := p.basePort
		for _, m := range matches {
			sub := re.FindStringSubmatch(m)
			if len(sub) == 2 {
				id, _ := strconv.Atoi(sub[1])
				t := &Tuner{
					ID:             id,
					Name:           fmt.Sprintf("DVB Adapter %d", id),
					FrontendPath:   filepath.Join(m, "frontend0"),
					DVRPath:        filepath.Join(m, "dvr0"),
					DemuxPath:      filepath.Join(m, "demux0"),
					status:         "idle",
					viewers:        make(map[string]*ViewerSession),
					channels:       make(map[string]*ChannelStream),
					detectedTracks: make(map[int]ChannelTracks),
					pmtPIDs:        make(map[int]int),
					videoUDPPort:   portCounter,
					audioUDPPort:   portCounter + 2,
					epgParser:      p.epgParser,
					Enabled:        true,
				}
				portCounter += 4
				p.tuners = append(p.tuners, t)
				log.Printf("[TunerPool] Discovered hardware tuner: %s (%s)", t.Name, m)
			}
		}
	}

	// If no hardware adapters exist or mock mode was requested, create a virtual tuner
	if len(p.tuners) == 0 {
		log.Printf("[TunerPool] No physical DVB tuners detected; enabling Virtual / Simulated Tuner")
		mockTuner := &Tuner{
			ID:           0,
			Name:         "Virtual DVB Tuner (Mock)",
			IsMock:       true,
			status:       "idle",
			Enabled:      true,
			viewers:      make(map[string]*ViewerSession),
			channels:     make(map[string]*ChannelStream),
			videoUDPPort: p.basePort,
			audioUDPPort: p.basePort + 2,
			epgParser:    p.epgParser,
		}
		p.tuners = append(p.tuners, mockTuner)
	}
}

// GetStatus returns the state of all tuners and connected viewers
func (p *Pool) GetStatus() []TunerStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var statuses []TunerStatus
	for _, t := range p.tuners {
		t.mu.Lock()
		var viewersList []*ViewerSession
		for _, v := range t.viewers {
			viewersList = append(viewersList, &ViewerSession{
				ID:          v.ID,
				IP:          v.IP,
				UserAgent:   v.UserAgent,
				ChannelID:   v.ChannelID,
				ChannelName: v.ChannelName,
				ConnectedAt: v.ConnectedAt,
			})
		}
		var sig *SignalInfo
		if t.status == "active" {
			if t.IsMock {
				sig = &SignalInfo{
					HasLock:        true,
					HasSignal:      true,
					SignalStrength: 95,
					SignalQuality:  98,
					SNR:            26.5,
					BER:            0,
				}
			} else if t.frontendFD > 0 {
				s := ReadFrontendSignalInfo(t.frontendFD)
				sig = &s
			}
		}
		var activeChNames []string
		seenCh := make(map[string]bool)
		for _, cs := range t.channels {
			if !seenCh[cs.ChannelName] {
				seenCh[cs.ChannelName] = true
				activeChNames = append(activeChNames, cs.ChannelName)
			}
		}
		chDisplay := t.currentChannel
		if len(activeChNames) > 0 {
			chDisplay = strings.Join(activeChNames, ", ")
		}

		var recCopy *ActiveRecordingInfo
		if t.activeRecording != nil {
			c := *t.activeRecording
			recCopy = &c
		}

		statuses = append(statuses, TunerStatus{
			ID:               t.ID,
			Name:             t.Name,
			FrontendPath:     t.FrontendPath,
			Status:           t.status,
			CurrentFrequency: t.currentFrequency,
			CurrentChannel:   chDisplay,
			ActiveChannels:   activeChNames,
			Enabled:          t.Enabled,
			Viewers:          viewersList,
			IsMock:           t.IsMock,
			Signal:           sig,
			ActiveRecording:  recCopy,
		})
		t.mu.Unlock()
	}
	return statuses
}

// SetTunerEnabled toggles whether a tuner is active and available for use
func (p *Pool) SetTunerEnabled(id int, enabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.tuners {
		if t.ID == id {
			t.mu.Lock()
			t.Enabled = enabled
			t.mu.Unlock()
			return nil
		}
	}
	return ErrTunerNotFound
}

func (t *Tuner) stopHardwareLocked() {
	if t.idleTimer != nil {
		t.idleTimer.Stop()
		t.idleTimer = nil
	}
	if t.tsCancel != nil {
		t.tsCancel()
		t.tsCancel = nil
	}
	for key, cs := range t.channels {
		cs.Stop()
		delete(t.channels, key)
	}
	if t.dvrFD > 0 {
		_ = syscall.Close(t.dvrFD)
		t.dvrFD = 0
	}
	if t.demuxFD > 0 {
		StopDemuxWildcard(t.demuxFD)
		t.demuxFD = 0
	}
	if t.frontendFD > 0 {
		_ = syscall.Close(t.frontendFD)
		t.frontendFD = 0
	}
	t.status = "idle"
	t.currentFrequency = 0
	t.currentChannel = ""
	t.currentChannelID = 0
	t.activeRecording = nil
	t.viewers = make(map[string]*ViewerSession)
	t.detectedTracks = make(map[int]ChannelTracks)
	t.pmtPIDs = make(map[int]int)
}

func (t *Tuner) Subscribe(channelID int64, sessionID string) chan []byte {
	return t.SubscribeTrack(channelID, 0, -1, 0, sessionID)
}

func (t *Tuner) SubscribeTrack(channelID int64, audioTrack, subTrack, subOffset int, sessionID string) chan []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.channels == nil {
		return nil
	}
	key := getStreamKey(channelID, audioTrack, subTrack, subOffset)
	if cs, ok := t.channels[key]; ok {
		return cs.Subscribe(sessionID)
	}
	for _, cs := range t.channels {
		if cs.ChannelID == channelID {
			return cs.Subscribe(sessionID)
		}
	}
	return nil
}

func (t *Tuner) Unsubscribe(channelID int64, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.channels == nil {
		return
	}
	for key, cs := range t.channels {
		if cs.ChannelID == channelID {
			cs.Unsubscribe(sessionID)
			if cs.ViewerCount() == 0 {
				cs.Stop()
				delete(t.channels, key)
			}
		}
	}
}

func (t *Tuner) CurrentChannelID() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentChannelID
}

// StopAll terminates all active pipelines, closes demux file descriptors, and releases all tuners
func (p *Pool) StopAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.tuners {
		t.mu.Lock()
		t.stopHardwareLocked()
		t.mu.Unlock()
	}
}

func (p *Pool) startChannelStreamLocked(t *Tuner, channel *db.Channel, audioTrack, subTrack, subOffset int) (*ChannelStream, error) {
	var inputPipe io.Reader
	var pw *io.PipeWriter

	if !t.IsMock {
		pr, pWriter := io.Pipe()
		inputPipe = pr
		pw = pWriter
	}

	// Validate subtitle track existence to prevent FFmpeg error 234
	if subTrack >= 0 && !t.IsMock {
		if t.detectedTracks != nil {
			if det, ok := t.detectedTracks[channel.ServiceID]; ok {
				if len(det.SubtitleTracks) <= subTrack {
					log.Printf("[TunerPool] Channel %s (SID: %d) has no subtitle track %d; forcing subTrack = -1",
						channel.Name, channel.ServiceID, subTrack)
					subTrack = -1
				}
			}
		}
	}

	cfg := transcoder.PipelineConfig{
		InputPath:        t.DVRPath,
		InputPipe:        inputPipe,
		IsMock:           t.IsMock,
		IsRadio:          channel.IsRadio,
		ServiceID:        channel.ServiceID,
		VideoUDPPort:     t.videoUDPPort,
		AudioUDPPort:     t.audioUDPPort,
		AudioTrackIdx:    audioTrack,
		SubtitleTrack:    subTrack,
		SubtitleOffsetMs: subOffset,
		HWType:           transcoder.HardwareType(p.db.GetSetting("transcoder_mode", "auto")),
		VideoBitrate:     p.db.GetSetting("video_bitrate", "2500k"),
	}

	pipeline, err := p.transcoder.StartPipeline(cfg)
	if err != nil && subTrack >= 0 {
		log.Printf("[TunerPool] Stream start failed with subTrack %d: %v. Retrying without subtitles...", subTrack, err)
		cfg.SubtitleTrack = -1
		pipeline, err = p.transcoder.StartPipeline(cfg)
	}
	if err != nil {
		if pw != nil {
			_ = pw.Close()
		}
		return nil, fmt.Errorf("failed to start stream for channel %s: %w", channel.Name, err)
	}

	cs := &ChannelStream{
		ChannelID:   channel.ID,
		ServiceID:   channel.ServiceID,
		ChannelName: channel.Name,
		pipeline:    pipeline,
		pipeWriter:  pw,
		subscribers: make(map[string]chan []byte),
	}

	if !t.IsMock && pw != nil {
		cs.tsQueue = make(chan []byte, 2048)
		go func() {
			defer pw.Close()
			for chunk := range cs.tsQueue {
				if _, err := pw.Write(chunk); err != nil {
					return
				}
			}
		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cs.cancel = cancel

	stdout := pipeline.Stdout()
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, err := stdout.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])

				cs.mu.Lock()
				for _, ch := range cs.subscribers {
					select {
					case ch <- chunk:
					default:
					}
				}
				cs.mu.Unlock()
			}
		}
	}()

	log.Printf("[TunerPool] Started ChannelStream for %s (SID: %d, Audio: %d, Sub: %d) on Tuner %d",
		channel.Name, channel.ServiceID, audioTrack, subTrack, t.ID)
	return cs, nil
}

// Standard MPEG-TS Null Packet (PID 0x1FFF, payload 0xFF)
var tsNullPacket = func() [188]byte {
	var p [188]byte
	p[0] = 0x47
	p[1] = 0x1F
	p[2] = 0xFF
	p[3] = 0x10 // payload only, CC 0
	for i := 4; i < 188; i++ {
		p[i] = 0xFF
	}
	return p
}()

// SanitizeAndSyncTS processes raw DVR input bytes, ensures 188-byte MPEG-TS packet alignment,
// and substitutes packets flagged with the Transport Error Indicator (TEI, bit 7 of byte 1)
// with standard MPEG-TS Null Packets (PID 0x1FFF). This prevents broken bitstreams, corrupt macroblocks,
// missing audio headers, and severe PTS/DTS discontinuities from crashing decoders during high BER.
func SanitizeAndSyncTS(input []byte, leftover []byte) ([]byte, []byte) {
	if len(leftover) > 0 {
		input = append(leftover, input...)
	}

	limit := len(input)
	if limit < 188 {
		return nil, input
	}

	var output []byte
	i := 0

	for i+188 <= limit {
		if input[i] != 0x47 {
			// Lost synchronization. Seek forward for a confirmed 0x47 packet boundary.
			found := false
			for j := i + 1; j+188 <= limit; j++ {
				if input[j] == 0x47 {
					// If another packet boundary is within this buffer, verify it also has 0x47
					if j+376 <= limit && input[j+188] != 0x47 {
						continue
					}
					i = j
					found = true
					break
				}
			}
			if !found {
				// No valid sync byte found in remainder of input.
				rem := limit - i
				if rem > 187 {
					rem = 187
				}
				newLeftover := make([]byte, rem)
				copy(newLeftover, input[limit-rem:])
				return output, newLeftover
			}
		}

		packet := input[i : i+188]
		i += 188

		// Check Transport Error Indicator (TEI bit 0x80 in byte 1).
		// Tuner hardware demodulators set TEI to 1 when uncorrectable bit errors occur (high BER).
		if (packet[1] & 0x80) != 0 {
			// Replace corrupt packet with null packet (PID 0x1FFF)
			// Preserves multiplex timing without feeding corrupt NAL/PES payloads into decoders
			output = append(output, tsNullPacket[:]...)
		} else {
			output = append(output, packet...)
		}
	}

	var newLeftover []byte
	if i < limit {
		newLeftover = make([]byte, limit-i)
		copy(newLeftover, input[i:])
	}

	return output, newLeftover
}

func (p *Pool) startHardwareTSPumpLocked(t *Tuner) {
	if t.IsMock || t.dvrFD <= 0 {
		return
	}
	if t.tsCancel != nil {
		t.tsCancel()
		t.tsCancel = nil
	}
	tsCtx, tsCancel := context.WithCancel(context.Background())
	t.tsCancel = tsCancel

	channels, _ := p.db.ListChannels()
	channelMap := make(map[int]int64)
	for _, c := range channels {
		if c.Frequency == t.currentFrequency {
			channelMap[c.ServiceID] = c.ID
			if c.ServiceID >= 3100 && c.ServiceID <= 3700 {
				channelMap[c.ServiceID-2000] = c.ID
			} else if c.ServiceID >= 1100 && c.ServiceID <= 1700 {
				channelMap[c.ServiceID+2000] = c.ID
			}
		}
	}

	dvrFD := t.dvrFD
	ep := t.epgParser
	tsQueue := make(chan []byte, 2048)
	parseQueue := make(chan []byte, 256)

	// Background EPG & PMT parser goroutine
	go func() {
		for chunk := range parseQueue {
			if ep != nil {
				ep.FeedTSChunk(chunk, channelMap)
			}
			t.FeedPMTChunk(chunk, channelMap)
		}
	}()

	// Hardware DVR reader goroutine with 188-byte TS packet synchronization and TEI filtering
	go func() {
		defer close(tsQueue)
		defer close(parseQueue)
		readBuf := make([]byte, 188*128) // 24KB chunk
		var leftover []byte

		for {
			select {
			case <-tsCtx.Done():
				return
			default:
			}

			n, err := syscall.Read(dvrFD, readBuf)
			if err != nil {
				if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				return
			}
			if n <= 0 {
				continue
			}

			cleanData, newLeftover := SanitizeAndSyncTS(readBuf[:n], leftover)
			leftover = newLeftover

			if len(cleanData) == 0 {
				continue
			}

			select {
			case tsQueue <- cleanData:
			default:
			}

			select {
			case parseQueue <- cleanData:
			default:
			}
		}
	}()

	// Multiplex TS distributor: fans out raw TS chunks to all active channel streams
	go func() {
		for chunk := range tsQueue {
			t.mu.Lock()
			for _, cs := range t.channels {
				cs.WriteTS(chunk)
			}
			t.mu.Unlock()
		}
	}()
}

// AllocateChannel matches or allocates a tuner for a viewer session with default tracks
func (p *Pool) AllocateChannel(channel *db.Channel, session *ViewerSession) (*Tuner, error) {
	t, _, err := p.AllocateChannelTrack(channel, 0, -1, 0, session)
	return t, err
}

// AllocateChannelTrack matches or allocates a tuner with specific audio & subtitle tracks
func (p *Pool) AllocateChannelTrack(channel *db.Channel, audioTrack, subTrack, subOffset int, session *ViewerSession) (*Tuner, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Sanitize subtitle track if the channel does not broadcast subtitles
	if subTrack >= 0 && !p.isMockMode {
		tracks := p.getChannelTracksLocked(channel)
		if len(tracks.SubtitleTracks) == 0 || subTrack >= len(tracks.SubtitleTracks) {
			log.Printf("[TunerPool] Subtitles requested (track %d) but none broadcast for channel %s; defaulting to subTrack = -1", subTrack, channel.Name)
			subTrack = -1
		}
	}

	streamKey := getStreamKey(channel.ID, audioTrack, subTrack, subOffset)

	// 1. Check if a tuner is ALREADY active on this exact frequency (channel group / multiplex)!
	for _, t := range p.tuners {
		t.mu.Lock()
		if t.status == "active" && t.currentFrequency == channel.Frequency {
			session.ChannelID = channel.ID
			session.ChannelName = channel.Name
			session.StreamKey = streamKey
			session.ConnectedAt = time.Now()
			t.viewers[session.ID] = session

			if t.idleTimer != nil {
				t.idleTimer.Stop()
				t.idleTimer = nil
			}

			// Clean up this session from any OTHER tuner it might have been on
			for _, other := range p.tuners {
				if other != t {
					other.mu.Lock()
					delete(other.viewers, session.ID)
					for key, cs := range other.channels {
						cs.Unsubscribe(session.ID)
						if cs.ViewerCount() == 0 {
							cs.Stop()
							delete(other.channels, key)
						}
					}
					if len(other.viewers) == 0 && other.activeRecording == nil && other.status == "active" {
						other.stopHardwareLocked()
					}
					other.mu.Unlock()
				}
			}

			// Clean up this session from any other stream on this same tuner
			for key, cs := range t.channels {
				if key != streamKey {
					cs.Unsubscribe(session.ID)
					if cs.ViewerCount() == 0 {
						cs.Stop()
						delete(t.channels, key)
					}
				}
			}

			// Ensure this channel & track combination has an active ChannelStream on this multiplex!
			if _, exists := t.channels[streamKey]; !exists {
				cs, err := p.startChannelStreamLocked(t, channel, audioTrack, subTrack, subOffset)
				if err != nil {
					delete(t.viewers, session.ID)
					t.mu.Unlock()
					return nil, subTrack, err
				}
				t.channels[streamKey] = cs
			}

			t.currentChannelID = channel.ID
			t.currentChannel = channel.Name

			t.mu.Unlock()
			log.Printf("[TunerPool] Session %s (IP: %s) joined Tuner %d multiplex (%d Hz, Channel: %s, Stream: %s)",
				session.ID, session.IP, t.ID, channel.Frequency, channel.Name, streamKey)
			return t, subTrack, nil
		}
		t.mu.Unlock()
	}

	// 2. Detach this session from any previous tuner (switching frequencies)
	for _, t := range p.tuners {
		t.mu.Lock()
		if _, exists := t.viewers[session.ID]; exists {
			delete(t.viewers, session.ID)
			for key, cs := range t.channels {
				cs.Unsubscribe(session.ID)
				if cs.ViewerCount() == 0 {
					cs.Stop()
					delete(t.channels, key)
				}
			}
			if len(t.viewers) == 0 && t.activeRecording == nil && t.status == "active" {
				t.stopHardwareLocked()
			}
		}
		t.mu.Unlock()
	}

	// 3. Look for an idle tuner to tune this new frequency
	for _, t := range p.tuners {
		t.mu.Lock()
		if !t.Enabled {
			t.mu.Unlock()
			continue
		}
		if t.status == "idle" {
			session.ChannelID = channel.ID
			session.ChannelName = channel.Name
			session.StreamKey = streamKey
			session.ConnectedAt = time.Now()

			t.status = "active"
			t.currentFrequency = channel.Frequency
			t.currentChannelID = channel.ID
			t.currentChannel = channel.Name
			t.viewers[session.ID] = session

			if t.tsCancel != nil {
				t.tsCancel()
				t.tsCancel = nil
			}
			for _, cs := range t.channels {
				cs.Stop()
			}
			t.channels = make(map[string]*ChannelStream)
			t.detectedTracks = make(map[int]ChannelTracks)
			t.pmtPIDs = make(map[int]int)

			if t.idleTimer != nil {
				t.idleTimer.Stop()
				t.idleTimer = nil
			}

			// Tune physical frontend if this is real DVB hardware
			if !t.IsMock {
				// Stop demux and close dvr before retuning to prevent corrupt packet bleeding from old frequency
				if t.demuxFD > 0 {
					StopDemuxWildcard(t.demuxFD)
					t.demuxFD = 0
				}
				if t.dvrFD > 0 {
					_ = syscall.Close(t.dvrFD)
					t.dvrFD = 0
				}

				if t.frontendFD <= 0 {
					feFD, err := syscall.Open(t.FrontendPath, syscall.O_RDWR, 0)
					if err != nil {
						t.status = "idle"
						delete(t.viewers, session.ID)
						t.mu.Unlock()
						return nil, subTrack, fmt.Errorf("failed to open frontend device %s: %w", t.FrontendPath, err)
					}
					t.frontendFD = feFD
				}

				bw := channel.Bandwidth
				if bw == 0 {
					bw = 8000000
				}
				if err := TuneFrontendFD(t.frontendFD, uint32(channel.Frequency), uint32(bw), channel.DeliverySystem); err != nil {
					log.Printf("[TunerPool] Warning: frontend tune error: %v", err)
				}

				// Start fresh wildcard demux filter and open clean DVR device AFTER frequency lock
				dmxFD, err := StartDemuxWildcard(t.DemuxPath)
				if err != nil {
					log.Printf("[TunerPool] Warning: start demux wildcard error: %v", err)
				} else {
					t.demuxFD = dmxFD
				}

				dvrFD, err := syscall.Open(t.DVRPath, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					log.Printf("[TunerPool] Warning: open DVR non-blocking error: %v", err)
				} else {
					t.dvrFD = dvrFD
					// Drain any residual stale packets from kernel buffer before piping to transcoder
					drainBuf := make([]byte, 64*1024)
					for {
						n, _ := syscall.Read(dvrFD, drainBuf)
						if n <= 0 {
							break
						}
					}
				}
			}

			// Start channel stream for this channel
			cs, err := p.startChannelStreamLocked(t, channel, audioTrack, subTrack, subOffset)
			if err != nil {
				t.stopHardwareLocked()
				delete(t.viewers, session.ID)
				t.mu.Unlock()
				return nil, subTrack, fmt.Errorf("failed to start stream pipeline: %w", err)
			}
			t.channels[streamKey] = cs

			// Start multiplex TS pump for real DVB hardware
			p.startHardwareTSPumpLocked(t)

			t.mu.Unlock()
			log.Printf("[TunerPool] Allocated Tuner %d for Session %s (IP: %s) on Channel %s (%s)",
				t.ID, session.ID, session.IP, channel.Name, streamKey)
			return t, subTrack, nil
		}
		t.mu.Unlock()
	}

	// 4. All tuners busy on different frequencies -> Tuner Conflict
	var activeStatuses []TunerStatus
	var activeRecs []ActiveRecordingInfo
	hasActiveRecording := false

	for _, t := range p.tuners {
		t.mu.Lock()
		var viewersList []*ViewerSession
		for _, v := range t.viewers {
			viewersList = append(viewersList, &ViewerSession{
				ID:          v.ID,
				IP:          v.IP,
				UserAgent:   v.UserAgent,
				ChannelID:   v.ChannelID,
				ChannelName: v.ChannelName,
				ConnectedAt: v.ConnectedAt,
			})
		}
		var recCopy *ActiveRecordingInfo
		if t.activeRecording != nil {
			c := *t.activeRecording
			recCopy = &c
			hasActiveRecording = true
			activeRecs = append(activeRecs, c)
		}
		activeStatuses = append(activeStatuses, TunerStatus{
			ID:               t.ID,
			Name:             t.Name,
			Status:           t.status,
			CurrentFrequency: t.currentFrequency,
			CurrentChannel:   t.currentChannel,
			Viewers:          viewersList,
			IsMock:           t.IsMock,
			ActiveRecording:  recCopy,
		})
		t.mu.Unlock()
	}

	return nil, subTrack, &TunerConflictError{
		ActiveTuners:       activeStatuses,
		HasActiveRecording: hasActiveRecording,
		ActiveRecordings:   activeRecs,
	}
}

// AllocateRecording allocates a tuner for an active recording session
func (p *Pool) AllocateRecording(channel *db.Channel, recInfo *ActiveRecordingInfo) (*Tuner, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	streamKey := getStreamKey(channel.ID, 0, -1, 0)

	// 1. Check if a tuner is already on this channel's multiplex
	for _, t := range p.tuners {
		t.mu.Lock()
		if t.status == "active" && t.currentFrequency == channel.Frequency {
			t.activeRecording = recInfo
			if t.idleTimer != nil {
				t.idleTimer.Stop()
				t.idleTimer = nil
			}

			if _, exists := t.channels[streamKey]; !exists {
				cs, err := p.startChannelStreamLocked(t, channel, 0, -1, 0)
				if err != nil {
					t.activeRecording = nil
					t.mu.Unlock()
					return nil, err
				}
				t.channels[streamKey] = cs
			}
			t.mu.Unlock()
			log.Printf("[TunerPool] Recording #%d joined Tuner %d multiplex (%d Hz, Channel: %s)",
				recInfo.RecordingID, t.ID, channel.Frequency, channel.Name)
			return t, nil
		}
		t.mu.Unlock()
	}

	// 2. Look for an idle tuner
	for _, t := range p.tuners {
		t.mu.Lock()
		if !t.Enabled {
			t.mu.Unlock()
			continue
		}
		if t.status == "idle" {
			t.status = "active"
			t.currentFrequency = channel.Frequency
			t.currentChannelID = channel.ID
			t.currentChannel = channel.Name
			t.activeRecording = recInfo
			t.channels = make(map[string]*ChannelStream)
			t.detectedTracks = make(map[int]ChannelTracks)
			t.pmtPIDs = make(map[int]int)

			if t.idleTimer != nil {
				t.idleTimer.Stop()
				t.idleTimer = nil
			}

			if !t.IsMock {
				if t.demuxFD > 0 {
					StopDemuxWildcard(t.demuxFD)
					t.demuxFD = 0
				}
				if t.dvrFD > 0 {
					_ = syscall.Close(t.dvrFD)
					t.dvrFD = 0
				}
				if t.frontendFD <= 0 {
					feFD, err := syscall.Open(t.FrontendPath, syscall.O_RDWR, 0)
					if err != nil {
						t.status = "idle"
						t.activeRecording = nil
						t.mu.Unlock()
						return nil, fmt.Errorf("failed to open frontend device: %w", err)
					}
					t.frontendFD = feFD
				}

				bw := channel.Bandwidth
				if bw == 0 {
					bw = 8000000
				}
				_ = TuneFrontendFD(t.frontendFD, uint32(channel.Frequency), uint32(bw), channel.DeliverySystem)

				dmxFD, _ := StartDemuxWildcard(t.DemuxPath)
				t.demuxFD = dmxFD

				dvrFD, _ := syscall.Open(t.DVRPath, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
				t.dvrFD = dvrFD
				drainBuf := make([]byte, 64*1024)
				for {
					n, _ := syscall.Read(dvrFD, drainBuf)
					if n <= 0 {
						break
					}
				}
			}

			cs, err := p.startChannelStreamLocked(t, channel, 0, -1, 0)
			if err != nil {
				t.stopHardwareLocked()
				t.activeRecording = nil
				t.mu.Unlock()
				return nil, err
			}
			t.channels[streamKey] = cs

			p.startHardwareTSPumpLocked(t)

			t.mu.Unlock()
			log.Printf("[TunerPool] Allocated Tuner %d for Recording #%d on Channel %s",
				t.ID, recInfo.RecordingID, channel.Name)
			return t, nil
		}
		t.mu.Unlock()
	}

	return nil, ErrAllTunersBusy
}

// ReleaseRecording removes the recording lock from a tuner
func (p *Pool) ReleaseRecording(tunerID int, channelID int64, recID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.tuners {
		if t.ID == tunerID {
			t.mu.Lock()
			if t.activeRecording != nil && t.activeRecording.RecordingID == recID {
				t.activeRecording = nil
				log.Printf("[TunerPool] Released recording lock #%d from Tuner %d", recID, tunerID)
			}
			sessionID := fmt.Sprintf("rec_%d", recID)
			for key, cs := range t.channels {
				if cs.ChannelID == channelID {
					cs.Unsubscribe(sessionID)
					if cs.ViewerCount() == 0 {
						cs.Stop()
						delete(t.channels, key)
					}
				}
			}
			if len(t.viewers) == 0 && t.activeRecording == nil && t.status == "active" {
				log.Printf("[TunerPool] Tuner %d has 0 viewers and 0 recordings. Conserving 100%% CPU.", t.ID)
				t.stopHardwareLocked()
			}
			t.mu.Unlock()
			break
		}
	}
}

// ForceReleaseTuner kicks viewers from a tuner and frees it immediately
func (p *Pool) ForceReleaseTuner(tunerID int, requesterIP string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var target *Tuner
	for _, t := range p.tuners {
		if t.ID == tunerID {
			target = t
			break
		}
	}
	if target == nil {
		return ErrTunerNotFound
	}

	target.mu.Lock()
	defer target.mu.Unlock()

	log.Printf("[TunerPool] Force-releasing Tuner %d by requester IP: %s", tunerID, requesterIP)

	// Notify and kick all active viewers
	for id, v := range target.viewers {
		if v.KickChan != nil {
			select {
			case v.KickChan <- fmt.Sprintf("Tuner was reclaimed by %s", requesterIP):
			default:
			}
		}
		delete(target.viewers, id)
	}

	// Cleanly terminate active pipeline, readers, and hardware
	target.stopHardwareLocked()
	return nil
}

// GetTunerForSession returns the active tuner that contains the session
func (p *Pool) GetTunerForSession(sessionID string) *Tuner {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, t := range p.tuners {
		t.mu.Lock()
		if _, exists := t.viewers[sessionID]; exists && t.status == "active" {
			t.mu.Unlock()
			return t
		}
		t.mu.Unlock()
	}
	return nil
}

// ReleaseSession frees a session and triggers power-saving idle teardown if 0 viewers and 0 recordings remain
func (p *Pool) ReleaseSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.tuners {
		t.mu.Lock()
		if _, exists := t.viewers[sessionID]; exists {
			delete(t.viewers, sessionID)
			log.Printf("[TunerPool] Session %s disconnected from Tuner %d. Remaining viewers: %d",
				sessionID, t.ID, len(t.viewers))

			for key, cs := range t.channels {
				cs.Unsubscribe(sessionID)
				if cs.ViewerCount() == 0 {
					cs.Stop()
					delete(t.channels, key)
				}
			}

			if len(t.viewers) == 0 && t.activeRecording == nil && t.status == "active" {
				log.Printf("[TunerPool] Tuner %d has 0 viewers and 0 recordings. Stopping pipeline to conserve 100%% CPU.", t.ID)
				t.stopHardwareLocked()
			}
		}
		t.mu.Unlock()
	}
}

// ReleaseStreamViewer cleans up a specific channel stream subscription
func (p *Pool) ReleaseStreamViewer(tunerID int, channelID int64, sessionID string) {
	p.ReleaseStreamViewerTrack(tunerID, channelID, "", sessionID)
}

// ReleaseStreamViewerTrack cleans up a specific stream track subscription without killing other active tracks
func (p *Pool) ReleaseStreamViewerTrack(tunerID int, channelID int64, streamKey, sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.tuners {
		if t.ID != tunerID {
			continue
		}
		t.mu.Lock()
		if streamKey != "" {
			if cs, ok := t.channels[streamKey]; ok {
				cs.Unsubscribe(sessionID)
				if cs.ViewerCount() == 0 {
					cs.Stop()
					delete(t.channels, streamKey)
				}
			}
		} else {
			for key, cs := range t.channels {
				if cs.ChannelID == channelID {
					cs.Unsubscribe(sessionID)
					if cs.ViewerCount() == 0 {
						cs.Stop()
						delete(t.channels, key)
					}
				}
			}
		}

		if v, exists := t.viewers[sessionID]; exists {
			// If streamKey was provided and viewer has switched to another track on this tuner, keep viewer active!
			if streamKey != "" && v.StreamKey != streamKey {
				log.Printf("[TunerPool] Session %s stream %s ended; viewer already active on %s (Tuner %d)",
					sessionID, streamKey, v.StreamKey, t.ID)
			} else if v.ChannelID == channelID {
				delete(t.viewers, sessionID)
				log.Printf("[TunerPool] Session %s stream ended on Tuner %d (Channel %d). Remaining viewers: %d",
					sessionID, t.ID, channelID, len(t.viewers))
				if len(t.viewers) == 0 && t.activeRecording == nil && t.status == "active" {
					log.Printf("[TunerPool] Tuner %d has 0 viewers and 0 recordings. Stopping pipeline to conserve 100%% CPU.", t.ID)
					t.stopHardwareLocked()
				}
			} else {
				log.Printf("[TunerPool] Session %s already switched to channel %d on Tuner %d; keeping session active.",
					sessionID, v.ChannelID, t.ID)
			}
		}
		t.mu.Unlock()
	}
}

var (
	tracksCacheMu sync.RWMutex
	tracksCache   = make(map[int64]ChannelTracks)
)

func getLanguageName(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	switch code {
	case "ell", "gre":
		return "Greek"
	case "eng":
		return "English"
	case "fra", "fre":
		return "French"
	case "deu", "ger":
		return "German"
	case "ita":
		return "Italian"
	case "spa", "esl":
		return "Spanish"
	case "por":
		return "Portuguese"
	case "rus":
		return "Russian"
	case "tur":
		return "Turkish"
	case "ara":
		return "Arabic"
	case "bul":
		return "Bulgarian"
	case "srp", "scc":
		return "Serbian"
	case "qaa", "mul", "und":
		return ""
	default:
		if len(code) == 3 {
			return strings.ToUpper(code)
		}
		return ""
	}
}

func formatDetectedTitle(typeStr string, idx int, lang string) string {
	langName := getLanguageName(lang)
	if typeStr == "Audio" {
		if langName != "" {
			return fmt.Sprintf("%s (Track %d)", langName, idx+1)
		}
		if lang != "und" && lang != "" {
			return fmt.Sprintf("Audio %d (%s)", idx+1, strings.ToUpper(lang))
		}
		return fmt.Sprintf("Stereo Audio %d", idx+1)
	}

	if langName != "" {
		return fmt.Sprintf("%s (%s)", langName, typeStr)
	}
	if lang != "und" && lang != "" {
		return fmt.Sprintf("%s [%s]", typeStr, strings.ToUpper(lang))
	}
	return fmt.Sprintf("%s (Track %d)", typeStr, idx+1)
}

// FeedPMTChunk parses live PAT & PMT packets to detect elementary streams in the multiplex
func (t *Tuner) FeedPMTChunk(chunk []byte, channelMap map[int]int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pmtPIDs == nil {
		t.pmtPIDs = make(map[int]int)
	}
	if t.detectedTracks == nil {
		t.detectedTracks = make(map[int]ChannelTracks)
	}

	for i := 0; i+188 <= len(chunk); i += 188 {
		pkt := chunk[i : i+188]
		if pkt[0] != 0x47 {
			continue
		}

		pid := (int(pkt[1]&0x1F) << 8) | int(pkt[2])
		payloadUnitStart := (pkt[1] & 0x40) != 0

		adaptation := (pkt[3] >> 4) & 0x03
		if adaptation == 2 {
			continue
		}
		payload := pkt[4:]
		if adaptation == 3 {
			if len(payload) == 0 {
				continue
			}
			adaptLen := int(payload[0])
			if adaptLen+1 < len(payload) {
				payload = payload[adaptLen+1:]
			} else {
				continue
			}
		}

		if payloadUnitStart && len(payload) > 0 {
			pointerField := int(payload[0])
			if pointerField+1 < len(payload) {
				payload = payload[pointerField+1:]
			} else {
				continue
			}
		}

		if len(payload) < 3 {
			continue
		}

		// 1. PAT (PID 0, Table ID 0x00)
		if pid == 0 && payload[0] == 0x00 {
			sectionLen := (int(payload[1]&0x0F) << 8) | int(payload[2])
			if len(payload) < 3+sectionLen || sectionLen < 9 {
				continue
			}
			entriesEnd := 3 + sectionLen - 4
			for p := 8; p+4 <= entriesEnd && p+4 <= len(payload); p += 4 {
				progNum := (int(payload[p]) << 8) | int(payload[p+1])
				pmtPID := (int(payload[p+2]&0x1F) << 8) | int(payload[p+3])
				if progNum != 0 {
					t.pmtPIDs[pmtPID] = progNum
				}
			}
			continue
		}

		// 2. PMT (PID matched from PAT, Table ID 0x02)
		if progNum, isPMT := t.pmtPIDs[pid]; isPMT && payload[0] == 0x02 {
			sectionLen := (int(payload[1]&0x0F) << 8) | int(payload[2])
			if len(payload) < 3+sectionLen || sectionLen < 13 {
				continue
			}
			actualProg := (int(payload[3]) << 8) | int(payload[4])
			if actualProg != 0 {
				progNum = actualProg
			}
			progInfoLen := (int(payload[10]&0x0F) << 8) | int(payload[11])
			esStart := 12 + progInfoLen
			esEnd := 3 + sectionLen - 4
			if esStart > esEnd || esEnd > len(payload) {
				continue
			}

			var audioTracks []TrackInfo
			var subTracks []TrackInfo
			hasTeletext := false
			aIdx := 0
			sIdx := 0

			pos := esStart
			for pos+5 <= esEnd {
				streamType := payload[pos]
				esInfoLen := (int(payload[pos+3]&0x0F) << 8) | int(payload[pos+4])
				pos += 5
				if pos+esInfoLen > esEnd {
					break
				}
				descData := payload[pos : pos+esInfoLen]
				pos += esInfoLen

				var lang string
				isAudio := false
				isSub := false
				subDesc := "DVB Subtitles"

				switch streamType {
				case 0x03, 0x04:
					isAudio = true
				case 0x0F, 0x11:
					isAudio = true
				}

				dPos := 0
				for dPos+2 <= len(descData) {
					tag := descData[dPos]
					dLen := int(descData[dPos+1])
					dPos += 2
					if dPos+dLen > len(descData) {
						break
					}
					dBytes := descData[dPos : dPos+dLen]
					dPos += dLen

					switch tag {
					case 0x0A: // ISO 639
						if len(dBytes) >= 3 {
							lang = strings.ToLower(string(dBytes[0:3]))
						}
						isAudio = true
					case 0x6A, 0x7A, 0x7F: // AC-3, E-AC-3
						isAudio = true
					case 0x59: // DVB subtitling
						isSub = true
						if len(dBytes) >= 3 && lang == "" {
							lang = strings.ToLower(string(dBytes[0:3]))
						}
						subDesc = "DVB Subtitles"
					case 0x56: // Teletext
						hasTeletext = true
						for tIdx := 0; tIdx+5 <= len(dBytes); tIdx += 5 {
							ttype := dBytes[tIdx+3] >> 3
							if ttype == 0x02 || ttype == 0x05 {
								isSub = true
								if lang == "" {
									lang = strings.ToLower(string(dBytes[tIdx : tIdx+3]))
								}
								subDesc = "Teletext Subtitles"
							}
						}
					}
				}

				if lang == "" {
					lang = "und"
				}

				if isAudio {
					title := formatDetectedTitle("Audio", aIdx, lang)
					audioTracks = append(audioTracks, TrackInfo{
						Index:    aIdx,
						Type:     "audio",
						Codec:    fmt.Sprintf("0x%02x", streamType),
						Language: lang,
						Title:    title,
					})
					aIdx++
				} else if isSub {
					title := formatDetectedTitle(subDesc, sIdx, lang)
					subTracks = append(subTracks, TrackInfo{
						Index:    sIdx,
						Type:     "subtitle",
						Codec:    "dvb_subtitle",
						Language: lang,
						Title:    title,
					})
					sIdx++
				}
			}

			if len(audioTracks) > 0 || len(subTracks) > 0 {
				dt := ChannelTracks{
					AudioTracks:    audioTracks,
					SubtitleTracks: subTracks,
					HasTeletext:    hasTeletext,
				}
				t.detectedTracks[progNum] = dt
				if channelMap != nil {
					if cid, ok := channelMap[progNum]; ok {
						tracksCacheMu.Lock()
						tracksCache[cid] = dt
						tracksCacheMu.Unlock()
					}
				}
			}
		}
	}
}

type ffprobeJSON struct {
	Streams []struct {
		Index     int               `json:"index"`
		CodecType string            `json:"codec_type"`
		CodecName string            `json:"codec_name"`
		Tags      map[string]string `json:"tags"`
	} `json:"streams"`
}

func parseProbeOutput(data []byte, serviceID int) ChannelTracks {
	var parsed ffprobeJSON
	_ = json.Unmarshal(data, &parsed)

	var audioList []TrackInfo
	var subList []TrackInfo
	aIdx := 0
	sIdx := 0
	hasTeletext := false

	for _, s := range parsed.Streams {
		lang := s.Tags["language"]
		if lang == "" {
			lang = "und"
		}
		title := s.Tags["title"]
		if s.CodecType == "audio" {
			if title == "" {
				title = fmt.Sprintf("Audio %d (%s)", aIdx+1, strings.ToUpper(lang))
			}
			audioList = append(audioList, TrackInfo{
				Index:    aIdx,
				Type:     "audio",
				Codec:    s.CodecName,
				Language: lang,
				Title:    title,
			})
			aIdx++
		} else if s.CodecType == "subtitle" || s.CodecType == "data" {
			codecLower := strings.ToLower(s.CodecName)
			titleLower := strings.ToLower(title)
			if strings.Contains(codecLower, "teletext") || strings.Contains(titleLower, "teletext") || strings.Contains(codecLower, "txt") {
				hasTeletext = true
			}
			if s.CodecType == "subtitle" {
				if title == "" {
					title = fmt.Sprintf("Subtitle %d (%s)", sIdx+1, strings.ToUpper(lang))
				}
				subList = append(subList, TrackInfo{
					Index:    sIdx,
					Type:     "subtitle",
					Codec:    s.CodecName,
					Language: lang,
					Title:    title,
				})
				sIdx++
			}
		}
	}

	if len(audioList) == 0 {
		audioList = append(audioList, TrackInfo{
			Index:    0,
			Type:     "audio",
			Codec:    "aac",
			Language: "und",
			Title:    "Stereo (Default)",
		})
	}

	return ChannelTracks{
		AudioTracks:    audioList,
		SubtitleTracks: subList,
		HasTeletext:    hasTeletext,
	}
}

func (p *Pool) getChannelTracksLocked(channel *db.Channel) ChannelTracks {
	tracksCacheMu.RLock()
	cached, ok := tracksCache[channel.ID]
	tracksCacheMu.RUnlock()
	if ok && len(cached.AudioTracks) > 0 {
		return cached
	}

	hbbtv := channel.HbbtvURL

	// 1. Check if any tuner active on this frequency has detected tracks for this serviceID via PMT
	for _, t := range p.tuners {
		t.mu.Lock()
		if t.status == "active" && t.currentFrequency == channel.Frequency && t.detectedTracks != nil {
			if det, ok := t.detectedTracks[channel.ServiceID]; ok && len(det.AudioTracks) > 0 {
				det.HbbtvURL = hbbtv
				t.mu.Unlock()

				if channel.HasTeletext != det.HasTeletext {
					channel.HasTeletext = det.HasTeletext
					_ = p.db.UpdateChannelTeletext(channel.ID, det.HasTeletext)
				}

				tracksCacheMu.Lock()
				tracksCache[channel.ID] = det
				tracksCacheMu.Unlock()
				return det
			}
		}
		t.mu.Unlock()
	}

	if p.isMockMode {
		subList := []TrackInfo{
			{Index: 0, Type: "subtitle", Codec: "dvb_subtitle", Language: "eng", Title: "English [CC]"},
		}
		if channel.IsRadio {
			subList = []TrackInfo{}
		}
		tracks := ChannelTracks{
			AudioTracks: []TrackInfo{
				{Index: 0, Type: "audio", Codec: "aac", Language: "eng", Title: "English (Stereo)"},
				{Index: 1, Type: "audio", Codec: "aac", Language: "qaa", Title: "Original Audio (Secondary)"},
			},
			SubtitleTracks: subList,
			HasTeletext:    false,
			HbbtvURL:       hbbtv,
		}
		tracksCacheMu.Lock()
		tracksCache[channel.ID] = tracks
		tracksCacheMu.Unlock()
		return tracks
	}

	// Safe fallback: never invent fake subtitle tracks
	return ChannelTracks{
		AudioTracks: []TrackInfo{
			{Index: 0, Type: "audio", Codec: "aac", Language: "und", Title: "Stereo (Default)"},
		},
		SubtitleTracks: []TrackInfo{},
		HasTeletext:    channel.HasTeletext,
		HbbtvURL:       hbbtv,
	}
}

// GetChannelTracks discovers or returns cached audio and subtitle tracks for a channel
func (p *Pool) GetChannelTracks(channel *db.Channel) ChannelTracks {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.getChannelTracksLocked(channel)
}

func (t *Tuner) Ports() (int, int) {
	return t.videoUDPPort, t.audioUDPPort
}

func (t *Tuner) GetSignal() *SignalInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status != "active" {
		return nil
	}
	if t.IsMock {
		return &SignalInfo{
			HasLock:        true,
			HasSignal:      true,
			SignalStrength: 95,
			SignalQuality:  98,
			SNR:            26.5,
			BER:            0,
		}
	}
	if t.frontendFD > 0 {
		sig := ReadFrontendSignalInfo(t.frontendFD)
		return &sig
	}
	return nil
}

// ScanFrequencies performs a channel scan on the specified adapter
func (p *Pool) ScanFrequencies(adapterID int, onProgress func(freq int64, progressPct int, foundCount int, recentChannels []string)) ([]db.Channel, error) {
	p.mu.Lock()
	var target *Tuner
	for _, t := range p.tuners {
		if t.ID == adapterID {
			target = t
			break
		}
	}
	if target == nil {
		p.mu.Unlock()
		return nil, ErrTunerNotFound
	}

	target.mu.Lock()
	if len(target.viewers) > 0 {
		target.mu.Unlock()
		p.mu.Unlock()
		return nil, errors.New("cannot scan: tuner is currently in use by viewers")
	}

	target.status = "scanning"
	target.mu.Unlock()
	p.mu.Unlock()

	defer func() {
		target.mu.Lock()
		target.status = "idle"
		target.mu.Unlock()
	}()

	// If mock mode, return standard simulated digital broadcast channels
	if target.IsMock {
		time.Sleep(500 * time.Millisecond) // simulate scan duration
		mockChs := []db.Channel{
			{Name: "BBC One HD", Number: 1, Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 101, VideoPID: 1011, AudioPID: 1012, Enabled: true},
			{Name: "BBC Two HD", Number: 2, Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 102, VideoPID: 1021, AudioPID: 1022, Enabled: true},
			{Name: "ITV1 HD", Number: 3, Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 201, VideoPID: 2011, AudioPID: 2012, Enabled: true},
			{Name: "Channel 4 HD", Number: 4, Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 202, VideoPID: 2021, AudioPID: 2022, Enabled: true},
			{Name: "Channel 5", Number: 5, Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT", ServiceID: 301, VideoPID: 3011, AudioPID: 3012, Enabled: true},
			{Name: "Sky News", Number: 11, Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT", ServiceID: 302, VideoPID: 3021, AudioPID: 3022, Enabled: true},
			{Name: "BBC Radio 1", Number: 701, Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 701, VideoPID: 0, AudioPID: 7012, Enabled: true, IsRadio: true},
			{Name: "BBC Radio 4", Number: 704, Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 704, VideoPID: 0, AudioPID: 7042, Enabled: true, IsRadio: true},
		}
		if onProgress != nil {
			var names []string
			for _, ch := range mockChs {
				names = append(names, ch.Name)
			}
			onProgress(506000000, 100, len(mockChs), names)
		}
		return mockChs, nil
	}

	// Real hardware scan using native Linux DVB API ioctl
	return ScanFrequenciesNative(target.FrontendPath, target.DVRPath, target.DemuxPath, onProgress)
}

// UDPListener creates a listener for receiving RTP packets from FFmpeg
func ListenUDP(port int) (*net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	return net.ListenUDP("udp", addr)
}
