// Command discovercheck is a live diagnostic tool for
// internal/pipeline.Discoverer — mirrors the existing cmd/dbcheck /
// cmd/extractorcheck / cmd/scrapercheck / cmd/crawlercheck pattern. It
// exercises Discoverer.Run end-to-end against a REAL SQLite store, a REAL
// browser session, and a REAL LLM backend — nothing mocked.
//
// This tool sits at the intersection of dbcheck and extractorcheck and
// borrows directly from both real, uploaded source files rather than
// guessing at either half:
//   - SQLite wiring (schema application, store.NewSQLiteStore, the
//     cipher/keychain options) is adapted from cmd/dbcheck/main.go.
//   - Browser/LLM wiring (.env.test loading, browser.NewSession,
//     pipeline.NewScraper, the -headless/-wait-time/-scrape-timeout flag
//     family) is adapted from cmd/extractorcheck's main.go.
//
// cmd/dbcheck and cmd/extractorcheck are separate `main` packages, so
// nothing is importable from either — applySchema/stripLineComments/
// firstNonEmpty/printJSON/etc. below are deliberately re-declared here
// rather than shared, matching how each cmd/*check tool in this project
// is already self-contained.
//
// ONE IMPORTANT DIVERGENCE FROM Discoverer.Run's OWN real behavior, worth
// stating up front: Run() itself NEVER reads an environment variable —
// every credential comes from store.PairStore.GetSettings(), by design
// (see discovery.go's package doc comment divergence #1). This tool
// still loads .env.test / real env vars as a CONVENIENCE for populating
// -selector-api-base/-selector-api-key/-selector-model when those flags
// are left blank, but the actual bytes Discoverer.Run sees always come
// from a real setting_overrides row this tool seeds first — Run is never
// special-cased to accept env vars directly. This keeps the tool
// exercising the real production code path, not a shortcut around it.
//
// Two modes, mutually exclusive via -pair-id:
//
//   - FRESH mode (-pair-id=0, the default): applies -schema to -db
//     (deleting -db first if -fresh), seeds one throwaway book/store/pair
//     with -product-url as its product_url, and seeds SELECTOR_* settings
//     resolved from -selector-*/env. Safe to run repeatedly against a
//     scratch file.
//   - EXISTING-PAIR mode (-pair-id > 0): opens -db as-is (assumed already
//     schema-applied — e.g. point this at your real app_data_dir()
//     database) and uses the tracking pair already there. Settings are
//     seeded ONLY if you explicitly pass -selector-api-base/-selector-
//     api-key/-selector-model/-custom-stock-in/-custom-stock-out flags —
//     env-var fallback is deliberately NOT applied to overwrite an
//     existing real database's settings just because a stray .env.test
//     value happened to resolve. This is a real safety choice, not an
//     oversight: silently mutating a real dev DB's settings from an
//     unrelated .env file would be a nasty surprise. If -db already has
//     SELECTOR_* configured (e.g. via a real Settings UI once Phase 3
//     exists, or via cmd/dbcheck -settings), this mode reads that as-is.
//
// Usage:
//
//	# Fresh throwaway DB, unvalidated suggestion only:
//	go run ./cmd/discovercheck -product-url "https://jumpbooks.lk/product/..." -title "Some Book"
//
//	# Fresh throwaway DB, full --commit validation via the real Scraper:
//	go run ./cmd/discovercheck -product-url "https://jumpbooks.lk/product/..." -title "Some Book" -commit
//
//	# Against an existing real dev DB and pair, using whatever settings are already stored there:
//	go run ./cmd/discovercheck -pair-id 3 -db ./vestige.sqlite -commit
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/security"
	"github.com/udanfernando2006/vestige-go/internal/store"

	_ "modernc.org/sqlite"
)

// envFile matches cmd/extractorcheck's own convention exactly (same
// name, same best-effort-not-fatal loading behavior, same reasoning for
// why it's ".env.test" and not ".env"/".env.test.go") — see that file's
// own doc comment for the full explanation, not repeated here.
const envFile = ".env.test"

func main() {
	if err := godotenv.Load(envFile); err != nil {
		fmt.Fprintf(os.Stderr, "discovercheck: note: could not load %s (%v) — falling back to real environment variables only\n", envFile, err)
	}

	var (
		dbPath  = flag.String("db", "./discovercheck.sqlite", "path to a SQLite file")
		schema  = flag.String("schema", "./schema.sql", "path to schema.sql (fresh mode only)")
		fresh   = flag.Bool("fresh", true, "fresh mode only: delete -db before applying schema, for a clean slate each time")
		pairID  = flag.Int64("pair-id", 0, "use an EXISTING tracking pair in -db instead of seeding a throwaway one (0 = fresh mode)")
		commit  = flag.Bool("commit", false, "pass Commit=true to Discoverer.Run — validates via the real Scraper.Scrape and writes to the DB on success")

		// Fresh-mode-only seed fixtures.
		productURL = flag.String("product-url", "", "fresh mode only, REQUIRED: real product page URL to seed the throwaway pair with")
		title      = flag.String("title", "Test Book", "fresh mode only: book title, passed through as the LLM prompt's title context")
		isbn       = flag.String("isbn", "9781473231061", "fresh mode only: book ISBN (cosmetic — Discoverer doesn't use it)")
		storeName  = flag.String("store-name", "Test Store", "fresh mode only: store name")
		storeBase  = flag.String("store-base-url", "https://example.lk", "fresh mode only: store base_url")

		// Settings resolution — see the package doc comment's safety note
		// on why env-var fallback is fresh-mode-only for these.
		selectorAPIBaseFlag = flag.String("selector-api-base", "", "SELECTOR_API_BASE to seed (fresh mode: falls back to env; existing-pair mode: only used if explicitly set)")
		selectorAPIKeyFlag  = flag.String("selector-api-key", "", "SELECTOR_API_KEY to seed (same fallback rules as -selector-api-base)")
		selectorModelFlag   = flag.String("selector-model", "", "SELECTOR_MODEL to seed (same fallback rules as -selector-api-base)")
		customStockIn       = flag.String("custom-stock-in", "", "CUSTOM_STOCK_IN_PATTERNS to seed, comma-separated regex (optional, either mode)")
		customStockOut      = flag.String("custom-stock-out", "", "CUSTOM_STOCK_OUT_PATTERNS to seed, comma-separated regex (optional, either mode)")

		// Cipher sourcing — mirrors cmd/dbcheck's -settings (here:
		// "throwaway") and -keychain (here: "keychain") modes exactly,
		// collapsed into one flag since discovercheck never needs
		// dbcheck's own bare cipher-round-trip-proof mode.
		cipherMode = flag.String("cipher", "none", `how to source a cipher for SELECTOR_API_KEY: "none" (store/read plaintext), "throwaway" (ephemeral key, this run only), or "keychain" (security.LoadOrGenerateKey — use this against a real dev DB whose settings were encrypted with the real persisted key)`)
		keyDir     = flag.String("keydir", "./discovercheck-keydir", "TEST-ONLY stand-in for the not-yet-decided app_data_dir() fallback path, used only when -cipher=keychain — never use this constant in real app code")

		headless  = flag.Bool("headless", true, "run the browser session headless")
		waitTimeF = flag.Duration("wait-time", 5*time.Second, "Scraper's wait_time / rate-limit-wait config")
		scrapeTOF = flag.Duration("scrape-timeout", 60*time.Second, "Scraper's per-navigation timeout")
		timeout   = flag.Duration("timeout", 90*time.Second, "overall context timeout, also passed to browser.NewSession")
		diag      = flag.Bool("diag", false, "print resolved settings (API key masked) and the pair's before/after state")
	)
	flag.Parse()

	if *pairID == 0 && strings.TrimSpace(*productURL) == "" {
		fatalUsage("-product-url is required in fresh mode (pass -pair-id to use an existing pair instead)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// --- Fresh mode: apply schema + seed a throwaway book/store/pair ---

	isFresh := *pairID == 0
	resolvedPairID := *pairID
	if isFresh {
		if *fresh {
			os.Remove(*dbPath)
		}
		if err := applySchema(*dbPath, *schema); err != nil {
			fatal("apply schema", err)
		}
		resolvedPairID = seedFixtures(*dbPath, *title, *isbn, *storeName, *storeBase, *productURL)
		fmt.Printf("[discovercheck] fresh mode: seeded pair_id=%d, product_url=%s\n", resolvedPairID, *productURL)
	} else {
		fmt.Printf("[discovercheck] existing-pair mode: using pair_id=%d in %s (schema/seed flags ignored)\n", resolvedPairID, *dbPath)
	}

	// --- Cipher sourcing ---

	var cipher store.Cipher
	switch *cipherMode {
	case "none":
		// no cipher — seeded settings (if any) are written plaintext,
		// and reading a real DB's already-encrypted rows would fail
		// with store.ErrCipherRequired, same as production would.
	case "throwaway":
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			fatal("generate throwaway key", err)
		}
		keyB64 := base64.URLEncoding.EncodeToString(key)
		c, err := security.NewSettingsCipher(keyB64)
		if err != nil {
			fatal("build throwaway cipher", err)
		}
		cipher = c
		fmt.Println("[discovercheck] using a throwaway encryption key for this run only")
	case "keychain":
		keyB64, src, err := security.LoadOrGenerateKey(*keyDir)
		if err != nil {
			fatal("LoadOrGenerateKey", err)
		}
		fmt.Printf("[discovercheck] LoadOrGenerateKey source: %s (fallback dir: %s)\n", src, *keyDir)
		c, err := security.NewSettingsCipher(keyB64)
		if err != nil {
			fatal("build cipher from keychain-sourced key", err)
		}
		cipher = c
	default:
		fatalUsage(fmt.Sprintf("unknown -cipher %q (want none|throwaway|keychain)", *cipherMode))
	}

	// --- Settings seeding ---
	//
	// Fresh mode: env fallback applies, and a resolved api-base/model is
	// mandatory (mirrors extractorcheck's own "no API base/model
	// resolved" fatal check) since a throwaway DB starts with zero
	// settings rows.
	//
	// Existing-pair mode: ONLY the raw flags (no env fallback) can
	// trigger a seed/overwrite — see the package doc comment's safety
	// note for why.
	if isFresh {
		apiBase := firstNonEmpty(*selectorAPIBaseFlag, os.Getenv("SELECTOR_API_BASE"))
		apiKey := firstNonEmpty(*selectorAPIKeyFlag, os.Getenv("SELECTOR_API_KEY"))
		model := firstNonEmpty(*selectorModelFlag, os.Getenv("SELECTOR_MODEL"))
		if apiBase == "" || model == "" {
			fatalUsage("no SELECTOR_API_BASE/SELECTOR_MODEL resolved — set them in .env.test, or pass -selector-api-base/-selector-model explicitly")
		}
		must("seed SELECTOR_API_BASE", seedSetting(*dbPath, nil, "SELECTOR_API_BASE", apiBase, false))
		must("seed SELECTOR_API_KEY", seedSetting(*dbPath, cipher, "SELECTOR_API_KEY", apiKey, cipher != nil && apiKey != ""))
		must("seed SELECTOR_MODEL", seedSetting(*dbPath, nil, "SELECTOR_MODEL", model, false))
		if *customStockIn != "" {
			must("seed CUSTOM_STOCK_IN_PATTERNS", seedSetting(*dbPath, nil, "CUSTOM_STOCK_IN_PATTERNS", *customStockIn, false))
		}
		if *customStockOut != "" {
			must("seed CUSTOM_STOCK_OUT_PATTERNS", seedSetting(*dbPath, nil, "CUSTOM_STOCK_OUT_PATTERNS", *customStockOut, false))
		}
	} else {
		// existing-pair mode: flags only, no env fallback
		if *selectorAPIBaseFlag != "" {
			must("seed SELECTOR_API_BASE", seedSetting(*dbPath, nil, "SELECTOR_API_BASE", *selectorAPIBaseFlag, false))
		}
		if *selectorAPIKeyFlag != "" {
			must("seed SELECTOR_API_KEY", seedSetting(*dbPath, cipher, "SELECTOR_API_KEY", *selectorAPIKeyFlag, cipher != nil))
		}
		if *selectorModelFlag != "" {
			must("seed SELECTOR_MODEL", seedSetting(*dbPath, nil, "SELECTOR_MODEL", *selectorModelFlag, false))
		}
		if *customStockIn != "" {
			must("seed CUSTOM_STOCK_IN_PATTERNS", seedSetting(*dbPath, nil, "CUSTOM_STOCK_IN_PATTERNS", *customStockIn, false))
		}
		if *customStockOut != "" {
			must("seed CUSTOM_STOCK_OUT_PATTERNS", seedSetting(*dbPath, nil, "CUSTOM_STOCK_OUT_PATTERNS", *customStockOut, false))
		}
	}

	// --- Open the real store ---

	s, err := store.NewSQLiteStore(*dbPath, cipher)
	if err != nil {
		fatal("open store", err)
	}
	defer s.Close()

	pairBefore, err := s.GetPair(ctx, resolvedPairID)
	must("GetPair (before)", err)
	if pairBefore == nil {
		fatal("resolve pair", fmt.Errorf("no tracking pair with id=%d in %s", resolvedPairID, *dbPath))
	}
	fmt.Println("\n=== Pair (before) ===")
	dump(pairBefore)

	settings, err := s.GetSettings(ctx)
	must("GetSettings", err)
	if *diag {
		fmt.Println("\n=== Resolved settings (API key masked) ===")
		fmt.Printf("SelectorAPIBase=%q SelectorModel=%q SelectorAPIKey=%s CustomStockInPatterns=%v CustomStockOutPatterns=%v\n",
			settings.SelectorAPIBase, settings.SelectorModel, maskSecret(settings.SelectorAPIKey),
			settings.CustomStockInPatterns, settings.CustomStockOutPatterns)
	}

	// --- Real browser session + real Scraper + real Discoverer ---

	sess, err := browser.NewSession(ctx, *headless, *timeout)
	if err != nil {
		fatal("browser.NewSession failed", err)
	}
	defer sess.Close(ctx)

	scraper := pipeline.NewScraper(*headless, *waitTimeF, *scrapeTOF)
	// NewDiscoverer no longer needs headless/timeout — Run() never opens
	// its own browser session as of the scrapeDoc-based validation
	// redesign (see discovery.go's package doc comment divergence #5).
	// -headless/-timeout are still used above for the initial fetch's
	// browser.NewSession and for the Scraper's own config.
	discoverer := pipeline.NewDiscoverer(s, scraper)

	fmt.Printf("\n=== Discoverer.Run(pair_id=%d, commit=%v) ===\n", resolvedPairID, *commit)
	result, err := discoverer.Run(ctx, sess, pipeline.DiscoverOptions{
		PairID: resolvedPairID,
		Commit: *commit,
	})
	if err != nil {
		fatal("Discoverer.Run failed", err)
	}
	printJSON(result)

	pairAfter, err := s.GetPair(ctx, resolvedPairID)
	must("GetPair (after)", err)
	fmt.Println("\n=== Pair (after) ===")
	dump(pairAfter)

	if *commit {
		fmt.Printf("\nCommitted=%v — status %s -> %s\n", result.Committed, pairBefore.Status, pairAfter.Status)
	}

	fmt.Println("\n[discovercheck] done")
}

// applySchema and stripLineComments are copied verbatim (same logic, same
// comments) from cmd/dbcheck/main.go — see that file for the full
// reasoning on why comments must be stripped before splitting on ";".
// Duplicated rather than shared because cmd/dbcheck and cmd/discovercheck
// are separate `main` packages.
func applySchema(dbPath, schemaPath string) error {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open for schema apply: %w", err)
	}
	defer db.Close()

	cleaned := stripLineComments(string(raw))
	for _, stmt := range strings.Split(cleaned, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec statement (full text below): %w\n--- STATEMENT START ---\n%s\n--- STATEMENT END ---", err, stmt)
		}
	}
	return nil
}

func stripLineComments(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx != -1 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// seedFixtures inserts one throwaway book/store/pair directly via raw
// SQL, same rationale as cmd/dbcheck's own seedFixtures (PairStore has no
// create methods by design — Phase 3 scope). Unlike dbcheck's version,
// product_url is set at INSERT time (via UpdatePairURL afterward in
// dbcheck, since dbcheck doesn't need a real URL for anything) because
// Discoverer.Run requires a non-empty product_url to even start.
func seedFixtures(dbPath, title, isbn, storeName, storeBaseURL, productURL string) int64 {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fatal("seed: open", err)
	}
	defer db.Close()

	res, err := db.Exec(`INSERT INTO books (name, isbn) VALUES (?, ?)`, title, isbn)
	must("seed: insert book", err)
	bookID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO stores (name, base_url) VALUES (?, ?)`, storeName, storeBaseURL)
	must("seed: insert store", err)
	storeID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO tracking_pairs (book_id, store_id, product_url) VALUES (?, ?, ?)`, bookID, storeID, productURL)
	must("seed: insert pair", err)
	pairID, _ := res.LastInsertId()

	return pairID
}

// seedSetting upserts one setting_overrides row. cipher is nil for a
// plaintext write regardless of the encrypt argument — matches
// store.SQLiteStore.GetSettings' own "is_encrypted but no cipher"
// behavior (store.ErrCipherRequired) if that combination is ever
// misused, rather than silently downgrading to plaintext.
func seedSetting(dbPath string, cipher store.Cipher, key, value string, encrypt bool) error {
	v := value
	isEncrypted := 0
	if encrypt {
		if cipher == nil {
			return fmt.Errorf("seedSetting: encrypt requested for %s but no cipher configured (pass -cipher throwaway or -cipher keychain)", key)
		}
		enc, err := cipher.Encrypt(value)
		if err != nil {
			return fmt.Errorf("seedSetting: encrypt %s: %w", key, err)
		}
		v = enc
		isEncrypted = 1
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO setting_overrides (key, value, is_encrypted) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, is_encrypted = excluded.is_encrypted
	`, key, v, isEncrypted)
	return err
}

// maskSecret mirrors the spirit of writer.py's get_settings_status()
// masked-hint behavior (never echo a real secret in full) without trying
// to reproduce its exact hint format, which is a Phase-3/handler-layer
// concern this diagnostic tool doesn't otherwise touch.
func maskSecret(s string) string {
	if s == "" {
		return "<empty>"
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func must(step string, err error) {
	if err != nil {
		fatal(step, err)
	}
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "[discovercheck] FAILED at %s: %v\n", step, err)
	os.Exit(1)
}

func fatalUsage(msg string) {
	fmt.Fprintf(os.Stderr, "discovercheck: %s\n\n", msg)
	flag.Usage()
	os.Exit(2)
}

func dump(v any) {
	fmt.Printf("%+v\n", v)
}

func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("(failed to marshal result for display: %v)\n", err)
		return
	}
	fmt.Println(string(b))
}