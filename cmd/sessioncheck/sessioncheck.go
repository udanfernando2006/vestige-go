// Command sessioncheck is a throwaway manual verification tool — NOT part
// of the migrated codebase, NOT something Antigravity should port or the
// workflow should track. It exists solely to visually confirm
// FindAndFillSearch actually finds and fills a real store's search box,
// and to diagnose exactly where it fails when it doesn't.
//
// Normal usage (exercises the real Session code):
//
//	go run ./cmd/sessioncheck -store "https://jumpbooks.lk" -query "Hobbit"
//
// Diagnostic usage (bypasses FindAndFillSearch, probes each selector tier
// directly and prints match counts, to isolate WHERE a failure happens):
//
//	go run ./cmd/sessioncheck -store "https://booxworm.lk" -diag
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"

	"github.com/udanfernando2006/vestige-go/internal/browser"
)

func main() {
	storeURL := flag.String("store", "", "base URL of the store to test against, e.g. https://jumpbooks.lk")
	query := flag.String("query", "test", "search query to type into the search box")
	headless := flag.Bool("headless", false, "run headless (default: false, so you can watch it)")
	timeout := flag.Duration("timeout", 30*time.Second, "per-operation timeout")
	pause := flag.Duration("pause", 5*time.Second, "how long to leave the browser open after the run")
	diag := flag.Bool("diag", false, "run the diagnostic selector probe instead of FindAndFillSearch")
	flag.Parse()

	if *storeURL == "" {
		log.Fatal("-store is required, e.g. -store https://jumpbooks.lk")
	}

	if *diag {
		runDiag(*storeURL, *headless, *timeout)
		return
	}
	runNormal(*storeURL, *query, *headless, *timeout, *pause)
}

func runNormal(storeURL, query string, headless bool, timeout, pause time.Duration) {
	ctx := context.Background()

	fmt.Printf("Launching browser (headless=%v)...\n", headless)
	sess, err := browser.NewSession(ctx, headless, timeout)
	if err != nil {
		log.Fatalf("NewSession failed: %v", err)
	}
	defer func() {
		fmt.Println("Closing session...")
		_ = sess.Close(ctx)
	}()

	fmt.Printf("Navigating to %s...\n", storeURL)
	if err := sess.Navigate(ctx, storeURL); err != nil {
		log.Fatalf("Navigate failed: %v", err)
	}

	beforeURL, _ := sess.GetURL(ctx)
	fmt.Printf("Landed on: %s\n", beforeURL)

	fmt.Printf("Attempting FindAndFillSearch(%q)...\n", query)
	ok, err := sess.FindAndFillSearch(ctx, query)
	if err != nil {
		log.Fatalf("FindAndFillSearch returned an error (not swallowed — worth investigating): %v", err)
	}
	if !ok {
		fmt.Println("RESULT: FindAndFillSearch returned false — no search form/input found on this store.")
	} else {
		fmt.Println("RESULT: FindAndFillSearch returned true — check the browser window: did it actually search?")
	}

	afterURL, _ := sess.GetURL(ctx)
	fmt.Printf("Current URL after search attempt: %s\n", afterURL)
	if afterURL == beforeURL {
		fmt.Println("WARNING: URL did not change at all — likely means nothing actually happened.")
	}

	fmt.Printf("Leaving browser open for %s so you can inspect the result...\n", pause)
	time.Sleep(pause)
}

// runDiag bypasses FindAndFillSearch entirely and reimplements just the
// probing steps of findSearchForm/findSearchInput inline, with print
// statements at each tier, so we can see exactly where zero matches start
// happening instead of only getting FindAndFillSearch's final bool.
func runDiag(storeURL string, headless bool, timeout time.Duration) {
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", headless))...)
	defer allocCancel()

	// Verbose logging so we can see WHY the browser process/context gets
	// canceled instead of only seeing "context canceled" downstream —
	// this prints chromedp's internal debug log, including the actual
	// Chrome subprocess's own stderr output if it crashes or exits.
	ctx, cancel := chromedp.NewContext(allocCtx,
		chromedp.WithDebugf(log.Printf),
		chromedp.WithErrorf(log.Printf),
	)
	defer cancel()

	// Force the target to actually exist before doing anything else —
	// this alone will surface a launch failure immediately and loudly,
	// rather than deferring the first real error to the first Nodes()
	// call several steps later.
	fmt.Println("Forcing initial target creation (chromedp.Run with no actions)...")
	if err := chromedp.Run(ctx); err != nil {
		log.Fatalf("initial chromedp.Run (target creation) failed: %v", err)
	}
	fmt.Println("Target created successfully. Proceeding to navigate.")

	// IMPORTANT: each phase below gets its OWN timeout derived fresh from
	// ctx, not one shared runCtx for the whole diagnostic. An earlier
	// version of this script shared a single 30s runCtx across every
	// probe cumulatively — navigation + ~13 selector probes + JS evals
	// genuinely exhausted that budget before reaching the fillAndSubmit
	// section, producing "context deadline exceeded" on every call there.
	// That was a bug in THIS DIAGNOSTIC, not evidence about SetValue/
	// SendKeys — worth remembering if any future probe here shows the
	// same error: check the budget before suspecting the API.
	phaseCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, timeout)
	}

	navCtx, navCancel := phaseCtx()
	fmt.Printf("Navigating to %s...\n", storeURL)
	if err := chromedp.Run(navCtx, chromedp.Navigate(storeURL), chromedp.WaitVisible("body", chromedp.ByQuery)); err != nil {
		navCancel()
		log.Fatalf("navigate failed: %v", err)
	}
	navCancel()

	fmt.Println("Waiting 2s for any client-side rendering to settle...")
	time.Sleep(2 * time.Second)

	formSelectors := []string{
		"form[role='search']",
		"form[action*='/search']",
		"predictive-search form",
		".search-modal form",
		"form", // fallback tier
	}

	fmt.Println("\n--- Form selector probe ---")
	var anyForm *cdp.Node
	for _, sel := range formSelectors {
		probeCtx, probeCancel := phaseCtx()
		var nodes []*cdp.Node
		err := chromedp.Run(probeCtx, chromedp.Nodes(sel, &nodes, chromedp.ByQuery, chromedp.AtLeast(0)))
		probeCancel()
		fmt.Printf("  %-30q -> %d matches (err: %v)\n", sel, len(nodes), err)
		if err == nil && len(nodes) > 0 && anyForm == nil {
			anyForm = nodes[0]
		}
	}

	if anyForm == nil {
		fmt.Println("\nNo forms found at all, even the bare 'form' fallback selector.")
		fmt.Println("This means EITHER the page genuinely has zero <form> elements at all")
		fmt.Println("(plausible for a pure-JS search widget with no <form> wrapper),")
		fmt.Println("OR chromedp.Nodes+AtLeast(0) isn't matching what you'd expect —")
		fmt.Println("worth checking the page's actual HTML (view-source or devtools)")
		fmt.Println("to see whether a <form> tag is present at all.")
		fmt.Println("\nLeaving browser open for 10s for manual inspection (open devtools now)...")
		time.Sleep(10 * time.Second)
		return
	}

	fmt.Printf("\nFound a form (NodeID %d). Probing input selectors within it...\n", anyForm.NodeID)

	inputSelectors := []string{
		`input[type="search"]`,
		`input[name="q"]`,
		`input[name="s"]`,
		`input[type="text"]`,
	}

	for _, sel := range inputSelectors {
		probeCtx, probeCancel := phaseCtx()
		var nodes []*cdp.Node
		err := chromedp.Run(probeCtx, chromedp.Nodes(sel, &nodes, chromedp.ByQuery, chromedp.AtLeast(0), chromedp.FromNode(anyForm)))
		probeCancel()
		fmt.Printf("  %-25q (scoped to form) -> %d matches (err: %v)\n", sel, len(nodes), err)
	}

	fmt.Println("\n--- Same input selectors, page-wide (no form scoping) ---")
	for _, sel := range inputSelectors {
		probeCtx, probeCancel := phaseCtx()
		var nodes []*cdp.Node
		err := chromedp.Run(probeCtx, chromedp.Nodes(sel, &nodes, chromedp.ByQuery, chromedp.AtLeast(0)))
		probeCancel()
		fmt.Printf("  %-25q (page-wide)      -> %d matches (err: %v)\n", sel, len(nodes), err)
	}

	fmt.Println("\n--- isVisible probe (this is the untested chromedp mechanism) ---")
	inputSel := `input[type="search"]` // both booxworm.lk and jumpbooks.lk matched this in your run
	var targetNodes []*cdp.Node
	{
		probeCtx, probeCancel := phaseCtx()
		_ = chromedp.Run(probeCtx, chromedp.Nodes(inputSel, &targetNodes, chromedp.ByQuery, chromedp.AtLeast(0), chromedp.FromNode(anyForm)))
		probeCancel()
	}
	if len(targetNodes) > 0 {
		target := targetNodes[0]
		fmt.Printf("Testing isVisible-equivalent on NodeID %d (selector %q)...\n", target.NodeID, inputSel)

		probeCtx1, probeCancel1 := phaseCtx()
		var visNodes []*cdp.Node
		err := chromedp.Run(probeCtx1, chromedp.Nodes([]cdp.NodeID{target.NodeID}, &visNodes, chromedp.ByNodeID, chromedp.NodeVisible))
		probeCancel1()
		fmt.Printf("  ByNodeID+NodeVisible -> %d matches, err: %v\n", len(visNodes), err)

		probeCtx2, probeCancel2 := phaseCtx()
		var jsVisible bool
		jsErr := chromedp.Run(probeCtx2, chromedp.EvaluateAsDevTools(
			fmt.Sprintf(`(function(){var els=document.querySelectorAll(%q); if(!els.length) return false; var el=els[0]; var r=el.getBoundingClientRect(); return !!(r.width || r.height) && window.getComputedStyle(el).visibility !== 'hidden';})()`, inputSel),
			&jsVisible))
		probeCancel2()
		fmt.Printf("  JS getBoundingClientRect/computedStyle check -> visible=%v, err: %v\n", jsVisible, jsErr)

		shortCtx, shortCancel := context.WithTimeout(ctx, 2*time.Second)
		waitErr := chromedp.Run(shortCtx, chromedp.WaitVisible(inputSel, chromedp.ByQuery))
		shortCancel()
		fmt.Printf("  WaitVisible(2s timeout) -> err: %v (nil = visible)\n", waitErr)
	} else {
		fmt.Println("  (no target input node resolved for this probe, skipping)")
	}

	fmt.Println("\n--- fillAndSubmit probe (SetValue/SendKeys with *cdp.Node vs selector+FromNode) ---")
	if len(targetNodes) > 0 {
		target := targetNodes[0]

		// CONFIRMED FINDING (from a live run against booxworm.lk): passing
		// a raw *cdp.Node as chromedp.SetValue's sel argument does NOT
		// work the way chromedp.Click/MouseClickNode's node-accepting
		// overload does — it silently falls through to a text-search
		// (DOM.performSearch) using the node's %v-formatted struct dump as
		// the literal query, which never matches and retries forever. This
		// bug was real and already present in session.go's fillAndSubmit()
		// before this diagnostic exposed it — now fixed there via the
		// selector+FromNode pattern (Approach B below), which IS confirmed
		// correct. Approach A is kept here only as a short-timeout
		// regression check that the failure mode is what we think it is,
		// not as something expected to succeed.
		shortTimeout := 3 * time.Second
		fmt.Println("Approach A: SetValue(node, ...) — known-broken pattern, 3s timeout expected to fail fast")
		aCtx, aCancel := context.WithTimeout(ctx, shortTimeout)
		errA := chromedp.Run(aCtx, chromedp.SetValue(target, "DIAG_TEST_A"))
		aCancel()
		rbCtx1, rbCancel1 := phaseCtx()
		var valA string
		_ = chromedp.Run(rbCtx1, chromedp.EvaluateAsDevTools(
			fmt.Sprintf(`document.querySelectorAll(%q)[0].value`, inputSel), &valA))
		rbCancel1()
		fmt.Printf("  SetValue(node,...) err=%v, readback value=%q (expected to FAIL — this pattern is confirmed broken)\n", errA, valA)

		clearCtx, clearCancel := phaseCtx()
		_ = chromedp.Run(clearCtx, chromedp.EvaluateAsDevTools(
			fmt.Sprintf(`document.querySelectorAll(%q)[0].value = ''`, inputSel), nil))
		clearCancel()

		fmt.Println("\nApproach B: SetValue(selector, ..., FromNode(form)) — the proven-working query pattern")
		bCtx, bCancel := phaseCtx()
		errB := chromedp.Run(bCtx, chromedp.SetValue(inputSel, "DIAG_TEST_B", chromedp.ByQuery, chromedp.FromNode(anyForm)))
		bCancel()
		rbCtx2, rbCancel2 := phaseCtx()
		var valB string
		_ = chromedp.Run(rbCtx2, chromedp.EvaluateAsDevTools(
			fmt.Sprintf(`document.querySelectorAll(%q)[0].value`, inputSel), &valB))
		rbCancel2()
		fmt.Printf("  SetValue(selector,FromNode) err=%v, readback value=%q (expected \"DIAG_TEST_B\")\n", errB, valB)

		fmt.Println("\nApproach C: SendKeys(selector, \"\\r\", FromNode) — using the CONFIRMED-WORKING pattern, since SendKeys(node,...) shares SetValue's bug")
		locCtx1, locCancel1 := phaseCtx()
		var beforeURL string
		_ = chromedp.Run(locCtx1, chromedp.Location(&beforeURL))
		locCancel1()

		cCtx, cCancel := phaseCtx()
		errC := chromedp.Run(cCtx, chromedp.SendKeys(inputSel, "\r", chromedp.ByQuery, chromedp.FromNode(anyForm)))
		cCancel()

		time.Sleep(1500 * time.Millisecond)

		locCtx2, locCancel2 := phaseCtx()
		var afterURL string
		_ = chromedp.Run(locCtx2, chromedp.Location(&afterURL))
		locCancel2()
		fmt.Printf("  SendKeys(node,\"\\r\") err=%v, URL before=%q after=%q (changed=%v)\n", errC, beforeURL, afterURL, beforeURL != afterURL)
	} else {
		fmt.Println("  (no target input node resolved for this probe, skipping)")
	}

	fmt.Println("\nLeaving browser open for 10s for manual inspection...")
	time.Sleep(10 * time.Second)
}