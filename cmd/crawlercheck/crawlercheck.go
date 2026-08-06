// Command crawlercheck is a throwaway manual verification tool — NOT part
// of the migrated codebase, mirroring cmd/sessioncheck's structure. It
// exercises Crawler.FindProductURL against a real store, either via the
// with-session (cached search_url_template) path or the full two-session
// discovery path, and prints the resulting CrawlResult.
//
// With-session path (a search_url_template is already known/cached):
//
//	go run ./cmd/crawlercheck -store "https://jumpbooks.lk" -template "https://jumpbooks.lk/?s=test" -title "The Hobbit"
//
// Discovery path (no cached template — exercises the full two-sequential-
// isolated-session Cloudflare-avoidance flow, runDiscovery):
//
//	go run ./cmd/crawlercheck -store "https://jumpbooks.lk" -title "The Hobbit" -isbn "9780547928227"
//
// Diagnostic usage (bypasses FindProductURL/runWithSession/runDiscovery's
// real control flow, reimplements just the search->extract->score->
// validate steps inline with verbose output at each stage — mirrors
// cmd/sessioncheck's own -diag mode exactly, same rationale: expose WHERE
// a wrong candidate gets picked, not just the final CrawlResult):
//
//	go run ./cmd/crawlercheck -store "https://jumpbooks.lk" -template "https://jumpbooks.lk/?s=test" -title "The Last Wish" -isbn "9781399611398" -diag
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/domain"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
	"github.com/udanfernando2006/vestige-go/internal/pipeline/heuristics"
)

func main() {
	storeURL := flag.String("store", "", "base URL of the store to test against, e.g. https://jumpbooks.lk (required)")
	template := flag.String("template", "", "cached search_url_template, e.g. https://jumpbooks.lk/?s=test — if set, exercises the with-session (runWithSession) path; if empty (and -diag isn't set), exercises full discovery (runDiscovery)")
	title := flag.String("title", "", "book title to search for (required)")
	isbn := flag.String("isbn", "", "book ISBN, optional — used for candidate scoring/validation boost, and as the search query itself if provided (matches Crawler's own `isbn if isbn else title` query preference)")
	headless := flag.Bool("headless", false, "run headless (default: false, so you can watch it)")
	timeout := flag.Duration("timeout", 30*time.Second, "per-session timeout")
	pause := flag.Duration("pause", 5*time.Second, "how long to leave the browser open after the run (with-session and -diag paths only — see note below)")
	diag := flag.Bool("diag", false, "run the diagnostic search/score/validate probe instead of the real FindProductURL flow")
	flag.Parse()

	if *storeURL == "" {
		log.Fatal("-store is required, e.g. -store https://jumpbooks.lk")
	}
	if *title == "" {
		log.Fatal("-title is required, e.g. -title \"The Hobbit\"")
	}

	if *diag {
		runDiag(*storeURL, *template, *title, *isbn, *headless, *timeout, *pause)
		return
	}
	runNormal(*storeURL, *template, *title, *isbn, *headless, *timeout, *pause)
}

func runNormal(storeURL, template, title, isbn string, headless bool, timeout, pause time.Duration) {
	ctx := context.Background()
	crawler := pipeline.NewCrawler(headless, timeout)

	store := &domain.Store{BaseURL: storeURL}

	var sess browser.Session
	if template != "" {
		store.SearchURLTemplate = &template
		fmt.Println("Path: runWithSession (a -template was provided)")
		fmt.Printf("Opening browser session (headless=%v)...\n", headless)

		s, err := browser.NewSession(ctx, headless, timeout)
		if err != nil {
			log.Fatalf("NewSession failed: %v", err)
		}
		defer func() {
			fmt.Println("Closing session...")
			_ = s.Close(ctx)
		}()
		sess = s
	} else {
		// sess stays nil here — a genuinely nil interface, not a wrapped
		// nil pointer (see Scrape's doc comment on that Go footgun) —
		// which is exactly what routes FindProductURL into runDiscovery.
		// runDiscovery manages its own two sessions' lifecycles
		// internally, so there is nothing here for THIS tool to hold
		// open after the call returns — the -pause sleep below is a
		// no-op for this path since both of runDiscovery's sessions are
		// already closed by the time FindProductURL returns.
		fmt.Println("Path: runDiscovery (no -template given — Crawler will open and close its own two sessions internally)")
		fmt.Println("NOTE: -pause has no visible effect on this path; both sessions close before this tool regains control.")
	}

	fmt.Printf("FindProductURL(store=%s, title=%q, isbn=%q)...\n", storeURL, title, isbn)
	result, err := crawler.FindProductURL(ctx, store, title, isbn, sess)
	if err != nil {
		log.Fatalf("FindProductURL returned an error: %v", err)
	}

	fmt.Println("\n--- RESULT ---")
	fmt.Printf("Success:    %v\n", result.Success)
	printStrPtr("ProductURL", result.ProductURL)
	printStrPtr("Status", result.Status)
	printStrPtr("Error", result.Error)
	if result.Confidence != nil {
		fmt.Printf("Confidence: %v\n", *result.Confidence)
	} else {
		fmt.Println("Confidence: <nil>")
	}
	printStrPtr("SearchURLTemplate", result.SearchURLTemplate)

	if template != "" {
		fmt.Printf("\nLeaving browser open for %s so you can inspect the result...\n", pause)
		time.Sleep(pause)
	}
}

// runDiag reimplements find_product_url's search->extract->score->validate
// pipeline inline, with verbose output at every stage — mirroring
// cmd/sessioncheck's own -diag mode. Now matches production isolation and
// control flow on three axes it previously simplified away or got wrong:
// (1) template discovery (when no -template is given) uses its own
// dedicated session, fully closed before the search session opens —
// mirroring runDiscovery's real discoverySess/searchSess split — rather
// than reusing one session across both steps; (2) each candidate is
// validated via its own brand-new top-level browser.NewSession (a
// genuinely new Chromium process), NOT session.FreshContext — mirroring
// crawler.go's tryValidateCandidate. FreshContext was tried first and
// found insufficient: a new tab forked via FreshContext shares its parent
// process's cookie jar and browser-level fingerprint, both of which
// Cloudflare's detection keys on, so a same-process "fresh tab" carried
// the same reputation as the tab before it — see crawler.go's
// tryValidateCandidate/validateCandidates doc comments for the full
// account; (3) the single-result-redirect check now actually calls
// heuristics.ScoreValidation and short-circuits (returning immediately,
// matching runDiscovery/checkForSingleResultRedirect's real behavior)
// when the redirected-to page validates — a real bug, not just a missing
// nicety: the previous version only PRINTED a warning describing what a
// redirect might mean, then unconditionally fell through to candidate
// extraction regardless of whether the redirect target actually
// validated, meaning a genuine single-result redirect (e.g. an ISBN
// search landing directly on the correct product page) never
// short-circuited here even when it would have in real production code —
// candidates were extracted from the product page's own links every time,
// even when that page WAS the answer. The only remaining simplification
// versus real runDiscovery: this tool still walks discovery -> search ->
// validate sequentially inline for verbose per-stage output, rather than
// calling FindProductURL directly (that's runNormal's job) — but every
// session boundary and control-flow branch now matches production
// exactly.
func runDiag(storeURL, template, title, isbn string, headless bool, timeout, pause time.Duration) {
	ctx := context.Background()

	searchTemplate := template
	if searchTemplate == "" {
		fmt.Printf("No -template given — discovering one first (own isolated session, mirrors runDiscovery's discoverySess step exactly, closed before the search session opens)...\n")
		fmt.Printf("Opening discovery session (headless=%v)...\n", headless)
		discoverySess, err := browser.NewSession(ctx, headless, timeout)
		if err != nil {
			log.Fatalf("NewSession (discovery) failed: %v", err)
		}

		if err := discoverySess.Navigate(ctx, storeURL); err != nil {
			_ = discoverySess.Close(ctx)
			log.Fatalf("Navigate(base_url) failed: %v", err)
		}
		filled, err := discoverySess.FindAndFillSearch(ctx, "test")
		if err != nil {
			_ = discoverySess.Close(ctx)
			log.Fatalf("FindAndFillSearch failed: %v", err)
		}
		if !filled {
			_ = discoverySess.Close(ctx)
			log.Fatal("FindAndFillSearch returned false — no search form found, can't proceed with diagnostics")
		}
		discovered, err := discoverySess.GetURL(ctx)
		if err != nil {
			_ = discoverySess.Close(ctx)
			log.Fatalf("GetURL after search-pattern discovery failed: %v", err)
		}
		searchTemplate = discovered
		fmt.Printf("Discovered template: %s\n", searchTemplate)

		fmt.Println("Closing discovery session before opening the search session (isolation matches runDiscovery)...")
		_ = discoverySess.Close(ctx)
	}

	fmt.Printf("Opening search session (headless=%v)...\n", headless)
	sess, err := browser.NewSession(ctx, headless, timeout)
	if err != nil {
		log.Fatalf("NewSession (search) failed: %v", err)
	}

	query := isbn
	if query == "" {
		query = title
	}
	searchURL := pipeline.BuildSearchURL(searchTemplate, query)
	fmt.Printf("\nIntended search URL (query=%q): %s\n", query, searchURL)

	if err := sess.Navigate(ctx, searchURL); err != nil {
		log.Fatalf("Navigate(searchURL) failed: %v", err)
	}

	actualURL, _ := sess.GetURL(ctx)
	fmt.Printf("Actual URL after navigation:      %s\n", actualURL)

	htmlStr, err := sess.GetHTML(ctx)
	if err != nil {
		_ = sess.Close(ctx)
		log.Fatalf("GetHTML failed: %v", err)
	}
	fmt.Printf("\nActual page text (first 400 chars, whitespace-collapsed):\n  %s\n", textSnippet(htmlStr, 400))

	if actualURL != searchURL {
		fmt.Println("\n*** REDIRECT DETECTED ***")
		fmt.Println("The store did NOT keep us on the constructed search URL — it took us")
		fmt.Println("somewhere else. Checking whether this is a genuine single-result redirect")
		fmt.Println("(the same heuristics.ScoreValidation check crawler.go's real")
		fmt.Println("checkForSingleResultRedirect runs) before deciding whether to treat this")
		fmt.Println("page as a listing to mine for candidates.")

		validation, verr := heuristics.ScoreValidation(htmlStr, actualURL, title, isbn)
		if verr != nil {
			fmt.Printf("    ScoreValidation error: %v — falling through to normal candidate extraction, matching checkForSingleResultRedirect's own error-handling.\n", verr)
		} else {
			fmt.Printf("    ValidationScore=%d Valid=%v Findings=%+v\n", validation.ValidationScore, validation.Valid, validation.Findings)
			if validation.Valid {
				fmt.Println("\n*** Redirect landed on a page that validates directly — this is exactly")
				fmt.Println("the case checkForSingleResultRedirect exists to short-circuit on. Real")
				fmt.Println("production code (runDiscovery) would STOP here and return this page as")
				fmt.Println("the result, WITHOUT ever extracting/scoring candidates from it. ***")
				_ = sess.Close(ctx)
				fmt.Printf("\nWinning URL: %s\n", actualURL)
				return
			}
			fmt.Println("\nRedirected-to page did NOT validate — falling through to normal candidate")
			fmt.Println("extraction against this same already-fetched HTML, matching")
			fmt.Println("checkForSingleResultRedirect's documented safety-net behavior.")
		}
	}

	candidates := extractLinksDiag(htmlStr)
	fmt.Printf("\nExtracted %d raw <a href> link(s) from this page.\n", len(candidates))

	scored := heuristics.ScoreCandidates(candidates, isbn, title, storeURL)
	fmt.Printf("\n%d candidate(s) survived scoring (title=%q isbn=%q) — sorted, top 3 marked '->' (what validateCandidates actually tries):\n\n", len(scored), title, isbn)
	for i, c := range scored {
		marker := "  "
		if i < 3 {
			marker = "->"
		}
		fmt.Printf("%s [score %3d] %-70s (text: %q)\n", marker, c.MatchScore, c.URL, c.Text)
	}

	// Search session's job is done — everything from here only needs the
	// already-captured htmlStr/scored candidates, never the live tab.
	// Closed explicitly here (not deferred) so it's gone BEFORE candidate
	// validation opens its own sessions below — matches runDiscovery's
	// searchSess.Close() timing exactly, and avoids the original bandwidth-
	// contention bug this tool was built to help diagnose (an idle search
	// tab competing for bandwidth/RAM with validation's own sessions on a
	// slow connection).
	fmt.Println("\nClosing search session (no longer needed — candidates capture their own sessions below)...")
	_ = sess.Close(ctx)

	if len(scored) == 0 {
		fmt.Println("\nNo candidates survived scoring — this would resolve to NOT_LISTED. Nothing further to validate.")
		fmt.Println("(The text snippet printed above is the ONLY page we ever saw — if it doesn't look like a real listing or product page, whatever's shown there IS the actual blocker, not the scoring logic.)")
		return
	}

	fmt.Println("\n--- Validating top candidates (what Crawler.validateCandidates actually does, depth=3) ---")
	fmt.Println("Each candidate below now opens its own brand-new top-level session")
	fmt.Println("(browser.NewSession — a genuinely new Chromium process), matching")
	fmt.Println("crawler.go's current tryValidateCandidate. An earlier version of this tool")
	fmt.Println("(and of tryValidateCandidate itself) used session.FreshContext — a new tab on")
	fmt.Println("the SAME process — which turned out to be insufficient: tabs share their")
	fmt.Println("parent process's cookie jar and browser-level fingerprint, both of which")
	fmt.Println("Cloudflare's detection keys on, so a same-process 'fresh tab' carried the same")
	fmt.Println("reputation as the tab before it.")

	limit := 3
	if limit > len(scored) {
		limit = len(scored)
	}
	var winnerSess browser.Session // kept open (not closed in the loop) only if a candidate validates successfully, so -pause has something real to show
	for i, c := range scored[:limit] {
		fmt.Printf("\n[%d] Opening new session + Navigate to %s ...\n", i+1, c.URL)

		fresh, err := browser.NewSession(ctx, headless, timeout)
		if err != nil {
			fmt.Printf("    NewSession error: %v  (validateCandidates would silently skip to the next candidate here)\n", err)
			continue
		}

		if err := fresh.Navigate(ctx, c.URL); err != nil {
			fmt.Printf("    Navigate error: %v  (validateCandidates would silently skip to the next candidate here)\n", err)
			_ = fresh.Close(ctx)
			continue
		}
		candHTML, err := fresh.GetHTML(ctx)
		if err != nil {
			fmt.Printf("    GetHTML error: %v  (validateCandidates would silently skip to the next candidate here)\n", err)
			_ = fresh.Close(ctx)
			continue
		}
		fmt.Printf("    Actual page text (first 400 chars, whitespace-collapsed):\n      %s\n", textSnippet(candHTML, 400))
		validation, err := heuristics.ScoreValidation(candHTML, c.URL, title, isbn)
		if err != nil {
			fmt.Printf("    ScoreValidation error: %v  (validateCandidates would silently skip to the next candidate here)\n", err)
			_ = fresh.Close(ctx)
			continue
		}
		fmt.Printf("    ValidationScore=%d Valid=%v Findings=%+v\n", validation.ValidationScore, validation.Valid, validation.Findings)
		if validation.Valid {
			fmt.Println("    *** This candidate would be SELECTED (first Valid=true wins, loop stops here) ***")
			winnerSess = fresh // deliberately NOT closed here — left open for the -pause inspection window below
			break
		}
		_ = fresh.Close(ctx)
	}

	if winnerSess != nil {
		fmt.Printf("\nLeaving the winning candidate's browser open for %s so you can inspect it...\n", pause)
		time.Sleep(pause)
		fmt.Println("Closing winning candidate's session...")
		_ = winnerSess.Close(ctx)
	} else {
		fmt.Println("\nNo candidate validated successfully — every session (search + every candidate tried) is already closed. Nothing left open to inspect.")
	}
}

// textSnippet renders the page's visible text (goquery's Text(), same
// extraction ScoreValidation itself uses internally) with all whitespace
// runs collapsed to single spaces, truncated to maxLen — for genuinely
// SEEING what a page contains rather than inferring it from a fragile
// keyword-marker match. Returns a bracketed error message inline (not a
// separate error return) since this is diagnostic-only output where a
// parse failure is itself informative, not fatal.
func textSnippet(htmlStr string, maxLen int) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return fmt.Sprintf("[failed to parse HTML for snippet: %v]", err)
	}
	text := strings.Join(strings.Fields(doc.Text()), " ")
	if len(text) > maxLen {
		return text[:maxLen] + "..."
	}
	if text == "" {
		return "[page text is empty]"
	}
	return text
}

// extractLinksDiag is a simplified, diagnostic-only duplicate of
// pipeline's unexported extractCandidateLinks (can't be called directly
// from this package — internal/pipeline doesn't export it, deliberately,
// per the file-first/no-unnecessary-exports convention). Text extraction
// here uses a plain TrimSpace rather than production's bs4GetTextStrip
// approximation — fine for diagnostic eyeballing, but NOT a byte-for-byte
// stand-in for what the real crawler extracts; only Href values (which
// this diagnosis actually hinges on) are guaranteed identical.
func extractLinksDiag(htmlStr string) []heuristics.LinkCandidate {
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
		if !exists {
			return
		}
		candidates = append(candidates, heuristics.LinkCandidate{Href: href, Text: strings.TrimSpace(sel.Text())})
	})
	return candidates
}

func printStrPtr(label string, s *string) {
	if s == nil {
		fmt.Printf("%-18s<nil>\n", label+":")
		return
	}
	fmt.Printf("%-18s%q\n", label+":", *s)
}
