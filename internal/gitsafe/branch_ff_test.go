package gitsafe

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// fixedTime returns a stable clock so the backup ref name is deterministic.
func fixedTime() func() time.Time {
	at := time.Unix(1700000000, 0)
	return func() time.Time { return at }
}

func TestBranchFF_FastForwardsWithBackup(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	sha1 := repo.Commit("c1")
	repo.RunGit("branch", "feature") // feature pinned at sha1
	repo.WriteFile("b.txt", "v2")
	sha2 := repo.Commit("c2") // main advances to sha2; feature behind

	runner := &git.ExecRunner{Dir: repo.Dir}
	res, err := BranchFF(context.Background(), runner, fixedTime(), "ship", "feature", sha2)
	if err != nil {
		t.Fatalf("BranchFF: %v", err)
	}
	if res.Outcome != FFOk {
		t.Fatalf("Outcome = %v, want FFOk (reason: %s)", res.Outcome, res.Reason)
	}
	if res.OldSHA != sha1 || res.NewSHA != sha2 {
		t.Errorf("Old/New = %s/%s, want %s/%s", res.OldSHA, res.NewSHA, sha1, sha2)
	}
	wantBackup := "refs/gk/ship-backup/feature/1700000000"
	if res.BackupRef != wantBackup {
		t.Errorf("BackupRef = %q, want %q", res.BackupRef, wantBackup)
	}
	// feature was actually moved to sha2 ...
	if got := repo.RunGit("rev-parse", "refs/heads/feature"); got != sha2 {
		t.Errorf("feature tip = %s, want %s", got, sha2)
	}
	// ... and the backup ref preserves the pre-move tip (recoverability).
	if got := repo.RunGit("rev-parse", wantBackup); got != sha1 {
		t.Errorf("backup ref tip = %s, want %s (pre-move sha)", got, sha1)
	}
}

func TestBranchFF_UpToDateNoBackup(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	sha1 := repo.Commit("c1")
	repo.RunGit("branch", "feature")

	runner := &git.ExecRunner{Dir: repo.Dir}
	res, err := BranchFF(context.Background(), runner, fixedTime(), "ship", "feature", sha1)
	if err != nil {
		t.Fatalf("BranchFF: %v", err)
	}
	if res.Outcome != FFUpToDate {
		t.Fatalf("Outcome = %v, want FFUpToDate", res.Outcome)
	}
	if res.BackupRef != "" {
		t.Errorf("BackupRef = %q, want empty (nothing moved)", res.BackupRef)
	}
}

func TestBranchFF_BlockedWhenNotFastForward(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "v2")
	sha2 := repo.Commit("c2")
	repo.RunGit("branch", "feature") // feature at sha2 (ahead of sha1)

	runner := &git.ExecRunner{Dir: repo.Dir}
	// Asking to move feature (sha2) "back" to sha1 is not a fast-forward.
	res, err := BranchFF(context.Background(), runner, fixedTime(), "ship", "feature", sha1)
	if err != nil {
		t.Fatalf("BranchFF returned error, want blocked result: %v", err)
	}
	if res.Outcome != FFBlocked {
		t.Fatalf("Outcome = %v, want FFBlocked", res.Outcome)
	}
	if res.Reason == "" {
		t.Errorf("FFBlocked must carry a Reason")
	}
	// feature ref must be untouched.
	if got := repo.RunGit("rev-parse", "refs/heads/feature"); got != sha2 {
		t.Errorf("feature tip = %s, want unchanged %s", got, sha2)
	}
}

func TestPruneBackupRefs_KeepsRecentDeletesOld(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	sha := repo.Commit("c1")
	runner := &git.ExecRunner{Dir: repo.Dir}

	// Four backups for kind=ship, branch=main, at increasing timestamps.
	for _, ts := range []int64{1000, 2000, 3000, 4000} {
		repo.RunGit("update-ref", fmt.Sprintf("refs/gk/ship-backup/main/%d", ts), sha)
	}

	// now far in the future + tiny maxAge → all four are "old"; keepRecent=2
	// preserves the two newest (4000, 3000), prunes the rest (2000, 1000).
	now := func() time.Time { return time.Unix(10000, 0) }
	deleted := pruneBackupRefs(context.Background(), runner, now, "ship", "main", time.Second, 2)
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}
	remaining := repo.RunGit("for-each-ref", "--format=%(refname)", "refs/gk/ship-backup/main/*")
	if strings.Contains(remaining, "/1000") || strings.Contains(remaining, "/2000") {
		t.Errorf("old backups should be pruned, got:\n%s", remaining)
	}
	if !strings.Contains(remaining, "/3000") || !strings.Contains(remaining, "/4000") {
		t.Errorf("recent backups must survive, got:\n%s", remaining)
	}
}

func TestBranchFF_BlockedWhenCheckedOutElsewhere(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	sha1 := repo.Commit("c1")
	repo.RunGit("branch", "feature")
	repo.WriteFile("b.txt", "v2")
	sha2 := repo.Commit("c2")

	// Check feature out in a linked worktree — moving its ref would desync it.
	wt := filepath.Join(t.TempDir(), "linked")
	repo.RunGit("worktree", "add", wt, "feature")

	runner := &git.ExecRunner{Dir: repo.Dir}
	res, err := BranchFF(context.Background(), runner, fixedTime(), "ship", "feature", sha2)
	if err != nil {
		t.Fatalf("BranchFF returned error, want blocked result: %v", err)
	}
	if res.Outcome != FFBlocked {
		t.Fatalf("Outcome = %v, want FFBlocked", res.Outcome)
	}
	if !strings.Contains(res.Reason, "checked out") {
		t.Errorf("Reason = %q, want mention of checkout", res.Reason)
	}
	if got := repo.RunGit("rev-parse", "refs/heads/feature"); got != sha1 {
		t.Errorf("feature tip = %s, want unchanged %s", got, sha1)
	}
}
