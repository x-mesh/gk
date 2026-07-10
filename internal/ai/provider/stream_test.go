package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Basic multi-event stream parses into the right (event, data) pairs in
// order.
func TestScanSSE_BasicEvents(t *testing.T) {
	raw := sseBlock("message_start", `{"a":1}`) + sseBlock("message_stop", `{"b":2}`)
	var got []sseEvent
	err := scanSSE(strings.NewReader(raw), func(ev sseEvent) bool {
		got = append(got, ev)
		return false
	})
	if err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(got), got)
	}
	if got[0].Event != "message_start" || got[0].Data != `{"a":1}` {
		t.Errorf("event[0] = %+v", got[0])
	}
	if got[1].Event != "message_stop" || got[1].Data != `{"b":2}` {
		t.Errorf("event[1] = %+v", got[1])
	}
}

// Comment lines (SSE keep-alive convention, leading ':') and blank
// keep-alive pings never surface as events and never corrupt adjacent
// real events.
func TestScanSSE_IgnoresCommentsAndKeepAlives(t *testing.T) {
	raw := ": keep-alive\n\n" +
		sseBlock("message_start", `{"a":1}`) +
		": another ping\n\n" +
		sseBlock("message_stop", `{"b":2}`)
	var got []sseEvent
	err := scanSSE(strings.NewReader(raw), func(ev sseEvent) bool {
		got = append(got, ev)
		return false
	})
	if err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %d, want 2 (keep-alives must not surface): %+v", len(got), got)
	}
}

// A bare "data:" stream with no "event:" lines at all (OpenAI-compatible
// Chat Completions shape) parses fine — Event stays "".
func TestScanSSE_DataOnlyNoEventField(t *testing.T) {
	raw := openAIDataBlock(`{"x":1}`) + openAIDataBlock("[DONE]")
	var got []sseEvent
	err := scanSSE(strings.NewReader(raw), func(ev sseEvent) bool {
		got = append(got, ev)
		return false
	})
	if err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(got), got)
	}
	if got[0].Event != "" || got[0].Data != `{"x":1}` {
		t.Errorf("event[0] = %+v", got[0])
	}
	if got[1].Data != "[DONE]" {
		t.Errorf("event[1] = %+v", got[1])
	}
}

// onEvent returning true stops the scan early with no error — the
// caller's decision to bail (e.g. tool_use detected) is not itself a
// failure.
func TestScanSSE_EarlyStopIsNotAnError(t *testing.T) {
	raw := sseBlock("a", `{}`) + sseBlock("b", `{}`) + sseBlock("c", `{}`)
	var got []sseEvent
	err := scanSSE(strings.NewReader(raw), func(ev sseEvent) bool {
		got = append(got, ev)
		return ev.Event == "b"
	})
	if err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %d, want 2 (stopped after b): %+v", len(got), got)
	}
}

// A read error that interrupts the stream mid-event (severed connection)
// must be reported distinctly from a clean end — this is the signal
// callers use to trigger "stream cutoff ⇒ full retry".
func TestScanSSE_ReadErrorPropagates(t *testing.T) {
	raw := sseBlock("message_start", `{"a":1}`) + `event: content_block_delta` + "\n" + `data: {"partial":true`
	errSevered := errors.New("connection reset by peer")
	r := &sseChunkReader{chunks: [][]byte{[]byte(raw)}, endErr: errSevered}
	var got []sseEvent
	err := scanSSE(r, func(ev sseEvent) bool {
		got = append(got, ev)
		return false
	})
	if !errors.Is(err, errSevered) {
		t.Fatalf("err = %v, want %v", err, errSevered)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1 (the dangling partial event was never terminated by a blank line)", len(got))
	}
}

// A clean EOF with no read error is reported as nil, even when the
// final event lacks its trailing blank line (some servers omit it).
func TestScanSSE_CleanEOFFlushesTrailingEvent(t *testing.T) {
	raw := "event: message_stop\ndata: {}" // no trailing blank line
	var got []sseEvent
	err := scanSSE(strings.NewReader(raw), func(ev sseEvent) bool {
		got = append(got, ev)
		return false
	})
	if err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	if len(got) != 1 || got[0].Event != "message_stop" {
		t.Fatalf("got = %+v, want one flushed message_stop event", got)
	}
}

// Adversarial: a multi-byte UTF-8 rune (and the "event:"/"data:" prefix
// literals themselves) split across one-byte reads must reassemble
// correctly with no panic and no corruption — bufio.Scanner buffers
// across Read() calls regardless of chunk size.
func TestScanSSE_FragmentedUTF8Boundaries(t *testing.T) {
	text := "안녕하세요 — 결과 🎉 done"
	raw := sseBlock("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+jsonStr(text)+`}}`)
	var gotData string
	err := scanSSE(&sseChunkReader{chunks: byteChunks(raw)}, func(ev sseEvent) bool {
		gotData = ev.Data
		return false
	})
	if err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	want := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + jsonStr(text) + `}}`
	if gotData != want {
		t.Fatalf("data = %q, want %q (UTF-8 corrupted across 1-byte reads)", gotData, want)
	}
}

// byteChunks splits s into 1-byte chunks for sseChunkReader.
func byteChunks(s string) [][]byte {
	out := make([][]byte, len(s))
	for i := 0; i < len(s); i++ {
		out[i] = []byte{s[i]}
	}
	return out
}

// TestScanSSE_SizeLimit pins the panel finding that scanSSE accumulated
// without bound: Scanner's own limit caps ONE line, so an endpoint that
// never stops emitting short, well-formed events grew the caller's text
// builder until the round deadline fired. Past maxSSEStreamBytes the scan
// aborts with errSSETooLarge, which callers already treat like any other
// stream error (abandon, fall back to non-stream).
func TestScanSSE_SizeLimit(t *testing.T) {
	// One event just over the budget, delivered as many short lines.
	line := "data: " + strings.Repeat("x", 1024) + "\n"
	reps := (maxSSEStreamBytes / len(line)) + 2
	body := strings.NewReader(strings.Repeat(line, reps))

	events := 0
	err := scanSSE(body, func(sseEvent) bool { events++; return false })
	if !errors.Is(err, errSSETooLarge) {
		t.Fatalf("scanSSE over budget = %v, want errSSETooLarge", err)
	}
}

// TestScanSSE_UnderLimitUnaffected proves the cap does not perturb any
// realistic stream: a normal-sized body still scans to a clean end.
func TestScanSSE_UnderLimitUnaffected(t *testing.T) {
	body := strings.NewReader("event: x\ndata: hello\n\n")
	got := 0
	if err := scanSSE(body, func(ev sseEvent) bool {
		got++
		if ev.Data != "hello" {
			t.Errorf("Data = %q, want hello", ev.Data)
		}
		return false
	}); err != nil {
		t.Fatalf("scanSSE under budget = %v, want nil", err)
	}
	if got != 1 {
		t.Errorf("events = %d, want 1", got)
	}
}

// ── streamAttemptContext (now an identity pass-through) ──────────────

// The streaming attempt gets the round context UNCHANGED — the old
// fraction cap cut off legitimately long answers that streamed past the
// 60% mark and re-generated them on a smaller budget (the 3rd-panel
// regression). A deadline'd parent must come back with the SAME deadline,
// not a shortened one.
func TestStreamAttemptContext_ReturnsRoundContextUnchanged(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	parentDeadline, _ := parent.Deadline()

	got, subCancel := streamAttemptContext(parent)
	defer subCancel()

	gotDeadline, ok := got.Deadline()
	if !ok {
		t.Fatalf("returned context lost the round's deadline entirely")
	}
	if !gotDeadline.Equal(parentDeadline) {
		t.Errorf("streaming attempt deadline = %v, want the round's OWN deadline %v unchanged (no fraction carved off)", gotDeadline, parentDeadline)
	}
}

// A context with no deadline at all (context.Background(), or any
// caller that doesn't bound rounds) has nothing to share from, so it is
// returned completely unchanged — no spurious cap is invented.
func TestStreamAttemptContext_NoDeadlinePassesThrough(t *testing.T) {
	parent := context.Background()
	got, cancel := streamAttemptContext(parent)
	defer cancel()
	if got != parent {
		t.Errorf("streamAttemptContext(no-deadline ctx) returned a different context, want the same one back unchanged")
	}
	if _, ok := got.Deadline(); ok {
		t.Errorf("returned context unexpectedly has a deadline")
	}
}

// A context whose deadline has ALREADY passed has nothing left to
// divide either — returned unchanged rather than manufacturing a
// deadline that's already in the past (which context.WithTimeout would
// happily do, but there is no reason to allocate a new context/timer
// for it).
func TestStreamAttemptContext_AlreadyExpiredPassesThrough(t *testing.T) {
	parent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	got, subCancel := streamAttemptContext(parent)
	defer subCancel()
	if got != parent {
		t.Errorf("streamAttemptContext(already-expired ctx) returned a different context, want the same one back unchanged")
	}
}
