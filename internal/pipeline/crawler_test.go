package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/domain"
)

func TestBuildSearchURL(t *testing.T) {
	cases := []struct {
		name, base, query, want string
	}{
		{"simple", "https://jumpbooks.lk/?s=test", "Hobbit", "https://jumpbooks.lk/?s=Hobbit"},
		{"space encoding uses + like quote_plus", "https://x.lk/?s=test", "The Hobbit", "https://x.lk/?s=The+Hobbit"},
		{"replaces every occurrence, not just the first", "https://x.lk/?s=test&ref=test", "Q", "https://x.lk/?s=Q&ref=Q"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildSearchURL(c.base, c.query)
			if got != c.want {
				t.Errorf("BuildSearchURL(%q, %q) = %q, want %q", c.base, c.query, got, c.want)
			}
		})
	}
}

func TestExtractCandidateLinks(t *testing.T) {
	html := `<a href="/products/hobbit">The <b>Hobbit</b></a><a href="/cart">Cart</a>`

	got := extractCandidateLinks(html)

	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	// "The <b>Hobbit</b>" -> two text nodes ("The ", "Hobbit"), each
	// stripped individually then joined with "" — this is the flagged
	// bs4GetTextStrip approximation of get_text(strip=True), not a plain
	// TrimSpace on the whole concatenation.
	if got[0].Href != "/products/hobbit" || got[0].Text != "TheHobbit" {
		t.Errorf("got[0] = %+v, want Href=/products/hobbit Text=TheHobbit", got[0])
	}
	if got[1].Href != "/cart" || got[1].Text != "Cart" {
		t.Errorf("got[1] = %+v, want Href=/cart Text=Cart", got[1])
	}
}

func TestExtractCandidateLinks_Empty(t *testing.T) {
	got := extractCandidateLinks("")
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestExtractCandidateLinks_NoHrefAttrSkipped(t *testing.T) {
	// An <a> with no href at all (e.g. a JS-only click handler anchor)
	// must not appear in the result — mirrors soup.find_all("a",
	// href=True) only matching anchors that actually have the attribute.
	html := `<a>no href here</a><a href="/real">Real Link</a>`
	got := extractCandidateLinks(html)
	if len(got) != 1 || got[0].Href != "/real" {
		t.Errorf("got %+v, want exactly one candidate for /real", got)
	}
}

// TestCrawler_FindProductURL_WithSession exercises FindProductURL's
// with-session path end-to-end: search-page navigation, candidate
// extraction, heuristics.ScoreCandidates, and heuristics.ScoreValidation
// on the winning candidate — all against a fakeSession, no real browser.
func TestCrawler_FindProductURL_WithSession(t *testing.T) {
	searchHTML := `<a href="/products/the-hobbit">The Hobbit - J.R.R. Tolkien</a><a href="/collections/all">Browse All</a>`
	productHTML := `<html><body><h1>The Hobbit</h1><p>In stock. ISBN 9780547928227. Available now for shipping.</p></body></html>`

	fs := &fakeSession{
		htmlByURL: map[string]string{
			"https://example.lk/?s=The+Hobbit":        searchHTML,
			"https://example.lk/products/the-hobbit": productHTML,
		},
	}

	store := &domain.Store{
		BaseURL:           "https://example.lk",
		SearchURLTemplate: ptrString("https://example.lk/?s=test"),
	}

	crawler := NewCrawler(true, 5*time.Second)
	result, err := crawler.FindProductURL(context.Background(), store, "The Hobbit", "", fs)
	if err != nil {
		t.Fatalf("FindProductURL returned error: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if result.ProductURL == nil || *result.ProductURL != "https://example.lk/products/the-hobbit" {
		t.Errorf("ProductURL = %v, want https://example.lk/products/the-hobbit", derefOrNilStr(result.ProductURL))
	}
	if result.SearchURLTemplate == nil || *result.SearchURLTemplate != "https://example.lk/?s=test" {
		t.Errorf("SearchURLTemplate = %v, want the store's cached template echoed back on success", derefOrNilStr(result.SearchURLTemplate))
	}
}

// TestCrawler_FindProductURL_NotListed verifies that a search page with
// no keyword-matching candidates resolves to a NOT_LISTED failure rather
// than an error — matching _run_with_session's `if not scored: return
// {"success": False, ..., "status": "NOT_LISTED"}` branch.
func TestCrawler_FindProductURL_NotListed(t *testing.T) {
	searchHTML := `<a href="/products/unrelated-title">Completely Unrelated Book</a>`
	fs := &fakeSession{
		htmlByURL: map[string]string{
			"https://example.lk/?s=The+Hobbit": searchHTML,
		},
	}
	store := &domain.Store{
		BaseURL:           "https://example.lk",
		SearchURLTemplate: ptrString("https://example.lk/?s=test"),
	}

	crawler := NewCrawler(true, 5*time.Second)
	result, err := crawler.FindProductURL(context.Background(), store, "The Hobbit", "", fs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success: %+v", result)
	}
	if result.Status == nil || *result.Status != "NOT_LISTED" {
		t.Errorf("Status = %v, want NOT_LISTED", derefOrNilStr(result.Status))
	}
}

// TestCrawler_FindProductURL_SingleResultRedirect_Success covers
// checkForSingleResultRedirect's new logic (NOT present in crawler.py —
// see that function's doc comment) — simulates a store that redirects an
// exact-match search (e.g. by ISBN, as confirmed live against
// jumpbooks.lk) straight to the single matching product's own page,
// rather than showing a results listing.
func TestCrawler_FindProductURL_SingleResultRedirect_Success(t *testing.T) {
	productHTML := `<html><body><h1>The Last Wish</h1><p>In stock. ISBN 9781399611398. Available now for shipping.</p></body></html>`

	fs := &fakeSession{
		htmlByURL: map[string]string{
			// Navigate is called with the constructed search URL; the
			// HTML actually there (post-redirect, from the store's point
			// of view) is the product page's own HTML.
			"https://example.lk/?s=9781399611398": productHTML,
		},
		getURLOverride: ptrString("https://example.lk/product/the-last-wish"),
	}

	store := &domain.Store{
		BaseURL:           "https://example.lk",
		SearchURLTemplate: ptrString("https://example.lk/?s=test"),
	}

	crawler := NewCrawler(true, 5*time.Second)
	result, err := crawler.FindProductURL(context.Background(), store, "The Last Wish", "9781399611398", fs)
	if err != nil {
		t.Fatalf("FindProductURL returned error: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success via the redirect shortcut, got %+v", result)
	}
	if result.ProductURL == nil || *result.ProductURL != "https://example.lk/product/the-last-wish" {
		t.Errorf("ProductURL = %v, want the redirected-to URL directly, not a re-scored candidate", derefOrNilStr(result.ProductURL))
	}
	// Exactly one navigate call — the redirect shortcut must validate
	// using the HTML already fetched, not re-navigate to confirm.
	if len(fs.navigateCalls) != 1 {
		t.Errorf("navigateCalls = %v, want exactly 1 (redirect shortcut must not re-navigate)", fs.navigateCalls)
	}
}

// TestCrawler_FindProductURL_RedirectDoesNotValidate_FallsThrough covers
// the safety net: a redirect was detected, but the page landed on does
// NOT validate against title/ISBN (e.g. redirected to an unrelated
// category page) — must fall through to normal candidate extraction
// against the SAME already-fetched HTML, not blindly trust the redirect.
func TestCrawler_FindProductURL_RedirectDoesNotValidate_FallsThrough(t *testing.T) {
	listingHTML := `<a href="/products/the-last-wish">The Last Wish - Andrzej Sapkowski</a>`
	productHTML := `<html><body><h1>The Last Wish</h1><p>In stock. Available for shipping.</p></body></html>`

	fs := &fakeSession{
		htmlByURL: map[string]string{
			"https://example.lk/?s=9781399611398":       listingHTML,
			"https://example.lk/products/the-last-wish": productHTML,
		},
		// Simulates a redirect to somewhere that ISN'T the target book —
		// this page's own HTML (listingHTML, keyed above) has no title/
		// ISBN match at all, so ScoreValidation must reject it.
		getURLOverride: ptrString("https://example.lk/category/some-other-listing"),
	}

	store := &domain.Store{
		BaseURL:           "https://example.lk",
		SearchURLTemplate: ptrString("https://example.lk/?s=test"),
	}

	crawler := NewCrawler(true, 5*time.Second)
	result, err := crawler.FindProductURL(context.Background(), store, "The Last Wish", "9781399611398", fs)
	if err != nil {
		t.Fatalf("FindProductURL returned error: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success via normal candidate fallback, got %+v", result)
	}
	if result.ProductURL == nil || *result.ProductURL != "https://example.lk/products/the-last-wish" {
		t.Errorf("ProductURL = %v, want the scored candidate extracted from the listing HTML, not the redirect target", derefOrNilStr(result.ProductURL))
	}
}

// TestCrawler_FindProductURL_ValidatesViaFreshContextPerCandidate asserts
// the NEW per-candidate isolation in tryValidateCandidate: FreshContext
// must be called once for each candidate actually tried (here, exactly
// one — the top candidate validates immediately) — not zero (reusing the
// shared session directly, the old pre-fix behavior) and not more than
// were actually needed.
func TestCrawler_FindProductURL_ValidatesViaFreshContextPerCandidate(t *testing.T) {
	searchHTML := `<a href="/products/the-hobbit">The Hobbit - J.R.R. Tolkien</a>`
	productHTML := `<html><body><h1>The Hobbit</h1><p>In stock. Available for shipping.</p></body></html>`

	fs := &fakeSession{
		htmlByURL: map[string]string{
			"https://example.lk/?s=The+Hobbit":       searchHTML,
			"https://example.lk/products/the-hobbit": productHTML,
		},
	}

	store := &domain.Store{
		BaseURL:           "https://example.lk",
		SearchURLTemplate: ptrString("https://example.lk/?s=test"),
	}

	crawler := NewCrawler(true, 5*time.Second)
	result, err := crawler.FindProductURL(context.Background(), store, "The Hobbit", "", fs)
	if err != nil {
		t.Fatalf("FindProductURL returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if fs.freshContextCalls != 1 {
		t.Errorf("freshContextCalls = %d, want exactly 1 (one candidate tried, one fresh context)", fs.freshContextCalls)
	}
}

// TestCrawler_FindProductURL_NoSessionOrTemplate_UsesDiscovery is a
// documentation-style test confirming the routing decision itself (no
// session, or no cached template -> runDiscovery, which needs its own
// two-brand-new-sessions flow and therefore is NOT exercisable via a
// single fakeSession the way runWithSession is). This is intentionally
// t.Skip'd — runDiscovery needs a real browser.NewSession, covered
// instead by a future cmd/crawlercheck live tool, not this fake-session
// suite.
func TestCrawler_FindProductURL_NoSessionOrTemplate_UsesDiscovery(t *testing.T) {
	t.Skip("runDiscovery opens its own top-level browser.NewSession internally — needs a real browser, covered by a live cmd/ tool instead")
}
