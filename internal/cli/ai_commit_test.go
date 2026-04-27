package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/aicommit"
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
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"log -1 --format=%s":              {Stdout: "--wip-- [skip ci]\n"},
		"diff --name-status HEAD~1..HEAD": {Stdout: "M\tinternal/cli/merge.go\nA\tnew.go\n"},
	}}

	wip, err := inspectWIPCommitForAICommit(context.Background(), runner)
	if err != nil {
		t.Fatalf("inspectWIPCommitForAICommit: %v", err)
	}
	if !wip.Present {
		t.Fatal("expected WIP commit")
	}
	if len(wip.Files) != 2 {
		t.Fatalf("expected 2 files, got %#v", wip.Files)
	}
	if wip.Files[0].Path != "internal/cli/merge.go" || wip.Files[0].Status != "modified" {
		t.Fatalf("unexpected first file: %#v", wip.Files[0])
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
