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
	"sync/atomic"
)

const (
	maxSizeBytes    = 10 * 1024 * 1024
	sessionFileName = "vestige.log"
)

var (
	mu          sync.Mutex // guards Setup/StartRun/Close against each other (unchanged)
	sessionFile *os.File
	liveDir     string
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

// pointStderrAt re-targets WHERE stderr output is teed to, without ever
// reassigning the global os.Stderr variable itself after the first call.
//
// CodeRabbit-flagged data race, fixed here: the previous version called
// `os.Stderr = w` on every StartRun/Close, creating a brand-new pipe each
// time. os.Stderr is a package-level var in the "os" package — every
// pipeline goroutine that does fmt.Fprintf(os.Stderr, ...) (per this
// package's own doc comment, that's exactly what [Orchestrator]/
// [Scraper]/[Session] logging does) reads that global WITHOUT taking
// applog's mu, because it can't — mu is unexported and those call sites
// have no reason to know applog exists. So a concurrent StartRun/Close
// swapping os.Stderr out from under an in-flight Fprintf was a genuine
// data race (flagged by `go test -race`), and in the worst case could
// write to a pipe whose write-end had already been closed by the very
// swap that raced it ("io: write on closed pipe").
//
// Fixed by inverting which side changes: os.Stderr is now pointed at ONE
// long-lived pipe write-end, set exactly once (see ensureStderrPipe
// below) and never reassigned again for the life of the process — so
// every concurrent Fprintf(os.Stderr, ...) always sees a stable, valid,
// open file, full stop. What changes on StartRun/Close instead is the
// pipe READ side's fan-out target, via currentDest — an atomic.Pointer
// swap that only this package's own single pump goroutine ever reads
// from, so no other goroutine's os.Stderr access is affected by it at
// all.
func pointStderrAt(dests ...io.Writer) error {
	if err := ensureStderrPipe(); err != nil {
		return err
	}
	allDests := append([]io.Writer{realStderr}, dests...)
	tee := io.MultiWriter(allDests...)
	currentDest.Store(&tee)
	return nil
}

var (
	realStderr = os.Stderr // captured once, before anything ever reassigns os.Stderr

	stderrPipeOnce sync.Once
	stderrPipeErr  error
	currentDest    atomic.Pointer[io.Writer] // read only by the pump goroutine below

	// idleDest is what currentDest holds before Setup's first
	// pointStderrAt call, and again after a RunLogger.Close() with no
	// active run — output still reaches the real terminal via realStderr
	// (always included in allDests above) even in that window.
	idleDest io.Writer = io.Discard
)

// ensureStderrPipe creates the single, process-lifetime pipe and points
// the real os.Stderr at its write end exactly once. Safe to call
// repeatedly (sync.Once) — every pointStderrAt call goes through this,
// but only the first actually does anything.
func ensureStderrPipe() error {
	stderrPipeOnce.Do(func() {
		r, w, err := os.Pipe()
		if err != nil {
			stderrPipeErr = fmt.Errorf("applog: create stderr pipe: %w", err)
			return
		}
		var initial io.Writer = idleDest
		currentDest.Store(&initial)
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					dest := currentDest.Load()
					if dest != nil {
						_, _ = (*dest).Write(buf[:n])
					}
				}
				if err != nil {
					return
				}
			}
		}()
		os.Stderr = w
	})
	return stderrPipeErr
}

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