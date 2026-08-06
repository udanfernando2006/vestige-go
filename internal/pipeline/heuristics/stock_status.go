// Package heuristics holds pure, store-tuned pattern-matching logic isolated
// from browser/DB-touching control flow in pipeline/. See
// vestige_go_pipeline_implementation.md Section 2 for why this is its own
// subpackage rather than a file inside pipeline/.
package heuristics

import (
	"regexp"
	"strings"
)

// Built-in stock-status patterns, direct port of scraper.py's
// parse_stock_status(). Order between the two lists is load-bearing:
// out-of-stock must be checked before in-stock, since "unavailable"
// contains "available" as a substring.
var (
	outOfStockPatterns = []string{
		`out of stock`,
		`sold out`,
		`unavailable`,
		`not available`,
		`pre-order`,
		`out-of-stock`,
	}

	inStockPatterns = []string{
		`in stock`,
		`in-stock`,
		`available`,
		`add to cart`,
		`add to basket`,
		`in stock now`,
		`ready to ship`,
		`buy now`,
		`low stock:?\s*\d+\s*left`, // confirmed production case: "Low stock: 4 left"
	}
)

// ClassifyStockText mirrors Scraper.parse_stock_status() exactly.
//
// Regex-only matching: built-in patterns and any caller-supplied custom
// patterns (from CUSTOM_STOCK_IN_PATTERNS / CUSTOM_STOCK_OUT_PATTERNS,
// comma-separated regex strings sourced from settings) are merged into one
// list per direction and checked uniformly — no separate substring pass.
//
// Out-of-stock is checked FIRST regardless of whether the matching pattern
// came from the built-in list or a custom one — this ordering is the
// substring trap the Python docstring calls out and must not be reordered.
//
// Return value mirrors Python's None/True/False: nil means "could not
// classify" (→ status ERROR / reason unparseable_stock_status upstream),
// non-nil true/false is a positive classification.
func ClassifyStockText(raw, customIn, customOut string) *bool {
	if raw == "" {
		return nil
	}

	text := strings.ToLower(strings.TrimSpace(raw))

	customOutPatterns := splitAndTrim(customOut)
	customInPatterns := splitAndTrim(customIn)

	if matchesAny(text, outOfStockPatterns, customOutPatterns) {
		f := false
		return &f
	}

	if matchesAny(text, inStockPatterns, customInPatterns) {
		t := true
		return &t
	}

	return nil
}

// splitAndTrim mirrors the Python list comprehension:
// [p.strip() for p in custom.split(",") if p.strip()]
func splitAndTrim(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// matchesAny reports whether text matches any regex in either pattern
// list, mirroring Python's `any(re.search(p, text) for p in built_in + custom)`.
//
// NOTE: a malformed custom regex (bad user input to
// CUSTOM_STOCK_IN_PATTERNS/CUSTOM_STOCK_OUT_PATTERNS) will fail to compile
// here. Python's re.search would raise re.error in the same situation —
// this implementation treats a bad pattern as a non-match rather than
// panicking, so one bad custom pattern doesn't take down classification
// for every pair. Flagging this as a deliberate behavioral choice, not a
// silent Python-parity claim: confirm this is acceptable, since Python's
// actual failure behavior on a bad custom regex isn't nailed down in the
// source provided.
func matchesAny(text string, builtIn, custom []string) bool {
	for _, p := range builtIn {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(text) {
			return true
		}
	}
	for _, p := range custom {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(text) {
			return true
		}
	}
	return false
}
