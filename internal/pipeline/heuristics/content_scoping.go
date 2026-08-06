// Package heuristics — see stock_status.go/candidate_scoring.go/
// validation_scoring.go for the rest of this package's pure,
// store-tuned pattern-matching functions.
//
// content_scoping.go is NEW — extracted from extractor.go's CleanHTML,
// not ported from any Python source (v1 never had this problem: Python's
// discover_selectors.py validated selectors via a genuine second live
// fetch of the untrimmed page, and Scraper.scrape() never trimmed
// anything at all — the scope-mismatch this file exists to close is
// specific to this Go rewrite's scrapeDoc/self-consistency design).
//
// Background (see the standalone "Path B Redundant-Navigation Redesign"
// implementation plan and the follow-up discussion around it): CleanHTML
// was originally the ONLY consumer of "which subtree of the page is the
// real product content" — it scoped a page down to <main> (or an
// id="main"/"content"/"primary" fallback) and stripped known noise
// (sidebars, related-product carousels, mini-cart widgets, etc.) purely
// to shrink what got shown to the LLM. Scraper.doScrape and
// discovery.go's --commit self-consistency validation both applied the
// LLM's resulting CSS selector against the FULL, un-scoped raw page
// instead — meaning a selector like "span.woocommerce-Price-amount" that
// was perfectly unambiguous in the cleaned excerpt the LLM saw could
// still resolve to the wrong element (e.g. a site-header mini-cart
// widget reusing the identical class) once evaluated for real, because
// CSS selectors carry no memory of the scope they were reasoned about
// in. Live-confirmed on a real jumpbooks.lk page (14 total matches for
// that exact class across the full page: header cart, a "Recently
// Viewed" aside, a "Related Products" carousel, and the one real product
// price).
//
// The fix: make "which subtree counts as the product's own content" a
// single shared decision, used identically by CleanHTML (LLM input
// shaping) AND by Scraper (real selector matching, both at --commit
// validation time and on every future Path C scrape) — not two separate
// views of the same page that happen to usually agree.
package heuristics

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// coreIDPattern, tagsToRemove, and noiseSelectors are moved here
// unchanged from extractor.go — same values, same behavior, just
// relocated so both consumers (CleanHTML and Scraper) share one
// definition instead of two that could silently drift apart.

var coreIDPattern = regexp.MustCompile(`(?i)^(main|content|primary)$`)

// tagsToRemove — operational/chrome elements stripped from the WHOLE
// document before any scoping decision is made, regardless of whether a
// <main> container is later found.
var tagsToRemove = []string{
	"script", "style", "nav", "footer", "header", "svg", "iframe",
	"noscript", "dialog", "form",
}

// noiseSelectors — heavy sidebar/noise components stripped from INSIDE
// the scoped area only (or the whole document, if no <main>/id-matched
// core was found). CSS selectors, unlike tagsToRemove's bare tag names,
// so these go through Find(selector) rather than Find(tag).
//
// KNOWN GAP, worth stating rather than silently living with: this list
// does not include a WooCommerce-mini-cart-specific selector
// (e.g. ".widget_shopping_cart", ".cart-contents") — live investigation
// against a real jumpbooks.lk page found the mini-cart widget lives
// inside a real <header> tag on that theme, so tagsToRemove's bare
// "header" removal already covers it there. A theme that renders its
// mini-cart OUTSIDE any <header>/<nav> tag would not be caught by
// anything in this list today. Not fixed speculatively — per this
// project's own discipline, add a targeted selector here only once a
// real store is found where it matters, the same way v3.10's
// Shopify /products/ fix and this file's own header/aside/related
// findings were each driven by an actual failing page, not guessed at
// in advance.
var noiseSelectors = []string{
	"aside", ".sidebar", "#sidebar", ".widget-area", ".related",
	".upsells", ".cross-sells", ".product-carousel", ".woocommerce-tabs",
	"#reviews", ".social-share", ".cookie-notice",
}

// ScopeToMainContent parses rawHTML and returns the *goquery.Selection
// that represents "this page's actual product content" — everything a
// CSS selector should be allowed to match against, and nothing else.
//
// This is steps 1-3 of what was CleanHTML's body (tag removal, <main>/
// id-regex scoping, in-scope noise removal) — identical logic, verbatim
// pattern lists, just factored out so it has exactly one definition.
// Deliberately does NOT do CleanHTML's step 4 (engine-specific attribute
// stripping / re-serializing to a string) — that step is LLM-token-
// economy-specific and actively wrong to apply to a document real CSS
// selectors are about to be matched against (an attribute-selector
// keyed on anything other than class/id would silently break under the
// "full" engine's attribute-keep policy, for instance). Callers that
// need the LLM-facing string form (CleanHTML) do their own stripping
// afterward, on top of this function's result; callers that need to
// match selectors for real (Scraper) use the returned Selection
// directly, with every attribute still intact.
//
// The returned Selection is either the found <main>/id-matched core
// element, or doc.Selection (the whole parsed document) if no core
// container was found — mirroring CleanHTML's own "scope := doc.Selection;
// if core != nil { scope = core }" fallback exactly.
func ScopeToMainContent(rawHTML string) (*goquery.Selection, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		return nil, fmt.Errorf("heuristics: failed to parse HTML: %w", err)
	}

	// 1. Eliminate heavy operational components from the WHOLE document,
	// before any scoping decision — matches CleanHTML's original step 1
	// exactly (this removal is unconditional, not scoped to core, since
	// it runs before core is even determined).
	for _, tag := range tagsToRemove {
		doc.Find(tag).Remove()
	}

	// 2. Scope down to the main content container, if present. Mirrors
	// BeautifulSoup's soup.find("main") first, then
	// soup.find(id=core_id_pattern) as a fallback — the id-regex branch
	// only runs if <main> wasn't found, preserved here via the same
	// short-circuit shape CleanHTML used.
	var core *goquery.Selection
	if mainSel := doc.Find("main").First(); mainSel.Length() > 0 {
		core = mainSel
	} else {
		doc.Find("[id]").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
			id, _ := sel.Attr("id")
			if coreIDPattern.MatchString(id) {
				core = sel
				return false
			}
			return true
		})
	}

	scope := doc.Selection
	if core != nil {
		scope = core
	}

	// 3. Remove heavy sidebar/noise components inside the scoped area
	// (or the whole document, if no core container was found) — matches
	// CleanHTML's original step 3 exactly.
	for _, sel := range noiseSelectors {
		scope.Find(sel).Remove()
	}

	return scope, nil
}
