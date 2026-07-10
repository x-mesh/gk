package provider

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
)

// maxSSEStreamBytes bounds the TOTAL bytes scanSSE will read from one
// response body. Scanner's own 4 MiB limit only bounds a single line; a
// server that never stops emitting well-formed short events would grow
// the caller's text accumulator without bound until the round deadline
// fired. 16 MiB is far above any real chat completion (a 200k-token
// answer is well under 1 MiB) and far below a memory problem.
//
// This bounds the STREAMING path only. The non-stream path still reads
// its body with io.ReadAll (parseResponse / anthropic invoke), which is
// pre-existing behavior and unchanged here.
const maxSSEStreamBytes = 16 << 20

// errSSETooLarge is returned by scanSSE when a stream exceeds
// maxSSEStreamBytes. Callers treat every scanSSE error alike — abandon
// the stream, fall back to a non-stream request — so this needs no
// special handling, but it is a distinct value so a caller that wants to
// stop retrying a flooding endpoint can tell it apart from a cutoff.
var errSSETooLarge = errors.New("sse: stream exceeded size limit")

// sseEvent is one decoded Server-Sent Event. Event is the optional
// `event:` field — Anthropic's Messages API always sends one (e.g.
// "content_block_delta"); OpenAI-compatible Chat Completions streaming
// sends bare `data:` lines with no `event:` field at all, so callers
// that only look at Data still work. Data is the join of every `data:`
// line in the event (SSE allows more than one; in practice both
// vendors here send exactly one per event).
type sseEvent struct {
	Event string
	Data  string
}

// scanSSE reads r as a Server-Sent Events stream, line by line, and
// invokes onEvent once per event (delimited by a blank line). onEvent
// returns true to stop scanning early — e.g. once a tool_use/tool_calls
// block has been spotted and the caller has already decided to fall
// back, there is no need to keep reading the rest of the body.
//
// Reading is bufio.Scanner-based: Scanner buffers across however the
// underlying network reader happens to chunk its Read() calls, so a
// multi-byte UTF-8 rune — or even the literal bytes "data:" — split
// across two physical reads is transparently reassembled before a
// line is ever handed to the parser. There is nothing SSE-specific to
// do for that case beyond scanning by line; a naive byte-at-a-time
// mock reader exercises exactly this path in tests.
//
// Comment lines (leading ':', SSE's keep-alive convention) and blank
// keep-alive pings are silently ignored, never surfacing as an event.
// Unrecognized fields (e.g. "id:", "retry:") are likewise ignored per
// the SSE spec but still mark the enclosing event non-empty so a lone
// unknown field doesn't get silently folded into whatever follows it.
//
// Returns nil when the stream ended cleanly — natural EOF (including
// one with a final, non-blank-line-terminated event, which is still
// flushed), or onEvent requesting an early stop — and returns the
// underlying reader's error when the connection was severed before a
// clean end. That distinction is what lets callers implement "stream
// cutoff ⇒ full retry": a non-nil error here always means the body
// never finished naturally, no matter how much was already delivered
// to onEvent.
func scanSSE(r io.Reader, onEvent func(sseEvent) (stop bool)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var cur sseEvent
	var data []string
	hasContent := false

	flush := func() bool {
		if !hasContent {
			return false
		}
		cur.Data = strings.Join(data, "\n")
		stop := onEvent(cur)
		cur = sseEvent{}
		data = data[:0]
		hasContent = false
		return stop
	}

	total := 0
	for sc.Scan() {
		line := sc.Text()
		// +1 for the newline the Scanner stripped — the budget tracks
		// wire bytes, not the post-split view of them.
		total += len(line) + 1
		if total > maxSSEStreamBytes {
			return errSSETooLarge
		}
		switch {
		case line == "":
			if flush() {
				return nil
			}
		case strings.HasPrefix(line, ":"):
			// SSE comment / keep-alive line — never becomes part of an
			// event, and never triggers a flush by itself.
		case strings.HasPrefix(line, "event:"):
			cur.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			hasContent = true
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ")
			data = append(data, d)
			hasContent = true
		default:
			hasContent = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	// A well-formed stream ends every event with a trailing blank line;
	// some servers omit it on the very last event, so flush whatever is
	// still pending before reporting a clean end.
	flush()
	return nil
}

// streamAttemptContext used to carve a fixed FRACTION of the round budget
// for the streaming attempt, reserving the rest for the non-stream
// fallback. That was wrong: a fraction of the TOTAL time cannot tell a
// hung stream (no bytes arriving) from a slow-but-progressing one (a
// legitimately long answer streaming steadily), so any reply whose
// generation ran past the fraction was cut off AND then re-generated from
// scratch on the smaller remaining budget — permanently unanswerable, the
// exact truncation the per-attempt Timeout floor was meant to end.
//
// The streaming attempt now gets the round context UNCHANGED. The upper
// bound on a truly hung stream is the same one every request has: the
// per-attempt HTTP Timeout (floored to round_timeout in chat.go), so a
// server that connects and then sends nothing is still cut at the round
// deadline — and in that case the non-stream fallback, hitting the same
// dead endpoint, would fail too, so reserving time for it buys nothing.
// This function is kept as an identity pass-through so both adapters'
// call sites and their defer cancel() stay unchanged.
func streamAttemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return ctx, func() {}
}
