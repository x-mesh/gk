package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

// ---------- 9.1 / 9.5: command registration & flag parsing ----------

func TestAIReviewRegistered(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"ai", "review"})
	if err != nil {
		t.Fatalf("rootCmd.Find(ai review): %v", err)
	}
	if found.Use != "review" {
		t.Errorf("Use: want %q, got %q", "review", found.Use)
	}
}

func TestAIReviewHelpListsFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	found, _, err := rootCmd.Find([]string{"ai", "review"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	found.SetOut(buf)
	found.SetErr(buf)
	if err := found.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"--range", "--format", "--dry-run", "--provider"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing flag %q\n%s", want, out)
		}
	}
}

// ---------- 9.2 / 9.5: runAIReviewCore flow ----------

func TestAIReviewCoreStagedDiff(t *testing.T) {
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{
		{Text: "Looks good overall.\n", Model: "test-model", TokensUsed: 10},
	}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached": {Stdout: "diff --git a/foo.go\n+added line\n"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{format: "text"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "Looks good overall.") {
		t.Errorf("output should contain summarize result, got: %s", out.String())
	}
	if len(fake.Calls) == 0 || fake.Calls[len(fake.Calls)-1] != "Summarize" {
		t.Errorf("expected Summarize call, got calls: %v", fake.Calls)
	}
}

func TestAIReviewCoreRangeDiff(t *testing.T) {
	var captured provider.SummarizeInput
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{{Text: "review output"}}
	fake.OnSummarize = func(in provider.SummarizeInput) {
		captured = in
	}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff main..HEAD": {Stdout: "diff content here"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "ko",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{rangeRef: "main..HEAD", format: "text"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Kind != "review" {
		t.Errorf("Kind: want %q, got %q", "review", captured.Kind)
	}
	if captured.Lang != "ko" {
		t.Errorf("Lang: want %q, got %q", "ko", captured.Lang)
	}
	if captured.Diff != "diff content here" {
		t.Errorf("Diff mismatch: got %q", captured.Diff)
	}
}

// ---------- 9.3: output format ----------

func TestAIReviewCoreFormatJSON(t *testing.T) {
	fake := provider.NewFake()
	fake.SummarizeResponses = []provider.SummarizeResult{
		{Text: `{"files":[{"path":"foo.go","severity":"info"}]}`, Model: "m", TokensUsed: 5},
	}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached": {Stdout: "some diff"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{format: "json"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// For both text and json, we output the raw text from the provider.
	if !strings.Contains(out.String(), `"files"`) {
		t.Errorf("json output should contain provider result, got: %s", out.String())
	}
}

// ---------- 9.3: dry-run ----------

func TestAIReviewCoreDryRun(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached": {Stdout: "some diff"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{dryRun: true, format: "text"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result := out.String()
	if !strings.Contains(result, "dry-run") {
		t.Errorf("dry-run output should mention dry-run, got: %s", result)
	}
	if !strings.Contains(result, "Kind: review") {
		t.Errorf("dry-run output should show Kind, got: %s", result)
	}
	// Summarize should NOT be called in dry-run.
	for _, c := range fake.Calls {
		if c == "Summarize" {
			t.Error("Summarize should not be called in dry-run mode")
		}
	}
}

func TestAIReviewCoreDryRunWithRange(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff v1.0..v2.0": {Stdout: "range diff content"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{dryRun: true, rangeRef: "v1.0..v2.0"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result := out.String()
	if !strings.Contains(result, "v1.0..v2.0") {
		t.Errorf("dry-run should show range, got: %s", result)
	}
}

// ---------- 9.4: edge case — empty diff ----------

func TestAIReviewCoreEmptyDiff(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached": {Stdout: ""},
		},
	}
	out := &bytes.Buffer{}

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{format: "text"})

	if err != nil {
		t.Fatalf("expected nil error for empty diff, got: %v", err)
	}
	if !strings.Contains(out.String(), "no changes to review") {
		t.Errorf("should print no-changes message, got: %s", out.String())
	}
	// Summarize should NOT be called.
	for _, c := range fake.Calls {
		if c == "Summarize" {
			t.Error("Summarize should not be called when diff is empty")
		}
	}
}

func TestAIReviewCoreEmptyRangeDiff(t *testing.T) {
	fake := provider.NewFake()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff main..HEAD": {Stdout: "   \n"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{rangeRef: "main..HEAD", format: "text"})

	if err != nil {
		t.Fatalf("expected nil error for empty range diff, got: %v", err)
	}
	if !strings.Contains(out.String(), "no changes to review") {
		t.Errorf("should print no-changes message, got: %s", out.String())
	}
}

// ---------- 9.5: provider without Summarizer ----------

func TestAIReviewCoreProviderNotSummarizer(t *testing.T) {
	prov := &providerOnly{name: "test-only"}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached": {Stdout: "some diff"},
		},
	}
	out := &bytes.Buffer{}

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: prov,
		Lang:     "en",
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{format: "text"})

	if err == nil {
		t.Fatal("expected error when provider doesn't implement Summarizer")
	}
	if !strings.Contains(err.Error(), "does not support Summarize") {
		t.Errorf("error should mention Summarize, got: %v", err)
	}
}
