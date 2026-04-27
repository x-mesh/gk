package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestStartBubbleSpinner_StartDelaySuppressesQuickOps(t *testing.T) {
	var buf bytes.Buffer
	stop := startBubbleSpinnerTo(&buf, "test")
	time.Sleep(20 * time.Millisecond)
	stop()

	out := buf.String()
	for _, frame := range []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"} {
		if strings.Contains(out, frame) {
			t.Fatalf("spinner frame %q appeared for sub-delay op: %q", frame, out)
		}
	}
}

func TestStartBubbleSpinner_DrawsThenClears(t *testing.T) {
	var buf bytes.Buffer
	stop := startBubbleSpinnerTo(&buf, "loading")
	time.Sleep(SpinnerStartDelay + 200*time.Millisecond)
	stop()

	out := buf.String()
	if !strings.Contains(out, "loading") {
		t.Fatalf("expected message in output, got: %q", out)
	}
	if !strings.Contains(out, "\x1b[2K") {
		t.Fatalf("expected ANSI clear escape after stop, got: %q", out)
	}
}

func TestStartBubbleSpinner_NonTTYNoop(t *testing.T) {
	// When stderr is not a TTY (the typical Go test environment),
	// StartBubbleSpinner returns a no-op stop function. Calling it
	// twice must be safe.
	stop := StartBubbleSpinner("noop")
	stop()
	stop()
}

func TestStartBubbleSpinner_StopIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	stop := startBubbleSpinnerTo(&buf, "x")
	stop()
	stop() // must not panic on double-close
}
