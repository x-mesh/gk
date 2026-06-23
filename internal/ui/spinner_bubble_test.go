package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestStartBubbleSpinner_StartDelaySuppressesQuickOps(t *testing.T) {
	var buf bytes.Buffer
	stop := startBubbleSpinnerTo(&buf, "test", 0)
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
	stop := startBubbleSpinnerTo(&buf, "loading", 0)
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
	stop := startBubbleSpinnerTo(&buf, "x", 0)
	stop()
	stop() // must not panic on double-close
}

func TestRenderBudgetTimer(t *testing.T) {
	got := renderBudgetTimer(42*time.Second, 120*time.Second)
	if !strings.Contains(got, "42s / 120s") {
		t.Fatalf("timer text = %q, want it to contain %q", got, "42s / 120s")
	}
}

func TestBudgetTimerColor(t *testing.T) {
	const (
		dim   = "241"
		amber = "214"
		red   = "203"
	)
	cases := []struct {
		name            string
		elapsed, budget time.Duration
		want            string
	}{
		{"early", 10 * time.Second, 120 * time.Second, dim}, // ~8%
		{"just under amber", 95 * time.Second, 120 * time.Second, dim},
		{"amber at 80%", 96 * time.Second, 120 * time.Second, amber},
		{"red at 95%", 114 * time.Second, 120 * time.Second, red},
		{"over budget", 130 * time.Second, 120 * time.Second, red},
		{"no budget stays dim", 42 * time.Second, 0, dim},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(budgetTimerColor(tc.elapsed, tc.budget)); got != tc.want {
				t.Errorf("budgetTimerColor(%s, %s) = %q, want %q", tc.elapsed, tc.budget, got, tc.want)
			}
		})
	}
}
