package recorder

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"dvb-web/internal/db"
	"dvb-web/internal/tuner"
)

type ActiveJob struct {
	Recording    *db.Recording
	TunerID      int
	File         *os.File
	BytesWritten int64
	cancel       context.CancelFunc
	mu           sync.Mutex
}

type Recorder struct {
	db            *db.Database
	tunerPool     *tuner.Pool
	dataDir       string
	jobs          map[int64]*ActiveJob
	mu            sync.Mutex
	stopChan      chan struct{}
	onTunerChange func()
}

func (r *Recorder) GetDataDir() string {
	return r.dataDir
}

func (r *Recorder) SetOnTunerChange(cb func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onTunerChange = cb
}

func (r *Recorder) notifyTunerChange() {
	r.mu.Lock()
	cb := r.onTunerChange
	r.mu.Unlock()
	if cb != nil {
		go cb()
	}
}

func New(database *db.Database, pool *tuner.Pool, dataDir string) *Recorder {
	recDir := filepath.Join(dataDir, "recordings")
	_ = os.MkdirAll(recDir, 0755)

	r := &Recorder{
		db:        database,
		tunerPool: pool,
		dataDir:   dataDir,
		jobs:      make(map[int64]*ActiveJob),
		stopChan:  make(chan struct{}),
	}

	r.recoverDanglingRecordings()
	go r.schedulerLoop()
	return r
}

func (r *Recorder) recoverDanglingRecordings() {
	recs, err := r.db.ListRecordings()
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec.Status == "recording" {
			log.Printf("[Recorder] Recovering dangling recording #%d ('%s') left from previous session", rec.ID, rec.Title)
			rec.Status = "completed"
			rec.EndTime = time.Now()
			rec.Duration = int(rec.EndTime.Sub(rec.StartTime).Seconds())
			if rec.Duration < 0 {
				rec.Duration = 0
			}
			if rec.FilePath != "" {
				if fi, err := os.Stat(rec.FilePath); err == nil {
					rec.FileSize = fi.Size()
				}
			}
			_ = r.db.UpdateRecording(&rec)
		}
	}
}

func (r *Recorder) Stop() {
	close(r.stopChan)
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, job := range r.jobs {
		job.cancel()
		if job.File != nil {
			_ = job.File.Close()
		}
		job.Recording.Status = "completed"
		job.Recording.FileSize = job.BytesWritten
		_ = r.db.UpdateRecording(job.Recording)
		delete(r.jobs, id)
	}
}

func (r *Recorder) schedulerLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopChan:
			return
		case now := <-ticker.C:
			r.checkRecordings(now)
		}
	}
}

func (r *Recorder) checkRecordings(now time.Time) {
	// 1. Check for active recordings that reached their end_time
	r.mu.Lock()
	for id, job := range r.jobs {
		effEnd := job.Recording.EffectiveEndTime()
		if now.After(effEnd) || now.Equal(effEnd) {
			go r.finalizeJob(id, "completed", "")
		}
	}
	r.mu.Unlock()

	// 2. Check for scheduled recordings that should start now
	due, err := r.db.ListDueScheduledRecordings(now)
	if err != nil {
		log.Printf("[Recorder] Error listing due scheduled recordings: %v", err)
		return
	}

	for _, rec := range due {
		r.mu.Lock()
		_, active := r.jobs[rec.ID]
		r.mu.Unlock()
		if !active {
			recCopy := rec
			go func() {
				if err := r.startRecordingJob(&recCopy); err != nil {
					log.Printf("[Recorder] Failed to start scheduled recording #%d (%s): %v", recCopy.ID, recCopy.Title, err)
					recCopy.Status = "failed"
					recCopy.ErrorMessage = err.Error()
					_ = r.db.UpdateRecording(&recCopy)
				}
			}()
		}
	}
}

func slugify(s string) string {
	reg := regexp.MustCompile("[^a-zA-Z0-9]+")
	slug := strings.Trim(reg.ReplaceAllString(s, "_"), "_")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	if slug == "" {
		slug = "recording"
	}
	return slug
}

func (r *Recorder) startRecordingJob(rec *db.Recording) error {
	channel, err := r.db.GetChannel(rec.ChannelID)
	if err != nil {
		return fmt.Errorf("channel ID %d not found: %w", rec.ChannelID, err)
	}

	fileName := fmt.Sprintf("%d_%s_%s_%s.ts",
		rec.ID,
		slugify(channel.Name),
		slugify(rec.Title),
		rec.StartTime.Format("20060102_150405"),
	)
	filePath := filepath.Join(r.dataDir, "recordings", fileName)

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open output recording file: %w", err)
	}

	recSession := &tuner.ActiveRecordingInfo{
		RecordingID: rec.ID,
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		Title:       rec.Title,
		StartTime:   rec.StartTime,
		EndTime:     rec.EndTime,
		FilePath:    filePath,
	}

	t, err := r.tunerPool.AllocateRecording(channel, recSession)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(filePath)
		return fmt.Errorf("failed to allocate tuner for recording: %w", err)
	}

	sessionID := fmt.Sprintf("rec_%d", rec.ID)
	ch := t.Subscribe(channel.ID, sessionID)
	if ch == nil {
		_ = file.Close()
		_ = os.Remove(filePath)
		r.tunerPool.ReleaseRecording(t.ID, channel.ID, rec.ID)
		return fmt.Errorf("failed to subscribe to channel stream for recording")
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &ActiveJob{
		Recording: rec,
		TunerID:   t.ID,
		File:      file,
		cancel:    cancel,
	}

	r.mu.Lock()
	r.jobs[rec.ID] = job
	r.mu.Unlock()

	rec.Status = "recording"
	rec.FilePath = filePath
	_ = r.db.UpdateRecording(rec)

	log.Printf("[Recorder] 🔴 Started recording #%d: '%s' on %s -> %s (Tuner %d)",
		rec.ID, rec.Title, channel.Name, fileName, t.ID)
	r.notifyTunerChange()

	go func() {
		defer func() {
			r.finalizeJob(rec.ID, "completed", "")
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-ch:
				if !ok {
					return
				}
				n, err := file.Write(chunk)
				if err != nil {
					log.Printf("[Recorder] Error writing chunk for recording #%d: %v", rec.ID, err)
					return
				}
				job.mu.Lock()
				job.BytesWritten += int64(n)
				job.mu.Unlock()
			}
		}
	}()

	return nil
}

func (r *Recorder) finalizeJob(recID int64, targetStatus, errMsg string) {
	r.mu.Lock()
	job, exists := r.jobs[recID]
	if !exists {
		r.mu.Unlock()
		return
	}
	delete(r.jobs, recID)
	r.mu.Unlock()

	job.cancel()
	if job.File != nil {
		_ = job.File.Sync()
		_ = job.File.Close()
	}

	sessionID := fmt.Sprintf("rec_%d", recID)
	r.tunerPool.ReleaseStreamViewer(job.TunerID, job.Recording.ChannelID, sessionID)
	r.tunerPool.ReleaseRecording(job.TunerID, job.Recording.ChannelID, recID)

	job.Recording.Status = targetStatus
	job.Recording.FileSize = job.BytesWritten
	job.Recording.ErrorMessage = errMsg
	job.Recording.EndTime = time.Now()
	job.Recording.Duration = int(job.Recording.EndTime.Sub(job.Recording.StartTime).Seconds())
	if job.Recording.Duration < 0 {
		job.Recording.Duration = 0
	}
	_ = r.db.UpdateRecording(job.Recording)

	log.Printf("[Recorder] Finished recording #%d ('%s'): %d bytes written, status=%s",
		recID, job.Recording.Title, job.BytesWritten, targetStatus)
	r.notifyTunerChange()

	// Remux to faststart MP4 for instant browser playback
	if job.Recording.FilePath != "" && strings.HasSuffix(job.Recording.FilePath, ".ts") {
		mp4Path := strings.TrimSuffix(job.Recording.FilePath, ".ts") + ".mp4"
		go func(ts, mp4 string) {
			cmd := exec.Command("ffmpeg", "-i", ts, "-c", "copy", "-movflags", "+faststart", mp4, "-y")
			if err := cmd.Run(); err == nil {
				log.Printf("[Recorder] Remuxed recording #%d to faststart MP4 for browser playback: %s", recID, mp4)
			}
		}(job.Recording.FilePath, mp4Path)
	}
}

// StartInstantRecording triggers an immediate recording of the given channel
func (r *Recorder) StartInstantRecording(channelID int64, title string, durationSec int) (*db.Recording, error) {
	channel, err := r.db.GetChannel(channelID)
	if err != nil {
		return nil, fmt.Errorf("channel not found: %w", err)
	}

	if durationSec <= 0 {
		durationSec = 3600 // default 60 minutes
	}

	now := time.Now()
	if title == "" {
		if channel.NowPlaying != nil && len(channel.NowPlaying.Titles) > 0 {
			for _, t := range channel.NowPlaying.Titles {
				title = t
				break
			}
		}
		if title == "" {
			title = fmt.Sprintf("%s Live Recording", channel.Name)
		}
	}

	rec := &db.Recording{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		Title:       title,
		StartTime:   now,
		EndTime:     now.Add(time.Duration(durationSec) * time.Second),
		Duration:    durationSec,
		Status:      "scheduled",
		CreatedAt:   now,
	}

	if channel.NowPlaying != nil {
		rec.EventID = channel.NowPlaying.EventID
		if len(channel.NowPlaying.Descriptions) > 0 {
			for _, d := range channel.NowPlaying.Descriptions {
				rec.Description = d
				break
			}
		}
	}

	if err := r.db.CreateRecording(rec); err != nil {
		return nil, fmt.Errorf("failed to create recording record: %w", err)
	}

	if err := r.startRecordingJob(rec); err != nil {
		rec.Status = "failed"
		rec.ErrorMessage = err.Error()
		_ = r.db.UpdateRecording(rec)
		return nil, err
	}

	return rec, nil
}

// StopRecording stops an active recording
func (r *Recorder) StopRecording(recID int64) error {
	r.mu.Lock()
	_, exists := r.jobs[recID]
	r.mu.Unlock()

	if !exists {
		// If not in in-memory jobs, check DB
		rec, err := r.db.GetRecording(recID)
		if err != nil {
			return err
		}
		if rec.Status == "scheduled" {
			rec.Status = "cancelled"
			return r.db.UpdateRecording(rec)
		}
		if rec.Status == "recording" {
			rec.Status = "completed"
			rec.EndTime = time.Now()
			rec.Duration = int(rec.EndTime.Sub(rec.StartTime).Seconds())
			if rec.Duration < 0 {
				rec.Duration = 0
			}
			if rec.FilePath != "" {
				if fi, err := os.Stat(rec.FilePath); err == nil {
					rec.FileSize = fi.Size()
				}
			}
			r.notifyTunerChange()
			return r.db.UpdateRecording(rec)
		}
		return nil
	}

	r.finalizeJob(recID, "completed", "")
	return nil
}

// IsRecordingChannel checks if a channel is actively being recorded
func (r *Recorder) IsRecordingChannel(channelID int64) (bool, *db.Recording) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs {
		if job.Recording.ChannelID == channelID {
			return true, job.Recording
		}
	}
	return false, nil
}

// GetActiveJobs returns all active recording jobs
func (r *Recorder) GetActiveJobs() []*db.Recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	var list []*db.Recording
	for _, job := range r.jobs {
		recCopy := *job.Recording
		recCopy.FileSize = job.BytesWritten
		list = append(list, &recCopy)
	}
	return list
}
