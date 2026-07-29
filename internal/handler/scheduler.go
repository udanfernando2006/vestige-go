// Scheduler — Go port of api_server.py's in-app scheduler
// (_parse_interval, _seed_last_run_at, _execute_run, _scheduler_tick,
// _scheduler_loop), preserving the "skip a tick, never queue one" run-lock
// semantics migration blueprint's Phase 4 entry explicitly calls out as
// load-bearing to preserve.
//
// ONE REAL SIMPLIFICATION over the Python source, not a fabrication:
// Python's _parse_interval(raw string) exists because setting_overrides
// stores everything as text and Python has to defensively int()-parse a
// possibly-garbage string on every tick. Go's domain.Settings already
// types ScrapeIntervalHours as *int at the store boundary (see
// vestige_go_pipeline_implementation.md §7 item 1) — SQLiteStore.GetSettings
// already did that parsing once, so there's no equivalent parse-on-every-
// tick step needed here. Not a missing port, a consequence of a decision
// made earlier in this project.
package handler

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/applog"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/runlog"
)

// ErrRunInProgress mirrors api_server.py's run-already-in-flight case
// (`if _run_lock.locked(): raise HTTPException(409, ...)` on the manual
// side; `if _run_lock.locked(): return` — silent skip — on the scheduler
// side). Both triggerRun and schedulerTick check for this via the same
// executeRun call; each decides separately what to do with it (409 vs.
// silent skip), matching Python's own two different responses to the same
// underlying condition.
var ErrRunInProgress = errors.New("handler: a run is already in progress")

// executeRun is the ONE place a pipeline run actually happens — mirrors
// _execute_run() exactly, including being "shared with the scheduler...
// so the two can never overlap" (Python's own comment, still true here).
// Both triggerRun (HTTP) and schedulerTick (background) call this and
// nothing else runs the pipeline directly.
func (srv *Server) executeRun(ctx context.Context) (*pipeline.RunSummary, error) {
	if !srv.running.CompareAndSwap(false, true) {
		return nil, ErrRunInProgress
	}
	defer srv.running.Store(false)

	runID := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	runLog, err := applog.StartRun(runID)
	if err != nil {
		log.Printf("applog: StartRun failed (continuing without per-run file): %v", err)
	} else {
		defer runLog.Close()
	}

	summary, err := srv.orch.RunAll(ctx, runID)
	if err != nil {
		return nil, err
	}

	if err := writeRunLog(srv.logDir, summary); err != nil {
		// A log-write failure shouldn't be treated as the RUN having
		// failed — the scrape itself succeeded. Logged, not returned as
		// part of the error contract; simpler than triggerRun's old
		// special-cased "completed but failed to write log" HTTP response,
		// which only ever mattered to one caller (the HTTP handler) and
		// has no equivalent meaning for the scheduler's caller (nobody).
		log.Printf("[run] scrape succeeded but writing the log failed: %v", err)
	}

	now := time.Now().UTC()
	srv.lastRunAt.Store(&now)
	return summary, nil
}

// seedLastRunAt mirrors _seed_last_run_at(): reads the single most recent
// run log's run_id on startup, so a routine restart doesn't immediately
// re-fire a run that already happened minutes ago. Best-effort, same as
// Python — any failure just means the schedule starts fresh (safe: worst
// case one extra run fires sooner than strictly needed).
//
// Reuses runlog.ReadRunSummaries(logDir, 1) rather than adding a new
// exported function to runlog.go — it already does exactly this (walk,
// parse, tolerate malformed files) and already exposes RunID.
func seedLastRunAt(logDir string) *time.Time {
	summaries, err := runlog.ReadRunSummaries(logDir, 1)
	if err != nil || len(summaries) == 0 {
		return nil
	}
	t, err := time.Parse(time.RFC3339, summaries[0].RunID)
	if err != nil {
		log.Printf("[scheduler] could not parse most recent run_id %q, starting fresh: %v", summaries[0].RunID, err)
		return nil
	}
	return &t
}

// StartScheduler seeds lastRunAt then polls every 60s — mirrors
// _scheduler_loop()'s SCHEDULER_POLL_SECONDS = 60. Meant to be started as
// `go srv.StartScheduler(ctx)` from main.go; returns when ctx is
// cancelled, for graceful shutdown.
func (srv *Server) StartScheduler(ctx context.Context) {
	if seeded := seedLastRunAt(srv.logDir); seeded != nil {
		srv.lastRunAt.Store(seeded)
		log.Printf("[scheduler] seeded last run time from log history: %v", seeded.Format(time.RFC3339))
	} else {
		log.Printf("[scheduler] no prior run found, starting fresh")
	}

	const pollInterval = 60 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	log.Println("[scheduler] started, polling every 60s")
	for {
		select {
		case <-ctx.Done():
			log.Println("[scheduler] stopped")
			return
		case <-ticker.C:
			srv.schedulerTick(ctx)
		}
	}
}

// schedulerTick mirrors _scheduler_tick() branch-for-branch: skip (fast
// path) if a run is already in flight, skip if scheduling is disabled,
// skip if not yet due, otherwise execute. defer/recover mirrors
// _scheduler_loop()'s own belt-and-suspenders try/except — "nothing here
// should ever be able to kill the loop permanently" (Python's own comment).
func (srv *Server) schedulerTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[scheduler] tick panicked (recovered): %v", r)
		}
	}()

	if srv.running.Load() {
		// Fast-path mirror of Python's `if _run_lock.locked(): return`,
		// checked first, before even reading settings — executeRun's own
		// CompareAndSwap is still the real source of truth below; this is
		// purely an optimization to skip a DB read when a run's obviously
		// already in flight.
		return
	}

	settings, err := srv.store.GetSettings(ctx)
	if err != nil {
		log.Printf("[scheduler] get settings failed: %v", err)
		return
	}
	if settings.ScrapeIntervalHours == nil {
		return // disabled — matches Python's `if interval_hours is None: return`
	}
	interval := time.Duration(*settings.ScrapeIntervalHours) * time.Hour

	last := srv.lastRunAt.Load()
	due := last == nil || time.Since(*last) >= interval
	if !due {
		return
	}

	if last != nil {
		log.Printf("[scheduler] interval elapsed (every %dh, last run %s) — starting scrape run",
			*settings.ScrapeIntervalHours, last.Format(time.RFC3339))
	} else {
		log.Printf("[scheduler] no prior run recorded — starting scrape run")
	}

	if _, err := srv.executeRun(ctx); err != nil {
		if errors.Is(err, ErrRunInProgress) {
			// A manual trigger snuck in between the fast-path check above
			// and this call — silent skip, matches Python's own silent
			// `if _run_lock.locked(): return`, not an error worth logging.
			return
		}
		log.Printf("[scheduler] scheduled scrape run failed: %v", err)
	}
}
