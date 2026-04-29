package cli

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

// setNoColor temporarily flips color.NoColor for the duration of fn.
// Restores the original value via t.Cleanup so test ordering is safe.
func setNoColor(t *testing.T, v bool, fn func()) {
	t.Helper()
	prev := color.NoColor
	color.NoColor = v
	t.Cleanup(func() { color.NoColor = prev })
	fn()
}

func TestCellColor_NoColorPassthrough(t *testing.T) {
	setNoColor(t, true, func() {
		cases := []struct {
			name string
			fn   func(string) string
		}{
			{"red", cellRed},
			{"green", cellGreen},
			{"yellow", cellYellow},
			{"cyan", cellCyan},
			{"redBold", cellRedBold},
			{"faint", cellFaint},
		}
		for _, c := range cases {
			got := c.fn("hello")
			if got != "hello" {
				t.Errorf("NoColor=true cell %s should pass through: got %q, want %q",
					c.name, got, "hello")
			}
		}
	})
}

func TestCellColor_FgOnlyResetSequences(t *testing.T) {
	setNoColor(t, false, func() {
		// fg-only reset (\x1b[39m / \x1b[22m) lets surrounding row
		// styles (e.g. bubbles/table Selected background) flow through.
		// Full reset (\x1b[0m) would break the cursor highlight.
		assertNoFullReset := func(t *testing.T, got string) {
			t.Helper()
			if strings.Contains(got, "\x1b[0m") {
				t.Errorf("must not emit full reset \\x1b[0m: got %q", got)
			}
		}
		t.Run("red", func(t *testing.T) {
			got := cellRed("X")
			if !strings.Contains(got, "\x1b[31m") || !strings.Contains(got, "\x1b[39m") {
				t.Errorf("red: missing fg/reset, got %q", got)
			}
			assertNoFullReset(t, got)
		})
		t.Run("green", func(t *testing.T) {
			got := cellGreen("X")
			if !strings.Contains(got, "\x1b[32m") || !strings.Contains(got, "\x1b[39m") {
				t.Errorf("green: missing fg/reset, got %q", got)
			}
			assertNoFullReset(t, got)
		})
		t.Run("yellow", func(t *testing.T) {
			got := cellYellow("X")
			if !strings.Contains(got, "\x1b[33m") || !strings.Contains(got, "\x1b[39m") {
				t.Errorf("yellow: missing fg/reset, got %q", got)
			}
			assertNoFullReset(t, got)
		})
		t.Run("cyan", func(t *testing.T) {
			got := cellCyan("X")
			if !strings.Contains(got, "\x1b[36m") || !strings.Contains(got, "\x1b[39m") {
				t.Errorf("cyan: missing fg/reset, got %q", got)
			}
			assertNoFullReset(t, got)
		})
		t.Run("redBold", func(t *testing.T) {
			got := cellRedBold("X")
			// Bold + red on, both reset partially at end.
			if !strings.Contains(got, "\x1b[1m") || !strings.Contains(got, "\x1b[31m") {
				t.Errorf("redBold: missing bold/red, got %q", got)
			}
			if !strings.Contains(got, "\x1b[22m") || !strings.Contains(got, "\x1b[39m") {
				t.Errorf("redBold: missing partial resets, got %q", got)
			}
			assertNoFullReset(t, got)
		})
		t.Run("faint", func(t *testing.T) {
			got := cellFaint("X")
			if !strings.Contains(got, "\x1b[2m") || !strings.Contains(got, "\x1b[22m") {
				t.Errorf("faint: missing dim/reset, got %q", got)
			}
			assertNoFullReset(t, got)
		})
	})
}

func TestCellColor_PreservesPayload(t *testing.T) {
	setNoColor(t, false, func() {
		const payload = "feat/x ↑3 ↓1"
		for _, fn := range []func(string) string{
			cellRed, cellGreen, cellYellow, cellCyan, cellRedBold, cellFaint,
		} {
			got := fn(payload)
			if !strings.Contains(got, payload) {
				t.Errorf("payload missing: got %q", got)
			}
		}
	})
}
