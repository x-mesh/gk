package aicommit

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// End-to-end tests that wire multiple aicommit pieces together with
// FakeRunner / FakeProvider. These sit at the boundary between the
// package's unit tests (per-file) and the CLI's integration tests
// (full process). They exist so refactors inside any single file
// can't break the expected choreography.

func TestIntegrationGatherClassifyComposeApplyHappyPath(t *testing.T) {
	// WIP: two files in the same top-level dir → heuristic classifier.
	stdout := strings.Join([]string{
		"1 .M N... 100644 100644 100644 aaa bbb internal/aicommit/gather.go",
		"1 .M N... 100644 100644 100644 aaa bbb internal/aicommit/apply.go",
	}, "\x00") + "\x00"
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2 --untracked-files=all -z": {Stdout: stdout},
			"symbolic-ref --quiet --short HEAD":              {Stdout: "ai-commit\n"},
			"rev-parse HEAD":                                 {Stdout: "deadbeef\n"},
			"write-tree":                                     {Stdout: "tree-oid\n"},
		},
		DefaultResp: git.FakeResponse{Stdout: "[ai-commit 1111111] ok\n"},
	}

	files, err := GatherWIP(context.Background(), fake, GatherOptions{})
	if err != nil {
		t.Fatalf("GatherWIP: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files: %+v", files)
	}

	p := provider.NewFake()
	// Heuristic path: homogeneous (same top-dir, <=5 files) — LLM not called.
	groups, err := Classify(context.Background(), p, files, ClassifyOptions{
		AllowedTypes:    []string{"feat", "chore"},
		HybridFileLimit: 5,
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups: %+v", groups)
	}

	p.ComposeResponses = []provider.ComposeResult{
		{Subject: "wire classifier and apply", Body: "heuristic hits internal/aicommit/"},
	}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat", "chore"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs: %+v", msgs)
	}

	res, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	if len(res.CommitShas) != 1 {
		t.Errorf("CommitShas: %+v", res.CommitShas)
	}
	if !strings.HasPrefix(res.BackupRef, "refs/gk/ai-commit-backup/") {
		t.Errorf("BackupRef: %q", res.BackupRef)
	}
}

func TestIntegrationSecretGateBlocksProvider(t *testing.T) {
	// Payload includes a fake AWS access key — the gate must refuse to
	// proceed, and we never reach Classify.
	payload := "key: AKIAIOSFODNN7EXAMPLE"
	findings, err := ScanPayload(context.Background(), payload, SecretGateOptions{}, fakeGitleaks{})
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected built-in scanner to catch AKIA key")
	}
	if findings[0].Kind != "aws-access-key" {
		t.Errorf("finding: %+v", findings[0])
	}
}

func TestIntegrationAbortRestoresHEADFromBackupRef(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "babecafe\n"},
		},
	}
	// Create the backup ref.
	ref, err := EnsureBackupRef(context.Background(), fake)
	if err != nil {
		t.Fatalf("EnsureBackupRef: %v", err)
	}
	if ref == "" {
		t.Fatal("backup ref should be non-empty for normal HEAD")
	}

	// Reset the runner and invoke AbortRestore — it should execute
	// `git reset --hard <ref>`.
	fake2 := &git.FakeRunner{}
	if err := AbortRestore(context.Background(), fake2, ref); err != nil {
		t.Fatalf("AbortRestore: %v", err)
	}
	if len(fake2.Calls) != 1 {
		t.Fatalf("calls: %+v", fake2.Calls)
	}
	if fake2.Calls[0].Args[0] != "reset" || fake2.Calls[0].Args[1] != "--hard" {
		t.Errorf("abort did not reset --hard: %+v", fake2.Calls[0].Args)
	}
}

func TestIntegrationPreflightBlocksDuringRebase(t *testing.T) {
	// Create a temporary "git dir" with a rebase-merge marker so
	// gitstate.Detect reports StateRebaseMerge.
	tmp := t.TempDir()
	gitDir := tmp + "/.git"
	rebaseMerge := gitDir + "/rebase-merge"
	if err := os.MkdirAll(rebaseMerge, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	state, err := gitstate.DetectFromGitDir(gitDir)
	if err != nil {
		t.Fatalf("DetectFromGitDir: %v", err)
	}
	if state == nil || state.Kind == gitstate.StateNone {
		t.Fatalf("expected rebase-merge state, got %+v", state)
	}
	// gitstate says "rebase in progress" — Preflight wired into CLI
	// translates that into ErrGitStateNotNone. We assert the gitstate
	// shape directly rather than re-running Preflight here because
	// Preflight calls Detect with a workDir not a gitDir.
	if !strings.Contains(state.Kind.String(), "rebase") {
		t.Errorf("state kind: %q", state.Kind.String())
	}
}

