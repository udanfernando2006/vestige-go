// Package browser defines the browser automation interfaces and implementation.
//
// The following method from browser/session.py is deliberately not exported:
// - wait_for_load (WaitForLoad): only called internally by find_and_fill_search.
package browser

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Default anti-detection headers, matching session.py's non-UA headers
// exactly. User-Agent is deliberately NOT set here anymore — see
// chromeUserAgent/chromeUAMetadata below for why.
//
// BUG 4A INVESTIGATION (headless-only Cloudflare challenge on first
// navigation): the previous version of this map set "User-Agent" to a
// spoofed Firefox string, applied via network.SetExtraHTTPHeaders below.
// That only rewrites the outgoing HTTP header — it does NOT change
// navigator.userAgent (JS-visible) or the Sec-CH-UA*/Client-Hints headers
// Chromium sends automatically based on its real identity, regardless of
// the HTTP header override. The result was a real, verifiable
// inconsistency on every single request: the User-Agent header claimed
// Firefox 149, while Client Hints and every JS-side probe still reported
// Chromium — a well-known, high-confidence bot-detection signal, since
// this exact "header spoofed, everything else not" pattern is what naive
// automation typically produces. Replaced with a full, consistent Chrome
// identity via emulation.SetUserAgentOverride (HTTP header + JS
// navigator.userAgent + Client Hints all agree) instead of continuing to
// impersonate Firefox, which sends no Client Hints at all and so could
// never be made fully consistent this way. This is a plausible
// contributor to bug 4a, not a confirmed root cause — the response-header
// diagnostic logging added alongside this change (see
// registerHeaderDiagnostics) is intended to help confirm or rule it out
// on the next live run, headed vs. headless, same URL.
var defaultHeaders = map[string]interface{}{
	"Referer":         "https://www.google.com/",
	"Accept-Language": "en-US,en;q=0.9",
	"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
}

// chromeUserAgent is a real, current Chrome-on-Windows User-Agent string.
// Presenting as real Chrome (the engine chromedp actually drives) rather
// than impersonating a different browser is what makes full UA/Client-Hints
// consistency achievable at all — see the doc comment on defaultHeaders.
const chromeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

// chromeUAMetadata carries the Sec-CH-UA-* Client Hints values that must
// accompany chromeUserAgent for emulation.SetUserAgentOverride to emit
// them consistently (per cdproto's own doc comment on that method:
// "userAgentMetadata must be set for Client Hint headers to be sent").
// Field values chosen to match a real Chrome 128 / Windows 10 identity —
// update the version numbers here in lockstep with chromeUserAgent if that
// string is ever bumped, or the header/Client-Hints mismatch this patch
// exists to fix would simply reappear one level down.
var chromeUAMetadata = &emulation.UserAgentMetadata{
	Brands: []*emulation.UserAgentBrandVersion{
		{Brand: "Not)A;Brand", Version: "24"},
		{Brand: "Chromium", Version: "128"},
		{Brand: "Google Chrome", Version: "128"},
	},
	FullVersionList: []*emulation.UserAgentBrandVersion{
		{Brand: "Not)A;Brand", Version: "24.0.0.0"},
		{Brand: "Chromium", Version: "128.0.0.0"},
		{Brand: "Google Chrome", Version: "128.0.0.0"},
	},
	Platform:        "Windows",
	PlatformVersion: "10.0.0",
	Architecture:    "x86",
	Mobile:          false,
	Bitness:         "64",
}

// Session defines the browser target/tab automation contract.
type Session interface {
	// Navigate loads the specified URL.
	Navigate(ctx context.Context, url string) error

	// GetHTML returns the full outer HTML content of the page.
	GetHTML(ctx context.Context) (string, error)

	// GetURL returns the current location URL of the target.
	GetURL(ctx context.Context) (string, error)

	// WaitForSelector waits until the given selector is visible in the page.
	//
	// NOTE: To match Python's except Exception: pass, this method swallows
	// timeout and selection errors, returning nil instead of propagating them.
	WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error

	// FindAndFillSearch finds the search input, fills it, and submits. (Stub for Session 1)
	FindAndFillSearch(ctx context.Context, query string) (bool, error)

	// FreshContext forks a new isolated context from the same browser. (Stub for Session 1)
	FreshContext(ctx context.Context) (Session, error)

	// Close terminates the target and allocator if owned.
	Close(ctx context.Context) error
}

// ChromedpSession implements the Session interface using chromedp.
type ChromedpSession struct {
	allocatorCtx    context.Context
	allocatorCancel context.CancelFunc // nil for forked sessions
	targetCtx       context.Context
	targetCancel    context.CancelFunc
	timeout         time.Duration
}

// bundledChromiumExecPath looks for a pinned headless-shell binary shipped
// alongside the running executable, under a `chromium/` subdirectory —
// e.g. <install-dir>/chromium/headless-shell(.exe). Returns "" if not
// found, NOT an error: this keeps `wails3 task dev`/any unpackaged local
// run working exactly as before (chromedp's own default discovery,
// whatever Chrome/Edge/Chromium happens to be on the dev machine), while
// a real packaged build — which the packaging step is expected to place
// the pinned binary into — gets a deterministic, frozen version instead.
//
// NOT yet wired into any OS's packaging Taskfile (create:app:bundle,
// create:nsis:installer, create:deb, etc.) — this only makes the Go side
// consume a bundled binary if one is placed at this relative path; the
// download-and-place-it-there step is separate, still open work.
func bundledChromiumExecPath() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	exeDir := filepath.Dir(exePath)

	binName := "headless-shell"
	if runtime.GOOS == "windows" {
		binName = "headless-shell.exe"
	}

	candidate := filepath.Join(exeDir, "chromium", binName)
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	return ""
}

// NewSession initializes a new top-level ChromedpSession with a dedicated allocator.
func NewSession(parent context.Context, headless bool, timeout time.Duration) (*ChromedpSession, error) {
	// NOTE: chromedp.NoSandbox is explicitly added for container/headless environments
	// (common when running root/headless Chrome inside Docker) even though it is not
	// present in the Python source.
	//
	// chromedp.WindowSize is a NEW addition, Go-only, no Python equivalent —
	// added to fix a real, live-confirmed bug distinct from (and, it turned
	// out, upstream of) the searchEnhancementSettleDelay fix below.
	// chromedp.DefaultExecAllocatorOptions sets NO window/viewport size at
	// all (confirmed via direct source inspection) — both headed and
	// headless previously launched at whatever unspecified internal default
	// Chrome itself picks, which are not guaranteed to be the same value for
	// the two modes. Live-confirmed on jumpbooks.lk: isVisible() (which
	// wraps chromedp's own layout-aware NodeVisible check) returned false in
	// headless for the exact same DOM node — same matched form/input id,
	// confirmed via FindAndFillSearch's own diagnostic logging — that
	// isVisible() correctly found visible in headed mode. A responsive
	// header collapsing its search widget below some viewport-width
	// breakpoint (an ordinary, ubiquitous pattern, not a bug in the site) is
	// the most likely explanation: if headless's unspecified default
	// viewport happens to fall under that breakpoint and headed's real
	// window doesn't, NodeVisible is reporting reality correctly in both
	// cases — the actual bug is that this code never pinned an explicit,
	// consistent size for either mode. This is a plausible, well-evidenced
	// mechanism, not a confirmed one — if search-widget discovery is still
	// wrong after this fix, that's a real signal this theory needs
	// revisiting, not a reason to silently retry.
	//
	// Applied to BOTH headless and headed, deliberately — headed mode's
	// previous behavior (real OS window default size) was never verified
	// against any specific value either; pinning both removes an unverified
	// assumption from both paths rather than just patching the one that was
	// observed failing.
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1920, 1080),
	)

	// Point at the bundled, version-pinned headless-shell — but only in
	// headless mode. headless-shell is a stripped binary with no windowed
	// UI at all, so it isn't a valid target for non-headless runs; those
	// keep chromedp's normal system-browser discovery unconditionally,
	// same as before this bundling work existed. In headless mode, if the
	// packaging step placed a bundled binary, use it; otherwise fall
	// through to system discovery same as today (unpackaged local dev, or
	// a packaged build that genuinely has no bundle for some reason — this
	// degrades gracefully rather than failing outright).
	if headless {
		if execPath := bundledChromiumExecPath(); execPath != "" {
			opts = append(opts, chromedp.ExecPath(execPath))
		} else {
			opts = append(opts, chromedp.Flag("headless", "new"))
		}
	} else {
		opts = append(opts, chromedp.Flag("headless", false))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(parent, opts...)

	targetCtx, targetCancel := chromedp.NewContext(allocCtx)

	s := &ChromedpSession{
		allocatorCtx:    allocCtx,
		allocatorCancel: allocCancel,
		targetCtx:       targetCtx,
		targetCancel:    targetCancel,
		timeout:         timeout,
	}

	// Diagnostic only, no Python equivalent — registers before the first
	// navigation so bug 4a's headed-vs-headless comparison run captures
	// the very first document response. See registerHeaderDiagnostics.
	registerHeaderDiagnostics(targetCtx)

	// Apply default anti-detection headers, UA/Client-Hints identity, and
	// evaluation scripts. Factored into stealthActions() (shared with
	// FreshContext below) rather than duplicated inline, closing a
	// duplication risk this file's own FreshContext doc comment already
	// flagged in principle: two independent copies of this block drifting
	// apart is exactly the shape of bug that already bit the FreshContext/
	// runWithContext issue documented below.
	err := chromedp.Run(targetCtx, stealthActions())
	if err != nil {
		s.Close(parent)
		return nil, err
	}

	return s, nil
}

// stealthActions returns the browser-identity setup run once per fresh
// chromedp target — top-level (NewSession) or forked (FreshContext).
// Centralizing this closes a real duplication risk: this exact block used
// to be copy-pasted between the two call sites, and FreshContext's own doc
// comment below already flags how easily two independent copies of
// target-initialization logic can drift apart and cause a hard-to-diagnose
// bug (see the runWithContext/FreshContext story in that comment) — this
// refactor removes that specific risk for this block going forward.
func stealthActions() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.ActionFunc(func(ctx context.Context) error {
			headers := network.Headers(defaultHeaders)
			return network.SetExtraHTTPHeaders(headers).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// emulation.SetUserAgentOverride's generated constructor takes
			// only the UA string — there is no fluent With*() setter for
			// UserAgentMetadata in this cdproto version (confirmed against
			// real source, not assumed), so it must be assigned directly on
			// the params struct before Do(). Skipping this line would
			// compile fine but silently leave Client Hints unset — per
			// cdproto's own doc comment, "userAgentMetadata must be set for
			// Client Hint headers to be sent" — which would recreate a
			// milder version of the exact header/Client-Hints mismatch this
			// patch exists to fix, just missing instead of contradictory.
			params := emulation.SetUserAgentOverride(chromeUserAgent)
			params.UserAgentMetadata = chromeUAMetadata
			return params.Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument("Object.defineProperty(navigator, 'webdriver', {get: () => false})").Do(ctx)
			return err
		}),
	}
}

// registerHeaderDiagnostics logs the response status and a few
// Cloudflare-relevant headers for every top-level document navigation on
// this target, to stderr. Diagnostic only, no Python equivalent — added
// specifically to arbitrate bug 4a (headless vs. headed divergence on
// first navigation) by giving a same-URL, headed-vs-headless comparison
// something concrete to diff: if Cloudflare returns a distinguishable
// signal (cf-mitigated, a managed-challenge cf-ray, or a __cf* cookie) in
// the headless case but not the headed case, that confirms the decision
// happens server-side before any client-side rendering signal could matter
// — which would redirect the investigation away from WebGL/canvas
// fingerprinting and toward whatever the request itself looks like
// (headers, TLS/JA3) in headless mode specifically.
//
// Uses network.Enable + chromedp.ListenTarget rather than a chromedp.Run
// action, since ListenTarget registers a standing event callback on the
// target rather than performing a one-shot action — it must be called
// once per target context, not per navigation.
func registerHeaderDiagnostics(targetCtx context.Context) {
	if err := chromedp.Run(targetCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return network.Enable().Do(ctx)
	})); err != nil {
		fmt.Fprintf(os.Stderr, "[Session][header-diag] network.Enable failed, diagnostics disabled: %v\n", err)
		return
	}

	chromedp.ListenTarget(targetCtx, func(ev interface{}) {
		e, ok := ev.(*network.EventResponseReceived)
		if !ok || e.Type != network.ResourceTypeDocument || e.Response == nil {
			return
		}
		fmt.Fprintf(os.Stderr,
			"[Session][header-diag] %s -> status=%d cf-ray=%q cf-mitigated=%q set-cookie-has-cf=%v content-length=%q\n",
			e.Response.URL,
			e.Response.Status,
			headerValue(e.Response.Headers, "cf-ray"),
			headerValue(e.Response.Headers, "cf-mitigated"),
			strings.Contains(headerValue(e.Response.Headers, "set-cookie"), "__cf"),
			headerValue(e.Response.Headers, "content-length"),
		)
	})
}

// headerValue does a case-insensitive lookup into a network.Headers map
// (map[string]any) — CDP does not normalize header-name casing, and it
// varies by server, so a plain map index would silently miss real values.
func headerValue(headers network.Headers, key string) string {
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// runWithContext is a helper that runs a chromedp action while linking the lifetime
// of the caller-supplied context with the session's target context.
func (s *ChromedpSession) runWithContext(ctx context.Context, timeout time.Duration, action chromedp.Action) error {
	runCtx, cancel := context.WithTimeout(s.targetCtx, timeout)
	defer cancel()

	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-runCtx.Done():
			}
		}()
	}

	return chromedp.Run(runCtx, action)
}

// Navigate loads the specified URL.
//
// NOTE: Python supports a custom wait_until parameter ("networkidle").
// Since no callers in the ported codebase use a non-default value, we hardcode
// navigation to wait until frame load (via Navigate) followed by body visibility
// check (WaitVisible), serving as a reliable approximation.
func (s *ChromedpSession) Navigate(ctx context.Context, url string) error {
	return s.runWithContext(ctx, s.timeout, chromedp.Tasks{
		chromedp.Navigate(url),
		chromedp.WaitVisible("body", chromedp.ByQuery),
	})
}

// GetHTML returns the full outer HTML of the page.
func (s *ChromedpSession) GetHTML(ctx context.Context) (string, error) {
	var html string
	err := s.runWithContext(ctx, s.timeout, chromedp.OuterHTML("html", &html, chromedp.ByQuery))
	return html, err
}

// GetURL returns the current location URL.
func (s *ChromedpSession) GetURL(ctx context.Context) (string, error) {
	var loc string
	err := s.runWithContext(ctx, s.timeout, chromedp.Location(&loc))
	return loc, err
}

// WaitForSelector waits for a selector to be visible.
//
// NOTE: This always returns nil on failure or timeout to preserve the Python
// browser.session.wait_for_selector design (which runs "except Exception: pass").
func (s *ChromedpSession) WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error {
	_ = s.runWithContext(ctx, timeout, chromedp.WaitVisible(selector, chromedp.ByQuery))
	return nil
}

// searchFormSelectors / searchInputSelectors / searchTriggerSelectors are
// direct ports of session.py's inline selector lists in
// _find_search_form / _find_search_input / _try_open_search_modal. Order is
// load-bearing — first match wins in all three.
var (
	searchFormSelectors = []string{
		"form[role='search']",
		"form[action*='/search']",
		"predictive-search form",
		".search-modal form",
	}
	searchInputSelectors = []string{
		"input[type=\"search\"]",
		"input[name=\"q\"]",
		"input[name=\"s\"]",
		"input[type=\"text\"]",
	}
	searchTriggerSelectors = []string{
		"button[aria-label*='search' i]",
		"summary[aria-label*='search' i]",
		".header__icon--search",
		".search-modal__toggle",
		"a[aria-label*='search' i]",
	}
)

// FindAndFillSearch is a direct port of session.py's find_and_fill_search().
//
// *** HIGHER-RISK PORT — VERIFY AGAINST A REAL BROWSER BEFORE TRUSTING ***
// The control flow below (including the early-return-skips-the-trailing-
// wait asymmetry on the modal-success path) is ported with full confidence
// — it's plain Go logic, traced directly against the Python source. The
// chromedp DOM-query mechanics (node-scoped queries via FromNode,
// visibility filtering via the NodeVisible query option) are this
// project's first use of that part of chromedp's API and have NOT been
// build- or run-verified in this pass, unlike everything ported before it.
// Confirm this compiles and actually finds/fills a real search box on at
// least one real store before relying on it.
func (s *ChromedpSession) FindAndFillSearch(ctx context.Context, query string) (bool, error) {
	// preURL captured BEFORE anything is filled/submitted — the baseline
	// waitForURLChange polls against below (Bug A fix, see that function's
	// doc comment). Best-effort: a failed GetURL here just leaves preURL
	// as "", which still works correctly as a baseline (any real URL the
	// page navigates to will differ from "").
	preURL, _ := s.GetURL(ctx)

	form, err := s.findSearchForm(ctx)
	if err != nil {
		return false, err
	}
	if form == nil {
		return false, nil
	}

	textInput, err := s.findSearchInput(ctx, form)
	if err != nil {
		return false, err
	}
	if textInput == nil {
		return false, nil
	}

	// NEW diagnostic (not a Python port, stderr-only): logs exactly which
	// selector matched the form and input, plus the form's action/method
	// and the input's name/id — added while investigating
	// jeyabookcentre.com landing on a bare, query-less /search every time
	// despite the input's DOM value being confirmed correct right before
	// Enter (see fillAndSubmit's own diagnostic). searchFormSelectors is
	// priority-ordered and "first match wins" (see that var's own doc
	// comment) — this is the EXACT same shape as an already-documented,
	// still-open bug on a different store: jumpbooks.lk's
	// findSearchForm matching a generic sitewide "form[role='search']"
	// widget before the store's real product-scoped search form, because
	// that selector is tried first. If jeyabookcentre.com's real search
	// form ISN'T what searchFormSelectors[0] finds, this line is what
	// will show it — the previous diagnostic could only tell us the value
	// reached whatever element it filled, not whether that element was
	// the right one.
	formAction, _, _ := s.attributeValue(ctx, form, "action")
	formMethod, _, _ := s.attributeValue(ctx, form, "method")
	inputName, _, _ := s.attributeValue(ctx, textInput, "name")
	inputID, _, _ := s.attributeValue(ctx, textInput, "id")
	fmt.Fprintf(os.Stderr, "[Session] FindAndFillSearch: matched form selector=%q action=%q method=%q; matched input selector=%q name=%q id=%q\n",
		form.selector, formAction, formMethod, textInput.selector, inputName, inputID)

	fillErr := s.attemptFillAndSubmit(ctx, form, textInput, query, preURL)

	if fillErr == errEarlyReturnTrue {
		return true, nil
	}
	if fillErr != nil {
		// try/except fallback: build a search URL from the ORIGINAL form
		// (not any refreshed one) and navigate directly, matching Python's
		// except-block behavior exactly.
		searchURL, buildErr := s.buildSearchURLFromForm(ctx, form, query)
		if buildErr != nil || searchURL == "" {
			return false, nil
		}
		if err := s.Navigate(ctx, searchURL); err != nil {
			return false, err
		}
	}

	// Shared trailing wait — best-effort, never fails the call. Polls for
	// a URL change rather than chromedp.WaitVisible("body") (Bug A fix —
	// see waitForURLChange's doc comment below). For the fallback-Navigate
	// branch just above, this resolves immediately (Navigate already
	// blocked until its own load completed, so the URL has already
	// changed by the time we get here); for the direct-fill-and-Enter
	// path it's the real fix, since "body" was already visible on the
	// pre-submission page too and previously provided no real wait at all.
	s.waitForURLChange(ctx, preURL, s.timeout)

	return true, nil
}

// waitForURLChange polls GetURL until it differs from preURL AND stays
// unchanged for a short settle window, or the overall timeout elapses.
// Replaces the previous chromedp.WaitVisible("body") trailing wait used
// after submitting a search (Bug A, jeyabookcentre.com): "body" is already
// visible on the PRE-submission page, so WaitVisible("body") returned
// near-instantly and never actually waited for the search's navigation to
// land — confirmed live: typing "test" + Enter on jeyabookcentre.com does
// navigate to a real results page (https://jeyabookcentre.com/search?key=test),
// it just takes a few seconds, and the old wait was reading the URL before
// that navigation resolved, caching the stale pre-search URL as the
// store's "discovered" search template.
//
// The settle window (added after the first live re-test of this fix)
// guards against a second, related failure mode a bare "return on first
// difference" polling loop is exposed to: some SPA search flows update the
// URL in more than one step — e.g. an immediate client-side route change
// to a bare path first, with the query string appended a moment later
// once the actual search request resolves. A poll that returns the
// instant it sees ANY difference from preURL can capture that first,
// incomplete transitional URL (a bare "/search" with no query) rather
// than the final one. Requiring the URL to hold steady for settleWindow
// before returning — and resetting the settle clock, not just re-polling,
// every time a further change is observed — makes this robust to that
// pattern without needing to know how many stages a given store's search
// flow uses.
func (s *ChromedpSession) waitForURLChange(ctx context.Context, preURL string, timeout time.Duration) {
	const pollInterval = 250 * time.Millisecond
	const settleWindow = 500 * time.Millisecond
	deadline := time.Now().Add(timeout)

	last := preURL
	var changedAt time.Time
	for {
		cur, err := s.GetURL(ctx)
		if err == nil {
			if cur != last {
				// First difference from preURL, or a further change during
				// the settle window below — either way, (re)start the
				// settle clock rather than declaring done on the spot.
				last = cur
				changedAt = time.Now()
			} else if cur != preURL && !changedAt.IsZero() && time.Since(changedAt) >= settleWindow {
				return
			}
		}
		if !time.Now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

// errEarlyReturnTrue is a sentinel used internally by attemptFillAndSubmit
// to signal the modal-success early-return path (Python's `return True`
// from inside the try block) without that path falling through to the
// shared trailing wait a second time.
var errEarlyReturnTrue = &sentinelError{"find_and_fill_search: modal success early return"}

type sentinelError struct{ msg string }

func (e *sentinelError) Error() string { return e.msg }

// elementRef is a resolved DOM element reference used throughout
// FindAndFillSearch's helpers. It intentionally does NOT expose its
// underlying *cdp.Node for direct use as a chromedp action's `sel`
// argument — real-world testing confirmed chromedp.SetValue/SendKeys do
// NOT accept a raw *cdp.Node as sel the way chromedp.Click/MouseClickNode
// do; passing one silently falls through to a text-search
// (DOM.performSearch) path using the node's %v-formatted Go struct dump as
// the literal search query, which never matches and loops indefinitely
// (confirmed via a live hang against booxworm.lk during verification).
//
// To act on the element (SetValue/SendKeys/Click/AttributeValue), always
// re-resolve it via its own selector scoped to its PARENT — never scoped
// to itself, and never by passing the node directly:
//
//	chromedp.SetValue(ref.selector, value, ref.queryOpts()...)
//
// `node` is kept only for (a) scoping queries for this element's OWN
// children via FromNode(ref.node), and (b) the isVisible check, which
// uses chromedp.ByNodeID + chromedp.NodeVisible — a different, narrower
// API confirmed working in earlier verification against real stores.
type elementRef struct {
	node     *cdp.Node // resolved node; used for child-scoping and isVisible only
	parent   *cdp.Node // nil = page root; used with selector to re-resolve/act on this element
	selector string    // the selector that found this element, scoped to `parent`
}

// queryOpts returns the ByQuery + (optional) FromNode(parent) options
// needed to act on or re-resolve this element via its own selector.
func (r *elementRef) queryOpts() []chromedp.QueryOption {
	opts := []chromedp.QueryOption{chromedp.ByQuery}
	if r.parent != nil {
		opts = append(opts, chromedp.FromNode(r.parent))
	}
	return opts
}

// attemptFillAndSubmit is a direct port of the try block in
// find_and_fill_search(): visible-input direct fill, or
// modal-open-then-refresh-then-fill, or (on any failure) a signal to fall
// back to URL construction — matching the Python try/except's fallback
// trigger exactly (any exception anywhere in the try block reaches the
// except block's fallback).
func (s *ChromedpSession) attemptFillAndSubmit(
	ctx context.Context,
	form *elementRef,
	textInput *elementRef,
	query string,
	preURL string,
) error {
	visible, err := s.isVisible(ctx, textInput)
	if err != nil {
		return err // -> caller treats as fallback trigger
	}

	if visible {
		return s.fillAndSubmit(ctx, textInput, query)
	}

	// Not visible: try opening a search modal, then re-query.
	_, _ = s.tryOpenSearchModal(ctx) // best-effort; Python ignores the return value here too

	refreshedForm, err := s.findSearchForm(ctx)
	if err != nil {
		return err
	}
	if refreshedForm != nil {
		refreshedInput, err := s.findSearchInput(ctx, refreshedForm)
		if err != nil {
			return err
		}
		if refreshedInput != nil {
			refreshedVisible, err := s.isVisible(ctx, refreshedInput)
			if err != nil {
				return err
			}
			if refreshedVisible {
				if err := s.fillAndSubmit(ctx, refreshedInput, query); err != nil {
					return err
				}
				// Own best-effort wait, matching Python's inner try/except.
				// Polls for a URL change rather than
				// chromedp.WaitVisible("body") — same Bug A fix as the
				// shared trailing wait in FindAndFillSearch, needed here
				// too since this modal-success path returns early via
				// errEarlyReturnTrue and skips that shared wait entirely.
				s.waitForURLChange(ctx, preURL, s.timeout)
				return errEarlyReturnTrue
			}
		}
	}

	// Modal path didn't produce a visible input — this is NOT an error in
	// Python (no exception raised), it just falls through to
	// build_search_url_from_form outside the try block. Signal that via a
	// distinct sentinel so the caller falls back WITHOUT treating this as
	// the except-block path (though both end up calling the same
	// buildSearchURLFromForm+Navigate fallback, so in practice returning a
	// plain non-nil, non-sentinel error here produces the same caller
	// behavior — flagged for a future reader rather than introduced as a
	// second silent special case).
	return &sentinelError{"find_and_fill_search: modal path found no visible input"}
}

// findSearchForm is a direct port of _find_search_form(). Returns nil (not
// an error) if no form is found at all, matching Python's implicit None.
func (s *ChromedpSession) findSearchForm(ctx context.Context) (*elementRef, error) {
	for _, sel := range searchFormSelectors {
		if ref, ok := s.queryFirst(ctx, sel, nil); ok {
			return ref, nil
		}
	}
	// Fallback: any form on the page.
	if ref, ok := s.queryFirst(ctx, "form", nil); ok {
		return ref, nil
	}
	return nil, nil
}

// findSearchInput is a direct port of _find_search_input(), scoped to the
// given form via chromedp's FromNode query option.
func (s *ChromedpSession) findSearchInput(ctx context.Context, form *elementRef) (*elementRef, error) {
	for _, sel := range searchInputSelectors {
		if ref, ok := s.queryFirst(ctx, sel, form); ok {
			return ref, nil
		}
	}
	return nil, nil
}

// isVisible is a direct port of _is_visible(). Checks visibility of an
// already-resolved node by re-querying its NodeID with the NodeVisible
// query option — a match means present and visible; no match (with no
// query error) means present-but-hidden, mirrored as `false` exactly like
// Python's is_visible() catching the exception and returning False.
//
// CONFIRMED WORKING against real stores (booxworm.lk, jumpbooks.lk) during
// verification — unlike the *cdp.Node-direct pattern this file used
// (incorrectly) for SetValue/SendKeys, ByNodeID+NodeVisible is a distinct
// chromedp code path that was empirically validated to agree with both a
// manual JS visibility check and chromedp.WaitVisible.
func (s *ChromedpSession) isVisible(ctx context.Context, ref *elementRef) (bool, error) {
	if ref == nil {
		return false, nil
	}
	var nodes []*cdp.Node
	err := s.runWithContext(ctx, s.timeout, chromedp.ActionFunc(func(actionCtx context.Context) error {
		return chromedp.Nodes([]cdp.NodeID{ref.node.NodeID}, &nodes, chromedp.ByNodeID, chromedp.NodeVisible).Do(actionCtx)
	}))
	if err != nil {
		// Python swallows this (except Exception: return False).
		return false, nil
	}
	return len(nodes) > 0, nil
}

// fillAndSubmit is a direct port of _fill_and_submit_search_input(): fill,
// then press Enter.
//
// Acts on the element via its own selector scoped to its parent
// (ref.queryOpts()), NOT via the raw resolved node — see elementRef's doc
// comment for why: passing a *cdp.Node directly to SetValue/SendKeys was
// confirmed, via a live hang against booxworm.lk, to silently degrade into
// an infinite DOM.performSearch retry loop instead of acting on the
// element or returning an error.
//
// Fills via real per-character chromedp.SendKeys typing, NOT
// chromedp.SetValue — a real, live-confirmed fix, not a first guess.
// SetValue sets the DOM .value directly and fires only synthetic
// 'input'/'change' events. Live diagnostics against jeyabookcentre.com
// confirmed the value DID land in the DOM ("test" read back correctly
// right before Enter) — yet the search still landed on a bare /search
// every time, with an empty query. A separate diagnostic (in
// FindAndFillSearch) showed the matched form/input have NO action,
// method, name, or id attributes at all, which rules out a native HTML
// form submission (a nameless input can't be serialized into a query
// string at all, and an action-less form defaults to reloading the
// current page, not navigating to /search) — this store's search is
// JS-driven: its own script intercepts Enter and builds the URL from
// wherever it tracks the input's value, almost certainly a React-style
// controlled-input state that only updates from real keystroke events,
// which SetValue's synthetic ones don't reliably produce.
// chromedp.SendKeys dispatches genuine per-character CDP
// Input.dispatchKeyEvent sequences — real keydown/keypress/input/keyup
// for every character (plus a real dom.Focus() first, via
// KeyEventNode) — the closest available simulation to actual typing,
// matching what a manual test on this exact store confirmed works.
//
// Should not regress jumpbooks.lk/booxworm.lk: both are native
// WooCommerce/Shopify forms that submit whatever's in the DOM .value at
// Enter-press time regardless of how it got there, so real keystrokes are
// a strict superset of what SetValue already provided for those two.
//
// The value-readback diagnostic below predates this change (added while
// still narrowing down the SetValue theory) and is kept — it's just as
// useful for confirming real typing lands correctly too.

// searchEnhancementSettleDelay is a deliberate pause inserted before typing
// into a located search input — Go-only, no Python equivalent.
//
// STATUS, UPDATED: this delay's own theory (a page-JS Enter/submit handler
// not yet attached when chromedp types) has NOT actually been tested yet,
// despite being added first. Live logging after this fix landed showed
// fillAndSubmit was never even being CALLED in the failing jumpbooks.lk
// runs — isVisible() was returning false for the located input before
// fillAndSubmit was ever reached, sending execution down the modal-retry
// path and then the buildSearchURLFromForm fallback instead (see
// chromedp.WindowSize in NewSession's opts for the fix targeting THAT
// upstream problem). This delay is being left in, not reverted — it's
// low-cost and was reasoned from real (if ultimately not the deciding)
// evidence — but treat it as unverified until a run actually reaches
// fillAndSubmit and its effect can be observed directly. If the
// WindowSize fix resolves search-widget discovery on its own, this delay
// may simply be unnecessary; that's a fine outcome, not a problem to chase.
//
// Original reasoning kept below for context.
// Added to fix
// a real, live-confirmed headless-only bug: on jumpbooks.lk, the EXACT SAME
// search form/input (confirmed via FindAndFillSearch's diagnostic logging —
// identical matched-form id="woocommerce-product-search-field-0" in both
// headless and headed runs, ruling out "matched a different form") produces
// TWO DIFFERENT destination URLs depending on headless vs. headed:
//   - headed:    https://jumpbooks.lk/?product_cat=&s=test&post_type=product
//     (WooCommerce's real product-scoped search — includes the form's
//     hidden product_cat/post_type fields)
//   - headless:  https://jumpbooks.lk/?s=test
//     (WordPress core's plain, unscoped search — confirmed via a real
//     screenshot of this exact URL: a generic "Nothing Found"-style page
//     that does not support ISBN search and pulls its sidebar from a
//     sitewide category widget, not real search results)
//
// A form's own plain native GET submission ALWAYS serializes every field,
// hidden or not, regardless of headless vs. headed — that behavior doesn't
// vary with rendering mode. The headless result is missing the hidden
// fields entirely, which means headless isn't reaching a native submission
// at all: something else is constructing ?s=test specifically, and the
// most likely candidate is the page's own product-search-enhancement JS
// simply not having attached its Enter/submit handler yet — a plausible,
// ordinary deferred-init race, not a chromedp mechanism issue. If that
// JS hasn't attached by the time Enter is sent, the browser falls through
// to its own default behavior for a bare <form method="get" action="/">
// with one visible field, which is exactly ?s=test.
//
// This is a mitigation for a race whose precise trigger (which script,
// what it waits on) is NOT confirmed — the delay is a deliberately
// generous, tunable buffer, not a verified minimum. If a store's search
// still resolves to an unscoped/generic URL after this fix, that specific
// store's enhancement JS likely needs longer than settleDelay, or this
// theory is wrong for that store — check FindAndFillSearch's existing
// "matched form selector=..." diagnostic and the Crawler's own
// "discovered search_url_template=..." log line to confirm either way,
// rather than assuming this fix generalizes untested.
const searchEnhancementSettleDelay = 1500 * time.Millisecond

func (s *ChromedpSession) fillAndSubmit(ctx context.Context, ref *elementRef, query string) error {
	fmt.Fprintf(os.Stderr, "[Session] fillAndSubmit: waiting %s for search-enhancement JS to settle before typing (see searchEnhancementSettleDelay doc comment)\n", searchEnhancementSettleDelay)
	return s.runWithContext(ctx, s.timeout, chromedp.Tasks{
		chromedp.Sleep(searchEnhancementSettleDelay),
		chromedp.SendKeys(ref.selector, query, ref.queryOpts()...),
		chromedp.ActionFunc(func(actionCtx context.Context) error {
			var domValue string
			if err := chromedp.Value(ref.selector, &domValue, ref.queryOpts()...).Do(actionCtx); err != nil {
				fmt.Fprintf(os.Stderr, "[Session] fillAndSubmit: could not read back input value before submit: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "[Session] fillAndSubmit: input DOM value right before Enter = %q (wanted %q)\n", domValue, query)
			}
			return nil
		}),
		chromedp.SendKeys(ref.selector, "\r", ref.queryOpts()...),
	})
}

// tryOpenSearchModal is a direct port of _try_open_search_modal(): tries
// each trigger selector in order, clicking the first one found; a click
// failure moves on to the next selector rather than aborting (matching
// Python's per-selector try/except: continue). Returns false, nil if no
// trigger was found or every click attempt failed — never an error, since
// Python's version never propagates one either (this is a best-effort
// probe, not a required step).
//
// Uses the same selector+queryOpts pattern as fillAndSubmit rather than
// chromedp.MouseClickNode(node) — MouseClickNode is plausibly a distinct,
// dedicated API that doesn't share SetValue/SendKeys's confirmed bug, but
// after being wrong once about *cdp.Node support in this file, every
// direct-node action here was switched to the one pattern actually
// verified against real stores, rather than trusting an unverified
// exception case-by-case.
func (s *ChromedpSession) tryOpenSearchModal(ctx context.Context) (bool, error) {
	for _, sel := range searchTriggerSelectors {
		ref, ok := s.queryFirst(ctx, sel, nil)
		if !ok {
			continue
		}
		err := s.runWithContext(ctx, s.timeout, chromedp.Click(ref.selector, ref.queryOpts()...))
		if err == nil {
			return true, nil
		}
		// click failed -> try next selector, matching Python's `continue`
	}
	return false, nil
}

// buildSearchURLFromForm is a direct port of _build_search_url_from_form().
//
// KNOWN BEHAVIORAL DEVIATION: Go's net/url.Values.Encode() always sorts
// query parameters alphabetically by key. Python's urlencode(params,
// doseq=True) preserves the original dict's insertion order instead. This
// means the RESULTING QUERY STRING'S PARAMETER ORDER can differ between
// the two implementations even when the parameter contents are identical.
// This should be harmless for any well-behaved store (query param order is
// not semantically meaningful per the URL spec), but it is a real,
// verifiable difference from the Python output, not a null risk — flagged
// here rather than silently accepted.
func (s *ChromedpSession) buildSearchURLFromForm(ctx context.Context, form *elementRef, query string) (string, error) {
	action, ok, err := s.attributeValue(ctx, form, "action")
	if err != nil {
		return "", err
	}
	if !ok || action == "" {
		return "", nil
	}

	method, ok, err := s.attributeValue(ctx, form, "method")
	if err != nil {
		return "", err
	}
	if !ok || method == "" {
		method = "get"
	}
	if strings.ToLower(method) != "get" {
		return "", nil
	}

	inputName := ""
	for _, sel := range searchInputSelectors {
		field, ok := s.queryFirst(ctx, sel, form)
		if !ok {
			continue
		}
		name, hasName, err := s.attributeValue(ctx, field, "name")
		if err != nil {
			return "", err
		}
		if hasName && strings.TrimSpace(name) != "" {
			inputName = name
			break
		}
	}
	if inputName == "" {
		inputName = "q"
	}

	currentURL, err := s.GetURL(ctx)
	if err != nil {
		return "", err
	}

	base, err := url.Parse(currentURL)
	if err != nil {
		return "", err
	}
	actionURL, err := url.Parse(action)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(actionURL)

	params := resolved.Query() // matches parse_qs(..., keep_blank_values=True) closely enough for this use
	params.Set(inputName, query)
	resolved.RawQuery = params.Encode() // NOTE: alphabetical key order — see doc comment above

	return resolved.String(), nil
}

// queryFirst runs sel (optionally scoped to parent.node via FromNode) and
// returns an *elementRef wrapping the first match, or ok=false if no node
// matched. A query error is treated the same as "not found" here,
// mirroring how Python's query_selector returning None is handled
// throughout session.py (no exception is raised for a simple non-match).
func (s *ChromedpSession) queryFirst(ctx context.Context, sel string, parent *elementRef) (*elementRef, bool) {
	var parentNode *cdp.Node
	if parent != nil {
		parentNode = parent.node
	}

	var nodes []*cdp.Node
	action := chromedp.ActionFunc(func(actionCtx context.Context) error {
		opts := []chromedp.QueryOption{chromedp.ByQuery, chromedp.AtLeast(0)}
		if parentNode != nil {
			opts = append(opts, chromedp.FromNode(parentNode))
		}
		return chromedp.Nodes(sel, &nodes, opts...).Do(actionCtx)
	})
	if err := s.runWithContext(ctx, s.timeout, action); err != nil || len(nodes) == 0 {
		return nil, false
	}
	return &elementRef{node: nodes[0], parent: parentNode, selector: sel}, true
}

// attributeValue reads an attribute off ref by re-resolving it via its own
// selector scoped to its parent — the same fix as fillAndSubmit/
// tryOpenSearchModal. An earlier draft of this function incorrectly scoped
// the query to FromNode(ref itself) using ref's own selector, which
// searches for a descendant matching that selector INSIDE ref rather than
// reading ref's own attribute; that bug was caught and fixed during review
// before this one (the *cdp.Node-as-sel bug) was found during live
// verification — two independent bugs in the same small area, both now
// fixed by routing every action through the same verified selector+parent
// pattern.
func (s *ChromedpSession) attributeValue(ctx context.Context, ref *elementRef, attr string) (string, bool, error) {
	if ref == nil {
		return "", false, nil
	}
	var value string
	var ok bool
	action := chromedp.ActionFunc(func(actionCtx context.Context) error {
		return chromedp.AttributeValue(ref.selector, attr, &value, &ok, ref.queryOpts()...).Do(actionCtx)
	})
	err := s.runWithContext(ctx, s.timeout, action)
	return value, ok, err
}

func (s *ChromedpSession) FreshContext(ctx context.Context) (Session, error) {
	targetCtx, targetCancel := chromedp.NewContext(s.allocatorCtx)

	forked := &ChromedpSession{
		allocatorCtx:    s.allocatorCtx,
		allocatorCancel: nil, // not owned — never cancel the parent's allocator
		targetCtx:       targetCtx,
		targetCancel:    targetCancel,
		timeout:         s.timeout,
	}

	// IMPORTANT: this init call must run directly on targetCtx, exactly like
	// NewSession does for the top-level session — NOT through
	// runWithContext, which derives a context.WithTimeout child and
	// deferred-cancels it the instant the call returns. For a brand-new
	// chromedp target, this initializing chromedp.Run is what actually
	// attaches the browser tab/event-loop to targetCtx in the first place;
	// cancelling that derived child context immediately afterward was
	// tearing down the freshly-attached target's underlying execution
	// context rather than just expiring an unrelated timeout, causing the
	// very next chromedp.Run against this same targetCtx (e.g. Navigate) to
	// fail immediately with context.Canceled. Confirmed via live testing
	// against two independent stores (jumpbooks.lk and jeyabookcentre.com,
	// the latter with no known Cloudflare/WAF involvement at all), ruling
	// out a store-specific WAF explanation.
	// Diagnostic only, no Python equivalent — see registerHeaderDiagnostics'
	// doc comment on NewSession's call site above for why this is here.
	registerHeaderDiagnostics(targetCtx)

	// NOTE: stealthActions() must run directly on targetCtx here, same as
	// NewSession — see the comment above this call about runWithContext
	// tearing down a freshly-attached target if used for this init step.
	err := chromedp.Run(targetCtx, stealthActions())
	if err != nil {
		targetCancel()
		return nil, err
	}

	return forked, nil
}

// Close cleans up the target context, and allocator context if owned.
func (s *ChromedpSession) Close(ctx context.Context) error {
	if s.targetCancel != nil {
		s.targetCancel()
	}
	if s.allocatorCancel != nil {
		s.allocatorCancel()
	}
	return nil
}

// Compile-time check to verify ChromedpSession implements Session interface.
var _ Session = (*ChromedpSession)(nil)
