package main

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log"
	"os"
	"strings"
)

//go:embed schema.sql
var defaultSchemaSQL string

// applySchemaIfNewDB mirrors the original behavior on the happy path
// exactly (skip if dbPath already exists; otherwise apply the schema from
// schemaPath, falling back to the embedded copy). The apply step itself
// is now crash-safe (CodeRabbit-flagged): the previous version opened
// sql.Open(dbPath) directly, which creates the file on disk as soon as
// the first db.Exec touches it — so a mid-schema failure (bad statement,
// disk full, unsupported SQL feature, etc.) left a half-initialized file
// sitting at dbPath. The NEXT launch's os.Stat(dbPath) check would then
// find that half-built file, conclude "already initialized," and skip
// schema application entirely — silently running the app against a DB
// missing tables, surfacing later as confusing "no such table" errors far
// from the actual root cause. Fixed by building the schema in a temp file
// first and only os.Rename-ing it into place at dbPath once every
// statement has succeeded — a failed apply now leaves nothing at dbPath
// for a future launch to mistake as already-initialized.
func applySchemaIfNewDB(dbPath, schemaPath string) error {
	if _, err := os.Stat(dbPath); err == nil {
		log.Printf("existing database found at %s, skipping schema application", dbPath)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", dbPath, err)
	}
	log.Printf("no database found at %s — applying schema", dbPath)

	var schemaBytes []byte
	var err error
	if schemaPath != "" {
		schemaBytes, err = os.ReadFile(schemaPath)
	}
	if err != nil || schemaPath == "" {
		log.Printf("schema file %s not found on disk (or unreadable); using embedded schema fallback", schemaPath)
		schemaBytes = []byte(defaultSchemaSQL)
	}

	// Build the schema in a temp file in the SAME directory as dbPath
	// (required for os.Rename to be atomic — renaming across filesystems
	// is not guaranteed atomic and can even fail outright on some
	// platforms/filesystem combinations).
	tmpPath := dbPath + ".tmp-init"
	// Best-effort cleanup of a stray temp file from a prior failed
	// attempt — os.Remove on a non-existent path is a harmless no-op, and
	// sql.Open below would fail loudly on a genuinely locked/bad path
	// regardless, so this isn't masking a real problem.
	_ = os.Remove(tmpPath)

	if err := func() error {
		db, err := sql.Open("sqlite", tmpPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", tmpPath, err)
		}
		defer db.Close()

		for _, stmt := range splitStatements(string(schemaBytes)) {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("exec statement %q: %w", stmt, err)
			}
		}
		return nil
	}(); err != nil {
		// Schema apply failed partway through — remove the half-built temp
		// file so it can never be mistaken for a real database, by this
		// launch or a future one.
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, dbPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s to %s: %w", tmpPath, dbPath, err)
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