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

func TestSearchEPG(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dvb-db-search-*")
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
		Name:      "BBC News HD",
		Number:    101,
		Frequency: 506000000,
		Enabled:   true,
	}
	if err := database.SaveChannel(ch); err != nil {
		t.Fatalf("SaveChannel error: %v", err)
	}

	now := time.Now().UTC()
	events := []db.EPGEvent{
		{
			ChannelID:    ch.ID,
			EventID:      1001,
			StartTime:    now.Add(1 * time.Hour),
			EndTime:      now.Add(2 * time.Hour),
			Titles:       map[string]string{"eng": "World News Today", "gre": "Παγκόσμια Νέα"},
			Descriptions: map[string]string{"eng": "Global headlines and in-depth analysis"},
			Genres:       []string{"News"},
		},
		{
			ChannelID:    ch.ID,
			EventID:      1002,
			StartTime:    now.Add(3 * time.Hour),
			EndTime:      now.Add(5 * time.Hour),
			Titles:       map[string]string{"eng": "Blockbuster Movie Night", "gre": "Βραδιά Ταινίας"},
			Descriptions: map[string]string{"eng": "An action-packed adventure thriller"},
			Genres:       []string{"Movie"},
		},
	}

	if err := database.SaveEPGEvents(events); err != nil {
		t.Fatalf("SaveEPGEvents error: %v", err)
	}

	// 1. Search by English title
	res, err := database.SearchEPG("World News", 10)
	if err != nil {
		t.Fatalf("SearchEPG error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("Expected 1 result for 'World News', got %d", len(res))
	}
	if res[0].ChannelName != "BBC News HD" || res[0].ChannelNumber != 101 {
		t.Errorf("Channel info mismatch: %+v", res[0])
	}
	if res[0].Titles["eng"] != "World News Today" {
		t.Errorf("Expected title 'World News Today', got %s", res[0].Titles["eng"])
	}

	// 2. Search by Greek title (exact with tone)
	resGreek, err := database.SearchEPG("Ταινίας", 10)
	if err != nil {
		t.Fatalf("SearchEPG Greek error: %v", err)
	}
	if len(resGreek) != 1 {
		t.Fatalf("Expected 1 result for 'Ταινίας', got %d", len(resGreek))
	}
	if resGreek[0].Titles["eng"] != "Blockbuster Movie Night" {
		t.Errorf("Expected movie title, got %s", resGreek[0].Titles["eng"])
	}

	// 2b. Search by Greek title without accents/tones (case-insensitive & tone-insensitive)
	resGreekNoTone, err := database.SearchEPG("ταινιας", 10)
	if err != nil {
		t.Fatalf("SearchEPG Greek no-tone error: %v", err)
	}
	if len(resGreekNoTone) != 1 {
		t.Fatalf("Expected 1 result for 'ταινιας' (no tone), got %d", len(resGreekNoTone))
	}

	// 2c. Search with uppercase and without tones
	resGreekUpper, err := database.SearchEPG("ΤΑΙΝΙΑ", 10)
	if err != nil {
		t.Fatalf("SearchEPG Greek uppercase error: %v", err)
	}
	if len(resGreekUpper) != 1 {
		t.Fatalf("Expected 1 result for 'ΤΑΙΝΙΑ', got %d", len(resGreekUpper))
	}

	// 2d. Search with multiple tokens across title and channel
	resMultiToken, err := database.SearchEPG("bbc παγκοσμια", 10)
	if err != nil {
		t.Fatalf("SearchEPG multi-token error: %v", err)
	}
	if len(resMultiToken) != 1 {
		t.Fatalf("Expected 1 result for 'bbc παγκοσμια', got %d", len(resMultiToken))
	}

	// 3. Search by description keyword
	resDesc, err := database.SearchEPG("thriller", 10)
	if err != nil {
		t.Fatalf("SearchEPG description error: %v", err)
	}
	if len(resDesc) != 1 {
		t.Fatalf("Expected 1 result for 'thriller', got %d", len(resDesc))
	}

	// 4. Search for non-existent keyword
	resNone, err := database.SearchEPG("NonExistentShowXYZ", 10)
	if err != nil {
		t.Fatalf("SearchEPG non-existent error: %v", err)
	}
	if len(resNone) != 0 {
		t.Fatalf("Expected 0 results, got %d", len(resNone))
	}
}
