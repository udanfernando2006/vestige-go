// Package runlog is the Go port of scraper/storage/local_logger.py — reads
// (and, generically, writes) date-nested JSON run-log files under logDir
// (logDir/YYYY/MM/DD/HH-MM-SS.json), matching vestige_guide.md §3's
// documented folder convention and §7's Local Logger module description.
//
// SCOPE NOTE: this package's READ side (ListRecentRuns/ReadRunSummaries/
// FindRunDetail) is fully source-grounded — the JSON field names it parses
// (run_id, total_pairs, changes[], errors[], duration_seconds, and each
// change's pair_id/book_name/store_name/from_status/to_status/from_price/
// to_price/product_url) come directly from the real, uploaded
// RunService.java, which is the authoritative Phase 3 reference per the
// migration blueprint. WriteRunLog is deliberately generic (map[string]any
// in, not a typed RunSummary struct) because the Go Orchestrator's actual
// run-summary type (collect_run_summary()'s Go equivalent) was never
// uploaded as real source — a typed writer would require guessing that
// struct's shape, which this project's own discipline says not to do.
package runlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrNotFound mirrors ResourceNotFoundException.java's use in
// RunService.getRunDetail() (no log file's run_id matched the requested one).
var ErrNotFound = errors.New("runlog: run not found")

// RunSummary mirrors RunSummaryDto.java field-for-field.
type RunSummary struct {
	RunID           string
	TotalPairs      int
	Changes         int
	Errors          int
	DurationSeconds float64
	LogPath         string // path relative to logDir, matches Java's logPath.relativize(file)
}

// RunChange mirrors RunChangeDto.java.
type RunChange struct {
	PairID     int64
	BookName   string
	StoreName  string
	FromStatus string
	ToStatus   string
	FromPrice  *float64
	ToPrice    *float64
	ProductURL string
}

// RunDetail mirrors RunDetailDto.java.
type RunDetail struct {
	RunID           string
	TotalPairs      int
	Errors          int
	DurationSeconds float64
	Changes         []RunChange
}

// rawRunLog is the on-disk JSON shape (snake_case, written by the Python/Go
// pipeline) — kept as an unexported struct rather than the outgoing
// RunSummary/RunDetail types, same wire-isolation reasoning
// vestige_api_implementation.md documents for DiscoverToolOutput.
type rawRunLog struct {
	RunID           string            `json:"run_id"`
	TotalPairs      int               `json:"total_pairs"`
	Changes         []rawRunChange    `json:"changes"`
	Errors          []json.RawMessage `json:"errors"` // shape unknown/unneeded — only length is ever used, matching Java's listSize()
	DurationSeconds float64           `json:"duration_seconds"`
}

type rawRunChange struct {
	PairID     int64    `json:"pair_id"`
	BookName   string   `json:"book_name"`
	StoreName  string   `json:"store_name"`
	FromStatus string   `json:"from_status"`
	ToStatus   string   `json:"to_status"`
	FromPrice  *float64 `json:"from_price"`
	ToPrice    *float64 `json:"to_price"`
	ProductURL string   `json:"product_url"`
}

// BuildLogPath mirrors local_logger.py's build_log_path(run_id): parses
// run_id as an RFC3339 timestamp and derives logDir/YYYY/MM/DD/HH-MM-SS.json.
// Simpler than the Python source needed to be — Go's time.Parse(time.RFC3339,
// ...) accepts a trailing "Z" natively, unlike older Python datetime.fromisoformat,
// which is why api_server.py's _seed_last_run_at has its own
// run_id.replace("Z", "+00:00") workaround; no equivalent workaround needed here.
func BuildLogPath(logDir string, runID string) (string, error) {
	t, err := time.Parse(time.RFC3339, runID)
	if err != nil {
		return "", fmt.Errorf("runlog: run_id %q is not a valid RFC3339 timestamp: %w", runID, err)
	}
	dir := filepath.Join(logDir, t.Format("2006"), t.Format("01"), t.Format("02"))
	file := t.Format("15-04-05") + ".json"
	return filepath.Join(dir, file), nil
}

// WriteRunLog mirrors local_logger.py's write_run_log(run_data): data must
// contain a "run_id" key (string, RFC3339) — everything else is written
// through as-is. See package doc comment for why this takes a generic map
// rather than a typed run-summary struct.
func WriteRunLog(logDir string, data map[string]any) error {
	runIDRaw, ok := data["run_id"]
	if !ok {
		return fmt.Errorf("runlog: write run log: data missing required \"run_id\" key")
	}
	runID, ok := runIDRaw.(string)
	if !ok {
		return fmt.Errorf("runlog: write run log: \"run_id\" must be a string, got %T", runIDRaw)
	}
	path, err := BuildLogPath(logDir, runID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("runlog: write run log: create dir: %w", err)
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("runlog: write run log: marshal: %w", err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		return fmt.Errorf("runlog: write run log: %w", err)
	}
	return nil
}

// listJSONFiles walks logDir collecting every *.json file path, sorted in
// reverse lexicographic order — mirrors RunService.getRecentRuns()'s
// Comparator.reverseOrder() on Path. Works as a chronological-descending
// sort specifically because BuildLogPath's YYYY/MM/DD/HH-MM-SS convention
// is zero-padded and therefore lexicographically ordered the same as
// chronologically — same property Java's own sort quietly relies on.
func listJSONFiles(logDir string) ([]string, error) {
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		return nil, nil // mirrors Java's `if (!Files.exists(logPath)) return List.of();`
	}
	var out []string
	err := filepath.Walk(logDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".json") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("runlog: walk %s: %w", logDir, err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

func toRunSummary(logDir, path string, raw rawRunLog) RunSummary {
	rel, err := filepath.Rel(logDir, path)
	if err != nil {
		rel = path
	}
	return RunSummary{
		RunID: raw.RunID, TotalPairs: raw.TotalPairs, Changes: len(raw.Changes),
		Errors: len(raw.Errors), DurationSeconds: raw.DurationSeconds, LogPath: rel,
	}
}

// ReadRunSummaries mirrors RunService.getRecentRuns(): up to `limit` most
// recent log files, tolerating malformed ones by skipping them entirely
// (same as Java's `catch (Exception ignored) { // Skip malformed log files }`).
func ReadRunSummaries(logDir string, limit int) ([]RunSummary, error) {
	files, err := listJSONFiles(logDir)
	if err != nil {
		return nil, err
	}
	if len(files) > limit {
		files = files[:limit]
	}

	var out []RunSummary
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			continue // skip unreadable file, same tolerance as a parse failure
		}
		var raw rawRunLog
		if err := json.Unmarshal(b, &raw); err != nil {
			continue
		}
		out = append(out, toRunSummary(logDir, path, raw))
	}
	return out, nil
}

// ListRecentRuns returns just the file paths (no parsing) for the N most
// recent runs — mirrors local_logger.py's list_recent_runs(limit) exactly,
// e.g. as used by api_server.py's _seed_last_run_at (which does its own
// single-file open+parse afterward, distinct from ReadRunSummaries above).
func ListRecentRuns(logDir string, limit int) ([]string, error) {
	files, err := listJSONFiles(logDir)
	if err != nil {
		return nil, err
	}
	if len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

// FindRunDetail mirrors RunService.getRunDetail(runId): walks every log
// file (no limit — matches Java's unbounded Files.walk here, unlike
// getRecentRuns()'s capped-at-50 listing) looking for one whose own run_id
// content matches, not a reconstructed path — resilient to path-convention
// changes, same reasoning as the Java source.
func FindRunDetail(logDir string, runID string) (*RunDetail, error) {
	files, err := listJSONFiles(logDir)
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var raw rawRunLog
		if err := json.Unmarshal(b, &raw); err != nil {
			continue
		}
		if raw.RunID != runID {
			continue
		}
		changes := make([]RunChange, 0, len(raw.Changes))
		for _, rc := range raw.Changes {
			changes = append(changes, RunChange{
				PairID: rc.PairID, BookName: rc.BookName, StoreName: rc.StoreName,
				FromStatus: rc.FromStatus, ToStatus: rc.ToStatus,
				FromPrice: rc.FromPrice, ToPrice: rc.ToPrice, ProductURL: rc.ProductURL,
			})
		}
		return &RunDetail{
			RunID: raw.RunID, TotalPairs: raw.TotalPairs, Errors: len(raw.Errors),
			DurationSeconds: raw.DurationSeconds, Changes: changes,
		}, nil
	}
	return nil, fmt.Errorf("runlog: run %s: %w", runID, ErrNotFound)
}
