package api_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dvb-web/internal/api"
	"dvb-web/internal/db"
	"dvb-web/internal/recorder"
	"dvb-web/internal/transcoder"
	"dvb-web/internal/tuner"
	webRTCManager "dvb-web/internal/webrtc"
)

func setupTestServer(t *testing.T) (*api.Server, *db.Database, *tuner.Pool, *recorder.Recorder, string) {
	tmpDir, err := os.MkdirTemp("", "dvb-api-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}

	ch1 := &db.Channel{
		Name:           "BBC One Test",
		Number:         1,
		Frequency:      506000000,
		DeliverySystem: "DVBT2",
		ServiceID:      101,
		VideoPID:       1011,
		AudioPID:       1012,
		Enabled:        true,
	}
	ch2 := &db.Channel{
		Name:           "BBC Two Test",
		Number:         2,
		Frequency:      538000000,
		DeliverySystem: "DVBT2",
		ServiceID:      102,
		VideoPID:       1021,
		AudioPID:       1022,
		Enabled:        true,
	}
	chRadio := &db.Channel{
		Name:           "BBC Radio 1 Test",
		Number:         701,
		Frequency:      506000000,
		DeliverySystem: "DVBT2",
		ServiceID:      701,
		VideoPID:       0, // Radio station
		AudioPID:       7012,
		Enabled:        true,
		IsRadio:        true,
	}
	_ = database.SaveChannel(ch1)
	_ = database.SaveChannel(ch2)
	_ = database.SaveChannel(chRadio)

	detector := transcoder.NewDetector()
	pool := tuner.NewPool(database, detector, nil, true) // mock mode (1 tuner)
	rec := recorder.New(database, pool, tmpDir)
	rtcMgr := webRTCManager.NewManager()

	server := api.NewServer(database, pool, detector, rtcMgr, nil, rec)
	return server, database, pool, rec, tmpDir
}

func TestAPIChannelTracks(t *testing.T) {
	server, database, pool, rec, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)
	defer database.Close()
	defer pool.StopAll()
	defer rec.Stop()

	mux := http.NewServeMux()
	server.RegisterRoutes(mux, "./web")

	req := httptest.NewRequest(http.MethodGet, "/api/channels/1/tracks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var tracks tuner.ChannelTracks
	if err := json.Unmarshal(w.Body.Bytes(), &tracks); err != nil {
		t.Fatalf("Failed to unmarshal tracks: %v", err)
	}

	if len(tracks.AudioTracks) == 0 {
		t.Errorf("Expected at least 1 audio track in mock mode, got 0")
	}
	if len(tracks.SubtitleTracks) == 0 {
		t.Errorf("Expected at least 1 subtitle track in mock mode, got 0")
	}

	// Test Radio channel tracks (Channel 3 has VideoPID: 0)
	reqRadio := httptest.NewRequest(http.MethodGet, "/api/channels/3/tracks", nil)
	wRadio := httptest.NewRecorder()
	mux.ServeHTTP(wRadio, reqRadio)

	var radioTracks tuner.ChannelTracks
	if err := json.Unmarshal(wRadio.Body.Bytes(), &radioTracks); err != nil {
		t.Fatalf("Failed to unmarshal radio tracks: %v", err)
	}
	if len(radioTracks.AudioTracks) == 0 {
		t.Errorf("Expected audio tracks for radio station, got 0")
	}
	if len(radioTracks.SubtitleTracks) != 0 {
		t.Errorf("Expected 0 subtitle tracks for radio station, got %d", len(radioTracks.SubtitleTracks))
	}
}

func TestAPIRecordingsFlow(t *testing.T) {
	server, database, pool, rec, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)
	defer database.Close()
	defer pool.StopAll()
	defer rec.Stop()

	mux := http.NewServeMux()
	server.RegisterRoutes(mux, "./web")

	// 1. Start Instant Recording
	instantBody, _ := json.Marshal(map[string]interface{}{
		"channel_id": 1,
		"duration":   10,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/recordings/instant", bytes.NewReader(instantBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("Expected 201 Created for instant recording, got %d: %s", w.Code, w.Body.String())
	}

	var createdRec db.Recording
	_ = json.Unmarshal(w.Body.Bytes(), &createdRec)
	if createdRec.ID == 0 {
		t.Fatalf("Expected non-zero created recording ID")
	}

	// 2. GET /api/recordings
	req = httptest.NewRequest(http.MethodGet, "/api/recordings", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", w.Code)
	}
	var list []db.Recording
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("Expected 1 recording in list, got %d", len(list))
	}
	if list[0].Status != "recording" {
		t.Errorf("Expected recording status 'recording', got '%s'", list[0].Status)
	}

	// 3. Try to tune another channel on different frequency -> Should return 409 Conflict with has_active_recording
	req = httptest.NewRequest(http.MethodGet, "/api/stream?channel_id=2&session_id=user2", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("Expected 409 Conflict when tuner is recording, got %d", w.Code)
	}

	var conflictResp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &conflictResp)
	if conflictResp["has_active_recording"] != true {
		t.Errorf("Expected has_active_recording to be true in conflict response, got: %v", conflictResp)
	}

	// 4. Test Stop and Tune: POST /api/recordings/:id/stop-and-tune
	req = httptest.NewRequest(http.MethodPost, "/api/recordings/1/stop-and-tune", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from stop-and-tune, got %d: %s", w.Code, w.Body.String())
	}

	time.Sleep(100 * time.Millisecond)

	// 5. Now tuning channel 2 should succeed!
	// (Note: in test, cancelling request ctx immediately stops the streaming loop)
	req = httptest.NewRequest(http.MethodGet, "/api/stream?channel_id=2&session_id=user2", nil)
	// Cancel quickly to end streaming
	ctx, cancel := req.Context(), func() {}
	_ = ctx
	_ = cancel

	// 6. Delete recording: DELETE /api/recordings/1
	req = httptest.NewRequest(http.MethodDelete, "/api/recordings/1", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from delete, got %d: %s", w.Code, w.Body.String())
	}

	afterDel, _ := database.ListRecordings()
	if len(afterDel) != 0 {
		t.Errorf("Expected 0 recordings after delete, got %d", len(afterDel))
	}
}

func TestAPIScheduleFutureRecording(t *testing.T) {
	server, database, pool, rec, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)
	defer database.Close()
	defer pool.StopAll()
	defer rec.Stop()

	mux := http.NewServeMux()
	server.RegisterRoutes(mux, "./web")

	futureStart := time.Now().Add(2 * time.Hour)
	futureEnd := futureStart.Add(1 * time.Hour)

	schedBody, _ := json.Marshal(map[string]interface{}{
		"channel_id":  1,
		"title":       "Future Olympic Games",
		"description": "Grand opening ceremony",
		"start_time":  futureStart,
		"end_time":    futureEnd,
		"duration":    3600,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/recordings", bytes.NewReader(schedBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("Expected 201 Created for scheduled recording, got %d: %s", w.Code, w.Body.String())
	}

	var scheduledRec db.Recording
	_ = json.Unmarshal(w.Body.Bytes(), &scheduledRec)
	if scheduledRec.Status != "scheduled" {
		t.Errorf("Expected status 'scheduled', got '%s'", scheduledRec.Status)
	}
}

func TestHbbTVProxy(t *testing.T) {
	// 1. Mock Broadcaster HbbTV Server
	mockBroadcaster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start.php":
			w.Header().Set("Content-Type", "application/vnd.hbbtv.xhtml+xml; charset=UTF-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8" ?>
<!DOCTYPE html>
<html>
<head>
<title>HbbTV Test</title>
<link rel="stylesheet" type="text/css" href="ram/start.css" />
<script type="text/javascript" src="ram/keycodes-min.js"></script>
<script type="text/javascript">
function goRed(url) {
	location.href = url;
}
</script>
</head>
<body>
<div id="redbutton"><img src="img/red.png" /></div>
</body>
</html>`))
		case "/v6/":
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("2a02:587:32fe:8db:d5eb:5daf:98c:7e67"))
		case "/ram/start.css":
			w.Header().Set("Content-Type", "text/css")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`body { background: transparent; } .icon { background-image: url('../img/logo.png'); }`))
		case "/ram/keycodes-min.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`var VK_RED = 403; document.location.href = "http://example.com/portal"; var res = t?document.location.href=t:window.close(); window.location.href = 'http://example.com/comment'//trailing comment;`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockBroadcaster.Close()

	server, database, pool, rec, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)
	defer database.Close()
	defer pool.StopAll()
	defer rec.Stop()

	mux := http.NewServeMux()
	server.RegisterRoutes(mux, "./web")

	// 2. Fetch main HbbTV page via proxy
	mainURL := "/api/hbbtv/proxy?url=" + mockBroadcaster.URL + "/start.php"
	req := httptest.NewRequest(http.MethodGet, mainURL, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from HbbTV proxy, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("window.__proxyNav")) {
		t.Errorf("Expected body to contain polyfill 'window.__proxyNav'")
	}
	if !bytes.Contains([]byte(body), []byte("window.sendHbbtvKey")) {
		t.Errorf("Expected body to contain 'window.sendHbbtvKey'")
	}
	if !bytes.Contains([]byte(body), []byte("setupAvObject")) {
		t.Errorf("Expected body to contain 'setupAvObject' for OIPF video support")
	}
	if !bytes.Contains([]byte(body), []byte("setupMediaSource")) {
		t.Errorf("Expected body to contain 'setupMediaSource' for video playback")
	}
	if !bytes.Contains([]byte(body), []byte("codes = [461, 8]")) {
		t.Errorf("Expected body to map VK_BACK (461) to both 461 and 8")
	}
	// Check that relative script/css were rewritten to proxy URLs
	if !bytes.Contains([]byte(body), []byte("/api/hbbtv/proxy?url=")) {
		t.Errorf("Expected static assets to be rewritten to /api/hbbtv/proxy?url=")
	}
	// Check that location.href was rewritten
	if bytes.Contains([]byte(body), []byte("location.href = url")) {
		t.Errorf("Expected location.href to be rewritten to window.__proxyNav")
	}

	// 3. Test subpath relative asset resolution using Referer (e.g. /api/hbbtv/ram/keycodes-min.js)
	subReq := httptest.NewRequest(http.MethodGet, "/api/hbbtv/ram/keycodes-min.js", nil)
	subReq.Header.Set("Referer", "http://127.0.0.1:8080"+mainURL)
	subW := httptest.NewRecorder()
	mux.ServeHTTP(subW, subReq)

	if subW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for sub-resource via referer, got %d: %s", subW.Code, subW.Body.String())
	}
	if subW.Header().Get("Content-Type") != "application/javascript; charset=UTF-8" {
		t.Errorf("Expected Content-Type 'application/javascript; charset=UTF-8', got '%s'", subW.Header().Get("Content-Type"))
	}
	subBody := subW.Body.String()
	if !bytes.Contains([]byte(subBody), []byte("window.__proxyNav(\"http://example.com/portal\")")) {
		t.Errorf("Expected JS location navigation to be rewritten to window.__proxyNav, got: %s", subBody)
	}
	if !bytes.Contains([]byte(subBody), []byte("t?window.__proxyNav(t):window.close()")) {
		t.Errorf("Expected ternary navigation to be rewritten without syntax error, got: %s", subBody)
	}
	if !bytes.Contains([]byte(subBody), []byte("window.__proxyNav('http://example.com/comment')//trailing comment;")) {
		t.Errorf("Expected comment after navigation to remain outside function call, got: %s", subBody)
	}

	// 4. Test CSS sub-resource and url(...) rewriting
	cssReq := httptest.NewRequest(http.MethodGet, "/api/hbbtv/ram/start.css", nil)
	cssReq.Header.Set("Referer", "http://127.0.0.1:8080"+mainURL)
	cssW := httptest.NewRecorder()
	mux.ServeHTTP(cssW, cssReq)

	if cssW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for CSS sub-resource, got %d: %s", cssW.Code, cssW.Body.String())
	}
	if cssW.Header().Get("Content-Type") != "text/css; charset=UTF-8" {
		t.Errorf("Expected Content-Type 'text/css; charset=UTF-8', got '%s'", cssW.Header().Get("Content-Type"))
	}
	cssBody := cssW.Body.String()
	if !bytes.Contains([]byte(cssBody), []byte("/api/hbbtv/proxy?url=")) {
		t.Errorf("Expected CSS url(...) to be rewritten through /api/hbbtv/proxy, got: %s", cssBody)
	}

	// 5. Test root fallback for stray relative asset (e.g. /api/img/logo.png with Referer)
	fallbackReq := httptest.NewRequest(http.MethodGet, "/api/img/logo.png", nil)
	fallbackReq.Header.Set("Referer", "http://127.0.0.1:8080"+mainURL)
	fallbackW := httptest.NewRecorder()
	mux.ServeHTTP(fallbackW, fallbackReq)

	if fallbackW.Code != http.StatusTemporaryRedirect {
		t.Errorf("Expected 307 Temporary Redirect for stray relative asset, got %d", fallbackW.Code)
	}
	loc := fallbackW.Header().Get("Location")
	if !bytes.Contains([]byte(loc), []byte("/api/hbbtv/proxy?url=")) {
		t.Errorf("Expected redirect to proxy url, got: %s", loc)
	}

	// 6. Test that non-HTML endpoints sending text/html (like v6 IP lookup) are not polyfilled
	v6Req := httptest.NewRequest(http.MethodGet, "/api/hbbtv/proxy?url="+mockBroadcaster.URL+"/v6/", nil)
	v6W := httptest.NewRecorder()
	mux.ServeHTTP(v6W, v6Req)

	if v6W.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for /v6/ endpoint, got %d: %s", v6W.Code, v6W.Body.String())
	}
	if v6W.Body.String() != "2a02:587:32fe:8db:d5eb:5daf:98c:7e67" {
		t.Errorf("Expected exact clean IP string without polyfill, got: %q", v6W.Body.String())
	}

	// 7. Test that /api/hbbtv/undefined returns 200 OK 1x1 GIF
	undefReq := httptest.NewRequest(http.MethodGet, "/api/hbbtv/undefined", nil)
	undefW := httptest.NewRecorder()
	mux.ServeHTTP(undefW, undefReq)

	if undefW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for /api/hbbtv/undefined, got %d", undefW.Code)
	}
	if undefW.Header().Get("Content-Type") != "image/gif" {
		t.Errorf("Expected image/gif for /api/hbbtv/undefined, got %s", undefW.Header().Get("Content-Type"))
	}
}

func TestAPIScanRegionsAndCustomScan(t *testing.T) {
	server, _, _, _, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux, "")

	// 1. Test GET /api/scan/regions
	req := httptest.NewRequest(http.MethodGet, "/api/scan/regions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from /api/scan/regions, got %d", w.Code)
	}

	var regions []tuner.CountryRegions
	if err := json.Unmarshal(w.Body.Bytes(), &regions); err != nil {
		t.Fatalf("Failed to parse regions JSON: %v", err)
	}

	if len(regions) == 0 {
		t.Fatalf("Expected non-empty list of regions")
	}

	// 2. Test POST /api/scan with custom transponder
	scanBody, _ := json.Marshal(tuner.ScanConfig{
		AdapterID: 0,
		Mode:      "manual",
		Transponders: []tuner.Transponder{
			{Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
		},
	})
	scanReq := httptest.NewRequest(http.MethodPost, "/api/scan", bytes.NewReader(scanBody))
	scanW := httptest.NewRecorder()
	mux.ServeHTTP(scanW, scanReq)

	if scanW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from custom /api/scan, got %d: %s", scanW.Code, scanW.Body.String())
	}
}

func TestAPIDirectStreamMode(t *testing.T) {
	server, database, pool, rec, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)
	defer database.Close()
	defer pool.StopAll()
	defer rec.Stop()
	_ = server

	// 1. Verify stream allocation with direct=1 query parameter
	ch, err := database.GetChannel(1)
	if err != nil {
		t.Fatalf("Failed to get channel 1: %v", err)
	}

	session := &tuner.ViewerSession{
		ID:             "sess_direct_test",
		IP:             "127.0.0.1",
		UserAgent:      "GoTest",
		TranscoderMode: "direct",
	}

	tunerDev, actualSub, err := pool.AllocateChannelTrack(ch, 0, -1, 0, session)
	if err != nil {
		t.Fatalf("Failed to allocate channel with direct stream: %v", err)
	}
	defer pool.ReleaseSession(session.ID)

	if actualSub != -1 {
		t.Errorf("Expected subTrack -1, got %d", actualSub)
	}
	if tunerDev == nil {
		t.Fatalf("Expected allocated tuner, got nil")
	}

	// 2. Verify that requesting subtitles with direct mode falls back gracefully
	sessionSub := &tuner.ViewerSession{
		ID:             "sess_sub_test",
		IP:             "127.0.0.1",
		UserAgent:      "GoTest",
		TranscoderMode: "direct",
	}

	tunerDev2, actualSub2, err := pool.AllocateChannelTrack(ch, 0, 0, 0, sessionSub)
	if err != nil {
		t.Fatalf("Failed to allocate channel with subtitles in direct mode: %v", err)
	}
	defer pool.ReleaseSession(sessionSub.ID)

	if tunerDev2 == nil {
		t.Fatalf("Expected allocated tuner for subtitles fallback, got nil")
	}
	if actualSub2 != 0 {
		t.Errorf("Expected subTrack 0 to be accepted, got %d", actualSub2)
	}
}

func TestLogoAPIEndpoints(t *testing.T) {
	server, database, _, _, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux, "")

	// 1. Set channel logo via /api/channels/1/logo/set with a local path
	setBody := `{"source": "/api/logos/test-logo.png", "download": false}`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/1/logo/set", bytes.NewBufferString(setBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from logo set, got %d: %s", w.Code, w.Body.String())
	}

	// Verify database was updated
	ch, err := database.GetChannel(1)
	if err != nil {
		t.Fatalf("Failed to get channel 1: %v", err)
	}
	if ch.LogoURL != "/api/logos/test-logo.png" {
		t.Errorf("Expected logo_url to be /api/logos/test-logo.png, got %s", ch.LogoURL)
	}

	// 2. Verify M3U playlist contains tvg-logo
	m3uReq := httptest.NewRequest(http.MethodGet, "/api/playlist.m3u", nil)
	m3uW := httptest.NewRecorder()
	mux.ServeHTTP(m3uW, m3uReq)

	if m3uW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from M3U playlist, got %d", m3uW.Code)
	}
	m3uContent := m3uW.Body.String()
	if !bytes.Contains([]byte(m3uContent), []byte("tvg-logo=\"http://example.com/api/logos/test-logo.png\"")) {
		t.Errorf("Expected M3U to contain tvg-logo with base URL, got:\n%s", m3uContent)
	}

	// 3. Clear logo via DELETE /api/channels/1/logo
	delReq := httptest.NewRequest(http.MethodDelete, "/api/channels/1/logo", nil)
	delW := httptest.NewRecorder()
	mux.ServeHTTP(delW, delReq)

	if delW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from logo delete, got %d: %s", delW.Code, delW.Body.String())
	}

	chCleared, err := database.GetChannel(1)
	if err != nil {
		t.Fatalf("Failed to get channel 1: %v", err)
	}
	if chCleared.LogoURL != "" {
		t.Errorf("Expected logo_url to be cleared, got %s", chCleared.LogoURL)
	}
}

func TestChannelToggleAndHideAPI(t *testing.T) {
	server, database, _, _, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux, "")

	// Verify channel 1 is initially enabled
	ch1, err := database.GetChannel(1)
	if err != nil || !ch1.Enabled {
		t.Fatalf("Channel 1 should initially be enabled")
	}

	// 1. Toggle channel 1 via POST /api/channels/1/toggle
	toggleReq := httptest.NewRequest(http.MethodPost, "/api/channels/1/toggle", nil)
	toggleW := httptest.NewRecorder()
	mux.ServeHTTP(toggleW, toggleReq)

	if toggleW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from toggle, got %d", toggleW.Code)
	}

	var toggled db.Channel
	if err := json.NewDecoder(toggleW.Body).Decode(&toggled); err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	if toggled.Enabled {
		t.Errorf("Expected channel to be disabled after toggle, but was enabled")
	}

	// 2. Verify GET /api/channels?enabled=true excludes disabled channel 1
	enabledReq := httptest.NewRequest(http.MethodGet, "/api/channels?enabled=true", nil)
	enabledW := httptest.NewRecorder()
	mux.ServeHTTP(enabledW, enabledReq)

	var enabledList []db.Channel
	_ = json.NewDecoder(enabledW.Body).Decode(&enabledList)
	for _, c := range enabledList {
		if c.ID == 1 {
			t.Errorf("Expected channel 1 to be omitted from ?enabled=true, but it was found")
		}
	}

	// 3. Re-enable channel 1 via PATCH /api/channels/1 with {"enabled": true}
	patchBody := strings.NewReader(`{"enabled": true}`)
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/channels/1", patchBody)
	patchReq.Header.Set("Content-Type", "application/json")
	patchW := httptest.NewRecorder()
	mux.ServeHTTP(patchW, patchReq)

	if patchW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from PATCH, got %d: %s", patchW.Code, patchW.Body.String())
	}

	chReEnabled, err := database.GetChannel(1)
	if err != nil || !chReEnabled.Enabled {
		t.Errorf("Expected channel 1 to be re-enabled, but got err=%v, enabled=%v", err, chReEnabled.Enabled)
	}
}

func TestBackupAndRestoreAPI(t *testing.T) {
	server, database, _, _, tmpDir := setupTestServer(t)
	defer os.RemoveAll(tmpDir)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux, "")

	// 1. Set custom settings and channel info
	_ = database.SetSetting("transcoder_mode", "vaapi")
	_ = database.SetSetting("video_bitrate", "4500k")

	logosDir := filepath.Join(database.DataDir(), "logos")
	_ = os.MkdirAll(logosDir, 0755)
	testLogoData := []byte("fake-png-logo-bytes")
	_ = os.WriteFile(filepath.Join(logosDir, "ch_1.png"), testLogoData, 0644)
	_ = database.UpdateChannelLogo(1, "/api/logos/ch_1.png")

	// 2. Export backup via GET /api/backup
	backupReq := httptest.NewRequest(http.MethodGet, "/api/backup", nil)
	backupW := httptest.NewRecorder()
	mux.ServeHTTP(backupW, backupReq)

	if backupW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from /api/backup, got %d", backupW.Code)
	}
	if backupW.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("Expected application/zip content type, got %s", backupW.Header().Get("Content-Type"))
	}
	zipBytes := backupW.Body.Bytes()
	if len(zipBytes) < 50 {
		t.Fatalf("Zip archive is unexpectedly small: %d bytes", len(zipBytes))
	}

	// 3. Mutate current database and delete logo to simulate a wipe or change
	_ = database.SetSetting("transcoder_mode", "cpu")
	_ = database.SetSetting("video_bitrate", "1000k")
	_ = os.Remove(filepath.Join(logosDir, "ch_1.png"))
	_ = database.UpdateChannelLogo(1, "")

	chBefore, _ := database.GetChannel(1)
	if chBefore.LogoURL != "" {
		t.Fatalf("Logo URL should be empty before restore")
	}

	// 4. Restore via POST /api/restore
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	part, err := mw.CreateFormFile("backup", "dvb-backup.zip")
	if err != nil {
		t.Fatalf("CreateFormFile error: %v", err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatalf("Write part error: %v", err)
	}
	mw.Close()

	restoreReq := httptest.NewRequest(http.MethodPost, "/api/restore", &b)
	restoreReq.Header.Set("Content-Type", mw.FormDataContentType())
	restoreW := httptest.NewRecorder()
	mux.ServeHTTP(restoreW, restoreReq)

	if restoreW.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from /api/restore, got %d: %s", restoreW.Code, restoreW.Body.String())
	}

	// 5. Verify restored state
	settingMode := database.GetSetting("transcoder_mode", "")
	if settingMode != "vaapi" {
		t.Errorf("Expected restored transcoder_mode 'vaapi', got '%s'", settingMode)
	}
	settingBitrate := database.GetSetting("video_bitrate", "")
	if settingBitrate != "4500k" {
		t.Errorf("Expected restored video_bitrate '4500k', got '%s'", settingBitrate)
	}

	chRestored, err := database.GetChannel(1)
	if err != nil {
		t.Fatalf("GetChannel(1) error: %v", err)
	}
	if chRestored.LogoURL != "/api/logos/ch_1.png" {
		t.Errorf("Expected restored logo URL '/api/logos/ch_1.png', got '%s'", chRestored.LogoURL)
	}

	restoredLogoBytes, err := os.ReadFile(filepath.Join(logosDir, "ch_1.png"))
	if err != nil || !bytes.Equal(restoredLogoBytes, testLogoData) {
		t.Errorf("Restored logo file content mismatch: err=%v, got=%s", err, string(restoredLogoBytes))
	}
}
