package heuristics

import (
	"net/url"
	"sort"
	"strings"
)

// ProductPathPatterns are the URL path fragments that reliably indicate a
// product detail page across the stores this project targets (Sarasavi,
// Jumpbooks, Books.lk, Jeyabookcentre, MDGunasena, BooxWorm). /products/
// (plural) was added in v3.10 after a Shopify store's /collections/
// listing page outranked its own /products/ detail page — see the archive
// catalog entry for the incident this pattern list was tuned against.
var ProductPathPatterns = []string{
	"/product/",
	"/products/",
	"/item/",
	"/p/",
	"/book/",
	"/bookdetail",
}

var socialDomains = []string{
	"facebook.com",
	"twitter.com",
	"linkedin.com",
	"pinterest.com",
	"telegram.me",
	"instagram.com",
}

var excludedPathPatterns = []string{
	"/product-tag/",
	"/tag/",
	"/category/",
	"search",
	"/page/",
}

// LinkCandidate is the raw (href, text) pair extracted from a search
// results page. Extraction itself (HTML parsing) happens in crawler.go,
// not here — this package only scores already-extracted links.
type LinkCandidate struct {
	Href string
	Text string
}

// ScoredCandidate is a LinkCandidate augmented with its resolved absolute
// URL and match score, direct port of the mutated dict shape
// crawler.py's _score_candidates() returns.
type ScoredCandidate struct {
	Href       string
	Text       string
	URL        string
	MatchScore int
}

// ScoreCandidates is a direct port of Crawler._score_candidates().
//
// Scores and filters candidate links by matching title keywords in
// href/text, prioritizing actual product-detail pages over
// filter/navigation/listing links, and normalizes relative hrefs to
// absolute URLs against baseURL. Returns candidates sorted by match score
// descending, ties broken by original input order (stable sort, matching
// Python's list.sort() stability).
func ScoreCandidates(links []LinkCandidate, isbn, title, baseURL string) []ScoredCandidate {
	titleLower := strings.ToLower(title)
	keywords := strings.Fields(titleLower)

	scored := make([]ScoredCandidate, 0, len(links))

	for _, link := range links {
		hrefLower := strings.ToLower(link.Href)
		textLower := strings.ToLower(link.Text)

		keywordMatches := 0
		anyKeywordInText := false
		for _, kw := range keywords {
			inHref := strings.Contains(hrefLower, kw)
			inText := strings.Contains(textLower, kw)
			if inHref || inText {
				keywordMatches++
			}
			if inText {
				anyKeywordInText = true
			}
		}

		// Must have at least 1 keyword in text OR 2+ total matches.
		if keywordMatches == 0 {
			continue
		}
		if keywordMatches == 1 && !anyKeywordInText {
			continue
		}

		// EXCLUDE: social media links.
		if containsAny(hrefLower, socialDomains) {
			continue
		}

		// EXCLUDE: tag/category/pagination/search pages (not product pages).
		if containsAny(hrefLower, excludedPathPatterns) {
			continue
		}

		// Normalize to absolute URL.
		absoluteURL, ok := resolveURL(baseURL, link.Href)
		if !ok {
			continue
		}

		// EXCLUDE: pseudo-links (javascript:void(0), mailto:, tel:) —
		// anything that didn't resolve to an http(s) URL.
		if !strings.HasPrefix(absoluteURL, "http") {
			continue
		}

		score := keywordMatches

		// +10: likely a product detail page (see ProductPathPatterns doc).
		switch {
		case containsAny(hrefLower, ProductPathPatterns):
			score += 10
		// -8: Shopify collection pages are listings, not product detail pages.
		case strings.Contains(hrefLower, "/collections/"):
			score -= 8
		// -5: likely just a filter/navigation link (pure query params, no
		// path change, and not already caught by the product-path check
		// above).
		case strings.Contains(link.Href, "?") && !containsAny(hrefLower, ProductPathPatterns):
			score -= 5
		}

		// +5: ISBN present in the URL, if one was provided.
		if isbn != "" && strings.Contains(hrefLower, isbn) {
			score += 5
		}

		scored = append(scored, ScoredCandidate{
			Href:       link.Href,
			Text:       link.Text,
			URL:        absoluteURL,
			MatchScore: score,
		})
	}

	stableSortByScoreDesc(scored)
	return scored
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// resolveURL mirrors Python's urllib.parse.urljoin(base_url, href).
// Returns ok=false if either URL fails to parse.
//
// See top-of-response note: this hasn't been tested against real captured
// hrefs from the target stores (protocol-relative //cdn... links,
// fragment-only hrefs, etc.) — worth a table-driven test against
// actual scraped data before trusting it in place of urljoin.
func resolveURL(base, href string) (string, bool) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	refURL, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	return baseURL.ResolveReference(refURL).String(), true
}

// stableSortByScoreDesc sorts in place by MatchScore descending, preserving
// relative order of equal-score elements (matches Python's stable
// list.sort(key=..., reverse=True)).
func stableSortByScoreDesc(scored []ScoredCandidate) {
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].MatchScore > scored[j].MatchScore
	})
}
