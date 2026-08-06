package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/udanfernando2006/vestige-go/internal/llm"
)

func newTestExtractor(t *testing.T, fake *fakeChatCompleter) *Extractor {
	t.Helper()
	ex, err := NewExtractor(ExtractorConfig{
		Engine:    EngineStripped,
		APIBase:   "https://example.invalid",
		ModelName: "test-model",
	}, fake)
	if err != nil {
		t.Fatalf("NewExtractor failed: %v", err)
	}
	return ex
}

// --- NewExtractor construction validation -----------------------------

func TestNewExtractor_RequiresAPIBaseAndModel(t *testing.T) {
	fake := &fakeChatCompleter{}

	if _, err := NewExtractor(ExtractorConfig{ModelName: "m"}, fake); err == nil {
		t.Error("expected error when api_base is missing, got nil")
	}
	if _, err := NewExtractor(ExtractorConfig{APIBase: "https://x"}, fake); err == nil {
		t.Error("expected error when model_name is missing, got nil")
	}
	if _, err := NewExtractor(ExtractorConfig{APIBase: "https://x", ModelName: "m"}, fake); err != nil {
		t.Errorf("expected no error with both fields present, got %v", err)
	}
}

func TestNewExtractor_DefaultsEngineToStripped(t *testing.T) {
	fake := &fakeChatCompleter{}
	ex, err := NewExtractor(ExtractorConfig{APIBase: "https://x", ModelName: "m"}, fake)
	if err != nil {
		t.Fatalf("NewExtractor failed: %v", err)
	}
	if ex.engine != EngineStripped {
		t.Errorf("expected default engine %q, got %q", EngineStripped, ex.engine)
	}
}

// --- callLLM / attempt: json_mode-then-plain retry ---------------------

func TestCallLLM_SucceedsOnFirstJSONModeAttempt(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse(`{"price": "Rs. 100"}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	raw, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err != nil {
		t.Fatalf("callLLM returned error: %v", err)
	}
	if string(raw) != `{"price": "Rs. 100"}` {
		t.Errorf("unexpected raw content: %s", raw)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected exactly 1 call (no retry needed), got %d", len(fake.calls))
	}
	if fake.calls[0].ResponseFormat == nil || fake.calls[0].ResponseFormat.Type != "json_object" {
		t.Error("first attempt should have set response_format to json_object")
	}
}

func TestCallLLM_RetriesInPlainModeAfterHTTPFailure(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{err: errors.New("connection reset")},
			{resp: contentResponse(`{"price": "Rs. 100"}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	raw, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err != nil {
		t.Fatalf("callLLM returned error after successful retry: %v", err)
	}
	if string(raw) != `{"price": "Rs. 100"}` {
		t.Errorf("unexpected raw content: %s", raw)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected exactly 2 calls (retry fired), got %d", len(fake.calls))
	}
	if fake.calls[0].ResponseFormat == nil {
		t.Error("first attempt should have used json_mode")
	}
	if fake.calls[1].ResponseFormat != nil {
		t.Error("retry attempt should NOT set response_format (plain mode)")
	}
}

func TestCallLLM_RetriesAfterInvalidJSONInFirstAttempt(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse("not valid json at all")},
			{resp: contentResponse(`{"price": "Rs. 100"}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	raw, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err != nil {
		t.Fatalf("callLLM returned error after successful retry: %v", err)
	}
	if string(raw) != `{"price": "Rs. 100"}` {
		t.Errorf("unexpected raw content: %s", raw)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(fake.calls))
	}
}

func TestCallLLM_FailsAfterBothAttemptsFail(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{err: errors.New("first failure")},
			{err: errors.New("second failure")},
		},
	}
	ex := newTestExtractor(t, fake)

	_, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err == nil {
		t.Fatal("expected error when both attempts fail, got nil")
	}
	if !strings.Contains(err.Error(), "first failure") || !strings.Contains(err.Error(), "second failure") {
		t.Errorf("expected error to reference both failures, got: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected exactly 2 calls, got %d", len(fake.calls))
	}
}

func TestAttempt_StripsCodeFenceBeforeValidating(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse("```json\n{\"price\": \"Rs. 100\"}\n```")},
		},
	}
	ex := newTestExtractor(t, fake)

	raw, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err != nil {
		t.Fatalf("callLLM returned error: %v", err)
	}
	if string(raw) != `{"price": "Rs. 100"}` {
		t.Errorf("expected fence-stripped JSON, got: %s", raw)
	}
}

func TestAttempt_EmptyChoicesArrayIsAFailure(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: emptyChoicesResponse()},
			{resp: contentResponse(`{"ok": true}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	raw, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err != nil {
		t.Fatalf("expected retry to succeed, got error: %v", err)
	}
	if string(raw) != `{"ok": true}` {
		t.Errorf("unexpected content: %s", raw)
	}
}

func TestAttempt_MalformedResponseIsAFailure(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: malformedResponse()},
			{resp: contentResponse(`{"ok": true}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	raw, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err != nil {
		t.Fatalf("expected retry to succeed, got error: %v", err)
	}
	if string(raw) != `{"ok": true}` {
		t.Errorf("unexpected content: %s", raw)
	}
}

func TestAttempt_EmptyContentStringIsAFailure(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse("")},
			{resp: contentResponse(`{"ok": true}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	raw, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err != nil {
		t.Fatalf("expected retry to succeed, got error: %v", err)
	}
	if string(raw) != `{"ok": true}` {
		t.Errorf("unexpected content: %s", raw)
	}
}

func TestAttempt_APIErrorFieldSurfacedOnMalformedResponse(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: &llm.ChatCompletionResponse{
				Choices: nil,
				Error:   &llm.ChatCompletionAPIError{Message: "rate limited"},
			}},
			{err: errors.New("second attempt also fails")},
		},
	}
	ex := newTestExtractor(t, fake)

	_, err := ex.callLLM(context.Background(), "user content", "system prompt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("expected backend error message to be surfaced, got: %v", err)
	}
}

// --- ExtractDetails ----------------------------------------------------

func TestExtractDetails_ParsesResultShape(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse(`{"price": "Rs. 3,590.00", "stock_status": "In stock", "description": "A great book.", "isbn": "9781473231061"}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	result, err := ex.ExtractDetails(context.Background(), "<html></html>", "Some Title", nil)
	if err != nil {
		t.Fatalf("ExtractDetails returned error: %v", err)
	}
	if result.Price == nil || *result.Price != "Rs. 3,590.00" {
		t.Errorf("unexpected Price: %+v", result.Price)
	}
	if result.StockStatus == nil || *result.StockStatus != "In stock" {
		t.Errorf("unexpected StockStatus: %+v", result.StockStatus)
	}
	if result.ISBN == nil || *result.ISBN != "9781473231061" {
		t.Errorf("unexpected ISBN: %+v", result.ISBN)
	}
}

func TestExtractDetails_NullFieldsBecomeNilPointers(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse(`{"price": null, "stock_status": null, "description": null, "isbn": null}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	result, err := ex.ExtractDetails(context.Background(), "<html></html>", "Some Title", nil)
	if err != nil {
		t.Fatalf("ExtractDetails returned error: %v", err)
	}
	if result.Price != nil || result.StockStatus != nil || result.Description != nil || result.ISBN != nil {
		t.Errorf("expected all-nil fields for all-null JSON, got: %+v", result)
	}
}

func TestExtractDetails_DefaultFieldsUsedWhenNilPassed(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse(`{"price": "x"}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	_, err := ex.ExtractDetails(context.Background(), "<html></html>", "Some Title", nil)
	if err != nil {
		t.Fatalf("ExtractDetails returned error: %v", err)
	}
	sentPrompt := fake.calls[0].Messages[1].Content
	for _, want := range defaultDetailFields {
		if !strings.Contains(sentPrompt, want) {
			t.Errorf("expected default field %q to appear in the rendered prompt's field list, prompt was: %s", want, sentPrompt)
		}
	}
}

func TestExtractDetails_PropagatesDispatchFailure(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{err: errors.New("boom")},
			{err: errors.New("boom again")},
		},
	}
	ex := newTestExtractor(t, fake)

	_, err := ex.ExtractDetails(context.Background(), "<html></html>", "Some Title", nil)
	if err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
}

// --- ExtractSelectors ----------------------------------------------------

func TestExtractSelectors_UnwrapsEnvelopeIntoSelectorConfigMap(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse(`{
				"selectors": {
					"title": {"selector": "h1.title"},
					"price": {"selector": "div[class*='price']", "direct_text": true},
					"isbn": {"find_by_text": ["span", "ISBN 13"], "then_next": "td"},
					"description": {"selector": ".blurb", "preserve_semantics": true}
				}
			}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	selectors, err := ex.ExtractSelectors(context.Background(), "<html></html>", "Some Title")
	if err != nil {
		t.Fatalf("ExtractSelectors returned error: %v", err)
	}
	if len(selectors) != 4 {
		t.Fatalf("expected 4 selector entries, got %d: %+v", len(selectors), selectors)
	}
	if selectors["price"] == nil || !selectors["price"].DirectText {
		t.Errorf("expected price.direct_text=true, got: %+v", selectors["price"])
	}
	if selectors["isbn"] == nil || len(selectors["isbn"].FindByText) != 2 || selectors["isbn"].ThenNext == nil || *selectors["isbn"].ThenNext != "td" {
		t.Errorf("unexpected isbn selector config: %+v", selectors["isbn"])
	}
	if selectors["description"] == nil || !selectors["description"].PreserveSemantics {
		t.Errorf("expected description.preserve_semantics=true, got: %+v", selectors["description"])
	}
}

func TestExtractSelectors_NullFieldBecomesNilMapEntry(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse(`{"selectors": {"title": {"selector": "h1"}, "isbn": null}}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	selectors, err := ex.ExtractSelectors(context.Background(), "<html></html>", "Some Title")
	if err != nil {
		t.Fatalf("ExtractSelectors returned error: %v", err)
	}
	if selectors["isbn"] != nil {
		t.Errorf("expected nil entry for a JSON-null field, got: %+v", selectors["isbn"])
	}
	if selectors["title"] == nil {
		t.Errorf("expected non-nil entry for title")
	}
}

func TestExtractSelectors_EmptyEnvelopeReturnsEmptyMapNotNil(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{
			{resp: contentResponse(`{}`)},
		},
	}
	ex := newTestExtractor(t, fake)

	selectors, err := ex.ExtractSelectors(context.Background(), "<html></html>", "Some Title")
	if err != nil {
		t.Fatalf("ExtractSelectors returned error: %v", err)
	}
	if selectors == nil {
		t.Error("expected an empty map, got nil")
	}
	if len(selectors) != 0 {
		t.Errorf("expected empty map, got: %+v", selectors)
	}
}

// --- ClassifyStockStatus -------------------------------------------------

func TestClassifyStockStatus_TrueResponse(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{{resp: contentResponse("TRUE")}},
	}
	ex := newTestExtractor(t, fake)

	got, err := ex.ClassifyStockStatus(context.Background(), "Low stock: 3 left")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || *got != true {
		t.Errorf("expected true, got %+v", got)
	}
}

func TestClassifyStockStatus_FalseResponse(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{{resp: contentResponse("FALSE")}},
	}
	ex := newTestExtractor(t, fake)

	got, err := ex.ClassifyStockStatus(context.Background(), "Currently unavailable")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || *got != false {
		t.Errorf("expected false, got %+v", got)
	}
}

func TestClassifyStockStatus_UnknownResponseReturnsNilNotError(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{{resp: contentResponse("UNKNOWN")}},
	}
	ex := newTestExtractor(t, fake)

	got, err := ex.ClassifyStockStatus(context.Background(), "ambiguous text")
	if err != nil {
		t.Fatalf("expected no Go error for UNKNOWN (mirrors Python returning None, not raising), got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil (unknown), got %+v", *got)
	}
}

func TestClassifyStockStatus_UnrecognizedResponseReturnsNilNotError(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{{resp: contentResponse("maybe? not sure")}},
	}
	ex := newTestExtractor(t, fake)

	got, err := ex.ClassifyStockStatus(context.Background(), "ambiguous text")
	if err != nil {
		t.Fatalf("expected no Go error, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", *got)
	}
}

func TestClassifyStockStatus_HTTPFailureReturnsNilNotError(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{{err: errors.New("connection reset")}},
	}
	ex := newTestExtractor(t, fake)

	got, err := ex.ClassifyStockStatus(context.Background(), "some text")
	if err != nil {
		t.Fatalf("ClassifyStockStatus must never return a Go error (mirrors Python's bare except-return-None), got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after a failed dispatch, got %+v", *got)
	}
	// Critically: NO retry should fire here — classify_stock_status has no
	// json_mode-then-plain fallback in Python (it's not routed through
	// _call_llm at all, it calls chat.completions.create directly).
	if len(fake.calls) != 1 {
		t.Errorf("expected exactly 1 call (no retry semantics for this method), got %d", len(fake.calls))
	}
}

func TestClassifyStockStatus_DoesNotUseJSONMode(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{{resp: contentResponse("TRUE")}},
	}
	ex := newTestExtractor(t, fake)

	_, _ = ex.ClassifyStockStatus(context.Background(), "some text")
	if fake.calls[0].ResponseFormat != nil {
		t.Error("classify_stock_status never sets response_format in Python; expected nil ResponseFormat")
	}
	if fake.calls[0].MaxTokens == nil || *fake.calls[0].MaxTokens != 5 {
		t.Errorf("expected max_tokens=5 (fixed, per Python), got %+v", fake.calls[0].MaxTokens)
	}
}

func TestClassifyStockStatus_EmptyChoicesReturnsNil(t *testing.T) {
	fake := &fakeChatCompleter{
		responses: []fakeChatResult{{resp: emptyChoicesResponse()}},
	}
	ex := newTestExtractor(t, fake)

	got, err := ex.ClassifyStockStatus(context.Background(), "some text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", *got)
	}
}
