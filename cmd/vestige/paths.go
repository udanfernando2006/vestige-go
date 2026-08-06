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
	// 0700 (CodeRabbit-flagged, was 0755): this directory holds the SQLite
	// DB and, per keystore.go's own fallback path, potentially the
	// settings-encryption key file itself. keystore.go already writes
	// that file with 0600, but a 0755 CONTAINING directory still let any
	// other local OS user account traverse into and list it — 0700
	// (owner read/write/execute only) closes that gap so the directory's
	// own permissions don't undercut the file-level protection already
	// applied one level down.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("paths: create data dir: %w", err)
	}
	return dir, nil
}
