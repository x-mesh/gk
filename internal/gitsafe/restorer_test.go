package gitsafe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestStrategy_String(t *testing.T) {
	tests := []struct {
		s    Strategy
		want string
	}{
		{StrategyMixed, "mixed"},
		{StrategyHard, "hard"},
		{StrategySoft, "soft"},
		{StrategyKeep, "keep"},
	}
	for _, tc := range tests {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("Strategy(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestRestorer_Backup_CreatesRef(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("init")

	runner := &git.ExecRunner{Dir: repo.Dir}
	at := time.Unix(1700000000, 0)
	r := NewRestorer(runner, func() time.Time { return at }, "undo")

	ref, err := r.Backup(context.Background(), "main")
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	want := "refs/gk/undo-backup/main/1700000000"
	if ref != want {
		t.Errorf("Backup() ref = %q, want %q", ref, want)
	}
	// Ref must point to current HEAD SHA.
	gotSHA := repo.RunGit("rev-parse", ref)
	headSHA := repo.RunGit("rev-parse", "HEAD")
	if gotSHA != headSHA {
		t.Errorf("backup ref sha = %q, want HEAD sha %q", gotSHA, headSHA)
	}
}

func TestRestorer_Restore_Mixed_MovesHEADAndKeepsBackup(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	sha1 := repo.Commit("commit 1")
	repo.WriteFile("b.txt", "v2")
	repo.Commit("commit 2")

	runner := &git.ExecRunner{Dir: repo.Dir}
	r := NewRestorer(runner, func() time.Time { return time.Unix(1700000000, 0) }, "undo")

	res, err := r.Restore(context.Background(), "main",
		Target{SHA: sha1}, StrategyMixed)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// HEAD moved.
	if head := repo.RunGit("rev-parse", "HEAD"); head != sha1 {
		t.Errorf("HEAD = %q, want %q", head, sha1)
	}
	// Backup ref reachable and points at the pre-restore tip (not sha1).
	if got := repo.RunGit("rev-parse", res.BackupRef); got == sha1 {
		t.Errorf("backup ref points at restore target, not pre-restore tip")
	}
	if !strings.HasPrefix(res.BackupRef, "refs/gk/undo-backup/main/") {
		t.Errorf("backup ref %q has unexpected prefix", res.BackupRef)
	}
	if res.To != sha1 {
		t.Errorf("Result.To = %q, want %q", res.To, sha1)
	}
	if res.From == "" || res.From == sha1 {
		t.Errorf("Result.From = %q, expected pre-restore HEAD", res.From)
	}
	if res.Strategy != StrategyMixed {
		t.Errorf("Result.Strategy = %v, want Mixed", res.Strategy)
	}
}

func TestRestorer_Restore_Hard_DiscardsWorktree(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	sha1 := repo.Commit("commit 1")
	repo.WriteFile("b.txt", "v2")
	repo.Commit("commit 2")

	// Dirty the working tree — hard reset should wipe it.
	repo.WriteFile("a.txt", "dirty")

	runner := &git.ExecRunner{Dir: repo.Dir}
	r := NewRestorer(runner, nil, "wipe")

	_, err := r.Restore(context.Background(), "main",
		Target{SHA: sha1}, StrategyHard)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Worktree contents restored to sha1 state (a.txt == "v1", b.txt gone).
	data, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if strings.TrimSpace(string(data)) != "v1" {
		t.Errorf("a.txt after hard reset = %q, want %q", data, "v1")
	}
}

func TestResolveRef(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	sha := repo.Commit("init")

	got, err := ResolveRef(context.Background(), &git.ExecRunner{Dir: repo.Dir}, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got != sha {
		t.Errorf("ResolveRef(HEAD) = %q, want %q", got, sha)
	}

	if _, err := ResolveRef(context.Background(), &git.ExecRunner{Dir: repo.Dir}, "does-not-exist"); err == nil {
		t.Errorf("ResolveRef(does-not-exist) = nil, want error")
	}
}

// --- TM-18: autostash + dirty-ordering + RestoreError tests ---------------

// fixedNow returns a deterministic timestamp so backup ref names are
// predictable across test runs.
func fixedNow() time.Time { return time.Unix(1700000000, 0) }

// TestRestore_DirtyHardAutostash_CallOrder — full 6-step ordering contract.
// Asserts exact argv sequence so any future reorder breaks this test loudly.
func TestRestore_DirtyHardAutostash_CallOrder(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify HEAD^{commit}": {Stdout: "old-head-sha\n"},
		},
		DefaultResp: git.FakeResponse{}, // all other calls succeed
	}

	r := NewRestorer(fake, fixedNow, "undo")
	res, err := r.Restore(context.Background(), "main",
		Target{SHA: "target-sha"}, StrategyHard, WithAutostash(true))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	want := [][]string{
		{"rev-parse", "--verify", "HEAD^{commit}"},                                     // step 1: snapshot
		{"update-ref", "refs/gk/undo-backup/main/1700000000", "HEAD"},                  // step 2: backup
		{"stash", "push", "--include-untracked", "-m", "gk-undo-autostash-1700000000"}, // step 3: autostash
		{"reset", "--hard", "target-sha"},                                              // step 4: reset
		{"stash", "pop", "--index"},                                                    // step 5: pop
		{"rev-parse", "--verify", "HEAD^{commit}"},                                     // step 6: verify
	}
	if len(fake.Calls) != len(want) {
		t.Fatalf("expected %d calls, got %d: %v", len(want), len(fake.Calls), fake.Calls)
	}
	for i, w := range want {
		if !equalArgs(fake.Calls[i].Args, w) {
			t.Errorf("call[%d]: got %v, want %v", i, fake.Calls[i].Args, w)
		}
	}
	if res.BackupRef != "refs/gk/undo-backup/main/1700000000" {
		t.Errorf("Result.BackupRef = %q", res.BackupRef)
	}
	if res.From != "old-head-sha" {
		t.Errorf("Result.From = %q, want old-head-sha", res.From)
	}
}

// TestRestore_BackupFails_NoReset — step 2 failure must abort before reset.
func TestRestore_BackupFails_NoReset(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify HEAD^{commit}":                    {Stdout: "old-head-sha\n"},
			"update-ref refs/gk/undo-backup/main/1700000000 HEAD": {ExitCode: 1, Stderr: "fatal: refusing to update ref"},
		},
	}

	r := NewRestorer(fake, fixedNow, "undo")
	_, err := r.Restore(context.Background(), "main",
		Target{SHA: "target-sha"}, StrategyMixed)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var rerr *RestoreError
	if !errors.As(err, &rerr) {
		t.Fatalf("error is not *RestoreError: %v", err)
	}
	if rerr.Stage != StageBackup {
		t.Errorf("Stage = %q, want backup", rerr.Stage)
	}

	// Reset must NOT have been invoked.
	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "reset" {
			t.Errorf("reset was called despite backup failure: %v", c.Args)
		}
	}
}

// TestRestore_AutostashFail_RollsBackBackup — step 3 failure must delete
// the backup ref and leave worktree untouched.
func TestRestore_AutostashFail_RollsBackBackup(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify HEAD^{commit}": {Stdout: "old-head-sha\n"},
			// Autostash fails.
			"stash push --include-untracked -m gk-undo-autostash-1700000000": {
				ExitCode: 1, Stderr: "no changes to stash",
			},
		},
	}

	r := NewRestorer(fake, fixedNow, "undo")
	res, err := r.Restore(context.Background(), "main",
		Target{SHA: "target-sha"}, StrategyHard, WithAutostash(true))

	var rerr *RestoreError
	if err == nil || !errors.As(err, &rerr) {
		t.Fatalf("expected *RestoreError, got %v", err)
	}
	if rerr.Stage != StageAutostash {
		t.Errorf("Stage = %q, want autostash", rerr.Stage)
	}

	// Backup ref must have been rolled back (Result.BackupRef cleared).
	if res.BackupRef != "" {
		t.Errorf("Result.BackupRef = %q, want empty after rollback", res.BackupRef)
	}

	// Calls must include update-ref -d for rollback.
	sawRollback := false
	for _, c := range fake.Calls {
		if len(c.Args) >= 2 && c.Args[0] == "update-ref" && c.Args[1] == "-d" {
			sawRollback = true
		}
		if len(c.Args) > 0 && c.Args[0] == "reset" {
			t.Errorf("reset was called despite autostash failure: %v", c.Args)
		}
	}
	if !sawRollback {
		t.Errorf("expected update-ref -d rollback call; got %v", fake.Calls)
	}
}

// TestRestore_ResetFails_RecoveryHint — step 4 failure returns Stage=reset
// with a recovery hint pointing at the backup ref.
func TestRestore_ResetFails_RecoveryHint(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify HEAD^{commit}": {Stdout: "old-head-sha\n"},
			"reset --mixed target-sha":         {ExitCode: 128, Stderr: "fatal: Could not parse object"},
		},
	}

	r := NewRestorer(fake, fixedNow, "undo")
	_, err := r.Restore(context.Background(), "main",
		Target{SHA: "target-sha"}, StrategyMixed)

	var rerr *RestoreError
	if err == nil || !errors.As(err, &rerr) {
		t.Fatalf("expected *RestoreError, got %v", err)
	}
	if rerr.Stage != StageReset {
		t.Errorf("Stage = %q, want reset", rerr.Stage)
	}
	wantRecovery := "git reset --hard refs/gk/undo-backup/main/1700000000"
	if rerr.Recovery != wantRecovery {
		t.Errorf("Recovery = %q, want %q", rerr.Recovery, wantRecovery)
	}
}

// TestRestore_PopFails_SetsAutostashRef — step 5 failure is non-fatal.
func TestRestore_PopFails_SetsAutostashRef(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify HEAD^{commit}": {Stdout: "old-head-sha\n"},
			"stash pop --index":                {ExitCode: 1, Stderr: "CONFLICT"},
		},
	}

	r := NewRestorer(fake, fixedNow, "undo")
	res, err := r.Restore(context.Background(), "main",
		Target{SHA: "target-sha"}, StrategyMixed, WithAutostash(true))
	if err != nil {
		t.Fatalf("pop failure should not be fatal; got err=%v", err)
	}
	if res.AutostashRef == "" {
		t.Errorf("Result.AutostashRef = empty, want stash@{0} marker")
	}
}

// --- test helpers ---------------------------------------------------------

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
