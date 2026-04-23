package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestQwenClassifySuccess(t *testing.T) {
	events := `[
    {"type":"assistant","text":"{\"groups\":[{\"type\":\"fix\",\"files\":[\"x.go\"],\"rationale\":\"npe\"}]}","model":"qwen3-coder"}
  ]`
	runner := &FakeCommandRunner{Responses: []FakeCommandResponse{{Stdout: []byte(events)}}}
	q := &Qwen{Runner: runner, Binary: "qwen", EnvLookup: func(string) string { return "x" }}
	res, err := q.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "x.go", Status: "modified", DiffHint: "-y\n+z\n"}},
		AllowedTypes: []string{"feat", "fix"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Type != "fix" {
		t.Errorf("groups: %+v", res.Groups)
	}
	if res.Model != "qwen3-coder" {
		t.Errorf("model: %q", res.Model)
	}
}

func TestQwenReturnsErrOnIsError(t *testing.T) {
	// Qwen can exit 0 AND embed an error inside the JSON array.
	events := `[{"type":"result","subtype":"error_during_execution","is_error":true,"error":{"message":"No auth type is selected"}}]`
	runner := &FakeCommandRunner{Responses: []FakeCommandResponse{{Stdout: []byte(events)}}}
	q := &Qwen{Runner: runner, Binary: "qwen", EnvLookup: func(string) string { return "x" }}
	_, err := q.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+a\n"}},
		AllowedTypes: []string{"feat"},
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("want ErrProviderResponse on is_error, got %v", err)
	}
	if !strings.Contains(err.Error(), "No auth type is selected") {
		t.Errorf("message not preserved: %v", err)
	}
}

func TestQwenAvailableWithoutEnvReturnsUnauth(t *testing.T) {
	// Fake LookPath via Binary path — we can't mock exec.LookPath here,
	// so use the actual `go` binary as a stand-in that exists on PATH.
	q := &Qwen{Binary: "go", EnvLookup: func(string) string { return "" }}
	err := q.Available(context.Background())
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("want ErrUnauthenticated, got %v", err)
	}
}

func TestQwenAvailableWithEnvOK(t *testing.T) {
	q := &Qwen{Binary: "go", EnvLookup: func(k string) string {
		if k == "DASHSCOPE_API_KEY" {
			return "sk-abc"
		}
		return ""
	}}
	if err := q.Available(context.Background()); err != nil {
		t.Errorf("Available: %v", err)
	}
}

func TestQwenClassifyFallbackWhenStdoutIsNonJSON(t *testing.T) {
	// Plain-text stdout should be passed through to parser; classify has
	// no plain-text fallback so the call fails with ErrProviderResponse.
	runner := &FakeCommandRunner{Responses: []FakeCommandResponse{{Stdout: []byte("not json")}}}
	q := &Qwen{Runner: runner, Binary: "qwen", EnvLookup: func(string) string { return "x" }}
	_, err := q.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "a.go", Status: "added"}},
		AllowedTypes: []string{"feat"},
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("want ErrProviderResponse, got %v", err)
	}
}
