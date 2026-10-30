package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
		case "/ram/start.css":
			w.Header().Set("Content-Type", "text/css")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`body { background: transparent; } .icon { background-image: url('../img/logo.png'); }`))
		case "/ram/keycodes-min.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`var VK_RED = 403; document.location.href = "http://example.com/portal"; var res = t?document.location.href=t:window.close();`))
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
}
