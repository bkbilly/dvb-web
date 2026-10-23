package tuner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
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
	ch := make(chan []byte, 128)
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

func getStreamKey(channelID int64, audioTrack, subTrack int) string {
	if audioTrack < 0 {
		audioTrack = 0
	}
	return fmt.Sprintf("%d_a%d_s%d", channelID, audioTrack, subTrack)
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
					ID:           id,
					Name:         fmt.Sprintf("DVB Adapter %d", id),
					FrontendPath: filepath.Join(m, "frontend0"),
					DVRPath:      filepath.Join(m, "dvr0"),
					DemuxPath:    filepath.Join(m, "demux0"),
					status:       "idle",
					viewers:      make(map[string]*ViewerSession),
					channels:     make(map[string]*ChannelStream),
					videoUDPPort: portCounter,
					audioUDPPort: portCounter + 2,
					epgParser:    p.epgParser,
					Enabled:      true,
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
}

func (t *Tuner) Subscribe(channelID int64, sessionID string) chan []byte {
	return t.SubscribeTrack(channelID, 0, -1, sessionID)
}

func (t *Tuner) SubscribeTrack(channelID int64, audioTrack, subTrack int, sessionID string) chan []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.channels == nil {
		return nil
	}
	key := getStreamKey(channelID, audioTrack, subTrack)
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

func (p *Pool) startChannelStreamLocked(t *Tuner, channel *db.Channel, audioTrack, subTrack int) (*ChannelStream, error) {
	var inputPipe io.Reader
	var pw *io.PipeWriter

	if !t.IsMock {
		pr, pWriter := io.Pipe()
		inputPipe = pr
		pw = pWriter
	}

	cfg := transcoder.PipelineConfig{
		InputPath:     t.DVRPath,
		InputPipe:     inputPipe,
		IsMock:        t.IsMock,
		IsRadio:       channel.IsRadio,
		ServiceID:     channel.ServiceID,
		VideoUDPPort:  t.videoUDPPort,
		AudioUDPPort:  t.audioUDPPort,
		AudioTrackIdx: audioTrack,
		SubtitleTrack: subTrack,
		HWType:        transcoder.HardwareType(p.db.GetSetting("transcoder_mode", "auto")),
		VideoBitrate:  p.db.GetSetting("video_bitrate", "2500k"),
	}

	pipeline, err := p.transcoder.StartPipeline(cfg)
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
		cs.tsQueue = make(chan []byte, 128)
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

func (p *Pool) startHardwareTSPumpLocked(t *Tuner) {
	if t.IsMock || t.dvrFD <= 0 {
		return
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
	tsQueue := make(chan []byte, 128)

	// Hardware DVR reader goroutine
	go func() {
		defer close(tsQueue)
		buf := make([]byte, 188*128) // 24KB chunk
		for {
			select {
			case <-tsCtx.Done():
				return
			default:
			}

			n, err := syscall.Read(dvrFD, buf)
			if err != nil {
				if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				return
			}
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case tsQueue <- chunk:
				default:
				}
				if ep != nil {
					ep.FeedTSChunk(buf[:n], channelMap)
				}
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
	return p.AllocateChannelTrack(channel, 0, -1, session)
}

// AllocateChannelTrack matches or allocates a tuner with specific audio & subtitle tracks
func (p *Pool) AllocateChannelTrack(channel *db.Channel, audioTrack, subTrack int, session *ViewerSession) (*Tuner, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	streamKey := getStreamKey(channel.ID, audioTrack, subTrack)

	// 1. Check if a tuner is ALREADY active on this exact frequency (channel group / multiplex)!
	for _, t := range p.tuners {
		t.mu.Lock()
		if t.status == "active" && t.currentFrequency == channel.Frequency {
			session.ChannelID = channel.ID
			session.ChannelName = channel.Name
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
				cs, err := p.startChannelStreamLocked(t, channel, audioTrack, subTrack)
				if err != nil {
					delete(t.viewers, session.ID)
					t.mu.Unlock()
					return nil, err
				}
				t.channels[streamKey] = cs
			}

			t.currentChannelID = channel.ID
			t.currentChannel = channel.Name

			t.mu.Unlock()
			log.Printf("[TunerPool] Session %s (IP: %s) joined Tuner %d multiplex (%d Hz, Channel: %s, Stream: %s)",
				session.ID, session.IP, t.ID, channel.Frequency, channel.Name, streamKey)
			return t, nil
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
			session.ConnectedAt = time.Now()

			t.status = "active"
			t.currentFrequency = channel.Frequency
			t.currentChannelID = channel.ID
			t.currentChannel = channel.Name
			t.viewers[session.ID] = session
			t.channels = make(map[string]*ChannelStream)

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
						return nil, fmt.Errorf("failed to open frontend device %s: %w", t.FrontendPath, err)
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
			cs, err := p.startChannelStreamLocked(t, channel, audioTrack, subTrack)
			if err != nil {
				t.stopHardwareLocked()
				delete(t.viewers, session.ID)
				t.mu.Unlock()
				return nil, fmt.Errorf("failed to start stream pipeline: %w", err)
			}
			t.channels[streamKey] = cs

			// Start multiplex TS pump for real DVB hardware
			p.startHardwareTSPumpLocked(t)

			t.mu.Unlock()
			log.Printf("[TunerPool] Allocated Tuner %d for Session %s (IP: %s) on Channel %s (%s)",
				t.ID, session.ID, session.IP, channel.Name, streamKey)
			return t, nil
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

	return nil, &TunerConflictError{
		ActiveTuners:       activeStatuses,
		HasActiveRecording: hasActiveRecording,
		ActiveRecordings:   activeRecs,
	}
}

// AllocateRecording allocates a tuner for an active recording session
func (p *Pool) AllocateRecording(channel *db.Channel, recInfo *ActiveRecordingInfo) (*Tuner, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	streamKey := getStreamKey(channel.ID, 0, -1)

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
				cs, err := p.startChannelStreamLocked(t, channel, 0, -1)
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

			cs, err := p.startChannelStreamLocked(t, channel, 0, -1)
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
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.tuners {
		if t.ID != tunerID {
			continue
		}
		t.mu.Lock()
		for key, cs := range t.channels {
			if cs.ChannelID == channelID {
				cs.Unsubscribe(sessionID)
				if cs.ViewerCount() == 0 {
					cs.Stop()
					delete(t.channels, key)
				}
			}
		}

		if v, exists := t.viewers[sessionID]; exists {
			if v.ChannelID == channelID {
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
		} else if s.CodecType == "subtitle" {
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
	}
}

// GetChannelTracks discovers or returns cached audio and subtitle tracks for a channel
func (p *Pool) GetChannelTracks(channel *db.Channel) ChannelTracks {
	tracksCacheMu.RLock()
	cached, ok := tracksCache[channel.ID]
	tracksCacheMu.RUnlock()
	if ok {
		return cached
	}

	tracks := ChannelTracks{
		AudioTracks: []TrackInfo{
			{Index: 0, Type: "audio", Codec: "aac", Language: "und", Title: "Stereo (Default)"},
		},
		SubtitleTracks: []TrackInfo{},
	}

	if p.isMockMode {
		subList := []TrackInfo{
			{Index: 0, Type: "subtitle", Codec: "dvb_subtitle", Language: "eng", Title: "English [CC]"},
		}
		if channel.IsRadio {
			subList = []TrackInfo{}
		}
		tracks = ChannelTracks{
			AudioTracks: []TrackInfo{
				{Index: 0, Type: "audio", Codec: "aac", Language: "eng", Title: "English (Stereo)"},
				{Index: 1, Type: "audio", Codec: "aac", Language: "qaa", Title: "Original Audio (Secondary)"},
			},
			SubtitleTracks: subList,
		}
		tracksCacheMu.Lock()
		tracksCache[channel.ID] = tracks
		tracksCacheMu.Unlock()
		return tracks
	}

	// For physical broadcast: check if a tuner is active on this frequency to probe live streams
	ffprobePath, err := exec.LookPath("ffprobe")
	if err == nil {
		p.mu.RLock()
		var activeDVR string
		for _, t := range p.tuners {
			if t.status == "active" && t.currentFrequency == channel.Frequency && t.dvrFD > 0 {
				activeDVR = t.DVRPath
				break
			}
		}
		p.mu.RUnlock()

		if activeDVR != "" {
			cmd := exec.Command(ffprobePath,
				"-v", "quiet",
				"-probesize", "1000000",
				"-analyzeduration", "1000000",
				"-show_streams",
				"-of", "json",
				activeDVR,
			)
			out, err := cmd.Output()
			if err == nil {
				parsed := parseProbeOutput(out, channel.ServiceID)
				if len(parsed.AudioTracks) > 0 {
					tracks = parsed
					tracksCacheMu.Lock()
					tracksCache[channel.ID] = tracks
					tracksCacheMu.Unlock()
					return tracks
				}
			}
		}
	}

	return tracks
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
func (p *Pool) ScanFrequencies(adapterID int, onProgress func(freq int64, progressPct int, foundCount int)) ([]db.Channel, error) {
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
		time.Sleep(1500 * time.Millisecond) // simulate scan duration
		return []db.Channel{
			{Name: "BBC One HD", Number: 1, Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 101, VideoPID: 1011, AudioPID: 1012, Enabled: true},
			{Name: "BBC Two HD", Number: 2, Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 102, VideoPID: 1021, AudioPID: 1022, Enabled: true},
			{Name: "ITV1 HD", Number: 3, Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 201, VideoPID: 2011, AudioPID: 2012, Enabled: true},
			{Name: "Channel 4 HD", Number: 4, Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 202, VideoPID: 2021, AudioPID: 2022, Enabled: true},
			{Name: "Channel 5", Number: 5, Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT", ServiceID: 301, VideoPID: 3011, AudioPID: 3012, Enabled: true},
			{Name: "Sky News", Number: 11, Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT", ServiceID: 302, VideoPID: 3021, AudioPID: 3022, Enabled: true},
			{Name: "BBC Radio 1", Number: 701, Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 701, VideoPID: 0, AudioPID: 7012, Enabled: true, IsRadio: true},
			{Name: "BBC Radio 4", Number: 704, Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", ServiceID: 704, VideoPID: 0, AudioPID: 7042, Enabled: true, IsRadio: true},
		}, nil
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
