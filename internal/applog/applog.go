// Package applog wires stdout/stderr-based logging to persistent files.
// Two tiers, on purpose:
//
//  1. A single continuous SESSION log (logs-live/vestige.log) covering the
//     whole app lifetime — startup, idle, scheduler ticks, shutdown. Opened
//     once via Setup, for the process's entire life.
//  2. A per-RUN log (logs-live/runs/<runID>.log) covering just one scrape
//     run's [Orchestrator]/[Scraper]/[Session] output, opened via StartRun
//     at the top of executeRun and closed when that run finishes. Named
//     by the same RunID runlog's JSON summaries use, so the two are
//     trivially correlatable — "what actually happened during run
//     abc123" pulls up both the structured JSON (via GET /api/runs/abc123)
//     and this raw text log side by side.
//
// A run's output is written to BOTH files, not just the run file — the
// session log stays a complete, continuous record; the run file is a
// focused excerpt, not a replacement.
//
// Deliberately NOT the same thing as internal/runlog's JSON run
// summaries (see runs.go's package doc comment) — different consumer,
// different format, different directory.
package applog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	maxSizeBytes    = 10 * 1024 * 1024
	sessionFileName = "vestige.log"
)

var (
	mu           sync.Mutex // guards os.Stderr swaps below — StartRun/Close must never race each other
	sessionFile  *os.File
	sessionPipeW *os.File // the write-end os.Stderr currently points at between runs
	liveDir      string
)

// Setup opens the general session log and wires log.SetOutput + the
// os.Stderr variable to it. Call once, at process startup. Returns the
// open file so main.go can Close() it during shutdown.
func Setup(baseLogDir string) (*os.File, error) {
	mu.Lock()
	defer mu.Unlock()

	liveDir = filepath.Join(filepath.Dir(baseLogDir), "logs-live")
	if err := os.MkdirAll(filepath.Join(liveDir, "runs"), 0o755); err != nil {
		return nil, fmt.Errorf("applog: create logs-live/runs dir: %w", err)
	}

	logPath := filepath.Join(liveDir, sessionFileName)
	if err := rotateIfOversized(logPath); err != nil {
		log.Printf("applog: rotation check failed (continuing anyway): %v", err)
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("applog: open %s: %w", logPath, err)
	}
	sessionFile = f

	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	if err := pointStderrAt(f); err != nil {
		return nil, err
	}

	log.Printf("applog: session log at %s", logPath)
	return f, nil
}

// RunLogger holds a per-run log file. Call Close() (via defer) when the
// run finishes, which restores os.Stderr back to the session log.
type RunLogger struct {
	file *os.File
	path string
}

// StartRun opens logs-live/runs/<runID>.log and re-points os.Stderr at a
// tee of (that file + the general session log), so run-scoped
// fmt.Fprintf(os.Stderr, ...) output — [Orchestrator]/[Scraper]/[Session]
// lines — lands in both places for the run's duration. Call at the very
// top of executeRun; defer runLogger.Close() immediately after, so
// os.Stderr is restored even if the run itself returns an error.
func StartRun(runID string) (*RunLogger, error) {
	mu.Lock()
	defer mu.Unlock()

	if sessionFile == nil {
		return nil, fmt.Errorf("applog: StartRun called before Setup")
	}

	safeName := strings.NewReplacer(":", "-").Replace(runID) // RunID's "2026-07-29T15:36:25Z" format contains colons, illegal in Windows filenames
	runPath := filepath.Join(liveDir, "runs", safeName+".log")
	rf, err := os.OpenFile(runPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("applog: open run log %s: %w", runPath, err)
	}

	if err := pointStderrAt(sessionFile, rf); err != nil {
		rf.Close()
		return nil, err
	}

	log.Printf("applog: run log at %s", runPath)
	return &RunLogger{file: rf, path: runPath}, nil
}

// Close restores os.Stderr to the general session log only, then closes
// the run-specific file. Safe to call exactly once per StartRun.
func (rl *RunLogger) Close() {
	mu.Lock()
	defer mu.Unlock()

	if sessionFile != nil {
		_ = pointStderrAt(sessionFile) // best-effort restore; a failure here shouldn't panic mid-shutdown
	}
	rl.file.Close()
}

// pointStderrAt tears down any existing os.Stderr pipe/pump goroutine and
// re-points os.Stderr at a fresh pipe that tees into all of dests. Always
// includes the ORIGINAL real stderr (captured once, at package init) as
// an implicit extra destination, so terminal visibility in dev mode is
// never lost regardless of how many times this is called.
func pointStderrAt(dests ...io.Writer) error {
	if currentStderrCancel != nil {
		currentStderrCancel()
		currentStderrCancel = nil
	}

	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("applog: create stderr pipe: %w", err)
	}

	allDests := append([]io.Writer{realStderr}, dests...)
	tee := io.MultiWriter(allDests...)

	done := make(chan struct{})
	go func() {
		io.Copy(tee, r)
		close(done)
	}()

	os.Stderr = w
	currentStderrCancel = func() {
		w.Close() // closing the write end unblocks io.Copy's read loop
		<-done    // wait for the pump goroutine to drain and exit before returning
		r.Close()
	}
	return nil
}

var (
	realStderr          = os.Stderr // captured once, before anything ever reassigns os.Stderr
	currentStderrCancel func()
)

func rotateIfOversized(logPath string) error {
	info, err := os.Stat(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < maxSizeBytes {
		return nil
	}
	return os.Rename(logPath, logPath+".1")
}
