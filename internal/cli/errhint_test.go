package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestWithHint_Nil(t *testing.T) {
	if WithHint(nil, "irrelevant") != nil {
		t.Error("WithHint(nil, ...) must return nil")
	}
}

func TestWithHint_EmptyHint(t *testing.T) {
	err := errors.New("boom")
	got := WithHint(err, "")
	if got != err {
		t.Errorf("empty hint should return original err, got %v", got)
	}
}

func TestWithHint_PreservesUnwrap(t *testing.T) {
	base := errors.New("boom")
	wrapped := WithHint(base, "try: x")
	if !errors.Is(wrapped, base) {
		t.Error("errors.Is should reach the base error through the hint wrapper")
	}
	if HintFrom(wrapped) != "try: x" {
		t.Errorf("HintFrom = %q, want 'try: x'", HintFrom(wrapped))
	}
}

func TestHintFrom_WalksChain(t *testing.T) {
	base := errors.New("root")
	withHint := WithHint(base, "try: outer")
	// Further wrapping via the standard library should not hide the hint.
	outer := wrapError("context", withHint)
	if HintFrom(outer) != "try: outer" {
		t.Errorf("HintFrom through wrapError = %q, want 'try: outer'", HintFrom(outer))
	}
}

func TestHintFrom_NoHint(t *testing.T) {
	if HintFrom(nil) != "" {
		t.Error("HintFrom(nil) must be empty")
	}
	if HintFrom(errors.New("plain")) != "" {
		t.Error("HintFrom on plain err must be empty")
	}
}

func TestFormatError_NoHint(t *testing.T) {
	got := FormatError(errors.New("boom"))
	want := "gk: boom"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatError_WithHint(t *testing.T) {
	err := WithHint(errors.New("boom"), "try: gk abort")
	got := FormatError(err)

	if !strings.HasPrefix(got, "gk: boom") {
		t.Errorf("first line should be 'gk: boom', got: %q", got)
	}
	if !strings.Contains(got, "hint: try: gk abort") {
		t.Errorf("output should contain hint, got: %q", got)
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("output should be exactly 2 lines, got %d newlines: %q", strings.Count(got, "\n"), got)
	}
}

func TestFormatError_Nil(t *testing.T) {
	if FormatError(nil) != "" {
		t.Error("FormatError(nil) must be empty string")
	}
}

func TestHintCommand(t *testing.T) {
	got := hintCommand("gk continue")
	if got != "try: gk continue" {
		t.Errorf("hintCommand = %q, want 'try: gk continue'", got)
	}
}

// wrapError is a tiny test helper that mimics fmt.Errorf("%s: %w") behavior
// without bringing in the full formatter — keeps the test table focused on
// error-chain semantics rather than string formatting.
type fmtWrap struct {
	msg string
	err error
}

func (f *fmtWrap) Error() string { return f.msg + ": " + f.err.Error() }
func (f *fmtWrap) Unwrap() error { return f.err }

func wrapError(msg string, err error) error { return &fmtWrap{msg: msg, err: err} }
