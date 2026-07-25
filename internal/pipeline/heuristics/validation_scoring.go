package heuristics

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var availabilityKeywords = []string{
	"available",
	"in stock",
	"shipping",
	"stock",
	"qty",
	"quantity",
}

var cartPagePhrases = []string{
	"items in cart",
	"cart subtotal",
	"your cart is empty",
	"checkout now",
}

// errorPagePhrases: narrowed from the original ["not found", "404", "page
// not found"] after live testing against jumpbooks.lk found this exact
// list producing false positives on genuinely valid, correctly-matching
// product pages (TitleFound/HasAvailability/HasContent all true, yet
// IsErrorPage still fired, forcing Valid=false). Bare "404" and bare "not
// found" are both far too broad for a whole-page substring scan on a real
// WordPress/WooCommerce storefront — either can appear in unrelated
// boilerplate (analytics/tracking config, a hidden AJAX-search empty-state
// template, a numeric asset id, an og:description fallback) that's present
// in the page's full text without the page actually being an error page.
// Narrowed to phrases specific enough that a real collision is unlikely;
// "404" alone and the generic "not found" fragment are deliberately
// dropped, keeping only "page not found" (a phrase overwhelmingly used as
// an actual page heading, not template boilerplate) plus a couple of
// similarly-specific additions for common CMS 404-page copy.
var errorPagePhrases = []string{
	"page not found",
	"error 404",
	"we can't find the page",
	"the page you are looking for",
}

// ValidationFindings mirrors the `findings` dict Crawler._validate_from_html()
// returns alongside the score — kept as named booleans rather than a map so
// callers get compile-time field checking.
type ValidationFindings struct {
	TitleFound      bool
	ISBNFound       bool
	HasAvailability bool
	HasContent      bool
	IsCartPage      bool
	IsErrorPage     bool
}

// ValidationResult is a direct port of _validate_from_html()'s return dict.
type ValidationResult struct {
	URL             string
	ValidationScore int
	Valid           bool
	Findings        ValidationFindings
}

// ScoreValidation is a direct port of Crawler._validate_from_html().
//
// Scores a fetched candidate page (not a link under consideration — see
// ScoreCandidates for that) on title/ISBN presence, availability-keyword
// presence, general content volume, and cart/error-page penalties.
//
// html is the full page HTML of the already-navigated-to candidate URL;
// pageURL is that candidate's URL (used only for the /cart /shopping path
// check, not re-fetched here — this function does no I/O of its own).
func ScoreValidation(html, pageURL, title, isbn string) (ValidationResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return ValidationResult{}, err
	}

	text := strings.ToLower(doc.Text())
	titleLower := strings.ToLower(title)
	urlLower := strings.ToLower(pageURL)

	score := 0
	var findings ValidationFindings

	findings.TitleFound = strings.Contains(text, titleLower)
	if findings.TitleFound {
		score += 3
	}

	findings.ISBNFound = isbn != "" && strings.Contains(text, isbn)
	if findings.ISBNFound {
		score += 2
	}

	findings.HasAvailability = containsAny(text, availabilityKeywords)
	if findings.HasAvailability {
		score += 2
	}

	// has_content: len(soup.find_all(["p","div","span","article"])) > 10
	findings.HasContent = doc.Find("p, div, span, article").Length() > 10
	if findings.HasContent {
		score += 2
	}

	findings.IsCartPage = strings.Contains(urlLower, "/cart") ||
		strings.Contains(urlLower, "/shopping") ||
		containsAny(text, cartPagePhrases)
	if findings.IsCartPage {
		score -= 5
	}

	// IsErrorPage is now a heavy penalty (-4), not an absolute override.
	// Previously this set score=-99 outright regardless of every other
	// signal — meaning one narrow phrase collision (see errorPagePhrases'
	// comment above) could veto an otherwise-clear match even with
	// TitleFound+ISBNFound+HasAvailability+HasContent all true. -4 was
	// chosen (not a larger penalty like -8) by modeling both ends: a
	// genuine error page typically has no title/ISBN/availability match
	// (score ~2 from HasContent alone) and -4 still fails it decisively;
	// a real, correctly-matching candidate with title+availability+
	// content present (score ~7) survives a -4 penalty and still clears
	// the >=3 Valid threshold, whereas the original -99 override — and
	// even a softer -8 — would still incorrectly fail it. This penalty is
	// deliberately defense-in-depth alongside the narrowed phrase list
	// above, not the primary fix on its own: the phrase narrowing should
	// stop most false positives from matching in the first place.
	findings.IsErrorPage = containsAny(text, errorPagePhrases)
	if findings.IsErrorPage {
		score -= 4
	}

	finalScore := score
	if finalScore < 0 {
		finalScore = 0
	}

	return ValidationResult{
		URL:             pageURL,
		ValidationScore: finalScore,
		Valid:           score >= 3,
		Findings:        findings,
	}, nil
}