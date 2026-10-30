package api

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"dvb-web/internal/db"
	"dvb-web/internal/epg"
	"dvb-web/internal/recorder"
	"dvb-web/internal/transcoder"
	"dvb-web/internal/tuner"
	webRTCManager "dvb-web/internal/webrtc"
	"dvb-web/web"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Server struct {
	db         *db.Database
	tunerPool  *tuner.Pool
	transcoder *transcoder.Detector
	webrtcMgr  *webRTCManager.Manager
	epgParser  *epg.Parser
	recorder   *recorder.Recorder
	wsClients  map[*wsClient]bool
	wsMu       sync.RWMutex
}

type wsClient struct {
	conn      *websocket.Conn
	sessionID string
	ip        string
	writeMu   sync.Mutex
}

func (c *wsClient) writeJSON(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(v)
}

func (c *wsClient) writeMessage(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(messageType, data)
}

func NewServer(database *db.Database, pool *tuner.Pool, detector *transcoder.Detector, rtcMgr *webRTCManager.Manager, epgP *epg.Parser, rec *recorder.Recorder) *Server {
	s := &Server{
		db:         database,
		tunerPool:  pool,
		transcoder: detector,
		webrtcMgr:  rtcMgr,
		epgParser:  epgP,
		recorder:   rec,
		wsClients:  make(map[*wsClient]bool),
	}
	s.startSignalMonitor()
	s.startHDHRDiscovery()
	return s
}

func (s *Server) RegisterRoutes(mux *http.ServeMux, webDir string) {
	// API routes
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/channels", s.handleChannels)
	mux.HandleFunc("/api/channels/reorder", s.handleReorderChannels)
	mux.HandleFunc("/api/channels/", s.handleChannelDetail)
	mux.HandleFunc("/api/scan", s.handleScan)
	mux.HandleFunc("/api/scan/prune", s.handleScanPrune)
	mux.HandleFunc("/api/epg", s.handleEPG)
	mux.HandleFunc("/api/epg/search", s.handleEPGSearch)
	mux.HandleFunc("/api/epg/languages", s.handleEPGLanguages)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/tuners/", s.handleTunerAction)
	mux.HandleFunc("/api/webrtc/offer", s.handleWebRTCOffer)
	mux.HandleFunc("/api/logos/", s.handleLogoStatic)
	mux.HandleFunc("/api/hbbtv/", s.handleHbbtvProxy)
	mux.HandleFunc("/api/hbbtv/proxy", s.handleHbbtvProxy)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/recordings", s.handleRecordings)
	mux.HandleFunc("/api/recordings/instant", s.handleRecordingInstant)
	mux.HandleFunc("/api/recordings/", s.handleRecordingDetail)

	// HDHomeRun Emulation (Native Plex & Jellyfin Tuner Support)
	mux.HandleFunc("/discover.json", s.handleHDHRDiscover)
	mux.HandleFunc("/lineup_status.json", s.handleHDHRLineupStatus)
	mux.HandleFunc("/lineup.json", s.handleHDHRLineup)
	mux.HandleFunc("/lineup.post", s.handleHDHRLineupPost)
	mux.HandleFunc("/auto/", s.handleAutoStream)
	mux.HandleFunc("/stream/", s.handleDirectStream)

	// External Player Integration (Jellyfin M3U, Kodi, VLC, XMLTV)
	mux.HandleFunc("/playlist.m3u", s.handleM3UPlaylist)
	mux.HandleFunc("/api/playlist.m3u", s.handleM3UPlaylist)
	mux.HandleFunc("/epg.xml", s.handleXMLTV)
	mux.HandleFunc("/api/epg.xml", s.handleXMLTV)

	// WebSocket
	mux.HandleFunc("/ws", s.handleWS)

	// Static Web UI (supports embedded binary assets with local directory override)
	fileServer := http.FileServer(web.GetFileSystem(webDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Fallback for HbbTV relative assets requested by browser
		ref := r.Referer()
		if strings.Contains(ref, "/api/hbbtv/proxy") {
			if refURL, err := url.Parse(ref); err == nil {
				if target := refURL.Query().Get("url"); target != "" {
					if parsedTarget, err := url.Parse(target); err == nil {
						cleanPath := r.URL.Path
						if strings.HasPrefix(cleanPath, "/api/") && !strings.HasPrefix(cleanPath, "/api/hbbtv/") {
							cleanPath = cleanPath[len("/api"):]
						}
						destURL := parsedTarget.ResolveReference(&url.URL{
							Path:     cleanPath,
							RawQuery: r.URL.RawQuery,
						})
						http.Redirect(w, r, "/api/hbbtv/proxy?url="+url.QueryEscape(destURL.String()), http.StatusTemporaryRedirect)
						return
					}
				}
			}
		}
		if strings.HasPrefix(r.URL.Path, "/ert-new/") {
			target := "http://hbbtvapplb.ert.gr" + r.URL.RequestURI()
			http.Redirect(w, r, "/api/hbbtv/proxy?url="+url.QueryEscape(target), http.StatusTemporaryRedirect)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	epgLangs, _ := s.db.GetEPGLanguages()
	if epgLangs == nil {
		epgLangs = []string{}
	}
	status := map[string]interface{}{
		"tuners":        s.tunerPool.GetStatus(),
		"hardware":      s.transcoder.GetActiveHardware(),
		"server_time":   time.Now().UTC(),
		"default_lang":  s.db.GetSetting("preferred_language", "eng"),
		"epg_languages": epgLangs,
	}
	jsonResponse(w, http.StatusOK, status)
}

func (s *Server) handleEPGLanguages(w http.ResponseWriter, r *http.Request) {
	langs, err := s.db.GetEPGLanguages()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if langs == nil {
		langs = []string{}
	}
	jsonResponse(w, http.StatusOK, langs)
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		channels, err := s.db.ListChannels()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, channels)

	case http.MethodPost:
		var c db.Channel
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := s.db.SaveChannel(&c); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, c)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleReorderChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var order []int64
	if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.db.ReorderChannels(order); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleChannelDetail(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/channels/")
	parts := strings.Split(path, "/")
	if len(parts) > 0 && parts[0] == "reorder" {
		s.handleReorderChannels(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		jsonError(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	// Handle Logo Upload: POST /api/channels/{id}/logo
	if len(parts) > 1 && parts[1] == "logo" && r.Method == http.MethodPost {
		s.handleLogoUpload(w, r, id)
		return
	}

	// Handle Channel Tracks: GET /api/channels/{id}/tracks
	if len(parts) > 1 && parts[1] == "tracks" && r.Method == http.MethodGet {
		s.handleChannelTracks(w, r, id)
		return
	}

	switch r.Method {
	case http.MethodGet:
		c, err := s.db.GetChannel(id)
		if err != nil {
			jsonError(w, "channel not found", http.StatusNotFound)
			return
		}
		jsonResponse(w, http.StatusOK, c)

	case http.MethodDelete:
		if err := s.db.DeleteChannel(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogoUpload(w http.ResponseWriter, r *http.Request, channelID int64) {
	err := r.ParseMultipartForm(10 << 20) // 10 MB max
	if err != nil {
		jsonError(w, "file too large or invalid multipart", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("logo")
	if err != nil {
		jsonError(w, "logo field is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(handler.Filename))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".svg" && ext != ".webp" {
		jsonError(w, "only png, jpg, svg, and webp images are supported", http.StatusBadRequest)
		return
	}

	filename := fmt.Sprintf("ch_%d_%d%s", channelID, time.Now().Unix(), ext)
	dstPath := filepath.Join(s.db.DataDir(), "logos", filename)

	dst, err := os.Create(dstPath)
	if err != nil {
		jsonError(w, "failed to save logo", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		jsonError(w, "failed to write logo", http.StatusInternalServerError)
		return
	}

	logoURL := "/api/logos/" + filename
	if err := s.db.UpdateChannelLogo(channelID, logoURL); err != nil {
		jsonError(w, "failed to update channel logo url", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"logo_url": logoURL})
}

func (s *Server) handleLogoStatic(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimPrefix(r.URL.Path, "/api/logos/")
	filePath := filepath.Join(s.db.DataDir(), "logos", filepath.Clean(filename))
	http.ServeFile(w, r, filePath)
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		AdapterID int `json:"adapter_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	discovered, err := s.tunerPool.ScanFrequencies(req.AdapterID, func(freq int64, progressPct int, foundCount int, recent []string) {
		s.BroadcastScanProgress(freq, progressPct, foundCount, recent)
	})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	diff, err := s.db.ProcessScanResults(discovered)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, diff)
}

func (s *Server) handleScanPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ChannelIDs []int64 `json:"channel_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	pruned, err := s.db.PruneChannels(req.ChannelIDs)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"pruned_count": pruned,
	})
}

func (s *Server) handleEPG(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelID, _ := strconv.ParseInt(q.Get("channel_id"), 10, 64)

	from := time.Now().Add(-2 * time.Hour)
	to := time.Now().Add(7 * 24 * time.Hour)

	if f := q.Get("from"); f != "" {
		if t, err := time.Parse(time.RFC3339, f); err == nil {
			from = t
		}
	}
	if t := q.Get("to"); t != "" {
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			to = parsed
		}
	}

	var events []db.EPGEvent
	var err error
	if channelID > 0 {
		events, err = s.db.GetEPG(channelID, from, to)
	} else {
		events, err = s.db.GetAllEPG(from, to)
	}
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, events)
}

func (s *Server) handleEPGSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		jsonResponse(w, http.StatusOK, []db.EPGSearchResult{})
		return
	}

	results, err := s.db.SearchEPG(q, 50)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, results)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.db.GetAllSettings()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, settings)

	case http.MethodPost:
		var updates map[string]string
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			jsonError(w, "invalid request", http.StatusBadRequest)
			return
		}
		for k, v := range updates {
			_ = s.db.SetSetting(k, v)
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "updated"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTunerAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/tuners/")
	parts := strings.Split(path, "/")
	tunerID, err := strconv.Atoi(parts[0])
	if err != nil {
		jsonError(w, "invalid tuner id", http.StatusBadRequest)
		return
	}

	// Action: POST /api/tuners/{id}/force-release
	if len(parts) > 1 && parts[1] == "force-release" && r.Method == http.MethodPost {
		clientIP := extractIP(r)
		if err := s.tunerPool.ForceReleaseTuner(tunerID, clientIP); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.BroadcastTuners(s.tunerPool.GetStatus())
		jsonResponse(w, http.StatusOK, map[string]string{"status": "released"})
		return
	}

	// Action: POST /api/tuners/{id}/toggle
	if len(parts) > 1 && parts[1] == "toggle" && r.Method == http.MethodPost {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := s.tunerPool.SetTunerEnabled(tunerID, req.Enabled); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.BroadcastTuners(s.tunerPool.GetStatus())
		jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "updated", "enabled": req.Enabled})
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func (s *Server) handleWebRTCOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string                    `json:"session_id"`
		Offer     webrtc.SessionDescription `json:"offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	session, err := s.webrtcMgr.CreateSession(req.SessionID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := session.PC.SetRemoteDescription(req.Offer); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	answer, err := session.PC.CreateAnswer(nil)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	gatherComplete := webrtc.GatheringCompletePromise(session.PC)
	if err := session.PC.SetLocalDescription(answer); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	<-gatherComplete

	// Check if this viewer already has an active tuner streaming
	if activeTuner := s.tunerPool.GetTunerForSession(req.SessionID); activeTuner != nil {
		vPort, aPort := activeTuner.Ports()
		session.StartRTPForwarding(vPort, aPort)
	}

	jsonResponse(w, http.StatusOK, session.PC.LocalDescription())
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	clientIP := extractIP(r)
	client := &wsClient{
		conn:      conn,
		sessionID: sessionID,
		ip:        clientIP,
	}

	s.wsMu.Lock()
	s.wsClients[client] = true
	s.wsMu.Unlock()

	// Send initial tuner status to newly connected client
	_ = client.writeJSON(map[string]interface{}{
		"event":  "tuners_update",
		"tuners": s.tunerPool.GetStatus(),
	})

	defer func() {
		s.wsMu.Lock()
		delete(s.wsClients, client)
		s.wsMu.Unlock()
		s.tunerPool.ReleaseSession(sessionID)
		s.webrtcMgr.RemoveSession(sessionID)
		conn.Close()
		s.BroadcastTuners(s.tunerPool.GetStatus())
	}()

	// Read pump
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var cmd struct {
			Action      string `json:"action"`
			ChannelID   int64  `json:"channel_id"`
			TunerID     int    `json:"tuner_id"`
			RecordingID int64  `json:"recording_id"`
		}
		if err := json.Unmarshal(message, &cmd); err != nil {
			continue
		}

		switch cmd.Action {
		case "tune":
			s.handleWSTune(client, cmd.ChannelID)
		case "force_release":
			_ = s.tunerPool.ForceReleaseTuner(cmd.TunerID, client.ip)
			_ = client.writeJSON(map[string]interface{}{
				"event":    "tuner_released",
				"tuner_id": cmd.TunerID,
			})
			s.BroadcastTuners(s.tunerPool.GetStatus())
		case "stop_recording_and_tune":
			_ = s.recorder.StopRecording(cmd.RecordingID)
			s.BroadcastTuners(s.tunerPool.GetStatus())
			s.handleWSTune(client, cmd.ChannelID)
		}
	}
}

func (s *Server) handleWSTune(client *wsClient, channelID int64) {
	channel, err := s.db.GetChannel(channelID)
	if err != nil {
		log.Printf("[Server] Tune error: channel ID %d not found in DB", channelID)
		_ = client.writeJSON(map[string]string{"error": "Channel not found"})
		return
	}

	log.Printf("[Server] Tune requested by Session %s (IP: %s) for Channel: %s (Freq: %d Hz, SID: %d)",
		client.sessionID, client.ip, channel.Name, channel.Frequency, channel.ServiceID)

	kickChan := make(chan string, 1)
	viewerSession := &tuner.ViewerSession{
		ID:          client.sessionID,
		IP:          client.ip,
		UserAgent:   "DVB-Web Client",
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		ConnectedAt: time.Now(),
		KickChan:    kickChan,
	}

	// Listen for kick events
	go func() {
		for reason := range kickChan {
			_ = client.writeJSON(map[string]interface{}{
				"event":  "kicked",
				"reason": reason,
			})
		}
	}()

	t, err := s.tunerPool.AllocateChannel(channel, viewerSession)
	if err != nil {
		log.Printf("[Server] AllocateChannel error: %v", err)
		if conflict, ok := err.(*tuner.TunerConflictError); ok {
			_ = client.writeJSON(map[string]interface{}{
				"event":                "conflict",
				"message":              "All tuners are busy",
				"active_tuners":        conflict.ActiveTuners,
				"has_active_recording": conflict.HasActiveRecording,
				"active_recordings":    conflict.ActiveRecordings,
				"target_channel":       channel,
			})
			return
		}
		_ = client.writeJSON(map[string]string{"error": err.Error()})
		return
	}

	// Start RTP Forwarding into this client's WebRTC session
	videoPort, audioPort := t.Ports()
	rtcSession := s.webrtcMgr.GetSession(client.sessionID)
	if rtcSession != nil {
		rtcSession.StartRTPForwarding(videoPort, audioPort)
	}

	sig := t.GetSignal()
	_ = client.writeJSON(map[string]interface{}{
		"event":        "tuned",
		"channel":      channel,
		"tuner_id":     t.ID,
		"signal":       sig,
		"now_playing":  channel.NowPlaying,
		"next_playing": channel.NextPlaying,
	})
	s.BroadcastTuners(s.tunerPool.GetStatus())
}

func getBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "127.0.0.1:8080"
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || localAddr == nil {
		return "127.0.0.1"
	}
	return localAddr.IP.String()
}

func (s *Server) streamChannel(w http.ResponseWriter, r *http.Request, channel *db.Channel, sessionID string) {
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	viewerSession := &tuner.ViewerSession{
		ID:          sessionID,
		IP:          extractIP(r),
		UserAgent:   r.UserAgent(),
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		ConnectedAt: time.Now(),
	}

	audioTrack := 0
	if at := r.URL.Query().Get("audio_track"); at != "" {
		if v, err := strconv.Atoi(at); err == nil {
			audioTrack = v
		}
	}
	subTrack := -1
	if st := r.URL.Query().Get("sub_track"); st != "" {
		if v, err := strconv.Atoi(st); err == nil {
			subTrack = v
		}
	}
	subOffset := 0 // default 0ms offset
	if so := r.URL.Query().Get("sub_offset"); so != "" {
		if v, err := strconv.Atoi(so); err == nil {
			subOffset = v
		}
	}

	t, actualSubTrack, err := s.tunerPool.AllocateChannelTrack(channel, audioTrack, subTrack, subOffset, viewerSession)
	if err != nil {
		log.Printf("[Server] streamChannel allocate error: %v", err)
		if conflict, ok := err.(*tuner.TunerConflictError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error":                "all tuners busy",
				"has_active_recording": conflict.HasActiveRecording,
				"active_recordings":    conflict.ActiveRecordings,
				"active_tuners":        conflict.ActiveTuners,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.BroadcastTuners(s.tunerPool.GetStatus())

	ch := t.SubscribeTrack(channel.ID, audioTrack, actualSubTrack, subOffset, sessionID)
	if ch == nil {
		http.Error(w, "stream unavailable", http.StatusInternalServerError)
		return
	}
	streamKey := tuner.GetStreamKey(channel.ID, audioTrack, actualSubTrack, subOffset)
	defer func() {
		s.tunerPool.ReleaseStreamViewerTrack(t.ID, channel.ID, streamKey, sessionID)
		s.BroadcastTuners(s.tunerPool.GetStatus())
	}()

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Sub-Track", strconv.Itoa(actualSubTrack))

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	channelIDStr := r.URL.Query().Get("channel_id")
	sessionID := r.URL.Query().Get("session_id")

	channelID, err := strconv.ParseInt(channelIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid channel_id", http.StatusBadRequest)
		return
	}

	channel, err := s.db.GetChannel(channelID)
	if err != nil {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}

	s.streamChannel(w, r, channel, sessionID)
}

func (s *Server) handleAutoStream(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/auto/")
	path = strings.TrimPrefix(path, "v")
	path = strings.TrimSuffix(path, ".ts")

	channels, err := s.db.ListChannels()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	var target *db.Channel
	if num, err := strconv.Atoi(path); err == nil {
		for i := range channels {
			if channels[i].Number == num {
				target = &channels[i]
				break
			}
		}
	}
	if target == nil {
		if id, err := strconv.ParseInt(path, 10, 64); err == nil {
			for i := range channels {
				if channels[i].ID == id {
					target = &channels[i]
					break
				}
			}
		}
	}
	if target == nil {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = "auto-" + uuid.New().String()
	}
	s.streamChannel(w, r, target, sessionID)
}

func (s *Server) handleDirectStream(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/stream/")
	path = strings.TrimSuffix(path, ".ts")
	path = strings.TrimPrefix(path, "v")

	channels, err := s.db.ListChannels()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	var target *db.Channel
	if id, err := strconv.ParseInt(path, 10, 64); err == nil {
		for i := range channels {
			if channels[i].ID == id {
				target = &channels[i]
				break
			}
		}
	}
	if target == nil {
		if num, err := strconv.Atoi(path); err == nil {
			for i := range channels {
				if channels[i].Number == num {
					target = &channels[i]
					break
				}
			}
		}
	}
	if target == nil {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = "stream-" + uuid.New().String()
	}
	s.streamChannel(w, r, target, sessionID)
}

func (s *Server) handleHDHRDiscover(w http.ResponseWriter, r *http.Request) {
	baseURL := getBaseURL(r)
	tuners := s.tunerPool.GetStatus()
	tunerCount := len(tuners)
	if tunerCount == 0 {
		tunerCount = 1
	}

	resp := map[string]interface{}{
		"FriendlyName":    "dvb-web",
		"ModelNumber":     "HDTC-2US",
		"FirmwareName":    "hdhomeruntc_atsc",
		"FirmwareVersion": "20230505",
		"DeviceID":        "10405060",
		"DeviceAuth":      "dvbweb12345",
		"TunerCount":      tunerCount,
		"BaseURL":         baseURL,
		"LineupURL":       baseURL + "/lineup.json",
	}
	jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleHDHRLineupStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"ScanInProgress": 0,
		"ScanPossible":   1,
		"Source":         "Antenna",
		"SourceList":     []string{"Antenna"},
	}
	jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleHDHRLineup(w http.ResponseWriter, r *http.Request) {
	channels, err := s.db.ListChannels()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	baseURL := getBaseURL(r)
	type LineupItem struct {
		GuideNumber string `json:"GuideNumber"`
		GuideName   string `json:"GuideName"`
		URL         string `json:"URL"`
	}

	var lineup []LineupItem
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		guideNum := strconv.Itoa(ch.Number)
		if ch.Number <= 0 {
			guideNum = strconv.FormatInt(ch.ID, 10)
		}
		lineup = append(lineup, LineupItem{
			GuideNumber: guideNum,
			GuideName:   ch.Name,
			URL:         fmt.Sprintf("%s/auto/v%s", baseURL, guideNum),
		})
	}
	jsonResponse(w, http.StatusOK, lineup)
}

func (s *Server) handleHDHRLineupPost(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleM3UPlaylist(w http.ResponseWriter, r *http.Request) {
	channels, err := s.db.ListChannels()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	baseURL := getBaseURL(r)
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")

	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		logoAttr := ""
		if ch.LogoURL != "" {
			if strings.HasPrefix(ch.LogoURL, "http") {
				logoAttr = fmt.Sprintf(" tvg-logo=\"%s\"", ch.LogoURL)
			} else {
				logoAttr = fmt.Sprintf(" tvg-logo=\"%s%s\"", baseURL, ch.LogoURL)
			}
		}
		guideNum := ch.Number
		if guideNum <= 0 {
			guideNum = int(ch.ID)
		}

		sb.WriteString(fmt.Sprintf(
			"#EXTINF:-1 tvg-id=\"%d\" tvg-name=\"%s\" tvg-chno=\"%d\"%s group-title=\"DVB\",%s\n",
			ch.ID, ch.Name, guideNum, logoAttr, ch.Name,
		))
		sb.WriteString(fmt.Sprintf("%s/stream/%d\n", baseURL, ch.ID))
	}

	w.Header().Set("Content-Type", "application/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=\"dvb-web.m3u\"")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(sb.String()))
}

func (s *Server) handleXMLTV(w http.ResponseWriter, r *http.Request) {
	channels, err := s.db.ListChannels()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	from := time.Now().Add(-6 * time.Hour)
	to := time.Now().Add(7 * 24 * time.Hour)
	events, err := s.db.GetAllEPG(from, to)
	if err != nil {
		http.Error(w, "epg database error", http.StatusInternalServerError)
		return
	}

	baseURL := getBaseURL(r)
	preferredLang := s.db.GetSetting("preferred_language", "eng")

	type XMLTVIcon struct {
		Src string `xml:"src,attr"`
	}
	type XMLTVDisplayName struct {
		Value string `xml:",chardata"`
	}
	type XMLTVChannel struct {
		ID           string             `xml:"id,attr"`
		DisplayNames []XMLTVDisplayName `xml:"display-name"`
		Icon         *XMLTVIcon         `xml:"icon,omitempty"`
	}
	type XMLTVText struct {
		Lang  string `xml:"lang,attr,omitempty"`
		Value string `xml:",chardata"`
	}
	type XMLTVProgramme struct {
		Start      string      `xml:"start,attr"`
		Stop       string      `xml:"stop,attr"`
		Channel    string      `xml:"channel,attr"`
		Titles     []XMLTVText `xml:"title"`
		Descs      []XMLTVText `xml:"desc,omitempty"`
		Categories []XMLTVText `xml:"category,omitempty"`
	}
	type XMLTVRoot struct {
		XMLName       xml.Name         `xml:"tv"`
		GeneratorName string           `xml:"generator-info-name,attr"`
		Channels      []XMLTVChannel   `xml:"channel"`
		Programmes    []XMLTVProgramme `xml:"programme"`
	}

	root := XMLTVRoot{
		GeneratorName: "dvb-web",
	}

	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		chIDStr := strconv.FormatInt(ch.ID, 10)
		xc := XMLTVChannel{
			ID: chIDStr,
			DisplayNames: []XMLTVDisplayName{
				{Value: ch.Name},
			},
		}
		if ch.Number > 0 {
			xc.DisplayNames = append(xc.DisplayNames, XMLTVDisplayName{
				Value: fmt.Sprintf("%d %s", ch.Number, ch.Name),
			})
			xc.DisplayNames = append(xc.DisplayNames, XMLTVDisplayName{
				Value: strconv.Itoa(ch.Number),
			})
		}
		if ch.LogoURL != "" {
			logoSrc := ch.LogoURL
			if !strings.HasPrefix(logoSrc, "http") {
				logoSrc = baseURL + logoSrc
			}
			xc.Icon = &XMLTVIcon{Src: logoSrc}
		}
		root.Channels = append(root.Channels, xc)
	}

	seenSlot := make(map[string]bool)
	for _, e := range events {
		slotKey := fmt.Sprintf("%d_%d", e.ChannelID, e.StartTime.Unix())
		if seenSlot[slotKey] {
			continue
		}
		seenSlot[slotKey] = true

		chIDStr := strconv.FormatInt(e.ChannelID, 10)
		startStr := e.StartTime.UTC().Format("20060102150405 +0000")
		stopStr := e.EndTime.UTC().Format("20060102150405 +0000")

		prog := XMLTVProgramme{
			Start:   startStr,
			Stop:    stopStr,
			Channel: chIDStr,
		}

		for lang, title := range e.Titles {
			if title != "" {
				prog.Titles = append(prog.Titles, XMLTVText{Lang: lang, Value: title})
			}
		}
		if len(prog.Titles) == 0 {
			prog.Titles = append(prog.Titles, XMLTVText{Lang: preferredLang, Value: "Live Broadcast"})
		}

		for lang, desc := range e.Descriptions {
			if desc != "" {
				prog.Descs = append(prog.Descs, XMLTVText{Lang: lang, Value: desc})
			}
		}

		for _, cat := range e.Genres {
			if cat != "" {
				prog.Categories = append(prog.Categories, XMLTVText{Lang: "en", Value: cat})
			}
		}

		root.Programmes = append(root.Programmes, prog)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(root)
}

func (s *Server) startHDHRDiscovery() {
	addr, err := net.ResolveUDPAddr("udp4", ":65001")
	if err != nil {
		return
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Printf("[HDHR] UDP 65001 listen notice (manual IP entry still works): %v", err)
		return
	}
	log.Printf("[HDHR] Listening for Plex/Jellyfin HDHomeRun discovery on UDP :65001")

	go func() {
		defer conn.Close()
		buf := make([]byte, 1500)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 4 {
				continue
			}
			// HDHOMERUN_TYPE_DISCOVER_REQ is 0x0002
			if buf[0] == 0x00 && buf[1] == 0x02 {
				resp := s.buildHDHRDiscoverResponse()
				_, _ = conn.WriteToUDP(resp, src)
			}
		}
	}()
}

func (s *Server) buildHDHRDiscoverResponse() []byte {
	localIP := getOutboundIP()
	baseURL := fmt.Sprintf("http://%s:8080", localIP)
	lineupURL := fmt.Sprintf("http://%s:8080/lineup.json", localIP)

	tuners := s.tunerPool.GetStatus()
	tunerCount := len(tuners)
	if tunerCount == 0 {
		tunerCount = 1
	}

	var payload []byte
	payload = append(payload, 0x01, 0x04, 0x00, 0x00, 0x00, 0x01)
	payload = append(payload, 0x02, 0x04, 0x10, 0x40, 0x50, 0x60)
	payload = append(payload, 0x2B, 0x01, byte(tunerCount))
	payload = appendTLV(payload, 0x10, []byte(baseURL))
	payload = appendTLV(payload, 0x29, []byte(lineupURL))

	frame := make([]byte, 4+len(payload)+4)
	binary.BigEndian.PutUint16(frame[0:2], 0x0003) // Discovery Reply
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(payload)))
	copy(frame[4:], payload)

	crc := crc32.ChecksumIEEE(frame[:4+len(payload)])
	binary.LittleEndian.PutUint32(frame[4+len(payload):], crc)

	return frame
}

func appendTLV(buf []byte, tag byte, val []byte) []byte {
	buf = append(buf, tag)
	l := len(val)
	if l < 128 {
		buf = append(buf, byte(l))
	} else {
		buf = append(buf, byte(0x80|(l&0x7F)), byte(l>>7))
	}
	return append(buf, val...)
}

func (s *Server) startSignalMonitor() {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.wsMu.RLock()
			clientCount := len(s.wsClients)
			s.wsMu.RUnlock()
			if clientCount == 0 {
				continue
			}

			statuses := s.tunerPool.GetStatus()
			for _, st := range statuses {
				if st.Status == "active" && st.Signal != nil && len(st.Viewers) > 0 {
					s.BroadcastSignal(st.ID, *st.Signal)
				}
			}
		}
	}()
}

// BroadcastTuners sends tuner statuses to all connected browser clients
func (s *Server) BroadcastTuners(statuses []tuner.TunerStatus) {
	msg, err := json.Marshal(map[string]interface{}{
		"event":  "tuners_update",
		"tuners": statuses,
	})
	if err != nil {
		return
	}

	s.wsMu.RLock()
	defer s.wsMu.RUnlock()
	for client := range s.wsClients {
		_ = client.writeMessage(websocket.TextMessage, msg)
	}
}

// BroadcastSignal sends live signal quality updates to connected browser clients
func (s *Server) BroadcastSignal(tunerID int, sig tuner.SignalInfo) {
	msg, err := json.Marshal(map[string]interface{}{
		"event":    "signal",
		"tuner_id": tunerID,
		"signal":   sig,
	})
	if err != nil {
		return
	}

	s.wsMu.RLock()
	defer s.wsMu.RUnlock()
	for client := range s.wsClients {
		_ = client.writeMessage(websocket.TextMessage, msg)
	}
}

// BroadcastEPGUpdate sends new EPG show updates to all connected browser clients
func (s *Server) BroadcastEPGUpdate(events []db.EPGEvent) {
	msg, err := json.Marshal(map[string]interface{}{
		"event":  "epg_update",
		"events": events,
	})
	if err != nil {
		return
	}

	s.wsMu.RLock()
	defer s.wsMu.RUnlock()
	for client := range s.wsClients {
		_ = client.writeMessage(websocket.TextMessage, msg)
	}
}

// BroadcastScanProgress notifies web clients of frequency scan progress
func (s *Server) BroadcastScanProgress(freq int64, progressPct int, foundCount int, recent []string) {
	msg, err := json.Marshal(map[string]interface{}{
		"event":           "scan_progress",
		"frequency":       freq,
		"progress":        progressPct,
		"found_count":     foundCount,
		"recent_channels": recent,
	})
	if err != nil {
		return
	}

	s.wsMu.RLock()
	defer s.wsMu.RUnlock()
	for client := range s.wsClients {
		_ = client.writeMessage(websocket.TextMessage, msg)
	}
}

func extractIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		parts := strings.Split(f, ",")
		return strings.TrimSpace(parts[0])
	}
	if r := r.Header.Get("X-Real-IP"); r != "" {
		return r
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func jsonResponse(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[Server] jsonResponse encode error: %v", err)
	}
}

func jsonError(w http.ResponseWriter, message string, code int) {
	jsonResponse(w, code, map[string]string{"error": message})
}

func (s *Server) handleChannelTracks(w http.ResponseWriter, r *http.Request, channelID int64) {
	c, err := s.db.GetChannel(channelID)
	if err != nil {
		jsonError(w, "channel not found", http.StatusNotFound)
		return
	}
	tracks := s.tunerPool.GetChannelTracks(c)
	jsonResponse(w, http.StatusOK, tracks)
}

func (s *Server) handleRecordings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		recs, err := s.db.ListRecordings()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Merge live file size & status from active jobs
		activeJobs := s.recorder.GetActiveJobs()
		activeMap := make(map[int64]*db.Recording)
		for _, job := range activeJobs {
			activeMap[job.ID] = job
		}
		for i := range recs {
			if active, ok := activeMap[recs[i].ID]; ok {
				recs[i] = *active
			}
		}
		if recs == nil {
			recs = []db.Recording{}
		}
		jsonResponse(w, http.StatusOK, recs)

	case http.MethodPost:
		var req struct {
			ChannelID   int64     `json:"channel_id"`
			Title       string    `json:"title"`
			Description string    `json:"description"`
			StartTime   time.Time `json:"start_time"`
			EndTime     time.Time `json:"end_time"`
			Duration    int       `json:"duration"`
			EventID     int       `json:"event_id"`
			PrePadding  *int      `json:"pre_padding,omitempty"`
			PostPadding *int      `json:"post_padding,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		channel, err := s.db.GetChannel(req.ChannelID)
		if err != nil {
			jsonError(w, "channel not found", http.StatusBadRequest)
			return
		}
		if req.Duration <= 0 && !req.EndTime.IsZero() && !req.StartTime.IsZero() {
			req.Duration = int(req.EndTime.Sub(req.StartTime).Seconds())
		}
		if req.Duration <= 0 {
			req.Duration = 3600
		}
		if req.StartTime.IsZero() {
			req.StartTime = time.Now()
		}
		if req.EndTime.IsZero() {
			req.EndTime = req.StartTime.Add(time.Duration(req.Duration) * time.Second)
		}
		if req.Title == "" {
			req.Title = fmt.Sprintf("%s Recording", channel.Name)
		}

		// If scheduled start time is immediately (within 10s), start instant recording
		if req.StartTime.Before(time.Now().Add(10 * time.Second)) {
			activeRec, err := s.recorder.StartInstantRecording(channel.ID, req.Title, req.Duration)
			if err != nil {
				if conflict, ok := err.(*tuner.TunerConflictError); ok {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"error":                err.Error(),
						"has_active_recording": conflict.HasActiveRecording,
						"active_recordings":    conflict.ActiveRecordings,
						"active_tuners":        conflict.ActiveTuners,
					})
					return
				}
				jsonError(w, err.Error(), http.StatusConflict)
				return
			}
			s.BroadcastTuners(s.tunerPool.GetStatus())
			jsonResponse(w, http.StatusCreated, activeRec)
			return
		}

		prePad := 2
		postPad := 5
		if req.PrePadding != nil {
			prePad = *req.PrePadding
		} else {
			if p, err := strconv.Atoi(s.db.GetSetting("rec_pre_padding", "2")); err == nil {
				prePad = p
			}
		}
		if req.PostPadding != nil {
			postPad = *req.PostPadding
		} else {
			if p, err := strconv.Atoi(s.db.GetSetting("rec_post_padding", "5")); err == nil {
				postPad = p
			}
		}

		rec := &db.Recording{
			ChannelID:   channel.ID,
			ChannelName: channel.Name,
			Title:       req.Title,
			Description: req.Description,
			StartTime:   req.StartTime,
			EndTime:     req.EndTime,
			Duration:    req.Duration,
			EventID:     req.EventID,
			PrePadding:  prePad,
			PostPadding: postPad,
			Status:      "scheduled",
			CreatedAt:   time.Now(),
		}

		if err := s.db.CreateRecording(rec); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusCreated, rec)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRecordingInstant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ChannelID int64  `json:"channel_id"`
		Title     string `json:"title"`
		Duration  int    `json:"duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	rec, err := s.recorder.StartInstantRecording(req.ChannelID, req.Title, req.Duration)
	if err != nil {
		if conflict, ok := err.(*tuner.TunerConflictError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error":                err.Error(),
				"has_active_recording": conflict.HasActiveRecording,
				"active_recordings":    conflict.ActiveRecordings,
				"active_tuners":        conflict.ActiveTuners,
			})
			return
		}
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	s.BroadcastTuners(s.tunerPool.GetStatus())
	jsonResponse(w, http.StatusCreated, rec)
}

func (s *Server) handleRecordingDetail(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/recordings/")
	parts := strings.Split(path, "/")

	// Storage Overview Stats: GET /api/recordings/storage
	if len(parts) > 0 && parts[0] == "storage" && r.Method == http.MethodGet {
		recs, _ := s.db.ListRecordings()
		var totalBytes int64
		var totalDuration int
		for _, rec := range recs {
			totalBytes += rec.FileSize
			totalDuration += rec.Duration
		}

		dataDir := s.recorder.GetDataDir()
		freeBytes, totalDiskBytes := getDiskSpace(dataDir)

		jsonResponse(w, http.StatusOK, map[string]interface{}{
			"total_recordings": len(recs),
			"total_bytes":      totalBytes,
			"total_duration":   totalDuration,
			"free_bytes":       freeBytes,
			"total_disk_bytes": totalDiskBytes,
		})
		return
	}

	// Clear Failed Recordings: POST /api/recordings/clear-failed
	if len(parts) > 0 && parts[0] == "clear-failed" && r.Method == http.MethodPost {
		count, err := s.db.ClearFailedRecordings()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]interface{}{
			"cleared": count,
		})
		return
	}

	// Instant Recording: POST /api/recordings/instant
	if len(parts) > 0 && parts[0] == "instant" && r.Method == http.MethodPost {
		var req struct {
			ChannelID int64  `json:"channel_id"`
			Title     string `json:"title"`
			Duration  int    `json:"duration"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		rec, err := s.recorder.StartInstantRecording(req.ChannelID, req.Title, req.Duration)
		if err != nil {
			if conflict, ok := err.(*tuner.TunerConflictError); ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error":                err.Error(),
					"has_active_recording": conflict.HasActiveRecording,
					"active_recordings":    conflict.ActiveRecordings,
					"active_tuners":        conflict.ActiveTuners,
				})
				return
			}
			jsonError(w, err.Error(), http.StatusConflict)
			return
		}
		s.BroadcastTuners(s.tunerPool.GetStatus())
		jsonResponse(w, http.StatusCreated, rec)
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		jsonError(w, "invalid recording id", http.StatusBadRequest)
		return
	}

	// Action: POST /api/recordings/{id}/stop
	if len(parts) > 1 && parts[1] == "stop" && r.Method == http.MethodPost {
		if err := s.recorder.StopRecording(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.BroadcastTuners(s.tunerPool.GetStatus())
		jsonResponse(w, http.StatusOK, map[string]string{"status": "stopped"})
		return
	}

	// Action: POST /api/recordings/{id}/stop-and-tune
	if len(parts) > 1 && parts[1] == "stop-and-tune" && r.Method == http.MethodPost {
		if err := s.recorder.StopRecording(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.BroadcastTuners(s.tunerPool.GetStatus())
		jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "stopped", "id": id})
		return
	}

	// Action: GET/HEAD /api/recordings/{id}/stream
	if len(parts) > 1 && parts[1] == "stream" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		rec, err := s.db.GetRecording(id)
		if err != nil {
			jsonError(w, "recording not found", http.StatusNotFound)
			return
		}
		if _, err := os.Stat(rec.FilePath); os.IsNotExist(err) {
			jsonError(w, "recording file not found on disk", http.StatusNotFound)
			return
		}

		servePath := rec.FilePath
		contentType := "video/mp2t"

		// Prefer faststart .mp4 for browser HTML5 playback if available or generate on-demand
		mp4Path := strings.TrimSuffix(rec.FilePath, ".ts") + ".mp4"
		if _, err := os.Stat(mp4Path); err == nil {
			servePath = mp4Path
			contentType = "video/mp4"
		} else if strings.HasSuffix(rec.FilePath, ".ts") {
			cmd := exec.Command("ffmpeg", "-i", rec.FilePath, "-c", "copy", "-movflags", "+faststart", mp4Path, "-y")
			if err := cmd.Run(); err == nil {
				servePath = mp4Path
				contentType = "video/mp4"
			}
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		http.ServeFile(w, r, servePath)
		return
	}

	// Action: GET/HEAD /api/recordings/{id}/download
	if len(parts) > 1 && parts[1] == "download" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		rec, err := s.db.GetRecording(id)
		if err != nil {
			jsonError(w, "recording not found", http.StatusNotFound)
			return
		}
		if _, err := os.Stat(rec.FilePath); os.IsNotExist(err) {
			jsonError(w, "recording file not found on disk", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(rec.FilePath)))
		http.ServeFile(w, r, rec.FilePath)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		rec, err := s.db.GetRecording(id)
		if err != nil {
			jsonError(w, "recording not found", http.StatusNotFound)
			return
		}
		jsonResponse(w, http.StatusOK, rec)

	case http.MethodDelete:
		rec, err := s.db.GetRecording(id)
		if err == nil {
			_ = s.recorder.StopRecording(id)
			if rec.FilePath != "" {
				_ = os.Remove(rec.FilePath)
				_ = os.Remove(strings.TrimSuffix(rec.FilePath, ".ts") + ".mp4")
			}
		}
		if err := s.db.DeleteRecording(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.BroadcastTuners(s.tunerPool.GetStatus())
		jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func rewriteLocationNav(code string) string {
	var sb strings.Builder
	sb.Grow(len(code))

	re := regexp.MustCompile(`(?i)(?:window\.|document\.)?location\.(href\s*=|assign\s*\(|replace\s*\()`)
	locs := re.FindAllStringIndex(code, -1)
	if len(locs) == 0 {
		return code
	}

	lastIdx := 0
	for _, loc := range locs {
		start := loc[0]
		end := loc[1]

		if start < lastIdx {
			continue
		}

		matchStr := code[start:end]
		lower := strings.ToLower(matchStr)

		// Don't match == or ===
		if strings.Contains(lower, "href") && end < len(code) && code[end] == '=' {
			continue
		}

		sb.WriteString(code[lastIdx:start])

		if strings.Contains(lower, "assign") || strings.Contains(lower, "replace") {
			sb.WriteString("window.__proxyNav(")
			lastIdx = end
			continue
		}

		exprEnd := end
		var inQuote rune = 0
		escaped := false
		parenDepth := 0
		bracketDepth := 0
		braceDepth := 0
		ternaryDepth := 0

		for exprEnd < len(code) {
			ch := rune(code[exprEnd])

			if inQuote != 0 {
				if escaped {
					escaped = false
				} else if ch == '\\' {
					escaped = true
				} else if ch == inQuote {
					inQuote = 0
				}
				exprEnd++
				continue
			}

			if ch == '\'' || ch == '"' || ch == '`' {
				inQuote = ch
				exprEnd++
				continue
			}

			if ch == '(' {
				parenDepth++
				exprEnd++
				continue
			} else if ch == ')' {
				if parenDepth > 0 {
					parenDepth--
					exprEnd++
					continue
				}
				break
			}

			if ch == '[' {
				bracketDepth++
				exprEnd++
				continue
			} else if ch == ']' {
				if bracketDepth > 0 {
					bracketDepth--
					exprEnd++
					continue
				}
				break
			}

			if ch == '{' {
				braceDepth++
				exprEnd++
				continue
			} else if ch == '}' {
				if braceDepth > 0 {
					braceDepth--
					exprEnd++
					continue
				}
				break
			}

			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				if ch == '?' {
					ternaryDepth++
				} else if ch == ':' {
					if ternaryDepth > 0 {
						ternaryDepth--
					} else {
						break
					}
				} else if ch == ';' || ch == ',' || ch == '\n' || ch == '\r' {
					break
				}
			}

			exprEnd++
		}

		expr := strings.TrimSpace(code[end:exprEnd])
		sb.WriteString("window.__proxyNav(" + expr + ")")
		lastIdx = exprEnd
	}

	sb.WriteString(code[lastIdx:])
	return sb.String()
}

func (s *Server) handleHbbtvProxy(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		// Relative asset requested directly by the browser under /api/hbbtv/ (e.g. /api/hbbtv/ram/keycodes-min.js)
		ref := r.Referer()
		if ref != "" {
			if refURL, err := url.Parse(ref); err == nil {
				if parentTarget := refURL.Query().Get("url"); parentTarget != "" {
					if pURL, err := url.Parse(parentTarget); err == nil {
						baseURL := pURL.Scheme + "://" + pURL.Host + "/"
						if idx := strings.LastIndex(pURL.Path, "/"); idx != -1 {
							baseURL = pURL.Scheme + "://" + pURL.Host + pURL.Path[:idx+1]
						}
						baseParsed, _ := url.Parse(baseURL)
						subPath := strings.TrimPrefix(r.URL.Path, "/api/hbbtv/")
						if baseParsed != nil {
							relURL := &url.URL{Path: subPath, RawQuery: r.URL.RawQuery}
							rawURL = baseParsed.ResolveReference(relURL).String()
						}
					}
				}
			}
		}
	}
	if rawURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	targetURL, err := url.Parse(rawURL)
	if err != nil || (targetURL.Scheme != "http" && targetURL.Scheme != "https") {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", rawURL, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (SmartTV; HbbTV/1.4.1 (+PVR; DVB-T/T2;)) AppleWebkit/537.36")
	req.Header.Set("Accept", "*/*")

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   15 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to fetch HbbTV page: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")

	// 1. JavaScript Resources: rewrite navigations and ensure valid executable JS MIME type
	isJS := strings.Contains(strings.ToLower(ct), "javascript") || strings.HasSuffix(strings.ToLower(targetURL.Path), ".js")
	if isJS {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsStr := rewriteLocationNav(string(bodyBytes))

		w.Header().Set("Content-Type", "application/javascript; charset=UTF-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write([]byte(jsStr))
		return
	}

	// 2. CSS Stylesheets: ensure valid text/css MIME type and rewrite url(...)
	isCSS := strings.Contains(strings.ToLower(ct), "css") || strings.HasSuffix(strings.ToLower(targetURL.Path), ".css")
	if isCSS {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cssStr := string(bodyBytes)
		reCSSURL := regexp.MustCompile(`(?i)url\(\s*['"]?([^'"\)\s]+)['"]?\s*\)`)
		cssStr = reCSSURL.ReplaceAllStringFunc(cssStr, func(m string) string {
			parts := reCSSURL.FindStringSubmatch(m)
			if len(parts) >= 2 {
				raw := strings.TrimSpace(parts[1])
				if raw == "" || strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "/api/hbbtv/") {
					return m
				}
				var fullURL string
				if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
					fullURL = raw
				} else {
					if u, err := url.Parse(raw); err == nil {
						fullURL = targetURL.ResolveReference(u).String()
					} else {
						return m
					}
				}
				return fmt.Sprintf("url('/api/hbbtv/proxy?url=%s')", url.QueryEscape(fullURL))
			}
			return m
		})

		w.Header().Set("Content-Type", "text/css; charset=UTF-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write([]byte(cssStr))
		return
	}

	// 3. HTML / XHTML / HbbTV Document
	isHbbtv := strings.Contains(strings.ToLower(ct), "hbbtv") || strings.Contains(strings.ToLower(ct), "xhtml+xml")
	if isHbbtv || strings.Contains(strings.ToLower(ct), "text/html") || strings.Contains(strings.ToLower(ct), "xml") {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		bodyStr := string(bodyBytes)
		lowerBody := strings.ToLower(bodyStr)
		isActualHTML := strings.Contains(lowerBody, "<html") || strings.Contains(lowerBody, "<!doctype") || strings.Contains(lowerBody, "<head") || strings.Contains(lowerBody, "<body")
		if !isActualHTML {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write([]byte(bodyStr))
			return
		}

		// Replace plugin-triggering <object> and <embed> tags with <div> to prevent browser plugin alerts
		bodyStr = regexp.MustCompile(`(?i)<object\b`).ReplaceAllString(bodyStr, "<div")
		bodyStr = regexp.MustCompile(`(?i)</object>`).ReplaceAllString(bodyStr, "</div>")
		bodyStr = regexp.MustCompile(`(?i)<embed\b`).ReplaceAllString(bodyStr, "<div")
		bodyStr = regexp.MustCompile(`(?i)</embed>`).ReplaceAllString(bodyStr, "</div>")

		baseURL := targetURL.Scheme + "://" + targetURL.Host + "/"
		if idx := strings.LastIndex(targetURL.Path, "/"); idx != -1 {
			baseURL = targetURL.Scheme + "://" + targetURL.Host + targetURL.Path[:idx+1]
		}
		baseParsed, _ := url.Parse(baseURL)

		resolveToProxy := func(raw string) string {
			raw = strings.TrimSpace(raw)
			if raw == "" || strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "javascript:") || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "/api/hbbtv/") {
				return raw
			}
			var fullURL string
			if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
				fullURL = raw
			} else if baseParsed != nil {
				if u, err := url.Parse(raw); err == nil {
					fullURL = baseParsed.ResolveReference(u).String()
				} else {
					return raw
				}
			} else {
				return raw
			}
			return "/api/hbbtv/proxy?url=" + url.QueryEscape(fullURL)
		}

		// Rewrite static assets in HTML to route through our proxy
		reScript := regexp.MustCompile(`(?i)(<script\b[^>]*?\bsrc\s*=\s*["'])([^"']+)(["'])`)
		bodyStr = reScript.ReplaceAllStringFunc(bodyStr, func(m string) string {
			parts := reScript.FindStringSubmatch(m)
			if len(parts) == 4 {
				return parts[1] + resolveToProxy(parts[2]) + parts[3]
			}
			return m
		})

		reLink := regexp.MustCompile(`(?i)(<link\b[^>]*?\bhref\s*=\s*["'])([^"']+)(["'])`)
		bodyStr = reLink.ReplaceAllStringFunc(bodyStr, func(m string) string {
			parts := reLink.FindStringSubmatch(m)
			if len(parts) == 4 {
				return parts[1] + resolveToProxy(parts[2]) + parts[3]
			}
			return m
		})

		reImg := regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc\s*=\s*["'])([^"']+)(["'])`)
		bodyStr = reImg.ReplaceAllStringFunc(bodyStr, func(m string) string {
			parts := reImg.FindStringSubmatch(m)
			if len(parts) == 4 {
				return parts[1] + resolveToProxy(parts[2]) + parts[3]
			}
			return m
		})

		// Rewrite url(...) in inline CSS or <style> tags
		reStyleURL := regexp.MustCompile(`(?i)url\(\s*['"]?([^'"\)\s]+)['"]?\s*\)`)
		bodyStr = reStyleURL.ReplaceAllStringFunc(bodyStr, func(m string) string {
			parts := reStyleURL.FindStringSubmatch(m)
			if len(parts) >= 2 {
				raw := strings.TrimSpace(parts[1])
				if raw == "" || strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "/api/hbbtv/") {
					return m
				}
				var fullURL string
				if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
					fullURL = raw
				} else if baseParsed != nil {
					if u, err := url.Parse(raw); err == nil {
						fullURL = baseParsed.ResolveReference(u).String()
					} else {
						return m
					}
				} else {
					return m
				}
				return fmt.Sprintf("url('/api/hbbtv/proxy?url=%s')", url.QueryEscape(fullURL))
			}
			return m
		})

		// Rewrite inline navigations in HTML
		bodyStr = rewriteLocationNav(bodyStr)

		polyfill := fmt.Sprintf(`
<style>
html, body {
  margin: 0 !important;
  padding: 0 !important;
  width: 100%% !important;
  height: 100%% !important;
  overflow: hidden !important;
  background-color: transparent !important;
}
div#tvbild, div[data-type="video/broadcast"], div[type="video/broadcast"], div#playerHolder {
  background: transparent !important;
}
</style>
<script>
window.__hbbtvBase = %q;
window.__proxyNav = function(u) {
  if (!u) return;
  if (typeof u === 'string' && u.startsWith('/api/hbbtv/')) {
    window.location.href = u;
    return;
  }
  var resolved = u;
  try {
    resolved = new URL(u, window.__hbbtvBase).href;
  } catch(e) {}
  window.location.href = '/api/hbbtv/proxy?url=' + encodeURIComponent(resolved);
};

window.VK_RED = 403;
window.VK_GREEN = 404;
window.VK_YELLOW = 405;
window.VK_BLUE = 406;
window.VK_ENTER = 13;
window.VK_BACK = 461;
window.VK_LEFT = 37;
window.VK_UP = 38;
window.VK_RIGHT = 39;
window.VK_DOWN = 40;
if (!window.KeyEvent) {
  window.KeyEvent = {
    VK_RED: 403, VK_GREEN: 404, VK_YELLOW: 405, VK_BLUE: 406,
    VK_ENTER: 13, VK_BACK: 461, VK_LEFT: 37, VK_UP: 38, VK_RIGHT: 39, VK_DOWN: 40,
    VK_0: 48, VK_1: 49, VK_2: 50, VK_3: 51, VK_4: 52,
    VK_5: 53, VK_6: 54, VK_7: 55, VK_8: 56, VK_9: 57
  };
}

(function() {
  function shouldProxy(u) {
    if (!u || typeof u !== 'string') return false;
    if (u.startsWith('data:') || u.startsWith('blob:') || u.startsWith('javascript:') || u.startsWith('#')) return false;
    if (u.startsWith('/api/hbbtv/') || u.startsWith(window.location.origin) || u.indexOf('/api/hbbtv/proxy') !== -1) return false;
    return true;
  }

  function toProxyUrl(u) {
    if (!shouldProxy(u)) return u;
    try {
      var resolved = new URL(u, window.__hbbtvBase).href;
      return '/api/hbbtv/proxy?url=' + encodeURIComponent(resolved);
    } catch(e) {
      return u;
    }
  }

  var origOpen = window.XMLHttpRequest.prototype.open;
  window.XMLHttpRequest.prototype.open = function(method, u, async, user, password) {
    u = toProxyUrl(u);
    return origOpen.call(this, method, u, async !== false, user, password);
  };

  if (window.fetch) {
    var origFetch = window.fetch;
    window.fetch = function(input, init) {
      if (typeof input === 'string') {
        input = toProxyUrl(input);
      }
      return origFetch.call(this, input, init);
    };
  }

  try {
    var origImgSrcDesc = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'src');
    if (origImgSrcDesc && origImgSrcDesc.set) {
      Object.defineProperty(HTMLImageElement.prototype, 'src', {
        get: function() { return origImgSrcDesc.get.call(this); },
        set: function(val) {
          val = toProxyUrl(val);
          return origImgSrcDesc.set.call(this, val);
        }
      });
    }
  } catch(e) {}

  try {
    var origScriptSrcDesc = Object.getOwnPropertyDescriptor(HTMLScriptElement.prototype, 'src');
    if (origScriptSrcDesc && origScriptSrcDesc.set) {
      Object.defineProperty(HTMLScriptElement.prototype, 'src', {
        get: function() { return origScriptSrcDesc.get.call(this); },
        set: function(val) {
          val = toProxyUrl(val);
          return origScriptSrcDesc.set.call(this, val);
        }
      });
    }
  } catch(e) {}

  try {
    var origLinkHrefDesc = Object.getOwnPropertyDescriptor(HTMLLinkElement.prototype, 'href');
    if (origLinkHrefDesc && origLinkHrefDesc.set) {
      Object.defineProperty(HTMLLinkElement.prototype, 'href', {
        get: function() { return origLinkHrefDesc.get.call(this); },
        set: function(val) {
          val = toProxyUrl(val);
          return origLinkHrefDesc.set.call(this, val);
        }
      });
    }
  } catch(e) {}

  if (navigator.sendBeacon) {
    var origSendBeacon = navigator.sendBeacon.bind(navigator);
    navigator.sendBeacon = function(url, data) {
      url = toProxyUrl(url);
      return origSendBeacon(url, data);
    };
  }

  var origGetElementsByTagName = document.getElementsByTagName.bind(document);
  document.getElementsByTagName = function(tag) {
    var res = origGetElementsByTagName(tag);
    if (tag && tag.toLowerCase() === 'object') {
      var arr = Array.prototype.slice.call(res);
      var mocks = Array.prototype.slice.call(document.querySelectorAll('div[data-type], div[type*="oipf"], div[type*="broadcast"], #appmgr, #appMan, #oipfcfg, #appConf, #mybroadcast, #oipfAppMan, #oipfConfig'));
      mocks.forEach(function(m) {
        if (arr.indexOf(m) === -1) arr.push(m);
      });
      arr.item = function(idx) { return arr[idx] || null; };
      arr.namedItem = function(name) { return document.getElementById(name) || null; };
      return arr;
    }
    return res;
  };

  document.addEventListener('click', function(e) {
    var a = e.target && e.target.closest ? e.target.closest('a') : null;
    if (a && a.href && !a.href.startsWith('javascript:')) {
      e.preventDefault();
      window.__proxyNav(a.getAttribute('href') || a.href);
    }
  }, true);
})();

var mockKeyset = {
  setValue: function() {},
  value: 255,
  RED: 1,
  GREEN: 2,
  YELLOW: 4,
  BLUE: 8,
  NAVIGATION: 16,
  VCR: 32,
  NUMERIC: 256
};
var mockApp = {
  show: function() {},
  hide: function() {},
  activate: function() {},
  activateInput: function() {},
  createApplication: function(uri, createChild) {
    if (uri) {
      window.__proxyNav(uri);
    }
    return mockApp;
  },
  destroyApplication: function() {},
  privateData: {
    keyset: mockKeyset
  }
};
var mockAppMan = {
  getOwnerApplication: function() { return mockApp; }
};
var mockConfig = {
  configuration: {},
  keyset: mockKeyset,
  localSystem: {
    deviceID: 'smarttv-dvb-001',
    modelName: 'SmartTV',
    vendorName: 'Antigravity'
  }
};
var mockBc = {
  bindToCurrentChannel: function() {},
  setFullScreen: function() {},
  release: function() {},
  stop: function() {},
  play: function() { return 0; },
  currentChannel: { name: 'DVB Broadcast', sid: 101 },
  getChannelConfig: function() {
    return {
      channelList: {
        getChannelByTriplet: function() { return {}; }
      }
    };
  }
};

window.oipfApplicationManager = function() { return mockAppMan; };
window.oipfConfiguration = function() { return mockConfig; };

function initHbbtvMocks() {
  document.querySelectorAll('#appmgr, #appMan, #oipfAppMan, [type*="ApplicationManager"], [data-type*="ApplicationManager"]').forEach(function(el) {
    Object.assign(el, mockAppMan);
    el.getOwnerApplication = function() { return mockApp; };
  });
  document.querySelectorAll('#oipfcfg, #appConf, #oipfConfig, [type*="Configuration"], [data-type*="Configuration"]').forEach(function(el) {
    Object.assign(el, mockConfig);
  });
  document.querySelectorAll('#mybroadcast, #tvbild, [type*="broadcast"], [data-type*="broadcast"]').forEach(function(el) {
    Object.assign(el, mockBc);
  });
  document.querySelectorAll('#redbutton, #RedButton, [id*="redbutton" i], [class*="redbutton" i], [id*="RedButton"]').forEach(function(el) {
    el.style.cursor = 'pointer';
    el.onclick = function(e) {
      if (e) e.stopPropagation();
      window.sendHbbtvKey(403);
    };
  });
}

function fitHbbTV() {
  var w = window.innerWidth || document.documentElement.clientWidth || 1280;
  var h = window.innerHeight || document.documentElement.clientHeight || 720;
  var scale = Math.min(w / 1280, h / 720);
  if (scale <= 0) scale = 1;
  if (document.body) {
    document.body.style.setProperty('width', '1280px', 'important');
    document.body.style.setProperty('height', '720px', 'important');
    document.body.style.setProperty('transform-origin', 'top left', 'important');
    document.body.style.setProperty('transform', 'scale(' + scale + ')', 'important');
    document.body.style.setProperty('position', 'absolute', 'important');
    document.body.style.setProperty('left', Math.max(0, (w - 1280 * scale) / 2) + 'px', 'important');
    document.body.style.setProperty('top', Math.max(0, (h - 720 * scale) / 2) + 'px', 'important');
    document.body.style.setProperty('overflow', 'hidden', 'important');
  }
}

window.sendHbbtvKey = function(code) {
  var codes = [code];
  if (code === 403) codes.push(116);
  if (code === 404) codes.push(117);
  if (code === 405) codes.push(118);
  if (code === 406) codes.push(119);

  codes.forEach(function(k) {
    ['keydown', 'keyup'].forEach(function(evtType) {
      try {
        var evt = new KeyboardEvent(evtType, {
          bubbles: true,
          cancelable: true,
          keyCode: k,
          which: k,
          view: window
        });
        Object.defineProperty(evt, 'keyCode', { get: function() { return k; } });
        Object.defineProperty(evt, 'which', { get: function() { return k; } });
        document.dispatchEvent(evt);
        window.dispatchEvent(evt);
        if (document.body) document.body.dispatchEvent(evt);
        if (document.activeElement && document.activeElement !== document.body) {
          document.activeElement.dispatchEvent(evt);
        }
      } catch(e) {}

      try {
        var legacyEvt = document.createEvent('Events');
        legacyEvt.initEvent(evtType, true, true);
        legacyEvt.keyCode = k;
        legacyEvt.which = k;
        Object.defineProperty(legacyEvt, 'keyCode', { get: function() { return k; } });
        Object.defineProperty(legacyEvt, 'which', { get: function() { return k; } });
        document.dispatchEvent(legacyEvt);
        window.dispatchEvent(legacyEvt);
      } catch(e) {}
    });
  });
};

window.addEventListener('keydown', function(e) {
  var code = e.keyCode || e.which;
  if (code === 82 || code === 49) {
    window.sendHbbtvKey(403);
  } else if (code === 71 || code === 50) {
    window.sendHbbtvKey(404);
  } else if (code === 89 || code === 51) {
    window.sendHbbtvKey(405);
  } else if (code === 66 || code === 52) {
    window.sendHbbtvKey(406);
  }
});

window.addEventListener('resize', fitHbbTV);
document.addEventListener('DOMContentLoaded', function() {
  initHbbtvMocks();
  fitHbbTV();
});
window.addEventListener('load', function() {
  initHbbtvMocks();
  fitHbbTV();
});
setTimeout(function() { initHbbtvMocks(); fitHbbTV(); }, 80);
setTimeout(function() { initHbbtvMocks(); fitHbbTV(); }, 400);
</script>`, baseURL)

		// Inject polyfill at the very top of <head> so it takes effect before any external scripts/links
		headIdx := -1
		lowerBody = strings.ToLower(bodyStr)
		if idx := strings.Index(lowerBody, "<head>"); idx != -1 {
			headIdx = idx + 6
		} else if idx := strings.Index(lowerBody, "<head "); idx != -1 {
			if endHead := strings.Index(lowerBody[idx:], ">"); endHead != -1 {
				headIdx = idx + endHead + 1
			}
		}

		if headIdx != -1 {
			bodyStr = bodyStr[:headIdx] + polyfill + bodyStr[headIdx:]
		} else if idx := strings.Index(lowerBody, "</head>"); idx != -1 {
			bodyStr = bodyStr[:idx] + polyfill + bodyStr[idx:]
		} else if idx := strings.Index(lowerBody, "<body"); idx != -1 {
			if endBody := strings.Index(lowerBody[idx:], ">"); endBody != -1 {
				bodyIdx := idx + endBody + 1
				bodyStr = bodyStr[:bodyIdx] + polyfill + bodyStr[bodyIdx:]
			} else {
				bodyStr = polyfill + bodyStr
			}
		} else {
			bodyStr = polyfill + bodyStr
		}

		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write([]byte(bodyStr))
		return
	}

	lowerPath := strings.ToLower(targetURL.Path)
	if strings.HasSuffix(lowerPath, ".otf") {
		ct = "font/otf"
	} else if strings.HasSuffix(lowerPath, ".ttf") {
		ct = "font/ttf"
	} else if strings.HasSuffix(lowerPath, ".woff") {
		ct = "font/woff"
	} else if strings.HasSuffix(lowerPath, ".woff2") {
		ct = "font/woff2"
	} else if strings.HasSuffix(lowerPath, ".png") {
		ct = "image/png"
	} else if strings.HasSuffix(lowerPath, ".jpg") || strings.HasSuffix(lowerPath, ".jpeg") {
		ct = "image/jpeg"
	} else if strings.HasSuffix(lowerPath, ".gif") {
		ct = "image/gif"
	} else if strings.HasSuffix(lowerPath, ".svg") {
		ct = "image/svg+xml"
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
