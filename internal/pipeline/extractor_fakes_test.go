package pipeline

import (
	"context"

	"github.com/udanfernando2006/vestige-go/internal/llm"
)

// fakeChatCompleter is an in-memory chatCompleter implementation for
// tests, mirroring the project's existing fakeSession convention
// (fakes_test.go implementing browser.Session in-memory) — full control
// over Extractor's dispatch logic (callLLM's json_mode-then-plain retry,
// ClassifyStockStatus's response parsing) with zero real network
// dependency.
//
// responses is consumed in call order (FIFO) via a simple index counter;
// each entry is either a *llm.ChatCompletionResponse to return, or an
// err to return instead (mutually exclusive per call, matching how a
// real HTTP call either succeeds or fails, never both). If responses is
// exhausted before all expected calls happen, CreateChatCompletion
// returns an error naming the overrun — a test bug (too many calls) is
// simpler to see this way than a nil-pointer panic deep in Extractor.
type fakeChatCompleter struct {
	responses []fakeChatResult
	calls     []llm.ChatCompletionRequest // records every request received, for assertions
	callIndex int
}

type fakeChatResult struct {
	resp *llm.ChatCompletionResponse
	err  error
}

func (f *fakeChatCompleter) CreateChatCompletion(_ context.Context, req llm.ChatCompletionRequest) (*llm.ChatCompletionResponse, error) {
	f.calls = append(f.calls, req)
	if f.callIndex >= len(f.responses) {
		panic("fakeChatCompleter: CreateChatCompletion called more times than responses were queued — add another fakeChatResult to the test's responses slice")
	}
	r := f.responses[f.callIndex]
	f.callIndex++
	return r.resp, r.err
}

// contentResponse is a small helper building the common-case
// *llm.ChatCompletionResponse shape (one choice, given content, no
// model/error fields set) so individual tests stay short.
func contentResponse(content string) *llm.ChatCompletionResponse {
	resp := &llm.ChatCompletionResponse{
		Choices: []llm.ChatCompletionChoice{{}},
	}
	resp.Choices[0].Message.Content = content
	return resp
}

// emptyChoicesResponse mirrors a response that decoded successfully but
// has a zero-length Choices slice — the "empty choices array" branch in
// _attempt, distinct from a nil Choices slice (malformed response).
func emptyChoicesResponse() *llm.ChatCompletionResponse {
	return &llm.ChatCompletionResponse{Choices: []llm.ChatCompletionChoice{}}
}

// malformedResponse mirrors a response with a nil Choices field
// entirely — the "no 'choices' field" branch in _attempt.
func malformedResponse() *llm.ChatCompletionResponse {
	return &llm.ChatCompletionResponse{Choices: nil}
}
