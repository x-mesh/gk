package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

// ---------- 8.1 / 8.5: command registration & flag parsing ----------

func TestAIPRRegistered(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"pr"})
	if err != nil {
		t.Fatalf("rootCmd.Find(pr): %v", err)
	}
	if found.Use != "pr" {
		t.Errorf("Use: want %q, got %q", "pr", found.Use)
	}
}

func TestAIPRHelpListsFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	found, _, err := rootCmd.Find([]string{"pr"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	found.SetOut(buf)
	found.SetErr(buf)
	if err := found.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"--output", "--dry-run", "--provider", "--lang"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing flag %q\n%s", want, out)
		}
	}
}

// ---------- 8.2 / 8.5: runAIPRCore flow with FakeRunner + FakeSummarizer ----------

func newPRFakeRunner(mergeBase, diff, commits string) *git.FakeRunner {
	return &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			// DefaultBranch probes
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			// merge-base
			"merge-base HEAD main": {Stdout: mergeBase + "\n"},
			// diff
			"diff " + mergeBase + "..HEAD": {Stdout: diff},
			// log
			"log --oneline " + mergeBase + "..HEAD": {Stdout: commits},
		},
	}
}

func TestAIPRCoreHappyPath(t *testing.T) {
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{
		{Text: "## Summary\nGreat PR\n", Model: "test-model", TokensUsed: 42},
	}

	runner := newPRFakeRunner("abc1234", "diff --git a/foo\n+bar\n", "abc1234 feat: add foo")
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}

	err := runAIPRCore(context.Background(), aiPRDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		BaseCfg:  "",
		Remote:   "origin",
		Out:      out,
		ErrOut:   errOut,
	}, aiPRFlags{output: "stdout"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "## Summary") {
		t.Errorf("output should contain summarize result, got: %s", out.String())
	}
	// Verify Summarize was called with correct input.
	if len(fake.Calls) == 0 || fake.Calls[len(fake.Calls)-1] != "Summarize" {
		t.Errorf("expected Summarize call, got calls: %v", fake.Calls)
	}
}

func TestAIPRCoreSummarizeInput(t *testing.T) {
	var captured provider.SummarizeInput
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{{Text: "ok"}}
	fake.OnSummarize = func(in provider.SummarizeInput) {
		captured = in
	}

	runner := newPRFakeRunner("abc1234",
		"diff content here",
		"abc1234 feat: first\ndef5678 fix: second",
	)
	out := &bytes.Buffer{}

	_ = runAIPRCore(context.Background(), aiPRDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "ko",
		BaseCfg:  "",
		Remote:   "origin",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiPRFlags{output: "stdout"})

	if captured.Kind != "pr" {
		t.Errorf("Kind: want %q, got %q", "pr", captured.Kind)
	}
	if captured.Lang != "ko" {
		t.Errorf("Lang: want %q, got %q", "ko", captured.Lang)
	}
	if len(captured.Commits) != 2 {
		t.Errorf("Commits: want 2, got %d", len(captured.Commits))
	}
	if captured.Diff != "diff content here" {
		t.Errorf("Diff mismatch: got %q", captured.Diff)
	}
}

// ---------- 8.3: dry-run output ----------

func TestAIPRCoreDryRun(t *testing.T) {
	fake := provider.NewFake()
	runner := newPRFakeRunner("abc1234", "some diff", "abc1234 feat: stuff")
	out := &bytes.Buffer{}

	err := runAIPRCore(context.Background(), aiPRDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		BaseCfg:  "",
		Remote:   "origin",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiPRFlags{dryRun: true, output: "stdout"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result := out.String()
	if !strings.Contains(result, "dry-run") {
		t.Errorf("dry-run output should mention dry-run, got: %s", result)
	}
	if !strings.Contains(result, "Kind: pr") {
		t.Errorf("dry-run output should show Kind, got: %s", result)
	}
	if !strings.Contains(result, "feat: stuff") {
		t.Errorf("dry-run output should list commits, got: %s", result)
	}
	// Summarize should NOT be called in dry-run.
	for _, c := range fake.Calls {
		if c == "Summarize" {
			t.Error("Summarize should not be called in dry-run mode")
		}
	}
}

// ---------- 8.4: edge case — no commits ahead of base ----------

func TestAIPRCoreNoCommits(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			"merge-base HEAD main":                          {Stdout: "abc1234\n"},
			"diff abc1234..HEAD":                            {Stdout: ""},
			"log --oneline abc1234..HEAD":                   {Stdout: ""},
		},
	}
	fake := provider.NewFake()
	out := &bytes.Buffer{}

	err := runAIPRCore(context.Background(), aiPRDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		BaseCfg:  "",
		Remote:   "origin",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiPRFlags{output: "stdout"})

	if err != nil {
		t.Fatalf("expected nil error for no-commits case, got: %v", err)
	}
	if !strings.Contains(out.String(), "no commits ahead") {
		t.Errorf("should print no-commits message, got: %s", out.String())
	}
	// Summarize should NOT be called.
	for _, c := range fake.Calls {
		if c == "Summarize" {
			t.Error("Summarize should not be called when no commits")
		}
	}
}

// ---------- 8.4: edge case — explicit base_branch from config ----------

func TestAIPRCoreExplicitBaseBranch(t *testing.T) {
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{{Text: "result"}}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"merge-base HEAD develop":          {Stdout: "def5678\n"},
			"diff def5678..HEAD":               {Stdout: "some diff"},
			"log --oneline def5678..HEAD":       {Stdout: "def5678 chore: update"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIPRCore(context.Background(), aiPRDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		BaseCfg:  "develop", // explicit
		Remote:   "origin",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiPRFlags{output: "stdout"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should NOT call DefaultBranch detection.
	for _, c := range runner.Calls {
		if len(c.Args) > 0 && c.Args[0] == "symbolic-ref" {
			t.Error("should not probe DefaultBranch when BaseCfg is set")
		}
	}
}

// ---------- 8.5: provider without Summarizer ----------

type nonSummarizerProvider struct {
	provider.Fake
}

// Override to remove Summarizer — embed Fake but shadow Summarize.
func (n *nonSummarizerProvider) Summarize(_ context.Context, _ provider.SummarizeInput) (provider.SummarizeResult, error) {
	panic("should not be called")
}

func TestAIPRCoreProviderNotSummarizer(t *testing.T) {
	// Use a provider that only implements Provider, not Summarizer.
	prov := &providerOnly{name: "test-only"}

	runner := newPRFakeRunner("abc1234", "some diff", "abc1234 feat: stuff")
	out := &bytes.Buffer{}

	err := runAIPRCore(context.Background(), aiPRDeps{
		Runner:   runner,
		Provider: prov,
		Lang:     "en",
		BaseCfg:  "",
		Remote:   "origin",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiPRFlags{output: "stdout"})

	if err == nil {
		t.Fatal("expected error when provider doesn't implement Summarizer")
	}
	if !strings.Contains(err.Error(), "does not support Summarize") {
		t.Errorf("error should mention Summarize, got: %v", err)
	}
}

// providerOnly implements Provider but NOT Summarizer.
type providerOnly struct {
	name string
}

func (p *providerOnly) Name() string                { return p.name }
func (p *providerOnly) Locality() provider.Locality  { return provider.LocalityLocal }
func (p *providerOnly) Available(_ context.Context) error { return nil }
func (p *providerOnly) Classify(_ context.Context, _ provider.ClassifyInput) (provider.ClassifyResult, error) {
	return provider.ClassifyResult{}, nil
}
func (p *providerOnly) Compose(_ context.Context, _ provider.ComposeInput) (provider.ComposeResult, error) {
	return provider.ComposeResult{}, nil
}
