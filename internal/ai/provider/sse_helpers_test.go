package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// sseChunkReader replays a queued sequence of raw byte chunks to simulate
// real network chunking — including chunks that split a single line, a
// multi-byte UTF-8 rune, or even the literal "data:" prefix across two
// Read() calls. After the last chunk it returns endErr: io.EOF (the
// zero value substitute, see below) for a clean stream end, anything
// else to simulate a connection severed mid-stream.
type sseChunkReader struct {
	chunks [][]byte
	idx    int
	endErr error
}

func (r *sseChunkReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		if r.endErr != nil {
			return 0, r.endErr
		}
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.idx])
	if n < len(r.chunks[r.idx]) {
		r.chunks[r.idx] = r.chunks[r.idx][n:]
	} else {
		r.idx++
	}
	return n, nil
}

func (r *sseChunkReader) Close() error { return nil }

// sseChunkResponse builds a 200 *http.Response whose body replays chunks
// in order (see sseChunkReader). endErr nil means a clean EOF after the
// last chunk; non-nil simulates a severed connection.
func sseChunkResponse(chunks []string, endErr error) *http.Response {
	raw := make([][]byte, len(chunks))
	for i, c := range chunks {
		raw[i] = []byte(c)
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       &sseChunkReader{chunks: raw, endErr: endErr},
	}
}

// sseOneByteResponse is sseChunkResponse with the whole body split into
// 1-byte reads — the adversarial shape that guarantees any multi-byte
// UTF-8 rune in body is split across two Read() calls, and that "event:"
// / "data:" prefixes are themselves split character by character.
func sseOneByteResponse(body string, endErr error) *http.Response {
	chunks := make([]string, len(body))
	for i := 0; i < len(body); i++ {
		chunks[i] = body[i : i+1]
	}
	return sseChunkResponse(chunks, endErr)
}

// sseErrResponse builds a non-200 *http.Response with a plain body — for
// asserting the streaming attempt falls back on a bad status without
// ever reaching the SSE parser.
func sseErrResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{},
		Body:       io.NopCloser(&sseChunkReader{chunks: [][]byte{[]byte(body)}}),
	}
}

// hangingThenFallbackClient simulates a streaming attempt whose HTTP
// round-trip never completes on its own — the call just hangs, the way
// a stalled/incomplete SSE connection would — followed by an ordinary,
// instant fallback response on the SECOND call. Real net/http ties a
// request's entire lifetime to the context passed to Do (cancelling ctx
// unblocks whatever Read/Do is in flight), so blocking on <-ctx.Done()
// here is a faithful stand-in for "the stream is stuck" without any
// actual network involved — the same pattern nvidia_retrybudget_test.go
// uses for "one attempt was slow" scenarios that FakeHTTPClient (which
// answers instantly and never blocks) can't express.
//
// The SECOND call deliberately checks ctx.Err() up front and fails fast
// if it is already expired — mirroring http.Client.Do's own refusal to
// even start a request on a dead context. That is what makes this fake
// able to catch a real mutation: give it the SAME context the streaming
// attempt just rode out to its deadline (the pre-fix behavior) and the
// fallback call fails immediately too, exactly like a real expired
// context would; give it a context that still has time left on it (the
// fix: a shorter, dedicated sub-deadline for the streaming attempt
// alone) and the fallback succeeds.
type hangingThenFallbackClient struct {
	mu       sync.Mutex
	calls    int
	fallback *http.Response
}

func (c *hangingThenFallbackClient) Do(ctx context.Context, _ *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if n == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.fallback, nil
}

func (c *hangingThenFallbackClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// jsonStr marshals s as a JSON string literal (quoted + escaped) for
// hand-assembling SSE data payloads without fighting Go string escaping.
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ── Anthropic SSE event builders ──────────────────────────────────────

func sseBlock(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

func anthropicMessageStartEvent(model string) string {
	return sseBlock("message_start", `{"type":"message_start","message":{"model":`+jsonStr(model)+`,"usage":{"input_tokens":10,"output_tokens":1}}}`)
}

func anthropicTextBlockStartEvent(index int) string {
	return sseBlock("content_block_start", jsonMustf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, index))
}

func anthropicToolUseBlockStartEvent(index int, id, name string) string {
	return sseBlock("content_block_start", jsonMustf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%s,"name":%s,"input":{}}}`, index, jsonStr(id), jsonStr(name)))
}

func anthropicTextDeltaEvent(index int, text string) string {
	return sseBlock("content_block_delta", jsonMustf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, index, jsonStr(text)))
}

func anthropicBlockStopEvent(index int) string {
	return sseBlock("content_block_stop", jsonMustf(`{"type":"content_block_stop","index":%d}`, index))
}

func anthropicMessageDeltaEvent(stopReason string, outputTokens int) string {
	return sseBlock("message_delta", jsonMustf(`{"type":"message_delta","delta":{"stop_reason":%s},"usage":{"output_tokens":%d}}`, jsonStr(stopReason), outputTokens))
}

func anthropicMessageStopEvent() string {
	return sseBlock("message_stop", `{"type":"message_stop"}`)
}

func anthropicErrorEvent(msg string) string {
	return sseBlock("error", jsonMustf(`{"type":"error","error":{"type":"overloaded_error","message":%s}}`, jsonStr(msg)))
}

// anthropicSSESuccess assembles a full, well-formed streaming response
// for a text-only reply, splitting text across two deltas so tests
// exercise incremental delivery, not just a single chunk.
func anthropicSSESuccess(text, model string, outputTokens int) string {
	part1, part2 := splitOnRuneBoundary(text)
	var out string
	out += anthropicMessageStartEvent(model)
	out += anthropicTextBlockStartEvent(0)
	if part1 != "" {
		out += anthropicTextDeltaEvent(0, part1)
	}
	if part2 != "" {
		out += anthropicTextDeltaEvent(0, part2)
	}
	out += anthropicBlockStopEvent(0)
	out += anthropicMessageDeltaEvent("end_turn", outputTokens)
	out += anthropicMessageStopEvent()
	return out
}

// splitOnRuneBoundary halves text at a RUNE (not byte) boundary — a
// provider's real text_delta payloads are always well-formed UTF-8
// strings (never a byte sequence split mid-character; that only
// happens at the wire/transport layer, which is what
// sseOneByteResponse exercises separately). Splitting at len(text)/2
// bytes directly can land inside a multi-byte rune and hand
// json.Marshal invalid UTF-8, which it silently replaces with U+FFFD —
// corrupting the test's own fixture rather than testing anything about
// scanSSE.
func splitOnRuneBoundary(text string) (string, string) {
	runes := []rune(text)
	mid := len(runes) / 2
	return string(runes[:mid]), string(runes[mid:])
}

// jsonMustf is fmt.Sprintf under a name that reads as "this format
// string's %s/%d slots are already JSON-quoted by the caller" at each
// use site above.
func jsonMustf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// ── OpenAI-compatible (Chat Completions) SSE chunk builders ───────────

func openAIDataBlock(data string) string {
	return "data: " + data + "\n\n"
}

func openAIContentDeltaChunk(model, content string) string {
	return openAIDataBlock(jsonMustf(`{"model":%s,"choices":[{"index":0,"delta":{"content":%s},"finish_reason":null}]}`, jsonStr(model), jsonStr(content)))
}

func openAIToolCallDeltaChunk(model string) string {
	return openAIDataBlock(jsonMustf(`{"model":%s,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_01","type":"function","function":{"name":"git_log","arguments":""}}]},"finish_reason":null}]}`, jsonStr(model)))
}

func openAIFinishChunk(model, finishReason string, totalTokens int) string {
	return openAIDataBlock(jsonMustf(`{"model":%s,"choices":[{"index":0,"delta":{},"finish_reason":%s}],"usage":{"total_tokens":%d}}`, jsonStr(model), jsonStr(finishReason), totalTokens))
}

func openAIDoneEvent() string {
	return openAIDataBlock("[DONE]")
}

// openAISSESuccess assembles a full, well-formed streaming response for
// a text-only reply, splitting text across two content deltas.
func openAISSESuccess(text, model string, totalTokens int) string {
	part1, part2 := splitOnRuneBoundary(text)
	var out string
	if part1 != "" {
		out += openAIContentDeltaChunk(model, part1)
	}
	if part2 != "" {
		out += openAIContentDeltaChunk(model, part2)
	}
	out += openAIFinishChunk(model, "stop", totalTokens)
	out += openAIDoneEvent()
	return out
}
