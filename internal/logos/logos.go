package logos

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	repoTreeURL      = "https://api.github.com/repos/tv-logo/tv-logos/git/trees/main?recursive=1"
	rawBaseURL       = "https://raw.githubusercontent.com/tv-logo/tv-logos/main/"
	cdnBaseURL       = "https://cdn.jsdelivr.net/gh/tv-logo/tv-logos@main/"
	cacheTTL         = 7 * 24 * time.Hour // 7 days cache for repo tree
)

var noiseWordRegex = regexp.MustCompile(`(?i)\b(hd|sd|uhd|4k|fhd|tv|channel|beyond|tele|gr|uk|us|de|fr|it|es)\b`)
var nonAlphaNumRegex = regexp.MustCompile(`[^a-z0-9]`)

// LogoEntry represents an indexed logo in tv-logo/tv-logos repository.
type LogoEntry struct {
	Path           string `json:"path"`
	Filename       string `json:"filename"`
	Country        string `json:"country"`
	CountryDisplay string `json:"country_display"`
	NormalizedName string `json:"normalized_name"`
	RawURL         string `json:"raw_url"`
	PreviewURL     string `json:"preview_url"`
}

// SearchResult represents a scored logo search result.
type SearchResult struct {
	Path           string `json:"path"`
	Name           string `json:"name"`
	Country        string `json:"country"`
	CountryDisplay string `json:"country_display"`
	RawURL         string `json:"raw_url"`
	PreviewURL     string `json:"preview_url"`
	Score          int    `json:"score"`
}

// Service manages the tv-logos repository index, logo searches, and logo downloading.
type Service struct {
	dataDir    string
	httpClient *http.Client
	mu         sync.RWMutex
	entries    []LogoEntry
	countries  []string
	lastLoaded time.Time
}

func NewService(dataDir string) *Service {
	return &Service{
		dataDir: dataDir,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// TransliterateGreek converts Greek characters to Latin for fuzzy channel name matching.
func TransliterateGreek(s string) string {
	s = strings.ToLower(s)
	replacements := []struct{ from, to string }{
		{"ου", "ou"}, {"ού", "ou"},
		{"μπ", "b"},
		{"ντ", "d"},
		{"γκ", "g"},
		{"γγ", "g"},
		{"τσ", "ts"},
		{"τζ", "tz"},
		{"αι", "e"}, {"αί", "e"},
		{"ει", "i"}, {"εί", "i"},
		{"οι", "i"}, {"οί", "i"},
		{"αυ", "av"}, {"αύ", "av"},
		{"ευ", "ev"}, {"εύ", "ev"},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.from, r.to)
	}

	var sb strings.Builder
	for _, r := range s {
		switch r {
		case 'α', 'ά':
			sb.WriteRune('a')
		case 'β':
			sb.WriteRune('v')
		case 'γ':
			sb.WriteRune('g')
		case 'δ':
			sb.WriteRune('d')
		case 'ε', 'έ':
			sb.WriteRune('e')
		case 'ζ':
			sb.WriteRune('z')
		case 'η', 'ή':
			sb.WriteRune('i')
		case 'θ':
			sb.WriteString("th")
		case 'ι', 'ί', 'ϊ', 'ΐ':
			sb.WriteRune('i')
		case 'κ':
			sb.WriteRune('k')
		case 'λ':
			sb.WriteRune('l')
		case 'μ':
			sb.WriteRune('m')
		case 'ν':
			sb.WriteRune('n')
		case 'ξ':
			sb.WriteString("x")
		case 'ο', 'ό':
			sb.WriteRune('o')
		case 'π':
			sb.WriteRune('p')
		case 'ρ':
			sb.WriteRune('r')
		case 'σ', 'ς':
			sb.WriteRune('s')
		case 'τ':
			sb.WriteRune('t')
		case 'υ', 'ύ', 'ϋ', 'ΰ':
			sb.WriteRune('y')
		case 'φ':
			sb.WriteRune('f')
		case 'χ':
			sb.WriteString("ch")
		case 'ψ':
			sb.WriteString("ps")
		case 'ω', 'ώ':
			sb.WriteRune('o')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// CleanChannelName normalizes a channel name by removing noise words, Greek characters, and punctuation.
func CleanChannelName(name string) string {
	trans := TransliterateGreek(name)
	stripped := noiseWordRegex.ReplaceAllString(trans, "")
	clean := nonAlphaNumRegex.ReplaceAllString(strings.ToLower(stripped), "")
	if clean == "" {
		clean = nonAlphaNumRegex.ReplaceAllString(strings.ToLower(trans), "")
	}
	return clean
}

func formatCountryName(c string) string {
	c = strings.ReplaceAll(c, "-", " ")
	c = strings.ReplaceAll(c, "_", " ")
	parts := strings.Fields(c)
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, " ")
}

// EnsureIndexLoaded ensures that the tv-logos repository entries are loaded in memory.
func (s *Service) EnsureIndexLoaded(ctx context.Context) error {
	s.mu.RLock()
	if len(s.entries) > 0 && time.Since(s.lastLoaded) < cacheTTL {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double check after lock
	if len(s.entries) > 0 && time.Since(s.lastLoaded) < cacheTTL {
		return nil
	}

	indexPath := filepath.Join(s.dataDir, "tv_logos_index.json")

	// Try reading cached index from disk if still fresh
	if fi, err := os.Stat(indexPath); err == nil && time.Since(fi.ModTime()) < cacheTTL {
		if data, err := os.ReadFile(indexPath); err == nil {
			var entries []LogoEntry
			if err := json.Unmarshal(data, &entries); err == nil && len(entries) > 0 {
				s.entries = entries
				s.countries = extractCountries(entries)
				s.lastLoaded = fi.ModTime()
				return nil
			}
		}
	}

	// Fetch repository tree from GitHub
	entries, err := s.fetchRepoTree(ctx)
	if err != nil {
		// If fetch fails (e.g. offline / rate limit), try loading any existing disk cache even if expired
		if data, errDisk := os.ReadFile(indexPath); errDisk == nil {
			var diskEntries []LogoEntry
			if json.Unmarshal(data, &diskEntries) == nil && len(diskEntries) > 0 {
				s.entries = diskEntries
				s.countries = extractCountries(diskEntries)
				s.lastLoaded = time.Now()
				return nil
			}
		}
		return fmt.Errorf("failed to load tv-logos index: %w", err)
	}

	s.entries = entries
	s.countries = extractCountries(entries)
	s.lastLoaded = time.Now()

	// Cache to disk
	if data, err := json.Marshal(entries); err == nil {
		_ = os.WriteFile(indexPath, data, 0644)
	}

	return nil
}

type gitTreeResponse struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
}

func (s *Service) fetchRepoTree(ctx context.Context) ([]LogoEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoTreeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DVB-Web-LogoFetcher/1.0")
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var treeResp gitTreeResponse
	if err := json.NewDecoder(resp.Body).Decode(&treeResp); err != nil {
		return nil, err
	}

	var entries []LogoEntry
	for _, item := range treeResp.Tree {
		p := item.Path
		if !strings.HasSuffix(p, ".png") && !strings.HasSuffix(p, ".svg") {
			continue
		}
		if strings.Contains(p, "0_all_logos_mosaic") {
			continue
		}
		if !strings.HasPrefix(p, "countries/") && !strings.HasPrefix(p, "misc/") {
			continue
		}

		parts := strings.Split(p, "/")
		country := "misc"
		if len(parts) >= 2 && parts[0] == "countries" {
			country = parts[1]
		}

		filename := path.Base(p)
		baseNoExt := strings.TrimSuffix(strings.TrimSuffix(filename, ".png"), ".svg")
		norm := CleanChannelName(baseNoExt)

		entries = append(entries, LogoEntry{
			Path:           p,
			Filename:       filename,
			Country:        country,
			CountryDisplay: formatCountryName(country),
			NormalizedName: norm,
			RawURL:         rawBaseURL + p,
			PreviewURL:     cdnBaseURL + p,
		})
	}

	return entries, nil
}

func extractCountries(entries []LogoEntry) []string {
	seen := make(map[string]bool)
	var list []string
	for _, e := range entries {
		if e.Country != "" && !seen[e.Country] {
			seen[e.Country] = true
			list = append(list, e.Country)
		}
	}
	sort.Strings(list)
	return list
}

// GetCountries returns the sorted list of available countries.
func (s *Service) GetCountries(ctx context.Context) ([]string, error) {
	if err := s.EnsureIndexLoaded(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make([]string, len(s.countries))
	copy(res, s.countries)
	return res, nil
}

// Search searches for matching logos in the tv-logos repository.
func (s *Service) Search(ctx context.Context, query string, countryFilter string, limit int) ([]SearchResult, error) {
	if err := s.EnsureIndexLoaded(ctx); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > 100 {
		limit = 30
	}

	cleanQuery := CleanChannelName(query)
	rawQueryLower := strings.ToLower(strings.TrimSpace(query))
	countryFilterLower := strings.ToLower(strings.TrimSpace(countryFilter))

	var results []SearchResult
	for _, e := range s.entries {
		if countryFilterLower != "" && countryFilterLower != "all" && strings.ToLower(e.Country) != countryFilterLower {
			continue
		}

		score := scoreMatch(cleanQuery, rawQueryLower, e)
		if score > 0 {
			name := strings.TrimSuffix(strings.TrimSuffix(e.Filename, ".png"), ".svg")
			results = append(results, SearchResult{
				Path:           e.Path,
				Name:           formatCountryName(name),
				Country:        e.Country,
				CountryDisplay: e.CountryDisplay,
				RawURL:         e.RawURL,
				PreviewURL:     e.PreviewURL,
				Score:          score,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Name < results[j].Name
	})

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

func scoreMatch(cleanQuery, rawQuery string, e LogoEntry) int {
	cleanName := e.NormalizedName
	rawFilename := strings.ToLower(e.Filename)

	if cleanQuery == "" && rawQuery == "" {
		return 10
	}

	// Exact normalized match (e.g. "mega" == "mega")
	if cleanQuery != "" && cleanName == cleanQuery {
		return 100
	}

	// Filename contains raw query
	if rawQuery != "" && strings.Contains(rawFilename, rawQuery) {
		return 90
	}

	// Prefix match
	if cleanQuery != "" && strings.HasPrefix(cleanName, cleanQuery) {
		return 85
	}
	if cleanQuery != "" && strings.HasPrefix(cleanQuery, cleanName) && len(cleanName) >= 3 {
		return 80
	}

	// Substring match
	if cleanQuery != "" && strings.Contains(cleanName, cleanQuery) {
		return 75
	}
	if cleanQuery != "" && strings.Contains(cleanQuery, cleanName) && len(cleanName) >= 4 {
		return 70
	}

	// Word token match
	tokens := strings.Fields(rawQuery)
	matchedTokens := 0
	for _, tok := range tokens {
		cleanTok := CleanChannelName(tok)
		if cleanTok != "" && (strings.Contains(cleanName, cleanTok) || strings.Contains(rawFilename, cleanTok)) {
			matchedTokens++
		}
	}
	if len(tokens) > 0 && matchedTokens == len(tokens) {
		return 65
	}
	if matchedTokens > 0 {
		return 40 + (matchedTokens * 10)
	}

	return 0
}

// AutoFindBestLogo searches for the best matching logo for a channel name.
func (s *Service) AutoFindBestLogo(ctx context.Context, channelName string, countryHint string) (*SearchResult, error) {
	results, err := s.Search(ctx, channelName, countryHint, 5)
	if err != nil {
		return nil, err
	}

	if len(results) == 0 && countryHint != "" && countryHint != "all" {
		// Fallback without country filter if no match in hint country
		results, err = s.Search(ctx, channelName, "all", 5)
		if err != nil {
			return nil, err
		}
	}

	if len(results) > 0 && results[0].Score >= 60 {
		return &results[0], nil
	}

	return nil, nil
}

// DownloadAndSaveLogo downloads a logo from a remote URL (or tv-logo path) and saves it locally.
// Returns the local URL path, e.g. "/api/logos/ch_1_1731100000.png"
func (s *Service) DownloadAndSaveLogo(ctx context.Context, channelID int64, logoSource string) (string, error) {
	logoSource = strings.TrimSpace(logoSource)
	if logoSource == "" {
		return "", fmt.Errorf("empty logo source")
	}

	// Already a local logo
	if strings.HasPrefix(logoSource, "/api/logos/") {
		return logoSource, nil
	}

	downloadURL := logoSource
	if !strings.HasPrefix(logoSource, "http://") && !strings.HasPrefix(logoSource, "https://") {
		// Treat as tv-logos repository relative path
		downloadURL = rawBaseURL + strings.TrimPrefix(logoSource, "/")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("invalid request for %s: %w", downloadURL, err)
	}
	req.Header.Set("User-Agent", "DVB-Web-LogoDownloader/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download logo from %s: %w", downloadURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned HTTP status %d for %s", resp.StatusCode, downloadURL)
	}

	ext := strings.ToLower(filepath.Ext(downloadURL))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".svg" && ext != ".webp" {
		ct := resp.Header.Get("Content-Type")
		switch {
		case strings.Contains(ct, "png"):
			ext = ".png"
		case strings.Contains(ct, "svg"):
			ext = ".svg"
		case strings.Contains(ct, "jpeg") || strings.Contains(ct, "jpg"):
			ext = ".jpg"
		case strings.Contains(ct, "webp"):
			ext = ".webp"
		default:
			ext = ".png"
		}
	}

	logosDir := filepath.Join(s.dataDir, "logos")
	if err := os.MkdirAll(logosDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create logos directory: %w", err)
	}

	filename := fmt.Sprintf("ch_%d_%d%s", channelID, time.Now().Unix(), ext)
	dstPath := filepath.Join(logosDir, filename)

	dst, err := os.Create(dstPath)
	if err != nil {
		return "", fmt.Errorf("failed to create local logo file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, resp.Body); err != nil {
		_ = os.Remove(dstPath)
		return "", fmt.Errorf("failed to write local logo: %w", err)
	}

	return "/api/logos/" + filename, nil
}
