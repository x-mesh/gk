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
// and the final ChatResult carries the same text plus model/tokens/stop
// reason from the terminal chunk + "[DONE]". Exactly one HTTP call is
// made — no fallback.
func TestNvidiaChatStream_TextOnlyRoundTrip(t *testing.T) {
	raw := openAISSESuccess("hello streaming world", "test-model", 9)
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	n := newTestNvidia(client, "test-key")

	var deltas []string
	res, err := n.ChatWithTools(context.Background(), ChatInput{
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
	if res.Model != "test-model" {
		t.Errorf("Model = %q", res.Model)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn (normalized from stop)", res.StopReason)
	}
	if res.TokensUsed != 9 {
		t.Errorf("TokensUsed = %d, want 9", res.TokensUsed)
	}
}

// The streaming request body must set "stream":true and
// "stream_options":{"include_usage":true}; a nil-callback call must
// never set either.
func TestNvidiaChatStream_RequestSetsStreamFields(t *testing.T) {
	raw := openAISSESuccess("ok", "test-model", 3)
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	n := newTestNvidia(client, "test-key")
	_, err := n.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	var req struct {
		Stream        bool `json:"stream"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	b, _ := io.ReadAll(client.Calls[0].Req.Body)
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if !req.Stream || !req.StreamOptions.IncludeUsage {
		t.Errorf("request = %+v, want stream=true and stream_options.include_usage=true", req)
	}
}

// tool_calls detected in the very first chunk (no preceding content):
// zero deltas fire, and ChatWithTools re-sends as a plain non-stream
// request whose reply is the only thing reflected in the result.
func TestNvidiaChatStream_ToolCallsFirstChunkFallsBack(t *testing.T) {
	raw := openAIToolCallDeltaChunk("test-model") + openAIDoneEvent()
	fallback := okResponse(chatResponse{
		Model: "test-model",
		Choices: []chatChoice{{
			Message: chatMessage{
				Role: "assistant",
				ToolCalls: []chatToolCall{{
					ID: "call_01", Type: "function",
					Function: chatToolCallFunction{Name: "git_log", Arguments: `{"limit":5}`},
				}},
			},
			FinishReason: "tool_calls",
		}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil), fallback}}
	n := newTestNvidia(client, "test-key")

	var deltas []string
	res, err := n.ChatWithTools(context.Background(), ChatInput{
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
		t.Errorf("deltas = %v, want none — tool_calls was the first chunk", deltas)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "git_log" {
		t.Fatalf("ToolCalls = %+v, want the fallback's git_log call", res.ToolCalls)
	}
}

// Adversarial: tool_calls interleaved AFTER some content has already
// streamed. The final result must be entirely the non-stream fallback's
// reply — never a splice with the streamed preamble.
func TestNvidiaChatStream_InterleavedContentThenToolCallsFallsBack(t *testing.T) {
	raw := openAIContentDeltaChunk("test-model", "let me check") + openAIToolCallDeltaChunk("test-model")
	fallback := okResponse(chatResponse{
		Model: "test-model",
		Choices: []chatChoice{{
			Message: chatMessage{
				Role:    "assistant",
				Content: "let me check that for you",
				ToolCalls: []chatToolCall{{
					ID: "call_01", Type: "function",
					Function: chatToolCallFunction{Name: "git_log", Arguments: `{"limit":5}`},
				}},
			},
			FinishReason: "tool_calls",
		}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil), fallback}}
	n := newTestNvidia(client, "test-key")

	var deltas []string
	res, err := n.ChatWithTools(context.Background(), ChatInput{
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
	if strings.Join(deltas, "") != "let me check" {
		t.Errorf("deltas = %v, want exactly the pre-tool_calls preamble", deltas)
	}
	if res.Text != "let me check that for you" {
		t.Errorf("Text = %q, want the fallback reply's text verbatim (no splice)", res.Text)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want 1 from the fallback", res.ToolCalls)
	}
}

// Adversarial: connection severed mid-chunk (no terminal "[DONE]" ever
// arrives). The partial content already delivered via OnTextDelta must
// not appear in the final result — the whole request is retried as
// non-stream instead.
func TestNvidiaChatStream_ConnectionCutMidChunkFallsBackWholesale(t *testing.T) {
	raw := openAIContentDeltaChunk("test-model", "partial answer that never fin")
	severed := errors.New("read: connection reset by peer")
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: &sseChunkReader{
		chunks: [][]byte{[]byte(raw)},
		endErr: severed,
	}}
	fallback := okResponse(chatResponse{
		Model: "test-model",
		Choices: []chatChoice{{
			Message:      chatMessage{Role: "assistant", Content: "the complete, correct answer"},
			FinishReason: "stop",
		}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{resp, fallback}}
	n := newTestNvidia(client, "test-key")

	var deltas []string
	res, err := n.ChatWithTools(context.Background(), ChatInput{
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

// Adversarial: keep-alive lines with no "data:" interspersed between
// real chunks must not corrupt parsing or panic.
func TestNvidiaChatStream_KeepAliveLinesDoNotBreakParsing(t *testing.T) {
	raw := ": keep-alive\n\n" +
		openAIContentDeltaChunk("test-model", "hello") +
		": ping\n\n" +
		openAIFinishChunk("test-model", "stop", 4) +
		": ping\n\n" +
		openAIDoneEvent()
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	n := newTestNvidia(client, "test-key")

	res, err := n.ChatWithTools(context.Background(), ChatInput{
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

// Adversarial: fragmented UTF-8 across arbitrary byte boundaries fed
// through the full ChatWithTools path.
func TestNvidiaChatStream_FragmentedUTF8ThroughFullPath(t *testing.T) {
	text := "안녕하세요 결과 🎉"
	raw := openAISSESuccess(text, "test-model", 6)
	client := &FakeHTTPClient{Responses: []*http.Response{sseOneByteResponse(raw, nil)}}
	n := newTestNvidia(client, "test-key")

	var sb strings.Builder
	res, err := n.ChatWithTools(context.Background(), ChatInput{
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

// A non-2xx status on the streaming attempt falls back to non-stream
// without ever touching the SSE parser.
func TestNvidiaChatStream_NonOKStatusFallsBack(t *testing.T) {
	client := &FakeHTTPClient{Responses: []*http.Response{
		errResponse(500, "internal error"),
		okResponse(chatResponse{
			Model: "test-model",
			Choices: []chatChoice{{
				Message:      chatMessage{Role: "assistant", Content: "fallback answer"},
				FinishReason: "stop",
			}},
		}),
	}}
	n := newTestNvidia(client, "test-key")
	res, err := n.ChatWithTools(context.Background(), ChatInput{
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

// F1 regression: a malformed (non-empty, non-JSON) data: chunk arriving
// mid-stream must NOT be silently dropped while scanning continues to a
// clean "[DONE]" — that would confirm an answer silently missing
// whatever content the corrupted chunk carried. This is exactly what
// this method's own docstring already promises ("a malformed/
// unparseable chunk ... returns (ChatResult{}, false)") but the pre-fix
// implementation violated: it treated an unparseable chunk as "ignore,
// keep scanning" instead of a fatal parse failure.
func TestNvidiaChatStream_MalformedChunkFallsBackInsteadOfPartialSuccess(t *testing.T) {
	raw := openAIContentDeltaChunk("test-model", "partial answer prefix") +
		// A non-empty data: payload that is NOT valid JSON — corruption
		// or a mid-write truncation, never a legitimate Chat Completions
		// chunk (every real one is JSON, or the literal "[DONE]").
		"data: {this is not json\n\n" +
		openAIDoneEvent()
	fallback := okResponse(chatResponse{
		Model: "test-model",
		Choices: []chatChoice{{
			Message:      chatMessage{Role: "assistant", Content: "the complete, correct answer"},
			FinishReason: "stop",
		}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil), fallback}}
	n := newTestNvidia(client, "test-key")

	var deltas []string
	res, err := n.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(s string) { deltas = append(deltas, s) },
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (malformed chunk must trigger the non-stream fallback)", len(client.Calls))
	}
	if strings.Join(deltas, "") != "partial answer prefix" {
		t.Errorf("deltas = %v, want exactly the pre-corruption preamble", deltas)
	}
	if res.Text != "the complete, correct answer" {
		t.Errorf("Text = %q, want the fallback's full reply — never the partial pre-corruption text", res.Text)
	}
}

// Boundary check for F1: a chunk with no "data:" line at all — only an
// unknown field (id:/retry:) — is SSE-legal and must NOT be treated as
// malformed: it has empty Data, which the existing early-return already
// (and correctly) treats as "nothing to parse," categorically different
// from a non-empty payload that fails to parse.
func TestNvidiaChatStream_IDOnlyChunkIsNotMalformed(t *testing.T) {
	raw := "id: 42\n\n" +
		openAIContentDeltaChunk("test-model", "hello") +
		openAIFinishChunk("test-model", "stop", 4) +
		openAIDoneEvent()
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	n := newTestNvidia(client, "test-key")

	res, err := n.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("HTTP calls = %d, want 1 (an id:-only chunk must not trigger a fallback)", len(client.Calls))
	}
	if res.Text != "hello" {
		t.Errorf("Text = %q, want %q", res.Text, "hello")
	}
}

// A hung stream now rides the FULL round context on the OpenAI-compatible
// path too (shared by nvidia, and openai/groq via delegation). See the
// Anthropic counterpart (TestAnthropicChatStream_HungStreamRidesFullRound-
// ThenFails) for the full rationale: the fraction cap that reserved
// fallback time was removed because it truncated slow-but-valid answers,
// and a server that hangs the stream would hang the fallback anyway.
func TestNvidiaChatStream_HungStreamRidesFullRoundThenFails(t *testing.T) {
	fallback := okResponse(chatResponse{
		Model: "test-model",
		Choices: []chatChoice{{
			Message:      chatMessage{Role: "assistant", Content: "fallback answer"},
			FinishReason: "stop",
		}},
	})
	client := &hangingThenFallbackClient{fallback: fallback}
	n := newTestNvidia(client, "test-key")

	roundCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := n.ChatWithTools(roundCtx, ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err == nil {
		t.Fatal("a hung stream on the full round budget must surface the deadline error, not a phantom success from a fallback on an expired context")
	}
}

// nil OnTextDelta must take the exact pre-existing non-stream path: one
// HTTP call, no "stream" field even sent.
func TestNvidiaChatStream_NilCallbackNeverStreams(t *testing.T) {
	client := &FakeHTTPClient{Responses: []*http.Response{
		okResponse(chatResponse{
			Model: "test-model",
			Choices: []chatChoice{{
				Message:      chatMessage{Role: "assistant", Content: "plain answer"},
				FinishReason: "stop",
			}},
		}),
	}}
	n := newTestNvidia(client, "test-key")
	res, err := n.ChatWithTools(context.Background(), ChatInput{
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

// openai/groq delegate ChatWithTools through their embedded Nvidia
// adapter (see TestOpenAIGroqChatDelegation in nvidia_chat_test.go for
// the non-stream regression); this is the streaming-path counterpart —
// the task's "회귀 표면 3배" (3x the regression surface) is exactly why
// this delegation must be verified for streaming too, not just assumed
// from the non-stream delegation test.
func TestOpenAIGroqChatStreamDelegation(t *testing.T) {
	raw := func(model string) string { return openAISSESuccess("delegated answer", model, 5) }

	o := NewOpenAI()
	o.APIKey = "k"
	o.Client = &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw("gpt-test")}, nil)}}
	o.nv = o.toNvidia()
	var oDeltas []string
	oRes, err := o.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(s string) { oDeltas = append(oDeltas, s) },
	})
	if err != nil {
		t.Fatalf("OpenAI delegation: %v", err)
	}
	if oRes.Text != "delegated answer" || strings.Join(oDeltas, "") != "delegated answer" {
		t.Errorf("OpenAI streamed result = %+v, deltas = %v", oRes, oDeltas)
	}

	g := NewGroq()
	g.APIKey = "k"
	g.Client = &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw("groq-test")}, nil)}}
	g.nv = g.toNvidia()
	var gDeltas []string
	gRes, err := g.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(s string) { gDeltas = append(gDeltas, s) },
	})
	if err != nil {
		t.Fatalf("Groq delegation: %v", err)
	}
	if gRes.Text != "delegated answer" || strings.Join(gDeltas, "") != "delegated answer" {
		t.Errorf("Groq streamed result = %+v, deltas = %v", gRes, gDeltas)
	}
}

// TestNvidiaChatStream_InBandErrorChunkFallsBack pins the v2 panel finding:
// a well-formed `data: {"error":{...}}` chunk (which parses cleanly, unlike
// a corrupted one) followed by `data: [DONE]` must NOT confirm the partial
// text as a complete answer — it must abandon the stream and retry
// non-stream, matching the Anthropic adapter's `event: error` handling.
func TestNvidiaChatStream_InBandErrorChunkFallsBack(t *testing.T) {
	raw := openAIContentDeltaChunk("test-model", "here is a partial ") +
		openAIDataBlock(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`) +
		openAIDoneEvent()
	fallback := okResponse(chatResponse{
		Model: "test-model",
		Choices: []chatChoice{{
			Message:      chatMessage{Role: "assistant", Content: "the complete answer"},
			FinishReason: "stop",
		}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil), fallback}}
	n := newTestNvidia(client, "test-key")

	res, err := n.ChatWithTools(context.Background(), ChatInput{
		Messages:    []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta: func(string) {},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(client.Calls) != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (error chunk abandons stream + non-stream retry)", len(client.Calls))
	}
	if res.Text != "the complete answer" {
		t.Errorf("Text = %q, want the fallback reply — an in-band error must never confirm the partial stream", res.Text)
	}
}

// TestNvidiaChatStream_OnStreamResetAfterPartialThenFallback pins the v2
// finding (agy's lone catch in round 1, confirmed by claude+codex in v2):
// when a stream delivers partial text and is then abandoned for the
// non-stream fallback, the adapter must fire OnStreamReset exactly once so
// the caller can void the already-printed partial instead of letting the
// fallback answer concatenate onto it.
func TestNvidiaChatStream_OnStreamResetAfterPartialThenFallback(t *testing.T) {
	// A stream that prints text, then severs before [DONE] → fallback.
	raw := openAIContentDeltaChunk("test-model", "partial ans")
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: &sseChunkReader{
		chunks: [][]byte{[]byte(raw)},
		endErr: errors.New("read: connection reset by peer"),
	}}
	fallback := okResponse(chatResponse{
		Model:   "test-model",
		Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "full answer"}, FinishReason: "stop"}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{resp, fallback}}
	n := newTestNvidia(client, "test-key")

	resets := 0
	res, err := n.ChatWithTools(context.Background(), ChatInput{
		Messages:      []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta:   func(string) {},
		OnStreamReset: func() { resets++ },
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if resets != 1 {
		t.Errorf("OnStreamReset fired %d times, want exactly 1 (partial streamed, then fallback)", resets)
	}
	if res.Text != "full answer" {
		t.Errorf("Text = %q, want the fallback reply", res.Text)
	}
}

// A clean stream (no fallback) must NOT fire OnStreamReset.
func TestNvidiaChatStream_NoResetOnCleanStream(t *testing.T) {
	raw := openAISSESuccess("all good", "test-model", 4)
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil)}}
	n := newTestNvidia(client, "test-key")

	resets := 0
	if _, err := n.ChatWithTools(context.Background(), ChatInput{
		Messages:      []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta:   func(string) {},
		OnStreamReset: func() { resets++ },
	}); err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if resets != 0 {
		t.Errorf("OnStreamReset fired %d times on a clean stream, want 0", resets)
	}
}

// A tool_use fallback with NO prior text delta must NOT fire OnStreamReset
// (nothing was printed, so there is no partial line to void).
func TestNvidiaChatStream_NoResetWhenToolCallBeforeAnyText(t *testing.T) {
	raw := openAIToolCallDeltaChunk("test-model")
	fallback := okResponse(chatResponse{
		Model:   "test-model",
		Choices: []chatChoice{{Message: chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{ID: "c1", Type: "function", Function: chatToolCallFunction{Name: "git_log"}}}}, FinishReason: "tool_calls"}},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{sseChunkResponse([]string{raw}, nil), fallback}}
	n := newTestNvidia(client, "test-key")

	resets := 0
	if _, err := n.ChatWithTools(context.Background(), ChatInput{
		Messages:      []ChatMessage{{Role: "user", Text: "hi"}},
		OnTextDelta:   func(string) {},
		OnStreamReset: func() { resets++ },
	}); err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if resets != 0 {
		t.Errorf("OnStreamReset fired %d times when no text preceded the tool_call, want 0", resets)
	}
}
