package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultDataDir returns a per-user writable directory for the SQLite DB,
// run logs, and settings-encryption key fallback — independent of where
// the executable is installed. On Windows this is %LOCALAPPDATA%\VestigeGo
// (via os.UserCacheDir); on macOS ~/Library/Caches/VestigeGo; on Linux
// $XDG_CACHE_HOME/VestigeGo (or ~/.cache/VestigeGo).
func defaultDataDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve user cache dir: %w", err)
	}
	dir := filepath.Join(base, "VestigeGo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("paths: create data dir: %w", err)
	}
	return dir, nil
}
