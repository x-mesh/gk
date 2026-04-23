package aicommit

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

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

	// Verify 2 `git add -- a.go` and 2 `git commit -m ... --` calls.
	addCalls, commitCalls := 0, 0
	for _, c := range fake.Calls {
		switch {
		case len(c.Args) >= 2 && c.Args[0] == "add" && c.Args[1] == "--":
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
			"add -- bad.go": {
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
