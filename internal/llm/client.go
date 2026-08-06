// Package llm provides a thin, role-agnostic OpenAI-compatible
// chat-completions client. It has no knowledge of prompts, HTML cleaning,
// or retry/fallback strategy — that dispatch logic (Extractor._call_llm /
// _attempt in llm_extractor.py) belongs in pipeline/extractor.go, which
// will wrap this client. See vestige_go_pipeline_implementation.md
// Section 1/3.
//
// Source-verified against llm_extractor.py's Extractor.__init__ (client
// construction) and the two call sites that hit the chat.completions.create
// API directly: Extractor._attempt (json_mode-toggling selector/detail
// extraction) and Extractor.classify_stock_status (fixed max_tokens=5, no
// json_mode). Only those two call shapes are what this client needs to
// support; nothing here fabricates behavior beyond what those two call
// sites actually use.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is one entry in a chat-completions request's messages array.
// Mirrors the {"role": ..., "content": ...} dicts built inline in
// llm_extractor.py (e.g. _attempt's messages list, classify_stock_status's
// messages list).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat mirrors _attempt's conditional
// kwargs["response_format"] = {"type": "json_object"} — only ever sent
// when json_mode=True.
type ResponseFormat struct {
	Type string `json:"type"`
}

// ChatCompletionRequest mirrors the kwargs dict built in _attempt, plus
// the max_tokens field classify_stock_status adds that _attempt never
// sets. Temperature is always 0.0 on both real call sites, but is exposed
// (not hardcoded) here since it's a request parameter, not something the
// client should silently decide.
//
// MaxTokens and ResponseFormat are pointers so "not set" (Python simply
// omitting the kwarg) is distinguishable from "set to zero"/"set to the
// zero-value struct" — matching how _attempt only adds response_format to
// kwargs conditionally, and classify_stock_status is the only call site
// that ever sets max_tokens at all.
type ChatCompletionRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	MaxTokens      *int            `json:"max_tokens,omitempty"`
}

// ChatCompletionChoice and ChatCompletionResponse mirror only the fields
// this project's two call sites actually read off the openai-python
// response object: response.choices[0].message.content, and (for logging
// only) response.model. Nothing else on the real OpenAI response shape is
// modeled, since nothing else is used.
type ChatCompletionChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type ChatCompletionResponse struct {
	Model   string                  `json:"model"`
	Choices []ChatCompletionChoice  `json:"choices"`
	Error   *ChatCompletionAPIError `json:"error,omitempty"`
}

// ChatCompletionAPIError mirrors the `response.error` shape _attempt
// defensively checks for (hasattr(response, "error") and response.error)
// on a malformed/error response from a non-strictly-OpenAI-compatible
// backend. openai-python's own client doesn't surface this on the typed
// response object in practice, but some OpenAI-compatible backends (per
// this project's role-based multi-provider design) return an inline error
// field instead of a proper HTTP error status — this exists to let a
// caller reproduce _attempt's err_msg branch faithfully.
type ChatCompletionAPIError struct {
	Message string `json:"message"`
}

// Client is a thin OpenAI-compatible chat-completions client bound to one
// role's credentials (SELECTOR_* or DIRECT_*) — direct port of
// Extractor.__init__'s self.client = OpenAI(base_url=..., api_key=...)
// construction, minus the prompt/engine/cleaning fields that stay in
// extractor.go's Extractor struct.
type Client struct {
	apiBase    string
	apiKey     string
	httpClient *http.Client
}

// defaultTimeout is used when NewClient is called with timeout <= 0.
// CodeRabbit-flagged: http.Client{Timeout: 0} means NO timeout in Go's
// stdlib (unlike a zero-value default meaning "use something sensible") —
// a caller passing an unset/zero time.Duration (e.g. a missing settings
// field) would silently get a client that can hang forever on a stuck LLM
// backend, blocking a pipeline run indefinitely with no way to recover
// short of killing the process. This doc comment on NewClient already said
// "callers should pick a reasonable value explicitly" but nothing actually
// enforced that — this constant plus the guard below does.
//
// Set to match this project's existing convention rather than picking a
// fresh value: discovery.go's discoveryLLMTimeout is 120s (chosen because
// selector discovery sends a full HTML subtree plus a long tuned prompt to
// a model that can be slow), and orchestrator.go's Path D uses
// directExtractionLLMTimeout at the same scale. A shorter fallback here
// (an earlier draft used 60s) would risk timing out exactly the kind of
// slow-but-legitimate call this default exists to protect, if it were ever
// actually hit — so this stays consistent with the real call sites instead
// of introducing a third, inconsistent value.
const defaultTimeout = 120 * time.Second

// NewClient constructs a Client. Mirrors Extractor.__init__'s validation
// exactly: apiBase and modelName are both required by the Python
// constructor (raises ValueError if either is missing) — but modelName is
// per-request in this port (see CreateChatCompletion's model parameter),
// not stored on the client, so it is NOT validated here. Callers
// constructing a Client from settings must still enforce "apiBase and
// modelName both present" themselves before issuing a request, matching
// the same "no default LLM endpoint is assumed" principle the Python
// constructor enforces up front.
//
// apiKey follows Python's `config.get("api_key") or "not-needed"` — an
// empty apiKey is replaced with the literal placeholder "not-needed"
// (some OpenAI-compatible backends, e.g. local Ollama, require *a*
// non-empty Authorization value even when no real key is needed).
//
// timeout has no Python equivalent — openai-python's default client
// timeout was never overridden in llm_extractor.py, so this is a new,
// additive parameter rather than a ported one; callers should pick a
// reasonable value explicitly rather than relying on a silent default.
// A timeout <= 0 (including an unset zero-value time.Duration) is NOT
// passed through to http.Client as-is — Go's http.Client treats Timeout:0
// as "no timeout at all," the opposite of a safe default — and is instead
// replaced with defaultTimeout, matching the doc comment's own stated
// expectation rather than silently trusting every caller to honor it.
func NewClient(apiBase, apiKey string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(apiBase) == "" {
		return nil, fmt.Errorf("llm: apiBase is required — no default LLM endpoint is assumed")
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = "not-needed"
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		apiBase:    strings.TrimRight(apiBase, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

// CreateChatCompletion issues a single POST to {apiBase}/chat/completions
// and returns the decoded response. This is the raw, single-attempt
// primitive — direct equivalent of
// self.client.chat.completions.create(**kwargs) as called from within
// _attempt and classify_stock_status. It does NOT implement the
// json_mode-then-plain retry/fallback behavior of _call_llm, and does NOT
// implement the ```-fence-stripping/json.loads parsing _attempt does on
// the result — both of those are prompt-aware dispatch concerns that
// belong to extractor.go's Extractor, which wraps this client.
//
// An HTTP-level failure (network error, non-2xx status, malformed JSON
// body) returns a Go error — the equivalent of an exception raised out of
// self.client.chat.completions.create(...) in Python, which is exactly
// what _call_llm's try/except is built to catch. A response that decodes
// successfully but is missing choices/content is NOT an error here (it
// returns a normal ChatCompletionResponse for the caller to inspect) —
// matching _attempt's own "if not response or choices is None: return
// {"error": ...}" branch, which detects a malformed-but-successfully-
// parsed response WITHOUT raising, and therefore does NOT trigger
// _call_llm's json_mode fallback retry. Preserving that distinction is the
// caller's responsibility (in extractor.go), not this method's.
func (c *Client) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("llm: failed to encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: failed to build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm: failed to read response body: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: backend returned HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("llm: failed to decode response JSON: %w", err)
	}

	return &result, nil
}