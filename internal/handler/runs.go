// Runs handler — Gin equivalent of RunController.java + RunService.java.
//
// Both endpoints are now fully wired against real, uploaded source
// (orchestrator.go's RunAll, discovery.go's Run, session.go's NewSession,
// scraper.go's NewScraper — nothing here is fabricated from prose).
//
// ONE STRUCTURAL SIMPLIFICATION worth naming, not hiding: Java's
// RunService.trigger() POSTs to a separate scraper-server process, then
// re-reads the freshest log file to get the result back — that round-trip
// exists ONLY because Java has no in-process access to the Python run's
// result. Vestige-Go's RunAll() returns the full *pipeline.RunSummary
// directly, in-process — so triggerRun below builds its HTTP response
// straight from that return value and does NOT re-read the log file it
// just wrote. The log file is still written (runlog.WriteRunLog), so
// GET /api/runs / GET /api/runs/{runId} see it exactly the same as before
// and after a process restart — this is a latency/complexity win, not a
// behavioral gap.
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/runlog"
)

func (srv *Server) getRecentRuns(c *gin.Context) {
	const defaultLimit = 50 // matches RunService.getRecentRuns()'s hardcoded .limit(50)
	summaries, err := runlog.ReadRunSummaries(srv.logDir, defaultLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: " + err.Error()})
		return
	}
	out := make([]RunSummaryResponse, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, RunSummaryResponse{
			RunID: s.RunID, TotalPairs: s.TotalPairs, Changes: s.Changes,
			Errors: s.Errors, DurationSeconds: s.DurationSeconds, LogPath: s.LogPath,
		})
	}
	c.JSON(http.StatusOK, out)
}

func (srv *Server) getRunDetail(c *gin.Context) {
	runID := c.Param("runId")
	detail, err := runlog.FindRunDetail(srv.logDir, runID)
	if err != nil {
		if err == runlog.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("Run not found: %s", runID)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: " + err.Error()})
		return
	}
	changes := make([]RunChangeResponse, 0, len(detail.Changes))
	for _, rc := range detail.Changes {
		changes = append(changes, RunChangeResponse{
			PairID: rc.PairID, BookName: rc.BookName, StoreName: rc.StoreName,
			FromStatus: rc.FromStatus, ToStatus: rc.ToStatus,
			FromPrice: rc.FromPrice, ToPrice: rc.ToPrice, ProductURL: rc.ProductURL,
		})
	}
	c.JSON(http.StatusOK, RunDetailResponse{
		RunID: detail.RunID, TotalPairs: detail.TotalPairs, Errors: detail.Errors,
		DurationSeconds: detail.DurationSeconds, Changes: changes,
	})
}

// triggerRun mirrors RunController.trigger() -> RunService.trigger(), with
// the log-re-read step removed — see package doc comment. Delegates the
// actual run execution to executeRun (scheduler.go), the single shared
// entry point both this handler and the background scheduler call — so a
// manual click and a scheduled tick can never run concurrently, matching
// api_server.py's _execute_run() being shared the same way.
func (srv *Server) triggerRun(c *gin.Context) {
	summary, err := srv.executeRun(c.Request.Context())
	if err != nil {
		if errors.Is(err, ErrRunInProgress) {
			c.JSON(http.StatusConflict, gin.H{"error": "A run is already in progress"})
			return
		}
		// Mirrors RunController's own PipelineExecutionException -> 500
		// local handler.
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Pipeline run failed: " + err.Error()})
		return
	}

	// LogPath here is logDir-joined (absolute-from-logDir), not relative the
	// way runlog.ReadRunSummaries' listing returns it — this is a fresh
	// write, not a listing, so there's no logDir to relativize against in
	// the same call. Minor, flagged asymmetry rather than a silent one.
	logPath, _ := runlog.BuildLogPath(srv.logDir, summary.RunID)
	c.JSON(http.StatusOK, RunSummaryResponse{
		RunID: summary.RunID, TotalPairs: summary.TotalPairs,
		Changes: len(summary.Changes), Errors: len(summary.ErrorEntries),
		DurationSeconds: summary.DurationSeconds, LogPath: logPath,
	})
}

// writeRunLog maps *pipeline.RunSummary's Go-field-named struct into the
// exact snake_case JSON shape runlog.go's read side (ReadRunSummaries/
// FindRunDetail, source-verified against RunService.java) expects back —
// built by hand rather than relying on Change/RunError's default Go JSON
// marshaling, which would produce PascalCase keys ("PairID" not "pair_id")
// since those structs (orchestrator.go, uploaded/read-only) carry no json
// tags. Getting this wrong would silently break every run this process
// writes from ever being read back correctly. Called from executeRun
// (scheduler.go), not directly from this handler anymore.
func writeRunLog(logDir string, summary *pipeline.RunSummary) error {
	changes := make([]map[string]any, 0, len(summary.Changes))
	for _, ch := range summary.Changes {
		changes = append(changes, map[string]any{
			"pair_id": ch.PairID, "book_name": ch.BookName, "store_name": ch.StoreName,
			"product_url": ch.ProductURL, "from_status": ch.FromStatus, "to_status": ch.ToStatus,
			"from_price": ch.FromPrice, "to_price": ch.ToPrice,
		})
	}
	errs := make([]map[string]any, 0, len(summary.ErrorEntries))
	for _, e := range summary.ErrorEntries {
		errs = append(errs, map[string]any{"pair_id": e.PairID, "reason": e.Reason})
	}
	return runlog.WriteRunLog(logDir, map[string]any{
		"run_id":           summary.RunID,
		"total_pairs":      summary.TotalPairs,
		"completed":        summary.Completed,
		"needs_setup":      summary.NeedsSetup,
		"changes":          changes,
		"errors":           errs,
		"duration_seconds": summary.DurationSeconds,
	})
}

// discover mirrors RunController.discover() -> RunService.discover():
// opens ONE top-level browser.Session for the initial fetch (mirrors
// discover_selectors.py's own top-level `async with BrowserSession()`),
// calls the already-built/verified pipeline.Discoverer, and — critically —
// never passes Commit: true, matching the documented behavior that the
// on-demand Discover button never validates-and-writes the way Path B's
// pipeline call always does (see vestige_archive_history_catalog.md Part B
// / vestige_api_implementation.md's note that "/discover never passes
// --commit").
func (srv *Server) discover(c *gin.Context) {
	pairID, ok := parseID(c, "pairId")
	if !ok {
		return
	}

	ctx := c.Request.Context()
	session, err := browser.NewSession(ctx, srv.headless, srv.sessionTO)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Could not open browser session: " + err.Error()})
		return
	}
	defer closeSessionQuietly(ctx, session)

	result, err := srv.discoverer.Run(ctx, session, pipeline.DiscoverOptions{
		PairID: pairID,
		Commit: false, // on-demand button never commits — see doc comment above
	})
	if err != nil {
		// Mirrors RunController's SelectorDiscoveryException -> 422 local
		// handler.
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Discovery failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, DiscoverResultResponse{
		PairID: result.PairID, PriceSelector: result.PriceSelector, StockSelector: result.StockSelector,
		PriceSample: result.PriceSample, StockSample: result.StockSample,
		ModelUsed: result.ModelUsed, Reason: result.Reason, Committed: result.Committed,
	})
}

// closeSessionQuietly best-effort closes the discovery session — a close
// failure here shouldn't ever mask a real discovery result/error the
// handler has already decided on, so this only logs, matching the general
// spirit of Python's `async with` cleanup never surfacing a close error to
// the HTTP caller.
func closeSessionQuietly(ctx context.Context, s browser.Session) {
	if err := s.Close(ctx); err != nil {
		fmt.Printf("[discover] session close error (non-fatal): %v\n", err)
	}
}
