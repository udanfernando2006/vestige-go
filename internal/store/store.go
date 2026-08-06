// Package store provides the pipeline's data-access boundary.
//
// Scope note: PairStore below is deliberately limited to the read/write surface
// the pipeline layer itself calls (Orchestrator, Discoverer) — matching
// vestige_go_pipeline_implementation.md §3. The broader CRUD surface writer.py
// also exposes (get_all_books, get_all_series, get_history, settings *writes*,
// sync_config — the last confirmed dropped, not ported) belongs to Phase 3's
// handler layer backing the seven-resource API, and is not defined here.
//
// Reconciliation note (this file merges two independently-written drafts):
// GetActivePairs/GetPair/GetPairsNeedingSetup return flattened, JOINed shapes
// (ActivePair/NeedsSetupPair — book_name/book_isbn/store_name inlined), matching
// writer.py's actual joinedload(TrackingPair.book, TrackingPair.store) behavior
// (writer.py lines 227-342) — a plain domain.TrackingPair with no join data was
// an earlier miss, corrected here after re-checking source. GetSettings returns
// *domain.Settings (typed), NOT map[string]string — writer.py's literal
// signature is dict[str, str], but this project already resolved that specific
// question (migration blueprint §7 item 12) in favor of a typed struct; a
// map-based GetSettings in an earlier draft predates that decision rather than
// contradicting it.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/domain"

	_ "modernc.org/sqlite"
)

// ActivePair matches writer.py's get_active_pairs()/get_pair() flattened dict
// shape: a tracking pair joined with its book and store, book_name/book_isbn/
// store_name inlined (source-verified against writer.py's joinedload calls).
type ActivePair struct {
	ID              int64      `json:"id"`
	BookID          int64      `json:"book_id"`
	StoreID         int64      `json:"store_id"`
	ProductURL      *string    `json:"product_url"`
	PriceSelector   *string    `json:"price_selector"`
	StockSelector   *string    `json:"stock_selector"`
	Status          string     `json:"status"`
	SelectorFoundAt *time.Time `json:"selector_found_at"`
	BookName        string     `json:"book_name"`
	BookISBN        string     `json:"book_isbn"`
	StoreName       string     `json:"store_name"`
}

// NeedsSetupPair matches writer.py's get_pairs_needing_setup() shape.
// selector_found_at is deliberately absent — the Python source's dict for
// this method omits it (line 328-340), unlike ActivePair's shape; kept
// asymmetric here on purpose, not an inconsistency.
type NeedsSetupPair struct {
	ID            int64   `json:"id"`
	BookID        int64   `json:"book_id"`
	StoreID       int64   `json:"store_id"`
	ProductURL    *string `json:"product_url"`
	PriceSelector *string `json:"price_selector"`
	StockSelector *string `json:"stock_selector"`
	Status        string  `json:"status"`
	BookName      string  `json:"book_name"`
	BookISBN      string  `json:"book_isbn"`
	StoreName     string  `json:"store_name"`
}

// Cipher is the decrypt/encrypt boundary for secret setting values
// (SELECTOR_API_KEY, DIRECT_API_KEY). Mirrors writer.py's injected
// `cipher: SettingsCipher | None`. Real implementation: security.SettingsCipher
// (ported from security/crypto.py, source-verified, AES-256-GCM).
type Cipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// ErrCipherRequired mirrors writer.py's RuntimeError raised when an encrypted
// setting is read without a configured cipher (writer.py lines 66-69).
var ErrCipherRequired = errors.New("store: setting is encrypted but no cipher is configured")

// settingsKeys mirrors writer.py's SETTINGS_KEYS — fixed, closed list.
var settingsKeys = []string{
	"LLM_DISCOVERY_ENABLED",
	"LLM_MODE",
	"NOTIFICATIONS_ENABLED",
	"SELECTOR_API_BASE",
	"SELECTOR_API_KEY",
	"SELECTOR_MODEL",
	"DIRECT_API_BASE",
	"DIRECT_API_KEY",
	"DIRECT_MODEL",
	"SCRAPE_INTERVAL_HOURS",
	"CUSTOM_STOCK_IN_PATTERNS",
	"CUSTOM_STOCK_OUT_PATTERNS",
}

// PairStore is the pipeline's data-access interface. A fake backs Phase 1
// unit tests; SQLiteStore below is the real implementation.
type PairStore interface {
	// GetActivePairs: pairs with status other than SKIP/NEEDS_SETUP, joined
	// with book/store (writer.py get_active_pairs()).
	GetActivePairs(ctx context.Context) ([]ActivePair, error)

	// GetPair: single pair by ID, joined shape, nil if not found
	// (writer.py get_pair()).
	GetPair(ctx context.Context, pairID int64) (*ActivePair, error)

	// GetPairsNeedingSetup: pairs with status NEEDS_SETUP, joined shape,
	// no selector_found_at (writer.py get_pairs_needing_setup()).
	GetPairsNeedingSetup(ctx context.Context) ([]NeedsSetupPair, error)

	// GetStore retrieves a single store by ID, nil if not found.
	GetStore(ctx context.Context, storeID int64) (*domain.Store, error)

	// GetLastSnapshot: most recent snapshot for a pair, nil if none exist.
	GetLastSnapshot(ctx context.Context, pairID int64) (*domain.AvailabilitySnapshot, error)

	// WriteSnapshot appends an immutable snapshot AND updates the pair's
	// current status pointer atomically (writer.py write_snapshot() does
	// both under one Session/commit — a Go implementation must too).
	WriteSnapshot(ctx context.Context, pairID int64, result *domain.AvailabilityResult) (*domain.AvailabilitySnapshot, error)

	UpdatePairStatus(ctx context.Context, pairID int64, status string) error
	UpdatePairURL(ctx context.Context, pairID int64, productURL string) error

	// UpdatePairSelectors: sets both selectors + selector_found_at; ONLY
	// auto-transitions NEEDS_SETUP -> PENDING if that was the pair's exact
	// current status (writer.py lines 535-554).
	UpdatePairSelectors(ctx context.Context, pairID int64, priceSel, stockSel string) error

	// ClearPairSelectors: clears both selectors + selector_found_at,
	// UNCONDITIONALLY forces status to NEEDS_SETUP regardless of current
	// status (writer.py lines 556-568 — no status guard, unlike Update).
	ClearPairSelectors(ctx context.Context, pairID int64) error

	UpdateStoreSearchTemplate(ctx context.Context, storeID int64, templateURL string) error

	// GetSettings returns the typed, decrypted effective settings.
	// Typed per migration blueprint §7 item 12's resolution — NOT
	// map[string]string, despite writer.py's own dict[str, str] signature.
	GetSettings(ctx context.Context) (*domain.Settings, error)

	// --- Books (BookController/BookService.java) ---
	GetAllBooksGrouped(ctx context.Context) ([]BookRow, error)
	GetBookByISBN(ctx context.Context, isbn string) (*domain.Book, error)
	BookExistsByISBN(ctx context.Context, isbn string) (bool, error)
	CreateBook(ctx context.Context, b domain.Book) (*domain.Book, error)
	UpdateBook(ctx context.Context, id int64, author, description *string) (*domain.Book, error)
	DeleteBook(ctx context.Context, id int64) error
	GetBooksBySeriesID(ctx context.Context, seriesID int64) ([]domain.Book, error)
	BulkAssignSeries(ctx context.Context, bookIDs []int64, seriesID int64) ([]domain.Book, error)

	// --- Series (SeriesController/SeriesService.java) ---
	GetAllSeries(ctx context.Context) ([]SeriesRow, error)
	SeriesExistsByName(ctx context.Context, name string) (bool, error)
	GetSeriesByName(ctx context.Context, name string) (*domain.Series, error)
	CreateSeries(ctx context.Context, s domain.Series) (*domain.Series, error)
	UpdateSeries(ctx context.Context, id int64, name, author, description *string) (*domain.Series, error)
	DeleteSeries(ctx context.Context, id int64) error

	// --- Stores (StoreController/StoreService.java) ---
	GetAllStores(ctx context.Context) ([]domain.Store, error)
	GetStoreByName(ctx context.Context, name string) (*domain.Store, error)
	StoreExistsByName(ctx context.Context, name string) (bool, error)
	CreateStore(ctx context.Context, s domain.Store) (*domain.Store, error)
	UpdateStore(ctx context.Context, id int64, name, baseURL, searchURLTemplate *string) (*domain.Store, error)
	DeleteStore(ctx context.Context, id int64) error

	// --- Tracking pairs, broader surface (TrackingController/TrackingService.java) ---
	GetAllTrackingPairs(ctx context.Context) ([]TrackingRow, error)
	TrackingPairExistsByBookStore(ctx context.Context, bookID, storeID int64) (bool, error)
	TrackingPairExistsByID(ctx context.Context, id int64) (bool, error)
	CreateTrackingPair(ctx context.Context, bookID, storeID int64, productURL *string) (*ActivePair, error)
	UpdateTrackingPairFields(ctx context.Context, id int64, productURL, priceSelector, stockSelector, status *string) (*ActivePair, error)

	// --- Availability (AvailabilityController/AvailabilityService.java) ---
	GetCurrentAvailability(ctx context.Context) ([]CurrentAvailabilityRow, error)
	GetAvailabilityHistory(ctx context.Context, isbn, storeName, status *string, limit int) ([]HistorySnapshotRow, error)
	SnapshotExists(ctx context.Context, id int64) (bool, error)
	DeleteSnapshot(ctx context.Context, id int64) error
	DeleteHistoryForPair(ctx context.Context, pairID int64) error

	// --- Settings writes (SettingsController/SettingsService.java via api_server.py) ---
	// value semantics mirror writer.py's apply_setting_update exactly, per
	// vestige_guide.md §7's Module Map: nil = no change (skip), "" =
	// explicit clear (delete override row), anything else = set (secrets
	// auto-encrypted via the configured Cipher).
	ApplySettingUpdate(ctx context.Context, key string, value *string) error
	// ApplySettingsBatch applies every update in ONE transaction — added
	// (CodeRabbit-flagged) to close a real non-atomicity gap:
	// internal/handler/settings.go's updateSettings previously called
	// ApplySettingUpdate once per key against this store directly, with no
	// shared transaction, so a failure partway through left whichever
	// subset of keys had already been written committed with no rollback.
	// Same per-key value semantics as ApplySettingUpdate; see
	// resources.go's ApplySettingsBatch doc comment for the full story.
	ApplySettingsBatch(ctx context.Context, updates []SettingUpdateInput) error
	GetSettingsStatus(ctx context.Context) (*domain.SettingsStatus, error)
}

// SQLiteStore is the real PairStore implementation, backed by database/sql
// against modernc.org/sqlite (pure Go, no CGo).
type SQLiteStore struct {
	db     *sql.DB
	cipher Cipher // may be nil; required only if an encrypted setting is touched
}

// NewSQLiteStore opens (but does not migrate) the SQLite database at path.
// Schema creation/migration is a separate, still-open concern (migration
// blueprint §7 item 3) — this assumes schema.sql has already been applied.
func NewSQLiteStore(path string, cipher Cipher) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite is single-writer; avoid SQLITE_BUSY under concurrent goroutines

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: enable foreign_keys pragma: %w", err)
	}

	return &SQLiteStore{db: db, cipher: cipher}, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// =============================================================================
// TRACKING PAIR QUERIES (joined shapes)
// =============================================================================

const activePairSelect = `
	SELECT p.id, p.book_id, p.store_id, p.product_url, p.price_selector, p.stock_selector,
	       p.status, p.selector_found_at, b.name, b.isbn, s.name
	FROM tracking_pairs p
	JOIN books b ON b.id = p.book_id
	JOIN stores s ON s.id = p.store_id
`

// GetActivePairs mirrors writer.py's get_active_pairs(): every pair whose
// status is neither SKIP nor NEEDS_SETUP, joined with book/store.
func (s *SQLiteStore) GetActivePairs(ctx context.Context) ([]ActivePair, error) {
	rows, err := s.db.QueryContext(ctx, activePairSelect+`
		WHERE p.status NOT IN ('SKIP', 'NEEDS_SETUP')
	`)
	if err != nil {
		return nil, fmt.Errorf("store: get active pairs: %w", err)
	}
	defer rows.Close()

	var out []ActivePair
	for rows.Next() {
		p, err := scanActivePair(rows)
		if err != nil {
			return nil, fmt.Errorf("store: get active pairs: scan: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPair mirrors writer.py's get_pair(pair_id). Returns (nil, nil) when the
// pair doesn't exist, matching Python's Optional[Dict] -> None (not an error).
func (s *SQLiteStore) GetPair(ctx context.Context, pairID int64) (*ActivePair, error) {
	row := s.db.QueryRowContext(ctx, activePairSelect+` WHERE p.id = ?`, pairID)

	p, err := scanActivePair(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pair %d: %w", pairID, err)
	}
	return p, nil
}

// GetPairsNeedingSetup mirrors writer.py's get_pairs_needing_setup().
func (s *SQLiteStore) GetPairsNeedingSetup(ctx context.Context) ([]NeedsSetupPair, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.book_id, p.store_id, p.product_url, p.price_selector, p.stock_selector,
		       p.status, b.name, b.isbn, s.name
		FROM tracking_pairs p
		JOIN books b ON b.id = p.book_id
		JOIN stores s ON s.id = p.store_id
		WHERE p.status = 'NEEDS_SETUP'
	`)
	if err != nil {
		return nil, fmt.Errorf("store: get pairs needing setup: %w", err)
	}
	defer rows.Close()

	var out []NeedsSetupPair
	for rows.Next() {
		var n NeedsSetupPair
		if err := rows.Scan(&n.ID, &n.BookID, &n.StoreID, &n.ProductURL, &n.PriceSelector,
			&n.StockSelector, &n.Status, &n.BookName, &n.BookISBN, &n.StoreName); err != nil {
			return nil, fmt.Errorf("store: get pairs needing setup: scan: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// =============================================================================
// STORE QUERIES
// =============================================================================

func (s *SQLiteStore) GetStore(ctx context.Context, storeID int64) (*domain.Store, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, base_url, search_url_template FROM stores WHERE id = ?
	`, storeID)

	var st domain.Store
	err := row.Scan(&st.ID, &st.Name, &st.BaseURL, &st.SearchURLTemplate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get store %d: %w", storeID, err)
	}
	return &st, nil
}

func (s *SQLiteStore) UpdateStoreSearchTemplate(ctx context.Context, storeID int64, template string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stores SET search_url_template = ? WHERE id = ?`, template, storeID)
	if err != nil {
		return fmt.Errorf("store: update store %d search template: %w", storeID, err)
	}
	return nil
}

// =============================================================================
// AVAILABILITY SNAPSHOT QUERIES / WRITES
// =============================================================================

func (s *SQLiteStore) GetLastSnapshot(ctx context.Context, pairID int64) (*domain.AvailabilitySnapshot, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, pair_id, in_stock, price, status, source, scraped_at
		FROM availability_snapshots WHERE pair_id = ? ORDER BY scraped_at DESC LIMIT 1
	`, pairID)

	snap, err := scanSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get last snapshot for pair %d: %w", pairID, err)
	}
	return snap, nil
}

// WriteSnapshot mirrors writer.py's write_snapshot(): appends an immutable
// snapshot row AND updates the pair's status pointer in one transaction.
//
// CodeRabbit-flagged: result.Status can legitimately be nil (domain.
// AvailabilityResult's own doc comment says "default null, never PENDING"
// — this isn't a defensive-nil-check edge case, it's an expected value on
// some paths). The snapshot row faithfully records "" for that (an
// accurate, immutable record of what this particular result carried), but
// blindly pushing that same "" into tracking_pairs.status would silently
// wipe out the pair's real current status (e.g. IN_STOCK -> "") with an
// enum value the UI's status badge doesn't even recognize. Fixed: the
// pair's status pointer is only updated when the result actually carries a
// non-empty status; a nil/empty result leaves the pair's existing status
// untouched, exactly like any other partial-update path in this file.
func (s *SQLiteStore) WriteSnapshot(ctx context.Context, pairID int64, result *domain.AvailabilityResult) (*domain.AvailabilitySnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: write snapshot: begin tx: %w", err)
	}
	defer tx.Rollback()

	scrapedAt := time.Now().UTC()
	if result.ScrapedAt != nil {
		scrapedAt = result.ScrapedAt.UTC()
	}
	status := ""
	if result.Status != nil {
		status = *result.Status
	}
	source := result.Source

	res, err := tx.ExecContext(ctx, `
		INSERT INTO availability_snapshots (pair_id, in_stock, price, status, source, scraped_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, pairID, result.InStock, result.Price, status, source, formatTime(scrapedAt))
	if err != nil {
		return nil, fmt.Errorf("store: write snapshot: insert: %w", err)
	}
	snapshotID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("store: write snapshot: last insert id: %w", err)
	}

	if status != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE tracking_pairs SET status = ? WHERE id = ?`, status, pairID); err != nil {
			return nil, fmt.Errorf("store: write snapshot: update pair status: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: write snapshot: commit: %w", err)
	}

	return &domain.AvailabilitySnapshot{
		ID: snapshotID, PairID: pairID, InStock: result.InStock, Price: result.Price,
		Status: status, Source: source, ScrapedAt: scrapedAt,
	}, nil
}

// =============================================================================
// TRACKING PAIR UPDATES
// =============================================================================

func (s *SQLiteStore) UpdatePairStatus(ctx context.Context, pairID int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tracking_pairs SET status = ? WHERE id = ?`, status, pairID)
	if err != nil {
		return fmt.Errorf("store: update pair %d status: %w", pairID, err)
	}
	return nil
}

func (s *SQLiteStore) UpdatePairURL(ctx context.Context, pairID int64, productURL string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tracking_pairs SET product_url = ? WHERE id = ?`, productURL, pairID)
	if err != nil {
		return fmt.Errorf("store: update pair %d url: %w", pairID, err)
	}
	return nil
}

// UpdatePairSelectors: auto-transitions NEEDS_SETUP -> PENDING ONLY if that
// was the pair's exact current status (writer.py lines 535-554).
func (s *SQLiteStore) UpdatePairSelectors(ctx context.Context, pairID int64, priceSelector, stockSelector string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: update pair %d selectors: begin tx: %w", pairID, err)
	}
	defer tx.Rollback()

	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `
		UPDATE tracking_pairs SET price_selector = ?, stock_selector = ?, selector_found_at = ? WHERE id = ?
	`, priceSelector, stockSelector, now, pairID); err != nil {
		return fmt.Errorf("store: update pair %d selectors: %w", pairID, err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE tracking_pairs SET status = 'PENDING' WHERE id = ? AND status = 'NEEDS_SETUP'
	`, pairID); err != nil {
		return fmt.Errorf("store: update pair %d selectors: auto-transition: %w", pairID, err)
	}

	return tx.Commit()
}

// ClearPairSelectors: UNCONDITIONALLY forces NEEDS_SETUP, no status guard —
// matches writer.py lines 556-568 exactly (deliberately asymmetric with
// UpdatePairSelectors above, not a bug).
func (s *SQLiteStore) ClearPairSelectors(ctx context.Context, pairID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tracking_pairs
		SET price_selector = NULL, stock_selector = NULL, selector_found_at = NULL, status = 'NEEDS_SETUP'
		WHERE id = ?
	`, pairID)
	if err != nil {
		return fmt.Errorf("store: clear pair %d selectors: %w", pairID, err)
	}
	return nil
}

// =============================================================================
// SETTINGS (read-only in PairStore — write path is Phase 3 scope)
// =============================================================================

// GetSettings mirrors writer.py's get_settings() EXCEPT the os.environ
// fallback (writer.py line 73) is deliberately NOT ported — that existed
// for v1's multi-process .env-sharing model, which has no equivalent in a
// single desktop binary. Confirmed decision, not an oversight; see prior
// conversation. Returns *domain.Settings (typed), per the already-resolved
// blueprint §7 item 12 decision.
func (s *SQLiteStore) GetSettings(ctx context.Context) (*domain.Settings, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT key, value, is_encrypted FROM setting_overrides WHERE key IN (`+placeholders(len(settingsKeys))+`)
	`, toArgs(settingsKeys)...)
	if err != nil {
		return nil, fmt.Errorf("store: get settings: %w", err)
	}
	defer rows.Close()

	raw := make(map[string]string, len(settingsKeys))
	for rows.Next() {
		var key, value string
		var isEncrypted bool
		if err := rows.Scan(&key, &value, &isEncrypted); err != nil {
			return nil, fmt.Errorf("store: get settings: scan: %w", err)
		}
		if isEncrypted {
			if s.cipher == nil {
				return nil, fmt.Errorf("store: get settings: key %s: %w", key, ErrCipherRequired)
			}
			decrypted, err := s.cipher.Decrypt(value)
			if err != nil {
				return nil, fmt.Errorf("store: get settings: decrypt %s: %w", key, err)
			}
			value = decrypted
		}
		raw[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: get settings: %w", err)
	}

	out := &domain.Settings{
		LLMDiscoveryEnabled:  strings.EqualFold(strings.TrimSpace(raw["LLM_DISCOVERY_ENABLED"]), "true"),
		LLMMode:              raw["LLM_MODE"],
		NotificationsEnabled: strings.EqualFold(strings.TrimSpace(raw["NOTIFICATIONS_ENABLED"]), "true"),
		SelectorAPIBase:      raw["SELECTOR_API_BASE"],
		SelectorAPIKey:       raw["SELECTOR_API_KEY"],
		SelectorModel:        raw["SELECTOR_MODEL"],
		DirectAPIBase:        raw["DIRECT_API_BASE"],
		DirectAPIKey:         raw["DIRECT_API_KEY"],
		DirectModel:          raw["DIRECT_MODEL"],
	}

	if raw["SCRAPE_INTERVAL_HOURS"] != "" {
		var hours int
		if _, err := fmt.Sscanf(raw["SCRAPE_INTERVAL_HOURS"], "%d", &hours); err == nil {
			out.ScrapeIntervalHours = &hours
		}
		// malformed value -> falls through to nil (disabled), matching the
		// defensive "bad input -> disabled, never crashes" posture documented
		// for _parse_interval() in vestige_guide.md.
	}
	if raw["CUSTOM_STOCK_IN_PATTERNS"] != "" {
		out.CustomStockInPatterns = strings.Split(raw["CUSTOM_STOCK_IN_PATTERNS"], ",")
	}
	if raw["CUSTOM_STOCK_OUT_PATTERNS"] != "" {
		out.CustomStockOutPatterns = strings.Split(raw["CUSTOM_STOCK_OUT_PATTERNS"], ",")
	}

	return out, nil
}

// =============================================================================
// scanning helpers
// =============================================================================

type rowScanner interface {
	Scan(dest ...any) error
}

func scanActivePair(row rowScanner) (*ActivePair, error) {
	var p ActivePair
	var selectorFoundAt *string
	err := row.Scan(&p.ID, &p.BookID, &p.StoreID, &p.ProductURL, &p.PriceSelector,
		&p.StockSelector, &p.Status, &selectorFoundAt, &p.BookName, &p.BookISBN, &p.StoreName)
	if err != nil {
		return nil, err
	}
	if selectorFoundAt != nil {
		t, err := parseTime(*selectorFoundAt)
		if err != nil {
			return nil, fmt.Errorf("scan active pair: selector_found_at: %w", err)
		}
		p.SelectorFoundAt = &t
	}
	return &p, nil
}

func scanSnapshot(row rowScanner) (*domain.AvailabilitySnapshot, error) {
	var snap domain.AvailabilitySnapshot
	var scrapedAtStr string
	err := row.Scan(&snap.ID, &snap.PairID, &snap.InStock, &snap.Price, &snap.Status, &snap.Source, &scrapedAtStr)
	if err != nil {
		return nil, err
	}
	t, err := parseTime(scrapedAtStr)
	if err != nil {
		return nil, fmt.Errorf("scan snapshot: scraped_at: %w", err)
	}
	snap.ScrapedAt = t
	return &snap, nil
}

func formatTime(t time.Time) string         { return t.UTC().Format(time.RFC3339) }
func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

func placeholders(n int) string {
	ph := make([]string, n)
	for i := range ph {
		ph[i] = "?"
	}
	return strings.Join(ph, ", ")
}

func toArgs(keys []string) []any {
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	return args
}
