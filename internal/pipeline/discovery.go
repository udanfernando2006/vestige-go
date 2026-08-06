// Package pipeline holds the Orchestrator/Crawler/Scraper/Extractor/
// Discoverer control-flow code — the browser/DB-touching layer that calls
// into the pure functions in pipeline/heuristics. See
// vestige_go_pipeline_implementation.md Section 1 for the full package
// layout this file is part of.
//
// Source-verified against discover_selectors.py in full, plus the real
// store.PairStore interface (store.go), browser.Session interface
// (session.go), llm.NewClient (client.go), and this package's own
// extractor.go/scraper.go — not fabricated from vestige_guide.md's prose
// description of the CLI tool alone.
//
// discover_selectors.py replaced entirely — no standalone CLI, no
// --url/--store mode. See vestige_go_archive_history_catalog.md Part B
// for the full reasoning: the CLI's direct-URL mode was a tinkering-phase
// debugging harness, never a load-bearing production path, and dropping
// it lets DiscoverOptions.PairID be a required int64 instead of a
// nullable field distinguishing "DB mode" from "direct-URL mode."
//
// Four deliberate divergences from discover_selectors.py, each flagged
// again at its point of use below rather than only here:
//
//  1. Discoverer holds a *Scraper, not a pre-built llm.Client — Settings
//     (and therefore SELECTOR_* credentials) are fetched fresh from
//     store.PairStore on every Run() call, matching the project's
//     no-settings-caching principle (vestige_guide.md Section 7: "nothing
//     on either side caches settings at process startup"). Building one
//     llm.Client/Extractor per Run() call, from whatever Settings say
//     *right now*, is the faithful behavior — a field pre-bound to
//     construction-time credentials would silently go stale after any
//     Settings-page edit.
//
//  2. CORRECTED after live testing — see divergence #5 below. This file's
//     first draft claimed Run() could safely reuse exactly ONE
//     caller-supplied browser.Session for both the initial HTML fetch and
//     the later validation scrape, reasoning that discover_selectors.py's
//     two separate `async with BrowserSession()` blocks were merely an
//     artifact of the subprocess-CLI model, not a load-bearing isolation
//     requirement. Live testing against jumpbooks.lk (cmd/discovercheck,
//     -commit) proved that reasoning wrong: four different models each
//     produced plausible-looking selectors that ALL failed validation
//     with "no_match" (not "missing" — the LLM proposed real selectors,
//     they just matched nothing), while the identical code against
//     jeyabookcentre.com (no known WAF) succeeded first try. This is the
//     exact "first navigation on a session succeeds, second gets
//     Cloudflare-blocked" pattern crawler.go's tryValidateCandidate doc
//     comment already documents from its own live testing — Run()'s
//     initial fetch is navigation #1 on the caller's session; the
//     validation scrape's internal Navigate call was navigation #2 on
//     that SAME session, and got blocked regardless of which selectors it
//     was even trying. See divergence #5 for the actual fix.
//
//  3. --commit validation is done by calling the REAL Scraper.Scrape
//     against the candidate selectors, not a reimplementation of
//     validate_selectors()'s bespoke BeautifulSoup digit/keyword checks.
//     This changes what "passes" means: Python asks "does the raw text
//     merely look plausible" (contains a digit / contains a stock
//     keyword); this asks "did ParsePrice/ClassifyStockText actually
//     resolve it" — using the exact parsing logic (and exact configured
//     CUSTOM_STOCK_*_PATTERNS) Path C will use on every real run
//     afterward. A stronger, more meaningful check, but a genuine
//     behavioral divergence from Python's pass/fail criteria — not a
//     mechanical port. See evaluateField's doc comment for the resulting
//     reason-string vocabulary, which also does not literally match
//     Python's.
//
//  4. Only the plain "selector" (CSS) strategy is ever considered for
//     price_selector/stock_selector — a find_by_text-strategy proposal
//     for either field is treated as absent, exactly mirroring Python's
//     literal `selectors.get("price", {}).get("selector")` (which is
//     None for a find_by_text-shaped dict). This is a REAL LIMITATION
//     CARRIED FORWARD, not fixed here: price_selector/stock_selector are
//     single flat-text DB columns (see scraper.go's SelectorConfig doc
//     comment), and deciding how a richer multi-field SelectorConfig
//     would be serialized into that column is explicitly flagged there as
//     undecided, Orchestrator/Phase-3 territory — not something this file
//     should resolve unilaterally just because Scraper.Scrape happens to
//     support find_by_text natively.
//
//  5. TWICE REVISED. First attempt (after live-testing corrected
//     divergence #2): open a genuinely new top-level browser.NewSession
//     for the --commit validation scrape, mirroring crawler.go's
//     tryValidateCandidate — a new Chromium process, not the
//     caller-supplied session and not session.FreshContext, since a
//     forked context on the same process still shares that process's
//     cookie jar and browser-level fingerprint (TLS/JA3, WebGL/canvas),
//     which is what Cloudflare's detection actually keys on. This
//     WORKED (confirmed live against jumpbooks.lk) but kept two full
//     Chromium processes alive simultaneously for the duration of one
//     Run() call, purely to re-fetch a page whose content wasn't
//     expected to meaningfully change in the few seconds since the
//     initial fetch.
//
//     SUPERSEDED by the current design: validation no longer navigates
//     at all. scraper.go's doScrape was split into doScrape (fetch) +
//     the new scrapeDoc (pure extraction+parsing of an already-built
//     *goquery.Document, no browser involved) — see scrapeDoc's own doc
//     comment in scraper.go. Validation here parses rawHTML (the SAME
//     bytes already fetched in Step 2 below, for the LLM) directly via
//     d.scraper.scrapeDoc, with zero second navigation. This doesn't
//     just isolate the Cloudflare-block risk the way the fresh-session
//     attempt did — it removes the risk's precondition entirely (there
//     is no navigation #2 for Cloudflare to block), and removes the
//     two-processes-at-once memory cost along with it. Discoverer no
//     longer needs headless/timeout config at all as a result — it never
//     opens a browser session of its own, first attempt or otherwise.
//
//     THE HONEST TRADE-OFF, stated plainly rather than glossed over:
//     this makes --commit validation a SELF-CONSISTENCY check (does the
//     LLM's proposed selector resolve to a plausible value against the
//     exact HTML the LLM was shown), not an INDEPENDENT check the way
//     discover_selectors.py's validate_selectors() (a genuine second
//     live fetch) or this file's own first-attempt fresh-session design
//     both were. It will not catch every failure mode a true independent
//     re-fetch would — e.g. content that finishes async-rendering a
//     moment after the initial GetHTML snapshot was taken, or
//     server-side variance between two requests to the same URL. Judged
//     an acceptable trade for eliminating both the Cloudflare risk and
//     the memory cost, given price/stock text is not expected to shift
//     within the same handful of seconds — but this is a real behavioral
//     narrowing from Python's design intent, not a pure improvement, and
//     worth revisiting if a store is ever found where it matters.
package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/domain"
	"github.com/udanfernando2006/vestige-go/internal/llm"
	"github.com/udanfernando2006/vestige-go/internal/pipeline/heuristics"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

// discoveryLLMTimeout bounds llm.Client's HTTP timeout for selector
// discovery calls specifically. Genuinely Go-only — llm_extractor.py's
// openai-python client was never given an explicit timeout override (see
// client.go's own NewClient doc comment), so there is no Python value to
// port. Selector discovery sends a full HTML subtree plus a long tuned
// prompt (SELECTOR_USER_PROMPT in extractor.go) to a model that can be
// slow; 120s is a deliberately generous placeholder, not a measured
// value — the same "confirm before treating as settled" caveat already
// attached to scraper.go's defaultWaitSelectorTimeout.
const discoveryLLMTimeout = 120 * time.Second

// DiscoverOptions mirrors discover_selectors.py's CLI args, minus
// everything only the now-dropped --url/--store mode needed. PairID is
// required (not a pointer) — there is no "no pair" mode to distinguish
// against anymore. See the package doc comment's opening paragraph.
type DiscoverOptions struct {
	// PairID is the tracking pair to resolve a product URL from and
	// (if Commit) write discovered selectors back to. Required.
	PairID int64

	// Commit gates BOTH validation and the DB write, exactly mirroring
	// Python's `if args.commit:` guard, which wraps validate_selectors()
	// AND commit_to_db() in the same branch — there is no
	// validate-without-committing mode in Python, and this port
	// preserves that coupling rather than adding a new option. This also
	// preserves a known, documented gap rather than silently fixing it:
	// per vestige_api_implementation.md's history (API v3.3 entry),
	// "/discover never passes --commit, so the on-demand Discover
	// button's output is never validated against the live page — only
	// the pipeline's own --commit calls validate." The future on-demand
	// Discover button (Phase 3) is expected to keep calling Run with
	// Commit=false, and will keep getting unvalidated suggestions,
	// exactly as today.
	Commit bool

	// PrefetchedHTML, if non-nil, is used directly for Step 2 instead of
	// navigating — NEW, no Python equivalent, part of the bug 4b
	// redundant-navigation redesign (see the standalone implementation
	// plan document). When set, `session` is never touched by Run() and
	// may be nil. Exists for Orchestrator's Path A -> B handoff: Crawler
	// already fetched this exact page's HTML while confirming ProductURL
	// (CrawlResult.HTML), so Path B's discovery call can reuse it instead
	// of navigating to the same URL a second time on the same session —
	// which is precisely the navigation Cloudflare was found to block on
	// jumpbooks.lk (bug 4b). Existing callers (cmd/discovercheck) never
	// set this, so they hit the else branch below unchanged.
	PrefetchedHTML *string
}

// DiscoverResult mirrors discover_selectors.py's final JSON output shape
// (price_selector, stock_selector, price_sample, stock_sample,
// model_used, committed, reason) and vestige_api_implementation.md's
// DiscoverResultDto field list — the outgoing shape the future Phase 3
// handler will map this into for the API/Wails-binding boundary, not
// invented independently of it.
type DiscoverResult struct {
	PairID int64

	// PriceSelector / StockSelector are populated ONLY when that
	// specific field passed validation (Commit=true) or, when Commit is
	// false, whenever the LLM proposed a plain CSS selector for it at
	// all — mirroring Python's independent per-field reporting: a
	// price selector can be reported while a stock selector is nil (or
	// vice versa), and this is NOT the same as Committed being true,
	// which additionally requires BOTH.
	PriceSelector *string
	StockSelector *string

	// PriceSample / StockSample: the raw extracted text for whichever
	// field(s) passed. Set only when Commit=true and that field passed —
	// mirrors Python's validation.price_sample/stock_sample, which are
	// nil/absent outside a successful validation run.
	PriceSample *string
	StockSample *string

	ModelUsed string

	// Committed is true only when Commit was requested AND both
	// selectors independently passed validation AND the DB write
	// succeeded. False in every other case, including Commit=false
	// (unvalidated suggestion) and a partial validation failure.
	Committed bool

	// Reason is set only when Commit=true and Committed ended up false —
	// mirrors Python's `reason` key, which is entirely absent from the
	// non-commit result dict and from a successful commit. See
	// evaluateField's doc comment: the actual reason strings here do NOT
	// literally match Python's vocabulary, since the underlying
	// pass/fail criteria differs (divergence #3 in the package doc
	// comment).
	Reason *string

	// ScrapeResult is the full self-consistency scrape already computed
	// during --commit validation (the `scraped := d.scraper.scrapeDoc(...)`
	// call below) — NEW, no Python equivalent, part of the bug 4b
	// redundant-navigation redesign. Set only when Committed is true.
	// Lets a caller (Orchestrator's Path B) reuse this directly as the
	// pair's first snapshot instead of re-scraping the same page a second
	// time via a fresh Scraper.Scrape call on the same session — see the
	// standalone implementation plan document for the full reasoning.
	// Populated identically regardless of whether Step 2's rawHTML came
	// from PrefetchedHTML or a fresh navigation — scrapeDoc runs the same
	// either way; only rawHTML's source differs.
	ScrapeResult *domain.AvailabilityResult
}

// Discoverer replaces discover_selectors.py entirely. See the package doc
// comment (divergence #1) for why this holds *Scraper rather than a
// pre-built llm.Client. It holds no browser-session config of its own —
// see divergence #5's "SUPERSEDED" note: Run() never opens a browser
// session, so there is nothing here to configure for one.
type Discoverer struct {
	store   store.PairStore
	scraper *Scraper
}

// NewDiscoverer constructs a Discoverer. scraper is the same *Scraper the
// rest of the Orchestrator uses for real Path C/B scrapes — reusing it
// here (rather than constructing a throwaway Scraper with made-up
// parameters inside Run) means validation's extraction/parsing logic
// (via scraper.scrapeDoc — see package doc comment divergence #5) is
// identical to what production scraping uses, and keeps this file from
// having to invent Scraper construction parameters discover_selectors.py
// never needed (it never used the Scraper class at all — validation was
// bespoke BeautifulSoup, not a Scraper.scrape() call).
func NewDiscoverer(pairStore store.PairStore, scraper *Scraper) *Discoverer {
	return &Discoverer{store: pairStore, scraper: scraper}
}

// Run is a direct port of discover_selectors.py's `_run()`, minus
// load_target's --url branch (dropped, see package doc comment) and
// validate_selectors' bespoke checks (replaced by a real Scraper.Scrape
// call, see package doc comment divergence #3).
//
// session is required and used for the initial HTML fetch only. If
// Commit is true, validation does NOT navigate again at all — it parses
// the already-fetched rawHTML directly via scraper.scrapeDoc. See
// package doc comment divergence #5 for the full history (a fresh-session
// design was tried first, worked, and was then superseded by this
// simpler no-second-navigation approach) and divergence #2 for the
// original (wrong) reasoning this replaces.
func (d *Discoverer) Run(ctx context.Context, session browser.Session, opts DiscoverOptions) (DiscoverResult, error) {
	// --- Step 1: resolve the target (mirrors load_target's --pair-id branch) ---

	pair, err := d.store.GetPair(ctx, opts.PairID)
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("discoverer: failed to load pair %d: %w", opts.PairID, err)
	}
	if pair == nil {
		// Mirrors load_target's `if not pair: sys.exit(1)`.
		return DiscoverResult{}, fmt.Errorf("discoverer: no tracking pair found with id=%d", opts.PairID)
	}
	if pair.ProductURL == nil || strings.TrimSpace(*pair.ProductURL) == "" {
		// Mirrors load_target's `if not pair.get("product_url"): sys.exit(1)`.
		return DiscoverResult{}, fmt.Errorf(
			"discoverer: pair %d has no product_url — run the Crawler first, or set one manually",
			opts.PairID,
		)
	}
	productURL := *pair.ProductURL

	// Mirrors `title_context = target["book_name"] or "unknown"`. In
	// practice ActivePair.BookName is always populated (a real DB join,
	// unlike Python's --url-mode book_name=None case this port dropped),
	// so this fallback is defensive rather than load-bearing.
	titleContext := pair.BookName
	if strings.TrimSpace(titleContext) == "" {
		titleContext = "unknown"
	}

	// --- Settings / LLM config (mirrors _load_llm_config + its validation) ---

	settings, err := d.store.GetSettings(ctx)
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("discoverer: failed to load settings: %w", err)
	}
	if strings.TrimSpace(settings.SelectorAPIBase) == "" || strings.TrimSpace(settings.SelectorModel) == "" {
		// Mirrors: "Error: SELECTOR_API_BASE and SELECTOR_MODEL must be set."
		return DiscoverResult{}, fmt.Errorf(
			"discoverer: SELECTOR_API_BASE and SELECTOR_MODEL must both be configured",
		)
	}

	llmClient, err := llm.NewClient(settings.SelectorAPIBase, settings.SelectorAPIKey, discoveryLLMTimeout)
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("discoverer: failed to build LLM client: %w", err)
	}
	extractor, err := NewExtractor(ExtractorConfig{
		Engine:    EngineFull, // selector discovery always uses "full" — mirrors _load_llm_config's hardcoded "engine": "full"
		APIBase:   settings.SelectorAPIBase,
		APIKey:    settings.SelectorAPIKey,
		ModelName: settings.SelectorModel,
	}, llmClient)
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("discoverer: failed to construct extractor: %w", err)
	}

	// --- Step 2: fetch raw HTML (mirrors fetch_html()) ---
	//
	// No rate-limit wait here — matches Python exactly (fetch_html() is a
	// bare navigate+get_html with no Scraper involvement at all).
	//
	// NEW conditional (bug 4b redesign): if opts.PrefetchedHTML is set,
	// Run() uses it directly and never touches session at all — this is
	// what lets Orchestrator's Path A -> B handoff avoid a second
	// navigation on the same session. Existing callers never set this
	// field, so they always take the else branch, which is byte-for-byte
	// the prior unconditional behavior.
	var rawHTML string
	if opts.PrefetchedHTML != nil {
		rawHTML = *opts.PrefetchedHTML
	} else {
		if session == nil {
			return DiscoverResult{}, fmt.Errorf("discoverer: no session and no PrefetchedHTML for pair %d", opts.PairID)
		}
		if err := session.Navigate(ctx, productURL); err != nil {
			return DiscoverResult{}, fmt.Errorf("discoverer: failed to navigate to %s: %w", productURL, err)
		}
		fetched, err := session.GetHTML(ctx)
		if err != nil {
			return DiscoverResult{}, fmt.Errorf("discoverer: failed to fetch HTML: %w", err)
		}
		rawHTML = fetched
	}

	// --- Steps 3-5: clean, call the LLM, extract selectors ---

	cleanedHTML, err := extractor.CleanHTML(rawHTML)
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("discoverer: failed to clean HTML: %w", err)
	}

	selectors, err := extractor.ExtractSelectors(ctx, cleanedHTML, titleContext)
	if err != nil {
		// Mirrors `if "error" in selectors: print(...); return 1` — a
		// total LLM/dispatch failure is fatal here, same as Python.
		return DiscoverResult{}, fmt.Errorf("discoverer: LLM selector extraction failed: %w", err)
	}

	// Mirrors selectors.get("price", {}).get("selector") /
	// selectors.get("availability", {}).get("selector") — see package
	// doc comment divergence #4 for why this ONLY reads the plain
	// "selector" strategy.
	priceSelText := flatSelector(selectors["price"])
	stockSelText := flatSelector(selectors["availability"])

	// --- No --commit: report the raw suggestion, unvalidated ---

	if !opts.Commit {
		res := DiscoverResult{PairID: opts.PairID, ModelUsed: settings.SelectorModel}
		if priceSelText != "" {
			res.PriceSelector = &priceSelText
		}
		if stockSelText != "" {
			res.StockSelector = &stockSelText
		}
		return res, nil
	}

	// --- Step 6: --commit path — validate against the ALREADY-FETCHED HTML ---
	//
	// See package doc comment divergence #5 (twice-revised): this parses
	// rawHTML — the SAME bytes already fetched in Step 2 above for the
	// LLM — via scraper.go's scrapeDoc, instead of navigating a second
	// time. No browser session is opened here at all. This supersedes an
	// earlier version of this file that opened a brand-new top-level
	// browser.NewSession specifically to dodge jumpbooks.lk's Cloudflare
	// two-navigation block; reusing the already-fetched HTML sidesteps
	// that failure mode entirely (there is no second navigation for
	// Cloudflare to block) rather than merely isolating it, and removes
	// the two-Chromium-processes-alive-at-once memory cost that approach
	// carried. See scrapeDoc's own doc comment in scraper.go, and this
	// file's divergence #5 for the honest self-consistency-vs-independent-
	// verification trade-off this implies.
	//
	// The validation selectors below wrap exactly the flat strings we're
	// about to (maybe) commit — not the LLM's original, possibly richer
	// SelectorConfig — so validation checks precisely what gets written
	// to the DB, never something we'd then discard (see divergence #4).
	valSelectors := map[string]*SelectorConfig{
		"price":        {Selector: &priceSelText},
		"availability": {Selector: &stockSelText},
	}

	// NEW: scope validation to the same "product content" view the LLM
	// was shown, via heuristics.ScopeToMainContent, instead of parsing
	// rawHTML unscoped. Was: `doc, err := goquery.NewDocumentFromReader(
	// strings.NewReader(rawHTML))`. See ScopeToMainContent's doc comment
	// for the full "why" — a selector genuinely unambiguous against the
	// scoped excerpt the LLM saw could previously still resolve to the
	// wrong element once matched against the full unscoped page here
	// (live-confirmed: a jumpbooks.lk page with 14 total matches for one
	// class across a header mini-cart widget, a "Recently Viewed" aside,
	// a "Related Products" carousel, and the one real product price).
	scope, err := heuristics.ScopeToMainContent(rawHTML)
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("discoverer: failed to parse fetched HTML for validation: %w", err)
	}

	// Passing the real configured custom stock patterns means validation
	// exercises the exact same ClassifyStockText path a real Path C
	// scrape of this pair will use afterward — no Python equivalent
	// existed here since validate_selectors() never consulted custom
	// patterns at all.
	customIn := strings.Join(settings.CustomStockInPatterns, ",")
	customOut := strings.Join(settings.CustomStockOutPatterns, ",")

	scraped := d.scraper.scrapeDoc(scope, valSelectors, customIn, customOut)

	// NEW: mechanical ambiguity check, cheap and deterministic — counts
	// how many elements each selector actually matches against the SAME
	// scoped view scrapeDoc just used, independent of whatever single
	// element scrapeDoc/goquery's .First() happened to pick. Catches the
	// "unique-per-excerpt but the excerpt still has 2 legitimate
	// candidates" case (e.g. a WooCommerce sale product's <del>/<ins>
	// original+sale price, both carrying the identical price class,
	// both genuinely inside the real product's own content — not noise
	// ScopeToMainContent would ever strip) that scoping alone doesn't
	// resolve, without spending an extra LLM call to catch it. An empty
	// selector string is never counted (evaluateField's own
	// "_selector_missing" check already covers that case distinctly).
	priceMatchCount := 0
	if priceSelText != "" {
		priceMatchCount = scope.Find(priceSelText).Length()
	}
	stockMatchCount := 0
	if stockSelText != "" {
		stockMatchCount = scope.Find(stockSelText).Length()
	}

	pricePassed, priceReason := evaluateField("price", priceSelText, scraped.RawPriceText, scraped.Price != nil, priceMatchCount)
	stockPassed, stockReason := evaluateField("stock", stockSelText, scraped.RawStockText, scraped.InStock != nil, stockMatchCount)

	res := DiscoverResult{PairID: opts.PairID, ModelUsed: settings.SelectorModel}
	if pricePassed {
		res.PriceSelector = &priceSelText
		res.PriceSample = scraped.RawPriceText
	}
	if stockPassed {
		res.StockSelector = &stockSelText
		res.StockSample = scraped.RawStockText
	}
	res.Committed = pricePassed && stockPassed
	if res.Committed {
		// NEW (bug 4b redesign) — see ScrapeResult's doc comment. scraped
		// is already a local variable at this point (the same one used
		// for the self-consistency check above); this is purely "stop
		// discarding it."
		res.ScrapeResult = scraped
	}

	if !res.Committed {
		// Mirrors: `if not (price_passed and stock_passed): ... return 1`
		// — a partial or total validation failure is a legitimate
		// STRUCTURED result, not a Go error. The future Path B caller is
		// expected to branch on Committed==false and fall back to a
		// one-off Path D extraction for that run, exactly as
		// vestige_guide.md documents ("on failure, falls back to a
		// one-off Path D extraction... rather than going straight to
		// NEEDS_SETUP").
		var reasons []string
		if !pricePassed {
			reasons = append(reasons, priceReason)
		}
		if !stockPassed {
			reasons = append(reasons, stockReason)
		}
		reason := strings.Join(reasons, ", ")
		res.Reason = &reason
		return res, nil
	}

	if err := d.store.UpdatePairSelectors(ctx, opts.PairID, priceSelText, stockSelText); err != nil {
		// Python's db.update_pair_selectors() call is likewise
		// unguarded — a write failure here is fatal, matching that.
		return DiscoverResult{}, fmt.Errorf("discoverer: failed to commit selectors for pair %d: %w", opts.PairID, err)
	}
	return res, nil
}

// flatSelector extracts only the plain CSS "selector" strategy from a
// SelectorConfig — see package doc comment divergence #4 for the full
// reasoning on why a find_by_text-strategy config is treated as absent
// here rather than being supported just because Scraper.Scrape happens to
// handle it natively.
func flatSelector(cfg *SelectorConfig) string {
	if cfg == nil || cfg.Selector == nil {
		return ""
	}
	return strings.TrimSpace(*cfg.Selector)
}

// evaluateField determines pass/fail for one field's validation result
// against the real Scraper.Scrape output, and a reason string for the
// failure case.
//
// REASON VOCABULARY DOES NOT MATCH PYTHON — this is expected, not an
// oversight. validate_selectors() classified failure by shallow raw-text
// heuristics ("no digit found", "no stock keyword found"); this classifies
// failure by whether the SAME parsing logic Path C uses in production
// (ParsePrice / ClassifyStockText, via Scraper.doScrape) actually resolved
// a value. Three failure buckets, checked in order:
//   - "<field>_selector_missing" — no plain CSS selector was available to
//     try at all (empty string; covers both "LLM proposed nothing" and
//     "LLM proposed a find_by_text strategy," per flatSelector).
//   - "<field>_selector_no_match" — a selector was tried but no element
//     matched, or the matched element's text was empty after cleanup
//     (Scraper.extractField collapses both cases to the same observable
//     signal: rawText nil or "").
//   - "<field>_selector_unparseable" — an element matched and had
//     non-empty text, but ParsePrice/ClassifyStockText still couldn't
//     make sense of it (e.g. a selector that grabbed the wrong node's
//     text — the exact real failure mode documented in this project's own
//     notes on sarasavi.lk's price-anchor-drift and Shopify's hidden
//     sold-out badge).
//
// evaluateField mirrors discover_selectors.py's validate_selectors()
// per-field pass/fail logic, with one addition: matchCount (NEW, no
// Python equivalent — see the standalone discussion this arc on
// selector ambiguity). A selector matching more than one element in the
// scoped view is rejected before its (arbitrary, whichever-scrapeDoc's-
// .First()-happened-to-pick) extracted value is even considered —
// "parsed successfully" is not the same as "parsed the right element."
// matchCount==0 or ==1 both fall through to the existing checks
// unchanged (0 naturally still fails via the no-match check below,
// since a selector matching nothing extracts no raw text either).
func evaluateField(name, sel string, rawText *string, parsedOK bool, matchCount int) (passed bool, reason string) {
	if sel == "" {
		return false, name + "_selector_missing"
	}
	if matchCount > 1 {
		return false, name + "_selector_ambiguous"
	}
	if rawText == nil || *rawText == "" {
		return false, name + "_selector_no_match"
	}
	if !parsedOK {
		return false, name + "_selector_unparseable"
	}
	return true, ""
}
