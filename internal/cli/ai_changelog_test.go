package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

// ---------- 10.1 / 10.5: command registration & flag parsing ----------

func TestAIChangelogRegistered(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"changelog"})
	if err != nil {
		t.Fatalf("rootCmd.Find(changelog): %v", err)
	}
	if found.Use != "changelog" {
		t.Errorf("Use: want %q, got %q", "changelog", found.Use)
	}
}

func TestAIChangelogHelpListsFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	found, _, err := rootCmd.Find([]string{"changelog"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	found.SetOut(buf)
	found.SetErr(buf)
	if err := found.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"--from", "--to", "--format", "--dry-run", "--provider"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing flag %q\n%s", want, out)
		}
	}
}

// ---------- 10.2 / 10.5: runAIChangelogCore flow ----------

func TestAIChangelogCoreHappyPath(t *testing.T) {
	var captured provider.SummarizeInput
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{
		{Text: "## Changelog\n- feat: add foo\n", Model: "test-model", TokensUsed: 20},
	}
	fake.OnSummarize = func(in provider.SummarizeInput) {
		captured = in
	}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"describe --tags --abbrev=0": {Stdout: "v1.0.0\n"},
			"log --oneline v1.0.0..HEAD": {Stdout: "abc1234 feat: add foo\ndef5678 fix: bar"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{format: "markdown"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "## Changelog") {
		t.Errorf("output should contain summarize result, got: %s", out.String())
	}
	if captured.Kind != "changelog" {
		t.Errorf("Kind: want %q, got %q", "changelog", captured.Kind)
	}
	if captured.Lang != "en" {
		t.Errorf("Lang: want %q, got %q", "en", captured.Lang)
	}
	if len(captured.Commits) != 2 {
		t.Errorf("Commits: want 2, got %d", len(captured.Commits))
	}
}

func TestAIChangelogCoreExplicitFromTo(t *testing.T) {
	var captured provider.SummarizeInput
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{{Text: "changelog output"}}
	fake.OnSummarize = func(in provider.SummarizeInput) {
		captured = in
	}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"log --oneline v0.9.0..v1.0.0": {Stdout: "abc1234 feat: first\ndef5678 fix: second\nghi9012 chore: third"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "ko",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{from: "v0.9.0", to: "v1.0.0", format: "markdown"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Kind != "changelog" {
		t.Errorf("Kind: want %q, got %q", "changelog", captured.Kind)
	}
	if captured.Lang != "ko" {
		t.Errorf("Lang: want %q, got %q", "ko", captured.Lang)
	}
	if len(captured.Commits) != 3 {
		t.Errorf("Commits: want 3, got %d", len(captured.Commits))
	}
	// Should NOT call git describe when --from is explicit.
	for _, c := range runner.Calls {
		if len(c.Args) > 0 && c.Args[0] == "describe" {
			t.Error("should not call git describe when --from is explicit")
		}
	}
}

// ---------- 10.3: output format ----------

func TestAIChangelogCoreFormatJSON(t *testing.T) {
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{
		{Text: `{"features":["add foo"],"fixes":["fix bar"]}`, Model: "m", TokensUsed: 5},
	}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"describe --tags --abbrev=0": {Stdout: "v1.0.0\n"},
			"log --oneline v1.0.0..HEAD": {Stdout: "abc1234 feat: add foo"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{format: "json"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), `"features"`) {
		t.Errorf("json output should contain provider result, got: %s", out.String())
	}
}

// ---------- 10.3: dry-run ----------

func TestAIChangelogCoreDryRun(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"describe --tags --abbrev=0": {Stdout: "v1.0.0\n"},
			"log --oneline v1.0.0..HEAD": {Stdout: "abc1234 feat: stuff\ndef5678 fix: thing"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{dryRun: true, format: "markdown"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result := out.String()
	if !strings.Contains(result, "dry-run") {
		t.Errorf("dry-run output should mention dry-run, got: %s", result)
	}
	if !strings.Contains(result, "Kind: changelog") {
		t.Errorf("dry-run output should show Kind, got: %s", result)
	}
	if !strings.Contains(result, "v1.0.0..HEAD") {
		t.Errorf("dry-run output should show range, got: %s", result)
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

func TestAIChangelogCoreDryRunExplicitRange(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"log --oneline v0.9.0..v1.0.0": {Stdout: "abc1234 feat: stuff"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{dryRun: true, from: "v0.9.0", to: "v1.0.0"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result := out.String()
	if !strings.Contains(result, "v0.9.0..v1.0.0") {
		t.Errorf("dry-run should show explicit range, got: %s", result)
	}
}

// ---------- 10.4: edge case — empty commit range ----------

func TestAIChangelogCoreEmptyCommitRange(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"describe --tags --abbrev=0": {Stdout: "v1.0.0\n"},
			"log --oneline v1.0.0..HEAD": {Stdout: ""},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{format: "markdown"})

	if err != nil {
		t.Fatalf("expected nil error for empty range, got: %v", err)
	}
	if !strings.Contains(out.String(), "no commits in range") {
		t.Errorf("should print no-commits message, got: %s", out.String())
	}
	// Summarize should NOT be called.
	for _, c := range fake.Calls {
		if c == "Summarize" {
			t.Error("Summarize should not be called when no commits")
		}
	}
}

func TestAIChangelogCoreEmptyCommitRangeWhitespace(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"log --oneline v1.0.0..HEAD": {Stdout: "   \n"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{from: "v1.0.0", format: "markdown"})

	if err != nil {
		t.Fatalf("expected nil error for whitespace-only range, got: %v", err)
	}
	if !strings.Contains(out.String(), "no commits in range") {
		t.Errorf("should print no-commits message, got: %s", out.String())
	}
}

// ---------- 10.4: edge case — no tags found ----------

func TestAIChangelogCoreNoTags(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"describe --tags --abbrev=0": {Err: fmt.Errorf("fatal: no tags")},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{format: "markdown"})

	if err == nil {
		t.Fatal("expected error when no tags found")
	}
	if !strings.Contains(err.Error(), "no tags found") {
		t.Errorf("error should mention no tags, got: %v", err)
	}
}

// ---------- 10.5: provider without Summarizer ----------

func TestAIChangelogCoreProviderNotSummarizer(t *testing.T) {
	prov := &providerOnly{name: "test-only"}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"describe --tags --abbrev=0": {Stdout: "v1.0.0\n"},
			"log --oneline v1.0.0..HEAD": {Stdout: "abc1234 feat: stuff"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: prov,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{format: "markdown"})

	if err == nil {
		t.Fatal("expected error when provider doesn't implement Summarizer")
	}
	if !strings.Contains(err.Error(), "does not support Summarize") {
		t.Errorf("error should mention Summarize, got: %v", err)
	}
}

// ---------- 10.5: default lang fallback ----------

func TestAIChangelogCoreDefaultLang(t *testing.T) {
	var captured provider.SummarizeInput
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{{Text: "ok"}}
	fake.OnSummarize = func(in provider.SummarizeInput) {
		captured = in
	}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"describe --tags --abbrev=0": {Stdout: "v1.0.0\n"},
			"log --oneline v1.0.0..HEAD": {Stdout: "abc1234 feat: stuff"},
		},
	}
	out := &bytes.Buffer{}

	_ = runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "", // empty → should fallback to "en"
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{format: "markdown"})

	if captured.Lang != "en" {
		t.Errorf("Lang: want %q (fallback), got %q", "en", captured.Lang)
	}
}
