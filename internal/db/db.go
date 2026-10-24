package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Database struct {
	db      *sql.DB
	dataDir string
}

type Channel struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Number         int       `json:"number"`
	LogoURL        string    `json:"logo_url"`
	Frequency      int64     `json:"frequency"` // in Hz, e.g. 506000000
	Bandwidth      int       `json:"bandwidth"` // in Hz, e.g. 8000000
	DeliverySystem string    `json:"delivery_system"` // DVBT, DVBT2, DVBC, DVBS, etc.
	ServiceID      int       `json:"service_id"`
	VideoPID       int       `json:"video_pid"`
	AudioPID       int       `json:"audio_pid"`
	ExtraParams    string    `json:"extra_params"`
	Enabled        bool      `json:"enabled"`
	IsRadio        bool      `json:"is_radio"`
	HbbtvURL       string    `json:"hbbtv_url,omitempty"`
	HasTeletext    bool      `json:"has_teletext"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	// Runtime enriched field:
	NowPlaying  *EPGEvent `json:"now_playing,omitempty"`
	NextPlaying *EPGEvent `json:"next_playing,omitempty"`
}

type EPGEvent struct {
	ID           int64             `json:"id"`
	ChannelID    int64             `json:"channel_id"`
	EventID      int               `json:"event_id"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	Titles       map[string]string `json:"titles"`       // e.g. {"eng": "BBC News", "fra": "Les Nouvelles"}
	Descriptions map[string]string `json:"descriptions"` // e.g. {"eng": "Morning bulletin"}
	Genres       []string          `json:"genres"`
}

type Recording struct {
	ID           int64     `json:"id"`
	ChannelID    int64     `json:"channel_id"`
	ChannelName  string    `json:"channel_name,omitempty"`
	EventID      int       `json:"event_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	Duration     int       `json:"duration"` // in seconds
	PrePadding   int       `json:"pre_padding"` // in minutes
	PostPadding  int       `json:"post_padding"` // in minutes
	FilePath     string    `json:"file_path"`
	FileSize     int64     `json:"file_size"` // in bytes
	Status       string    `json:"status"` // "scheduled", "recording", "completed", "failed", "cancelled"
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func (r *Recording) EffectiveStartTime() time.Time {
	pad := r.PrePadding
	if pad < 0 {
		pad = 0
	}
	return r.StartTime.Add(-time.Duration(pad) * time.Minute)
}

func (r *Recording) EffectiveEndTime() time.Time {
	pad := r.PostPadding
	if pad < 0 {
		pad = 0
	}
	return r.EndTime.Add(time.Duration(pad) * time.Minute)
}

func Open(dataDir string) (*Database, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	logosDir := filepath.Join(dataDir, "logos")
	if err := os.MkdirAll(logosDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create logos dir: %w", err)
	}

	recordingsDir := filepath.Join(dataDir, "recordings")
	if err := os.MkdirAll(recordingsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create recordings dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "tv.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// Optimize SQLite for concurrent reading & fast writes
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;"); err != nil {
		return nil, err
	}

	d := &Database{
		db:      sqlDB,
		dataDir: dataDir,
	}

	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return d, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) DataDir() string {
	return d.dataDir
}

func (d *Database) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		number INTEGER NOT NULL,
		logo_url TEXT DEFAULT '',
		frequency INTEGER NOT NULL,
		bandwidth INTEGER DEFAULT 8000000,
		delivery_system TEXT DEFAULT 'DVBT',
		service_id INTEGER NOT NULL,
		video_pid INTEGER DEFAULT 0,
		audio_pid INTEGER DEFAULT 0,
		extra_params TEXT DEFAULT '',
		enabled INTEGER DEFAULT 1,
		is_radio INTEGER DEFAULT 0,
		hbbtv_url TEXT DEFAULT '',
		has_teletext INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS epg_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id INTEGER NOT NULL,
		event_id INTEGER NOT NULL,
		start_time DATETIME NOT NULL,
		end_time DATETIME NOT NULL,
		titles_json TEXT NOT NULL,
		descriptions_json TEXT NOT NULL,
		genres_json TEXT DEFAULT '[]',
		UNIQUE(channel_id, event_id, start_time) ON CONFLICT REPLACE,
		FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS recordings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id INTEGER NOT NULL,
		event_id INTEGER DEFAULT 0,
		title TEXT NOT NULL,
		description TEXT DEFAULT '',
		start_time DATETIME NOT NULL,
		end_time DATETIME NOT NULL,
		duration INTEGER NOT NULL,
		pre_padding INTEGER DEFAULT 2,
		post_padding INTEGER DEFAULT 5,
		file_path TEXT DEFAULT '',
		file_size INTEGER DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'scheduled',
		error_message TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_channels_freq ON channels(frequency);
	CREATE INDEX IF NOT EXISTS idx_epg_chan_time ON epg_events(channel_id, start_time, end_time);
	CREATE INDEX IF NOT EXISTS idx_recordings_status ON recordings(status, start_time);
	`
	_, err := d.db.Exec(schema)
	if err != nil {
		return err
	}

	// Ensure newer columns exist for existing installations
	_, _ = d.db.Exec("ALTER TABLE channels ADD COLUMN is_radio INTEGER DEFAULT 0;")
	_, _ = d.db.Exec("ALTER TABLE channels ADD COLUMN hbbtv_url TEXT DEFAULT '';")
	_, _ = d.db.Exec("ALTER TABLE channels ADD COLUMN has_teletext INTEGER DEFAULT 0;")
	_, _ = d.db.Exec("ALTER TABLE recordings ADD COLUMN pre_padding INTEGER DEFAULT 2;")
	_, _ = d.db.Exec("ALTER TABLE recordings ADD COLUMN post_padding INTEGER DEFAULT 5;")

	// Clear legacy hardcoded teletext flags so teletext is only enabled when actually broadcast in PMT
	_, _ = d.db.Exec("UPDATE channels SET has_teletext = 0;")

	// Fix known frequency-specific service ID discrepancies (e.g. 522MHz Digea SD multiplex)
	_, _ = d.db.Exec("UPDATE channels SET service_id = 3500 WHERE frequency = 522000000 AND service_id = 1500;")
	_, _ = d.db.Exec("UPDATE channels SET service_id = 3600 WHERE frequency = 522000000 AND service_id = 1600;")
	_, _ = d.db.Exec("UPDATE channels SET service_id = 3700 WHERE frequency = 522000000 AND service_id = 1700;")

	// Clean up any corrupt dates or duplicate event_ids from older schema versions
	_, _ = d.db.Exec(`
		DELETE FROM epg_events WHERE id NOT IN (
			SELECT MAX(id) FROM epg_events GROUP BY channel_id, event_id
		);
		DELETE FROM epg_events WHERE start_time < '2024-01-01' OR start_time > '2035-01-01';
		CREATE UNIQUE INDEX IF NOT EXISTS idx_epg_chan_event ON epg_events(channel_id, event_id);
	`)

	// Clean up any legacy descriptions that were split across extended event descriptors with \n
	d.cleanDescriptions()

	// Seed default settings if empty
	defaults := map[string]string{
		"preferred_language": "eng",
		"transcoder_mode":    "auto", // auto, vaapi, nvenc, v4l2m2m, cpu
		"video_bitrate":      "2500k",
		"video_preset":       "ultrafast",
		"idle_timeout_sec":   "15",
	}

	for k, v := range defaults {
		d.db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)", k, v)
	}

	return nil
}

// Settings methods
func (d *Database) GetSetting(key, fallback string) string {
	var val string
	err := d.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		return fallback
	}
	return val
}

func (d *Database) GetAllSettings() (map[string]string, error) {
	rows, err := d.db.Query("SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			res[k] = v
		}
	}
	return res, nil
}

func (d *Database) SetSetting(key, value string) error {
	_, err := d.db.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

// Channels methods
func (d *Database) ListChannels() ([]Channel, error) {
	rows, err := d.db.Query(`
		SELECT id, name, number, logo_url, frequency, bandwidth, delivery_system, 
		       service_id, video_pid, audio_pid, extra_params, enabled, COALESCE(is_radio, 0),
		       COALESCE(hbbtv_url, ''), COALESCE(has_teletext, 0), created_at, updated_at
		FROM channels 
		ORDER BY number ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var c Channel
		var enabledInt, isRadioInt, hasTxtInt int
		if err := rows.Scan(
			&c.ID, &c.Name, &c.Number, &c.LogoURL, &c.Frequency, &c.Bandwidth,
			&c.DeliverySystem, &c.ServiceID, &c.VideoPID, &c.AudioPID, &c.ExtraParams,
			&enabledInt, &isRadioInt, &c.HbbtvURL, &hasTxtInt, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		c.Enabled = (enabledInt == 1)
		c.IsRadio = (isRadioInt == 1)
		c.HasTeletext = (hasTxtInt == 1)
		channels = append(channels, c)
	}

	// Populate Now & Next EPG
	now := time.Now().UTC()
	for i := range channels {
		channels[i].NowPlaying = d.getEventAt(channels[i].ID, now)
		channels[i].NextPlaying = d.getNextEvent(channels[i].ID, now)
	}

	return channels, nil
}

func (d *Database) GetChannel(id int64) (*Channel, error) {
	row := d.db.QueryRow(`
		SELECT id, name, number, logo_url, frequency, bandwidth, delivery_system, 
		       service_id, video_pid, audio_pid, extra_params, enabled, COALESCE(is_radio, 0),
		       COALESCE(hbbtv_url, ''), COALESCE(has_teletext, 0), created_at, updated_at
		FROM channels WHERE id = ?
	`, id)

	var c Channel
	var enabledInt, isRadioInt, hasTxtInt int
	if err := row.Scan(
		&c.ID, &c.Name, &c.Number, &c.LogoURL, &c.Frequency, &c.Bandwidth,
		&c.DeliverySystem, &c.ServiceID, &c.VideoPID, &c.AudioPID, &c.ExtraParams,
		&enabledInt, &isRadioInt, &c.HbbtvURL, &hasTxtInt, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	c.Enabled = (enabledInt == 1)
	c.IsRadio = (isRadioInt == 1)
	c.HasTeletext = (hasTxtInt == 1)

	// Populate Now & Next EPG
	now := time.Now().UTC()
	c.NowPlaying = d.getEventAt(c.ID, now)
	c.NextPlaying = d.getNextEvent(c.ID, now)

	return &c, nil
}

func (d *Database) SaveChannel(c *Channel) error {
	now := time.Now()
	enabledInt := 0
	if c.Enabled {
		enabledInt = 1
	}
	isRadioInt := 0
	if c.IsRadio {
		isRadioInt = 1
	}
	hasTxtInt := 0
	if c.HasTeletext {
		hasTxtInt = 1
	}

	if c.ID == 0 {
		res, err := d.db.Exec(`
			INSERT INTO channels (name, number, logo_url, frequency, bandwidth, delivery_system, service_id, video_pid, audio_pid, extra_params, enabled, is_radio, hbbtv_url, has_teletext, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, c.Name, c.Number, c.LogoURL, c.Frequency, c.Bandwidth, c.DeliverySystem, c.ServiceID, c.VideoPID, c.AudioPID, c.ExtraParams, enabledInt, isRadioInt, c.HbbtvURL, hasTxtInt, now, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		c.ID = id
		c.CreatedAt = now
		c.UpdatedAt = now
		return nil
	}

	_, err := d.db.Exec(`
		UPDATE channels SET name=?, number=?, logo_url=?, frequency=?, bandwidth=?, delivery_system=?, service_id=?, video_pid=?, audio_pid=?, extra_params=?, enabled=?, is_radio=?, hbbtv_url=?, has_teletext=?, updated_at=?
		WHERE id = ?
	`, c.Name, c.Number, c.LogoURL, c.Frequency, c.Bandwidth, c.DeliverySystem, c.ServiceID, c.VideoPID, c.AudioPID, c.ExtraParams, enabledInt, isRadioInt, c.HbbtvURL, hasTxtInt, now, c.ID)
	if err == nil {
		c.UpdatedAt = now
	}
	return err
}

func (d *Database) UpdateChannelLogo(id int64, logoURL string) error {
	_, err := d.db.Exec("UPDATE channels SET logo_url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", logoURL, id)
	return err
}

func (d *Database) UpdateChannelTeletext(id int64, hasTeletext bool) error {
	val := 0
	if hasTeletext {
		val = 1
	}
	_, err := d.db.Exec("UPDATE channels SET has_teletext = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", val, id)
	return err
}

func (d *Database) DeleteChannel(id int64) error {
	_, err := d.db.Exec("DELETE FROM channels WHERE id = ?", id)
	return err
}

// Channel Scanning comparison & pruning
type ScanDiff struct {
	DiscoveredCount int       `json:"discovered_count"`
	NewChannels     []Channel `json:"new_channels"`
	UpdatedChannels []Channel `json:"updated_channels"`
	MissingChannels []Channel `json:"missing_channels"`
}

func (d *Database) ProcessScanResults(scanned []Channel) (*ScanDiff, error) {
	existing, err := d.ListChannels()
	if err != nil {
		return nil, err
	}

	existingMap := make(map[string]Channel)
	for _, c := range existing {
		// Key by Frequency + ServiceID (the unique physical DVB channel identifier)
		key := fmt.Sprintf("%d:%d", c.Frequency, c.ServiceID)
		existingMap[key] = c
	}

	diff := &ScanDiff{
		DiscoveredCount: len(scanned),
		NewChannels:     make([]Channel, 0),
		UpdatedChannels: make([]Channel, 0),
		MissingChannels: make([]Channel, 0),
	}

	scannedKeys := make(map[string]bool)

	for _, s := range scanned {
		key := fmt.Sprintf("%d:%d", s.Frequency, s.ServiceID)
		scannedKeys[key] = true

		if match, exists := existingMap[key]; exists {
			s.ID = match.ID
			s.Number = match.Number
			if s.LogoURL == "" {
				s.LogoURL = match.LogoURL
			}
			_ = d.SaveChannel(&s)
			diff.UpdatedChannels = append(diff.UpdatedChannels, s)
		} else {
			// New channel discovered
			if s.Number == 0 {
				s.Number = len(existing) + len(diff.NewChannels) + 1
			}
			s.Enabled = true
			_ = d.SaveChannel(&s)
			diff.NewChannels = append(diff.NewChannels, s)
		}
	}

	// Any existing channel not discovered in this scan is considered missing
	for key, c := range existingMap {
		if !scannedKeys[key] {
			diff.MissingChannels = append(diff.MissingChannels, c)
		}
	}

	return diff, nil
}

func (d *Database) PruneChannels(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf("DELETE FROM channels WHERE id IN (%s)", strings.Join(placeholders, ","))
	res, err := d.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Clean legacy newline splits in descriptions
func (d *Database) cleanDescriptions() {
	rows, err := d.db.Query("SELECT id, descriptions_json FROM epg_events WHERE descriptions_json LIKE '%\n%' OR descriptions_json LIKE '%\\n%'")
	if err != nil {
		return
	}
	defer rows.Close()

	type updateItem struct {
		id   int64
		json string
	}
	var updates []updateItem

	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			continue
		}
		changed := false
		for k, v := range m {
			if strings.Contains(v, "\n") {
				m[k] = strings.ReplaceAll(v, "\n", "")
				changed = true
			}
		}
		if changed {
			b, err := json.Marshal(m)
			if err == nil {
				updates = append(updates, updateItem{id: id, json: string(b)})
			}
		}
	}

	for _, u := range updates {
		_, _ = d.db.Exec("UPDATE epg_events SET descriptions_json = ? WHERE id = ?", u.json, u.id)
	}
}

// EPG Methods
func (d *Database) SaveEPGEvents(events []EPGEvent) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO epg_events (channel_id, event_id, start_time, end_time, titles_json, descriptions_json, genres_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id, event_id) DO UPDATE SET
			start_time=excluded.start_time,
			end_time=excluded.end_time,
			titles_json=CASE WHEN excluded.titles_json != '{}' THEN excluded.titles_json ELSE epg_events.titles_json END,
			descriptions_json=CASE WHEN length(excluded.descriptions_json) > 4 THEN excluded.descriptions_json ELSE epg_events.descriptions_json END,
			genres_json=CASE WHEN excluded.genres_json != '[]' THEN excluded.genres_json ELSE epg_events.genres_json END
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range events {
		if e.StartTime.IsZero() || e.EndTime.IsZero() || e.EndTime.Before(e.StartTime) {
			continue
		}
		sTime := e.StartTime.UTC().Format("2006-01-02 15:04:05")
		eTime := e.EndTime.UTC().Format("2006-01-02 15:04:05")

		titlesBytes, _ := json.Marshal(e.Titles)
		descBytes, _ := json.Marshal(e.Descriptions)
		genreBytes, _ := json.Marshal(e.Genres)

		_, err := stmt.Exec(e.ChannelID, e.EventID, sTime, eTime, string(titlesBytes), string(descBytes), string(genreBytes))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (d *Database) GetEPG(channelID int64, from, to time.Time) ([]EPGEvent, error) {
	fromStr := from.UTC().Format("2006-01-02 15:04:05")
	toStr := to.UTC().Format("2006-01-02 15:04:05")

	rows, err := d.db.Query(`
		SELECT id, channel_id, event_id, start_time, end_time, titles_json, descriptions_json, genres_json
		FROM epg_events
		WHERE channel_id = ? AND end_time >= ? AND start_time <= ?
		ORDER BY start_time ASC
	`, channelID, fromStr, toStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return d.scanEPGEvents(rows)
}

func (d *Database) GetAllEPG(from, to time.Time) ([]EPGEvent, error) {
	fromStr := from.UTC().Format("2006-01-02 15:04:05")
	toStr := to.UTC().Format("2006-01-02 15:04:05")

	rows, err := d.db.Query(`
		SELECT id, channel_id, event_id, start_time, end_time, titles_json, descriptions_json, genres_json
		FROM epg_events
		WHERE end_time >= ? AND start_time <= ?
		ORDER BY channel_id ASC, start_time ASC, id DESC
	`, fromStr, toStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return d.scanEPGEvents(rows)
}

func (d *Database) getEventAt(channelID int64, t time.Time) *EPGEvent {
	tStr := t.UTC().Format("2006-01-02 15:04:05")
	row := d.db.QueryRow(`
		SELECT id, channel_id, event_id, start_time, end_time, titles_json, descriptions_json, genres_json
		FROM epg_events
		WHERE channel_id = ? AND start_time <= ? AND end_time > ?
		ORDER BY start_time DESC LIMIT 1
	`, channelID, tStr, tStr)

	var e EPGEvent
	var startRaw, endRaw interface{}
	var titlesJSON, descJSON, genresJSON string
	if err := row.Scan(&e.ID, &e.ChannelID, &e.EventID, &startRaw, &endRaw, &titlesJSON, &descJSON, &genresJSON); err != nil {
		return nil
	}
	e.StartTime = parseDBTime(startRaw)
	e.EndTime = parseDBTime(endRaw)
	_ = json.Unmarshal([]byte(titlesJSON), &e.Titles)
	_ = json.Unmarshal([]byte(descJSON), &e.Descriptions)
	_ = json.Unmarshal([]byte(genresJSON), &e.Genres)
	return &e
}

func (d *Database) getNextEvent(channelID int64, t time.Time) *EPGEvent {
	tStr := t.UTC().Format("2006-01-02 15:04:05")
	row := d.db.QueryRow(`
		SELECT id, channel_id, event_id, start_time, end_time, titles_json, descriptions_json, genres_json
		FROM epg_events
		WHERE channel_id = ? AND start_time >= ?
		ORDER BY start_time ASC LIMIT 1
	`, channelID, tStr)

	var e EPGEvent
	var startRaw, endRaw interface{}
	var titlesJSON, descJSON, genresJSON string
	if err := row.Scan(&e.ID, &e.ChannelID, &e.EventID, &startRaw, &endRaw, &titlesJSON, &descJSON, &genresJSON); err != nil {
		return nil
	}
	e.StartTime = parseDBTime(startRaw)
	e.EndTime = parseDBTime(endRaw)
	_ = json.Unmarshal([]byte(titlesJSON), &e.Titles)
	_ = json.Unmarshal([]byte(descJSON), &e.Descriptions)
	_ = json.Unmarshal([]byte(genresJSON), &e.Genres)
	return &e
}

func (d *Database) scanEPGEvents(rows *sql.Rows) ([]EPGEvent, error) {
	var events []EPGEvent
	for rows.Next() {
		var e EPGEvent
		var startRaw, endRaw interface{}
		var titlesJSON, descJSON, genresJSON string
		if err := rows.Scan(&e.ID, &e.ChannelID, &e.EventID, &startRaw, &endRaw, &titlesJSON, &descJSON, &genresJSON); err != nil {
			return nil, err
		}
		e.StartTime = parseDBTime(startRaw)
		e.EndTime = parseDBTime(endRaw)
		_ = json.Unmarshal([]byte(titlesJSON), &e.Titles)
		_ = json.Unmarshal([]byte(descJSON), &e.Descriptions)
		_ = json.Unmarshal([]byte(genresJSON), &e.Genres)
		events = append(events, e)
	}
	return events, nil
}

func parseDBTime(val interface{}) time.Time {
	if val == nil {
		return time.Time{}
	}
	switch v := val.(type) {
	case time.Time:
		return v.UTC()
	case string:
		layouts := []string{
			"2006-01-02 15:04:05",
			"2006-01-02 15:04:05 +0000 UTC",
			time.RFC3339,
			"2006-01-02T15:04:05Z",
		}
		for _, l := range layouts {
			if t, err := time.Parse(l, v); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

// ReorderChannels updates the logical order (channel numbers) of channels according to the provided ID slice
func (d *Database) ReorderChannels(order []int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("UPDATE channels SET number = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, id := range order {
		newNumber := i + 1
		if _, err := stmt.Exec(newNumber, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetEPGLanguages returns all distinct language codes present in the EPG database that are supported by broadcasted events
func (d *Database) GetEPGLanguages() ([]string, error) {
	rows, err := d.db.Query(`
		SELECT json_each.key, COUNT(*) as cnt
		FROM epg_events, json_each(epg_events.titles_json)
		WHERE length(json_each.key) = 3
		GROUP BY json_each.key
		HAVING cnt >= 5
		ORDER BY cnt DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var langs []string
	seen := make(map[string]bool)
	for rows.Next() {
		var l string
		var cnt int
		if err := rows.Scan(&l, &cnt); err == nil {
			l = strings.ToLower(strings.TrimSpace(l))
			if l == "ell" {
				l = "gre"
			}
			if len(l) == 3 && l[0] >= 'a' && l[0] <= 'z' && l[1] >= 'a' && l[1] <= 'z' && l[2] >= 'a' && l[2] <= 'z' {
				if !seen[l] {
					seen[l] = true
					langs = append(langs, l)
				}
			}
		}
	}
	return langs, nil
}

// ==========================================================
// RECORDINGS PERSISTENCE
// ==========================================================

func (d *Database) CreateRecording(rec *Recording) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	if rec.PrePadding < 0 {
		rec.PrePadding = 2
	}
	if rec.PostPadding < 0 {
		rec.PostPadding = 5
	}
	sTime := rec.StartTime.UTC().Format("2006-01-02 15:04:05")
	eTime := rec.EndTime.UTC().Format("2006-01-02 15:04:05")
	cTime := rec.CreatedAt.UTC().Format("2006-01-02 15:04:05")

	res, err := d.db.Exec(`
		INSERT INTO recordings (channel_id, event_id, title, description, start_time, end_time, duration, pre_padding, post_padding, file_path, file_size, status, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.ChannelID, rec.EventID, rec.Title, rec.Description, sTime, eTime, rec.Duration, rec.PrePadding, rec.PostPadding, rec.FilePath, rec.FileSize, rec.Status, rec.ErrorMessage, cTime)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	rec.ID = id
	return nil
}

func (d *Database) GetRecording(id int64) (*Recording, error) {
	row := d.db.QueryRow(`
		SELECT r.id, r.channel_id, COALESCE(c.name, 'Unknown'), r.event_id, r.title, r.description,
		       r.start_time, r.end_time, r.duration, COALESCE(r.pre_padding, 2), COALESCE(r.post_padding, 5),
		       r.file_path, r.file_size, r.status, r.error_message, r.created_at
		FROM recordings r
		LEFT JOIN channels c ON r.channel_id = c.id
		WHERE r.id = ?
	`, id)

	var rec Recording
	var startRaw, endRaw, createdRaw interface{}
	err := row.Scan(
		&rec.ID, &rec.ChannelID, &rec.ChannelName, &rec.EventID, &rec.Title, &rec.Description,
		&startRaw, &endRaw, &rec.Duration, &rec.PrePadding, &rec.PostPadding,
		&rec.FilePath, &rec.FileSize, &rec.Status, &rec.ErrorMessage, &createdRaw,
	)
	if err != nil {
		return nil, err
	}
	rec.StartTime = parseDBTime(startRaw)
	rec.EndTime = parseDBTime(endRaw)
	rec.CreatedAt = parseDBTime(createdRaw)
	return &rec, nil
}

func (d *Database) ListRecordings() ([]Recording, error) {
	rows, err := d.db.Query(`
		SELECT r.id, r.channel_id, COALESCE(c.name, 'Unknown'), r.event_id, r.title, r.description,
		       r.start_time, r.end_time, r.duration, COALESCE(r.pre_padding, 2), COALESCE(r.post_padding, 5),
		       r.file_path, r.file_size, r.status, r.error_message, r.created_at
		FROM recordings r
		LEFT JOIN channels c ON r.channel_id = c.id
		ORDER BY r.start_time DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Recording
	for rows.Next() {
		var rec Recording
		var startRaw, endRaw, createdRaw interface{}
		if err := rows.Scan(
			&rec.ID, &rec.ChannelID, &rec.ChannelName, &rec.EventID, &rec.Title, &rec.Description,
			&startRaw, &endRaw, &rec.Duration, &rec.PrePadding, &rec.PostPadding,
			&rec.FilePath, &rec.FileSize, &rec.Status, &rec.ErrorMessage, &createdRaw,
		); err == nil {
			rec.StartTime = parseDBTime(startRaw)
			rec.EndTime = parseDBTime(endRaw)
			rec.CreatedAt = parseDBTime(createdRaw)
			list = append(list, rec)
		}
	}
	return list, nil
}

func (d *Database) ListDueScheduledRecordings(now time.Time) ([]Recording, error) {
	nowStr := now.UTC().Format("2006-01-02 15:04:05")
	rows, err := d.db.Query(`
		SELECT r.id, r.channel_id, COALESCE(c.name, 'Unknown'), r.event_id, r.title, r.description,
		       r.start_time, r.end_time, r.duration, COALESCE(r.pre_padding, 2), COALESCE(r.post_padding, 5),
		       r.file_path, r.file_size, r.status, r.error_message, r.created_at
		FROM recordings r
		LEFT JOIN channels c ON r.channel_id = c.id
		WHERE r.status = 'scheduled'
		  AND datetime(r.start_time, '-' || COALESCE(r.pre_padding, 2) || ' minutes') <= ?
		  AND datetime(r.end_time, '+' || COALESCE(r.post_padding, 5) || ' minutes') > ?
		ORDER BY r.start_time ASC
	`, nowStr, nowStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Recording
	for rows.Next() {
		var rec Recording
		var startRaw, endRaw, createdRaw interface{}
		if err := rows.Scan(
			&rec.ID, &rec.ChannelID, &rec.ChannelName, &rec.EventID, &rec.Title, &rec.Description,
			&startRaw, &endRaw, &rec.Duration, &rec.PrePadding, &rec.PostPadding,
			&rec.FilePath, &rec.FileSize, &rec.Status, &rec.ErrorMessage, &createdRaw,
		); err == nil {
			rec.StartTime = parseDBTime(startRaw)
			rec.EndTime = parseDBTime(endRaw)
			rec.CreatedAt = parseDBTime(createdRaw)
			list = append(list, rec)
		}
	}
	return list, nil
}

func (d *Database) UpdateRecording(rec *Recording) error {
	eTime := rec.EndTime.UTC().Format("2006-01-02 15:04:05")
	_, err := d.db.Exec(`
		UPDATE recordings
		SET file_path = ?, file_size = ?, status = ?, error_message = ?, end_time = ?, duration = ?, pre_padding = ?, post_padding = ?
		WHERE id = ?
	`, rec.FilePath, rec.FileSize, rec.Status, rec.ErrorMessage, eTime, rec.Duration, rec.PrePadding, rec.PostPadding, rec.ID)
	return err
}

func (d *Database) DeleteRecording(id int64) error {
	_, err := d.db.Exec("DELETE FROM recordings WHERE id = ?", id)
	return err
}

func (d *Database) ClearFailedRecordings() (int64, error) {
	res, err := d.db.Exec("DELETE FROM recordings WHERE status = 'failed'")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
