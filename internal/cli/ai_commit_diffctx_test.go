package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/git"
)

// uFlagOf reports whether a recorded `git diff` call carried -U<n>.
func uFlagOf(args []string) (string, bool) {
	for _, a := range args {
		if strings.HasPrefix(a, "-U") {
			return a, true
		}
	}
	return "", false
}

// TestCollectGroupDiffsUsesU1 proves lever #2: the compose diff is now
// emitted with -U1 (one context line) rather than git's default -U3.
// Every `git diff` collectGroupDiffs spawns must carry the -U1 flag.
func TestCollectGroupDiffsUsesU1(t *testing.T) {
	if composeDiffContextLines != 1 {
		t.Fatalf("composeDiffContextLines = %d, want 1", composeDiffContextLines)
	}

	runner := &git.FakeRunner{
		DefaultResp: git.FakeResponse{Stdout: "diff --git a/foo.go b/foo.go\n"},
	}
	groups := []provider.Group{
		{Type: "feat", Files: []string{"foo.go"}},
	}

	if _, err := collectGroupDiffs(context.Background(), runner, groups, wipCommitForAICommit{}); err != nil {
		t.Fatalf("collectGroupDiffs: %v", err)
	}

	diffCalls := 0
	for _, c := range runner.Calls {
		if len(c.Args) == 0 || c.Args[0] != "diff" {
			continue
		}
		diffCalls++
		got, ok := uFlagOf(c.Args)
		if !ok {
			t.Errorf("git diff call missing -U flag: %v", c.Args)
			continue
		}
		if got != "-U1" {
			t.Errorf("git diff call uses %s, want -U1: %v", got, c.Args)
		}
	}
	// Without a WIP commit: one --cached + one unstaged diff per group.
	if diffCalls != 2 {
		t.Fatalf("git diff calls = %d, want 2 (cached + unstaged)", diffCalls)
	}
}

// TestCollectGroupDiffsU1IncludesWIP proves the -U1 flag also lands on the
// WIP range diff, so the lower context applies to the whole compose payload.
func TestCollectGroupDiffsU1IncludesWIP(t *testing.T) {
	runner := &git.FakeRunner{
		DefaultResp: git.FakeResponse{Stdout: "diff --git a/foo.go b/foo.go\n"},
	}
	groups := []provider.Group{
		{Type: "feat", Files: []string{"foo.go"}},
	}
	wip := wipCommitForAICommit{Present: true, ChainLen: 2}

	if _, err := collectGroupDiffs(context.Background(), runner, groups, wip); err != nil {
		t.Fatalf("collectGroupDiffs: %v", err)
	}

	sawRange := false
	for _, c := range runner.Calls {
		if len(c.Args) == 0 || c.Args[0] != "diff" {
			continue
		}
		if _, ok := uFlagOf(c.Args); !ok {
			t.Errorf("git diff call missing -U flag: %v", c.Args)
		}
		for _, a := range c.Args {
			if strings.Contains(a, "HEAD~2..HEAD") {
				sawRange = true
			}
		}
	}
	if !sawRange {
		t.Fatal("expected a -U1 diff over the WIP range HEAD~2..HEAD")
	}
}

// TestCommitDisplayStatsKeepsOwnContext documents the lever #6 decision:
// commitDisplayStats deliberately does NOT share collectGroupDiffs's -U1
// diff. A lower context changes git's hunk-header funcname pick (measured:
// 18/30 real commits differ), which would shift the preview's symbol
// column. Preserving exact preview stats means the stat pass keeps git's
// default context — so its `git diff` calls carry NO -U flag.
//
// This guards against a future "dedup" that naively shares the -U1 payload
// and silently regresses the preview symbols.
func TestCommitDisplayStatsKeepsOwnContext(t *testing.T) {
	const fooDiff = "diff --git a/foo.go b/foo.go\n" +
		"index 111..222 100644\n" +
		"--- a/foo.go\n" +
		"+++ b/foo.go\n" +
		"@@ -1,3 +1,4 @@\n" +
		" package main\n" +
		"+// added\n" +
		" func main() {}\n"

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached -- foo.go": {Stdout: fooDiff},
		},
	}
	files := []aicommit.FileChange{{Path: "foo.go", Status: "M", Staged: true}}

	stats := commitDisplayStats(context.Background(), runner, files, wipCommitForAICommit{})
	if stats == nil {
		t.Fatal("commitDisplayStats returned nil")
	}
	fs, ok := stats["foo.go"]
	if !ok {
		t.Fatalf("no FileStat for foo.go; got keys %v", statKeys(stats))
	}
	if fs.Added != 1 || fs.Deleted != 0 {
		t.Errorf("FileStat added/deleted = %d/%d, want 1/0", fs.Added, fs.Deleted)
	}

	// The stat pass must run at git's default context: no -U flag on any
	// of its diff calls. (If a future change shares the -U1 payload this
	// assertion fails, flagging the symbol-semantics risk.)
	for _, c := range runner.Calls {
		if len(c.Args) == 0 || c.Args[0] != "diff" {
			continue
		}
		if flag, ok := uFlagOf(c.Args); ok {
			t.Errorf("commitDisplayStats diff carried %s; stat pass must keep default context: %v", flag, c.Args)
		}
	}
}

func statKeys(m map[string]aicommit.FileStat) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
