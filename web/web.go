package web

import (
	"embed"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed *
var EmbeddedFS embed.FS

// GetFileSystem returns an http.FileSystem. If overrideDir exists and contains index.html,
// it serves directly from disk (ideal for live development).
// Otherwise, it serves from the embedded filesystem directly inside the binary.
func GetFileSystem(overrideDir string) http.FileSystem {
	if overrideDir != "" {
		indexPath := filepath.Join(overrideDir, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			return http.Dir(overrideDir)
		}
	}
	return http.FS(EmbeddedFS)
}

// GetIconPNG returns the raw PNG icon bytes embedded in the binary
func GetIconPNG() []byte {
	data, _ := EmbeddedFS.ReadFile("favicon.png")
	return data
}
