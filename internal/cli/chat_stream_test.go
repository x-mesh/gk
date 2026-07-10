package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// onTextDelta must echo every chunk to out immediately and accumulate it
// into streamed, so a later textAlreadyPrinted check can detect the
// final round's answer was already fully displayed.
func TestChatTurnUI_OnTextDeltaEchoesAndAccumulates(t *testing.T) {
	var buf bytes.Buffer
	u := &chatTurnUI{out: &buf}
	u.onTextDelta("hello ")
	u.onTextDelta("world")
	if buf.String() != "hello world" {
		t.Errorf("out = %q, want %q", buf.String(), "hello world")
	}
	if u.streamed.String() != "hello world" {
		t.Errorf("streamed = %q, want %q", u.streamed.String(), "hello world")
	}
	if !u.textAlreadyPrinted("hello world") {
		t.Error("textAlreadyPrinted(exact match) = false, want true")
	}
	if !u.textAlreadyPrinted("  hello world  ") {
		t.Error("textAlreadyPrinted must tolerate surrounding whitespace (both sides TrimSpace)")
	}
}

// A round with no streamed text at all (JSONOut()/GK_AGENT, or the
// final round fell back to non-stream) must report textAlreadyPrinted
// == false so the caller falls back to printing res.Text normally —
// an empty streamed buffer must never accidentally "match" empty text.
func TestChatTurnUI_TextAlreadyPrintedFalseWhenNothingStreamed(t *testing.T) {
	u := &chatTurnUI{out: &bytes.Buffer{}}
	if u.textAlreadyPrinted("") {
		t.Error("textAlreadyPrinted(\"\") on an empty streamed buffer must be false")
	}
	if u.textAlreadyPrinted("some answer") {
		t.Error("textAlreadyPrinted must be false when nothing was ever streamed")
	}
}

// onToolCall must reset streamed — text that leaked before a tool_use/
// tool_calls interleave fallback (see provider.ChatInput.OnTextDelta's
// docstring) belongs to a round that turned out to need tools, never
// the final answer, so it must not poison the post-turn
// "already printed?" check for a LATER round's real final answer.
func TestChatTurnUI_OnToolCallResetsStreamedBuffer(t *testing.T) {
	var buf bytes.Buffer
	u := &chatTurnUI{out: &buf}
	u.onTextDelta("let me check") // leaked preamble for a round that turns out to call a tool
	u.onToolCall(provider.ToolCall{Name: "git_log", Input: nil})
	if u.streamed.Len() != 0 {
		t.Fatalf("streamed = %q after onToolCall, want empty (reset)", u.streamed.String())
	}
	// A later round's real final answer must be detected as fully
	// printed on its own, unpolluted by the earlier leaked preamble.
	u.onTextDelta("the final answer")
	if !u.textAlreadyPrinted("the final answer") {
		t.Error("textAlreadyPrinted should be true for the final round's own streamed text")
	}
	// The leaked preamble's line must have been terminated (a newline)
	// before the tool-call transparency line, not run together with it.
	if !strings.Contains(buf.String(), "let me check\n") {
		t.Errorf("output = %q, want the leaked preamble line terminated before the tool-call line", buf.String())
	}
}

// onTextDelta must stop the spinner (idempotently) so streamed text
// doesn't fight the spinner for the same terminal line — verified via
// the exported spinning flag rather than actually starting a real
// spinner (which no-ops outside a TTY in tests anyway).
func TestChatTurnUI_OnTextDeltaStopsSpinner(t *testing.T) {
	u := &chatTurnUI{out: &bytes.Buffer{}}
	u.spinning = true  // pretend a spinner is running
	u.spin = func() {} // no-op stop function
	u.onTextDelta("hi")
	if u.spinning {
		t.Error("onTextDelta must stop the spinner")
	}
}

// onStreamReset must terminate the stale partial line and clear streamed,
// so the fallback answer prints fresh instead of concatenating onto the
// partial (the v2 panel finding: streamed partial + fallback = "ans ans…").
func TestChatTurnUI_OnStreamResetTerminatesPartial(t *testing.T) {
	var buf bytes.Buffer
	u := &chatTurnUI{out: &buf}
	u.onTextDelta("here is a par")
	u.onStreamReset()
	if u.streamed.Len() != 0 {
		t.Errorf("streamed = %q after reset, want empty", u.streamed.String())
	}
	// The partial must be terminated with a newline so the fallback starts
	// on its own line.
	if buf.String() != "here is a par\n" {
		t.Errorf("out = %q, want the partial followed by a newline", buf.String())
	}
	// After a reset, the fallback's full text must NOT be treated as
	// already-printed — the caller has to print it in full.
	if u.textAlreadyPrinted("here is a partial and complete answer") {
		t.Error("textAlreadyPrinted must be false after a reset — the partial was voided")
	}
}

// onStreamReset with nothing streamed is a no-op (no spurious newline).
func TestChatTurnUI_OnStreamResetNoopWhenNothingStreamed(t *testing.T) {
	var buf bytes.Buffer
	u := &chatTurnUI{out: &buf}
	u.onStreamReset()
	if buf.Len() != 0 {
		t.Errorf("out = %q, want empty — reset with no partial must print nothing", buf.String())
	}
}
