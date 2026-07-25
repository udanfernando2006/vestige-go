// Command extractorcheck is a live diagnostic tool for internal/pipeline's
// Extractor — mirrors the existing cmd/sessioncheck / cmd/crawlercheck /
// cmd/scrapercheck pattern (per vestige_go_pipeline_implementation.md's
// testing conventions): a real invocation against real input, not a unit
// test, so CleanHTML/ExtractDetails/ExtractSelectors/ClassifyStockStatus
// can be checked against actual markup and a real LLM backend before
// being treated as verified the way scraper.go's three fixtures were.
//
// NOTE ON PROVENANCE: this file's flag shape (-fixture/-url,
// -price-selector-style naming, a -diag dump mode) is INFERRED from this
// project's memory-recorded description of cmd/scrapercheck's
// conventions, not copied from that file's real source — it was not
// available to verify against at the time this was written. If the real
// cmd/scrapercheck.go has a different flag naming convention, prefer
// matching that file directly over what's guessed here; flagging this
// explicitly rather than silently presenting it as a confirmed match.
//
// Two independent modes, selectable via -mode:
//   - "details"   -> Extractor.ExtractDetails (Path D direct extraction)
//   - "selectors" -> Extractor.ExtractSelectors (selector discovery)
//   - "stock"     -> Extractor.ClassifyStockStatus (short-text fallback;
//     -text required instead of -fixture/-url)
//
// Input is either a local file (-fixture, read via file://-equivalent
// local path, no actual file:// URL scheme needed since this reads the
// filesystem directly) or a live URL (-url, fetched fresh via net/http)
// — never both. This lets the same binary run the fixture-first-then-live
// escalation this project's own testing discipline follows (scraper.go's
// hand-simulate -> fixture -> live sequence).
//
// PowerShell note carried over from the recorded cmd/scrapercheck gotcha:
// any -selectors-json-equivalent JSON flag value must be single-quoted in
// PowerShell (double-quoted JSON with escaped \" throws a JSON parse
// error from PowerShell's own quoting, not a Go bug). This tool doesn't
// currently take a raw JSON flag, so this note is precautionary only —
// keeping it here in case -fields (details mode) or a future flag needs
// JSON input later.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/udanfernando2006/vestige-go/internal/browser"
	"github.com/udanfernando2006/vestige-go/internal/llm"
	"github.com/udanfernando2006/vestige-go/internal/pipeline"
)

// envFile is the .env-style file this tool loads credentials from,
// relative to the working directory `go run`/the built binary is invoked
// from (i.e. vestige-go's repo root, if run the normal `go run
// ./cmd/extractorcheck` way from there). Named .env.test deliberately —
// NOT .env.test.go or .env — since a .go suffix would make the Go
// toolchain try to compile it as source, and a bare .env risks collision
// with any future Go-side .env convention that isn't this diagnostic
// tool's concern. Loading is best-effort: if the file doesn't exist,
// godotenv.Load returns an error that is intentionally NOT fatal here —
// falls through to whatever's already in the real environment (option 1
// from the earlier conversation, exported shell vars), so both loading
// styles keep working side by side rather than one disabling the other.
const envFile = ".env.test"

func main() {
	if err := godotenv.Load(envFile); err != nil {
		fmt.Fprintf(os.Stderr, "extractorcheck: note: could not load %s (%v) — falling back to real environment variables only\n", envFile, err)
	}

	var (
		fixture     = flag.String("fixture", "", "path to a local HTML fixture file (mutually exclusive with -url)")
		url         = flag.String("url", "", "live URL to fetch fresh HTML from (mutually exclusive with -fixture)")
		title       = flag.String("title", "", "target book title, passed through to the LLM prompt (required for details/selectors modes)")
		mode        = flag.String("mode", "details", `which Extractor method to exercise: "details", "selectors", or "stock"`)
		text        = flag.String("text", "", `raw stock-status text to classify (only used in -mode=stock, e.g. "Low stock: 3 left")`)
		engine      = flag.String("engine", "stripped", `CleanHTML engine: "stripped" (Path D) or "full" (selector discovery) — ignored in -mode=stock`)
		apiBaseFlag = flag.String("api-base", "", "override API base URL (defaults to SELECTOR_API_BASE or DIRECT_API_BASE env var depending on -mode)")
		apiKeyFlag  = flag.String("api-key", "", "override API key (defaults to SELECTOR_API_KEY or DIRECT_API_KEY env var depending on -mode)")
		modelFlag   = flag.String("model", "", "override model name (defaults to SELECTOR_MODEL or DIRECT_MODEL env var depending on -mode)")
		diag        = flag.Bool("diag", false, "dump the cleaned HTML actually sent to the LLM, plus the raw prompt, before showing the result")
		validate    = flag.Bool("validate", false, "mode=selectors only: after ExtractSelectors returns, run the resulting selectors through the REAL Scraper.Scrape against -url and report per-field pass/fail. Requires -url (a live browser navigation) — NOT usable with -fixture, since there is no URL to navigate to. This deliberately does NOT reimplement discover_selectors.py's separate validate_selectors() re-fetch-and-check routine; it calls the actual production Scraper instead, which checks something stronger than \"got a non-empty string\" — it checks \"this is what scraper.go's real extractData will produce at runtime.\"")
		waitTimeF   = flag.Duration("wait-time", 5*time.Second, "-validate only: Scraper's wait_time / rate-limit-wait config, passed to browser.NewSession")
		scrapeTOF   = flag.Duration("scrape-timeout", 60*time.Second, "-validate only: Scraper's per-navigation timeout")
		headlessF   = flag.Bool("headless", true, "-validate only: run the validation browser session headless")
		timeout     = flag.Duration("timeout", 90*time.Second, "overall request timeout")
	)
	flag.Parse()

	if *fixture == "" && *url == "" && *mode != "stock" {
		fatalUsage("either -fixture or -url is required for -mode=details|selectors")
	}
	if *fixture != "" && *url != "" {
		fatalUsage("-fixture and -url are mutually exclusive, pass only one")
	}
	if *mode == "stock" && *text == "" {
		fatalUsage("-text is required for -mode=stock")
	}
	if *mode != "stock" && *title == "" {
		fatalUsage("-title is required for -mode=details|selectors (the LLM prompt is title-anchored)")
	}
	if *validate && *mode != "selectors" {
		fatalUsage("-validate only applies to -mode=selectors")
	}
	// CORRECTED from an earlier wrong assumption: Scraper.Scrape does NOT
	// require a network URL — browser.Session.Navigate accepts any URL a
	// real Chrome instance can open, and file:// URLs work natively
	// (confirmed against this project's own cmd/scrapercheck usage, which
	// already navigates chromedp to file://... fixture paths successfully).
	// So -validate works with EITHER -fixture or -url — -fixture is
	// converted to a file:// URL below before being handed to Scrape. The
	// only real requirement is that -fixture/-url was given at all, which
	// the existing mutually-exclusive check above already enforces.

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Role-based credential resolution mirrors this project's SELECTOR_*
	// vs DIRECT_* convention: selectors mode uses SELECTOR_*, details and
	// stock-classification default to DIRECT_* (matching
	// classify_stock_status's real "DIRECT_* first" preference documented
	// in llm_extractor.py / vestige_guide.md Section 5), with explicit
	// -api-base/-api-key/-model flags always taking precedence over env
	// vars, so a single fixture can be pointed at a different backend
	// without touching .env.
	envPrefix := "DIRECT_"
	if *mode == "selectors" {
		envPrefix = "SELECTOR_"
	}
	apiBase := firstNonEmpty(*apiBaseFlag, os.Getenv(envPrefix+"API_BASE"))
	apiKey := firstNonEmpty(*apiKeyFlag, os.Getenv(envPrefix+"API_KEY"))
	model := firstNonEmpty(*modelFlag, os.Getenv(envPrefix+"MODEL"))

	if apiBase == "" || model == "" {
		fatalUsage(fmt.Sprintf(
			"no API base/model resolved — set %sAPI_BASE/%sMODEL in your .env, or pass -api-base/-model explicitly",
			envPrefix, envPrefix,
		))
	}

	client, err := llm.NewClient(apiBase, apiKey, *timeout)
	if err != nil {
		fatal("llm.NewClient failed: %v", err)
	}

	eng := pipeline.EngineStripped
	if *engine == "full" {
		eng = pipeline.EngineFull
	}

	extractor, err := pipeline.NewExtractor(pipeline.ExtractorConfig{
		Engine:    eng,
		APIBase:   apiBase,
		APIKey:    apiKey,
		ModelName: model,
	}, client)
	if err != nil {
		fatal("pipeline.NewExtractor failed: %v", err)
	}

	if *mode == "stock" {
		runStockMode(ctx, extractor, *text, *diag)
		return
	}

	rawHTML, source, err := loadInput(ctx, *fixture, *url)
	if err != nil {
		fatal("failed to load input: %v", err)
	}
	fmt.Printf("=== Source: %s (%d bytes raw) ===\n", source, len(rawHTML))

	cleaned, err := extractor.CleanHTML(rawHTML)
	if err != nil {
		fatal("CleanHTML failed: %v", err)
	}

	if *diag {
		fmt.Printf("\n--- Cleaned HTML sent to LLM (engine=%s, %d bytes) ---\n%s\n--- end cleaned HTML ---\n\n",
			*engine, len(cleaned), cleaned)
	} else {
		fmt.Printf("Cleaned HTML: %d bytes (raw was %d bytes, %.1f%% reduction). Pass -diag to see the full body.\n",
			len(cleaned), len(rawHTML), 100*(1-float64(len(cleaned))/float64(maxInt(len(rawHTML), 1))))
	}

	navigableURL, err := resolveNavigableURL(*fixture, *url)
	if err != nil {
		fatal("resolving a navigable URL for -validate: %v", err)
	}

	switch *mode {
	case "details":
		runDetailsMode(ctx, extractor, cleaned, *title, *diag)
	case "selectors":
		runSelectorsMode(ctx, extractor, cleaned, *title, *diag, validateConfig{
			enabled:  *validate,
			url:      navigableURL,
			headless: *headlessF,
			waitTime: *waitTimeF,
			timeout:  *scrapeTOF,
		})
	default:
		fatalUsage(fmt.Sprintf("unknown -mode %q (want details|selectors|stock)", *mode))
	}
}

// resolveNavigableURL converts whichever input was given (-fixture or
// -url) into a URL string browser.Session.Navigate can actually open.
// A live -url is passed through unchanged. A -fixture path is converted
// to an absolute file:// URL — mirrors what cmd/scrapercheck already does
// successfully against these same fixture files (confirmed: chromedp
// opens file:// URLs natively, no special-casing needed on the Scraper
// side). Uses net/url + filepath.Abs rather than a naive string
// concatenation specifically because a bare "file://" + a Windows path
// like D:\Projects\...\product_page.html produces a malformed URL
// (backslashes aren't valid URL path separators, and the drive-letter
// colon needs correct handling) — url.URL{Scheme, Path: filepath.ToSlash(abs)}.String()
// handles this correctly on both Windows and POSIX paths.
func resolveNavigableURL(fixture, liveURL string) (string, error) {
	if liveURL != "" {
		return liveURL, nil
	}
	abs, err := filepath.Abs(fixture)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path for %s: %w", fixture, err)
	}
	slashed := filepath.ToSlash(abs)
	// On Windows, filepath.ToSlash("D:\...") yields "D:/..." — a leading
	// slash is required to make this a valid file:// URL path
	// (file:///D:/... not file://D:/...), matching the file:///
	// three-slash form your own scrapercheck run already used
	// successfully.
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	u := url.URL{Scheme: "file", Path: slashed}
	return u.String(), nil
}

// validateConfig carries the -validate flag family through to
// runSelectorsMode without widening its parameter list on every future
// flag addition.
type validateConfig struct {
	enabled  bool
	url      string // always a navigable URL by the time runSelectorsMode sees it — file:// for fixtures, as-given for -url
	headless bool
	waitTime time.Duration
	timeout  time.Duration
}

func runDetailsMode(ctx context.Context, ex *pipeline.Extractor, cleaned, title string, diag bool) {
	result, err := ex.ExtractDetails(ctx, cleaned, title, nil)
	if err != nil {
		fatal("ExtractDetails failed: %v", err)
	}
	fmt.Println("\n=== ExtractDetails result ===")
	printJSON(result)
	if diag {
		fmt.Println("\nField-by-field:")
		printField("price", result.Price)
		printField("stock_status", result.StockStatus)
		printField("description", result.Description)
		printField("isbn", result.ISBN)
	}
}

func runSelectorsMode(ctx context.Context, ex *pipeline.Extractor, cleaned, title string, diag bool, vc validateConfig) {
	selectors, err := ex.ExtractSelectors(ctx, cleaned, title)
	if err != nil {
		fatal("ExtractSelectors failed: %v", err)
	}
	fmt.Println("\n=== ExtractSelectors result (map[string]*SelectorConfig) ===")
	printJSON(selectors)
	if diag {
		fmt.Println("\nField-by-field:")
		for _, field := range []string{"title", "price", "availability", "isbn", "description"} {
			cfg, ok := selectors[field]
			if !ok || cfg == nil {
				fmt.Printf("  %-14s -> null (field entirely absent from LLM output)\n", field)
				continue
			}
			fmt.Printf("  %-14s -> selector=%s find_by_text=%v then_next=%s direct_text=%v preserve_semantics=%v\n",
				field, derefStr(cfg.Selector), cfg.FindByText, derefStr(cfg.ThenNext), cfg.DirectText, cfg.PreserveSemantics)
		}
	}

	if !vc.enabled {
		return
	}

	fmt.Printf("\n=== -validate: running these selectors through the REAL Scraper.Scrape against %s ===\n", vc.url)

	sess, err := browser.NewSession(ctx, vc.headless, vc.timeout)
	if err != nil {
		fatal("-validate: browser.NewSession failed: %v", err)
	}
	defer sess.Close(ctx)

	scraper := pipeline.NewScraper(vc.headless, vc.waitTime, vc.timeout)

	// waitSelectors intentionally left empty: this tool doesn't know
	// ahead of time which selector on the page indicates "loaded," and
	// requiring one would make -validate unusable for a page whose real
	// production TrackingPair config hasn't been decided yet — this is
	// meant as a first-pass sanity check on ExtractSelectors' raw output,
	// not a rehearsal of a fully-configured production scrape.
	result, err := scraper.Scrape(ctx, vc.url, selectors, nil, sess, "", "")
	if err != nil {
		fatal("-validate: Scraper.Scrape failed: %v", err)
	}

	fmt.Println("\n--- Scrape result ---")
	printJSON(result)

	fmt.Println("\n--- Per-field pass/fail (checking for null/empty, same spirit as discover_selectors.py's validate_selectors, but via the real production Scraper rather than a separate reimplementation) ---")
	reportValidationField("price (raw)", result.RawPriceText)
	reportValidationField("price (parsed)", floatPtrToStrPtr(result.Price))
	reportValidationField("stock (raw)", result.RawStockText)
	if result.InStock != nil {
		fmt.Printf("  %-16s -> PASS: %v\n", "stock (parsed)", *result.InStock)
	} else {
		fmt.Printf("  %-16s -> FAIL: could not classify (nil)\n", "stock (parsed)")
	}
	fmt.Printf("\n  Overall status: %s", derefStatus(result.Status))
	if result.Reason != nil && *result.Reason != "" {
		fmt.Printf(" (reason: %s)", *result.Reason)
	}
	fmt.Println()
}

// derefStatus renders result.Status (a *string per domain.AvailabilityResult
// — confirmed against scraper.go's own doScrape, which assigns
// result.Status = &status rather than a plain string) for display,
// showing an explicit marker if it's nil rather than silently printing
// an empty string.
func derefStatus(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// reportValidationField prints PASS/FAIL for one extracted string field,
// mirroring validate_selectors()'s "confirms each selector returns a
// plausible non-empty value" check — a nil pointer or an empty string
// both count as FAIL, matching doScrape's own documented truthiness
// handling (Python's `if extracted.get(...)` treats "" the same as None).
func reportValidationField(label string, v *string) {
	if v == nil || *v == "" {
		fmt.Printf("  %-16s -> FAIL: null/empty\n", label)
		return
	}
	fmt.Printf("  %-16s -> PASS: %q\n", label, *v)
}

func floatPtrToStrPtr(f *float64) *string {
	if f == nil {
		return nil
	}
	s := fmt.Sprintf("%.2f", *f)
	return &s
}

func runStockMode(ctx context.Context, ex *pipeline.Extractor, text string, diag bool) {
	fmt.Printf("=== ClassifyStockStatus(%q) ===\n", text)
	result, err := ex.ClassifyStockStatus(ctx, text)
	if err != nil {
		// Per extractor.go's own doc comment, this should never actually
		// happen (the method is designed to never return a Go error) —
		// surfacing it loudly here if it ever does, since that would
		// itself be a regression worth knowing about.
		fatal("ClassifyStockStatus returned a Go error (this should never happen per its own contract): %v", err)
	}
	switch {
	case result == nil:
		fmt.Println("Result: <nil> (model returned UNKNOWN, an unrecognized response, or the call failed — indistinguishable from here by design, matching Python's bare except-return-None)")
	case *result:
		fmt.Println("Result: TRUE (in stock)")
	default:
		fmt.Println("Result: FALSE (out of stock)")
	}
	if diag {
		fmt.Println("\nNote: -diag has nothing extra to show in stock mode — ClassifyStockStatus takes raw text only, never HTML, so there is no cleaned-HTML/prompt body to dump beyond what's already logged to stderr by the method itself (model resolution, classification result) if you're running with your terminal showing stderr.")
	}
}

// loadInput reads either a local fixture file or fetches a live URL,
// returning the raw HTML string and a human-readable source label for
// the diagnostic header line.
func loadInput(ctx context.Context, fixture, url string) (string, string, error) {
	if fixture != "" {
		b, err := os.ReadFile(fixture)
		if err != nil {
			return "", "", fmt.Errorf("reading fixture %s: %w", fixture, err)
		}
		return string(b), "fixture:" + fixture, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("building request for %s: %w", url, err)
	}
	// A plausible desktop UA — some stores (per this project's own
	// Cloudflare-avoidance findings elsewhere) behave differently for
	// obviously-bot-shaped requests. This tool uses plain net/http, NOT
	// browser.Session/chromedp, so it cannot execute JS or dodge a real
	// WAF challenge — if a live URL returns a Cloudflare interstitial
	// instead of the real page, that's expected and not a bug in this
	// tool; use a real browser.Session-backed fetch path (or manually
	// save the rendered page as a fixture) for WAF-guarded stores.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("reading response body from %s: %w", url, err)
	}
	return string(b), "url:" + url, nil
}

func printField(name string, v *string) {
	if v == nil {
		fmt.Printf("  %-14s -> null\n", name)
		return
	}
	display := *v
	if len(display) > 120 {
		display = display[:120] + "…"
	}
	fmt.Printf("  %-14s -> %q\n", name, display)
}

func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("(failed to marshal result for display: %v)\n", err)
		return
	}
	fmt.Println(string(b))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fatalUsage(msg string) {
	fmt.Fprintf(os.Stderr, "extractorcheck: %s\n\n", msg)
	flag.Usage()
	os.Exit(2)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "extractorcheck: "+format+"\n", args...)
	os.Exit(1)
}
