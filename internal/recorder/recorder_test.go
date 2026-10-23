package recorder_test

import (
	"os"
	"testing"
	"time"

	"dvb-web/internal/db"
	"dvb-web/internal/recorder"
	"dvb-web/internal/transcoder"
	"dvb-web/internal/tuner"
)

func TestRecorderInstantAndStop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dvb-rec-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}
	defer database.Close()

	ch := &db.Channel{
		Name:           "BBC One Test",
		Number:         1,
		Frequency:      506000000,
		Bandwidth:      8000000,
		DeliverySystem: "DVBT2",
		ServiceID:      101,
		VideoPID:       1011,
		AudioPID:       1012,
		Enabled:        true,
	}
	if err := database.SaveChannel(ch); err != nil {
		t.Fatalf("SaveChannel error: %v", err)
	}

	detector := transcoder.NewDetector()
	pool := tuner.NewPool(database, detector, nil, true) // mock mode
	defer pool.StopAll()

	rec := recorder.New(database, pool, tmpDir)
	defer rec.Stop()

	// Start instant recording for 5 seconds
	recording, err := rec.StartInstantRecording(ch.ID, "Test Instant Show", 5)
	if err != nil {
		t.Fatalf("StartInstantRecording failed: %v", err)
	}
	if recording.ID == 0 {
		t.Fatalf("Expected non-zero recording ID")
	}

	// Verify active jobs
	activeJobs := rec.GetActiveJobs()
	if len(activeJobs) != 1 {
		t.Errorf("Expected 1 active job, got %d", len(activeJobs))
	}

	// Check IsRecordingChannel
	isRec, r := rec.IsRecordingChannel(ch.ID)
	if !isRec || r == nil {
		t.Errorf("Expected IsRecordingChannel to be true")
	}

	// Let it capture some data for ~1 second
	time.Sleep(1 * time.Second)

	// Stop recording
	if err := rec.StopRecording(recording.ID); err != nil {
		t.Fatalf("StopRecording failed: %v", err)
	}

	// Wait briefly for job finalization
	time.Sleep(100 * time.Millisecond)

	// Check updated DB record
	updated, err := database.GetRecording(recording.ID)
	if err != nil {
		t.Fatalf("GetRecording failed: %v", err)
	}
	if updated.Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", updated.Status)
	}
	if updated.FilePath == "" {
		t.Errorf("Expected non-empty FilePath")
	}
	if _, err := os.Stat(updated.FilePath); os.IsNotExist(err) {
		t.Errorf("Expected recording file to exist at %s", updated.FilePath)
	}
}

func TestRecorderTunerConflictWhenAllTunersBusy(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dvb-rec-conflict-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}
	defer database.Close()

	ch1 := &db.Channel{
		Name:      "Channel 1",
		Number:    1,
		Frequency: 506000000,
		Enabled:   true,
	}
	ch2 := &db.Channel{
		Name:      "Channel 2",
		Number:    2,
		Frequency: 538000000, // Different frequency
		Enabled:   true,
	}
	_ = database.SaveChannel(ch1)
	_ = database.SaveChannel(ch2)

	detector := transcoder.NewDetector()
	pool := tuner.NewPool(database, detector, nil, true) // mock mode has 1 tuner
	defer pool.StopAll()

	rec := recorder.New(database, pool, tmpDir)
	defer rec.Stop()

	// Start recording ch1 on the only tuner
	_, err = rec.StartInstantRecording(ch1.ID, "Rec Ch1", 300)
	if err != nil {
		t.Fatalf("StartInstantRecording failed: %v", err)
	}

	// Now try to allocate another channel on different frequency
	viewerSession := &tuner.ViewerSession{
		ID:        "viewer-test",
		IP:        "127.0.0.1",
		ChannelID: ch2.ID,
	}
	_, err = pool.AllocateChannel(ch2, viewerSession)
	if err == nil {
		t.Fatalf("Expected conflict error, got nil")
	}

	conflict, ok := err.(*tuner.TunerConflictError)
	if !ok {
		t.Fatalf("Expected *tuner.TunerConflictError, got %T: %v", err, err)
	}
	if !conflict.HasActiveRecording {
		t.Errorf("Expected conflict.HasActiveRecording to be true")
	}
	if len(conflict.ActiveRecordings) != 1 {
		t.Errorf("Expected 1 active recording in conflict info, got %d", len(conflict.ActiveRecordings))
	}
}
