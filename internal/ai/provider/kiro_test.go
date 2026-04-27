package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestKiroClassifySuccess(t *testing.T) {
	// Kiro emits plain text / markdown; we feed JSON directly to verify
	// the parser hook works end-to-end.
	body := `{"groups":[{"type":"docs","files":["README.md"],"rationale":"typo"}]}`
	runner := &FakeCommandRunner{Responses: []FakeCommandResponse{{Stdout: []byte(body)}}}
	k := &Kiro{Runner: runner, Binary: "kiro-cli"}
	res, err := k.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "README.md", Status: "modified", DiffHint: "-typo\n+fixed\n"}},
		AllowedTypes: []string{"docs", "feat"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Type != "docs" {
		t.Errorf("groups: %+v", res.Groups)
	}
	args := runner.Calls[0].Args
	want := []string{"chat", "--no-interactive", "--trust-tools="}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("args[%d]: want %q, got %q (%v)", i, w, args[i], args)
		}
	}
}

func TestKiroComposePlainTextFallback(t *testing.T) {
	// Kiro responds in plain text — the parser's text fallback should
	// take over when JSON isn't parseable.
	runner := &FakeCommandRunner{
		Responses: []FakeCommandResponse{{Stdout: []byte("feat(ai): add kiro provider\n\nSubprocess-based adapter for kiro-cli headless.\n")}},
	}
	k := &Kiro{Runner: runner, Binary: "kiro-cli"}
	res, err := k.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Scope: "ai", Files: []string{"kiro.go"}},
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
		Diff:             "+x\n",
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if res.Subject != "feat(ai): add kiro provider" {
		t.Errorf("subject: %q", res.Subject)
	}
	if !strings.Contains(res.Body, "Subprocess") {
		t.Errorf("body: %q", res.Body)
	}
}

func TestKiroSummarizeSuccess(t *testing.T) {
	runner := &FakeCommandRunner{
		Responses: []FakeCommandResponse{{Stdout: []byte("\x1b[1mRisk: low\x1b[0m\nInspect: file.go\n")}},
	}
	k := &Kiro{Runner: runner, Binary: "kiro-cli"}
	res, err := k.Summarize(context.Background(), SummarizeInput{
		Kind:    "merge-plan",
		Diff:    "Target: main\nPrecheck: clean\n",
		Commits: []string{"abc123 feat: incoming"},
		Lang:    "en",
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.Contains(res.Text, "Risk: low") {
		t.Errorf("summary text: %q", res.Text)
	}
	if strings.Contains(res.Text, "\x1b[") {
		t.Errorf("summary should strip ANSI escapes: %q", res.Text)
	}
	if res.Provider != "kiro" {
		t.Errorf("Provider = %q", res.Provider)
	}
	call := runner.Calls[0]
	if !strings.Contains(call.Args[3], "Task: generate a merge plan") {
		t.Errorf("prompt missing merge-plan task: %q", call.Args[3])
	}
	if !strings.Contains(call.Args[3], "Target: main") {
		t.Errorf("prompt argument should include diff payload")
	}
	if len(call.Stdin) != 0 {
		t.Errorf("summarize should not duplicate diff on stdin")
	}
}

func TestKiroEmptyResponseErrors(t *testing.T) {
	runner := &FakeCommandRunner{Responses: []FakeCommandResponse{{Stdout: nil}}}
	k := &Kiro{Runner: runner, Binary: "kiro-cli"}
	_, err := k.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("want ErrProviderResponse, got %v", err)
	}
}

// Note: Kiro.Available's "kiro IDE launcher detected" branch uses
// exec.LookPath, which we cannot mock without adding indirection.
// The branch is exercised in integration tests; here we only test
// the not-found case.
func TestKiroAvailableMissingBinary(t *testing.T) {
	k := &Kiro{Binary: "kiro-cli-absolutely-not-on-this-system-xyz"}
	err := k.Available(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("want ErrNotInstalled, got %v", err)
	}
}
