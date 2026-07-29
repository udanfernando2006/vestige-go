package handler

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

// Server holds the dependencies every handler needs. Deliberately just the
// store plus what Runs/Discover need — there's no separate service layer
// in this port (explicit user decision, see resources.go's package doc
// comment), so handlers call store.PairStore / pipeline.Orchestrator
// directly, the way this project's own Java Controllers call a Service.
type Server struct {
	store      store.PairStore
	logDir     string
	orch       *pipeline.Orchestrator
	discoverer *pipeline.Discoverer
	headless   bool
	sessionTO  time.Duration

	// running guards executeRun (scheduler.go) against overlapping calls —
	// mirrors api_server.py's _run_lock (asyncio.Lock), shared between the
	// manual /trigger endpoint AND the background scheduler so the two can
	// never run concurrently, exactly matching Python's own comment on
	// _execute_run(). Implemented with atomic.Bool.CompareAndSwap instead
	// of Python's check-then-lock — same intended behavior (one run at a
	// time; the loser gets 409 if it was an HTTP call, a silent skip if it
	// was a scheduler tick), race-free by construction.
	running atomic.Bool

	// lastRunAt tracks when a run last actually completed — mirrors
	// api_server.py's module-level _last_run_at, seeded on startup from
	// log history (seedLastRunAt) and updated by every executeRun call
	// regardless of what triggered it (manual or scheduled), same as
	// Python's single source of truth. atomic.Pointer for lock-free
	// concurrent access from both HTTP handler goroutines and the
	// scheduler goroutine.
	lastRunAt atomic.Pointer[time.Time]
}

// NewRouter builds the full Gin engine — routes, CORS, everything a
// caller needs to r.Run(":8080") (or whatever port) against.
//
//   - logDir: where runlog.go reads/writes date-nested run-log JSON files
//     (mirrors vestige.log-dir / LOG_DIR).
//   - orch: a fully-constructed *pipeline.Orchestrator — this package does
//     NOT construct Crawler/Scraper/Discoverer/Orchestrator itself. That
//     wiring (NewCrawler(...)/NewScraper(...)/NewDiscoverer(...)/
//     NewOrchestrator(...)) belongs in the application's entrypoint
//     (cmd/vestige/main.go or equivalent), not the HTTP layer — same
//     reasoning NewOrchestrator itself already applies (shared,
//     already-configured instances reused across every pair/run, not
//     rebuilt per call). NewCrawler's exact signature was never uploaded
//     to this project, which is precisely why it isn't called from here.
//   - discoverer: passed separately from orch even though Orchestrator
//     already holds one internally (it's an unexported field, orch.discoverer,
//     not reachable from this package) — the on-demand "Discover" button
//     endpoint needs its own reference, matching how discovery.go's own
//     doc comment describes it being "called identically from two places."
//   - headless/sessionTimeout: config for the one top-level browser.Session
//     the discover() handler opens per on-demand call — same two knobs
//     NewOrchestrator/NewScraper/NewSession already take explicitly, Go
//     having no default-parameter equivalent for Python's bare
//     `BrowserSession()`.
//
// Returns both the engine (for router.Run()/http.Server{Handler: router})
// and *Server itself, since main.go needs the latter to start the
// scheduler (go srv.StartScheduler(ctx)) — NewRouter no longer starts any
// background goroutine as a side effect of "building a router" (a router
// silently spawning long-lived goroutines would be a surprising side
// effect); the caller decides that explicitly.
func NewRouter(s store.PairStore, logDir string, orch *pipeline.Orchestrator, discoverer *pipeline.Discoverer, headless bool, sessionTimeout time.Duration) (*gin.Engine, *Server) {
	srv := &Server{
		store: s, logDir: logDir, orch: orch, discoverer: discoverer,
		headless: headless, sessionTO: sessionTimeout,
	}

	r := gin.Default()
	r.Use(corsMiddleware())

	// Top-level, not under /api — mirrors api_server.py's GET /health
	// exactly (Python's is also bare, not under any prefix). Referenced by
	// vestige_go_ui_implementation.md §3 as the shape Wails' first-run
	// readiness gate will likely probe, the same way v1's Tauri shell
	// probed Java's /actuator/health.
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	{
		books := api.Group("/books")
		books.GET("", srv.listBooksGrouped)
		books.POST("", srv.createBook)
		books.PATCH("/series", srv.bulkAssignSeries) // must precede /:id for the same reason Java's needs-setup route did — see router doc note below
		books.PATCH("/:id", srv.updateBook)
		books.DELETE("/:id", srv.deleteBook)

		series := api.Group("/series")
		series.GET("", srv.listSeries)
		series.POST("", srv.createSeries)
		series.PATCH("/:id", srv.updateSeries)
		series.DELETE("/:id", srv.deleteSeries)

		stores := api.Group("/stores")
		stores.GET("", srv.listStores)
		stores.POST("", srv.createStore)
		stores.PATCH("/:id", srv.updateStore)
		stores.DELETE("/:id", srv.deleteStore)

		tracking := api.Group("/tracking")
		tracking.GET("", srv.listTrackingPairs)
		tracking.GET("/needs-setup", srv.listNeedsSetup) // Gin's router handles this literal-vs-:id precedence automatically — see router doc note
		tracking.POST("", srv.createTrackingPair)
		tracking.PATCH("/:id", srv.updateTrackingPair)

		availability := api.Group("/availability")
		availability.GET("", srv.currentAvailability)
		availability.GET("/history", srv.availabilityHistory)
		availability.DELETE("/:id", srv.deleteSnapshot)
		availability.DELETE("/pair/:pairId", srv.deleteHistoryForPair)

		settings := api.Group("/settings")
		settings.GET("", srv.getSettings)
		settings.PUT("", srv.updateSettings)

		runs := api.Group("/runs")
		runs.GET("", srv.getRecentRuns)
		runs.GET("/status", srv.getRunStatus)
		runs.GET("/:runId", srv.getRunDetail)
		runs.POST("/trigger", srv.triggerRun)
		runs.POST("/discover/:pairId", srv.discover)
	}

	return r, srv
}

// Note on route ordering (books' /series vs /:id, tracking's /needs-setup
// vs /:id): Gin's underlying router (httprouter-style radix tree) resolves
// a literal path segment ("series", "needs-setup") over a same-position
// :param wildcard automatically, regardless of registration order — unlike
// Spring MVC, which required /needs-setup to be declared textually before
// /{id} in TrackingController.java or it would be matched as a path
// variable and throw a type-conversion error. Registered in a sensible
// order above for readability, not because Gin requires it — flagged here
// so this isn't mistaken for a copy-paste of Java's actual constraint.

// corsMiddleware mirrors CorsConfig.java: allowed origin patterns
// http://localhost:*, http://tauri.localhost, tauri://localhost; methods
// GET/POST/PUT/PATCH/DELETE/OPTIONS; all headers. Hand-rolled rather than
// pulling in gin-contrib/cors, to avoid an extra go-get dependency for
// three straightforward pattern checks.
//
// STILL RELEVANT, not dead weight: even though the backend is now
// co-located with the frontend in one binary (per vestige_go_ui_implementation.md
// §4, which flags v1's *wildcard-port* CSP scoping as removable since
// there's no other deployment target to point at) — during development the
// Vite dev server still runs on its own localhost port, separate from this
// Gin server's port, so cross-origin requests are still real during `wails
// dev`. Worth revisiting once Phase 3's actual dev workflow is nailed down;
// not removed preemptively here.
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if isAllowedOrigin(origin) {
			c.Header("Access-Control-Allow-Origin", origin)
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}

func isAllowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	switch {
	case strings.HasPrefix(origin, "http://localhost:"):
		return true
	case strings.HasPrefix(origin, "http://127.0.0.1:"):
		return true
	case strings.HasPrefix(origin, "http://wails.localhost"):
		return true
	default:
		return false
	}
}
