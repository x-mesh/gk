package ui

import (
	"testing"
)

func TestAutoPrompter_ReturnsDefault(t *testing.T) {
	p := &AutoPrompter{Default: ChoiceAbort}
	got, err := p.ConflictChoice("title", "context", []ConflictChoice{ChoiceContinue, ChoiceAbort})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ChoiceAbort {
		t.Errorf("expected %q, got %q", ChoiceAbort, got)
	}
}

func TestAutoPrompter_EmptyDefault_ReturnsErrNonInteractive(t *testing.T) {
	p := &AutoPrompter{}
	_, err := p.ConflictChoice("title", "context", []ConflictChoice{ChoiceContinue, ChoiceAbort})
	if err != ErrNonInteractive {
		t.Errorf("expected ErrNonInteractive, got %v", err)
	}
}

func TestAutoPrompter_DefaultNotInAllowed_ReturnsErrNonInteractive(t *testing.T) {
	p := &AutoPrompter{Default: "xxx"}
	_, err := p.ConflictChoice("title", "context", []ConflictChoice{ChoiceContinue, ChoiceAbort})
	if err != ErrNonInteractive {
		t.Errorf("expected ErrNonInteractive, got %v", err)
	}
}

func TestAutoPrompter_SingleAllowed_Match(t *testing.T) {
	p := &AutoPrompter{Default: ChoiceContinue}
	got, err := p.ConflictChoice("title", "context", []ConflictChoice{ChoiceContinue})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ChoiceContinue {
		t.Errorf("expected %q, got %q", ChoiceContinue, got)
	}
}

// TestIsTerminal only checks that the function runs without panicking.
// In CI stdout is not a TTY, so we do not assert the return value.
func TestIsTerminal_NoPanic(t *testing.T) {
	_ = IsTerminal()
}

func TestNewPrompter_NonTTY_ReturnsAutoPrompter(t *testing.T) {
	// In CI (non-TTY) NewPrompter should return *AutoPrompter.
	// In a real terminal it returns *TermPrompter — either is valid.
	p := NewPrompter(ChoiceAbort)
	if p == nil {
		t.Fatal("expected non-nil Prompter")
	}
}
