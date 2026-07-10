package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGitContextToolReturnsCollectorOutput(t *testing.T) {
	r := NewRegistry(nil, 0)
	want := `{"branch":"main","ahead":0,"behind":0,"dirty":{"staged":0,"unstaged":0,"untracked":0,"conflicts":0}}`
	RegisterContextTools(r, func(context.Context) (string, error) {
		return want, nil
	})
	res := dispatch(t, r, "git_context", `{}`)
	if res.IsError {
		t.Fatalf("git_context error: %s", res.Content)
	}
	if res.Content != want {
		t.Errorf("git_context content = %q, want %q", res.Content, want)
	}
}

// A collector failure must surface as a normal IsError tool result — the
// model should see "that didn't work" and adapt, not crash the turn.
func TestGitContextToolPropagatesCollectorError(t *testing.T) {
	r := NewRegistry(nil, 0)
	RegisterContextTools(r, func(context.Context) (string, error) {
		return "", errors.New("collect failed")
	})
	res := dispatch(t, r, "git_context", `{}`)
	if !res.IsError {
		t.Fatal("expected IsError=true when the collector fails")
	}
	if !strings.Contains(res.Content, "collect failed") {
		t.Errorf("content = %q, want it to mention the collector error", res.Content)
	}
}

// git_context's output goes through Registry.Dispatch like any other tool
// result, so it must be redacted before it reaches the model — the same
// non-negotiable stage every other tool's output gets.
func TestGitContextToolResultIsRedacted(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "hunter2", "[REDACTED]") }
	r := NewRegistry(redact, 0)
	RegisterContextTools(r, func(context.Context) (string, error) {
		return `{"branch":"hunter2"}`, nil
	})
	res := dispatch(t, r, "git_context", `{}`)
	if strings.Contains(res.Content, "hunter2") {
		t.Errorf("git_context result must go through Registry's redactor, got %q", res.Content)
	}
}

// git_context takes no arguments; the handler must still work (and call
// the injected collector exactly once) even if the model sends a stray
// input object.
func TestGitContextToolIgnoresInput(t *testing.T) {
	r := NewRegistry(nil, 0)
	calls := 0
	RegisterContextTools(r, func(context.Context) (string, error) {
		calls++
		return "{}", nil
	})
	res := dispatch(t, r, "git_context", `{"unexpected":"field"}`)
	if res.IsError {
		t.Fatalf("git_context error: %s", res.Content)
	}
	if calls != 1 {
		t.Errorf("collect called %d times, want 1", calls)
	}
}
