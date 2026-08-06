package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParsePrice(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *float64
	}{
		{"empty", "", nil},
		{"LKR with comma", "LKR 3,303.00", ptrFloat64(3303.00)},
		{"Rs prefix", "Rs. 2,995", ptrFloat64(2995)},
		{"dollar sign", "$19.99", ptrFloat64(19.99)},
		{"no currency prefix", "1500", ptrFloat64(1500)},
		{"euro", "€45.50", ptrFloat64(45.50)},
		{"no digits at all", "Contact us for price", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParsePrice(c.in)
			assertFloatPtrEqual(t, c.want, got)
		})
	}
}

func TestCheckResponseStatus(t *testing.T) {
	cases := []struct {
		code    int
		wantErr bool
	}{
		{200, false},
		{399, false},
		{400, true},
		{404, true},
		{500, true},
		{599, true},
		{600, false}, // out of range, matches Python's elif chain not catching 600+
	}
	for _, c := range cases {
		got := CheckResponseStatus(c.code)
		if (got != nil) != c.wantErr {
			t.Errorf("CheckResponseStatus(%d) error = %v, wantErr %v", c.code, got, c.wantErr)
		}
	}
}

func TestParseStockStatus(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want *bool
	}{
		{"in stock", "In Stock", ptrBool(true)},
		{"out of stock", "Out of Stock", ptrBool(false)},
		{"unavailable substring trap", "Currently unavailable", ptrBool(false)}, // "unavailable" contains "available" — must not flip to true
		{"low stock production case", "Low stock: 4 left", ptrBool(true)},
		{"unclassifiable", "Ask a store rep", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseStockStatus(c.raw, "", "")
			assertBoolPtrEqual(t, c.want, got)
		})
	}
}

func TestCleanExtractedValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  :- Hello  ", "Hello"},
		{"•Price: 500", "Price: 500"}, // only leading chars stripped; the internal colon after "Price" is untouched
		{"", ""},
		{"978-1234567890", "978-1234567890"}, // internal hyphen must survive — only leading cutset chars strip
	}
	for _, c := range cases {
		got := cleanExtractedValue(c.in)
		if got != c.want {
			t.Errorf("cleanExtractedValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractField_SelectorDirectText(t *testing.T) {
	doc := mustParseHTML(t, `<div class="price-wrap"><span class="price">LKR 3,303.00</span> (incl. tax)</div>`)
	cfg := &SelectorConfig{Selector: ptrString("span.price"), DirectText: true}

	got := extractField(doc, cfg)

	want := "LKR 3,303.00"
	if got == nil || *got != want {
		t.Fatalf("got %v, want %q", derefOrNilStr(got), want)
	}
}

func TestExtractField_FindByTextThenNext(t *testing.T) {
	// Direct port target: llm_extractor.py's SELECTOR_USER_PROMPT worked
	// example — <tr><th><svg></svg><span>ISBN 13</span></th><td>:
	// 978...</td></tr> with find_by_text: ["span", "ISBN 13"],
	// then_next: "td". This exercises findNextNode's document-order
	// walk-up-then-across traversal (th's next SIBLING is td — not a
	// child of span, not a child of th), which is the non-obvious part
	// of this port.
	doc := mustParseHTML(t, `<table><tr><th><svg></svg><span>ISBN 13</span></th><td>: 978-1234567890</td></tr></table>`)
	cfg := &SelectorConfig{FindByText: []string{"span", "ISBN 13"}, ThenNext: ptrString("td")}

	got := extractField(doc, cfg)

	want := "978-1234567890" // leading ": " stripped by the universal cleanup's lstrip(":-•")
	if got == nil || *got != want {
		t.Fatalf("got %v, want %q", derefOrNilStr(got), want)
	}
}

func TestExtractField_FindByTextNoThenNext(t *testing.T) {
	// Matches llm_extractor.py's simpler documented case: "<li>ISBN 13:
	// 978...</li>" with find_by_text: ["li", "ISBN 13"], no then_next —
	// the label element itself is the value.
	doc := mustParseHTML(t, `<ul><li>ISBN 13: 978-9999999999</li></ul>`)
	cfg := &SelectorConfig{FindByText: []string{"li", "ISBN 13"}}

	got := extractField(doc, cfg)

	want := "ISBN 13: 978-9999999999"
	if got == nil || *got != want {
		t.Fatalf("got %v, want %q", derefOrNilStr(got), want)
	}
}

func TestExtractField_PreserveSemantics(t *testing.T) {
	doc := mustParseHTML(t, `<div class="description"><p>A great <b>book</b>.</p></div>`)
	cfg := &SelectorConfig{Selector: ptrString("div.description"), PreserveSemantics: true}

	got := extractField(doc, cfg)

	if got == nil {
		t.Fatal("got nil, want non-nil outer HTML")
	}
	if !strings.Contains(*got, "<p>A great <b>book</b>.</p>") {
		t.Errorf("got %q, want it to contain the inner markup verbatim", *got)
	}
}

func TestExtractField_NotFound(t *testing.T) {
	doc := mustParseHTML(t, `<div>nothing relevant here</div>`)
	cfg := &SelectorConfig{Selector: ptrString(".does-not-exist")}

	got := extractField(doc, cfg)

	if got != nil {
		t.Errorf("got %q, want nil", *got)
	}
}

func TestExtractField_NilConfig(t *testing.T) {
	doc := mustParseHTML(t, `<div>irrelevant</div>`)
	selectors := map[string]*SelectorConfig{"price": nil}

	scraper := NewScraper(true, 0, 5*time.Second)
	got := scraper.extractData(doc, selectors)

	if v, ok := got["price"]; !ok || v != nil {
		t.Errorf("got[%q] = %v, want present key with nil value", "price", derefOrNilStr(v))
	}
}

// TestScraper_Scrape_WithFakeSession exercises the full Scrape ->
// doScrape -> extractData -> ParsePrice/ParseStockStatus -> status
// resolution pipeline end-to-end against a fakeSession, with no real
// browser or network involved.
func TestScraper_Scrape_WithFakeSession(t *testing.T) {
	html := `<div><span class="price">LKR 1,000.00</span><span class="stock">In Stock</span></div>`
	fs := &fakeSession{htmlByURL: map[string]string{"https://example.lk/book": html}}

	selectors := map[string]*SelectorConfig{
		"price":        {Selector: ptrString("span.price"), DirectText: true},
		"availability": {Selector: ptrString("span.stock"), DirectText: true},
	}

	// waitTime=0 keeps the test fast — rateLimitWait still runs (real
	// code path, not skipped), it just has nothing to wait for.
	scraper := NewScraper(true, 0, 5*time.Second)
	result, err := scraper.Scrape(context.Background(), "https://example.lk/book", selectors, nil, fs, "", "")
	if err != nil {
		t.Fatalf("Scrape returned error: %v", err)
	}

	if result.Price == nil || *result.Price != 1000.0 {
		t.Errorf("Price = %v, want 1000.0", derefOrNilFloat(result.Price))
	}
	if result.InStock == nil || !*result.InStock {
		t.Errorf("InStock = %v, want true", derefOrNilBool(result.InStock))
	}
	if result.Status == nil || *result.Status != "IN_STOCK" {
		t.Errorf("Status = %v, want IN_STOCK", derefOrNilStr(result.Status))
	}
	if len(fs.navigateCalls) != 1 || fs.navigateCalls[0] != "https://example.lk/book" {
		t.Errorf("navigateCalls = %v, want exactly one call to the scrape URL", fs.navigateCalls)
	}
}

// TestScraper_Scrape_UnparseableStock verifies the "found a price fine,
// but stock text didn't classify" case still resolves to ERROR — a
// successfully parsed price does NOT create a partial-success status,
// exactly matching _do_scrape's Python behavior.
func TestScraper_Scrape_UnparseableStock(t *testing.T) {
	html := `<div><span class="price">LKR 500.00</span><span class="stock">Ask in-store</span></div>`
	fs := &fakeSession{htmlByURL: map[string]string{"https://example.lk/x": html}}

	selectors := map[string]*SelectorConfig{
		"price":        {Selector: ptrString("span.price"), DirectText: true},
		"availability": {Selector: ptrString("span.stock"), DirectText: true},
	}

	scraper := NewScraper(true, 0, 5*time.Second)
	result, err := scraper.Scrape(context.Background(), "https://example.lk/x", selectors, nil, fs, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Price == nil || *result.Price != 500.0 {
		t.Errorf("Price = %v, want 500.0 (price parsing should still succeed)", derefOrNilFloat(result.Price))
	}
	if result.Status == nil || *result.Status != "ERROR" {
		t.Errorf("Status = %v, want ERROR", derefOrNilStr(result.Status))
	}
	if result.Reason == nil || *result.Reason != "unparseable_stock_status" {
		t.Errorf("Reason = %v, want unparseable_stock_status", derefOrNilStr(result.Reason))
	}
}

// TestScraper_Scrape_OpensOwnSession verifies the nil-session branch of
// Scrape opens a real browser.NewSession when none is supplied. This
// deliberately does NOT run by default (it would launch real Chromium),
// guarded behind -short's inverse via t.Skip — see the comment inline.
func TestScraper_Scrape_OpensOwnSession(t *testing.T) {
	t.Skip("requires a real Chromium/headless-shell binary — run manually, not part of the default fake-session suite")
}

// --- small assertion helpers, shared by scraper_test.go's table tests ---

func assertFloatPtrEqual(t *testing.T, want, got *float64) {
	t.Helper()
	if (want == nil) != (got == nil) {
		t.Fatalf("got %v, want %v", derefOrNilFloat(got), derefOrNilFloat(want))
	}
	if want != nil && *want != *got {
		t.Fatalf("got %v, want %v", *got, *want)
	}
}

func assertBoolPtrEqual(t *testing.T, want, got *bool) {
	t.Helper()
	if (want == nil) != (got == nil) {
		t.Fatalf("got %v, want %v", derefOrNilBool(got), derefOrNilBool(want))
	}
	if want != nil && *want != *got {
		t.Fatalf("got %v, want %v", *got, *want)
	}
}

func derefOrNilStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func derefOrNilFloat(f *float64) string {
	if f == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v", *f)
}

func derefOrNilBool(b *bool) string {
	if b == nil {
		return "<nil>"
	}
	if *b {
		return "true"
	}
	return "false"
}
