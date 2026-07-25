// Command scrapercheck is a throwaway manual verification tool — NOT part
// of the migrated codebase, mirroring cmd/sessioncheck's structure. It
// exercises Scraper.Scrape against a real product page and prints the
// resulting AvailabilityResult.
//
// Simple usage (plain CSS selectors, direct_text assumed for both):
//
//	go run ./cmd/scrapercheck -url "https://jumpbooks.lk/products/the-hobbit" -price-selector "span.price" -stock-selector "span.stock-status"
//
// Full selector-config usage (find_by_text/then_next/preserve_semantics —
// the shape real LLM-discovered selectors or a hand-authored config would
// use):
//
//	go run ./cmd/scrapercheck -url "https://example.lk/product/1" -selectors-json "{\"price\":{\"selector\":\"span.price\",\"direct_text\":true},\"availability\":{\"find_by_text\":[\"span\",\"Stock\"],\"then_next\":\"td\"}}"
//
// Custom stock-pattern usage (CUSTOM_STOCK_IN_PATTERNS/CUSTOM_STOCK_OUT_PATTERNS equivalents):
//
//	go run ./cmd/scrapercheck -url "..." -stock-selector "..." -custom-in "arriving soon" -custom-out "temporarily unavailable"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
)

func main() {
	url := flag.String("url", "", "product page URL to scrape (required)")
	priceSelector := flag.String("price-selector", "", "CSS selector for the price field (direct_text assumed) — ignored if -selectors-json is set")
	stockSelector := flag.String("stock-selector", "", "CSS selector for the availability/stock field (direct_text assumed) — ignored if -selectors-json is set")
	selectorsJSON := flag.String("selectors-json", "", "full selectors map as JSON (map[string]*pipeline.SelectorConfig) — overrides -price-selector/-stock-selector; see file header comment for an example")
	waitSelectors := flag.String("wait-selectors", "", "comma-separated CSS selectors to wait for before extracting (optional)")
	customIn := flag.String("custom-in", "", "comma-separated custom in-stock regex patterns (CUSTOM_STOCK_IN_PATTERNS)")
	customOut := flag.String("custom-out", "", "comma-separated custom out-of-stock regex patterns (CUSTOM_STOCK_OUT_PATTERNS)")
	headless := flag.Bool("headless", false, "run headless (default: false, so you can watch it)")
	timeout := flag.Duration("timeout", 30*time.Second, "per-operation timeout")
	waitTime := flag.Duration("wait-time", 2*time.Second, "rate-limit wait before navigating — Scraper's own throttle; kept short here deliberately, NOT the production 5s default")
	pause := flag.Duration("pause", 5*time.Second, "how long to leave the browser open after the run")
	flag.Parse()

	if *url == "" {
		log.Fatal("-url is required, e.g. -url https://jumpbooks.lk/products/the-hobbit")
	}

	selectors, err := buildSelectors(*selectorsJSON, *priceSelector, *stockSelector)
	if err != nil {
		log.Fatal(err)
	}

	var waitSels []string
	if *waitSelectors != "" {
		for _, s := range strings.Split(*waitSelectors, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				waitSels = append(waitSels, trimmed)
			}
		}
	}

	fmt.Printf("Selectors:\n%s\n", dumpSelectors(selectors))

	ctx := context.Background()
	fmt.Printf("Opening browser session (headless=%v)...\n", *headless)
	sess, err := browser.NewSession(ctx, *headless, *timeout)
	if err != nil {
		log.Fatalf("NewSession failed: %v", err)
	}
	defer func() {
		fmt.Println("Closing session...")
		_ = sess.Close(ctx)
	}()

	fmt.Printf("Scraping %s...\n", *url)
	scraper := pipeline.NewScraper(*headless, *waitTime, *timeout)
	result, err := scraper.Scrape(ctx, *url, selectors, waitSels, sess, *customIn, *customOut)
	if err != nil {
		log.Fatalf("Scrape returned an error: %v", err)
	}

	fmt.Println("\n--- RESULT ---")
	printStrPtr("RawPriceText", result.RawPriceText)
	printStrPtr("RawStockText", result.RawStockText)
	if result.Price != nil {
		fmt.Printf("%-14s%v\n", "Price:", *result.Price)
	} else {
		fmt.Printf("%-14s<nil>\n", "Price:")
	}
	if result.InStock != nil {
		fmt.Printf("%-14s%v\n", "InStock:", *result.InStock)
	} else {
		fmt.Printf("%-14s<nil>\n", "InStock:")
	}
	printStrPtr("Status", result.Status)
	printStrPtr("Reason", result.Reason)
	if result.ScrapedAt != nil {
		fmt.Printf("%-14s%s\n", "ScrapedAt:", result.ScrapedAt.Format(time.RFC3339))
	}

	fmt.Printf("\nLeaving browser open for %s so you can inspect the result...\n", *pause)
	time.Sleep(*pause)
}

// buildSelectors resolves the -selectors-json flag if given (full
// map[string]*pipeline.SelectorConfig shape — find_by_text,
// preserve_semantics, etc.), otherwise builds a simple two-field map from
// -price-selector/-stock-selector, each defaulting to a plain CSS
// selector with direct_text: true (the common case).
func buildSelectors(selectorsJSON, priceSelector, stockSelector string) (map[string]*pipeline.SelectorConfig, error) {
	if selectorsJSON != "" {
		selectors := map[string]*pipeline.SelectorConfig{}
		if err := json.Unmarshal([]byte(selectorsJSON), &selectors); err != nil {
			return nil, fmt.Errorf("failed to parse -selectors-json: %w", err)
		}
		return selectors, nil
	}

	if priceSelector == "" && stockSelector == "" {
		return nil, fmt.Errorf("provide -price-selector/-stock-selector, or -selectors-json for the full config shape (find_by_text, preserve_semantics, etc.) — see the file header comment for examples")
	}

	selectors := map[string]*pipeline.SelectorConfig{}
	if priceSelector != "" {
		sel := priceSelector
		selectors["price"] = &pipeline.SelectorConfig{Selector: &sel, DirectText: true}
	}
	if stockSelector != "" {
		sel := stockSelector
		selectors["availability"] = &pipeline.SelectorConfig{Selector: &sel, DirectText: true}
	}
	return selectors, nil
}

func dumpSelectors(selectors map[string]*pipeline.SelectorConfig) string {
	b, err := json.MarshalIndent(selectors, "  ", "  ")
	if err != nil {
		return fmt.Sprintf("  %+v", selectors)
	}
	return "  " + string(b)
}

func printStrPtr(label string, s *string) {
	if s == nil {
		fmt.Printf("%-14s<nil>\n", label+":")
		return
	}
	fmt.Printf("%-14s%q\n", label+":", *s)
}
