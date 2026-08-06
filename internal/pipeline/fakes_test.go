package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/udanfernando2006/vestige-go/internal/browser"
)

// fakeSession is a scripted, in-memory implementation of browser.Session
// used to unit-test Scraper/Crawler control flow without a real browser
// or network — see vestige_go_pipeline_implementation.md Section 3:
// "browser.Session ... Real implementation uses chromedp; tests use a
// fake," mirroring the existing pytest convention of
// `BrowserSession -> AsyncMock`.
//
// It is intentionally simple: Navigate records the URL and makes it the
// "current" one; GetHTML looks up canned HTML for whatever URL was most
// recently navigated to. This is enough to drive every code path in
// Scraper.doScrape and Crawler.runWithSession/validateCandidates without
// needing a real page.
type fakeSession struct {
	// htmlByURL maps a navigated-to URL to the HTML GetHTML should return
	// next. A URL with no entry returns "".
	htmlByURL map[string]string

	// navigateErr, if non-nil, is returned by every Navigate call instead
	// of actually navigating — used to test error propagation.
	navigateErr error

	// getURLOverride, if non-nil, is returned by GetURL instead of the
	// current URL — used to simulate a store's search_url_template being
	// discovered as something other than the literal navigated-to URL.
	getURLOverride *string

	// findAndFillSearchResult/-Err control FindAndFillSearch's return
	// value for tests that exercise Crawler.runDiscovery.
	findAndFillSearchResult bool
	findAndFillSearchErr    error

	currentURL         string
	navigateCalls      []string
	freshContextCalls  int
	closed             bool
}

func (f *fakeSession) Navigate(ctx context.Context, url string) error {
	f.navigateCalls = append(f.navigateCalls, url)
	f.currentURL = url
	return f.navigateErr
}

func (f *fakeSession) GetHTML(ctx context.Context) (string, error) {
	if f.htmlByURL != nil {
		if html, ok := f.htmlByURL[f.currentURL]; ok {
			return html, nil
		}
	}
	return "", nil
}

func (f *fakeSession) GetURL(ctx context.Context) (string, error) {
	if f.getURLOverride != nil {
		return *f.getURLOverride, nil
	}
	return f.currentURL, nil
}

func (f *fakeSession) WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error {
	return nil
}

func (f *fakeSession) FindAndFillSearch(ctx context.Context, query string) (bool, error) {
	return f.findAndFillSearchResult, f.findAndFillSearchErr
}

func (f *fakeSession) FreshContext(ctx context.Context) (browser.Session, error) {
	f.freshContextCalls++
	return f, nil
}

func (f *fakeSession) Close(ctx context.Context) error {
	f.closed = true
	return nil
}

// mustParseHTML is a test helper wrapping goquery.NewDocumentFromReader,
// failing the test immediately on a parse error rather than requiring
// every test case to handle it.
func mustParseHTML(t *testing.T, htmlStr string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		t.Fatalf("failed to parse test HTML: %v", err)
	}
	return doc
}

func ptrFloat64(v float64) *float64 { return &v }
func ptrBool(v bool) *bool          { return &v }
func ptrString(v string) *string    { return &v }
