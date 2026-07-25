// Package pipeline holds the Orchestrator/Crawler/Scraper/Extractor/
// Discoverer control-flow code — the browser/DB-touching layer that calls
// into the pure functions in pipeline/heuristics. See
// vestige_go_pipeline_implementation.md Section 1 for the full package
// layout this file is part of.
//
// Source-verified against llm_extractor.py in full, plus llm.Client
// (client.go — thin OpenAI-compatible chat-completions client, scoped
// deliberately to exclude the retry-dispatch and fence/json parsing logic
// this file supplies) and scraper.go's SelectorConfig (the map shape
// scraper.go's extractData actually consumes — ExtractSelectors's return
// type is chosen to match that shape directly, not invented separately).
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/net/html"

	"github.com/udanfernando2006/vestige-go/internal/llm"
	"github.com/udanfernando2006/vestige-go/internal/pipeline/heuristics"
)

// --- Prompts -----------------------------------------------------------
//
// Carried over verbatim from llm_extractor.py's SEMANTIC_SYSTEM_PROMPT /
// SEMANTIC_USER_PROMPT / SELECTOR_SYSTEM_PROMPT / SELECTOR_USER_PROMPT.
// These are tuned prompt text, not code — no wording changes, no
// "improvements," since any rewording risks silently regressing real LLM
// output quality (framework-hash wildcarding, anti-installment-widget
// rules, ISBN-13-priority instructions, etc. are all load-bearing English,
// not decoration). Go's %s-via-fmt.Sprintf stands in for Python's
// str.format(); {{ }} literal-brace escaping in the Python source (used
// for the literal JSON schema examples inside an str.format() template)
// is simply literal { } here, since Go's fmt verbs don't need brace
// escaping the way Python's format mini-language does.

const semanticSystemPrompt = `You are a highly precise, data-focused extraction engine. Your only task is to convert raw semantic text layout data into structured JSON matching the requested schema. Do not write explanations, markdown syntax code blocks around the JSON object, or conversational replies. Return ONLY raw JSON.`

const semanticUserPromptTemplate = `Target Book Title: "%s"

Task: Read the following semantic HTML context and extract specific details for this target book.

Rules:
1. Only extract data that directly belongs to the main book profile.
2. Completely ignore surrounding site furniture (sidebars, footer links, header menus, payment installment plans, share buttons, tags, or categories).
3. Price: Extract the exact raw text for the price including currency symbols (e.g., "LKR 3,303.00", "රු 2,995.00").
4. Stock Status: Extract the precise stock state (e.g., "In stock", "Out of Stock", "Sold Out").
5. Description: Extract ONLY the narrative book summary / blurb text. Format this string into clean markdown paragraphs (using standard newlines). Exclude titles, authors, prices, or store notices from this block. Keep it focused and descriptive.
6. ISBN (CRITICAL VALUE LINKING):
   - Locate the exact 10 or 13-digit commercial identifier on the page. If both are present, prioritize the 13-digit ISBN.
   - Look sequentially: It will appear directly inside the text node immediately following a label like "ISBN-13:", "ISBN:", "ISBN 10:", or "SKU:".
   - Example sequence: If you see "<li><strong>ISBN-13:</strong> 978...</li>", the sequence of numbers after the tag is the value. Capture it explicitly.
   - If you see scientific notation (like 9.78147E+12), convert it back into full text digits. If completely absent, return null.

ONLY RETURN THE FIELDS IN THIS LIST, DON'T INCLUDE ANY OF THE OTHERS: %s
Expected JSON Schema:
{
  "price": "string or null",
  "stock_status": "string or null",
  "description": "string or null (formatted as clean markdown paragraphs)",
  "isbn": "string or null"
}

HTML Content:
%s`

const selectorSystemPrompt = `You are an advanced web-scraping metadata generator. Your sole function is to analyze raw HTML DOM structures and output a clean, compliant JSON extraction map tailored for a custom BeautifulSoup scraping pipeline.

You must strictly output raw JSON matching the requested schema. Do not write markdown blocks, explanations, or code commentary.`

const selectorUserPromptTemplate = `Target Book Title: "%s"

Task: Analyze the provided HTML context and generate the exact JSON selection criteria required to extract target fields for this book profile.

Target Fields to Extract:
- "title"
- "price"
- "availability"
- "isbn"
- "description"

Extraction Rulebook:
1. CSS Selectors Strategy:
   - For standard layout wrappers, use the "selector" key with a valid CSS selector string suitable for BeautifulSoup's ` + "`soup.select_one()`" + `.
   - If an element uses multiple classes, chain them using periods without spaces.
   - Avoid unstable, heavily auto-generated platform classes if structural class names are available.

2. Price Target Isolation (CRITICAL):
   - Locate the primary retail purchase price container for the book.
   - NEVER target secondary promotional pricing, credit card discount schedules, bank partnership rates, or installment plans (e.g., Koko, Mintpay, or payment installment preview widgets).
   - If a selector contains text like "payment", "installment", "preview", "card", or "discount", it is WRONG. Skip it and find the standalone retail price wrapper.

3. Framework Resiliency Rules (CRITICAL FOR DYNAMIC HASHES):
   - Modern JS Frameworks (Next.js, Nuxt, React) append unique production compilation hashes to classes (e.g., class="ProductInner_productinnerwrap_price__gttmW").
   - NEVER match against these temporary trailing suffixes. Instead, instantly switch to a CSS partial attribute wildcard identifier (` + "`*=`" + `).
   - Example: Instead of writing ` + "`div.ProductInner_productinnerwrap_price__gttmW`" + `, you MUST output ` + "`div[class*='ProductInner_productinnerwrap_price']`" + `.

4. Target Containers Directly:
   - Do NOT append unverified structural layout tags like ` + "`strong`, `span`, or `b`" + ` to the end of a selector unless that component explicitly wraps the target text exclusively inside the layout tree.

5. Text-Lookup Fallback Strategy ("find_by_text"):
   - If a target field (especially "isbn") lives in a complex data grid or specification table with inner layout noise (like structural SVGs, inline spans, or icons), do NOT use rigid nth-child rules.
   - Use the text-lookup fallback format to track the semantic label directly:
     "find_by_text": ["tag_name_wrapping_text", "Exact/Sub-String Label Text"]
     "then_next": "target_value_tag_name"
   - Priority Constraint for ISBN: If both an "ISBN" (10-digit) and "ISBN 13" (13-digit) label are available, ALWAYS explicitly target the "ISBN 13" option to capture clean standard commercial records.
   - Example for <tr><th><svg></svg><span>ISBN 13</span></th><td>: 978...</td></tr>:
     "find_by_text": ["span", "ISBN 13"], "then_next": "td"
   - Try looking for simpler elements which house the same ISBN, so that the selector is not dependent on the presence of specific structural tags. For instance, if the page has a clean <li>ISBN 13: 978...</li> element, prefer that with "find_by_text": ["li", "ISBN 13"] and no "then_next" traversal. Or if another element with a class has the ISBN, target that with a direct "selector" strategy.

6. Extraction Behavior Flags:
   - For the "price" field, append ` + "`\"direct_text\": true`" + ` to capture only its immediate inner string value.
   - For the "description" field, always include ` + "`\"preserve_semantics\": true`" + `.

Expected JSON Structure Output:
{
  "selectors": {
    "title": { "selector": "string" },
    "price": { "selector": "string", "direct_text": true },
    "availability": { "selector": "string" },
    "isbn": { "find_by_text": ["tag", "text"], "then_next": "tag" },
    "description": { "selector": "string", "preserve_semantics": true }
  }
}

If a field is entirely absent from the HTML context, return its field value configuration block as null.

HTML Content:
%s`

// --- Config / construction ----------------------------------------------

// Engine selects clean_html()'s attribute-stripping behavior. Fixed,
// not user-configurable — direct port of the "full" vs "stripped"
// distinction documented in vestige_guide.md Section 5 ("Engine modes
// (fixed, not configurable)").
type Engine string

const (
	// EngineStripped strips all attributes from every tag — used for
	// Path D direct extraction, where small-model context economy
	// matters more than selector-relevant attributes.
	EngineStripped Engine = "stripped"
	// EngineFull keeps only class/id attributes — used for selector
	// discovery, since generated selectors require class/id to exist
	// in the source HTML.
	EngineFull Engine = "full"
)

// ExtractorConfig mirrors Extractor.__init__'s config dict fields
// (engine, api_base, api_key, model_name).
type ExtractorConfig struct {
	Engine    Engine
	APIBase   string
	APIKey    string
	ModelName string
}

// chatCompleter is the minimal interface Extractor actually calls against
// llm.Client — extracted (not present in client.go itself) specifically
// so tests can substitute a fake, mirroring the existing browser.Session
// interface/fakeSession convention used elsewhere in this project
// (fakes_test.go). *llm.Client satisfies this automatically since its
// CreateChatCompletion method already matches this shape; no change to
// client.go itself is required.
type chatCompleter interface {
	CreateChatCompletion(ctx context.Context, req llm.ChatCompletionRequest) (*llm.ChatCompletionResponse, error)
}

// Extractor wraps an llm.Client with the prompt/HTML-cleaning/dispatch
// logic that client.go deliberately excludes. Direct port of
// llm_extractor.py's Extractor class.
type Extractor struct {
	engine    Engine
	modelName string
	client    chatCompleter
}

// NewExtractor constructs an Extractor. Mirrors Extractor.__init__'s
// validation exactly: api_base and model_name are both required (Python
// raises ValueError at construction if either is missing) — "no default
// LLM endpoint is assumed" is preserved as a hard error here, not a
// silently-defaulted zero value. api_key falls back to the same
// "not-needed" placeholder Python uses, handled inside llm.NewClient.
//
// engine defaults to EngineStripped if unset (empty string), mirroring
// Python's config.get("engine", "stripped") default.
//
// client is accepted as the chatCompleter interface rather than the
// concrete *llm.Client so callers can pass a fake in tests; any real
// *llm.Client value satisfies this automatically with no call-site
// change required — see chatCompleter's doc comment.
func NewExtractor(cfg ExtractorConfig, client chatCompleter) (*Extractor, error) {
	if strings.TrimSpace(cfg.APIBase) == "" || strings.TrimSpace(cfg.ModelName) == "" {
		return nil, fmt.Errorf(
			"extractor: requires api_base and model_name — no default LLM " +
				"endpoint is assumed. Check that SELECTOR_API_BASE/SELECTOR_MODEL " +
				"or DIRECT_API_BASE/DIRECT_MODEL are set",
		)
	}
	engine := cfg.Engine
	if engine == "" {
		engine = EngineStripped
	}
	return &Extractor{
		engine:    engine,
		modelName: cfg.ModelName,
		client:    client,
	}, nil
}

// --- clean_html() ---------------------------------------------------
//
// The scoping logic this comment used to describe in detail (re-scoping
// via *goquery.Selection.Find/Each rather than Python's
// re-parse-from-string BeautifulSoup(str(core_element), "lxml") trick,
// since goquery has no direct equivalent of "select a subtree, then
// treat it as its own fresh document") now lives in
// heuristics.ScopeToMainContent — see that function's doc comment for
// the full reasoning, unchanged from when it lived here. The
// "judged behaviorally equivalent but not yet byte-for-byte live-
// verified" caveat that used to apply to this file's own copy of that
// logic has since been addressed differently than originally planned:
// rather than confirming CleanHTML's OWN output byte-for-byte, live
// testing against a real jumpbooks.lk page (14 duplicate
// ".woocommerce-Price-amount" matches across the full page) surfaced a
// more important finding — the scoped view was never the thing that
// needed re-verifying; the actual bug was that Scraper was matching
// selectors against the FULL unscoped page instead of this same scoped
// view. See ScopeToMainContent's doc comment and this file's own
// CleanHTML below for the fix.

// allowedFullAttrs mirrors clean_html()'s allowed_attrs = ["class", "id"]
// for the "full" engine branch. LLM-attribute-policy concern only —
// deliberately NOT moved to heuristics.go alongside tagsToRemove/
// noiseSelectors, since Scraper's real selector-matching path wants
// every attribute intact, not just class/id.
var allowedFullAttrs = map[string]bool{"class": true, "id": true}

// CleanHTML is a direct port of Extractor.clean_html(), with its scoping
// logic (tag removal, <main>/id-regex scoping, in-scope noise removal)
// now factored out into heuristics.ScopeToMainContent — see that
// function's doc comment for the full "why" (bug: a selector reasoned
// about against a scoped excerpt was being evaluated for real against
// the full unscoped page). CleanHTML's own remaining job is purely
// LLM-facing: take the shared scoped view and apply engine-specific
// attribute stripping (all attributes removed for "stripped", only
// class/id kept for "full"), then render back to a string for the
// prompt. No behavioral change to CleanHTML's own output for any
// existing caller — same tag list, same scoping rule, same noise list,
// same attribute policy; only WHERE steps 1-3 live has changed.
func (e *Extractor) CleanHTML(rawHTML string) (string, error) {
	scope, err := heuristics.ScopeToMainContent(rawHTML)
	if err != nil {
		return "", fmt.Errorf("extractor: %w", err)
	}

	// Render the scoped subtree (or the whole document, if no core
	// container was found) back to a string, then apply engine-specific
	// attribute stripping directly on the raw *html.Node tree — goquery
	// has no bulk "set attrs" API, so this walks nodes manually.
	nodesToStrip := scope.Nodes

	for _, n := range nodesToStrip {
		stripAttrs(n, e.engine)
	}

	out, err := renderNodes(nodesToStrip)
	if err != nil {
		return "", fmt.Errorf("extractor: failed to render cleaned HTML: %w", err)
	}
	return out, nil
}

// stripAttrs walks n and all descendants, applying the engine's
// attribute-keep policy in place. Mirrors clean_html()'s two branches:
// EngineStripped clears tag.attrs entirely; EngineFull keeps only
// class/id.
func stripAttrs(n *html.Node, engine Engine) {
	if n.Type == html.ElementNode {
		if engine == EngineStripped {
			n.Attr = nil
		} else {
			kept := n.Attr[:0]
			for _, a := range n.Attr {
				if allowedFullAttrs[a.Key] {
					kept = append(kept, a)
				}
			}
			n.Attr = kept
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		stripAttrs(c, engine)
	}
}

// renderNodes renders a slice of top-level *html.Node back to a single
// HTML string, mirroring Python's final `return str(soup)`.
func renderNodes(nodes []*html.Node) (string, error) {
	var sb strings.Builder
	for _, n := range nodes {
		if err := html.Render(&sb, n); err != nil {
			return "", err
		}
	}
	return sb.String(), nil
}

// --- ExtractDetails (Path D) ---------------------------------------

// DetailsResult mirrors extract_details()'s returned dict shape and the
// SEMANTIC_USER_PROMPT's documented JSON schema exactly (price,
// stock_status, description, isbn — all nullable strings).
type DetailsResult struct {
	Price       *string `json:"price"`
	StockStatus *string `json:"stock_status"`
	Description *string `json:"description"`
	ISBN        *string `json:"isbn"`
}

// defaultDetailFields mirrors extract_details()'s
// fields = ["price", "stock_status", "description", "isbn"] default.
var defaultDetailFields = []string{"price", "stock_status", "description", "isbn"}

// ExtractDetails is a direct port of Extractor.extract_details(). fields
// may be nil, matching Python's `if not fields:` default-substitution
// check.
//
// Returns (nil, err) on any dispatch/parse failure — corresponding to
// Python's `{"error": ...}` dict return, which callers there check via
// `"error" in raw_response`. Go surfaces the same failure as a genuine
// error instead, since a Go caller checking a typed result's error field
// (as Python does) has no equivalent without an awkward sentinel field on
// DetailsResult itself; callers porting logic that branched on
// `"error" in raw_response` should branch on `err != nil` instead.
func (e *Extractor) ExtractDetails(ctx context.Context, cleanedHTML, targetTitle string, fields []string) (*DetailsResult, error) {
	if len(fields) == 0 {
		fields = defaultDetailFields
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("extractor: failed to encode fields list: %w", err)
	}

	userContent := fmt.Sprintf(semanticUserPromptTemplate, targetTitle, string(fieldsJSON), cleanedHTML)

	raw, err := e.callLLM(ctx, userContent, semanticSystemPrompt)
	if err != nil {
		return nil, err
	}

	var result DetailsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("extractor: failed to decode details JSON: %w", err)
	}
	return &result, nil
}

// --- ExtractSelectors (selector discovery) --------------------------

// selectorsResponseEnvelope mirrors the top-level {"selectors": {...}}
// wrapper extract_selectors() unwraps via raw_response.get("selectors", {}).
type selectorsResponseEnvelope struct {
	Selectors map[string]*SelectorConfig `json:"selectors"`
}

// ExtractSelectors is a direct port of Extractor.extract_selectors().
//
// Returns map[string]*SelectorConfig directly — the exact shape
// scraper.go's Scrape/extractData already consumes (see SelectorConfig's
// doc comment in scraper.go) — rather than a generic map[string]any, so
// this function's output can be passed straight into Scraper.Scrape with
// no intermediate translation layer. This is a deliberate design choice
// beyond a literal Python port, made because the real consumer shape was
// available to verify against (scraper.go), not fabricated from prompt
// text alone.
func (e *Extractor) ExtractSelectors(ctx context.Context, cleanedHTML, targetTitle string) (map[string]*SelectorConfig, error) {
	userContent := fmt.Sprintf(selectorUserPromptTemplate, targetTitle, cleanedHTML)

	raw, err := e.callLLM(ctx, userContent, selectorSystemPrompt)
	if err != nil {
		return nil, err
	}

	var envelope selectorsResponseEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("extractor: failed to decode selectors JSON: %w", err)
	}
	if envelope.Selectors == nil {
		return map[string]*SelectorConfig{}, nil
	}
	return envelope.Selectors, nil
}

// --- ClassifyStockStatus (v3.9 short-text fallback) -------------------

// ClassifyStockStatus is a direct port of Extractor.classify_stock_status().
//
// Lightweight fallback for a stock-status string that neither the
// hardcoded phrase list nor any CUSTOM_STOCK_*_PATTERNS regex could
// classify (heuristics.ClassifyStockText returning nil upstream). Takes
// only the short raw text, never HTML — CleanHTML/e.engine are
// irrelevant here, so this behaves identically regardless of which
// role's credentials (DIRECT_* or SELECTOR_*) constructed this Extractor,
// exactly as documented in llm_extractor.py's own docstring.
//
// Returns (true/false, nil) on a clear classification, or (false, nil)
// with the second return distinguishing "unknown" — see the doc on the
// return signature below. Never returns a Go error for a failed/
// ambiguous LLM call; mirrors Python's `except Exception: return None`
// and "UNKNOWN"/unrecognized-response branches, both of which return
// None, not raise.
//
// Return shape note: Python returns `bool | None`. Go's zero-value bool
// can't represent "unknown" distinctly from "false" on its own, so this
// returns (*bool, error) where a nil *bool return value (with a nil
// error) is the direct equivalent of Python's None — NOT an error
// condition. Callers should check for a nil pointer, not a non-nil
// error, to detect the "couldn't classify" case; a non-nil error here
// would only ever come from a context-cancellation-style Go-specific
// failure that Python's implementation has no equivalent path for.
func (e *Extractor) ClassifyStockStatus(ctx context.Context, rawText string) (*bool, error) {
	maxTokens := 5
	req := llm.ChatCompletionRequest{
		Model: e.modelName,
		Messages: []llm.Message{
			{
				Role: "system",
				Content: "You are a retail stock-status classifier. Read the " +
					"text and reply with exactly one word: TRUE if the " +
					"item can currently be purchased, FALSE if it is out " +
					"of stock, or UNKNOWN if you cannot tell.",
			},
			{Role: "user", Content: fmt.Sprintf("Text: %s", rawText)},
		},
		Temperature: 0.0,
		MaxTokens:   &maxTokens,
	}

	resp, err := e.client.CreateChatCompletion(ctx, req)
	if err != nil {
		// Mirrors `except Exception as e: print(f"[LLM Fallback Error]: {e}", ...); return None`.
		// Note the Python message here uses a different prefix
		// ("[LLM Fallback Error]") than the other four branches in this
		// method ("[Stock Classify]") — that's not a typo I'm
		// normalizing away, it's what the real source does, preserved
		// verbatim.
		fmt.Fprintf(os.Stderr, "[LLM Fallback Error]: %v\n", err)
		return nil, nil
	}

	if resp == nil || len(resp.Choices) == 0 {
		fmt.Fprintf(os.Stderr, "[Stock Classify] %s: no choices returned for '%s'\n", e.modelName, rawText)
		return nil, nil
	}

	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if content == "" {
		fmt.Fprintf(os.Stderr, "[Stock Classify] %s: empty content for '%s'\n", e.modelName, rawText)
		return nil, nil
	}

	result := strings.ToUpper(content)
	switch {
	case strings.Contains(result, "TRUE"):
		fmt.Fprintf(os.Stderr, "[Stock Classify] %s: '%s' -> TRUE (in stock)\n", e.modelName, rawText)
		v := true
		return &v, nil
	case strings.Contains(result, "FALSE"):
		fmt.Fprintf(os.Stderr, "[Stock Classify] %s: '%s' -> FALSE (out of stock)\n", e.modelName, rawText)
		v := false
		return &v, nil
	default:
		fmt.Fprintf(os.Stderr, "[Stock Classify] %s: '%s' -> unrecognized response '%s'\n", e.modelName, rawText, result)
		return nil, nil
	}
}

// --- LLM dispatch: _call_llm / _attempt ------------------------------

// callLLM is a direct port of Extractor._call_llm(): attempts once with
// json_mode=true, and on ANY failure (HTTP error, malformed response,
// JSON parse failure) retries once with json_mode=false, matching
// Python's bare `except Exception:` catching everything from the first
// attempt indiscriminately. Returns the raw JSON bytes on success, or a
// wrapped error on double failure — mirroring `{"error": f"LLM Request
// Failure: {e}"}` but as a genuine Go error rather than an in-band error
// dict, for the same reasoning given in ExtractDetails's doc comment.
func (e *Extractor) callLLM(ctx context.Context, userContent, systemPrompt string) ([]byte, error) {
	raw, err := e.attempt(ctx, userContent, systemPrompt, true)
	if err == nil {
		return raw, nil
	}

	raw, err2 := e.attempt(ctx, userContent, systemPrompt, false)
	if err2 == nil {
		return raw, nil
	}

	return nil, fmt.Errorf("extractor: LLM request failure (json_mode attempt: %v; plain attempt: %w)", err, err2)
}

// attempt is a direct port of Extractor._attempt(). Issues one
// chat-completions call, validates the response shape, strips a
// ```-fenced code block if present (mirroring the raw_content.startswith
// ("```") branch), and returns the raw (still-unparsed) JSON bytes for
// the caller to unmarshal into its own typed result — deliberately NOT
// unmarshaled here, since callLLM's callers (ExtractDetails,
// ExtractSelectors) each need a different target type.
//
// Distinguishes two failure classes exactly as Python's _attempt does:
//   - A malformed-but-successfully-received response (no choices, empty
//     content) returns {"error": ...} in Python WITHOUT raising — this
//     does NOT trigger _call_llm's json_mode fallback retry in Python.
//     Preserving that exact distinction in Go (a "malformed response"
//     case that should NOT trigger callLLM's retry) is not fully
//     achievable with a single Go error type without more context on how
//     this is consumed upstream — flagging as a known, deliberate
//     simplification: this port treats a malformed response the same as
//     any other failure (both return a Go error, both DO trigger
//     callLLM's fallback retry), which is a narrow behavioral divergence
//     from Python's "malformed response = no retry" rule. Revisit if a
//     real backend is observed returning a malformed-but-200 response in
//     practice, since today this only diverges in that specific edge
//     case, not in the common success/HTTP-failure paths.
//   - An HTTP-level or network-level failure (a real exception in
//     Python, a non-nil error from CreateChatCompletion in Go) DOES
//     trigger the fallback retry in both languages — this path is
//     faithfully preserved.
func (e *Extractor) attempt(ctx context.Context, userContent, systemPrompt string, jsonMode bool) ([]byte, error) {
	req := llm.ChatCompletionRequest{
		Model: e.modelName,
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
		Temperature: 0.0,
	}
	if jsonMode {
		req.ResponseFormat = &llm.ResponseFormat{Type: "json_object"}
	}

	resp, err := e.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}

	if resp == nil || resp.Choices == nil {
		if resp != nil && resp.Error != nil {
			return nil, fmt.Errorf("extractor: LLM backend error: %s", resp.Error.Message)
		}
		return nil, fmt.Errorf("extractor: LLM endpoint returned a malformed response (no 'choices' field)")
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("extractor: LLM endpoint returned an empty choices array")
	}

	// Direct port of _attempt's resolved_model logging — this was
	// dropped in an earlier version of this file without being flagged
	// as an intentional omission; restored here to match Python exactly,
	// same stderr destination, same message shape, same fallback text
	// for a response that came back without a model field set.
	resolvedModel := resp.Model
	if resolvedModel == "" {
		resolvedModel = "Unknown Fallback Model"
	}
	fmt.Fprintf(os.Stderr, "[LLM] %s -> resolved to: %s\n", e.modelName, resolvedModel)

	rawContent := strings.TrimSpace(resp.Choices[0].Message.Content)
	if rawContent == "" {
		return nil, fmt.Errorf("extractor: model executed but returned an empty response string")
	}

	rawContent = stripCodeFence(rawContent)

	// Validate it's parseable JSON before returning, matching Python's
	// json.loads(raw_content) at the end of _attempt — a parse failure
	// here is exactly what _call_llm's except Exception catches (json.
	// JSONDecodeError is a subclass of Exception, indistinguishable from
	// any other failure in that broad except clause).
	if !json.Valid([]byte(rawContent)) {
		return nil, fmt.Errorf("extractor: model response is not valid JSON")
	}

	fmt.Fprintf(os.Stderr, "[LLM Response] %s: %s\n", e.modelName, rawContent)

	return []byte(rawContent), nil
}

// stripCodeFence mirrors _attempt's ```-fence-stripping block: if the
// content starts with a fenced code block, the first and last fence
// lines are dropped and the remainder is re-joined and trimmed. Only
// strips a leading fence if raw_content.startswith("```") — a fence
// appearing mid-string is left alone, matching Python exactly.
func stripCodeFence(raw string) string {
	if !strings.HasPrefix(raw, "```") {
		return raw
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "```") {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
