package logos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTransliterateGreekAndClean(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"MEGA HD", "mega"},
		{"ALPHA HD", "alpha"},
		{"ANT1 HD", "ant1"},
		{"STAR HD", "star"},
		{"SKAI HD", "skai"},
		{"OPEN BEYOND HD", "open"},
		{"ERT1 HD", "ert1"},
		{"ERT 2", "ert2"},
		{"ACTION 24", "action24"},
		{"ΣΚΑΪ HD", "skai"},
		{"ΕΡΤ1", "ert1"},
		{"ΑΛΦΑ", "alfa"},
	}

	for _, tc := range tests {
		clean := CleanChannelName(tc.input)
		if clean != tc.expected {
			t.Errorf("CleanChannelName(%q) = %q; expected %q", tc.input, clean, tc.expected)
		}
	}
}

func TestSearchAndAutoFind(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dvb-logos-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	svc := NewService(tmpDir)
	svc.entries = []LogoEntry{
		{
			Path:           "countries/greece/mega-channel-gr.png",
			Filename:       "mega-channel-gr.png",
			Country:        "greece",
			CountryDisplay: "Greece",
			NormalizedName: "mega",
			RawURL:         "https://raw.githubusercontent.com/tv-logo/tv-logos/main/countries/greece/mega-channel-gr.png",
			PreviewURL:     "https://cdn.jsdelivr.net/gh/tv-logo/tv-logos@main/countries/greece/mega-channel-gr.png",
		},
		{
			Path:           "countries/greece/skai-tv-gr.png",
			Filename:       "skai-tv-gr.png",
			Country:        "greece",
			CountryDisplay: "Greece",
			NormalizedName: "skai",
			RawURL:         "https://raw.githubusercontent.com/tv-logo/tv-logos/main/countries/greece/skai-tv-gr.png",
			PreviewURL:     "https://cdn.jsdelivr.net/gh/tv-logo/tv-logos@main/countries/greece/skai-tv-gr.png",
		},
		{
			Path:           "countries/spain/mega-es.png",
			Filename:       "mega-es.png",
			Country:        "spain",
			CountryDisplay: "Spain",
			NormalizedName: "mega",
			RawURL:         "https://raw.githubusercontent.com/tv-logo/tv-logos/main/countries/spain/mega-es.png",
			PreviewURL:     "https://cdn.jsdelivr.net/gh/tv-logo/tv-logos@main/countries/spain/mega-es.png",
		},
	}
	svc.countries = []string{"greece", "spain"}
	svc.lastLoaded = svc.lastLoaded.Add(100 * 365 * 24 * 3600) // fake fresh time

	ctx := context.Background()

	// 1. Search for MEGA with Greece country filter
	res, err := svc.Search(ctx, "MEGA HD", "greece", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("Expected results, got 0")
	}
	if res[0].Path != "countries/greece/mega-channel-gr.png" {
		t.Errorf("Expected mega-channel-gr.png, got %s", res[0].Path)
	}

	// 2. AutoFind best logo for Greek SKAI HD
	best, err := svc.AutoFindBestLogo(ctx, "ΣΚΑΪ HD", "greece")
	if err != nil {
		t.Fatalf("AutoFind failed: %v", err)
	}
	if best == nil {
		t.Fatalf("Expected best match for SKAI HD, got nil")
	}
	if best.Path != "countries/greece/skai-tv-gr.png" {
		t.Errorf("Expected skai-tv-gr.png, got %s", best.Path)
	}
}

func TestDownloadAndSaveLogo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) // PNG header
	}))
	defer ts.Close()

	tmpDir, err := os.MkdirTemp("", "dvb-logos-dl-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	svc := NewService(tmpDir)
	localURL, err := svc.DownloadAndSaveLogo(context.Background(), 42, ts.URL+"/test-logo.png")
	if err != nil {
		t.Fatalf("DownloadAndSaveLogo failed: %v", err)
	}

	if localURL == "" {
		t.Fatalf("Expected localURL, got empty")
	}

	expectedPrefix := "/api/logos/ch_42_"
	if len(localURL) < len(expectedPrefix) || localURL[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("Expected localURL to start with %q, got %q", expectedPrefix, localURL)
	}

	// Verify file exists on disk
	filename := filepath.Base(localURL)
	diskPath := filepath.Join(tmpDir, "logos", filename)
	if _, err := os.Stat(diskPath); err != nil {
		t.Errorf("Downloaded file does not exist on disk: %v", err)
	}
}
