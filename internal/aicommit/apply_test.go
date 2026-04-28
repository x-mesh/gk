package aicommit

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestApplyMessagesCreatesCommitPerGroup(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "ai-commit\n"},
			"rev-parse HEAD":                    {Stdout: "abcdef\n"},
			"write-tree":                        {Stdout: "tree123\n"},
		},
		DefaultResp: git.FakeResponse{Stdout: "[ai-commit 1111111] feat: subject\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"a.go"}}, Subject: "add a"},
		{Group: provider.Group{Type: "test", Files: []string{"a_test.go"}}, Subject: "cover a"},
	}
	res, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	if len(res.CommitShas) != 2 {
		t.Fatalf("CommitShas: %+v", res.CommitShas)
	}
	if res.BackupRef == "" || !strings.HasPrefix(res.BackupRef, "refs/gk/ai-commit-backup/") {
		t.Errorf("BackupRef malformed: %q", res.BackupRef)
	}
	if res.TreeBefore != "tree123" {
		t.Errorf("TreeBefore: %q", res.TreeBefore)
	}

	// Verify 2 `git add -A -- a.go` and 2 `git commit -m ... --` calls.
	addCalls, commitCalls := 0, 0
	for _, c := range fake.Calls {
		switch {
		case len(c.Args) >= 3 && c.Args[0] == "add" && c.Args[1] == "-A" && c.Args[2] == "--":
			addCalls++
		case len(c.Args) >= 1 && c.Args[0] == "commit":
			commitCalls++
		}
	}
	if addCalls != 2 || commitCalls != 2 {
		t.Errorf("add=%d commit=%d (want 2,2), calls=%+v", addCalls, commitCalls, fake.Calls)
	}
}

func TestApplyMessagesDryRunMakesNoCommits(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
		},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"a.go"}}, Subject: "add a"},
	}
	res, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "commit" {
			t.Error("DryRun must not invoke git commit")
		}
	}
	if len(res.CommitShas) != 1 || res.CommitShas[0] != "" {
		t.Errorf("CommitShas in dry-run: %+v", res.CommitShas)
	}
}

func TestApplyMessagesAddFailureAborts(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"add -A -- bad.go": {
				Stderr:   "fatal: pathspec 'bad.go' did not match any files",
				ExitCode: 128,
			},
		},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"bad.go"}}, Subject: "x"},
	}
	_, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{})
	if err == nil {
		t.Fatal("want error when git add fails")
	}
	if !strings.Contains(err.Error(), "pathspec") {
		t.Errorf("error should include stderr: %v", err)
	}
}

// TestApplyMessagesStagesUnstagedDeletion covers the path where the
// user removed a tracked file from disk but hadn't run `git rm` — the
// index still has the entry, working tree doesn't (porcelain " D"). On
// these `git add -A -- <path>` matches the index entry and stages the
// deletion. The earlier bug (pre-`-A`) silently lost the deletion; this
// guards the fix.
func TestApplyMessagesStagesUnstagedDeletion(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD":                          {Stdout: "main\n"},
			"rev-parse HEAD":                                             {Stdout: "abc\n"},
			"write-tree":                                                 {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 2222222] chore: drop\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "chore", Files: []string{"gone.go"}}, Subject: "drop unused"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	var saw bool
	for _, c := range fake.Calls {
		if len(c.Args) >= 4 && c.Args[0] == "add" && c.Args[1] == "-A" && c.Args[2] == "--" && c.Args[3] == "gone.go" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected `git add -A -- gone.go`, got calls=%+v", fake.Calls)
	}
}

// TestApplyMessagesSkipsAddForFullyStagedDeletion guards the
// 2026-04-29 regression: when a tracked file is already fully staged
// for deletion (porcelain "D " — gone from working tree AND from
// index), `git add -A -- <path>` fails with "pathspec did not match
// any files". The fix excludes such paths from the add invocation;
// `git commit -- <path>` still picks up the deletion via HEAD diff.
func TestApplyMessagesSkipsAddForFullyStagedDeletion(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {
				Stdout: "removed.go\x00",
			},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 3333333] chore: drop\n"},
	}
	// Group mixes a fully-staged-deletion path with a normal one. Only
	// the normal path should reach `git add`; both reach `git commit`.
	msgs := []Message{
		{
			Group:   provider.Group{Type: "chore", Files: []string{"removed.go", "kept.go"}},
			Subject: "tidy",
		},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	var addArgs, commitArgs []string
	for _, c := range fake.Calls {
		if len(c.Args) >= 1 && c.Args[0] == "add" {
			addArgs = c.Args
		}
		if len(c.Args) >= 1 && c.Args[0] == "commit" {
			commitArgs = c.Args
		}
	}
	// add must be called with kept.go only — never removed.go.
	want := []string{"add", "-A", "--", "kept.go"}
	if !reflect.DeepEqual(addArgs, want) {
		t.Errorf("add args = %v, want %v", addArgs, want)
	}
	// commit still references both paths.
	if !contains(commitArgs, "removed.go") || !contains(commitArgs, "kept.go") {
		t.Errorf("commit args missing one of the files: %v", commitArgs)
	}
}

// TestApplyMessagesSkipsAddEntirelyWhenAllStagedDeleted exercises the
// edge case where every file in a group is already fully staged for
// deletion — `git add` should not be invoked at all (zero pathspecs
// would otherwise stage every dirty file in the repo).
func TestApplyMessagesSkipsAddEntirelyWhenAllStagedDeleted(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {
				Stdout: "a\x00b\x00",
			},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 4444444] chore: drop\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "chore", Files: []string{"a", "b"}}, Subject: "drop"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	for _, c := range fake.Calls {
		if len(c.Args) >= 1 && c.Args[0] == "add" {
			t.Errorf("git add must not be invoked when all files are fully staged-deleted, got %v", c.Args)
		}
	}
}

func TestAbortRestoreEmptyBackupIsNoop(t *testing.T) {
	fake := &git.FakeRunner{}
	if err := AbortRestore(context.Background(), fake, ""); err != nil {
		t.Errorf("empty backup should be no-op, got %v", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("expected no git calls, got %+v", fake.Calls)
	}
}

func TestAbortRestoreRunsHardReset(t *testing.T) {
	fake := &git.FakeRunner{}
	if err := AbortRestore(context.Background(), fake, "refs/gk/ai-commit-backup/main/123"); err != nil {
		t.Errorf("AbortRestore: %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("want 1 git call, got %+v", fake.Calls)
	}
	args := fake.Calls[0].Args
	want := []string{"reset", "--hard", "refs/gk/ai-commit-backup/main/123"}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("args[%d]: want %q, got %q", i, w, args[i])
		}
	}
}

func TestFormatCommitMessageJoinsCleanly(t *testing.T) {
	m := Message{
		Group:   provider.Group{Type: "feat", Scope: "ai"},
		Subject: "add provider factory",
		Body:    "Autodetects gemini, qwen, kiro.",
		Footers: []provider.Footer{{Token: "Refs", Value: "#42"}},
	}
	got := formatCommitMessage(m, "gemini@0.38.2")
	wantHeader := "feat(ai): add provider factory"
	wantBody := "Autodetects gemini, qwen, kiro."
	wantFoot := "Refs: #42"
	wantTrailer := "AI-Assisted-By: gemini@0.38.2"
	for _, want := range []string{wantHeader, wantBody, wantFoot, wantTrailer} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted message missing %q:\n%s", want, got)
		}
	}
	// Trailer is last.
	if idx := strings.Index(got, wantTrailer); idx == -1 || idx+len(wantTrailer) != len(got) {
		t.Errorf("trailer should be at end, got:\n%s", got)
	}
}

// TestFormatCommitMessage_StripsDuplicatedPrefix guards against the
// "build: build: ..." style header that surfaces when the LLM tucks the
// Conventional Commits header onto Subject and the formatter prepends
// it again.
func TestFormatCommitMessage_StripsDuplicatedPrefix(t *testing.T) {
	cases := []struct {
		name    string
		group   provider.Group
		subject string
		want    string
	}{
		{
			name:    "type only",
			group:   provider.Group{Type: "build"},
			subject: "build: embed branch and worktree name",
			want:    "build: embed branch and worktree name",
		},
		{
			name:    "type with scope",
			group:   provider.Group{Type: "refactor", Scope: "internal"},
			subject: "refactor(internal): improve dirty-tree UX",
			want:    "refactor(internal): improve dirty-tree UX",
		},
		{
			name:    "scope mismatch still strips",
			group:   provider.Group{Type: "feat", Scope: "ai"},
			subject: "feat(internal): add provider factory",
			want:    "feat(ai): add provider factory",
		},
		{
			name:    "case-insensitive type",
			group:   provider.Group{Type: "fix"},
			subject: "Fix: handle nil pointer",
			want:    "fix: handle nil pointer",
		},
		{
			name:    "no prefix to strip",
			group:   provider.Group{Type: "feat"},
			subject: "add provider factory",
			want:    "feat: add provider factory",
		},
		{
			name:    "subject is *only* prefix — keep as-is",
			group:   provider.Group{Type: "feat"},
			subject: "feat:",
			want:    "feat: feat:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCommitMessage(Message{Group: tc.group, Subject: tc.subject}, "")
			// Compare just the header (first line).
			line := strings.SplitN(got, "\n", 2)[0]
			if line != tc.want {
				t.Errorf("header = %q, want %q", line, tc.want)
			}
		})
	}
}
