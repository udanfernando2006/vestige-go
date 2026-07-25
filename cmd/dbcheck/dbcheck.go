// Command dbcheck is a live diagnostic tool for internal/store.SQLiteStore,
// mirroring the cmd/sessioncheck / cmd/scrapercheck / cmd/crawlercheck
// pattern already established for other layers. It exercises real SQLite
// I/O against a real file — nothing mocked, nothing simulating pipeline
// logic that doesn't exist yet (orchestrator.go/discovery.go aren't built,
// so this deliberately doesn't try to mirror them).
//
// Usage:
//
//	go run ./cmd/dbcheck -db ./dbcheck.sqlite -schema ./schema.sql
//	go run ./cmd/dbcheck -db ./dbcheck.sqlite -schema ./schema.sql -settings
//
// -settings additionally round-trips an encrypted setting through
// security.SettingsCipher, using a throwaway key generated for this run
// only — never touches a real SETTINGS_ENCRYPTION_KEY source, since that
// mechanism (OS keychain vs. file fallback) isn't decided/built yet.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/udanfernando2006/vestige-go/internal/domain"
	"github.com/udanfernando2006/vestige-go/internal/security"
	"github.com/udanfernando2006/vestige-go/internal/store"

	_ "modernc.org/sqlite"
)

// keyFingerprint returns a short, non-reversible identifier for a key —
// safe to print/compare across runs without ever putting the real key
// value in terminal scrollback or logs.
func keyFingerprint(keyB64 string) string {
	sum := sha256.Sum256([]byte(keyB64))
	return hex.EncodeToString(sum[:6]) // 12 hex chars, plenty to distinguish, nowhere near reversible
}

func main() {
	dbPath := flag.String("db", "./dbcheck.sqlite", "path to a SQLite file (will be created)")
	schemaPath := flag.String("schema", "./schema.sql", "path to schema.sql")
	fresh := flag.Bool("fresh", true, "delete -db before running, for a clean slate each time")
	withSettings := flag.Bool("settings", false, "also round-trip an encrypted setting via a throwaway key")
	testKeychain := flag.Bool("keychain", false, "exercise security.LoadOrGenerateKey against -keydir instead of a throwaway key")
	keyDir := flag.String("keydir", "./dbcheck-keydir", "TEST-ONLY stand-in for the not-yet-decided app_data_dir() fallback path — never use this constant in real app code")
	flag.Parse()

	if *fresh {
		os.Remove(*dbPath)
	}

	if err := applySchema(*dbPath, *schemaPath); err != nil {
		fatal("apply schema", err)
	}

	var cipher store.Cipher
	if *testKeychain {
		keyB64, src, err := security.LoadOrGenerateKey(*keyDir)
		if err != nil {
			fatal("LoadOrGenerateKey", err)
		}
		fmt.Printf("[dbcheck] LoadOrGenerateKey source: %s (fallback dir: %s)\n", src, *keyDir)
		fmt.Printf("[dbcheck] key fingerprint: %s  (compare this across separate `go run` invocations — same fingerprint = same persisted key)\n", keyFingerprint(keyB64))
		if src == security.KeySourceFile {
			fmt.Println("[dbcheck] WARNING: OS keychain unavailable in this environment — used the file fallback. " +
				"This is the exact degraded-mode warning a real first-run UI should surface, not hide.")
		}
		c, err := security.NewSettingsCipher(keyB64)
		if err != nil {
			fatal("build cipher from keychain-sourced key", err)
		}
		cipher = c

		// Run it a second time in the same process to prove persistence —
		// a real second app launch should get the SAME key back, not a
		// freshly generated one, whichever source was used.
		keyB64Again, srcAgain, err := security.LoadOrGenerateKey(*keyDir)
		if err != nil {
			fatal("LoadOrGenerateKey (second call)", err)
		}
		if keyB64Again != keyB64 {
			fatal("LoadOrGenerateKey persistence check", fmt.Errorf("second call returned a DIFFERENT key — persistence is broken (source=%s)", srcAgain))
		}
		fmt.Println("[dbcheck] persistence check passed: second LoadOrGenerateKey call returned the identical key")
	} else if *withSettings {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			fatal("generate throwaway key", err)
		}
		keyB64 := base64.URLEncoding.EncodeToString(key)
		c, err := security.NewSettingsCipher(keyB64)
		if err != nil {
			fatal("build cipher", err)
		}
		cipher = c
		fmt.Println("[dbcheck] using a throwaway encryption key for this run only")
	}

	s, err := store.NewSQLiteStore(*dbPath, cipher)
	if err != nil {
		fatal("open store", err)
	}
	defer s.Close()

	ctx := context.Background()

	fmt.Println("\n=== seeding a book/store/pair directly (bypassing PairStore — it has no create methods, by design; see the scope note in store.go) ===")
	bookID, storeID, pairID := seedFixtures(*dbPath)
	fmt.Printf("book_id=%d store_id=%d pair_id=%d\n", bookID, storeID, pairID)

	fmt.Println("\n=== GetPair ===")
	pair, err := s.GetPair(ctx, pairID)
	must("GetPair", err)
	dump(pair)

	fmt.Println("\n=== GetStore ===")
	st, err := s.GetStore(ctx, storeID)
	must("GetStore", err)
	dump(st)

	fmt.Println("\n=== GetActivePairs (should include our pair — status defaults to PENDING) ===")
	active, err := s.GetActivePairs(ctx)
	must("GetActivePairs", err)
	fmt.Printf("%d active pair(s)\n", len(active))
	for _, p := range active {
		dump(p)
	}

	fmt.Println("\n=== WriteSnapshot ===")
	inStock := true
	price := 3590.0
	status := "IN_STOCK"
	source := "scraper"
	result := &domain.AvailabilityResult{
		InStock: &inStock,
		Price:   &price,
		Status:  &status,
		Source:  &source,
	}
	snap, err := s.WriteSnapshot(ctx, pairID, result)
	must("WriteSnapshot", err)
	dump(snap)

	fmt.Println("\n=== GetLastSnapshot (should match what we just wrote) ===")
	last, err := s.GetLastSnapshot(ctx, pairID)
	must("GetLastSnapshot", err)
	dump(last)

	fmt.Println("\n=== UpdatePairURL ===")
	must("UpdatePairURL", s.UpdatePairURL(ctx, pairID, "https://example.lk/product/123"))

	fmt.Println("\n=== UpdatePairSelectors (pair status is currently IN_STOCK, not NEEDS_SETUP -> should NOT auto-transition) ===")
	must("UpdatePairSelectors", s.UpdatePairSelectors(ctx, pairID, "div.price", "div.stock"))
	pair, _ = s.GetPair(ctx, pairID)
	fmt.Printf("status after update (expect IN_STOCK, unchanged): %s\n", pair.Status)

	fmt.Println("\n=== ClearPairSelectors (should unconditionally force NEEDS_SETUP) ===")
	must("ClearPairSelectors", s.ClearPairSelectors(ctx, pairID))
	pair, _ = s.GetPair(ctx, pairID)
	fmt.Printf("status after clear (expect NEEDS_SETUP): %s, price_selector nil? %v\n", pair.Status, pair.PriceSelector == nil)

	fmt.Println("\n=== GetActivePairs again (pair is now NEEDS_SETUP -> should be EXCLUDED) ===")
	active, err = s.GetActivePairs(ctx)
	must("GetActivePairs", err)
	fmt.Printf("%d active pair(s) (expect 0)\n", len(active))

	fmt.Println("\n=== UpdateStoreSearchTemplate ===")
	must("UpdateStoreSearchTemplate", s.UpdateStoreSearchTemplate(ctx, storeID, "?s={query}"))
	st, _ = s.GetStore(ctx, storeID)
	dump(st)

	fmt.Println("\n=== GetSettings (all keys empty — nothing written yet) ===")
	settings, err := s.GetSettings(ctx)
	must("GetSettings", err)
	dump(settings)

	if *withSettings || *testKeychain {
		fmt.Println("\n=== round-tripping an encrypted setting through the cipher directly ===")
		// PairStore has no settings-write method (Phase 3 scope, per store.go's
		// package doc) — so this writes straight to setting_overrides to prove
		// GetSettings' decrypt path, not to prove a write path that doesn't exist.
		if err := seedEncryptedSetting(*dbPath, cipher, "SELECTOR_API_KEY", "sk-fake-test-key-123"); err != nil {
			fatal("seed encrypted setting", err)
		}
		settings, err = s.GetSettings(ctx)
		must("GetSettings after encrypted seed", err)
		fmt.Printf("decrypted SelectorAPIKey: %q (expect sk-fake-test-key-123)\n", settings.SelectorAPIKey)
	}

	fmt.Println("\n[dbcheck] all checks completed without error")
}

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

	// modernc.org/sqlite's database/sql Exec, like most drivers, expects one
	// statement per call. A naive split on ";" breaks when a semicolon
	// appears inside a "--" comment (schema.sql's books table comment does
	// exactly this: "-- SQLite has no native BOOLEAN; 0/1 by convention...")
	// — the semicolon inside that comment was being treated as a statement
	// terminator, cutting CREATE TABLE books off mid-comment. Fix: strip
	// "--" comments line-by-line FIRST, then split the comment-free text
	// on ";". This also makes the earlier comment-only-chunk problem
	// (isEmptyOrCommentOnly) moot — an all-comment chunk becomes an empty
	// chunk once comments are stripped, so a plain blank check is enough now.
	cleaned := stripLineComments(raw2str(raw))
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

// seedFixtures inserts one book/store/pair directly via raw SQL, since
// PairStore intentionally has no create methods for these (that's Phase 3's
// broader CRUD surface, not the pipeline-scoped PairStore built so far).
func seedFixtures(dbPath string) (bookID, storeID, pairID int64) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fatal("seed: open", err)
	}
	defer db.Close()

	res, err := db.Exec(`INSERT INTO books (name, isbn) VALUES (?, ?)`, "Test Book", "9781473231061")
	must("seed: insert book", err)
	bookID, _ = res.LastInsertId()

	res, err = db.Exec(`INSERT INTO stores (name, base_url) VALUES (?, ?)`, "Test Store", "https://example.lk")
	must("seed: insert store", err)
	storeID, _ = res.LastInsertId()

	res, err = db.Exec(`INSERT INTO tracking_pairs (book_id, store_id) VALUES (?, ?)`, bookID, storeID)
	must("seed: insert pair", err)
	pairID, _ = res.LastInsertId()

	return bookID, storeID, pairID
}

func seedEncryptedSetting(dbPath string, cipher store.Cipher, key, plaintext string) error {
	enc, err := cipher.Encrypt(plaintext)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`
		INSERT INTO setting_overrides (key, value, is_encrypted) VALUES (?, ?, 1)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, is_encrypted = 1
	`, key, enc)
	return err
}

func must(step string, err error) {
	if err != nil {
		fatal(step, err)
	}
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "[dbcheck] FAILED at %s: %v\n", step, err)
	os.Exit(1)
}

func dump(v any) {
	fmt.Printf("%+v\n", v)
}

func raw2str(b []byte) string { return string(b) }

// stripLineComments removes everything from "--" to end-of-line, for every
// line. This must run BEFORE splitting on ";" — see the comment in
// applySchema for why (a semicolon inside a "--" comment must never be
// treated as a statement terminator).
//
// NOTE: this is a plain substring search, not a real SQL tokenizer — it does
// NOT know about "--" appearing inside a quoted string literal (e.g. a
// default value like '--example'). schema.sql has no such literals today,
// so this is safe for this specific file, but it is not a general-purpose
// SQL comment stripper. Flagging rather than silently overclaiming
// robustness it doesn't have.
func stripLineComments(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx != -1 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}