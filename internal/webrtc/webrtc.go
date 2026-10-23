package webrtc

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

type Session struct {
	ID         string
	PC         *webrtc.PeerConnection
	VideoTrack *webrtc.TrackLocalStaticRTP
	AudioTrack *webrtc.TrackLocalStaticRTP
	cancelLoop context.CancelFunc
	mu         sync.Mutex
}

type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	api      *webrtc.API
}

func NewManager() *Manager {
	m := &webrtc.MediaEngine{}

	// Register H.264 Video Codec
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: nil,
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		log.Printf("[WebRTC] Warning: failed to register H264 codec: %v", err)
	}

	// Register Opus Audio Codec
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeOpus,
			ClockRate:    48000,
			Channels:     2,
			SDPFmtpLine:  "minptime=10;useinbandfec=1",
			RTCPFeedback: nil,
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		log.Printf("[WebRTC] Warning: failed to register Opus codec: %v", err)
	}

	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		log.Printf("[WebRTC] Warning: failed to register default interceptors: %v", err)
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir))

	return &Manager{
		sessions: make(map[string]*Session),
		api:      api,
	}
}

// CreateSession sets up a WebRTC PeerConnection for a client
func (m *Manager) CreateSession(sessionID string) (*Session, error) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := m.api.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create PeerConnection: %w", err)
	}

	// Create local video track for H.264 RTP
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video",
		"dvb-stream",
	)
	if err != nil {
		pc.Close()
		return nil, err
	}

	// Create local audio track for Opus RTP (shares the same stream ID so browser receives audio & video in one MediaStream)
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio",
		"dvb-stream",
	)
	if err != nil {
		pc.Close()
		return nil, err
	}

	if _, err = pc.AddTrack(videoTrack); err != nil {
		pc.Close()
		return nil, err
	}
	if _, err = pc.AddTrack(audioTrack); err != nil {
		pc.Close()
		return nil, err
	}

	s := &Session{
		ID:         sessionID,
		PC:         pc,
		VideoTrack: videoTrack,
		AudioTrack: audioTrack,
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[WebRTC] Session %s connection state: %s", sessionID, state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			m.RemoveSession(sessionID)
		}
	})

	m.mu.Lock()
	m.sessions[sessionID] = s
	m.mu.Unlock()

	return s, nil
}

// StartRTPForwarding streams RTP packets from local UDP ports into the WebRTC session tracks
func (s *Session) StartRTPForwarding(videoPort, audioPort int) {
	s.mu.Lock()
	if s.cancelLoop != nil {
		s.cancelLoop() // cancel any existing forwarding loop on channel switch
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancelLoop = cancel
	s.mu.Unlock()

	// Video forwarder (payload type 96)
	go s.forwardUDPToTrack(ctx, videoPort, s.VideoTrack, 96)
	// Audio forwarder (payload type 111)
	go s.forwardUDPToTrack(ctx, audioPort, s.AudioTrack, 111)
}

func (s *Session) forwardUDPToTrack(ctx context.Context, port int, track *webrtc.TrackLocalStaticRTP, expectedPT uint8) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return
	}

	// Retry connecting to UDP in case FFmpeg is still initializing
	var conn net.PacketConn
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 15, 1) // SO_REUSEPORT
				_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
		},
	}

	for i := 0; i < 20; i++ {
		conn, err = lc.ListenPacket(ctx, "udp", addr.String())
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conn == nil {
		return
	}
	defer conn.Close()

	buf := make([]byte, 1600)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}

		if n > 12 {
			// Enforce expected payload type in RTP header (byte 1: bits 0-6 are PT)
			buf[1] = (buf[1] & 0x80) | (expectedPT & 0x7F)
			if _, wErr := track.Write(buf[:n]); wErr != nil {
				// peer may have closed
				return
			}
		}
	}
}

func (m *Manager) GetSession(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

func (m *Manager) RemoveSession(id string) {
	m.mu.Lock()
	s, exists := m.sessions[id]
	if exists {
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	if exists && s != nil {
		s.mu.Lock()
		if s.cancelLoop != nil {
			s.cancelLoop()
		}
		s.mu.Unlock()
		_ = s.PC.Close()
	}
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()

	for _, s := range sessions {
		if s != nil {
			s.mu.Lock()
			if s.cancelLoop != nil {
				s.cancelLoop()
			}
			s.mu.Unlock()
			if s.PC != nil {
				_ = s.PC.Close()
			}
		}
	}
}
