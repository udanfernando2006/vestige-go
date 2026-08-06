package pipeline

// NEW DIAGNOSTIC PRINTS (this file, plus scraper.go): none of the
// fmt.Fprintf(os.Stderr, "[Crawler] ...") lines below exist in crawler.py
// — they were added directly to this Go port for live debugging
// visibility, matching extractor.go's existing os.Stderr convention for
// diagnostics rather than orchestrator.go's stdout convention.
// orchestrator.go's prints stay split: EXISTING lines there are verified
// print()-for-print() parity with orchestrator.py (stdout, since
// orchestrator.py's own print() calls carry no file=sys.stderr kwarg);
// any NEW diagnostic lines added to orchestrator.go alongside these use
// os.Stderr too, for the same "new instrumentation, not a Python port"
// reason. Since orchestratorcheck runs the whole pipeline in-process
// (no subprocess), every one of these lines surfaces directly in its
// terminal output with no extra plumbing required.

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"

	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/domain"
	"github.com/udanfernando2006/vestige-go/internal/pipeline/heuristics"
)

// BuildSearchURL is a direct port of Crawler._build_search_url().
//
// Replaces the literal "=test" placeholder (left by the store's cached
// search_url_template, or by the discovery-phase "test" probe query) with
// the real query, URL-encoded.
//
// Python's str.replace(old, new) with no count argument replaces ALL
// occurrences, not just the first — strings.ReplaceAll (not
// strings.Replace with n=1) is used here to match that.
//
// query is encoded via net/url.QueryEscape, which — like Python's
// urllib.parse.quote_plus — encodes spaces as "+". The two encoders should
// produce identical output for the query strings this project actually
// sends (titles, ISBNs), but they haven't been diffed character-for-
// character against quote_plus's exact reserved-character set, so this is
// a reasonable-confidence port, not a verified-identical one.
func BuildSearchURL(baseURL, query string) string {
	return strings.ReplaceAll(baseURL, "=test", "="+url.QueryEscape(query))
}

// validationDepth mirrors Crawler._validate_candidates()'s
// `depth: int = 3` default parameter — every real call site in
// crawler.py (both from _run_with_session and _run_discovery) calls
// _validate_candidates with no explicit depth override, so this constant
// is the one effective value ever used in practice, not an invented one.
const validationDepth = 3

// CrawlResult is the Go shape for the several differently-keyed dict
// literals crawler.py's find_product_url()/_run_with_session()/
// _run_discovery()/_validate_candidates() return across their various
// success/failure branches:
//
//	{"success": False, "product_url": None, "status": "NOT_LISTED"}
//	{"success": False, "error": "No search form found"}
//	{"success": True, "product_url": ..., "confidence": ...}
//	  (+ "search_url_template" mutated in afterward on success, by the caller)
//
// Every field below corresponds to a key that actually appears in one of
// those literals — nothing here is invented beyond what the Python source
// shows.
type CrawlResult struct {
	Success           bool
	ProductURL        *string
	Status            *string // e.g. "NOT_LISTED" — only set on a NOT_LISTED failure
	Error             *string // e.g. "No search form found" — only set on the discovery-phase failure
	Confidence        *float64
	SearchURLTemplate *string // only set on success, mirroring the post-hoc `result["search_url_template"] = ...` mutation

	// HTML is the page HTML Crawler already fetched at the moment it
	// confirmed ProductURL, when available — NEW, no Python equivalent.
	// Populated by both checkForSingleResultRedirect (the redirect-landed
	// page itself) and tryValidateCandidate (the validated candidate's
	// page). Nil for NOT_LISTED / discovery-phase failures, where Crawler
	// never actually saw a page worth keeping. Exists so a caller
	// (Orchestrator's Path A -> B/D handoff) can reuse this HTML instead
	// of re-navigating to ProductURL a second time — see the Path B
	// redundant-navigation redesign plan (bug 4b) for the full reasoning.
	HTML *string
}

// Crawler discovers a product page URL for a book on a given store.
// Direct port of crawler.py's Crawler class.
type Crawler struct {
	headless bool
	timeout  time.Duration
}

// NewCrawler constructs a Crawler. Mirrors Crawler.__init__'s two config
// fields (headless, timeout) — Python's defaults (headless=True,
// timeout=60000ms) are the caller's responsibility to supply in Go, same
// convention as NewScraper.
func NewCrawler(headless bool, timeout time.Duration) *Crawler {
	return &Crawler{headless: headless, timeout: timeout}
}

// FindProductURL is a direct port of Crawler.find_product_url().
//
// store's fields stand in for Python's `urls: dict` parameter
// (urls["base_url"], urls["search_url_template"]) — domain.Store's
// BaseURL/SearchURLTemplate fields already match those two keys exactly
// by name and nullability, so a typed struct is used here instead of a
// generic map.
//
// isbn follows the same "" = absent convention used throughout this port
// (see ParseStockStatus/ScoreCandidates) rather than a nullable *string —
// every real use of isbn in this file (`isbn if isbn else title`,
// `if isbn and isbn in href_lower`) treats Python's None and "" as
// behaviorally identical (both falsy), so there is no real distinction to
// preserve.
func (c *Crawler) FindProductURL(ctx context.Context, store *domain.Store, title, isbn string, sess browser.Session) (*CrawlResult, error) {
	if sess != nil && store.SearchURLTemplate != nil && *store.SearchURLTemplate != "" {
		// NEW diagnostic, not a Python port — see file header note on the
		// stdout/stderr split this project now follows.
		fmt.Fprintf(os.Stderr, "[Crawler] %s: using cached search_url_template %q\n", store.Name, *store.SearchURLTemplate)
		return c.runWithSession(ctx, sess, store, title, isbn)
	}
	fmt.Fprintf(os.Stderr, "[Crawler] %s: no cached search_url_template — running fresh two-session discovery\n", store.Name)
	return c.runDiscovery(ctx, store.BaseURL, title, isbn)
}

// checkForSingleResultRedirect is NEW logic with no equivalent in
// crawler.py — flagged explicitly as an intentional divergence, not a
// port. It exists because live testing found that some stores (confirmed:
// jumpbooks.lk) redirect an exact-match search query (specifically:
// searching by ISBN) straight to the single matching product's own page,
// rather than showing a search-results listing — while others show a
// genuine listing even for an ISBN query. Neither _run_with_session nor
// _run_discovery in the Python source accounts for this at all; without
// this check, a redirect's resulting product page gets fed into
// extractCandidateLinks/ScoreCandidates as if it were a listing, meaning
// the "candidates" scored are actually links WITHIN that page (its own
// add-to-cart button, its related-products widget, nav links) rather than
// distinct search results — a real bug found via live testing against
// jumpbooks.lk, where an unrelated related-product's add-to-cart link
// occasionally outscored the genuine candidates.
//
// Detection: compare the URL actually navigated to (post-redirect, if
// any) against the URL we intended to navigate to. If they match, no
// redirect happened — return (nil, false), and the caller proceeds with
// normal candidate extraction exactly as before.
//
// If they differ, this does NOT blindly trust that the redirect landed on
// the correct product — it runs the same heuristics.ScoreValidation used
// for real candidates against the current page. Only if that validates
// (title/ISBN match, or enough availability/content signal per
// ScoreValidation's existing threshold) does this return a success
// result directly. If the redirected-to page does NOT validate — a
// redirect to a category/listing page, or a "closest match" redirect that
// picked the wrong product — this returns (nil, false) too, so the caller
// falls through to normal candidate extraction against the SAME html
// already fetched (no extra navigation), exactly as if no redirect had
// been detected. This fallback is a deliberate safety net beyond the
// original proposal: trusting a redirect unconditionally would risk a
// false positive on some store this hasn't been tested against.
func (c *Crawler) checkForSingleResultRedirect(intendedURL, actualURL, htmlStr, title, isbn string) (*CrawlResult, bool) {
	if actualURL == intendedURL {
		return nil, false
	}

	validation, err := heuristics.ScoreValidation(htmlStr, actualURL, title, isbn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Crawler] checkForSingleResultRedirect: validation error for %s: %v (fetched %d bytes of HTML)\n", actualURL, err, len(htmlStr))
		return nil, false
	}
	if !validation.Valid {
		fmt.Fprintf(os.Stderr, "[Crawler] checkForSingleResultRedirect: %s REJECTED (validationScore=%d, findings=%+v, fetched %d bytes of HTML)\n",
			actualURL, validation.ValidationScore, validation.Findings, len(htmlStr))
		return nil, false
	}
	if !passesIdentityGate(validation) {
		fmt.Fprintf(os.Stderr, "[Crawler] checkForSingleResultRedirect: %s REJECTED (validationScore=%d cleared the >=3 threshold but neither title nor ISBN matched — identity gate, findings=%+v, fetched %d bytes of HTML)\n",
			actualURL, validation.ValidationScore, validation.Findings, len(htmlStr))
		return nil, false
	}

	confidence := math.Round(float64(validation.ValidationScore)/9*100) / 100
	productURL := actualURL
	// html captures htmlStr (this function's parameter) for CrawlResult.HTML
	// — NEW, see that field's doc comment (bug 4b redesign). A local copy
	// is taken (not &htmlStr directly) purely so the field holds its own
	// string value rather than aliasing the parameter, matching the same
	// pattern already used for productURL/confidence above.
	html := htmlStr
	return &CrawlResult{Success: true, ProductURL: &productURL, Confidence: &confidence, HTML: &html}, true
}

// runWithSession is a direct port of Crawler._run_with_session(), plus
// the checkForSingleResultRedirect shortcut (see that function's doc
// comment — NOT present in the Python source).
func (c *Crawler) runWithSession(ctx context.Context, sess browser.Session, store *domain.Store, title, isbn string) (*CrawlResult, error) {
	query := isbn
	if query == "" {
		query = title
	}

	searchURLTemplate := ""
	if store.SearchURLTemplate != nil {
		searchURLTemplate = *store.SearchURLTemplate
	}
	searchURL := BuildSearchURL(searchURLTemplate, query)
	fmt.Fprintf(os.Stderr, "[Crawler] runWithSession: query=%q searchURL=%s\n", query, searchURL)

	if err := sess.Navigate(ctx, searchURL); err != nil {
		return nil, err
	}

	actualURL, err := sess.GetURL(ctx)
	if err != nil {
		return nil, err
	}
	if actualURL != searchURL {
		fmt.Fprintf(os.Stderr, "[Crawler] runWithSession: redirected -> %s\n", actualURL)
	}

	htmlStr, err := sess.GetHTML(ctx)
	if err != nil {
		return nil, err
	}

	if result, redirected := c.checkForSingleResultRedirect(searchURL, actualURL, htmlStr, title, isbn); redirected {
		fmt.Fprintf(os.Stderr, "[Crawler] runWithSession: single-result redirect validated -> %s (confidence=%.2f)\n", *result.ProductURL, *result.Confidence)
		result.SearchURLTemplate = &searchURLTemplate
		return result, nil
	}

	candidates := extractCandidateLinks(htmlStr)
	scored := heuristics.ScoreCandidates(candidates, isbn, title, store.BaseURL)
	fmt.Fprintf(os.Stderr, "[Crawler] runWithSession: %d links extracted, %d survived scoring\n", len(candidates), len(scored))
	logTopCandidates(scored)
	if len(scored) == 0 {
		fmt.Fprintf(os.Stderr, "[Crawler] runWithSession: zero candidates -> NOT_LISTED\n")
		status := "NOT_LISTED"
		return &CrawlResult{Success: false, Status: &status}, nil
	}

	result := c.validateCandidates(ctx, sess, scored, title, isbn)
	if result.Success {
		result.SearchURLTemplate = &searchURLTemplate
	}
	return result, nil
}

// runDiscovery is a direct port of Crawler._run_discovery().
//
// Preserves the two-sequential-isolated-browser-session design exactly:
// discoverySess (search-pattern discovery) is fully closed before
// searchSess (the actual search) is ever opened — this ordering, not just
// "two sessions exist somewhere," is the Cloudflare-avoidance mechanism
// per vestige_go_migration_blueprint.md Section 4, so it's implemented
// with explicit Close() calls at every exit point rather than a single
// deferred Close that could outlive the intended scope.
//
// FLAGGED BEHAVIORAL GAP: Python calls
// `discovery_session.navigate(base_url, wait_until="domcontentloaded")` —
// a non-default wait_until value. session.go's Navigate has no
// wait_until parameter at all; its own doc comment states this was
// because "no callers in the ported codebase use a non-default value" —
// but this call site, now that crawler.py is real source, contradicts
// that assumption. This port falls back to plain Navigate(ctx, baseURL)
// (session.go's hardcoded Navigate+WaitVisible("body")), which is a real
// behavioral gap, not a cosmetic one: "domcontentloaded" is a lighter
// wait condition than whatever session.go's WaitVisible("body") approach
// resolves to, and Python's choice to use it specifically for a store's
// homepage (as opposed to the second, unqualified navigate() call to the
// actual search results page later in this same function) suggests it
// mattered for slow-loading or JS-heavy store homepages. Needs a decision
// — either extend Session.Navigate to accept a wait strategy, or confirm
// this gap is acceptable — before treating discovery as fully verified.
func (c *Crawler) runDiscovery(ctx context.Context, baseURL, title, isbn string) (*CrawlResult, error) {
	fmt.Fprintf(os.Stderr, "[Crawler] runDiscovery: opening discovery session, navigating to base_url=%s\n", baseURL)
	discoverySess, err := browser.NewSession(ctx, c.headless, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("crawler: failed to open discovery session: %w", err)
	}

	if err := discoverySess.Navigate(ctx, baseURL); err != nil {
		discoverySess.Close(ctx)
		return nil, err
	}

	filled, err := discoverySess.FindAndFillSearch(ctx, "test")
	if err != nil {
		discoverySess.Close(ctx)
		return nil, err
	}
	if !filled {
		discoverySess.Close(ctx)
		fmt.Fprintf(os.Stderr, "[Crawler] runDiscovery: no search form found on %s\n", baseURL)
		errMsg := "No search form found"
		return &CrawlResult{Success: false, Error: &errMsg}, nil
	}

	searchURLTemplate, err := discoverySess.GetURL(ctx)
	discoverySess.Close(ctx) // discovery_session's `async with` scope ends here, before search_session opens
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "[Crawler] runDiscovery: discovered search_url_template=%q\n", searchURLTemplate)

	searchSess, err := browser.NewSession(ctx, c.headless, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("crawler: failed to open search session: %w", err)
	}

	query := isbn
	if query == "" {
		query = title
	}
	actualSearchURL := BuildSearchURL(searchURLTemplate, query)
	fmt.Fprintf(os.Stderr, "[Crawler] runDiscovery: query=%q actualSearchURL=%s\n", query, actualSearchURL)

	if err := searchSess.Navigate(ctx, actualSearchURL); err != nil {
		searchSess.Close(ctx)
		return nil, err
	}

	actualURL, err := searchSess.GetURL(ctx)
	if err != nil {
		searchSess.Close(ctx)
		return nil, err
	}
	if actualURL != actualSearchURL {
		fmt.Fprintf(os.Stderr, "[Crawler] runDiscovery: redirected -> %s\n", actualURL)
	}

	htmlStr, err := searchSess.GetHTML(ctx)
	if err != nil {
		searchSess.Close(ctx)
		return nil, err
	}

	// searchSess's `async with`-equivalent scope ends here — a full Close
	// (browser process and all), matching discoverySess's existing
	// early-close pattern above. Safe to fully close (not just CloseTarget)
	// because tryValidateCandidate no longer forks tabs off this session's
	// allocator — each candidate now opens its own genuinely independent
	// top-level session (see tryValidateCandidate's doc comment for why
	// FreshContext/same-process tabs turned out to be insufficient for
	// Cloudflare-avoidance). searchSess is still passed into
	// validateCandidates below purely for signature stability; it is
	// already closed and unused by the time any candidate is checked.
	searchSess.Close(ctx)

	if result, redirected := c.checkForSingleResultRedirect(actualSearchURL, actualURL, htmlStr, title, isbn); redirected {
		fmt.Fprintf(os.Stderr, "[Crawler] runDiscovery: single-result redirect validated -> %s (confidence=%.2f)\n", *result.ProductURL, *result.Confidence)
		template := searchURLTemplate
		result.SearchURLTemplate = &template
		return result, nil
	}

	candidates := extractCandidateLinks(htmlStr)
	scored := heuristics.ScoreCandidates(candidates, isbn, title, baseURL)
	fmt.Fprintf(os.Stderr, "[Crawler] runDiscovery: %d links extracted, %d survived scoring\n", len(candidates), len(scored))
	logTopCandidates(scored)
	if len(scored) == 0 {
		fmt.Fprintf(os.Stderr, "[Crawler] runDiscovery: zero candidates -> NOT_LISTED\n")
		status := "NOT_LISTED"
		return &CrawlResult{Success: false, Status: &status}, nil
	}

	result := c.validateCandidates(ctx, searchSess, scored, title, isbn)
	if result.Success {
		template := searchURLTemplate
		result.SearchURLTemplate = &template
	}
	return result, nil
}

// logTopCandidates is a NEW diagnostic helper (no Python equivalent) —
// prints up to validationDepth candidates in score order so a run's
// terminal output shows exactly what validateCandidates is about to try,
// before any of them are actually fetched/validated. This is precisely
// the visibility that would have shown a false-positive candidate (e.g.
// a category page scoring above zero purely by keyword-substring
// accident) BEFORE it got treated as the winning result.
func logTopCandidates(scored []heuristics.ScoredCandidate) {
	limit := validationDepth
	if limit > len(scored) {
		limit = len(scored)
	}
	for i := 0; i < limit; i++ {
		c := scored[i]
		fmt.Fprintf(os.Stderr, "[Crawler]   candidate #%d: score=%d url=%s text=%q\n", i+1, c.MatchScore, c.URL, c.Text)
	}
}

// validateCandidates is a direct port of Crawler._validate_candidates(),
// PLUS one intentional divergence not present in the Python source: each
// candidate is now checked via its own brand-new top-level browser session
// (a genuinely new Chromium process, see tryValidateCandidate) instead of
// the shared sess passed in.
//
// Why this changed: live testing against jumpbooks.lk found that within
// one shared session, the FIRST navigation always succeeds and the
// SECOND (and any subsequent) navigation gets Cloudflare-blocked —
// regardless of what that second navigation actually is (a second search
// query, or the first candidate check after a search). An initial fix
// attempted per-candidate isolation via session.FreshContext (a new tab on
// the SAME process) — this turned out to be insufficient: tabs opened via
// FreshContext share their parent process's cookie jar and browser-level
// fingerprint, both of which Cloudflare's detection keys on, so a
// same-process "fresh tab" carried the same reputation as the tab before
// it. The actual fix is a full new browser.NewSession per candidate — a
// genuinely new process — matching the isolation runDiscovery's two-
// session split already relies on for the identical reason.
//
// Still mirrors Python's `except Exception: pass` per-candidate: a
// NewSession/Navigate/GetHTML/ScoreValidation failure on one candidate
// is silently skipped, trying the next candidate rather than aborting the
// whole function — so this never returns a Go error, matching the Python
// function's own "never raises" behavior. NOTE: this still cannot
// distinguish "candidate genuinely doesn't match" from "candidate was
// blocked" — both look identical to ScoreValidation. If per-candidate
// process isolation turns out not to fully resolve blocking on some
// store, that distinction is the next thing worth building (to stop
// cycling early and surface a distinct "found a good candidate, couldn't
// verify it" result rather than silently falling through to NOT_LISTED).
func (c *Crawler) validateCandidates(ctx context.Context, sess browser.Session, scored []heuristics.ScoredCandidate, title, isbn string) *CrawlResult {
	limit := validationDepth
	if limit > len(scored) {
		limit = len(scored)
	}

	for _, candidate := range scored[:limit] {
		if result, ok := c.tryValidateCandidate(ctx, sess, candidate, title, isbn); ok {
			return result
		}
	}

	status := "NOT_LISTED"
	return &CrawlResult{Success: false, Status: &status}
}

// tryValidateCandidate checks ONE candidate via a brand-new top-level
// browser session (a genuinely new Chromium process via browser.NewSession)
// — NOT sess.FreshContext. This is a deliberate correction from an earlier
// version of this fix, which used FreshContext (a new CDP target/tab
// forked off sess's SAME allocator/process). That turned out to be
// insufficient for Cloudflare-avoidance purposes: tabs opened via
// FreshContext on one Chromium process share that process's cookie
// jar/profile (any cf_clearance token or reputation the process already
// picked up carries straight over) and, more fundamentally, share the
// same browser-level fingerprint (TLS/JA3, WebGL/canvas, etc.) Cloudflare
// actually keys much of its detection on — "isolated session," as used
// throughout this codebase's own design docs (runDiscovery's two-session
// split, the migration blueprint's Section 4 parity checklist), has always
// meant a new process, not a new tab. A closed sess is never touched here
// — sess is retained as a parameter only for signature stability with
// validateCandidates/existing callers; it is otherwise unused now that
// each candidate gets its own fully independent session.
func (c *Crawler) tryValidateCandidate(ctx context.Context, sess browser.Session, candidate heuristics.ScoredCandidate, title, isbn string) (*CrawlResult, bool) {
	fmt.Fprintf(os.Stderr, "[Crawler] validating candidate (score=%d): %s\n", candidate.MatchScore, candidate.URL)

	fresh, err := browser.NewSession(ctx, c.headless, c.timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Crawler]   -> could not open isolated session: %v\n", err)
		return nil, false
	}
	defer fresh.Close(ctx)

	if err := fresh.Navigate(ctx, candidate.URL); err != nil {
		fmt.Fprintf(os.Stderr, "[Crawler]   -> navigate failed: %v\n", err)
		return nil, false
	}

	htmlStr, err := fresh.GetHTML(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Crawler]   -> fetch failed: %v\n", err)
		return nil, false
	}

	validation, err := heuristics.ScoreValidation(htmlStr, candidate.URL, title, isbn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Crawler]   -> validation error: %v\n", err)
		return nil, false
	}
	if !validation.Valid {
		fmt.Fprintf(os.Stderr, "[Crawler]   -> REJECTED (validationScore=%d, findings=%+v)\n", validation.ValidationScore, validation.Findings)
		return nil, false
	}
	if !passesIdentityGate(validation) {
		fmt.Fprintf(os.Stderr, "[Crawler]   -> REJECTED (validationScore=%d cleared the >=3 threshold but neither title nor ISBN matched — identity gate, findings=%+v)\n", validation.ValidationScore, validation.Findings)
		return nil, false
	}
	fmt.Fprintf(os.Stderr, "[Crawler]   -> ACCEPTED (validationScore=%d, findings=%+v)\n", validation.ValidationScore, validation.Findings)

	// round(validation_score / 9, 2) — Python 3's round() uses
	// round-half-to-even; math.Round below is round-half-away-from-zero.
	// These can disagree on an exact tie at the 3rd decimal place, which
	// validation_score/9 (validation_score being a small non-negative
	// int, 0-9ish per ScoreValidation's point values) could in principle
	// hit — flagged as a minor, likely-inconsequential rounding-mode
	// discrepancy rather than a silently-assumed-identical one.
	confidence := math.Round(float64(validation.ValidationScore)/9*100) / 100
	productURL := candidate.URL
	// html captures htmlStr (this function's local, already fetched via
	// fresh.GetHTML above) for CrawlResult.HTML — NEW, see that field's
	// doc comment (bug 4b redesign).
	html := htmlStr
	return &CrawlResult{Success: true, ProductURL: &productURL, Confidence: &confidence, HTML: &html}, true
}

// passesIdentityGate is an additional hard gate applied AFTER
// heuristics.ScoreValidation's own Valid check (score >= 3), not a
// replacement for it — ScoreValidation itself is deliberately left
// untouched here, since its existing threshold is already calibrated
// against jumpbooks.lk's real pages and changing it risks breaking that
// working calibration for every other caller of ScoreValidation.
//
// The gap this closes: ScoreValidation.Valid can clear its >=3 threshold
// from HasAvailability (+2) and HasContent (+2) alone — four points, with
// TitleFound and ISBNFound both false. A page can look "valid" by that
// measure while having zero confirmation it's actually the book being
// searched for. Confirmed live: a broken search-template cache (a
// separate, since-fixed bug) once caused a completely unrelated book's
// page to score just high enough to pass, and it was accepted and
// committed as if it were the intended book — wrong price/stock data
// written under the right pair, with nothing in the output looking wrong
// unless the real page was already known.
//
// Requiring TitleFound || ISBNFound closes that gap without touching
// ScoreValidation's scoring/threshold itself: a genuine match for the
// intended book overwhelmingly has its own title or ISBN somewhere on its
// own product page, so this should not affect the already-working
// acceptance path — only the case where nothing on the page actually ties
// it to the book being searched for.
//
// Applied at both of ScoreValidation's real call sites in this file
// (checkForSingleResultRedirect and tryValidateCandidate) rather than
// only the one where the wrong-data incident was first observed — both
// accept a page as "the product" based on the same Valid flag, so both
// carry the identical risk.
func passesIdentityGate(v heuristics.ValidationResult) bool {
	return v.Findings.TitleFound || v.Findings.ISBNFound
}

// extractCandidateLinks is a direct port of
// Crawler._extract_candidate_links(). Returns every <a href="..."> on the
// page as a heuristics.LinkCandidate, ready for heuristics.ScoreCandidates
// — URL resolution (Python's `url: None # populated by urljoin later`)
// happens inside ScoreCandidates itself, not here, matching the original
// split between extraction and scoring.
func extractCandidateLinks(htmlStr string) []heuristics.LinkCandidate {
	if htmlStr == "" {
		return nil
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return nil
	}

	var candidates []heuristics.LinkCandidate
	doc.Find("a[href]").Each(func(_ int, sel *goquery.Selection) {
		href, exists := sel.Attr("href")
		if !exists || len(sel.Nodes) == 0 {
			return
		}
		text := bs4GetTextStrip(sel.Nodes[0])
		candidates = append(candidates, heuristics.LinkCandidate{Href: href, Text: text})
	})

	return candidates
}

// bs4GetTextStrip approximates BeautifulSoup's `element.get_text(strip=True)`
// — used for `link.get_text(strip=True)` in _extract_candidate_links().
// bs4's strip=True strips each individual descendant text node before
// joining them (default separator ""), which is subtly different from
// stripping the final concatenated string as a whole — a link like
// "<a> Foo <b> Bar </b></a>" becomes "FooBar" under get_text(strip=True)
// (each piece stripped, then joined with no separator), not "Foo  Bar"
// or "Foo Bar". This walks the node tree replicating that per-node
// stripping. FLAGGED AS AN APPROXIMATION like findTagWithText in
// scraper.go — correct for the common case (simple single-text-run
// anchors), not verified character-for-character against bs4's own
// source.
func bs4GetTextStrip(n *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			if t := strings.TrimSpace(node.Data); t != "" {
				parts = append(parts, t)
			}
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(parts, "")
}
