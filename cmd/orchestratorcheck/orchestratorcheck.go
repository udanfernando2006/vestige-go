// Command orchestratorcheck is a live diagnostic tool for exercising the
// FULL pipeline (Orchestrator.RunAll) end-to-end against a real SQLite
// database — mirrors the established cmd/dbcheck / cmd/crawlercheck /
// cmd/scrapercheck / cmd/extractorcheck / cmd/discovercheck convention
// (a single flat flag set, no subcommand parser, real store/browser/LLM
// wiring, no reimplementation of production logic).
//
// Orchestrator.RunAll is the ONLY exported entry point on *pipeline.Orchestrator
// — it processes every active pair in one pass, there is no "run just this
// pair" method. So this tool's job is: seed exactly the DB state you want
// (stores/books/tracking_pairs/settings), run the REAL Orchestrator against
// it, then inspect what changed — not simulate path routing itself.
//
// SCOPE BOUNDARY, stated up front rather than discovered by surprise:
// internal/security/keystore.go and cipher.go (OS-keychain-backed
// SettingsCipher) were NOT available as source to verify against when this
// tool was written, so seed-settings ALWAYS writes plaintext
// (is_encrypted=0) rows — this tool never constructs a real store.Cipher
// and never exercises the encrypted-settings path. That path is already
// covered by cmd/dbcheck's own -settings flag. Real API keys typed into
// this tool's flags land in plaintext in whatever -db file you point it
// at; treat that file as a local scratch/test artifact, not anything to
// commit or share.
//
// Typical session, matching the "fresh store -> cached search template ->
// cached selectors" walkthrough this tool exists to support:
//
//	go run ./cmd/orchestratorcheck -db=test.db -action=init -schema=../../schema.sql
//	go run ./cmd/orchestratorcheck -db=test.db -action=seed-store -store-name="Jumpbooks" -store-base-url="https://jumpbooks.lk/?s=test&post_type=product"
//	go run ./cmd/orchestratorcheck -db=test.db -action=seed-book -book-name="Some Title" -book-isbn="9781234567890"
//	go run ./cmd/orchestratorcheck -db=test.db -action=seed-pair -pair-isbn=9781234567890 -pair-store="Jumpbooks"
//	go run ./cmd/orchestratorcheck -db=test.db -action=seed-settings -llm-discovery-enabled -llm-mode=selector -selector-api-base=... -selector-api-key=... -selector-model=...
//	go run ./cmd/orchestratorcheck -db=test.db -action=run
//	go run ./cmd/orchestratorcheck -db=test.db -action=list
//	# Wrong base_url discovered a bad match? Fix it in place and clear the
//	# now-inaccurate cached template to force fresh discovery next run —
//	# "" is an explicit clear here, same convention as everywhere else in
//	# this project (Settings secrets, Store's searchUrlTemplate, Tracking's
//	# selectors):
//	go run ./cmd/orchestratorcheck -db=test.db -action=update-store -store-name="Jumpbooks" -store-base-url="https://jumpbooks.lk/" -store-search-template=""
//	go run ./cmd/orchestratorcheck -db=test.db -action=run
//	# ...still discovered the wrong page (e.g. a category listing, not the
//	# real product)? Clear product_url (and the selectors scraped off that
//	# wrong page, now meaningless) to force fresh Path A rediscovery next
//	# run — same ""=explicit-clear convention. Status is left untouched
//	# deliberately (pass -pair-status too if you also want that reset):
//	go run ./cmd/orchestratorcheck -db=test.db -action=update-pair -pair-id=1 -pair-url="" -pair-price-selector="" -pair-stock-selector=""
//	go run ./cmd/orchestratorcheck -db=test.db -action=run
//	# Second book on the SAME store — Path A now hits the cached
//	# search_url_template fast path (Crawler.FindProductURL's
//	# runWithSession branch) instead of runDiscovery's two-session dance:
//	go run ./cmd/orchestratorcheck -db=test.db -action=seed-book -book-name="Another Title" -book-isbn="9789999999999"
//	go run ./cmd/orchestratorcheck -db=test.db -action=seed-pair -pair-isbn=9789999999999 -pair-store="Jumpbooks"
//	go run ./cmd/orchestratorcheck -db=test.db -action=run
//	# A pair with selectors already set exercises Path C directly, no LLM
//	# involvement at all:
//	go run ./cmd/orchestratorcheck -db=test.db -action=seed-pair -pair-isbn=... -pair-store="Jumpbooks" -pair-url="https://..." -pair-price-selector="..." -pair-stock-selector="..."
//	go run ./cmd/orchestratorcheck -db=test.db -action=run
//
// -action reference:
//
//	init            apply schema.sql to a fresh or existing -db file
//	seed-store      insert one store row
//	update-store    fix an existing store's base_url and/or search_url_template ("" = explicit clear, omitted = no change — same convention as everywhere else in this project)
//	seed-book       insert one book row
//	seed-pair       insert one tracking_pairs row (book+store resolved by isbn/name)
//	seed-settings   upsert one or more setting_overrides rows (plaintext — see SCOPE BOUNDARY above)
//	clear-setting   delete one setting_overrides row (falls back to "unset", matching apply_setting_update's ""=clear convention)
//	update-pair-status  force one pair's status directly (e.g. SKIP, to test GetActivePairs' exclusion)
//	update-pair     fix an existing pair's product_url/selectors/status ("" = explicit clear, omitted = no change) — e.g. clear product_url alone to force fresh Path A rediscovery next run
//	seed-snapshot   insert a synthetic prior snapshot for one pair, for controlled detectChange testing without a real prior run
//	list            dump current stores/books/tracking_pairs(+latest snapshot)/settings
//	reset           wipe all rows from every table (requires -confirm)
//	run             construct the REAL SQLiteStore/Crawler/Scraper/Discoverer/Orchestrator and call RunAll — prints the full RunSummary
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/store"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := flag.String("db", "", "path to the SQLite database file (required for every action)")
	action := flag.String("action", "", "init|seed-store|seed-book|seed-pair|seed-settings|clear-setting|update-pair-status|seed-snapshot|list|reset|run")

	schemaPath := flag.String("schema", "schema.sql", "path to schema.sql (init only)")

	storeName := flag.String("store-name", "", "seed-store: store name (unique)")
	storeBaseURL := flag.String("store-base-url", "", "seed-store: base_url")
	storeSearchTemplate := flag.String("store-search-template", "", "seed-store: pre-seed a cached search_url_template (optional — leave unset to test Path A's fresh-discovery branch)")

	bookName := flag.String("book-name", "", "seed-book: book name")
	bookISBN := flag.String("book-isbn", "", "seed-book: isbn (unique)")
	bookSeriesEntry := flag.Bool("book-series-entry", false, "seed-book: is_series_entry")
	bookAuthor := flag.String("book-author", "", "seed-book: author (optional)")
	bookDescription := flag.String("book-description", "", "seed-book: description (optional)")

	pairISBN := flag.String("pair-isbn", "", "seed-pair: book isbn to resolve book_id from")
	pairStore := flag.String("pair-store", "", "seed-pair: store name to resolve store_id from")
	pairURL := flag.String("pair-url", "", "seed-pair: product_url (optional — leave unset to force Path A)")
	pairPriceSel := flag.String("pair-price-selector", "", "seed-pair: price_selector (optional — set together with -pair-stock-selector to force Path C)")
	pairStockSel := flag.String("pair-stock-selector", "", "seed-pair: stock_selector (optional)")
	pairStatus := flag.String("pair-status", "PENDING", "seed-pair / update-pair-status: tracking_pairs.status")
	pairID := flag.Int64("pair-id", 0, "update-pair-status / seed-snapshot: target tracking_pairs.id")

	llmDiscoveryEnabled := flag.Bool("llm-discovery-enabled", false, "seed-settings: LLM_DISCOVERY_ENABLED")
	llmMode := flag.String("llm-mode", "", "seed-settings: LLM_MODE (\"selector\"|\"direct\")")
	selectorAPIBase := flag.String("selector-api-base", "", "seed-settings: SELECTOR_API_BASE")
	selectorAPIKey := flag.String("selector-api-key", "", "seed-settings: SELECTOR_API_KEY (written PLAINTEXT — see file header)")
	selectorModel := flag.String("selector-model", "", "seed-settings: SELECTOR_MODEL")
	directAPIBase := flag.String("direct-api-base", "", "seed-settings: DIRECT_API_BASE")
	directAPIKey := flag.String("direct-api-key", "", "seed-settings: DIRECT_API_KEY (written PLAINTEXT — see file header)")
	directModel := flag.String("direct-model", "", "seed-settings: DIRECT_MODEL")
	scrapeIntervalHours := flag.Int("scrape-interval-hours", 0, "seed-settings: SCRAPE_INTERVAL_HOURS (0 written explicitly = disabled/empty, matching GetSettings' \"\"->nil convention)")
	customStockIn := flag.String("custom-stock-in", "", "seed-settings: CUSTOM_STOCK_IN_PATTERNS (comma-separated regex)")
	customStockOut := flag.String("custom-stock-out", "", "seed-settings: CUSTOM_STOCK_OUT_PATTERNS (comma-separated regex)")
	settingKey := flag.String("setting-key", "", "clear-setting: key to delete from setting_overrides")

	snapInStock := flag.String("snap-in-stock", "", "seed-snapshot: \"true\"|\"false\"|\"\" (empty = NULL)")
	snapPrice := flag.String("snap-price", "", "seed-snapshot: numeric string, empty = NULL")
	snapStatus := flag.String("snap-status", "", "seed-snapshot: status string")
	snapSource := flag.String("snap-source", "scraper", "seed-snapshot: \"scraper\"|\"llm_direct\"")

	confirm := flag.Bool("confirm", false, "reset: required safety confirmation")

	headless := flag.Bool("headless", true, "run: browser headless mode")
	browserTimeout := flag.Duration("browser-timeout", 60*time.Second, "run: per-navigation browser timeout")
	waitTime := flag.Duration("wait-time", 5*time.Second, "run: Scraper's rate-limit wait between navigations")
	runTimeout := flag.Duration("run-timeout", 10*time.Minute, "run: overall context timeout for the whole RunAll call")

	flag.Parse()

	if *dbPath == "" {
		fatalf("missing required -db flag")
	}
	if *action == "" {
		fatalf("missing required -action flag (see file header for the full list)")
	}

	// Track which flags were actually passed on the command line, so
	// seed-settings can upsert ONLY the keys the user explicitly named —
	// mirrors apply_setting_update()'s per-key None=no-change semantics,
	// just expressed via flag.Visit() instead of a nullable-field DTO.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	switch *action {
	case "init":
		runInit(*dbPath, *schemaPath)
	case "seed-store":
		runSeedStore(*dbPath, *storeName, *storeBaseURL, *storeSearchTemplate)
	case "update-store":
		runUpdateStore(*dbPath, setFlags, *storeName, *storeBaseURL, *storeSearchTemplate)
	case "seed-book":
		runSeedBook(*dbPath, *bookName, *bookISBN, *bookSeriesEntry, *bookAuthor, *bookDescription)
	case "seed-pair":
		runSeedPair(*dbPath, *pairISBN, *pairStore, *pairURL, *pairPriceSel, *pairStockSel, *pairStatus)
	case "seed-settings":
		runSeedSettings(*dbPath, setFlags, *llmDiscoveryEnabled, *llmMode, *selectorAPIBase, *selectorAPIKey,
			*selectorModel, *directAPIBase, *directAPIKey, *directModel, *scrapeIntervalHours, *customStockIn, *customStockOut)
	case "clear-setting":
		if *settingKey == "" {
			fatalf("clear-setting requires -setting-key")
		}
		runClearSetting(*dbPath, *settingKey)
	case "update-pair-status":
		if *pairID == 0 {
			fatalf("update-pair-status requires -pair-id")
		}
		runUpdatePairStatus(*dbPath, *pairID, *pairStatus)
	case "update-pair":
		runUpdatePair(*dbPath, setFlags, *pairID, *pairURL, *pairPriceSel, *pairStockSel, *pairStatus)
	case "seed-snapshot":
		if *pairID == 0 {
			fatalf("seed-snapshot requires -pair-id")
		}
		runSeedSnapshot(*dbPath, *pairID, *snapInStock, *snapPrice, *snapStatus, *snapSource)
	case "list":
		runList(*dbPath)
	case "reset":
		if !*confirm {
			fatalf("reset requires -confirm (this wipes every row in every table)")
		}
		runReset(*dbPath)
	case "run":
		runOrchestrator(*dbPath, *headless, *browserTimeout, *waitTime, *runTimeout)
	default:
		fatalf("unknown -action %q", *action)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "orchestratorcheck: "+format+"\n", args...)
	os.Exit(1)
}

func openRawDB(path string) *sql.DB {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		fatalf("open %s: %v", path, err)
	}
	db.SetMaxOpenConns(1) // mirrors store.go's own SQLite single-writer discipline
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		fatalf("enable foreign_keys pragma: %v", err)
	}
	return db
}

// --- init ---------------------------------------------------------------

func runInit(dbPath, schemaPath string) {
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		fatalf("read %s: %v", schemaPath, err)
	}

	db := openRawDB(dbPath)
	defer db.Close()

	// Mirrors cmd/dbcheck's established fix: strip "--" line comments
	// BEFORE splitting on ";", so a stray semicolon inside a comment
	// (the schema.sql bug memory already documents) can never break
	// naive statement splitting again, regardless of which comment it's
	// hiding in this time.
	for _, stmt := range splitSQLStatements(string(schemaBytes)) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			fatalf("apply schema statement failed: %v\n--- statement ---\n%s", err, stmt)
		}
	}
	fmt.Printf("Schema applied from %s to %s\n", schemaPath, dbPath)
}

func splitSQLStatements(schema string) []string {
	var noComments strings.Builder
	for _, line := range strings.Split(schema, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		noComments.WriteString(line)
		noComments.WriteByte('\n')
	}
	return strings.Split(noComments.String(), ";")
}

// --- seed-store -----------------------------------------------------------

func runSeedStore(dbPath, name, baseURL, searchTemplate string) {
	if name == "" || baseURL == "" {
		fatalf("seed-store requires -store-name and -store-base-url")
	}
	db := openRawDB(dbPath)
	defer db.Close()

	var template any
	if searchTemplate != "" {
		template = searchTemplate
	}

	res, err := db.Exec(`INSERT INTO stores (name, base_url, search_url_template) VALUES (?, ?, ?)`,
		name, baseURL, template)
	if err != nil {
		fatalf("insert store: %v (does a store named %q already exist? try -action=list or -action=reset)", err, name)
	}
	id, _ := res.LastInsertId()
	fmt.Printf("Seeded store id=%d name=%q base_url=%q search_url_template=%q\n", id, name, baseURL, searchTemplate)
}

// runUpdateStore fixes an existing store's base_url and/or
// search_url_template in place — added specifically for the case where a
// bad base_url (e.g. a search-results URL pasted in by mistake, rather
// than the site's real homepage/base) skewed candidate scoring for every
// run against that store, cached template included: crawler.go's
// runWithSession AND runDiscovery both pass store.BaseURL straight into
// heuristics.ScoreCandidates on every single call, not just the first —
// caching search_url_template does not retire base_url from the pipeline.
//
// Follows the SAME "" = explicit clear / omitted = no-change convention
// used throughout this project (Settings secrets, Store's
// searchUrlTemplate, Tracking's selectors — see vestige_guide.md's Common
// Mistakes list) via setFlags (populated by flag.Visit in main()): a flag
// not passed at all leaves that column untouched; -store-search-template=""
// passed explicitly clears the cached template back to NULL, forcing
// Path A's fresh two-session runDiscovery on the NEXT run against this
// store rather than the cached-template runWithSession fast path.
func runUpdateStore(dbPath string, setFlags map[string]bool, name, baseURL, searchTemplate string) {
	if name == "" {
		fatalf("update-store requires -store-name")
	}
	if !setFlags["store-base-url"] && !setFlags["store-search-template"] {
		fatalf("update-store requires at least one of -store-base-url or -store-search-template to be passed")
	}

	db := openRawDB(dbPath)
	defer db.Close()

	var storeID int64
	if err := db.QueryRow(`SELECT id FROM stores WHERE name = ?`, name).Scan(&storeID); err != nil {
		fatalf("resolve store by name %q: %v", name, err)
	}

	if setFlags["store-base-url"] {
		if baseURL == "" {
			// base_url has no clear semantics — it's NOT NULL in the
			// schema (every crawl needs SOME base to navigate to), unlike
			// search_url_template below. An empty value here is almost
			// certainly a mistake, not an intentional clear.
			fatalf("-store-base-url cannot be set to empty — base_url is required (did you mean to clear -store-search-template instead?)")
		}
		if _, err := db.Exec(`UPDATE stores SET base_url = ? WHERE id = ?`, baseURL, storeID); err != nil {
			fatalf("update store base_url: %v", err)
		}
		fmt.Printf("Store %q base_url -> %q\n", name, baseURL)
	}

	if setFlags["store-search-template"] {
		var templateArg any
		if searchTemplate != "" {
			templateArg = searchTemplate
		}
		if _, err := db.Exec(`UPDATE stores SET search_url_template = ? WHERE id = ?`, templateArg, storeID); err != nil {
			fatalf("update store search_url_template: %v", err)
		}
		if searchTemplate == "" {
			fmt.Printf("Store %q search_url_template -> CLEARED (next Path A run against this store does fresh discovery, not the cached-template fast path)\n", name)
		} else {
			fmt.Printf("Store %q search_url_template -> %q\n", name, searchTemplate)
		}
	}
}

// --- seed-book ------------------------------------------------------------

func runSeedBook(dbPath, name, isbn string, seriesEntry bool, author, description string) {
	if name == "" || isbn == "" {
		fatalf("seed-book requires -book-name and -book-isbn")
	}
	db := openRawDB(dbPath)
	defer db.Close()

	var authorArg, descArg any
	if author != "" {
		authorArg = author
	}
	if description != "" {
		descArg = description
	}

	res, err := db.Exec(`INSERT INTO books (name, isbn, is_series_entry, author, description) VALUES (?, ?, ?, ?, ?)`,
		name, isbn, boolToInt(seriesEntry), authorArg, descArg)
	if err != nil {
		fatalf("insert book: %v (does a book with isbn %q already exist? try -action=list or -action=reset)", err, isbn)
	}
	id, _ := res.LastInsertId()
	fmt.Printf("Seeded book id=%d name=%q isbn=%q\n", id, name, isbn)
}

// --- seed-pair ------------------------------------------------------------

func runSeedPair(dbPath, isbn, storeName, productURL, priceSel, stockSel, status string) {
	if isbn == "" || storeName == "" {
		fatalf("seed-pair requires -pair-isbn and -pair-store")
	}
	db := openRawDB(dbPath)
	defer db.Close()

	var bookID int64
	if err := db.QueryRow(`SELECT id FROM books WHERE isbn = ?`, isbn).Scan(&bookID); err != nil {
		fatalf("resolve book by isbn %q: %v (seed the book first)", isbn, err)
	}
	var storeID int64
	if err := db.QueryRow(`SELECT id FROM stores WHERE name = ?`, storeName).Scan(&storeID); err != nil {
		fatalf("resolve store by name %q: %v (seed the store first)", storeName, err)
	}

	var urlArg, priceArg, stockArg, foundAtArg any
	if productURL != "" {
		urlArg = productURL
	}
	if priceSel != "" {
		priceArg = priceSel
	}
	if stockSel != "" {
		stockArg = stockSel
	}
	if priceSel != "" && stockSel != "" {
		// Mirrors UpdatePairSelectors always setting selector_found_at
		// alongside both selectors — kept consistent here even though no
		// current path handler reads this field directly, since a
		// half-consistent seeded row (selectors present, timestamp null)
		// would look wrong under -action=list.
		foundAtArg = time.Now().UTC().Format(time.RFC3339)
	}

	res, err := db.Exec(`
		INSERT INTO tracking_pairs (book_id, store_id, product_url, price_selector, stock_selector, status, selector_found_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, bookID, storeID, urlArg, priceArg, stockArg, status, foundAtArg)
	if err != nil {
		fatalf("insert tracking_pair: %v (does a pair for this book+store already exist? UNIQUE(book_id, store_id))", err)
	}
	id, _ := res.LastInsertId()
	fmt.Printf("Seeded tracking_pair id=%d book_id=%d store_id=%d status=%s url=%q\n", id, bookID, storeID, status, productURL)
}

// --- seed-settings ----------------------------------------------------

// runSeedSettings upserts ONLY the setting_overrides keys whose flag was
// actually passed (per setFlags, from flag.Visit) — matching
// apply_setting_update()'s per-key None=no-change semantics. See file
// header SCOPE BOUNDARY: every row is written with is_encrypted=0.
func runSeedSettings(dbPath string, setFlags map[string]bool,
	llmDiscoveryEnabled bool, llmMode, selectorAPIBase, selectorAPIKey, selectorModel,
	directAPIBase, directAPIKey, directModel string, scrapeIntervalHours int,
	customStockIn, customStockOut string) {

	db := openRawDB(dbPath)
	defer db.Close()

	type kv struct {
		flagName, key, value string
	}
	candidates := []kv{
		{"llm-discovery-enabled", "LLM_DISCOVERY_ENABLED", strconv.FormatBool(llmDiscoveryEnabled)},
		{"llm-mode", "LLM_MODE", llmMode},
		{"selector-api-base", "SELECTOR_API_BASE", selectorAPIBase},
		{"selector-api-key", "SELECTOR_API_KEY", selectorAPIKey},
		{"selector-model", "SELECTOR_MODEL", selectorModel},
		{"direct-api-base", "DIRECT_API_BASE", directAPIBase},
		{"direct-api-key", "DIRECT_API_KEY", directAPIKey},
		{"direct-model", "DIRECT_MODEL", directModel},
		{"scrape-interval-hours", "SCRAPE_INTERVAL_HOURS", scrapeIntervalHoursValue(scrapeIntervalHours, setFlags)},
		{"custom-stock-in", "CUSTOM_STOCK_IN_PATTERNS", customStockIn},
		{"custom-stock-out", "CUSTOM_STOCK_OUT_PATTERNS", customStockOut},
	}

	written := 0
	for _, c := range candidates {
		if !setFlags[c.flagName] {
			continue
		}
		if _, err := db.Exec(`
			INSERT INTO setting_overrides (key, value, is_encrypted) VALUES (?, ?, 0)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, is_encrypted = 0
		`, c.key, c.value); err != nil {
			fatalf("upsert setting %s: %v", c.key, err)
		}
		display := c.value
		if strings.Contains(c.key, "API_KEY") && display != "" {
			display = "***" // never echo a real key back to the terminal, even plaintext
		}
		fmt.Printf("Set %s = %q\n", c.key, display)
		written++
	}
	if written == 0 {
		fmt.Println("No setting flags were passed — nothing written. Pass e.g. -llm-mode=selector explicitly.")
	}
}

// scrapeIntervalHoursValue mirrors GetSettings' own "" -> nil convention:
// an explicitly-passed -scrape-interval-hours=0 is written as "" (disabled),
// any positive value is written as its decimal string.
func scrapeIntervalHoursValue(hours int, setFlags map[string]bool) string {
	if !setFlags["scrape-interval-hours"] || hours <= 0 {
		return ""
	}
	return strconv.Itoa(hours)
}

// --- clear-setting ------------------------------------------------------

func runClearSetting(dbPath, key string) {
	db := openRawDB(dbPath)
	defer db.Close()

	res, err := db.Exec(`DELETE FROM setting_overrides WHERE key = ?`, key)
	if err != nil {
		fatalf("delete setting %s: %v", key, err)
	}
	n, _ := res.RowsAffected()
	fmt.Printf("Cleared %s (%d row(s) removed)\n", key, n)
}

// --- update-pair-status -------------------------------------------------

func runUpdatePairStatus(dbPath string, id int64, status string) {
	db := openRawDB(dbPath)
	defer db.Close()

	res, err := db.Exec(`UPDATE tracking_pairs SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		fatalf("update pair %d status: %v", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fatalf("no tracking_pair with id=%d", id)
	}
	fmt.Printf("Pair %d status -> %s\n", id, status)
}

// --- update-pair --------------------------------------------------------

// runUpdatePair fixes an existing pair's product_url/selectors/status in
// place — added specifically for the case where Path A discovered (or a
// prior test seeded) a wrong product_url, and every field it produced
// downstream (selectors, scraped price/stock) is now equally suspect and
// worth clearing along with it.
//
// Clearing product_url is what actually matters for forcing rediscovery:
// determinePath() (orchestrator.go) routes purely on `!hasURL -> Path A`,
// checked before selector presence at all — so -pair-url="" alone is
// sufficient to force the NEXT run back through Crawler.FindProductURL.
// Selectors tied to the old wrong page are cleared too by default reasoning
// (see the worked example in this file's header comment) since they're
// meaningless once the URL they were discovered against is gone — but each
// field here is independently optional via setFlags, same convention as
// runUpdateStore and the rest of this project's ""=clear/omitted=no-change
// pattern (Settings secrets, Store's searchUrlTemplate, Tracking's
// selectors — vestige_guide.md's Common Mistakes list).
//
// Deliberately does NOT replicate TrackingService.update()'s real
// production auto-transition logic (clearing a selector auto-transitions
// NEEDS_SETUP unless SKIP, etc.) — this is a raw diagnostic seeding tool,
// not the real write path, so it only ever touches exactly the columns a
// flag was passed for for, with no implicit side effects. Pass -pair-status
// explicitly if you also want the status column changed.
func runUpdatePair(dbPath string, setFlags map[string]bool, id int64, url, priceSel, stockSel, status string) {
	if id == 0 {
		fatalf("update-pair requires -pair-id")
	}
	if !setFlags["pair-url"] && !setFlags["pair-price-selector"] && !setFlags["pair-stock-selector"] && !setFlags["pair-status"] {
		fatalf("update-pair requires at least one of -pair-url, -pair-price-selector, -pair-stock-selector, -pair-status to be passed")
	}

	db := openRawDB(dbPath)
	defer db.Close()

	var exists int64
	if err := db.QueryRow(`SELECT id FROM tracking_pairs WHERE id = ?`, id).Scan(&exists); err != nil {
		fatalf("resolve tracking_pair id=%d: %v", id, err)
	}

	if setFlags["pair-url"] {
		var arg any
		if url != "" {
			arg = url
		}
		if _, err := db.Exec(`UPDATE tracking_pairs SET product_url = ? WHERE id = ?`, arg, id); err != nil {
			fatalf("update pair %d product_url: %v", id, err)
		}
		if url == "" {
			fmt.Printf("Pair %d product_url -> CLEARED (next run re-enters Path A for fresh discovery, unless status is SKIP/excluded)\n", id)
		} else {
			fmt.Printf("Pair %d product_url -> %q\n", id, url)
		}
	}

	if setFlags["pair-price-selector"] {
		var arg any
		if priceSel != "" {
			arg = priceSel
		}
		if _, err := db.Exec(`UPDATE tracking_pairs SET price_selector = ? WHERE id = ?`, arg, id); err != nil {
			fatalf("update pair %d price_selector: %v", id, err)
		}
		fmt.Printf("Pair %d price_selector -> %s\n", id, clearedOrQuoted(priceSel))
	}

	if setFlags["pair-stock-selector"] {
		var arg any
		if stockSel != "" {
			arg = stockSel
		}
		if _, err := db.Exec(`UPDATE tracking_pairs SET stock_selector = ? WHERE id = ?`, arg, id); err != nil {
			fatalf("update pair %d stock_selector: %v", id, err)
		}
		fmt.Printf("Pair %d stock_selector -> %s\n", id, clearedOrQuoted(stockSel))
	}

	if setFlags["pair-status"] {
		if status == "" {
			fatalf("-pair-status cannot be cleared to empty — status is required (tracking_pairs.status is NOT NULL); use -action=update-pair-status for a plain status change")
		}
		if _, err := db.Exec(`UPDATE tracking_pairs SET status = ? WHERE id = ?`, status, id); err != nil {
			fatalf("update pair %d status: %v", id, err)
		}
		fmt.Printf("Pair %d status -> %s\n", id, status)
	}
}

func clearedOrQuoted(v string) string {
	if v == "" {
		return "CLEARED"
	}
	return fmt.Sprintf("%q", v)
}

// --- seed-snapshot --------------------------------------------------------

// runSeedSnapshot inserts a synthetic PRIOR snapshot directly, bypassing
// WriteSnapshot's status-pointer side effect entirely (tracking_pairs.status
// is deliberately left untouched) — this exists purely to give
// Orchestrator.detectChange a known "last" row to diff a real run's fresh
// result against, without needing two real scrapes to set one up.
func runSeedSnapshot(dbPath string, pairID int64, inStockStr, priceStr, status, source string) {
	if status == "" {
		fatalf("seed-snapshot requires -snap-status")
	}
	db := openRawDB(dbPath)
	defer db.Close()

	var inStockArg any
	switch inStockStr {
	case "true":
		inStockArg = 1
	case "false":
		inStockArg = 0
	case "":
		inStockArg = nil
	default:
		fatalf("-snap-in-stock must be \"true\", \"false\", or empty, got %q", inStockStr)
	}

	var priceArg any
	if priceStr != "" {
		p, err := strconv.ParseFloat(priceStr, 64)
		if err != nil {
			fatalf("-snap-price %q is not a number: %v", priceStr, err)
		}
		priceArg = p
	}

	res, err := db.Exec(`
		INSERT INTO availability_snapshots (pair_id, in_stock, price, status, source, scraped_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, pairID, inStockArg, priceArg, status, source, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		fatalf("insert snapshot: %v", err)
	}
	id, _ := res.LastInsertId()
	fmt.Printf("Seeded snapshot id=%d for pair %d: in_stock=%s price=%s status=%s\n", id, pairID, inStockStr, priceStr, status)
}

// --- list -----------------------------------------------------------------

func runList(dbPath string) {
	db := openRawDB(dbPath)
	defer db.Close()

	fmt.Println("=== stores ===")
	rows, err := db.Query(`SELECT id, name, base_url, search_url_template FROM stores ORDER BY id`)
	must(err)
	for rows.Next() {
		var id int64
		var name, baseURL string
		var template *string
		must(rows.Scan(&id, &name, &baseURL, &template))
		fmt.Printf("  [%d] %-20s base_url=%-45s search_url_template=%s\n", id, name, baseURL, derefOrDash(template))
	}
	rows.Close()

	fmt.Println("=== books ===")
	rows, err = db.Query(`SELECT id, name, isbn FROM books ORDER BY id`)
	must(err)
	for rows.Next() {
		var id int64
		var name, isbn string
		must(rows.Scan(&id, &name, &isbn))
		fmt.Printf("  [%d] %-30s isbn=%s\n", id, name, isbn)
	}
	rows.Close()

	fmt.Println("=== tracking_pairs (+ latest snapshot) ===")
	rows, err = db.Query(`
		SELECT p.id, b.name, b.isbn, s.name, p.product_url, p.price_selector, p.stock_selector, p.status
		FROM tracking_pairs p
		JOIN books b ON b.id = p.book_id
		JOIN stores s ON s.id = p.store_id
		ORDER BY p.id
	`)
	must(err)
	type pairRow struct {
		id                              int64
		book, isbn, storeName           string
		url, priceSel, stockSel, status *string
	}
	var pairs []pairRow
	for rows.Next() {
		var r pairRow
		must(rows.Scan(&r.id, &r.book, &r.isbn, &r.storeName, &r.url, &r.priceSel, &r.stockSel, &r.status))
		pairs = append(pairs, r)
	}
	rows.Close()

	for _, r := range pairs {
		fmt.Printf("  [%d] %s (%s) @ %s\n", r.id, r.book, r.isbn, r.storeName)
		fmt.Printf("        status=%s url=%s\n", derefOrDash(r.status), derefOrDash(r.url))
		fmt.Printf("        price_selector=%s stock_selector=%s\n", derefOrDash(r.priceSel), derefOrDash(r.stockSel))

		var snapID int64
		var inStock *bool
		var price *float64
		var snapStatus, source, scrapedAt string
		snapErr := db.QueryRow(`
			SELECT id, in_stock, price, status, source, scraped_at
			FROM availability_snapshots WHERE pair_id = ? ORDER BY scraped_at DESC LIMIT 1
		`, r.id).Scan(&snapID, &inStock, &price, &snapStatus, &source, &scrapedAt)
		switch {
		case snapErr == sql.ErrNoRows:
			fmt.Printf("        latest snapshot: (none)\n")
		case snapErr != nil:
			fmt.Printf("        latest snapshot: ERROR reading: %v\n", snapErr)
		default:
			fmt.Printf("        latest snapshot: [%d] in_stock=%s price=%s status=%s source=%s at=%s\n",
				snapID, derefBoolOrDash(inStock), derefFloatOrDash(price), snapStatus, source, scrapedAt)
		}
	}

	fmt.Println("=== settings (setting_overrides) ===")
	rows, err = db.Query(`SELECT key, value, is_encrypted FROM setting_overrides ORDER BY key`)
	must(err)
	for rows.Next() {
		var key, value string
		var encrypted bool
		must(rows.Scan(&key, &value, &encrypted))
		display := value
		if encrypted {
			display = "<encrypted — this tool never writes encrypted rows; this one came from elsewhere, e.g. dbcheck>"
		} else if strings.Contains(key, "API_KEY") && value != "" {
			display = "***"
		}
		fmt.Printf("  %-30s = %s\n", key, display)
	}
	rows.Close()
}

func must(err error) {
	if err != nil {
		fatalf("query failed: %v", err)
	}
}

func derefOrDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

func derefBoolOrDash(b *bool) string {
	if b == nil {
		return "-"
	}
	if *b {
		return "true"
	}
	return "false"
}

func derefFloatOrDash(f *float64) string {
	if f == nil {
		return "-"
	}
	return strconv.FormatFloat(*f, 'f', 2, 64)
}

// --- reset ------------------------------------------------------------

func runReset(dbPath string) {
	db := openRawDB(dbPath)
	defer db.Close()

	// FK-safe order: children before parents.
	tables := []string{"availability_snapshots", "tracking_pairs", "setting_overrides", "books", "series", "stores"}
	for _, t := range tables {
		if _, err := db.Exec(`DELETE FROM ` + t); err != nil {
			fatalf("wipe table %s: %v", t, err)
		}
	}
	fmt.Println("All tables wiped (schema untouched).")
}

// --- run ----------------------------------------------------------------

// runOrchestrator constructs the REAL SQLiteStore, Crawler, Scraper,
// Discoverer, and Orchestrator — the exact same construction shape
// Phase 3's real Wails entrypoint will eventually use — and calls RunAll,
// printing the full RunSummary. Nothing about path routing/selection is
// reimplemented here; whatever the real Orchestrator decides is what runs.
func runOrchestrator(dbPath string, headless bool, browserTimeout, waitTime, runTimeout time.Duration) {
	// See file header SCOPE BOUNDARY: no store.Cipher is constructed here.
	// This is fine as long as every setting_overrides row in this DB has
	// is_encrypted=0, which is all seed-settings ever writes — but if a
	// row with is_encrypted=1 exists (e.g. written by dbcheck against the
	// same file), GetSettings will fail with store.ErrCipherRequired, and
	// that failure is real and correct, not a bug in this tool.
	st, err := store.NewSQLiteStore(dbPath, nil)
	if err != nil {
		fatalf("open store: %v", err)
	}
	defer st.Close()

	settings, err := st.GetSettings(context.Background())
	if err != nil {
		fatalf("load settings before run (sanity check): %v", err)
	}
	if settings.LLMDiscoveryEnabled || strings.EqualFold(strings.TrimSpace(settings.LLMMode), "direct") {
		fmt.Println("NOTE: LLM discovery/direct-extraction is enabled in settings — this run will make real LLM API calls.")
	}

	crawler := pipeline.NewCrawler(headless, browserTimeout)
	scraper := pipeline.NewScraper(headless, waitTime, browserTimeout)
	discoverer := pipeline.NewDiscoverer(st, scraper)
	orch := pipeline.NewOrchestrator(st, crawler, scraper, discoverer, headless, browserTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	runID := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	summary, err := orch.RunAll(ctx, runID)
	if err != nil {
		fatalf("RunAll failed: %v", err)
	}

	printSummary(summary)
}

func printSummary(s *pipeline.RunSummary) {
	fmt.Println()
	fmt.Println("=== RunSummary ===")
	fmt.Printf("run_id=%s total_pairs=%d completed=%d errors(count)=%d needs_setup=%v duration=%.1fs\n",
		s.RunID, s.TotalPairs, s.Completed, s.Errors, s.NeedsSetup, s.DurationSeconds)

	fmt.Println("--- results ---")
	for _, r := range s.Results {
		changedMark := ""
		if r.Changed {
			changedMark = " (CHANGED)"
		}
		priceStr := "-"
		if r.Price != nil {
			priceStr = strconv.FormatFloat(*r.Price, 'f', 2, 64)
		}
		fmt.Printf("  pair=%d %-30s @ %-20s status=%-18s price=%s%s\n", r.PairID, r.Book, r.Store, r.Status, priceStr, changedMark)
	}

	if len(s.Changes) > 0 {
		fmt.Println("--- changes ---")
		for _, c := range s.Changes {
			fromPrice, toPrice := "-", "-"
			if c.FromPrice != nil {
				fromPrice = strconv.FormatFloat(*c.FromPrice, 'f', 2, 64)
			}
			if c.ToPrice != nil {
				toPrice = strconv.FormatFloat(*c.ToPrice, 'f', 2, 64)
			}
			fmt.Printf("  pair=%d %s @ %s: %s (%s) -> %s (%s)\n", c.PairID, c.BookName, c.StoreName, c.FromStatus, fromPrice, c.ToStatus, toPrice)
		}
	}

	// This is RunSummary.ErrorEntries — what Python's own final
	// summary["errors"] dict key actually ends up holding (the list,
	// not collect_run_summary's int). See orchestrator.go's package doc
	// comment divergence #7 for the full reasoning.
	if len(s.ErrorEntries) > 0 {
		fmt.Println("--- errors (run_pair-level exceptions only — see divergence #6/#7 in orchestrator.go) ---")
		for _, e := range s.ErrorEntries {
			reason := "-"
			if e.Reason != nil {
				reason = *e.Reason
			}
			fmt.Printf("  pair=%d reason=%s\n", e.PairID, reason)
		}
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
