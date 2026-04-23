package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestStartSpinner_StartDelaySuppressesQuickOps asserts the 150ms
// start delay — a call that stops before the first frame must emit
// zero output to the writer.
func TestStartSpinner_StartDelaySuppressesQuickOps(t *testing.T) {
	buf := &bytes.Buffer{}
	stop := startSpinnerTo(buf, "quick op")
	// Stop well before SpinnerStartDelay.
	time.Sleep(20 * time.Millisecond)
	stop()
	if buf.Len() != 0 {
		t.Errorf("quick op should not draw, got %q", buf.String())
	}
}

// TestStartSpinner_DrawsThenClears asserts the spinner both paints a
// frame and leaves a clean trailing clear sequence. We do not assert
// on specific frames because the goroutine scheduler may land us on
// any frame between start and stop.
func TestStartSpinner_DrawsThenClears(t *testing.T) {
	buf := &bytes.Buffer{}
	stop := startSpinnerTo(buf, "slow op")
	// Sleep long enough for at least one frame to render.
	time.Sleep(SpinnerStartDelay + 100*time.Millisecond)
	stop()

	got := buf.String()
	if !strings.Contains(got, "slow op") {
		t.Errorf("expected message in output, got %q", got)
	}
	// The final write should end with the clear sequence (either the
	// padded overwrite or the CSI 2K escape).
	if !strings.HasSuffix(got, "\x1b[2K") {
		t.Errorf("expected trailing ANSI clear, got tail %q", last(got, 12))
	}
}

// last returns the last n bytes of s (or the full string if shorter).
func last(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
