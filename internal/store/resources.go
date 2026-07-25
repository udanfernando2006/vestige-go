// Package store — Phase 3 extension.
//
// This file adds the broader CRUD surface store.go's own doc comment
// explicitly deferred to "Phase 3's handler layer" — GetAllBooks-shaped
// reads, Series/Store/TrackingPair/Snapshot writes, and settings *writes*
// (store.go's existing GetSettings is read-only). Source-verified against
// the uploaded Java files (BookService/SeriesService/StoreService/
// TrackingService/AvailabilityService/SettingsService + their Repository/
// Controller/DTO counterparts) — per vestige_go_migration_blueprint.md
// Phase 3's own stated dependency ("needs the actual Java Controller/
// Service/DTO files as reference before real porting begins"), NOT
// fabricated from vestige_api_implementation.md's prose summary alone.
//
// Kept as ONE interface (PairStore, extended below) rather than split into
// resource-scoped interfaces — explicit user decision, refactor-later-if-
// needed. See the interface-extension snippet at the bottom of this file's
// companion instructions for what to paste into store.go's PairStore block.
//
// ONE FLAGGED GAP, not silently guessed around: SettingsService.java /
// api_server.py both proxy to writer.py's get_settings_status() for the
// masked-secret-key "hint" format, but writer.py itself was never
// uploaded to this project — the real hint format (e.g. "sk-...ab12" vs.
// just a bare "configured" boolean with no hint text) is UNVERIFIED here.
// maskHint() below is a reasonable placeholder (last 4 chars), explicitly
// flagged at its definition — confirm or supply writer.py if exact
// fidelity matters.
//
// TrackingService.update()'s auto-transition state machine (NEEDS_SETUP <->
// PENDING, SKIP-exempt) is genuine business logic, not plain CRUD — ported
// directly into UpdateTrackingPairFields below since this project is
// deliberately keeping one flat store layer for now (explicit decision,
// not an oversight) rather than a separate service layer the way Java has
// one. Flagged here so it's not a surprise if a service layer is split out
// later.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/domain"
)

// =============================================================================
// Sentinel errors — mirror GlobalExceptionHandler.java's mapping targets
// (ResourceNotFoundException -> 404, DataIntegrityViolationException -> 409).
// Phase 3's Gin handlers are expected to map these the same way.
// =============================================================================

var (
	// ErrNotFound mirrors ResourceNotFoundException.java.
	ErrNotFound = errors.New("store: resource not found")
	// ErrConflict mirrors DataIntegrityViolationException.java (UNIQUE
	// constraint violations — duplicate ISBN, duplicate store/series name,
	// duplicate (book_id, store_id) tracking pair).
	ErrConflict = errors.New("store: unique constraint violated")
)

// isUniqueConstraintErr checks for SQLite's UNIQUE constraint failure
// message. modernc.org/sqlite doesn't export a typed sentinel for this in
// the version pinned by go.mod, so this is a string check on the driver
// error — consistent with how store.go's existing code already treats
// sql.ErrNoRows as the only structured error type it relies on.
func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// =============================================================================
// Joined read-shapes — same pattern as store.go's own ActivePair/NeedsSetupPair
// =============================================================================

// BookRow matches BookDto.java's field list (id/name/isbn/seriesEntry/
// seriesId/seriesName/author/description). SeriesName is nil for a
// standalone book, mirroring Book.getSeries() == null.
type BookRow struct {
	ID            int64
	Name          string
	ISBN          string
	IsSeriesEntry bool
	Author        *string
	Description   *string
	SeriesID      *int64
	SeriesName    *string
}

// SeriesRow matches SeriesDto.java: BookCount is derived (COUNT(*) of
// books.series_id = series.id), mirroring SeriesService.toDto()'s
// s.getBooks().size().
type SeriesRow struct {
	ID          int64
	Name        string
	Author      *string
	Description *string
	BookCount   int
}

// TrackingRow matches TrackingPairDto.java's flat field list, extending
// ActivePair with LastScrapedAt — TrackingService.toDto() computes this via
// a correlated findTopByPairIdOrderByScrapedAtDesc(pairId) call per pair;
// this port does the equivalent as one correlated subquery per row instead
// of N+1 queries. Deliberate behavioral-neutral optimization (same result,
// fewer round-trips), not a divergence in what data is returned.
type TrackingRow struct {
	ID              int64
	BookID          int64
	StoreID         int64
	ProductURL      *string
	PriceSelector   *string
	StockSelector   *string
	Status          string
	SelectorFoundAt *time.Time
	BookName        string
	BookISBN        string
	StoreName       string
	LastScrapedAt   *time.Time
}

// CurrentAvailabilityRow matches AvailabilityDto.java.
type CurrentAvailabilityRow struct {
	PairID     int64
	BookName   string
	StoreName  string
	Status     string
	Price      *float64
	ProductURL *string
	ScrapedAt  time.Time
}

// HistorySnapshotRow matches SnapshotHistoryDto.java.
type HistorySnapshotRow struct {
	ID        int64
	PairID    int64
	BookName  string
	StoreName string
	Status    string
	Price     *float64
	ScrapedAt time.Time
}

// =============================================================================
// PairStore interface extension
//
// Paste these method signatures into store.go's existing `type PairStore
// interface { ... }` block (kept here as a doc block, not a redeclared
// interface, since Go can't split one interface's method set across two
// type declarations in the same package). The *SQLiteStore methods below
// satisfy them the moment they're added to the interface.
// =============================================================================

/*
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
	GetSettingsStatus(ctx context.Context) (*domain.SettingsStatus, error)
*/

// secretSettingKeys mirrors the two secret keys SettingsDto.java/
// ScraperSettingsResponse.java mask: SELECTOR_API_KEY, DIRECT_API_KEY.
var secretSettingKeys = map[string]bool{
	"SELECTOR_API_KEY": true,
	"DIRECT_API_KEY":   true,
}

// =============================================================================
// BOOKS
// =============================================================================

const bookRowSelect = `
	SELECT b.id, b.name, b.isbn, b.is_series_entry, b.author, b.description,
	       b.series_id, sr.name
	FROM books b
	LEFT JOIN series sr ON sr.id = b.series_id
`

// GetAllBooksGrouped returns every book, joined with its series name if
// any. Mirrors BookRepository.findAllByOrderByNameAsc() + the @EntityGraph
// eager series load. Grouping-by-series (BookService.getAllGrouped()'s
// LinkedHashMap logic) is left to the handler layer — this returns the
// flat, sorted rows a handler groups from, matching the store layer's
// existing "joins are the store's job, shaping response DTOs isn't"
// convention (see ActivePair vs. Orchestrator's own path logic).
func (s *SQLiteStore) GetAllBooksGrouped(ctx context.Context) ([]BookRow, error) {
	rows, err := s.db.QueryContext(ctx, bookRowSelect+` ORDER BY b.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: get all books: %w", err)
	}
	defer rows.Close()

	var out []BookRow
	for rows.Next() {
		var b BookRow
		if err := rows.Scan(&b.ID, &b.Name, &b.ISBN, &b.IsSeriesEntry, &b.Author,
			&b.Description, &b.SeriesID, &b.SeriesName); err != nil {
			return nil, fmt.Errorf("store: get all books: scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetBookByISBN(ctx context.Context, isbn string) (*domain.Book, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, isbn, is_series_entry, author, description, series_id
		FROM books WHERE isbn = ?
	`, isbn)
	var b domain.Book
	err := row.Scan(&b.ID, &b.Name, &b.ISBN, &b.IsSeriesEntry, &b.Author, &b.Description, &b.SeriesID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get book by isbn %s: %w", isbn, err)
	}
	return &b, nil
}

func (s *SQLiteStore) BookExistsByISBN(ctx context.Context, isbn string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM books WHERE isbn = ?)`, isbn).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: book exists by isbn %s: %w", isbn, err)
	}
	return exists, nil
}

// CreateBook mirrors BookService.create(): caller is responsible for
// resolving/creating the Series row first (find-by-name-or-create — see
// CreateSeries/GetSeriesByName below) and passing its ID in b.SeriesID,
// exactly as BookService.create() does before calling bookRepo.save().
func (s *SQLiteStore) CreateBook(ctx context.Context, b domain.Book) (*domain.Book, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO books (name, isbn, is_series_entry, author, description, series_id)
		VALUES (?, ?, ?, ?, ?, ?)
	`, b.Name, b.ISBN, b.IsSeriesEntry, b.Author, b.Description, b.SeriesID)
	if isUniqueConstraintErr(err) {
		return nil, fmt.Errorf("store: create book: isbn %s: %w", b.ISBN, ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("store: create book: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("store: create book: last insert id: %w", err)
	}
	b.ID = id
	return &b, nil
}

// UpdateBook mirrors BookService.update(): author/description only, ""=clear
// (stored as NULL), nil=no change (field untouched). Matches BookUpdateDto's
// documented convention exactly.
func (s *SQLiteStore) UpdateBook(ctx context.Context, id int64, author, description *string) (*domain.Book, error) {
	if author != nil {
		v := nilIfEmpty(*author)
		if _, err := s.db.ExecContext(ctx, `UPDATE books SET author = ? WHERE id = ?`, v, id); err != nil {
			return nil, fmt.Errorf("store: update book %d author: %w", id, err)
		}
	}
	if description != nil {
		v := nilIfEmpty(*description)
		if _, err := s.db.ExecContext(ctx, `UPDATE books SET description = ? WHERE id = ?`, v, id); err != nil {
			return nil, fmt.Errorf("store: update book %d description: %w", id, err)
		}
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, isbn, is_series_entry, author, description, series_id FROM books WHERE id = ?
	`, id)
	var b domain.Book
	err := row.Scan(&b.ID, &b.Name, &b.ISBN, &b.IsSeriesEntry, &b.Author, &b.Description, &b.SeriesID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: update book %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: update book %d: reload: %w", id, err)
	}
	return &b, nil
}

// DeleteBook mirrors BookService.delete()'s manual 3-step cascade exactly:
// snapshots for each of the book's pairs -> the pairs themselves -> the
// book. One transaction, matching the atomicity @Transactional gave Java.
func (s *SQLiteStore) DeleteBook(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete book %d: begin tx: %w", id, err)
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM books WHERE id = ?)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("store: delete book %d: exists check: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("store: delete book %d: %w", id, ErrNotFound)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM availability_snapshots WHERE pair_id IN (SELECT id FROM tracking_pairs WHERE book_id = ?)
	`, id); err != nil {
		return fmt.Errorf("store: delete book %d: delete snapshots: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracking_pairs WHERE book_id = ?`, id); err != nil {
		return fmt.Errorf("store: delete book %d: delete pairs: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete book %d: %w", id, err)
	}
	return tx.Commit()
}

func (s *SQLiteStore) GetBooksBySeriesID(ctx context.Context, seriesID int64) ([]domain.Book, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, isbn, is_series_entry, author, description, series_id
		FROM books WHERE series_id = ?
	`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("store: get books by series %d: %w", seriesID, err)
	}
	defer rows.Close()

	var out []domain.Book
	for rows.Next() {
		var b domain.Book
		if err := rows.Scan(&b.ID, &b.Name, &b.ISBN, &b.IsSeriesEntry, &b.Author, &b.Description, &b.SeriesID); err != nil {
			return nil, fmt.Errorf("store: get books by series %d: scan: %w", seriesID, err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BulkAssignSeries mirrors BookService.bulkAssignSeries(): unconditionally
// replaces every listed book's series_id, never a merge. Caller (handler
// layer) resolves seriesID first — via GetSeriesByName-or-CreateSeries for
// the "newSeriesName" branch, or a direct existence check for "seriesId" —
// mirroring BookService's own resolution logic, which happens before this
// method is ever called in the Java source too.
func (s *SQLiteStore) BulkAssignSeries(ctx context.Context, bookIDs []int64, seriesID int64) ([]domain.Book, error) {
	if len(bookIDs) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: bulk assign series: begin tx: %w", err)
	}
	defer tx.Rollback()

	placeholders := make([]string, len(bookIDs))
	args := make([]any, 0, len(bookIDs)+1)
	args = append(args, seriesID)
	for i, id := range bookIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf(`UPDATE books SET series_id = ? WHERE id IN (%s)`, strings.Join(placeholders, ", "))
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: bulk assign series: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("store: bulk assign series: rows affected: %w", err)
	}
	if int(affected) != len(bookIDs) {
		// Mirrors BookService's `if (books.size() != dto.getBookIds().size())
		// throw ResourceNotFoundException` — one or more book IDs didn't exist.
		return nil, fmt.Errorf("store: bulk assign series: one or more books not found: %w", ErrNotFound)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, isbn, is_series_entry, author, description, series_id
		FROM books WHERE id IN (`+strings.Join(placeholders, ", ")+`)
	`, args[1:]...)
	if err != nil {
		return nil, fmt.Errorf("store: bulk assign series: reload: %w", err)
	}
	defer rows.Close()

	var out []domain.Book
	for rows.Next() {
		var b domain.Book
		if err := rows.Scan(&b.ID, &b.Name, &b.ISBN, &b.IsSeriesEntry, &b.Author, &b.Description, &b.SeriesID); err != nil {
			return nil, fmt.Errorf("store: bulk assign series: scan: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// =============================================================================
// SERIES
// =============================================================================

func (s *SQLiteStore) GetAllSeries(ctx context.Context) ([]SeriesRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sr.id, sr.name, sr.author, sr.description,
		       (SELECT COUNT(*) FROM books b WHERE b.series_id = sr.id)
		FROM series sr
	`)
	if err != nil {
		return nil, fmt.Errorf("store: get all series: %w", err)
	}
	defer rows.Close()

	var out []SeriesRow
	for rows.Next() {
		var sr SeriesRow
		if err := rows.Scan(&sr.ID, &sr.Name, &sr.Author, &sr.Description, &sr.BookCount); err != nil {
			return nil, fmt.Errorf("store: get all series: scan: %w", err)
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SeriesExistsByName(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM series WHERE name = ?)`, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: series exists by name %q: %w", name, err)
	}
	return exists, nil
}

func (s *SQLiteStore) GetSeriesByName(ctx context.Context, name string) (*domain.Series, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, author, description FROM series WHERE name = ?`, name)
	var sr domain.Series
	err := row.Scan(&sr.ID, &sr.Name, &sr.Author, &sr.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get series by name %q: %w", name, err)
	}
	return &sr, nil
}

func (s *SQLiteStore) CreateSeries(ctx context.Context, sr domain.Series) (*domain.Series, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO series (name, author, description) VALUES (?, ?, ?)
	`, sr.Name, sr.Author, sr.Description)
	if isUniqueConstraintErr(err) {
		return nil, fmt.Errorf("store: create series %q: %w", sr.Name, ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("store: create series: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("store: create series: last insert id: %w", err)
	}
	sr.ID = id
	return &sr, nil
}

// UpdateSeries mirrors SeriesService.update(): name is a rename only if
// non-nil (blank-string rejection is the handler/caller's job — mirrors
// Java rejecting it in the service layer before ever reaching the
// repository save, not something this store method re-validates).
// author/description follow the standard nil=no-change/""=clear convention.
func (s *SQLiteStore) UpdateSeries(ctx context.Context, id int64, name, author, description *string) (*domain.Series, error) {
	if name != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE series SET name = ? WHERE id = ?`, *name, id); isUniqueConstraintErr(err) {
			return nil, fmt.Errorf("store: update series %d: name %q: %w", id, *name, ErrConflict)
		} else if err != nil {
			return nil, fmt.Errorf("store: update series %d: name: %w", id, err)
		}
	}
	if author != nil {
		v := nilIfEmpty(*author)
		if _, err := s.db.ExecContext(ctx, `UPDATE series SET author = ? WHERE id = ?`, v, id); err != nil {
			return nil, fmt.Errorf("store: update series %d: author: %w", id, err)
		}
	}
	if description != nil {
		v := nilIfEmpty(*description)
		if _, err := s.db.ExecContext(ctx, `UPDATE series SET description = ? WHERE id = ?`, v, id); err != nil {
			return nil, fmt.Errorf("store: update series %d: description: %w", id, err)
		}
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, name, author, description FROM series WHERE id = ?`, id)
	var sr domain.Series
	err := row.Scan(&sr.ID, &sr.Name, &sr.Author, &sr.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: update series %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: update series %d: reload: %w", id, err)
	}
	return &sr, nil
}

// DeleteSeries mirrors SeriesService.delete(): books are orphaned
// (series_id -> NULL), never deleted, before the series row itself goes.
func (s *SQLiteStore) DeleteSeries(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete series %d: begin tx: %w", id, err)
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM series WHERE id = ?)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("store: delete series %d: exists check: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("store: delete series %d: %w", id, ErrNotFound)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE books SET series_id = NULL WHERE series_id = ?`, id); err != nil {
		return fmt.Errorf("store: delete series %d: orphan books: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM series WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete series %d: %w", id, err)
	}
	return tx.Commit()
}

// =============================================================================
// STORES
// =============================================================================

func (s *SQLiteStore) GetAllStores(ctx context.Context) ([]domain.Store, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, base_url, search_url_template FROM stores`)
	if err != nil {
		return nil, fmt.Errorf("store: get all stores: %w", err)
	}
	defer rows.Close()

	var out []domain.Store
	for rows.Next() {
		var st domain.Store
		if err := rows.Scan(&st.ID, &st.Name, &st.BaseURL, &st.SearchURLTemplate); err != nil {
			return nil, fmt.Errorf("store: get all stores: scan: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) StoreExistsByName(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM stores WHERE name = ?)`, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: store exists by name %q: %w", name, err)
	}
	return exists, nil
}

func (s *SQLiteStore) CreateStore(ctx context.Context, st domain.Store) (*domain.Store, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO stores (name, base_url, search_url_template) VALUES (?, ?, ?)
	`, st.Name, st.BaseURL, st.SearchURLTemplate)
	if isUniqueConstraintErr(err) {
		return nil, fmt.Errorf("store: create store %q: %w", st.Name, ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("store: create store: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("store: create store: last insert id: %w", err)
	}
	st.ID = id
	return &st, nil
}

// UpdateStore mirrors StoreService.update(): searchUrlTemplate follows the
// ""=clear (-> NULL, back to "undiscovered") / nil=no-change convention;
// name/baseUrl are plain nil=no-change (Java applies them unconditionally
// once non-null, with no blank/empty special-casing for these two fields).
func (s *SQLiteStore) UpdateStore(ctx context.Context, id int64, name, baseURL, searchURLTemplate *string) (*domain.Store, error) {
	if name != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE stores SET name = ? WHERE id = ?`, *name, id); isUniqueConstraintErr(err) {
			return nil, fmt.Errorf("store: update store %d: name %q: %w", id, *name, ErrConflict)
		} else if err != nil {
			return nil, fmt.Errorf("store: update store %d: name: %w", id, err)
		}
	}
	if baseURL != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE stores SET base_url = ? WHERE id = ?`, *baseURL, id); err != nil {
			return nil, fmt.Errorf("store: update store %d: base_url: %w", id, err)
		}
	}
	if searchURLTemplate != nil {
		v := nilIfBlank(*searchURLTemplate)
		if _, err := s.db.ExecContext(ctx, `UPDATE stores SET search_url_template = ? WHERE id = ?`, v, id); err != nil {
			return nil, fmt.Errorf("store: update store %d: search_url_template: %w", id, err)
		}
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, name, base_url, search_url_template FROM stores WHERE id = ?`, id)
	var st domain.Store
	err := row.Scan(&st.ID, &st.Name, &st.BaseURL, &st.SearchURLTemplate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: update store %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: update store %d: reload: %w", id, err)
	}
	return &st, nil
}

// DeleteStore mirrors StoreService.delete()'s manual cascade — same shape
// as DeleteBook above (snapshots -> pairs -> the resource itself).
func (s *SQLiteStore) DeleteStore(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete store %d: begin tx: %w", id, err)
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM stores WHERE id = ?)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("store: delete store %d: exists check: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("store: delete store %d: %w", id, ErrNotFound)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM availability_snapshots WHERE pair_id IN (SELECT id FROM tracking_pairs WHERE store_id = ?)
	`, id); err != nil {
		return fmt.Errorf("store: delete store %d: delete snapshots: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracking_pairs WHERE store_id = ?`, id); err != nil {
		return fmt.Errorf("store: delete store %d: delete pairs: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stores WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete store %d: %w", id, err)
	}
	return tx.Commit()
}

// =============================================================================
// TRACKING PAIRS — broader surface beyond store.go's pipeline-scoped methods
// =============================================================================

// GetAllTrackingPairs mirrors TrackingPairRepository.findAllByOrderByIdAsc()
// — ALL statuses, unlike store.go's GetActivePairs (excludes SKIP/
// NEEDS_SETUP) or GetPairsNeedingSetup (NEEDS_SETUP only). Includes a
// correlated LastScrapedAt subquery — see TrackingRow's doc comment.
func (s *SQLiteStore) GetAllTrackingPairs(ctx context.Context) ([]TrackingRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.book_id, p.store_id, p.product_url, p.price_selector, p.stock_selector,
		       p.status, p.selector_found_at, b.name, b.isbn, s.name,
		       (SELECT MAX(scraped_at) FROM availability_snapshots WHERE pair_id = p.id)
		FROM tracking_pairs p
		JOIN books b ON b.id = p.book_id
		JOIN stores s ON s.id = p.store_id
		ORDER BY p.id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("store: get all tracking pairs: %w", err)
	}
	defer rows.Close()

	var out []TrackingRow
	for rows.Next() {
		var t TrackingRow
		var selectorFoundAt, lastScrapedAt *string
		if err := rows.Scan(&t.ID, &t.BookID, &t.StoreID, &t.ProductURL, &t.PriceSelector, &t.StockSelector,
			&t.Status, &selectorFoundAt, &t.BookName, &t.BookISBN, &t.StoreName, &lastScrapedAt); err != nil {
			return nil, fmt.Errorf("store: get all tracking pairs: scan: %w", err)
		}
		if selectorFoundAt != nil {
			ts, err := parseTime(*selectorFoundAt)
			if err != nil {
				return nil, fmt.Errorf("store: get all tracking pairs: selector_found_at: %w", err)
			}
			t.SelectorFoundAt = &ts
		}
		if lastScrapedAt != nil {
			ts, err := parseTime(*lastScrapedAt)
			if err != nil {
				return nil, fmt.Errorf("store: get all tracking pairs: last_scraped_at: %w", err)
			}
			t.LastScrapedAt = &ts
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) TrackingPairExistsByBookStore(ctx context.Context, bookID, storeID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM tracking_pairs WHERE book_id = ? AND store_id = ?)
	`, bookID, storeID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: tracking pair exists (book=%d, store=%d): %w", bookID, storeID, err)
	}
	return exists, nil
}

func (s *SQLiteStore) TrackingPairExistsByID(ctx context.Context, id int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracking_pairs WHERE id = ?)`, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: tracking pair %d exists: %w", id, err)
	}
	return exists, nil
}

// CreateTrackingPair mirrors TrackingService.create(): status always starts
// PENDING. Caller resolves bookID (by ISBN) / storeID (by name) first —
// mirrors TrackingService.create()'s own bookRepo.findByIsbn/
// storeRepo.findByName calls happening before pairRepo.save().
func (s *SQLiteStore) CreateTrackingPair(ctx context.Context, bookID, storeID int64, productURL *string) (*ActivePair, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO tracking_pairs (book_id, store_id, product_url, status) VALUES (?, ?, ?, 'PENDING')
	`, bookID, storeID, productURL)
	if isUniqueConstraintErr(err) {
		return nil, fmt.Errorf("store: create tracking pair (book=%d, store=%d): %w", bookID, storeID, ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("store: create tracking pair: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("store: create tracking pair: last insert id: %w", err)
	}
	return s.GetPair(ctx, id)
}

// UpdateTrackingPairFields is a direct port of TrackingService.update()'s
// full auto-transition state machine — see this file's package doc comment
// for why it lives at the store layer rather than a separate service layer.
//
// Semantics, exactly mirroring the Java source:
//  1. productUrl: nil=no change, else set.
//  2. priceSelector/stockSelector: nil=no change; ""=clear (-> NULL); any
//     non-empty value sets it. Setting either marks selectorsTouched.
//  3. status != nil: explicit status ALWAYS wins, auto-transition below is
//     skipped entirely — matches Java's if/else-if exactly (not independent
//     checks).
//  4. else if selectorsTouched:
//     - both selectors now present (after this update) AND current status
//     was NEEDS_SETUP -> PENDING + selector_found_at = now.
//     - otherwise (at least one now missing) AND current status != SKIP
//     -> NEEDS_SETUP. SKIP pairs are deliberately left alone either way.
func (s *SQLiteStore) UpdateTrackingPairFields(ctx context.Context, id int64, productURL, priceSelector, stockSelector, status *string) (*ActivePair, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: update tracking pair %d: begin tx: %w", id, err)
	}
	defer tx.Rollback()

	var curPrice, curStock *string
	var curStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT price_selector, stock_selector, status FROM tracking_pairs WHERE id = ?
	`, id).Scan(&curPrice, &curStock, &curStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: update tracking pair %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: update tracking pair %d: load current: %w", id, err)
	}

	if productURL != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE tracking_pairs SET product_url = ? WHERE id = ?`, *productURL, id); err != nil {
			return nil, fmt.Errorf("store: update tracking pair %d: product_url: %w", id, err)
		}
	}

	selectorsTouched := false
	newPrice, newStock := curPrice, curStock
	if priceSelector != nil {
		newPrice = nilIfBlank(*priceSelector)
		if _, err := tx.ExecContext(ctx, `UPDATE tracking_pairs SET price_selector = ? WHERE id = ?`, newPrice, id); err != nil {
			return nil, fmt.Errorf("store: update tracking pair %d: price_selector: %w", id, err)
		}
		selectorsTouched = true
	}
	if stockSelector != nil {
		newStock = nilIfBlank(*stockSelector)
		if _, err := tx.ExecContext(ctx, `UPDATE tracking_pairs SET stock_selector = ? WHERE id = ?`, newStock, id); err != nil {
			return nil, fmt.Errorf("store: update tracking pair %d: stock_selector: %w", id, err)
		}
		selectorsTouched = true
	}

	switch {
	case status != nil:
		if _, err := tx.ExecContext(ctx, `UPDATE tracking_pairs SET status = ? WHERE id = ?`, *status, id); err != nil {
			return nil, fmt.Errorf("store: update tracking pair %d: status: %w", id, err)
		}
	case selectorsTouched:
		bothPresent := newPrice != nil && newStock != nil
		if bothPresent {
			if curStatus == "NEEDS_SETUP" {
				now := formatTime(time.Now().UTC())
				if _, err := tx.ExecContext(ctx, `
					UPDATE tracking_pairs SET status = 'PENDING', selector_found_at = ? WHERE id = ?
				`, now, id); err != nil {
					return nil, fmt.Errorf("store: update tracking pair %d: auto-transition to PENDING: %w", id, err)
				}
			}
		} else if curStatus != "SKIP" {
			if _, err := tx.ExecContext(ctx, `UPDATE tracking_pairs SET status = 'NEEDS_SETUP' WHERE id = ?`, id); err != nil {
				return nil, fmt.Errorf("store: update tracking pair %d: auto-transition to NEEDS_SETUP: %w", id, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: update tracking pair %d: commit: %w", id, err)
	}
	return s.GetPair(ctx, id)
}

// =============================================================================
// AVAILABILITY
// =============================================================================

// GetCurrentAvailability mirrors AvailabilitySnapshotRepository.findLatestPerPair()
// + AvailabilityService.getCurrentStatus()'s mapping.
func (s *SQLiteStore) GetCurrentAvailability(ctx context.Context) ([]CurrentAvailabilityRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.pair_id, b.name, st.name, a.status, a.price, p.product_url, a.scraped_at
		FROM availability_snapshots a
		JOIN tracking_pairs p ON p.id = a.pair_id
		JOIN books b ON b.id = p.book_id
		JOIN stores st ON st.id = p.store_id
		WHERE a.scraped_at = (
			SELECT MAX(a2.scraped_at) FROM availability_snapshots a2 WHERE a2.pair_id = p.id
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("store: get current availability: %w", err)
	}
	defer rows.Close()

	var out []CurrentAvailabilityRow
	for rows.Next() {
		var c CurrentAvailabilityRow
		var scrapedAtStr string
		if err := rows.Scan(&c.PairID, &c.BookName, &c.StoreName, &c.Status, &c.Price, &c.ProductURL, &scrapedAtStr); err != nil {
			return nil, fmt.Errorf("store: get current availability: scan: %w", err)
		}
		t, err := parseTime(scrapedAtStr)
		if err != nil {
			return nil, fmt.Errorf("store: get current availability: scraped_at: %w", err)
		}
		c.ScrapedAt = t
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetAvailabilityHistory mirrors AvailabilitySnapshotRepository.findHistory():
// every filter independently optional (nil = don't filter on it), newest
// first, limited. Matches AvailabilityController's default limit=100 at the
// handler layer, not enforced here (this just takes whatever limit is passed).
func (s *SQLiteStore) GetAvailabilityHistory(ctx context.Context, isbn, storeName, status *string, limit int) ([]HistorySnapshotRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.pair_id, b.name, st.name, a.status, a.price, a.scraped_at
		FROM availability_snapshots a
		JOIN tracking_pairs p ON p.id = a.pair_id
		JOIN books b ON b.id = p.book_id
		JOIN stores st ON st.id = p.store_id
		WHERE (? IS NULL OR b.isbn = ?)
		  AND (? IS NULL OR st.name = ?)
		  AND (? IS NULL OR a.status = ?)
		ORDER BY a.scraped_at DESC
		LIMIT ?
	`, isbn, isbn, storeName, storeName, status, status, limit)
	if err != nil {
		return nil, fmt.Errorf("store: get availability history: %w", err)
	}
	defer rows.Close()

	var out []HistorySnapshotRow
	for rows.Next() {
		var h HistorySnapshotRow
		var scrapedAtStr string
		if err := rows.Scan(&h.ID, &h.PairID, &h.BookName, &h.StoreName, &h.Status, &h.Price, &scrapedAtStr); err != nil {
			return nil, fmt.Errorf("store: get availability history: scan: %w", err)
		}
		t, err := parseTime(scrapedAtStr)
		if err != nil {
			return nil, fmt.Errorf("store: get availability history: scraped_at: %w", err)
		}
		h.ScrapedAt = t
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SnapshotExists(ctx context.Context, id int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM availability_snapshots WHERE id = ?)`, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: snapshot %d exists: %w", id, err)
	}
	return exists, nil
}

// DeleteSnapshot mirrors AvailabilityService.deleteSnapshot(): 404 (via
// ErrNotFound) if absent, single-row delete otherwise.
func (s *SQLiteStore) DeleteSnapshot(ctx context.Context, id int64) error {
	exists, err := s.SnapshotExists(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("store: delete snapshot %d: %w", id, ErrNotFound)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM availability_snapshots WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete snapshot %d: %w", id, err)
	}
	return nil
}

// DeleteHistoryForPair mirrors AvailabilityService.deleteHistoryForPair():
// wipes one pair's snapshot history only — the pair row and its cached
// selectors are untouched. 404 (ErrNotFound) if the pair itself doesn't exist.
func (s *SQLiteStore) DeleteHistoryForPair(ctx context.Context, pairID int64) error {
	exists, err := s.TrackingPairExistsByID(ctx, pairID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("store: delete history for pair %d: %w", pairID, ErrNotFound)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM availability_snapshots WHERE pair_id = ?`, pairID); err != nil {
		return fmt.Errorf("store: delete history for pair %d: %w", pairID, err)
	}
	return nil
}

// =============================================================================
// SETTINGS WRITES
// =============================================================================

// ApplySettingUpdate mirrors writer.py's apply_setting_update(key, value)
// per vestige_guide.md §7's Module Map description (writer.py itself was
// not uploaded — this is built from that description plus
// SettingsController/SettingsService.java's observed call pattern, not
// fabricated from nothing). value == nil: no-op (skip entirely). value ==
// ""&: DELETE the override row (explicit clear, falls back to "not
// configured"). value == anything else: INSERT OR REPLACE, encrypting via
// s.cipher first if key is one of the two secret keys.
func (s *SQLiteStore) ApplySettingUpdate(ctx context.Context, key string, value *string) error {
	if value == nil {
		return nil
	}
	if *value == "" {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM setting_overrides WHERE key = ?`, key); err != nil {
			return fmt.Errorf("store: clear setting %s: %w", key, err)
		}
		return nil
	}

	stored := *value
	isEncrypted := secretSettingKeys[key]
	if isEncrypted {
		if s.cipher == nil {
			return fmt.Errorf("store: apply setting %s: %w", key, ErrCipherRequired)
		}
		enc, err := s.cipher.Encrypt(stored)
		if err != nil {
			return fmt.Errorf("store: apply setting %s: encrypt: %w", key, err)
		}
		stored = enc
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO setting_overrides (key, value, is_encrypted) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, is_encrypted = excluded.is_encrypted
	`, key, stored, isEncrypted)
	if err != nil {
		return fmt.Errorf("store: apply setting %s: %w", key, err)
	}
	return nil
}

// GetSettingsStatus mirrors writer.py's get_settings_status() /
// SettingsDto.java: secret fields never return plaintext, only a
// configured bool + masked hint.
//
// FLAGGED, UNVERIFIED: the real hint format lives in writer.py, which was
// never uploaded to this project. maskHint() below is a placeholder (last
// 4 characters, e.g. "...ab12") — confirm this matches the real format, or
// supply writer.py, before treating this as production-faithful.
func (s *SQLiteStore) GetSettingsStatus(ctx context.Context) (*domain.SettingsStatus, error) {
	full, err := s.GetSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: get settings status: %w", err)
	}

	out := &domain.SettingsStatus{
		LLMDiscoveryEnabled:    full.LLMDiscoveryEnabled,
		LLMMode:                full.LLMMode,
		SelectorAPIBase:        full.SelectorAPIBase,
		SelectorModel:          full.SelectorModel,
		DirectAPIBase:          full.DirectAPIBase,
		DirectModel:            full.DirectModel,
		ScrapeIntervalHours:    full.ScrapeIntervalHours,
		CustomStockInPatterns:  full.CustomStockInPatterns,
		CustomStockOutPatterns: full.CustomStockOutPatterns,
	}
	if full.SelectorAPIKey != "" {
		out.SelectorAPIKeyConfig = true
		hint := maskHint(full.SelectorAPIKey)
		out.SelectorAPIKeyHint = &hint
	}
	if full.DirectAPIKey != "" {
		out.DirectAPIKeyConfig = true
		hint := maskHint(full.DirectAPIKey)
		out.DirectAPIKeyHint = &hint
	}
	return out, nil
}

// maskHint — SEE THE FLAGGED GAP ABOVE. Placeholder: last 4 characters,
// prefixed with "...". Not verified against writer.py's real implementation.
func maskHint(secret string) string {
	const tail = 4
	if len(secret) <= tail {
		return "..." + secret
	}
	return "..." + secret[len(secret)-tail:]
}

// =============================================================================
// small helpers
// =============================================================================

// nilIfEmpty: ""->nil, else a pointer to the value. Used for BookUpdateDto/
// SeriesUpdateDto's author/description ""=clear convention (any non-empty
// string is kept as-is, including whitespace-only — Java's .isEmpty() check
// is the same: it does NOT trim, unlike nilIfBlank below).
func nilIfEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// nilIfBlank: ""/whitespace-only -> nil, else a pointer to the value. Used
// for TrackingPairUpdateDto's selectors (Java: .isBlank()) and
// StoreUpdateDto's searchUrlTemplate (Java: .isBlank()) — both explicitly
// trim-aware, unlike BookUpdateDto/SeriesUpdateDto's plain .isEmpty() above.
// This distinction is real, source-verified (TrackingService.java line
// "isBlank() ? null : ...", StoreService.java line "isBlank() ? null : ...",
// vs. BookService.java/SeriesService.java's "isEmpty() ? null : ..."), not
// an arbitrary choice made during this port.
func nilIfBlank(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}
