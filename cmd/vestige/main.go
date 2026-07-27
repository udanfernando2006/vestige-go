// Command vestige is the Vestige-Go entrypoint — wires together security
// (key + cipher), the store layer, the pipeline (Crawler/Scraper/
// Discoverer/Orchestrator), and the Gin HTTP layer, then serves — now with
// the background scheduler started alongside it and graceful shutdown on
// SIGINT/SIGTERM (Ctrl+C). /health is registered inside handler.NewRouter
// itself, not here.
//
// This is genuinely new code, not a port of any single Python/Java file —
// v1 has no equivalent single entrypoint (its three processes — api,
// scraper, scraper-server — are each wired separately, mostly by Spring's
// own DI container and docker-compose, not hand-written Go). Every
// constructor call below uses REAL, source-confirmed signatures from the
// uploaded files (store.go, scraper.go, crawler.go, discovery.go,
// orchestrator.go, keystore.go, cipher.go, handler/router.go) — nothing
// here is guessed.
//
// FLAGGED PLACEHOLDERS, not silently baked in:
//   - -keydir's default is a plain relative folder. The real value should
//     be Wails' app_data_dir() equivalent, which migration blueprint §7
//     item 14 explicitly still lists as open ("not wired up until Phase
//     3+"). This flag exists so the real path can be supplied without
//     touching this file once that's decided.
//   - -db/-schema/-logdir defaults are similarly plain relative paths,
//     fine for `go run`/local dev, almost certainly wrong for a real
//     packaged Wails app — same open question.
//   - wait_time=5s / timeout=60s are NOT placeholders — these are
//     Python's real, documented defaults, confirmed directly in
//     scraper.go's own NewScraper doc comment ("Python's defaults
//     (headless=True, wait_time=5, timeout=60000ms)"), not invented here.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/handler"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/security"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

func main() {
	dbPath := flag.String("db", "vestige.db", "path to the SQLite database file")
	schemaPath := flag.String("schema", "schema.sql", "path to schema.sql (applied once, only if -db doesn't exist yet)")
	logDir := flag.String("logdir", "logs", "directory for date-nested run-log JSON files")
	keyDir := flag.String("keydir", "./.vestige-go-keys", "fallback dir for the settings-encryption key if the OS keychain is unavailable — PLACEHOLDER, see package doc comment")
	headless := flag.Bool("headless", true, "run the browser headless (Python's own default: True)")
	port := flag.String("port", "8080", "HTTP port to listen on")
	flag.Parse()

	// Real Python defaults — see package doc comment. NOT arbitrary.
	const waitTime = 5 * time.Second
	const opTimeout = 60 * time.Second

	keyB64, keySource, err := security.LoadOrGenerateKey(*keyDir)
	if err != nil {
		log.Fatalf("load encryption key: %v", err)
	}
	log.Printf("settings encryption key source: %s", keySource)

	cipher, err := security.NewSettingsCipher(keyB64)
	if err != nil {
		log.Fatalf("build settings cipher: %v", err)
	}

	if err := applySchemaIfNewDB(*dbPath, *schemaPath); err != nil {
		log.Fatalf("apply schema: %v", err)
	}

	pairStore, err := store.NewSQLiteStore(*dbPath, cipher)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	crawler := pipeline.NewCrawler(*headless, opTimeout)
	scraper := pipeline.NewScraper(*headless, waitTime, opTimeout)
	discoverer := pipeline.NewDiscoverer(pairStore, scraper)
	orch := pipeline.NewOrchestrator(pairStore, crawler, scraper, discoverer, *headless, opTimeout)

	router, srv := handler.NewRouter(pairStore, *logDir, orch, discoverer, *headless, opTimeout)

	// ctx is cancelled on SIGINT/SIGTERM (Ctrl+C, or a normal process-manager
	// stop) — the single root signal for both the scheduler goroutine's
	// lifetime and the HTTP server's graceful shutdown trigger below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go srv.StartScheduler(ctx)

	httpServer := &http.Server{Addr: ":" + *port, Handler: router}
	go func() {
		log.Printf("Vestige-Go listening on :%s (db=%s, headless=%v)", *port, *dbPath, *headless)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received, shutting down gracefully...")

	// Separate, un-cancelled context for the shutdown call itself — ctx is
	// already Done() at this point (that's why we're here), so using it
	// again would make Shutdown() return immediately without actually
	// waiting for in-flight requests to finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	log.Println("shutdown complete")
}

// applySchemaIfNewDB applies schema.sql ONLY when dbPath doesn't exist yet.
// schema.sql's CREATE TABLE statements have no IF NOT EXISTS guard — real,
// confirmed by reading the actual uploaded file — so unconditional
// re-application on every launch would fail on the second run onward.
// This is the fix: schema creation is a true one-time, first-launch event,
// same as any real installed app's first-run DB initialization, without
// requiring any change to schema.sql itself.
func applySchemaIfNewDB(dbPath, schemaPath string) error {
	if _, err := os.Stat(dbPath); err == nil {
		log.Printf("existing database found at %s, skipping schema application", dbPath)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", dbPath, err)
	}

	log.Printf("no database found at %s — applying schema from %s", dbPath, schemaPath)
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema file %s: %w", schemaPath, err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer db.Close()

	for _, stmt := range splitStatements(string(schemaBytes)) {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec statement %q: %w", stmt, err)
		}
	}
	return nil
}

// splitStatements strips "--"-style line comments (a stray semicolon
// inside a comment previously broke naive semicolon-splitting, per the
// project's own documented, already-fixed schema.sql bug) then splits on
// ";", discarding empty/whitespace-only fragments. Mirrors the exact fix
// already applied once in cmd/dbcheck (not uploaded to this project, but
// its fix is documented in vestige_go_migration_blueprint.md/pipeline
// implementation notes) — reimplemented here rather than assumed shared,
// since cmd/dbcheck's own source wasn't available to import from.
func splitStatements(schemaSQL string) []string {
	lines := strings.Split(schemaSQL, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	stripped := strings.Join(lines, "\n")

	var out []string
	for _, stmt := range strings.Split(stripped, ";") {
		s := strings.TrimSpace(stmt)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
