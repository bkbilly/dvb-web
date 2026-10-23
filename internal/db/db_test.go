package db_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dvb-web/internal/db"
)

func TestRecordingsCRUD(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dvb-db-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	ch := &db.Channel{
		Name:      "Test Channel",
		Number:    1,
		Frequency: 506000000,
		Enabled:   true,
	}
	if err := database.SaveChannel(ch); err != nil {
		t.Fatalf("SaveChannel error: %v", err)
	}

	now := time.Now()
	rec := &db.Recording{
		ChannelID:   ch.ID,
		ChannelName: ch.Name,
		Title:       "Test Movie",
		Description: "A great movie description",
		StartTime:   now.Add(-10 * time.Minute),
		EndTime:     now.Add(50 * time.Minute),
		Duration:    3600,
		Status:      "scheduled",
		CreatedAt:   now,
	}

	if err := database.CreateRecording(rec); err != nil {
		t.Fatalf("CreateRecording error: %v", err)
	}
	if rec.ID == 0 {
		t.Fatalf("Expected non-zero ID, got %d", rec.ID)
	}

	fetched, err := database.GetRecording(rec.ID)
	if err != nil {
		t.Fatalf("GetRecording error: %v", err)
	}
	if fetched.Title != rec.Title {
		t.Errorf("Expected title %s, got %s", rec.Title, fetched.Title)
	}

	// Test ListDueScheduledRecordings
	due, err := database.ListDueScheduledRecordings(now)
	if err != nil {
		t.Fatalf("ListDueScheduledRecordings error: %v", err)
	}
	if len(due) != 1 {
		t.Errorf("Expected 1 due recording, got %d", len(due))
	}

	// Update status and file size
	rec.Status = "completed"
	rec.FileSize = 1048576
	rec.FilePath = filepath.Join(tmpDir, "recordings", "test.ts")
	if err := database.UpdateRecording(rec); err != nil {
		t.Fatalf("UpdateRecording error: %v", err)
	}

	all, err := database.ListRecordings()
	if err != nil {
		t.Fatalf("ListRecordings error: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("Expected 1 recording in list, got %d", len(all))
	}
	if all[0].Status != "completed" || all[0].FileSize != 1048576 {
		t.Errorf("Recording update mismatch: status=%s, size=%d", all[0].Status, all[0].FileSize)
	}

	// Delete
	if err := database.DeleteRecording(rec.ID); err != nil {
		t.Fatalf("DeleteRecording error: %v", err)
	}
	afterDel, err := database.ListRecordings()
	if err != nil {
		t.Fatalf("ListRecordings after delete error: %v", err)
	}
	if len(afterDel) != 0 {
		t.Errorf("Expected 0 recordings after delete, got %d", len(afterDel))
	}
}
