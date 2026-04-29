package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func TestAICommitRegistered(t *testing.T) {
	// rootCmd should resolve "commit" directly.
	found, _, err := rootCmd.Find([]string{"commit"})
	if err != nil {
		t.Fatalf("rootCmd.Find(commit): %v", err)
	}
	if found.Use != "commit" {
		t.Errorf("Use: want %q, got %q", "commit", found.Use)
	}
}

func TestAICommitHelpListsFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	found, _, err := rootCmd.Find([]string{"commit"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	found.SetOut(buf)
	found.SetErr(buf)
	if err := found.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"--force", "--dry-run", "--provider", "--lang",
		"--staged-only", "--include-unstaged", "--abort",
		"--allow-secret-kind", "--ci", "--yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing flag %q\n%s", want, out)
		}
	}
}

func TestReadAICommitFlagsMutualExclusion(t *testing.T) {
	found, _, _ := rootCmd.Find([]string{"commit"})
	_ = found.Flags().Set("staged-only", "true")
	_ = found.Flags().Set("include-unstaged", "true")
	_, err := readAICommitFlags(found)
	if err == nil {
		t.Error("want error when both --staged-only and --include-unstaged are set")
	}
	// Reset for other tests.
	_ = found.Flags().Set("staged-only", "false")
	_ = found.Flags().Set("include-unstaged", "false")
}

func TestNewRunIDIsHex(t *testing.T) {
	id := newRunID()
	if len(id) < 8 {
		t.Errorf("runID too short: %q", id)
	}
	// Either hex (16 chars) or time-based fallback starting with 't'.
	if id[0] != 't' {
		for _, r := range id {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				t.Errorf("non-hex rune in runID: %q", id)
				break
			}
		}
	}
}

func TestInspectWIPCommitForAICommitIncludesFiles(t *testing.T) {
	// Fake responses for the chain detector: one WIP at HEAD, then a
	// non-WIP commit at HEAD~1 to stop the walk. Files emitted in -z
	// (NUL-separated) form.
	const wipSHA = "wipsha11"
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --abbrev-ref HEAD":                     {Stdout: "improve\n"},
		"log -1 --format=%s HEAD~0":                       {Stdout: "--wip-- [skip ci]\n"},
		"rev-parse HEAD~0":                                {Stdout: wipSHA + "\n"},
		"log -1 --format=%P HEAD~0":                       {Stdout: wipSHA + "-parent\n"},
		"branch -r --contains " + wipSHA:                  {Stdout: ""},
		"diff -z --name-status " + wipSHA + "^ " + wipSHA: {Stdout: "M\x00internal/cli/merge.go\x00A\x00new.go\x00"},
		"log -1 --format=%s HEAD~1":                       {Stdout: "feat: real commit\n"},
		"rev-parse HEAD":                                  {Stdout: wipSHA + "\n"},
	}}

	cfg := config.AICommitConfig{WIPMaxChain: 5, WIPEnabled: true}
	wip, err := inspectWIPCommitForAICommit(context.Background(), runner, cfg, []string{"main"}, false)
	if err != nil {
		t.Fatalf("inspectWIPCommitForAICommit: %v", err)
	}
	if !wip.Present {
		t.Fatal("expected WIP commit")
	}
	if len(wip.Files) != 2 {
		t.Fatalf("expected 2 files, got %#v", wip.Files)
	}
	if wip.HeadSHA != wipSHA {
		t.Errorf("HeadSHA: want %q, got %q", wipSHA, wip.HeadSHA)
	}
	// Files end up sorted by path in MergeChainFiles.
	hasFoo, hasNew := false, false
	for _, f := range wip.Files {
		if f.Path == "internal/cli/merge.go" && f.Status == "modified" {
			hasFoo = true
		}
		if f.Path == "new.go" && f.Status == "added" {
			hasNew = true
		}
	}
	if !hasFoo || !hasNew {
		t.Fatalf("unexpected files: %#v", wip.Files)
	}
}

func TestAppendWIPCommitFilesDedupesCurrentFiles(t *testing.T) {
	files := appendWIPCommitFiles([]aicommit.FileChange{
		{Path: "current.go", Status: "modified"},
	}, []aicommit.FileChange{
		{Path: "current.go", Status: "modified"},
		{Path: "wip.go", Status: "added"},
	})
	if len(files) != 2 {
		t.Fatalf("expected deduped files, got %#v", files)
	}
	if files[1].Path != "wip.go" {
		t.Fatalf("expected WIP file appended, got %#v", files)
	}
}

func TestUnwrapWIPCommitBeforeApplySkipsDryRun(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"reset HEAD~1": {Stdout: ""},
	}}

	err := unwrapWIPCommitBeforeApply(context.Background(), runner, wipCommitForAICommit{Present: true}, aiCommitFlags{dryRun: true}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unwrapWIPCommitBeforeApply: %v", err)
	}
	calls := joinedShipCalls(runner.Calls)
	if strings.Contains(calls, "reset HEAD~1") {
		t.Fatalf("dry-run should not reset, calls:\n%s", calls)
	}
}

func TestUnwrapWIPCommitBeforeApplyResetsAfterPlan(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"reset HEAD~1": {Stdout: ""},
	}}
	var out bytes.Buffer

	err := unwrapWIPCommitBeforeApply(context.Background(), runner, wipCommitForAICommit{Present: true}, aiCommitFlags{}, &out)
	if err != nil {
		t.Fatalf("unwrapWIPCommitBeforeApply: %v", err)
	}
	calls := joinedShipCalls(runner.Calls)
	if !strings.Contains(calls, "reset HEAD~1") {
		t.Fatalf("expected WIP reset, calls:\n%s", calls)
	}
	if !strings.Contains(out.String(), "after AI plan") {
		t.Fatalf("expected after-plan output, got %q", out.String())
	}
}

// TestUnwrapWIPCommitRefusesWhenHEADMoved — the M4 fix.
// When the recorded HeadSHA differs from the current HEAD, the
// reset must be refused with a "HEAD moved" error.
func TestUnwrapWIPCommitRefusesWhenHEADMoved(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse HEAD": {Stdout: "now99999999\n"},
	}}
	var out bytes.Buffer
	wip := wipCommitForAICommit{
		Present:  true,
		ChainLen: 2,
		HeadSHA:  "was11111111",
	}
	err := unwrapWIPCommitBeforeApply(context.Background(), runner, wip, aiCommitFlags{}, &out)
	if err == nil {
		t.Fatal("expected refusal when HEAD moved")
	}
	if !strings.Contains(err.Error(), "HEAD moved") {
		t.Errorf("err: %v", err)
	}
	// And the reset should NOT have been issued.
	calls := joinedShipCalls(runner.Calls)
	if strings.Contains(calls, "reset HEAD~") {
		t.Errorf("must not reset after HEAD-moved detection; calls:\n%s", calls)
	}
}

// TestUnwrapWIPCommitProceedsWhenHEADUnchanged — companion to the
// M4 test. When recorded HeadSHA matches current HEAD, reset proceeds.
func TestUnwrapWIPCommitProceedsWhenHEADUnchanged(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse HEAD": {Stdout: "abc11111aaaa\n"},
		"reset HEAD~2":   {Stdout: ""},
	}}
	var out bytes.Buffer
	wip := wipCommitForAICommit{
		Present:  true,
		ChainLen: 2,
		HeadSHA:  "abc11111aaaa",
	}
	err := unwrapWIPCommitBeforeApply(context.Background(), runner, wip, aiCommitFlags{}, &out)
	if err != nil {
		t.Fatalf("expected success when HEAD matches: %v", err)
	}
	calls := joinedShipCalls(runner.Calls)
	if !strings.Contains(calls, "reset HEAD~2") {
		t.Errorf("expected reset HEAD~2, calls:\n%s", calls)
	}
}
