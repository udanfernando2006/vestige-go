//go:build !server

// Command vestige is the Vestige-Go desktop entrypoint (the default build —
// excluded from server-mode builds via the constraint above, since
// main_server.go provides its own main() for -tags server and two main()
// declarations compiling together is a real error, not a style choice).
// Wires together security (key + cipher), the store layer, the pipeline
// (Crawler/Scraper/Discoverer/Orchestrator), and the Gin HTTP layer, then
// serves — now with the background scheduler started alongside it, wrapped
// as a Wails v3 desktop app. /health is registered inside handler.NewRouter
// itself, not here. applySchemaIfNewDB/splitStatements now live in
// schema.go (shared, no build tag) — main_server.go uses the same two.
//
// WAILS INTEGRATION (this pass's changes, everything else below is
// unchanged from the prior version of this file):
//   - -port's default changed from "8080" to "0" — the Gin listener now
//     binds an OS-assigned ephemeral port by default (net.Listen("tcp",
//     "127.0.0.1:0")), specifically so nothing the user already has running
//     can collide with it. A fixed port is still supported by passing
//     -port explicitly (relevant for a future server-mode/headless build
//     target — see Taskfile.yml's build:server/run:server tasks, not yet
//     investigated).
//   - APIService (cmd/vestige/apiservice.go) is bound via Wails' Services
//     list and exposes GetAPIPort() — the frontend calls this once at
//     startup (in-process IPC, no HTTP round-trip) to learn which port the
//     Gin server actually landed on. See ui/api/settings.ts.
//   - Shutdown ownership moved from a standalone signal.NotifyContext
//     blocking-wait to Wails' own OnShutdown lifecycle hook (confirmed
//     real API: v3.wails.io/concepts/lifecycle/ — "OnShutdown - A callback
//     for when the application is about to quit", fired regardless of
//     whether the quit came from window-close, Cmd+Q/Alt+F4, or a
//     programmatic app.Quit() call). OS signals (Ctrl+C in a dev terminal,
//     SIGTERM from a process manager) now route THROUGH app.Quit() instead
//     of bypassing Wails' shutdown sequence — a small goroutine watches
//     signal.NotifyContext purely to call app.Quit(), deliberately only
//     started after app.Window.NewWithOptions (not before app.Run()),
//     since Wails' own changelog documents a real "nil pointer crash in
//     application.Quit when called before Run or after Run returned early"
//     bug class.
//   - The pipeline scheduler's context is now a plain context.WithCancel,
//     cancelled from inside OnShutdown — decoupled from OS-signal delivery
//     specifically because window-close/Cmd+Q are now valid shutdown
//     triggers that were never OS signals to begin with.
//
// FLAGGED PLACEHOLDERS, not silently baked in (unchanged from before):
//   - -keydir's default is a plain relative folder. The real value should
//     be Wails' app_data_dir() equivalent, which migration blueprint §7
//     item 14 explicitly still lists as open ("not wired up until Phase
//     3+"). Still open — not resolved by this pass.
//   - -db/-schema/-logdir defaults are similarly plain relative paths,
//     fine for `go run`/local dev, almost certainly wrong for a real
//     packaged Wails app — same open question.
//   - wait_time=5s / timeout=60s are NOT placeholders — Python's real,
//     documented defaults, confirmed in scraper.go's own doc comment.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	appicon "github.com/udanfernando2006/vestige-go/build"
	frontendassets "github.com/udanfernando2006/vestige-go/frontend"
	"github.com/udanfernando2006/vestige-go/internal/handler"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/security"
	"github.com/udanfernando2006/vestige-go/internal/store"
	"github.com/udanfernando2006/vestige-go/internal/tray"
)

//go:embed assets/tray-icon.png
var trayIcon []byte

func main() {
	dbPath := flag.String("db", "vestige.db", "path to the SQLite database file")
	schemaPath := flag.String("schema", "schema.sql", "path to schema.sql (applied once, only if -db doesn't exist yet)")
	logDir := flag.String("logdir", "logs", "directory for date-nested run-log JSON files")
	keyDir := flag.String("keydir", "./.vestige-go-keys", "fallback dir for the settings-encryption key if the OS keychain is unavailable — PLACEHOLDER, see package doc comment")
	headless := flag.Bool("headless", true, "run the browser headless (Python's own default: True)")
	port := flag.String("port", "0", "HTTP port for the Gin API to bind (0 = OS-assigned ephemeral port, the default for the desktop build)")
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

	// Ephemeral by default (-port 0): bind now so the real port is known
	// before APIService is constructed below. A fixed port (if -port was
	// passed explicitly) is recovered the same way, via listener.Addr().
	listener, err := net.Listen("tcp", "127.0.0.1:"+*port)
	if err != nil {
		log.Fatalf("bind API listener: %v", err)
	}
	apiPort := listener.Addr().(*net.TCPAddr).Port

	httpServer := &http.Server{Handler: router}
	go func() {
		log.Printf("Vestige-Go API listening on 127.0.0.1:%d (db=%s, headless=%v)", apiPort, *dbPath, *headless)
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Decoupled from OS-signal delivery on purpose — window-close and Cmd+Q/
	// Alt+F4 are now valid shutdown triggers that were never OS signals to
	// begin with, and both need to stop the scheduler too, not just an
	// explicit Ctrl+C. Cancelled from inside OnShutdown below.
	schedulerCtx, cancelScheduler := context.WithCancel(context.Background())
	go srv.StartScheduler(schedulerCtx)

	apiService := &APIService{port: apiPort}
	notifier := notifications.New()

	var mainWindow *application.WebviewWindow

	app := application.New(application.Options{
		Name:        "Vestige",
		Description: "Book price and availability tracker",
		Icon:        appicon.IconPNG,
		ShouldQuit: func() bool {
			return true // tray Quit / Cmd+Q / Alt+F4 / app.Quit() all proceed; window-hide is handled separately by the WindowClosing hook, not here
		},
		Services: []application.Service{
			application.NewService(apiService),
			application.NewService(notifier),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(frontendassets.Assets),
		},
		OnShutdown: func() {
			log.Println("shutdown requested, shutting down gracefully...")
			// mainWindow.Close() removed — Wails closes windows automatically
			// as part of its own shutdown sequence (step 4, after OnShutdown
			// per the documented lifecycle), so calling Close() here raced
			// against that and double-closed window #1.
			cancelScheduler()

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				log.Printf("graceful shutdown error: %v", err)
			}
			log.Println("shutdown complete")
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "io.github.udanfernando2006.vestigego",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				tray.ShowAndFocus(mainWindow)
			},
		},
	})

	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Vestige",
		Width:  1280,
		Height: 800,
		URL:    "/",
	})

	tray.Setup(app, mainWindow, trayIcon)

	// Route OS signals (Ctrl+C in a dev terminal, SIGTERM from a process
	// manager) through Wails' own quit sequence rather than bypassing it —
	// app.Quit() triggers the same OnShutdown callback above, so there's
	// exactly one graceful-shutdown code path regardless of trigger.
	// Started only after the window exists, not before app.Run(): Wails'
	// own changelog documents a real nil-pointer crash class when Quit is
	// called before Run has actually started or after Run has already
	// returned.
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-sigCtx.Done()
		app.Quit()
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
