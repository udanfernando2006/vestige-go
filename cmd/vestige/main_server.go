//go:build server

// Command vestige (server mode) is the headless counterpart to main.go's
// desktop build — no Wails, no window, no Chromium-bundling concerns beyond
// what the pipeline itself needs. This is essentially your original,
// pre-Wails main.go's own logic (plain http.Server + signal.NotifyContext-
// based graceful shutdown) preserved almost unchanged, just gated behind
// the "server" build tag instead of being the package's only entrypoint —
// see Taskfile.yml's build:server/run:server/build:docker tasks, and
// run:docker's own comment ("the internal container port is always 8080")
// confirming a FIXED default port is correct here, unlike main.go's
// ephemeral :0 default (a real deployed server needs a knowable port to
// point a reverse proxy or firewall rule at).
//
// Duplicates main.go's key/cipher/store/pipeline/router construction rather
// than sharing it through a helper — deliberately: handler.NewRouter's own
// return types weren't fully known here (only inferred locally via :=), so
// writing a shared function's signature would mean guessing at a type
// rather than confirming it, which is worse than the ~30 lines of
// duplication this avoids. Revisit if/when internal/handler's real
// signatures are confirmed directly.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/handler"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/security"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

func main() {
	dataDir, err := defaultDataDir()
	if err != nil {
		log.Fatalf("resolve data dir: %v", err)
	}

	dbPath := flag.String("db", filepath.Join(dataDir, "vestige.db"), "path to the SQLite database file")
	schemaPath := flag.String("schema", "schema.sql", "path to schema.sql (applied once, only if -db doesn't exist yet); falls back to embedded schema if missing")
	logDir := flag.String("logdir", filepath.Join(dataDir, "logs"), "directory for date-nested run-log JSON files")
	keyDir := flag.String("keydir", filepath.Join(dataDir, "keys"), "fallback dir for the settings-encryption key if the OS keychain is unavailable")
	headless := flag.Bool("headless", true, "run the browser headless (Python's own default: True)")
	port := flag.String("port", "8080", "HTTP port to listen on — fixed by default in server mode, unlike the desktop build's ephemeral port")
	flag.Parse()

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go srv.StartScheduler(ctx)

	httpServer := &http.Server{Addr: ":" + *port, Handler: router}
	go func() {
		log.Printf("Vestige-Go (server mode) listening on :%s (db=%s, headless=%v)", *port, *dbPath, *headless)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received, shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	log.Println("shutdown complete")
}
