package ui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestFzfAvailable_NonTTY verifies that FzfAvailable returns false when
// IsTerminal() is false (test processes have no TTY attached).
func TestFzfAvailable_NonTTY(t *testing.T) {
	// In a test binary, stdin/stdout are not a TTY, so IsTerminal() == false.
	got := FzfAvailable()
	if got {
		t.Fatal("expected FzfAvailable()=false in non-TTY test environment")
	}
}

// TestFallbackPicker_Pick verifies that a numeric selection returns the correct item.
func TestFallbackPicker_Pick(t *testing.T) {
	items := []PickerItem{
		{Display: "alpha", Key: "a"},
		{Display: "beta", Key: "b"},
		{Display: "gamma", Key: "c"},
	}
	in := strings.NewReader("2\n")
	var out strings.Builder
	p := &FallbackPicker{In: in, Out: &out}

	got, err := p.Pick(context.Background(), "choose", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Key != "b" {
		t.Fatalf("expected key=%q, got %q", "b", got.Key)
	}
	if got.Display != "beta" {
		t.Fatalf("expected display=%q, got %q", "beta", got.Display)
	}
}

// TestFallbackPicker_EmptyInput verifies that EOF/empty input returns ErrPickerAborted.
func TestFallbackPicker_EmptyInput(t *testing.T) {
	items := []PickerItem{{Display: "only", Key: "only"}}
	in := strings.NewReader("") // EOF immediately
	var out strings.Builder
	p := &FallbackPicker{In: in, Out: &out}

	_, err := p.Pick(context.Background(), "choose", items)
	if !errors.Is(err, ErrPickerAborted) {
		t.Fatalf("expected ErrPickerAborted, got %v", err)
	}
}

// TestFallbackPicker_Quit verifies that "q" returns ErrPickerAborted.
func TestFallbackPicker_Quit(t *testing.T) {
	items := []PickerItem{{Display: "only", Key: "only"}}
	in := strings.NewReader("q\n")
	var out strings.Builder
	p := &FallbackPicker{In: in, Out: &out}

	_, err := p.Pick(context.Background(), "choose", items)
	if !errors.Is(err, ErrPickerAborted) {
		t.Fatalf("expected ErrPickerAborted, got %v", err)
	}
}

// TestFallbackPicker_InvalidNumber verifies that out-of-range input returns an error
// containing "invalid selection".
func TestFallbackPicker_InvalidNumber(t *testing.T) {
	items := []PickerItem{
		{Display: "alpha", Key: "a"},
		{Display: "beta", Key: "b"},
	}
	in := strings.NewReader("99\n")
	var out strings.Builder
	p := &FallbackPicker{In: in, Out: &out}

	_, err := p.Pick(context.Background(), "choose", items)
	if err == nil {
		t.Fatal("expected error for invalid selection, got nil")
	}
	if !strings.Contains(err.Error(), "invalid selection") {
		t.Fatalf("expected 'invalid selection' in error, got %q", err.Error())
	}
}

// TestFallbackPicker_NoItems verifies that an empty item slice returns an error.
func TestFallbackPicker_NoItems(t *testing.T) {
	var out strings.Builder
	p := &FallbackPicker{In: strings.NewReader(""), Out: &out}

	_, err := p.Pick(context.Background(), "choose", nil)
	if err == nil {
		t.Fatal("expected error for no items, got nil")
	}
	if !strings.Contains(err.Error(), "no items to pick") {
		t.Fatalf("expected 'no items to pick' in error, got %q", err.Error())
	}
}

// TestShellQuote verifies that single-quotes inside the string are properly escaped.
func TestShellQuote(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
		{"a'b'c", `'a'\''b'\''c'`},
	}
	for _, tc := range cases {
		got := shellQuote(tc.input)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestFzfPicker_SkipWhenNoFzf exercises FzfPicker.Pick only in a TTY + fzf-present
// environment. Otherwise the test would hang — fzf waits on the TTY indefinitely —
// so we skip outside that narrow combination.
func TestFzfPicker_SkipWhenNoFzf(t *testing.T) {
	if _, err := exec.LookPath("fzf"); err != nil {
		t.Skip("fzf not found on PATH; skipping FzfPicker integration test")
	}
	if !IsTerminal() {
		t.Skip("stdout/stdin not a TTY; FzfPicker requires a TTY (fzf blocks otherwise)")
	}
	// Enforce a hard ceiling so a misbehaving fzf can't wedge the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p := &FzfPicker{}
	items := []PickerItem{{Display: "one", Key: "1"}}
	_, err := p.Pick(ctx, "test", items)
	_ = err // any outcome is acceptable; we only guard against hangs.
}
