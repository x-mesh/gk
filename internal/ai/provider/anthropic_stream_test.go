package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A clean streaming round-trip: text arrives via OnTextDelta in order,
// and the final ChatResult carries the SAME text plus model/tokens/stop
// reason parsed from the stream's message_start/message_delta/
// message_stop events. Exactly one HTTP call is made — no fallback.
func TestAnthropicChatStream_TextOnlyRoundTrip(t *testing.T) {
	raw := anthropicSSESuccess("hello streaming world", "test-model", 7)
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	a := newTestAnthropic(client)

	var deltas []string
	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(s string) { deltas = append(deltas, s) },
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("HTTP calls = %d, want 1 (no fallback on a clean stream)", len(client.Calls))
	}
	if res.Text != "hello streaming world" {
		t.Errorf("Text = %q", res.Text)
	}
	if got := strings.Join(deltas, ""); got != "hello streaming world" {
		t.Errorf("streamed deltas joined = %q, want %q", got, res.Text)
	}
	if len(deltas) < 2 {
		t.Errorf("want at least 2 delta callbacks (incremental delivery), got %d: %v", len(deltas), deltas)
	}
	if res.Model != "test-model" {
		t.Errorf("Model = %q", res.Model)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", res.StopReason)
	}
	if res.TokensUsed == 0 {
		t.Errorf("TokensUsed = 0, want > 0 (input from message_start + output from message_delta)")
	}
}

// Verify the streaming request body actually sets "stream":true, and
// that a NON-streaming call (OnTextDelta nil) never sets it — the
// opt-in must be visible on the wire, not just behaviorally.
func TestAnthropicChatStream_RequestSetsStreamFlag(t *testing.T) {
	raw := anthropicSSESuccess("ok", "test-model", 3)
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	a := newTestAnthropic(client)
	_, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	b, _ := io.ReadAll(client.Calls[0].Req.Body)
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if !req.Stream {
		t.Error(`request body must set "stream":true when OnTextDelta is set`)
	}
}

// tool_use detected at the very first content block (no preamble text):
// zero deltas fire, the stream is abandoned, and ChatWithTools re-sends
// as a plain non-stream request whose reply is the ONLY thing reflected
// in the result.
func TestAnthropicChatStream_ToolUseFirstBlockFallsBack(t *testing.T) {
	raw := anthropicMessageStartEvent("test-model") +
		anthropicToolUseBlockStartEvent(0, "toolu_01", "git_log")
	fallback := anthropicOKResponse(anthropicResponse{
		Model:      "test-model",
		StopReason: "tool_use",
		Content: []anthropicContentBlock{
			{Type: "tool_use", ID: "toolu_01", Name: "git_log", Input: json.RawMessage(`{"limit":5}`)},
		},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{
		sseChunkResponse([]string{raw}, nil),
		fallback,
	}}
	a := newTestAnthropic(client)

	var deltas []string
	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		Tools:       []ToolSpec{{Name: "git_log", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		OnTextDelta: func(s string) { deltas = append(deltas, s) },
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (stream attempt + non-stream fallback)", len(client.Calls))
	}
	if len(deltas) != 0 {
		t.Errorf("deltas = %v, want none — tool_use was the first block", deltas)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "git_log" {
		t.Fatalf("ToolCalls = %+v, want the fallback's git_log call", res.ToolCalls)
	}
}

// Adversarial: tool_use interleaved AFTER some text has already
// streamed. The already-streamed text must never leak into the final
// ChatResult — the result is entirely the non-stream fallback's reply,
// never a splice.
func TestAnthropicChatStream_InterleavedTextThenToolUseFallsBack(t *testing.T) {
	raw := anthropicMessageStartEvent("test-model") +
		anthropicTextBlockStartEvent(0) +
		anthropicTextDeltaEvent(0, "let me check") +
		anthropicBlockStopEvent(0) +
		anthropicToolUseBlockStartEvent(1, "toolu_01", "git_log")
	fallback := anthropicOKResponse(anthropicResponse{
		Model:      "test-model",
		StopReason: "tool_use",
		Content: []anthropicContentBlock{
			{Type: "text", Text: "let me check that for you"},
			{Type: "tool_use", ID: "toolu_01", Name: "git_log", Input: json.RawMessage(`{"limit":5}`)},
		},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{
		sseChunkResponse([]string{raw}, nil),
		fallback,
	}}
	a := newTestAnthropic(client)

	var deltas []string
	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		Tools:       []ToolSpec{{Name: "git_log", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		OnTextDelta: func(s string) { deltas = append(deltas, s) },
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (stream attempt + non-stream fallback)", len(client.Calls))
	}
	// Some text WAS streamed (the pre-tool_use preamble) — the provider
	// cannot un-print what the UI already displayed, but the RETURNED
	// result must be exactly the fallback's, not a merge.
	if strings.Join(deltas, "") != "let me check" {
		t.Errorf("deltas = %v, want exactly the pre-tool_use preamble", deltas)
	}
	if res.Text != "let me check that for you" {
		t.Errorf("Text = %q, want the fallback reply's text verbatim (no splice with streamed text)", res.Text)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "git_log" {
		t.Fatalf("ToolCalls = %+v, want the fallback's git_log call", res.ToolCalls)
	}
}

// Adversarial: the connection is severed mid-event (no message_stop
// ever arrives). The partial text already delivered via OnTextDelta
// must NOT appear, concatenated or otherwise, in the final result — the
// whole request is retried as non-stream instead.
func TestAnthropicChatStream_ConnectionCutMidEventFallsBackWholesale(t *testing.T) {
	raw := anthropicMessageStartEvent("test-model") +
		anthropicTextBlockStartEvent(0) +
		anthropicTextDeltaEvent(0, "partial answer that never fin")
	// No message_stop, and the reader errors instead of a clean EOF —
	// simulating a severed TCP connection mid-event.
	severed := errors.New("read: connection reset by peer")
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: &sseChunkReader{
		chunks: [][]byte{[]byte(raw)},
		endErr: severed,
	}}
	fallback := anthropicOKResponse(anthropicResponse{
		Model:      "test-model",
		StopReason: "end_turn",
		Content:    []anthropicContentBlock{{Type: "text", Text: "the complete, correct answer"}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{resp, fallback}}
	a := newTestAnthropic(client)

	var deltas []string
	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(s string) { deltas = append(deltas, s) },
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (severed stream + full non-stream retry)", len(client.Calls))
	}
	if res.Text != "the complete, correct answer" {
		t.Errorf("Text = %q, want the fallback's full reply — never the partial stream text", res.Text)
	}
	if strings.Join(deltas, "") == res.Text {
		t.Errorf("deltas accidentally match the fallback text — test isn't exercising the cutoff path")
	}
}

// Adversarial: keep-alive lines with no "data:" (bare SSE comments)
// interspersed between real events must not corrupt parsing or panic.
func TestAnthropicChatStream_KeepAliveLinesDoNotBreakParsing(t *testing.T) {
	raw := anthropicMessageStartEvent("test-model") +
		": keep-alive\n\n" +
		anthropicTextBlockStartEvent(0) +
		": ping\n\n" +
		anthropicTextDeltaEvent(0, "hello") +
		": ping\n\n" +
		anthropicBlockStopEvent(0) +
		anthropicMessageDeltaEvent("end_turn", 2) +
		anthropicMessageStopEvent()
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	a := newTestAnthropic(client)

	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("HTTP calls = %d, want 1 (keep-alives must not trigger a fallback)", len(client.Calls))
	}
	if res.Text != "hello" {
		t.Errorf("Text = %q, want %q", res.Text, "hello")
	}
}

// Adversarial: fragmented UTF-8 across arbitrary byte boundaries, fed
// through the real ChatWithTools path (not just scanSSE directly) —
// covers the JSON-decode-of-a-partially-buffered-line layer too.
func TestAnthropicChatStream_FragmentedUTF8ThroughFullPath(t *testing.T) {
	text := "안녕하세요 결과 🎉"
	raw := anthropicSSESuccess(text, "test-model", 5)
	client := &FakeHTTPClient{Responses: []*http.Response{sseOneByteResponse(raw, nil)}}
	a := newTestAnthropic(client)

	var sb strings.Builder
	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(s string) { sb.WriteString(s) },
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if res.Text != text {
		t.Errorf("Text = %q, want %q", res.Text, text)
	}
	if sb.String() != text {
		t.Errorf("streamed text = %q, want %q", sb.String(), text)
	}
}

// A non-200 status on the streaming attempt falls back to non-stream
// without ever touching the SSE parser.
func TestAnthropicChatStream_NonOKStatusFallsBack(t *testing.T) {
	client := &FakeHTTPClient{Responses: []*http.Response{
		sseErrResponse(529, "overloaded"),
		anthropicOKResponse(anthropicResponse{
			Model:   "test-model",
			Content: []anthropicContentBlock{{Type: "text", Text: "fallback answer"}},
		}),
	}}
	a := newTestAnthropic(client)
	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if res.Text != "fallback answer" {
		t.Errorf("Text = %q", res.Text)
	}
}

// F1 regression: a malformed (non-empty, non-JSON) data: payload
// arriving mid-stream must NOT be silently dropped while scanning
// continues to a clean message_stop — that would confirm an answer that
// is silently missing whatever content the corrupted event carried.
// This is exactly what this method's own docstring already promises
// ("a malformed/unparseable chunk ... returns (ChatResult{}, false)")
// but the pre-fix implementation violated: it treated an unparseable
// event as "ignore, keep scanning" instead of a fatal parse failure.
func TestAnthropicChatStream_MalformedEventFallsBackInsteadOfPartialSuccess(t *testing.T) {
	raw := anthropicMessageStartEvent("test-model") +
		anthropicTextBlockStartEvent(0) +
		anthropicTextDeltaEvent(0, "partial answer prefix") +
		// A non-empty data: payload that is NOT valid JSON — corruption
		// or a mid-write truncation, never a legitimate Messages API
		// event (every real one is JSON).
		"event: content_block_delta\ndata: {this is not json\n\n" +
		anthropicMessageStopEvent()
	fallback := anthropicOKResponse(anthropicResponse{
		Model:      "test-model",
		StopReason: "end_turn",
		Content:    []anthropicContentBlock{{Type: "text", Text: "the complete, correct answer"}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil), fallback}}
	a := newTestAnthropic(client)

	var deltas []string
	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(s string) { deltas = append(deltas, s) },
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (malformed event must trigger the non-stream fallback)", len(client.Calls))
	}
	if strings.Join(deltas, "") != "partial answer prefix" {
		t.Errorf("deltas = %v, want exactly the pre-corruption preamble", deltas)
	}
	if res.Text != "the complete, correct answer" {
		t.Errorf("Text = %q, want the fallback's full reply — never the partial pre-corruption text", res.Text)
	}
}

// Boundary check for F1: an event carrying only an unknown field
// (id:/retry:) and NO data: line at all is SSE-legal and must NOT be
// treated as malformed — it has an empty Data, which is categorically
// different from a non-empty payload that fails to parse. Confirms the
// malformed-detection fix didn't overreach into this legitimate case.
func TestAnthropicChatStream_IDOnlyEventIsNotMalformed(t *testing.T) {
	raw := anthropicMessageStartEvent("test-model") +
		anthropicTextBlockStartEvent(0) +
		"id: 42\n\n" +
		anthropicTextDeltaEvent(0, "hello") +
		anthropicBlockStopEvent(0) +
		anthropicMessageDeltaEvent("end_turn", 2) +
		anthropicMessageStopEvent()
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	a := newTestAnthropic(client)

	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("HTTP calls = %d, want 1 (an id:-only event must not trigger a fallback)", len(client.Calls))
	}
	if res.Text != "hello" {
		t.Errorf("Text = %q, want %q", res.Text, "hello")
	}
}

// A truly hung stream now rides the FULL round context — the old
// fraction cap that reserved fallback time was removed because it also
// killed slow-but-valid answers (3rd-panel regression). The accepted
// trade-off: a stream that connects and then sends nothing consumes the
// round deadline, and the non-stream fallback, hitting the SAME dead
// endpoint on the now-expired context, fails too — so ChatWithTools
// surfaces the deadline error rather than a phantom success. (A server
// that hangs the stream would hang the fallback anyway; reserving time
// for it bought nothing but the truncation of good answers.)
func TestAnthropicChatStream_HungStreamRidesFullRoundThenFails(t *testing.T) {
	fallback := anthropicOKResponse(anthropicResponse{
		Model:      "test-model",
		StopReason: "end_turn",
		Content:    []anthropicContentBlock{{Type: "text", Text: "fallback answer"}},
	})
	client := &hangingThenFallbackClient{fallback: fallback}
	a := newTestAnthropic(client)

	roundCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := a.ChatWithTools(roundCtx, ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err == nil {
		t.Fatal("a hung stream on the full round budget must surface the deadline error, not a phantom success from a fallback on an expired context")
	}
}

// nil OnTextDelta must take the EXACT pre-existing non-stream path: one
// HTTP call, no "stream" field even sent.
func TestAnthropicChatStream_NilCallbackNeverStreams(t *testing.T) {
	client := &FakeHTTPClient{Responses: []*http.Response{
		anthropicOKResponse(anthropicResponse{
			Model:   "test-model",
			Content: []anthropicContentBlock{{Type: "text", Text: "plain answer"}},
		}),
	}}
	a := newTestAnthropic(client)
	res, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "user", Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("HTTP calls = %d, want 1", len(client.Calls))
	}
	if res.Text != "plain answer" {
		t.Errorf("Text = %q", res.Text)
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	b, _ := io.ReadAll(client.Calls[0].Req.Body)
	_ = json.Unmarshal(b, &req)
	if req.Stream {
		t.Error(`non-streaming call must never set "stream":true`)
	}
}
