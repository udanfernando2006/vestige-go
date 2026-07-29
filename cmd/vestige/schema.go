package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
)

// applySchemaIfNewDB applies schema.sql ONLY when dbPath doesn't exist yet.
// schema.sql's CREATE TABLE statements have no IF NOT EXISTS guard — real,
// confirmed by reading the actual uploaded file — so unconditional
// re-application on every launch would fail on the second run onward.
// This is the fix: schema creation is a true one-time, first-launch event,
// same as any real installed app's first-run DB initialization, without
// requiring any change to schema.sql itself.
//
// Shared (no build tag) between main.go (desktop) and main_server.go
// (server mode) — both need identical schema-bootstrap behavior, and this
// function's signature has no Wails-specific or otherwise-uncertain types
// in it, so sharing it carries no risk of guessing a wrong type across the
// two entrypoints the way the rest of their setup (store/pipeline/router
// construction) would.
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
