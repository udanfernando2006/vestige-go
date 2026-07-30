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
