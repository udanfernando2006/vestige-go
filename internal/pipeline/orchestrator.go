// Package pipeline holds the Orchestrator/Crawler/Scraper/Extractor/
// Discoverer control-flow code — the browser/DB-touching layer that calls
// into the pure functions in pipeline/heuristics. See
// vestige_go_pipeline_implementation.md Section 1 for the full package
// layout this file is part of.
//
// Source-verified against orchestrator.py in full, plus the real
// store.PairStore interface (store.go), domain.Settings (settings.go),
// domain.AvailabilityResult (result.go), browser.Session (session.go),
// Crawler.FindProductURL (crawler.go), Scraper.Scrape (scraper.go),
// Extractor (extractor.go), and Discoverer.Run (discovery.go) — not
// fabricated from vestige_guide.md's prose description of the pipeline
// alone.
//
// Nine deliberate divergences from orchestrator.py, each flagged again at
// its point of use below rather than only here:
//
//  1. Path B's subprocess call to discover_selectors.py is replaced with
//     an in-process call to Discoverer.Run — no subprocess, no CLI, no
//     stdout/stderr parsing. This isn't a design choice made fresh here;
//     it's the exact wiring discovery.go's own package doc comment
//     already sketches ("Orchestrator's Path B (not yet built) — how
//     session gets to Run"), implemented as written there: a
//     session.FreshContext(ctx)-forked session for the initial fetch,
//     closed immediately once Run() returns (not deferred to Path B's own
//     return — Path B continues with a real scrape afterward on a
//     SEPARATE session, and holding two Chromium processes alive through
//     that would be wasteful), then the post-discovery scrape uses the
//     ORIGINAL caller-supplied session, matching Python's
//     subprocess-does-discovery-then-real-code-scrapes-again shape
//     exactly on that specific point.
//  2. Python's single "proc.returncode != 0" branch (a failed subprocess)
//     is reproduced by THREE Go conditions collapsing into the same
//     "discovery failed, fall back to Path D" branch: FreshContext()
//     failing to open a session, Discoverer.Run returning a Go error
//     (fatal — e.g. no product_url, settings missing, LLM extraction
//     totally failed), or Run returning a nil error with Committed==false
//     (a structured partial/total validation failure per discovery.go's
//     own doc comment). This mirrors how a nonzero subprocess exit code
//     in Python covered both "the CLI crashed" and "the CLI printed a
//     reason and deliberately returned 1."
//  3. handle_error's "selector_not_found" classification is a literal,
//     mechanical port of Python's `"selector_not_found" in
//     error_msg.lower()` SUBSTRING matching — NOT the sentinel-errors
//     (errors.Is) approach the migration blueprint (§7 item 13) leans
//     toward. That decision is explicitly still open ("small judgment
//     call, not yet formally closed"), so this file preserves the exact
//     Python behavior rather than presupposing an unmade decision. Both
//     Path B and Path C signal this case via errors.New("selector_not_found"),
//     matching Python's `raise Exception("selector_not_found")` literally
//     (lowercasing is a no-op on this literal, but the check itself
//     lowercases the full error message first, exactly as Python does,
//     in case a wrapped error ever capitalizes something upstream).
//  4. Two places where this port is deliberately MORE defensive than
//     Python's literal (crash-prone) behavior, both flagged again inline:
//     (a) run_all's per-pair loop in Python has NO error handling around
//     db_writer.get_last_snapshot()/write_snapshot() — a failure there
//     would propagate uncaught and crash the ENTIRE run, not just skip
//     that one pair. This port logs and continues instead, per this
//     project's own documented principle ("Always wrap each pair's
//     pipeline execution so one failure doesn't abort the whole run" —
//     vestige_guide.md's Common Mistakes section). (b) handle_error's
//     clear_pair_selectors() call in Python is similarly unguarded —
//     a failure there would propagate straight out of the error handler
//     itself, uncaught, since it's called from inside an already-active
//     except block with no nested try. This port logs and continues.
//  5. CORRECTED, per explicit user confirmation — Path D DOES thread
//     settings.CustomStockInPatterns/CustomStockOutPatterns through
//     ParseStockStatus, exactly like Paths B/C. orchestrator.py's own
//     Path D (`scraper.parse_stock_status(details.get("stock_status"))`,
//     no custom-pattern arguments at all) omits this, and that omission
//     was confirmed to be a real bug in the Python source, not a
//     deliberate design choice worth preserving — the user has flagged
//     it for a matching fix on the Python side post-migration (tracked in
//     project memory), and asked for the Go port to be correct now rather
//     than faithfully reproducing the same gap. This is therefore a
//     DELIBERATE, CONFIRMED divergence from a strict mechanical port, not
//     an oversight.
//  6. A COMPLETED-outcome AvailabilityResult whose own Status is "ERROR"
//     (e.g. Path D parsed a price but couldn't classify stock, and no
//     fallback resolved it) is still written to the DB as a real
//     snapshot — exactly matching Python's control flow, where
//     `result.get("status") == "COMPLETED"` gates the snapshot write, and
//     pair_summary["status"] is THEN overwritten to the real (possibly
//     "ERROR") availability.status afterward. This "ERROR" is later
//     counted by collectRunSummary's Errors bucket despite never
//     appearing in the outer errorEntries list (reserved for run_pair-
//     level exceptions only) — a real, subtle distinction preserved
//     deliberately, not a bug.
//  7. A REAL LATENT BUG in orchestrator.py's run_all, preserved-but-not-
//     silently-reproduced: `summary = self.collect_run_summary(all_results)`
//     computes summary["errors"] as an INT count, but the very next
//     statement, `summary.update({..., "errors": errors, ...})`,
//     overwrites that same dict key with the LIST of error records built
//     during the loop — a plain variable-name collision that silently
//     discards the int count from the object run_all actually returns.
//     (summary["total_pairs"], summary["completed"], and
//     summary["needs_setup"] are NOT overwritten this way — only
//     "errors" collides.) This Go port preserves Python's real runtime
//     OUTPUT (RunSummary.ErrorEntries is what actually corresponds to
//     Python's final summary["errors"] list) but does NOT throw away the
//     int count the way Python's dict-key collision does — RunSummary.Errors
//     is kept alongside it. No downstream consumer of this discarded
//     count exists yet (Phase 3 hasn't been built), so nothing is broken
//     by not reproducing the loss, and there is no evidence in the
//     available source that the loss was intentional rather than a typo'd
//     reuse of the "errors" name for two different things.
//  8. Timeouts for LLM client construction (stockFallbackLLMTimeout,
//     directExtractionLLMTimeout) are Go-only additive parameters with no
//     Python equivalent — openai-python's client was never given an
//     explicit timeout override anywhere in llm_extractor.py. Placeholder
//     values, same caveat already attached to discovery.go's
//     discoveryLLMTimeout and scraper.go's defaultWaitSelectorTimeout:
//     confirm before treating as settled.
//  9. context.Context threading throughout is purely additive (migration
//     blueprint §7 item 3, already resolved) — no Python equivalent, no
//     behavior change.
//
// One thing NOT a divergence, worth stating so it isn't mistaken for one:
// Python's per-path-handler `Crawler()`/`Scraper()` construction (fresh,
// zero-argument instances built inline inside _run_pair_path_a/_run_pair_path_d)
// is NOT ported literally — Go's NewCrawler/NewScraper require explicit
// headless/timeout config with no default-parameter equivalent, so
// NewOrchestrator takes one already-configured *Crawler/*Scraper/*Discoverer,
// reused across every pair and every run. This mirrors the exact pattern
// NewDiscoverer already established for its own *Scraper field.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/domain"
	"github.com/udanfernando2006/vestige-go/internal/llm"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

// stockFallbackLLMTimeout bounds llm.Client's HTTP timeout for the
// short-text stock-classification fallback specifically. Go-only, no
// Python equivalent — see package doc comment divergence #8. A short
// classification call (system+user prompt only, max_tokens=5) needs far
// less headroom than selector discovery's full-HTML call
// (discoveryLLMTimeout in discovery.go), hence the smaller value here —
// still a placeholder judgment call, not a measured one.
const stockFallbackLLMTimeout = 30 * time.Second

// directExtractionLLMTimeout bounds llm.Client's HTTP timeout for Path
// D's per-run direct-extraction call. Go-only, no Python equivalent — see
// package doc comment divergence #8. Uses the same magnitude as
// discovery.go's discoveryLLMTimeout since both send a full cleaned-HTML
// payload to a model that can be slow; not independently measured.
const directExtractionLLMTimeout = 120 * time.Second

// --- Result shapes returned by RunAll -------------------------------

// PairResult is one row of RunAll's per-pair result list — direct port of
// the pair_summary dict built inside run_all's loop
// ({"pair_id","book","store","status","changed","price"}).
type PairResult struct {
	PairID  int64
	Book    string
	Store   string
	Status  string
	Changed bool
	Price   *float64
}

// Change is one entry of RunAll's changes list — direct port of the dict
// appended to `changes` inside run_all's loop, flattened with pair/
// book/store context the way _detect_change's own return value doesn't
// carry on its own.
type Change struct {
	PairID     int64
	BookName   string
	StoreName  string
	ProductURL *string
	FromStatus string
	ToStatus   string
	FromPrice  *float64
	ToPrice    *float64
}

// RunError is one entry of RunAll's error list — direct port of the
// {"pair_id","reason"} dicts appended to the `errors` list inside
// run_all's loop (the ELSE branch: anything that isn't COMPLETED or
// NEEDS_SETUP). See package doc comment divergence #7 for how this
// relates to (and differs from) collect_run_summary's own "errors" int.
type RunError struct {
	PairID int64
	Reason *string
}

// RunSummary is RunAll's return shape — direct port of the dict run_all
// returns, reconciling collect_run_summary's totals with the
// run_all-loop-local results/changes/errors/duration_seconds additions.
// See package doc comment divergence #7: unlike Python's actual returned
// dict, Errors (int) is NOT discarded in favor of ErrorEntries (list) —
// both are kept.
type RunSummary struct {
	RunID           string
	TotalPairs      int
	Completed       int
	Errors          int // see divergence #7 — Python's own summary.update() silently discards this in favor of the list below
	NeedsSetup      []int64
	Results         []PairResult
	Changes         []Change
	ErrorEntries    []RunError // corresponds to what Python's final summary["errors"] actually ends up holding
	DurationSeconds float64
}

// pairOutcome is the internal per-pair result shape threaded through
// runPair/runPairPathA-D — direct port of the {"pair_id","status",
// "result"|"reason"} dicts every path handler and run_pair itself return
// in Python. Not exported: RunAll's own RunSummary/PairResult/Change/
// RunError types are the public surface a future Phase 3 handler needs;
// this is purely internal plumbing between run_pair and its callers.
type pairOutcome struct {
	PairID int64
	Status string                     // "COMPLETED" | "NEEDS_SETUP" | "ERROR" (or any other handleError-derived status)
	Result *domain.AvailabilityResult // set only when Status == "COMPLETED"
	Reason *string                    // set when Status != "COMPLETED", mirrors result.get("reason")
}

// pipelinePath mirrors determine_path()'s "A"/"B"/"C"/"D"/"NEEDS_SETUP"
// string return values as a closed, typed set instead of a bare string.
type pipelinePath string

const (
	pathA          pipelinePath = "A"
	pathB          pipelinePath = "B"
	pathC          pipelinePath = "C"
	pathD          pipelinePath = "D"
	pathNeedsSetup pipelinePath = "NEEDS_SETUP"
)

// Orchestrator is the entry point for a scrape run — loads active pairs,
// routes each through its determined path (A/B/C/D), coordinates results
// and change detection. Direct port of orchestrator.py's Orchestrator
// class.
type Orchestrator struct {
	store      store.PairStore
	crawler    *Crawler
	scraper    *Scraper
	discoverer *Discoverer

	// headless/timeout configure the ONE top-level browser.NewSession
	// opened per pair inside runPair — mirrors Python's bare
	// `async with BrowserSession() as session:` (no explicit config;
	// Python's own BrowserSession() defaults apply there). Go has no
	// default-parameter equivalent, so these are supplied explicitly by
	// whatever wires up NewOrchestrator, same convention as
	// NewCrawler/NewScraper's own headless/timeout parameters.
	headless bool
	timeout  time.Duration
}

// NewOrchestrator constructs an Orchestrator. crawler/scraper/discoverer
// are shared, already-configured instances reused across every pair and
// every run — see the package doc comment's closing note for why this is
// NOT a literal port of Python's per-path-handler Crawler()/Scraper()
// construction.
func NewOrchestrator(
	pairStore store.PairStore,
	crawler *Crawler,
	scraper *Scraper,
	discoverer *Discoverer,
	headless bool,
	sessionTimeout time.Duration,
) *Orchestrator {
	return &Orchestrator{
		store:      pairStore,
		crawler:    crawler,
		scraper:    scraper,
		discoverer: discoverer,
		headless:   headless,
		timeout:    sessionTimeout,
	}
}

// --- Settings helpers --------------------------------------------------

// isLLMDiscoveryEnabled is a direct port of is_llm_discovery_enabled(),
// EXCEPT the string-parsing Python does per call
// (`settings["LLM_DISCOVERY_ENABLED"].strip().lower() == "true"`) has
// already happened once, upstream, at the store boundary — see
// settings.go's domain.Settings.LLMDiscoveryEnabled doc comment and
// store.go's GetSettings (`strings.EqualFold(strings.TrimSpace(...), "true")`).
// This is not a behavioral divergence, just an earlier point in the
// pipeline where the identical normalization already ran once.
func (o *Orchestrator) isLLMDiscoveryEnabled(settings *domain.Settings) bool {
	return settings.LLMDiscoveryEnabled
}

// getLLMMode is a direct port of get_llm_mode(): settings["LLM_MODE"]
// still needs the trim+lower normalization here, unlike
// isLLMDiscoveryEnabled above — domain.Settings.LLMMode is stored as the
// raw DB value (store.go's GetSettings assigns raw["LLM_MODE"] directly,
// no normalization), matching Python's own settings dict shape for this
// key exactly.
func (o *Orchestrator) getLLMMode(settings *domain.Settings) string {
	return strings.ToLower(strings.TrimSpace(settings.LLMMode))
}

// determinePath is a direct port of Orchestrator.determine_path().
func (o *Orchestrator) determinePath(pair *store.ActivePair, settings *domain.Settings) pipelinePath {
	hasURL := pair.ProductURL != nil && *pair.ProductURL != ""
	hasSelectors := pair.PriceSelector != nil && *pair.PriceSelector != "" &&
		pair.StockSelector != nil && *pair.StockSelector != ""

	switch {
	case !hasURL:
		return pathA
	case hasURL && hasSelectors:
		return pathC
	case o.getLLMMode(settings) == "direct":
		return pathD
	case o.isLLMDiscoveryEnabled(settings):
		return pathB
	default:
		return pathNeedsSetup
	}
}

// loadActivePairs is a direct port of load_active_pairs() — a thin,
// named wrapper kept for parity with the Python source's own explicit
// method, even though it's a one-line delegation to the store.
func (o *Orchestrator) loadActivePairs(ctx context.Context) ([]store.ActivePair, error) {
	return o.store.GetActivePairs(ctx)
}

// --- Error handling ------------------------------------------------

// handleError is a direct port of Orchestrator.handle_error(). See
// package doc comment divergence #3 (string-matching kept, not sentinel
// errors) and divergence #4b (clear_pair_selectors failure is logged, not
// left to propagate uncaught out of the error handler itself).
func (o *Orchestrator) handleError(ctx context.Context, pair *store.ActivePair, cause error) *domain.AvailabilityResult {
	errMsg := cause.Error()
	fmt.Printf("[ERROR] Pair %d (%s): %s\n", pair.ID, pair.BookName, errMsg)

	errorStatus := "ERROR"
	result := &domain.AvailabilityResult{Status: &errorStatus, Reason: &errMsg}

	if strings.Contains(strings.ToLower(errMsg), "selector_not_found") {
		if clearErr := o.store.ClearPairSelectors(ctx, pair.ID); clearErr != nil {
			// Python has no guard here — a failure at this exact point
			// would propagate straight out of handle_error itself,
			// uncaught (it's inside run_pair's except block, with no
			// nested try). Logged and continued instead, per this
			// project's own "wrap each pair's execution so one failure
			// doesn't abort the whole run" principle. See divergence #4b.
			fmt.Printf("[ERROR] Pair %d: failed to clear selectors after selector_not_found: %v\n", pair.ID, clearErr)
		}
		needsSetupStatus := "NEEDS_SETUP"
		selectorNotFoundReason := "selector_not_found"
		result.Status = &needsSetupStatus
		result.Reason = &selectorNotFoundReason
	}

	return result
}

// --- Two-tier stock-status fallback (v3.9 parity) ----------------------

// classifyStockFallback is a direct port of _classify_stock_fallback():
// tries DIRECT_* credentials, then SELECTOR_*, never returns a Go error —
// a nil *bool return is the "couldn't classify, or role not configured"
// case, matching Python's `return None`.
func (o *Orchestrator) classifyStockFallback(ctx context.Context, settings *domain.Settings, rawText string) *bool {
	type roleCreds struct {
		role    string
		apiBase string
		apiKey  string
		model   string
	}
	// Fixed order: DIRECT_* first, then SELECTOR_* — matches
	// vestige_guide.md Section 5's documented fallback order exactly.
	roles := []roleCreds{
		{"DIRECT", settings.DirectAPIBase, settings.DirectAPIKey, settings.DirectModel},
		{"SELECTOR", settings.SelectorAPIBase, settings.SelectorAPIKey, settings.SelectorModel},
	}

	for _, r := range roles {
		if strings.TrimSpace(r.apiBase) == "" || strings.TrimSpace(r.model) == "" {
			continue
		}

		llmClient, err := llm.NewClient(r.apiBase, r.apiKey, stockFallbackLLMTimeout)
		if err != nil {
			fmt.Printf("[Stock Fallback] %s role attempt failed: %v\n", r.role, err)
			continue
		}
		extractor, err := NewExtractor(ExtractorConfig{
			APIBase:   r.apiBase,
			APIKey:    r.apiKey,
			ModelName: r.model,
		}, llmClient)
		if err != nil {
			fmt.Printf("[Stock Fallback] %s role attempt failed: %v\n", r.role, err)
			continue
		}

		result, err := extractor.ClassifyStockStatus(ctx, rawText)
		if err != nil {
			// Per ClassifyStockStatus's own doc comment, this should be
			// rare — it only ever wraps a Go-specific failure Python's
			// implementation has no path for (e.g. context cancellation).
			fmt.Printf("[Stock Fallback] %s role attempt failed: %v\n", r.role, err)
			continue
		}
		if result != nil {
			return result
		}
		// nil, nil: this role's model couldn't classify it — try the next role.
	}

	return nil
}

// applyStockFallbackIfNeeded is a direct port of
// _apply_stock_fallback_if_needed(): the gate — fires ONLY when
// status=="ERROR" AND reason=="unparseable_stock_status" AND raw stock
// text is actually present (distinguishing this from a separate
// selector_not_found case, where no text was captured at all).
func (o *Orchestrator) applyStockFallbackIfNeeded(ctx context.Context, scrapeData *domain.AvailabilityResult, settings *domain.Settings) *domain.AvailabilityResult {
	if scrapeData.Status == nil || *scrapeData.Status != "ERROR" {
		return scrapeData
	}
	if scrapeData.Reason == nil || *scrapeData.Reason != "unparseable_stock_status" {
		return scrapeData
	}
	if scrapeData.RawStockText == nil || *scrapeData.RawStockText == "" {
		return scrapeData
	}

	fallback := o.classifyStockFallback(ctx, settings, *scrapeData.RawStockText)
	if fallback != nil {
		scrapeData.InStock = fallback
		resolvedStatus := "OUT_OF_STOCK"
		if *fallback {
			resolvedStatus = "IN_STOCK"
		}
		scrapeData.Status = &resolvedStatus
		scrapeData.Reason = nil
	}
	return scrapeData
}

// --- Change detection ------------------------------------------------

// changeRecord is _detect_change()'s return shape before run_all flattens
// it into a Change with pair/book/store context added.
type changeRecord struct {
	FromStatus string
	ToStatus   string
	FromPrice  *float64
	ToPrice    *float64
}

// detectChange is a direct port of Orchestrator._detect_change(). Diffs
// status AND price (rounded to 2dp) against the prior snapshot; a nil
// last snapshot (first-ever scrape for this pair) is never reported as a
// change, matching Python's `if last is None: return None`.
func (o *Orchestrator) detectChange(last *domain.AvailabilitySnapshot, availability *domain.AvailabilityResult) *changeRecord {
	if last == nil {
		return nil
	}

	statusChanged := !boolPtrEqual(last.InStock, availability.InStock)
	priceChanged := last.Price != nil && availability.Price != nil &&
		roundTo2(*last.Price) != roundTo2(*availability.Price)

	if !statusChanged && !priceChanged {
		return nil
	}

	toStatus := ""
	if availability.Status != nil {
		toStatus = *availability.Status
	}

	change := &changeRecord{FromStatus: last.Status, ToStatus: toStatus}
	if last.Price != nil {
		v := roundTo2(*last.Price)
		change.FromPrice = &v
	}
	if availability.Price != nil {
		v := roundTo2(*availability.Price)
		change.ToPrice = &v
	}
	return change
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func roundTo2(v float64) float64 {
	return math.Round(v*100) / 100
}

// joinPatterns joins a []string of custom stock-status regex patterns
// back into the single comma-separated string Scraper.Scrape's
// customIn/customOut parameters expect — the Go-side split already
// happened once, at the store boundary (store.go's GetSettings splits on
// ","), so this is just re-joining for the one call site that still wants
// the flat string shape. Matches discovery.go's own inline
// strings.Join(settings.CustomStock*Patterns, ",") usage exactly.
func joinPatterns(patterns []string) string {
	return strings.Join(patterns, ",")
}

// --- Run summary aggregation ------------------------------------------

// collectRunSummary is a direct port of Orchestrator.collect_run_summary().
// See package doc comment divergence #7 for how this relates to (and is
// NOT silently overwritten the way Python's own dict is) run_all's final
// RunSummary.
func (o *Orchestrator) collectRunSummary(results []PairResult) RunSummary {
	summary := RunSummary{TotalPairs: len(results)}
	for _, res := range results {
		switch res.Status {
		case "IN_STOCK", "OUT_OF_STOCK", "NOT_LISTED":
			summary.Completed++
		case "NEEDS_SETUP":
			summary.NeedsSetup = append(summary.NeedsSetup, res.PairID)
		default:
			summary.Errors++
		}
	}
	return summary
}

// --- Path A: no product URL -----------------------------------------

// runPairPathA is a direct port of _run_pair_path_a(). Any error from the
// Crawler, or from the "not success" branch below, propagates unchanged
// back to runPair's single catch point — mirroring Python's own
// try/except-that-just-re-raises around the find_product_url() call (a
// no-op in Python, so nothing extra to replicate on that specific point).
func (o *Orchestrator) runPairPathA(ctx context.Context, pair *store.ActivePair, session browser.Session, settings *domain.Settings) (pairOutcome, error) {
	st, err := o.store.GetStore(ctx, pair.StoreID)
	if err != nil {
		return pairOutcome{}, fmt.Errorf("failed to load store %d: %w", pair.StoreID, err)
	}
	if st == nil {
		return pairOutcome{}, fmt.Errorf("store %d not found", pair.StoreID)
	}

	crawlResult, err := o.crawler.FindProductURL(ctx, st, pair.BookName, pair.BookISBN, session)
	if err != nil {
		return pairOutcome{}, err
	}
	if crawlResult == nil || !crawlResult.Success {
		errMsg := "No result returned"
		if crawlResult != nil {
			errMsg = "Unknown error"
			if crawlResult.Error != nil {
				errMsg = *crawlResult.Error
			}
		}
		return pairOutcome{}, fmt.Errorf("Crawler failed to discover URL: %s", errMsg)
	}

	// Mirrors `if crawl_result.get("search_url_template"):` — a truthy
	// check, so an empty-but-non-nil template is treated the same as
	// absent.
	if st.SearchURLTemplate == nil && crawlResult.SearchURLTemplate != nil && *crawlResult.SearchURLTemplate != "" {
		if err := o.store.UpdateStoreSearchTemplate(ctx, pair.StoreID, *crawlResult.SearchURLTemplate); err != nil {
			return pairOutcome{}, fmt.Errorf("failed to persist search template for store %d: %w", pair.StoreID, err)
		}
	}

	// crawlResult.ProductURL should always be set when Success==true per
	// CrawlResult's own doc comment; defensively falling back to "" rather
	// than panicking if that contract is ever violated. Note UpdatePairURL's
	// interface signature takes a plain string, not *string — there is no
	// way to represent "write NULL" through this specific call, a pre-
	// existing interface limitation, not something introduced here.
	foundURL := ""
	if crawlResult.ProductURL != nil {
		foundURL = *crawlResult.ProductURL
	}

	if err := o.store.UpdatePairURL(ctx, pair.ID, foundURL); err != nil {
		return pairOutcome{}, fmt.Errorf("failed to persist discovered url for pair %d: %w", pair.ID, err)
	}
	fmt.Fprintf(os.Stderr, "[Orchestrator] Path A: pair %d discovered url=%s\n", pair.ID, foundURL)
	// Mutate the local pair so the B/D handler this dispatches to next
	// sees the freshly discovered URL — mirrors Python's
	// `pair["product_url"] = found_url` (a same-object mutation visible
	// to the very next call, since pair is a *store.ActivePair here too).
	pair.ProductURL = &foundURL

	// crawlResult.HTML (NEW, bug 4b redesign) is the page HTML Crawler
	// already fetched while confirming foundURL — passed through to
	// Path B/D so neither has to re-navigate to the SAME url on this
	// SAME session a second time. This is precisely the second-navigation
	// pattern Cloudflare was found to block on jumpbooks.lk. May be nil
	// (e.g. a NOT_LISTED-adjacent path that still somehow succeeded, or a
	// future CrawlResult-constructing branch that doesn't set it) — both
	// runPairPathB/D treat a nil prefetchedHTML as "fetch normally,"
	// identical to today's behavior.
	if o.getLLMMode(settings) == "direct" {
		return o.runPairPathD(ctx, pair, session, settings, crawlResult.HTML)
	}
	return o.runPairPathB(ctx, pair, session, settings, crawlResult.HTML)
}

// --- Path B: URL present, no selectors, LLM_MODE=selector -----------

// runPairPathB is a direct port of _run_pair_path_b(). See package doc
// comment divergences #1 and #2 for the full reasoning behind the
// subprocess -> in-process Discoverer.Run replacement.
// prefetchedHTML is NEW (bug 4b redesign), no Python equivalent — see the
// standalone implementation plan document ("Path B Redundant-Navigation
// Redesign") for the full background. Non-nil only when this call arrived
// via Path A's redirect-shortcut this same run (Crawler already navigated
// `session` once to confirm the product URL); nil when Path B is reached
// directly (a pair that already had product_url set from a prior run, no
// preceding Path A this run — nothing to prefetch).
//
// This supersedes the previous unconditional design, which ALWAYS called
// o.scraper.Scrape(...) again on `session` after a successful discovery
// commit — a SECOND navigation on a session that, when reached via Path A,
// had already navigated once this run. Live testing against jumpbooks.lk
// (non-headless) confirmed this second navigation gets Cloudflare-blocked
// (bug 4b): the search+redirect (navigation #1) succeeds cleanly, LLM
// selector discovery/self-consistency validation succeeds, but the
// re-scrape (navigation #2 on the same session) gets served a generic
// challenge page, both selectors return <no match>, and the pair
// incorrectly lands on NEEDS_SETUP despite everything upstream having
// actually worked. The fix removes the second navigation's precondition
// entirely rather than isolating it: Discoverer.Run's own --commit
// validation already computes a full self-consistency scrape internally
// (discovery.go's scraped local, now exposed via DiscoverResult.ScrapeResult)
// — reusing that IS the pair's first real snapshot, so no re-scrape call
// is needed here at all once discovery has committed.
func (o *Orchestrator) runPairPathB(ctx context.Context, pair *store.ActivePair, session browser.Session, settings *domain.Settings, prefetchedHTML *string) (pairOutcome, error) {
	if !o.isLLMDiscoveryEnabled(settings) {
		if err := o.store.UpdatePairStatus(ctx, pair.ID, "NEEDS_SETUP"); err != nil {
			return pairOutcome{}, fmt.Errorf("failed to mark pair %d NEEDS_SETUP: %w", pair.ID, err)
		}
		return pairOutcome{PairID: pair.ID, Status: "NEEDS_SETUP"}, nil
	}

	discoveryFailed := false
	var discoverySession browser.Session

	if prefetchedHTML == nil {
		// No prior navigation on `session` this run (Path B reached
		// directly, not via Path A) — fork exactly as before. See the
		// package's own "design choice considered and rejected" note in
		// the implementation plan: session itself is deliberately NOT
		// used directly for Discoverer's fetch even though it hasn't
		// navigated yet in this specific branch, because a discovery
		// failure falls back to Path D on this SAME session below — using
		// session directly here would let Path D's own navigate become
		// the second navigation this whole redesign exists to avoid.
		var sessErr error
		discoverySession, sessErr = session.FreshContext(ctx)
		if sessErr != nil {
			// Mirrors "same fallback-on-failure as today" from discovery.go's
			// package doc comment's own Path B sketch — an inability to even
			// open an isolated discovery session is treated identically to a
			// failed discovery/validation attempt below. See divergence #2.
			discoveryFailed = true
		}
	}
	// else: prefetchedHTML != nil means Path A already navigated `session`
	// once this run — skip the fork AND skip Discoverer's own navigation
	// entirely by passing PrefetchedHTML through below.

	var discoverResult DiscoverResult
	if !discoveryFailed {
		var discErr error
		discoverResult, discErr = o.discoverer.Run(ctx, discoverySession, DiscoverOptions{
			PairID: pair.ID, Commit: true, PrefetchedHTML: prefetchedHTML,
		})
		// Close discoverySession immediately once Run() returns, NOT
		// deferred to this function's own return — matches the prior
		// behavior exactly when it was opened. A nil discoverySession
		// (the prefetchedHTML != nil branch above) has nothing to close.
		if discoverySession != nil {
			discoverySession.Close(ctx)
		}

		if discErr != nil || !discoverResult.Committed {
			discoveryFailed = true
		}
	}

	if discoveryFailed {
		// Mirrors Python's LOCAL try/except around the Path D fallback —
		// this catch point is inside runPairPathB itself, not propagated
		// to runPair's outer dispatch, matching discover_selectors.py's
		// subprocess-failure branch exactly. prefetchedHTML threads
		// through here too (NEW) so the fallback doesn't re-navigate
		// either, for the same reason as the primary discovery attempt.
		outcome, fallbackErr := o.runPairPathD(ctx, pair, session, settings, prefetchedHTML)
		if fallbackErr != nil {
			fmt.Printf("Path B: Direct-extraction fallback also failed for Pair %d: %v\n", pair.ID, fallbackErr)
			if setErr := o.store.UpdatePairStatus(ctx, pair.ID, "NEEDS_SETUP"); setErr != nil {
				return pairOutcome{}, fmt.Errorf("failed to mark pair %d NEEDS_SETUP after fallback failure: %w", pair.ID, setErr)
			}
			return pairOutcome{PairID: pair.ID, Status: "NEEDS_SETUP"}, nil
		}
		return outcome, nil
	}

	fmt.Fprintf(os.Stderr, "[Orchestrator] Path B: pair %d discovery committed — price_selector=%s stock_selector=%s\n",
		pair.ID, derefOrNotFound(discoverResult.PriceSelector), derefOrNotFound(discoverResult.StockSelector))

	// NEW (bug 4b redesign): NO second o.scraper.Scrape(...) call on
	// `session` at all — reuse the full self-consistency scrape
	// Discoverer.Run already computed internally during --commit
	// validation. This is what eliminates the second-navigation-on-the-
	// same-session pattern Cloudflare was found to block; it does not
	// merely isolate it. See discovery.go's DiscoverResult.ScrapeResult
	// doc comment and the standalone implementation plan document
	// ("Section 5: Trade-off, Stated Plainly") for the honest cost: this
	// pair's FIRST snapshot after a fresh discovery reflects a
	// self-consistency check (the HTML the LLM was shown), not an
	// independently re-verified fetch moments later — a one-time-per-pair
	// effect, since every later run goes through Path C with a genuine
	// fresh scrape.
	if discoverResult.ScrapeResult == nil {
		// Defensive: per discovery.go's own contract, ScrapeResult is
		// always set when Committed is true — this should never happen.
		// Guarded rather than nil-dereferenced so a contract violation
		// surfaces as a clear error instead of a panic.
		return pairOutcome{}, fmt.Errorf("pair %d: discovery committed but returned no scrape result", pair.ID)
	}
	scrapeData := o.applyStockFallbackIfNeeded(ctx, discoverResult.ScrapeResult, settings)

	if (scrapeData.RawStockText == nil || *scrapeData.RawStockText == "") &&
		(scrapeData.RawPriceText == nil || *scrapeData.RawPriceText == "") {
		fmt.Fprintf(os.Stderr, "[Orchestrator] Path B: pair %d self-consistency scrape found neither price nor stock text\n", pair.ID)
		// Mirrors `raise Exception("selector_not_found")` — see
		// divergence #3.
		return pairOutcome{}, errors.New("selector_not_found")
	}
	source := "scraper"
	scrapeData.Source = &source
	return pairOutcome{PairID: pair.ID, Status: "COMPLETED", Result: scrapeData}, nil
}

// --- Path C: URL + cached selectors present (production fast path) -----

// runPairPathC is a direct port of _run_pair_path_c(). v3.9 fixed a
// pre-existing Python bug where this handler didn't receive `settings` at
// all (see vestige_archive_history_catalog.md's v3.9 entry) — settings is
// a required parameter here from the start, so that specific historical
// bug has no Go equivalent to reproduce.
func (o *Orchestrator) runPairPathC(ctx context.Context, pair *store.ActivePair, session browser.Session, settings *domain.Settings) (pairOutcome, error) {
	selectors := map[string]*SelectorConfig{
		"price":        {Selector: pair.PriceSelector, DirectText: true},
		"availability": {Selector: pair.StockSelector},
	}
	productURL := ""
	if pair.ProductURL != nil {
		productURL = *pair.ProductURL
	}
	fmt.Fprintf(os.Stderr, "[Orchestrator] Path C: pair %d scraping url=%s price_selector=%s stock_selector=%s\n",
		pair.ID, productURL, derefOrNotFound(pair.PriceSelector), derefOrNotFound(pair.StockSelector))

	scrapeData, err := o.scraper.Scrape(ctx, productURL, selectors, nil, session,
		joinPatterns(settings.CustomStockInPatterns), joinPatterns(settings.CustomStockOutPatterns))
	if err != nil {
		return pairOutcome{}, err
	}
	scrapeData = o.applyStockFallbackIfNeeded(ctx, scrapeData, settings)

	if (scrapeData.RawStockText == nil || *scrapeData.RawStockText == "") &&
		(scrapeData.RawPriceText == nil || *scrapeData.RawPriceText == "") {
		fmt.Fprintf(os.Stderr, "[Orchestrator] Path C: pair %d re-scrape found neither price nor stock text\n", pair.ID)
		return pairOutcome{}, errors.New("selector_not_found")
	}
	source := "scraper"
	scrapeData.Source = &source
	return pairOutcome{PairID: pair.ID, Status: "COMPLETED", Result: scrapeData}, nil
}

// --- Path D: URL present, no selectors, LLM_MODE=direct --------------

// runPairPathD is a direct port of _run_pair_path_d(). See package doc
// comment divergence #5 for the intentional (Python-source-inherited)
// omission of custom stock patterns from the ParseStockStatus call below.
// prefetchedHTML is NEW (bug 4b redesign) — see runPairPathB's doc
// comment and the standalone implementation plan document. Non-nil when
// this call arrived via Path A's redirect-shortcut this same run (or via
// Path B's own discovery-failure fallback, which threads its own
// prefetchedHTML through unchanged); skips this function's own
// navigate+fetch entirely in that case, avoiding a redundant second
// navigation on `session`. nil (the prior, only) behavior is unchanged:
// navigate and fetch as before.
func (o *Orchestrator) runPairPathD(ctx context.Context, pair *store.ActivePair, session browser.Session, settings *domain.Settings, prefetchedHTML *string) (pairOutcome, error) {
	var htmlStr string
	if prefetchedHTML != nil {
		htmlStr = *prefetchedHTML
	} else {
		productURL := ""
		if pair.ProductURL != nil {
			productURL = *pair.ProductURL
		}
		if err := session.Navigate(ctx, productURL); err != nil {
			return pairOutcome{}, err
		}
		fetched, err := session.GetHTML(ctx)
		if err != nil {
			return pairOutcome{}, err
		}
		htmlStr = fetched
	}

	llmClient, err := llm.NewClient(settings.DirectAPIBase, settings.DirectAPIKey, directExtractionLLMTimeout)
	if err != nil {
		return pairOutcome{}, fmt.Errorf("path D: failed to build LLM client: %w", err)
	}
	extractor, err := NewExtractor(ExtractorConfig{
		Engine:    EngineStripped,
		APIBase:   settings.DirectAPIBase,
		APIKey:    settings.DirectAPIKey,
		ModelName: settings.DirectModel,
	}, llmClient)
	if err != nil {
		return pairOutcome{}, fmt.Errorf("path D: failed to construct extractor: %w", err)
	}

	cleanedHTML, err := extractor.CleanHTML(htmlStr)
	if err != nil {
		return pairOutcome{}, err
	}

	details, err := extractor.ExtractDetails(ctx, cleanedHTML, pair.BookName, []string{"price", "stock_status"})
	if err != nil {
		// Mirrors `if "error" in details: raise Exception(f"LLM Direct
		// Extraction failed: {details['error']}")` — ExtractDetails
		// already returns a Go error instead of an in-band {"error":...}
		// dict (per its own doc comment), so this branches on err != nil
		// directly rather than inspecting a result field.
		return pairOutcome{}, fmt.Errorf("LLM Direct Extraction failed: %w", err)
	}

	priceText := ""
	if details.Price != nil {
		priceText = *details.Price
	}
	stockText := ""
	if details.StockStatus != nil {
		stockText = *details.StockStatus
	}

	parsedPrice := ParsePrice(priceText)
	// NOTE (divergence #5, CORRECTED per user confirmation): unlike
	// orchestrator.py's own Path D, this DOES thread the configured
	// custom stock patterns through — the Python source's omission here
	// was confirmed to be a real bug, not an intentional asymmetry with
	// Paths B/C, and is tracked separately for a matching Python-side fix
	// post-migration.
	parsedStock := ParseStockStatus(stockText,
		joinPatterns(settings.CustomStockInPatterns), joinPatterns(settings.CustomStockOutPatterns))

	result := domain.NewAvailabilityResult()
	result.InStock = parsedStock
	result.Price = parsedPrice
	// Assigned unconditionally, even if nil — mirrors
	// `raw_price_text=details.get("price")` running unconditionally in
	// Python (DetailsResult.Price/StockStatus are already *string, so nil
	// here is the direct equivalent of Python's None).
	result.RawPriceText = details.Price
	result.RawStockText = details.StockStatus

	resolvedStatus := "ERROR"
	switch {
	case parsedStock != nil && *parsedStock:
		resolvedStatus = "IN_STOCK"
	case parsedStock != nil && !*parsedStock:
		resolvedStatus = "OUT_OF_STOCK"
	}
	result.Status = &resolvedStatus
	source := "llm_direct"
	result.Source = &source

	return pairOutcome{PairID: pair.ID, Status: "COMPLETED", Result: result}, nil
}

// --- Lazy per-pair browser session --------------------------------------

// lazySession wraps browser.NewSession so the underlying Chromium process
// is only actually spawned the first time one of its methods is genuinely
// called, rather than eagerly at the top of runPair. This is NEW, no
// Python equivalent — Python's subprocess-per-invocation model never had
// an "idle browser instance" concept to begin with, since a subprocess
// that never navigates never launches a browser in the first place.
//
// The bug this closes, confirmed via live testing and source review, not
// just theorized: runPair previously opened one top-level
// browser.NewSession unconditionally, before it's even known which path
// (A/B/C/D) — let alone which branch within Path A — will actually run.
// Path A's no-cached-template branch (Crawler.FindProductURL ->
// runDiscovery) opens two-to-three of its OWN independent top-level
// sessions internally and never touches the session runPair handed it.
// Once that branch succeeds, CrawlResult.HTML is always populated (both
// checkForSingleResultRedirect and tryValidateCandidate set it), which
// means runPairPathB and runPairPathD's prefetchedHTML!=nil branches
// don't touch runPair's session either — see both functions' own doc
// comments. Net effect: a full, real, visible Chromium window sat open
// and completely idle for that pair's entire run. Confirmed live
// (non-headless run against jeyabookcentre.com showed a second, visibly
// blank browser window alongside the one actually doing the discovery
// work) and confirmed via direct source review of every call site above.
//
// Every OTHER path still needs a real session exactly as before: Path A
// WITH a cached template (Crawler.runWithSession uses it directly), Path
// C (always), and Path B/D reached directly rather than via Path A this
// run (prefetchedHTML == nil, so they call session.FreshContext/Navigate
// for real). Lazy creation is transparent to all of those — the first
// genuine method call spawns the real session exactly once, memoized for
// every call after that, so nothing in Crawler/Discoverer/Scraper/
// Extractor needs to change; they all only ever see the browser.Session
// interface.
//
// One deliberate, minor behavioral trade-off, stated plainly: previously
// a broken/unreachable Chromium install failed immediately and clearly at
// the top of runPair ("failed to open browser session: %w"). With lazy
// creation, that same failure now surfaces at whatever point first tries
// to use the session — inside runWithSession's Navigate call, Path C's
// Scraper.Scrape, or Path B/D's own Navigate/FreshContext — wrapped in
// that call's own error instead of one common message. The pair still
// ends in the same ERROR outcome via the same runPair->handleError path
// either way; only the specific wrapping message differs. For the flow
// this fix targets (Path A, no cached template), a broken Chromium
// install still fails immediately and clearly regardless, since
// runDiscovery opens its own real sessions eagerly and independently of
// this wrapper.
type lazySession struct {
	headless bool
	timeout  time.Duration

	real browser.Session // nil until first genuine use
}

func newLazySession(headless bool, timeout time.Duration) *lazySession {
	return &lazySession{headless: headless, timeout: timeout}
}

// ensure spawns the real underlying session on first call, and returns
// the already-spawned one on every call after that.
func (l *lazySession) ensure(ctx context.Context) (browser.Session, error) {
	if l.real != nil {
		return l.real, nil
	}
	sess, err := browser.NewSession(ctx, l.headless, l.timeout)
	if err != nil {
		return nil, err
	}
	l.real = sess
	return sess, nil
}

func (l *lazySession) Navigate(ctx context.Context, targetURL string) error {
	sess, err := l.ensure(ctx)
	if err != nil {
		return err
	}
	return sess.Navigate(ctx, targetURL)
}

func (l *lazySession) GetHTML(ctx context.Context) (string, error) {
	sess, err := l.ensure(ctx)
	if err != nil {
		return "", err
	}
	return sess.GetHTML(ctx)
}

func (l *lazySession) GetURL(ctx context.Context) (string, error) {
	sess, err := l.ensure(ctx)
	if err != nil {
		return "", err
	}
	return sess.GetURL(ctx)
}

func (l *lazySession) WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error {
	sess, err := l.ensure(ctx)
	if err != nil {
		return err
	}
	return sess.WaitForSelector(ctx, selector, timeout)
}

func (l *lazySession) FindAndFillSearch(ctx context.Context, query string) (bool, error) {
	sess, err := l.ensure(ctx)
	if err != nil {
		return false, err
	}
	return sess.FindAndFillSearch(ctx, query)
}

func (l *lazySession) FreshContext(ctx context.Context) (browser.Session, error) {
	sess, err := l.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return sess.FreshContext(ctx)
}

// Close is a no-op if the real session was never actually opened — the
// common case for the exact flow this fix targets, where there was never
// a Chromium process to close in the first place.
func (l *lazySession) Close(ctx context.Context) error {
	if l.real == nil {
		return nil
	}
	return l.real.Close(ctx)
}

var _ browser.Session = (*lazySession)(nil)

// --- Master per-pair dispatcher --------------------------------------

// runPair is a direct port of Orchestrator.run_pair() — the single catch
// point for any exception a path handler raises, mirroring Python's one
// try/except wrapping the A/B/C/D dispatch (Path B's own internal
// try/except around its Path D fallback is a SEPARATE, nested catch that
// never reaches here — see runPairPathB).
func (o *Orchestrator) runPair(ctx context.Context, pair *store.ActivePair, path pipelinePath, settings *domain.Settings) pairOutcome {
	fmt.Printf("Running Pair ID %d via Path %s\n", pair.ID, path)

	if path == pathNeedsSetup {
		if err := o.store.UpdatePairStatus(ctx, pair.ID, "NEEDS_SETUP"); err != nil {
			// Python has no guard here either — a DB failure at this
			// exact point would propagate uncaught in Python too. Logged
			// and continued instead; see divergence #4.
			fmt.Printf("[ERROR] Pair %d: failed to persist NEEDS_SETUP status: %v\n", pair.ID, err)
		}
		return pairOutcome{PairID: pair.ID, Status: "NEEDS_SETUP"}
	}

	// Lazily opened, not eagerly here — see lazySession's doc comment for
	// the real, confirmed bug this closes: Path A's no-cached-template
	// branch (Crawler.runDiscovery) opens its own independent sessions
	// internally and never touches the session runPair hands it, and once
	// that succeeds, crawlResult.HTML is always populated, so neither
	// runPairPathB nor runPairPathD's prefetchedHTML!=nil branches touch
	// it either — meaning a full, real Chromium process previously sat
	// open and completely idle for that entire flow. lazySession defers
	// the actual browser.NewSession call to the first genuine method call,
	// so that flow now never opens one at all; every other path (cached-
	// template Path A, Path C, bare Path B/D) triggers creation
	// transparently on first real use, exactly as before from every
	// caller's point of view.
	session := newLazySession(o.headless, o.timeout)
	defer session.Close(ctx)

	var outcome pairOutcome
	var pathErr error

	switch path {
	case pathA:
		outcome, pathErr = o.runPairPathA(ctx, pair, session, settings)
	case pathB:
		// nil prefetchedHTML (NEW param, bug 4b redesign) — a bare
		// top-level Path B (no preceding Path A this run, e.g. a pair
		// that already had product_url from a prior run) never has
		// anything prefetched to hand it.
		outcome, pathErr = o.runPairPathB(ctx, pair, session, settings, nil)
	case pathC:
		outcome, pathErr = o.runPairPathC(ctx, pair, session, settings)
	case pathD:
		// nil prefetchedHTML — same reasoning as pathB above.
		outcome, pathErr = o.runPairPathD(ctx, pair, session, settings, nil)
	default:
		pathErr = fmt.Errorf("unknown path routing: %s", path)
	}

	if pathErr != nil {
		errResult := o.handleError(ctx, pair, pathErr)
		return outcomeFromErrorResult(pair.ID, errResult)
	}
	return outcome
}

func outcomeFromErrorResult(pairID int64, errResult *domain.AvailabilityResult) pairOutcome {
	status := ""
	if errResult.Status != nil {
		status = *errResult.Status
	}
	return pairOutcome{PairID: pairID, Status: status, Reason: errResult.Reason}
}

// --- Top-level run entry point -----------------------------------------

// RunAll is a direct port of Orchestrator.run_all(). See package doc
// comment divergence #4a (get_last_snapshot/write_snapshot failures are
// logged and the pair is skipped, rather than crashing the entire run)
// and divergence #7 (the int/list "errors" collision in Python's own
// returned dict — this port keeps both values rather than discarding one).
func (o *Orchestrator) RunAll(ctx context.Context) (*RunSummary, error) {
	runID := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	startTime := time.Now().UTC()

	pairs, err := o.loadActivePairs(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: failed to load active pairs: %w", err)
	}
	fmt.Printf("[%s] Starting run — %d active pairs\n", runID, len(pairs))

	settings, err := o.store.GetSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: failed to load settings: %w", err)
	}

	var allResults []PairResult
	var changes []Change
	var errorEntries []RunError

	for i := range pairs {
		pair := &pairs[i]
		path := o.determinePath(pair, settings)
		result := o.runPair(ctx, pair, path, settings)

		pairSummary := PairResult{
			PairID:  pair.ID,
			Book:    pair.BookName,
			Store:   pair.StoreName,
			Status:  result.Status,
			Changed: false,
		}

		if result.Status == "COMPLETED" {
			// Capture the prior snapshot BEFORE writing the new one —
			// mirrors Python's `last = self.db_writer.get_last_snapshot(...)`
			// running before `self.db_writer.write_snapshot(...)`.
			last, snapErr := o.store.GetLastSnapshot(ctx, pair.ID)
			if snapErr != nil {
				// See divergence #4a — logged and this pair is skipped
				// for change-detection/summary purposes, rather than
				// crashing the entire run the way an uncaught Python
				// exception here would.
				fmt.Printf("[ERROR] Pair %d: failed to load last snapshot: %v\n", pair.ID, snapErr)
			}

			availability := result.Result
			if _, writeErr := o.store.WriteSnapshot(ctx, pair.ID, availability); writeErr != nil {
				fmt.Printf("[ERROR] Pair %d: failed to write snapshot: %v\n", pair.ID, writeErr)
			}

			pairSummary.Price = availability.Price
			if availability.Status != nil {
				// Overwrites the transient "COMPLETED" sentinel with the
				// REAL status (IN_STOCK/OUT_OF_STOCK/ERROR — see
				// divergence #6) — mirrors
				// `pair_summary["status"] = availability.status` exactly.
				pairSummary.Status = *availability.Status
			}

			change := o.detectChange(last, availability)
			if change != nil {
				pairSummary.Changed = true
				changes = append(changes, Change{
					PairID:     pair.ID,
					BookName:   pair.BookName,
					StoreName:  pair.StoreName,
					ProductURL: pair.ProductURL,
					FromStatus: change.FromStatus,
					ToStatus:   change.ToStatus,
					FromPrice:  change.FromPrice,
					ToPrice:    change.ToPrice,
				})
			}
		} else if result.Status == "NEEDS_SETUP" {
			// The store was already updated to NEEDS_SETUP inside the
			// path handler itself (or by handleError) — nothing further
			// to do here, matching Python's bare `pass`.
		} else {
			errorEntries = append(errorEntries, RunError{PairID: pair.ID, Reason: result.Reason})
		}

		allResults = append(allResults, pairSummary)
	}

	durationSeconds := time.Since(startTime).Seconds()
	summary := o.collectRunSummary(allResults)
	summary.RunID = runID
	summary.Results = allResults
	summary.Changes = changes
	summary.ErrorEntries = errorEntries
	summary.DurationSeconds = math.Round(durationSeconds*10) / 10

	fmt.Printf("[%s] Done — %d completed, %d changes, %d errors in %.1fs\n",
		runID, summary.Completed, len(changes), len(errorEntries), durationSeconds)

	return &summary, nil
}
