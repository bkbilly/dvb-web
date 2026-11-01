package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"dvb-web/internal/db"
	"dvb-web/internal/tuner"
)

var (
	ErrStreamNotReady = errors.New("hls stream buffering initial segments")
	ErrSegmentNotFound = errors.New("hls segment not found")
)

type Segment struct {
	Seq      int
	Duration float64
	Data     []byte
}

type ChannelHLSStream struct {
	channelID   int64
	mu          sync.RWMutex
	segments    []*Segment
	nextSeq     int
	lastAccess  time.Time
	subSession  string
	tunerID     int
	streamKey   string
	cancel      context.CancelFunc
	readyCond   *sync.Cond
}

type TunerPoolProvider interface {
	AllocateChannelTrack(channel *db.Channel, audioTrack, subTrack, subOffset int, session *tuner.ViewerSession) (*tuner.Tuner, int, error)
	ReleaseStreamViewerTrack(tunerID int, channelID int64, streamKey, sessionID string)
	GetStatus() []tuner.TunerStatus
}

type DBProvider interface {
	GetChannel(id int64) (*db.Channel, error)
}

type Manager struct {
	mu        sync.Mutex
	tunerPool TunerPoolProvider
	db        DBProvider
	streams   map[int64]*ChannelHLSStream
	stopChan  chan struct{}
}

func NewManager(tunerPool TunerPoolProvider, database DBProvider) *Manager {
	m := &Manager{
		tunerPool: tunerPool,
		db:        database,
		streams:   make(map[int64]*ChannelHLSStream),
		stopChan:  make(chan struct{}),
	}
	go m.reaperLoop()
	return m
}

func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.stopChan:
		return
	default:
		close(m.stopChan)
	}

	for _, s := range m.streams {
		s.stopLocked(m.tunerPool)
	}
	m.streams = make(map[int64]*ChannelHLSStream)
}

func (m *Manager) reaperLoop() {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.mu.Lock()
			now := time.Now()
			for id, s := range m.streams {
				s.mu.RLock()
				inactive := now.Sub(s.lastAccess) > 12*time.Second
				s.mu.RUnlock()

				if inactive {
					log.Printf("[HLS] Stream for channel %d timed out (inactive >12s). Cleaning up...", id)
					s.stopLocked(m.tunerPool)
					delete(m.streams, id)
				}
			}
			m.mu.Unlock()
		}
	}
}

func (s *ChannelHLSStream) stopLocked(pool TunerPoolProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if pool != nil && s.subSession != "" {
		pool.ReleaseStreamViewerTrack(s.tunerID, s.channelID, s.streamKey, s.subSession)
	}
	s.segments = nil
	s.readyCond.Broadcast()
}

func (m *Manager) GetOrCreateStream(channelID int64) (*ChannelHLSStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, exists := m.streams[channelID]; exists {
		s.mu.Lock()
		s.lastAccess = time.Now()
		s.mu.Unlock()
		return s, nil
	}

	channel, err := m.db.GetChannel(channelID)
	if err != nil {
		return nil, err
	}

	subSession := fmt.Sprintf("hls_%d_%d", channelID, time.Now().UnixNano())
	session := &tuner.ViewerSession{
		ID:          subSession,
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		IP:          "127.0.0.1",
		UserAgent:   "HLS-Bridge",
		ConnectedAt: time.Now(),
	}

	t, actualSubTrack, err := m.tunerPool.AllocateChannelTrack(channel, 0, -1, 0, session)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate tuner for HLS: %w", err)
	}

	ch := t.SubscribeTrack(channel.ID, 0, actualSubTrack, 0, subSession)
	if ch == nil {
		m.tunerPool.ReleaseStreamViewerTrack(t.ID, channel.ID, tuner.GetStreamKey(channel.ID, 0, actualSubTrack, 0), subSession)
		return nil, errors.New("failed to subscribe to channel stream for HLS")
	}

	ctx, cancel := context.WithCancel(context.Background())
	streamKey := tuner.GetStreamKey(channel.ID, 0, actualSubTrack, 0)

	st := &ChannelHLSStream{
		channelID:  channelID,
		nextSeq:    1,
		lastAccess: time.Now(),
		subSession: subSession,
		tunerID:    t.ID,
		streamKey:  streamKey,
		cancel:     cancel,
	}
	st.readyCond = sync.NewCond(&st.mu)

	m.streams[channelID] = st

	// Start background TS segmenter
	go st.runSegmenter(ctx, ch)

	return st, nil
}

func (s *ChannelHLSStream) runSegmenter(ctx context.Context, ch chan []byte) {
	var currentSeg bytes.Buffer
	var lastPatPmt bytes.Buffer
	segStartTime := time.Now()
	var leftover []byte

	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if len(chunk) == 0 {
				continue
			}

			data := chunk
			if len(leftover) > 0 {
				data = append(leftover, data...)
				leftover = nil
			}

			// Process 188-byte MPEG-TS packets
			for len(data) >= 188 {
				if data[0] != 0x47 {
					// Search for next sync byte
					idx := bytes.IndexByte(data, 0x47)
					if idx < 0 {
						data = nil
						break
					}
					data = data[idx:]
					if len(data) < 188 {
						break
					}
				}

				pkt := data[:188]
				data = data[188:]

				pid := (uint16(pkt[1]&0x1F) << 8) | uint16(pkt[2])

				// Cache PAT (PID 0) and PMT (PID != 0x1FFF & not audio/video) so every segment can start self-contained
				if pid == 0 {
					lastPatPmt.Reset()
					lastPatPmt.Write(pkt)
				} else if pid >= 0x1000 && pid < 0x1FFF && (pkt[1]&0x40 != 0) && lastPatPmt.Len() == 188 {
					lastPatPmt.Write(pkt)
				}

				// Check if this packet starts a video keyframe
				isKey := isVideoKeyframe(pkt)
				segDur := time.Since(segStartTime).Seconds()

				// If we have >= 1.8 seconds of video and hit a keyframe, roll over into a new HLS segment!
				if isKey && segDur >= 1.8 && currentSeg.Len() > 0 {
					segData := make([]byte, currentSeg.Len())
					copy(segData, currentSeg.Bytes())

					s.mu.Lock()
					newSeg := &Segment{
						Seq:      s.nextSeq,
						Duration: segDur,
						Data:     segData,
					}
					s.segments = append(s.segments, newSeg)
					s.nextSeq++

					// Keep the last 5 segments in the sliding window
					if len(s.segments) > 5 {
						s.segments = s.segments[len(s.segments)-5:]
					}
					s.readyCond.Broadcast()
					s.mu.Unlock()

					// Reset for next segment
					currentSeg.Reset()
					segStartTime = time.Now()

					// Prepend cached PAT/PMT to the new segment so media players can initialize cleanly without waiting
					if lastPatPmt.Len() > 0 {
						currentSeg.Write(lastPatPmt.Bytes())
					}
				}

				currentSeg.Write(pkt)
			}

			if len(data) > 0 {
				leftover = data
			}
		}
	}
}

// isVideoKeyframe checks whether an MPEG-TS packet begins a new video access unit with IDR/SPS
func isVideoKeyframe(pkt []byte) bool {
	if len(pkt) < 188 || pkt[0] != 0x47 {
		return false
	}
	// PUSI flag: bit 6 of byte 1 must be set
	if (pkt[1] & 0x40) == 0 {
		return false
	}

	payloadOffset := 4
	adaptation := (pkt[3] >> 4) & 0x03
	if adaptation == 2 {
		return false // adaptation field only
	}
	if adaptation == 3 {
		adaptLen := int(pkt[4])
		payloadOffset = 5 + adaptLen
	}
	if payloadOffset >= 188 {
		return false
	}

	payload := pkt[payloadOffset:]
	// PES start code prefix: 0x00 0x00 0x01
	if len(payload) >= 9 && payload[0] == 0x00 && payload[1] == 0x00 && payload[2] == 0x01 {
		streamID := payload[3]
		// Video streams in MPEG-TS are 0xE0..0xEF
		if streamID >= 0xE0 && streamID <= 0xEF {
			headerLen := int(payload[8])
			esOffset := 9 + headerLen
			if esOffset < len(payload) {
				es := payload[esOffset:]
				// Check for H.264 NAL header
				for i := 0; i < len(es)-4; i++ {
					if es[i] == 0 && es[i+1] == 0 {
						var nalType byte
						if es[i+2] == 1 {
							nalType = es[i+3] & 0x1F
						} else if es[i+2] == 0 && es[i+3] == 1 && i+4 < len(es) {
							nalType = es[i+4] & 0x1F
						}
						// NAL type 5 is IDR keyframe; type 7 is Sequence Parameter Set (SPS)
						if nalType == 5 || nalType == 7 {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func (m *Manager) GetPlaylist(channelID int64) (string, error) {
	st, err := m.GetOrCreateStream(channelID)
	if err != nil {
		return "", err
	}

	st.mu.Lock()
	st.lastAccess = time.Now()

	// If no segments are ready yet, wait up to 2.5s for the first segment
	if len(st.segments) == 0 {
		timer := time.AfterFunc(2500*time.Millisecond, func() {
			st.readyCond.Broadcast()
		})
		st.readyCond.Wait()
		timer.Stop()
	}

	segments := make([]*Segment, len(st.segments))
	copy(segments, st.segments)
	st.mu.Unlock()

	if len(segments) == 0 {
		return "", ErrStreamNotReady
	}

	targetDur := 2
	for _, seg := range segments {
		if int(seg.Duration)+1 > targetDur {
			targetDur = int(seg.Duration) + 1
		}
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	buf.WriteString("#EXT-X-VERSION:3\n")
	buf.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", targetDur))
	buf.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", segments[0].Seq))

	for _, seg := range segments {
		buf.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", seg.Duration))
		buf.WriteString(fmt.Sprintf("segment_%d.ts\n", seg.Seq))
	}

	return buf.String(), nil
}

func (m *Manager) GetSegment(channelID int64, seq int) ([]byte, error) {
	m.mu.Lock()
	st, exists := m.streams[channelID]
	m.mu.Unlock()

	if !exists {
		return nil, ErrSegmentNotFound
	}

	st.mu.Lock()
	st.lastAccess = time.Now()
	defer st.mu.Unlock()

	for _, seg := range st.segments {
		if seg.Seq == seq {
			return seg.Data, nil
		}
	}

	return nil, ErrSegmentNotFound
}
