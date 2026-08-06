// Package pipeline holds the Orchestrator/Crawler/Scraper/Extractor/
// Discoverer control-flow code — the browser/DB-touching layer that calls
// into the pure functions in pipeline/heuristics. See
// vestige_go_pipeline_implementation.md Section 1 for the full package
// layout this file is part of.
//
// Source-verified against scraper.py in full, plus domain.AvailabilityResult
// (result.go, direct port of models/result.py) and browser.Session
// (session.go, chromedp-backed, live-verified — see
// vestige_go_pipeline_implementation.md Section 3).
//
// NEW DIAGNOSTIC PRINTS: the fmt.Fprintf(os.Stderr, "[Scraper] ...")
// lines in doScrape/scrapeDoc below have no scraper.py equivalent —
// added directly to this Go port for live debugging visibility, matching
// crawler.go's/extractor.go's os.Stderr convention. See crawler.go's file
// header for the full stdout/stderr split rationale.
package pipeline

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"

	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/domain"
	"github.com/udanfernando2006/vestige-go/internal/pipeline/heuristics"
)

var (
	currencyPrefixPattern = regexp.MustCompile(`(?i)(LKR|Rs\.?|USD|\$|€|£)`)
	priceNumberPattern    = regexp.MustCompile(`[\d,]+\.?\d*`)
)

// ParsePrice is a direct port of Scraper.parse_price().
//
// Strips common currency prefixes (LKR, Rs., USD, $, €, £ — case
// insensitive), then extracts the first numeric substring (handling both
// "1,500.00" and "1500" comma-grouping styles) and parses it as a float.
//
// Returns nil for an empty input, a non-matching input, or a match that
// fails to parse as a float — mirroring Python's three separate `return
// None` paths (empty check, no regex match, ValueError on float()).
func ParsePrice(rawText string) *float64 {
	if rawText == "" {
		return nil
	}

	text := strings.TrimSpace(currencyPrefixPattern.ReplaceAllString(rawText, ""))

	match := priceNumberPattern.FindString(text)
	if match == "" {
		return nil
	}

	cleaned := strings.ReplaceAll(match, ",", "")
	value, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return nil
	}

	return &value
}

// CheckResponseStatus is a direct port of Scraper.check_response_status().
//
// Python raises a bare Exception with message "http_error_{code}" for any
// 4xx or 5xx status; both branches did the identical thing (same message
// format, same behavior), so they're collapsed into one range check here
// rather than mechanically preserved as two — this is a faithful
// behavioral port, not a Python-structure-preserving one.
func CheckResponseStatus(statusCode int) error {
	if statusCode >= 400 && statusCode < 600 {
		return fmt.Errorf("http_error_%d", statusCode)
	}
	return nil
}

// defaultWaitSelectorTimeout is used for every WaitForSelector call made
// from Scraper.doScrape. scraper.py's call site
// (`await session.wait_for_selector(selector)`) passes no timeout at all
// — Python's wait_for_selector must carry its own default internally, but
// browser/session.py was not provided as source, so that default value
// could not be verified. 10 seconds is a placeholder judgment call, not a
// ported value — confirm against session.py's real default before
// treating this as settled.
const defaultWaitSelectorTimeout = 10 * time.Second

// SelectorConfig is the per-field extraction strategy config, direct port
// of the dict shape scraper.py's _extract_data() reads per field
// (config["find_by_text"], config["then_next"], config["selector"],
// config["direct_text"], config["preserve_semantics"]). This shape is
// evidenced by both _extract_data's field reads and
// llm_extractor.py's SELECTOR_USER_PROMPT's documented output schema —
// not fabricated from blueprint prose alone.
//
// A TrackingPair's PriceSelector/StockSelector DB columns each hold one
// flattened selector *string* (see writer.py's update_pair_selectors),
// not this whole multi-field config shape — something upstream
// (Orchestrator, not yet ported) is responsible for turning those two
// raw strings, or a cached LLM discovery result, into the
// map[string]*SelectorConfig this function actually consumes. This file
// only owns what happens once that map already exists.
type SelectorConfig struct {
	// Selector is a CSS selector string for goquery's Find(...).First(),
	// equivalent to BeautifulSoup's soup.select_one(). Corresponds to
	// config["selector"].
	Selector *string `json:"selector,omitempty"`

	// FindByText is the [target_tag, search_text] pair for the
	// text-lookup fallback strategy. Corresponds to
	// config["find_by_text"]; must have length >= 2 to be used, mirroring
	// Python's `isinstance(lookup, (list, tuple)) and len(lookup) >= 2`
	// guard.
	FindByText []string `json:"find_by_text,omitempty"`

	// ThenNext is the tag name to search forward for (document order,
	// not sibling-only — see findNextNode) once FindByText's label node
	// is located. Corresponds to config["then_next"].
	ThenNext *string `json:"then_next,omitempty"`

	// DirectText, if true, captures only the matched element's direct
	// child text nodes rather than the full recursive text content,
	// falling back to full text if that's empty. Corresponds to
	// config["direct_text"].
	DirectText bool `json:"direct_text,omitempty"`

	// PreserveSemantics, if true, keeps the matched element's outer HTML
	// intact instead of extracting plain text — used for the
	// "description" field. Corresponds to config["preserve_semantics"].
	PreserveSemantics bool `json:"preserve_semantics,omitempty"`
}

// Scraper extracts price/stock from a known URL using confirmed
// selectors. Direct port of scraper.py's Scraper class.
type Scraper struct {
	headless bool
	waitTime time.Duration
	timeout  time.Duration
}

// NewScraper constructs a Scraper. Mirrors Scraper.__init__'s three
// config fields exactly (headless, wait_time, timeout) — Python's
// defaults (headless=True, wait_time=5, timeout=60000ms) are the
// caller's responsibility to supply in Go, not baked in here, since Go
// has no default-parameter-value syntax to mirror faithfully.
func NewScraper(headless bool, waitTime, timeout time.Duration) *Scraper {
	return &Scraper{headless: headless, waitTime: waitTime, timeout: timeout}
}

// Scrape is a direct port of Scraper.scrape().
//
// If sess is non-nil, it is used directly and its lifecycle stays owned
// by the caller — matching Python's `if session:` branch. If sess is
// nil, a brand-new top-level browser.Session is opened for the duration
// of this one scrape and closed before returning, matching Python's
// `async with BrowserSession(...) as session:` branch.
//
// NOTE (Go interface-nil gotcha): sess must be either the literal `nil`
// or a Session obtained from a real constructor (browser.NewSession,
// FreshContext, etc.) — passing a nil-valued *ChromedpSession wrapped in
// a non-nil browser.Session interface value will NOT be treated as "no
// session" by the `sess != nil` check below, since a non-nil interface
// holding a nil concrete pointer is itself non-nil. This is a Go-specific
// footgun with no Python equivalent (Python's `if session:` just checks
// for None).
func (s *Scraper) Scrape(
	ctx context.Context,
	url string,
	selectors map[string]*SelectorConfig,
	waitSelectors []string,
	sess browser.Session,
	customIn string,
	customOut string,
) (*domain.AvailabilityResult, error) {
	if sess != nil {
		return s.doScrape(ctx, sess, url, selectors, waitSelectors, customIn, customOut)
	}

	newSess, err := browser.NewSession(ctx, s.headless, s.timeout)
	if err != nil {
		return nil, fmt.Errorf("scraper: failed to open browser session: %w", err)
	}
	defer newSess.Close(ctx)

	return s.doScrape(ctx, newSess, url, selectors, waitSelectors, customIn, customOut)
}

// doScrape is a direct port of Scraper._do_scrape(), navigation half
// only — see scrapeDoc below for the extraction+parsing half, factored
// out as its own method (NEW split, not present in the original port;
// see scrapeDoc's own doc comment for why).
//
// Behavioral notes preserved exactly from the Python source (now living
// partly here, partly in scrapeDoc — the split changes nothing about
// what either half does, only where the seam is):
//   - Status resolution only has two success branches (IN_STOCK/
//     OUT_OF_STOCK) and one failure branch (ERROR/unparseable_stock_status)
//     gated purely on whether stock could be classified — a successfully
//     parsed price does NOT change this; a page with a good price but an
//     unparseable stock string still ends in ERROR, exactly as Python does.
//   - RawPriceText/RawStockText are always set from the extraction result
//     (even if nil), before the truthy-style presence checks that gate
//     ParsePrice/ClassifyStockText — mirroring
//     result = AvailabilityResult(raw_price_text=..., raw_stock_text=...)
//     running unconditionally, followed by separate `if extracted.get(...)`
//     guards.
//   - Python's `if extracted.get("price"):` is a truthiness check — an
//     empty string is just as falsy as None, so ParsePrice/
//     ClassifyStockText are skipped for a field that was found but
//     extracted as "". This is preserved via the `v != nil && *v != ""`
//     guards in scrapeDoc, not just a nil check.
func (s *Scraper) doScrape(
	ctx context.Context,
	sess browser.Session,
	url string,
	selectors map[string]*SelectorConfig,
	waitSelectors []string,
	customIn string,
	customOut string,
) (*domain.AvailabilityResult, error) {
	s.rateLimitWait(ctx)

	fmt.Fprintf(os.Stderr, "[Scraper] navigating to %s\n", url)
	if err := sess.Navigate(ctx, url); err != nil {
		return nil, err
	}

	// Wait for critical selectors if specified. session.go's
	// WaitForSelector already swallows its own timeout/selection errors
	// (documented on the Session interface, matching Python's
	// `except Exception: pass`), so any error returned here is not
	// expected in practice — not further suppressed, just genuinely rare.
	for _, sel := range waitSelectors {
		if err := sess.WaitForSelector(ctx, sel, defaultWaitSelectorTimeout); err != nil {
			return nil, err
		}
	}

	htmlStr, err := sess.GetHTML(ctx)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "[Scraper] fetched %d bytes of HTML from %s\n", len(htmlStr), url)

	// NEW: scope to the same "product content" view CleanHTML shows the
	// LLM, via heuristics.ScopeToMainContent, instead of parsing the raw
	// page unscoped. Was: `doc, err := goquery.NewDocumentFromReader(...)`
	// against the full page. See ScopeToMainContent's doc comment for the
	// full "why" — a selector the LLM reasoned about against a scoped
	// excerpt was previously being matched for real against the full
	// unscoped page, where duplicate similarly-classed elements (mini-cart
	// widgets, related-product carousels, etc.) could silently win instead
	// of the real target. This applies identically to every real Path C
	// scrape here, not just discovery-time validation (discovery.go makes
	// the equivalent change to its own scrapeDoc call).
	scope, err := heuristics.ScopeToMainContent(htmlStr)
	if err != nil {
		return nil, err
	}

	return s.scrapeDoc(scope, selectors, customIn, customOut), nil
}

// scrapeDoc is the extraction+parsing tail of Scraper._do_scrape() —
// everything that happens once HTML is already in hand, with no browser
// or navigation involved. Factored out (NEW split; the original port had
// this inlined at the end of doScrape) so a caller that already has a
// scoped view of the page — because it fetched the page itself moments
// ago, for a different reason — can reuse this exact extraction/parsing/
// status-resolution logic instead of duplicating it, or being forced
// through a real Navigate call it doesn't actually need.
//
// Concrete motivating caller: discovery.go's --commit validation step.
// Selector discovery already fetches the product page once (to feed the
// LLM); re-navigating a second time purely to validate the LLM's proposed
// selectors turned out to be actively harmful against at least one real
// store (jumpbooks.lk) — Cloudflare blocks a session's second navigation
// regardless of what that navigation is. scrapeDoc lets validation reuse
// the FIRST (and only) fetch's HTML directly, sidestepping that failure
// mode entirely rather than working around it with a second isolated
// session. See discovery.go's package doc comment divergence #5 for the
// full reasoning and the honest trade-off this implies (validation
// becomes a self-consistency check against the LLM's own input HTML,
// not an independently live-refetched check the way
// discover_selectors.py's validate_selectors() did it).
//
// Parameter type changed from *goquery.Document to *goquery.Selection
// (NEW, second change on top of the original scrapeDoc split above) —
// scope is now always the result of heuristics.ScopeToMainContent, the
// SAME scoped view CleanHTML shows the LLM, not the full raw page. This
// closes a real bug: a selector the LLM proposed against a scoped
// excerpt (where it was genuinely unambiguous) was previously matched
// for real against the full unscoped page here, where duplicate
// similarly-classed elements elsewhere on the page (a site-header
// mini-cart widget, a "Recently Viewed" aside, a "Related Products"
// carousel — all live-confirmed on a real jumpbooks.lk page, 14 total
// matches for one class) could silently win over the real target
// instead. goquery.Document embeds *goquery.Selection, so every .Find()
// call below behaves identically either way — this is a scoping change,
// not a Find-API change.
func (s *Scraper) scrapeDoc(scope *goquery.Selection, selectors map[string]*SelectorConfig, customIn, customOut string) *domain.AvailabilityResult {
	extracted := s.extractData(scope, selectors)
	fmt.Fprintf(os.Stderr, "[Scraper] raw extracted: price=%s availability=%s\n",
		derefOrNotFound(extracted["price"]), derefOrNotFound(extracted["availability"]))

	result := domain.NewAvailabilityResult()
	result.RawPriceText = extracted["price"]
	result.RawStockText = extracted["availability"]

	if v := extracted["price"]; v != nil && *v != "" {
		result.Price = ParsePrice(*v)
	}

	if v := extracted["availability"]; v != nil && *v != "" {
		result.InStock = ParseStockStatus(*v, customIn, customOut)
	}

	switch {
	case result.InStock != nil && *result.InStock:
		status := "IN_STOCK"
		result.Status = &status
	case result.InStock != nil && !*result.InStock:
		status := "OUT_OF_STOCK"
		result.Status = &status
	default:
		status := "ERROR"
		reason := "unparseable_stock_status"
		result.Status = &status
		result.Reason = &reason
	}
	fmt.Fprintf(os.Stderr, "[Scraper] parsed: price=%v in_stock=%v status=%s\n", result.Price, result.InStock, *result.Status)

	return result
}

// derefOrNotFound is a NEW diagnostic helper (no Python equivalent) —
// distinguishes "selector matched nothing" (nil) from "selector matched
// an empty string" ("") in printed output, since extractField's own doc
// comment notes these are two genuinely different outcomes it preserves.
func derefOrNotFound(v *string) string {
	if v == nil {
		return "<no match>"
	}
	return fmt.Sprintf("%q", *v)
}

// rateLimitWait is a direct port of Scraper._rate_limit_wait().
//
// Python's `await asyncio.sleep(self._wait_time)` is itself interruptible
// by task cancellation; select-ing on ctx.Done() alongside time.After is
// the Go-idiomatic equivalent of that (a blind time.Sleep would not be
// cancellable), not a behavioral deviation.
func (s *Scraper) rateLimitWait(ctx context.Context) {
	select {
	case <-time.After(s.waitTime):
	case <-ctx.Done():
	}
}

// extractData is a direct port of Scraper._extract_data().
//
// Returns one entry per key in selectors: nil for a nil config, nil if no
// element matched, or a pointer to the (possibly empty-string, per
// Python's own behavior — see extractField's doc comment) cleaned
// extracted value.
func (s *Scraper) extractData(scope *goquery.Selection, selectors map[string]*SelectorConfig) map[string]*string {
	productInfo := make(map[string]*string, len(selectors))
	for field, cfg := range selectors {
		if cfg == nil {
			productInfo[field] = nil
			continue
		}
		productInfo[field] = extractField(scope, cfg)
	}
	return productInfo
}

// extractField handles one selector's worth of Scraper._extract_data()'s
// per-field try/except body: strategy dispatch (find_by_text vs. selector),
// value extraction (preserve_semantics vs. direct_text vs. default
// get_text-equivalent), and the universal cleanup pass. A panic during
// extraction (the Go analog of Python's `except Exception`, since a
// malformed CSS selector string can panic inside cascadia's parser rather
// than returning an error) is recovered and treated as a nil result for
// this field only — it does not abort extraction of the other fields, and returns
// mirroring Python's per-field except Exception: product_info[field] = None
// behavior.
//
// Returns nil only when no element matched (or a panic was recovered) —
// note that Python's own behavior stores "" (not None) for a field that
// WAS found but extracted to an empty string after cleanup, so this
// returns a pointer to "" in that case, not nil, matching
// `product_info[field] = extracted_value` running unconditionally once
// `element is not None`.
func extractField(scope *goquery.Selection, cfg *SelectorConfig) (result *string) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
		}
	}()

	var node *html.Node

	switch {
	case len(cfg.FindByText) >= 2:
		targetTag, searchText := cfg.FindByText[0], cfg.FindByText[1]
		labelNode := findTagWithText(scope, targetTag, searchText)
		if labelNode != nil {
			if cfg.ThenNext != nil && *cfg.ThenNext != "" {
				node = findNextNode(labelNode, *cfg.ThenNext)
			} else {
				node = labelNode
			}
		}
	case cfg.Selector != nil && *cfg.Selector != "":
		sel := scope.Find(*cfg.Selector).First()
		if sel.Length() > 0 {
			node = sel.Nodes[0]
		}
	}

	if node == nil {
		return nil
	}

	var extracted string
	switch {
	case cfg.PreserveSemantics:
		outer, err := nodeOuterHTML(node)
		if err != nil {
			return nil
		}
		extracted = outer
	case cfg.DirectText:
		extracted = nodeDirectText(node)
		if strings.TrimSpace(extracted) == "" {
			extracted = nodeText(node)
		}
		extracted = strings.TrimSpace(extracted)
	default:
		extracted = strings.TrimSpace(nodeText(node))
	}

	extracted = cleanExtractedValue(extracted)
	return &extracted
}

// cleanExtractedValue mirrors the "Universal Sanity Cleanup Layer":
// extracted_value.strip().lstrip(":-•").strip(). strings.TrimLeft's
// cutset semantics (trim any of the given runes, repeatedly, from the
// left) match Python's str.lstrip(chars) exactly.
func cleanExtractedValue(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, ":-•")
	s = strings.TrimSpace(s)
	return s
}

// findTagWithText is the Go equivalent of BeautifulSoup's
// `soup.find(target_tag, string=lambda t: t and search_text.lower() in
// t.lower())`.
//
// FLAGGED APPROXIMATION: BS4's `string=` filter, when combined with a
// tag-name filter, matches against a tag's `.string` property — which has
// its own quirky recursive-single-child-chain semantics in real
// BeautifulSoup (it returns None for a tag with multiple children or
// mixed content, walking down single-child chains otherwise). Without
// bs4's own source available to verify that logic precisely, this
// implementation instead matches against the tag's full recursive text
// content (get_text()-equivalent) — which is behaviorally identical to
// `.string` for the simple, single-text-node label elements this
// project's real prompts document as the expected case (e.g. `<span>ISBN
// 13</span>`, `<li>ISBN 13: 978...</li>`), but could diverge from BS4 on
// a label tag with genuinely mixed/nested content. Flagging this
// explicitly rather than presenting it as verified-identical.
//
// goquery.Find(tag) is trusted to return matches in document order (a
// documented cascadia/goquery property), so the first textual match here
// corresponds to BS4's find()'s "first match" semantics.
func findTagWithText(scope *goquery.Selection, tag, searchText string) *html.Node {
	searchLower := strings.ToLower(searchText)
	var found *html.Node

	scope.Find(tag).EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		if len(sel.Nodes) == 0 {
			return true // keep looking
		}
		text := strings.ToLower(nodeText(sel.Nodes[0]))
		if strings.Contains(text, searchLower) {
			found = sel.Nodes[0]
			return false // stop iteration
		}
		return true
	})

	return found
}

// findNextNode is the Go equivalent of BeautifulSoup's
// `label_node.find_next(tag)`.
//
// Despite the "then_next" config field's name suggesting a sibling
// relationship, BS4's find_next() is a genuine document-order forward
// search (starting from the node's own first child, if any, then
// continuing via next-sibling-or-ancestor's-next-sibling) — it is NOT
// scoped to siblings of label_node. This implementation walks the raw
// *html.Node tree in that same document-order-continuation pattern,
// filtering for the first ElementNode whose tag name matches, exactly
// mirroring find_next(name)'s behavior.
func findNextNode(start *html.Node, tag string) *html.Node {
	cur := start
	for {
		cur = nextDocumentNode(cur)
		if cur == nil {
			return nil
		}
		if cur.Type == html.ElementNode && cur.Data == tag {
			return cur
		}
	}
}

// nextDocumentNode advances one step in document (pre-)order from n: into
// n's own first child if it has one, otherwise to n's next sibling,
// otherwise walking up to the nearest ancestor with a next sibling.
// Mirrors BS4's Tag.next_element traversal step.
func nextDocumentNode(n *html.Node) *html.Node {
	if n.FirstChild != nil {
		return n.FirstChild
	}
	for n != nil {
		if n.NextSibling != nil {
			return n.NextSibling
		}
		n = n.Parent
	}
	return nil
}

// nodeText returns the concatenated text content of n and all its
// descendants, mirroring BeautifulSoup's Tag.get_text().
func nodeText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// nodeDirectText concatenates only n's direct child text nodes, not
// descending into child elements — mirrors BS4's
// "".join(element.find_all(text=True, recursive=False)).
func nodeDirectText(n *html.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	}
	return sb.String()
}

// nodeOuterHTML renders n and its subtree back to an HTML string,
// mirroring BS4's str(element) — used for the preserve_semantics
// extraction strategy.
func nodeOuterHTML(n *html.Node) (string, error) {
	var buf strings.Builder
	if err := html.Render(&buf, n); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ParseStockStatus is a thin delegation to heuristics.ClassifyStockText —
// direct port of Scraper.parse_stock_status(). Kept as a package-level
// function rather than a Scraper method, mirroring ParsePrice's existing
// shape, since it depends on no Scraper field. Per
// vestige_go_pipeline_implementation.md Section 1: "ParseStockStatus
// delegates to heuristics.ClassifyStockText".
func ParseStockStatus(rawText, customIn, customOut string) *bool {
	return heuristics.ClassifyStockText(rawText, customIn, customOut)
}
