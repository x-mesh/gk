package gitsafe

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// FFOutcome classifies the result of a BranchFF call. Callers map it to their
// own reporting (e.g. the agent envelope's ok/blocked state) — gitsafe stays
// ignorant of that layer, returning only this domain classification.
type FFOutcome int

const (
	// FFOk: the branch ref was fast-forwarded to newSHA via a compare-and-swap
	// update-ref, and a backup ref was written first. Result.BackupRef is set.
	FFOk FFOutcome = iota
	// FFUpToDate: the branch already pointed at newSHA. Nothing moved and no
	// backup was written.
	FFUpToDate
	// FFBlocked: the move was refused as a precondition, not an error — newSHA
	// is not a descendant of the branch tip (diverged or ahead), or the branch
	// is checked out in a worktree. Result.Reason explains which.
	FFBlocked
	// FFError: an unexpected git failure occurred; the returned error is non-nil.
	FFError
)

// BranchFFResult is the full outcome of a BranchFF call. It is always returned
// populated (OldSHA/NewSHA included even on a blocked/error path) so callers
// can log the attempted move.
type BranchFFResult struct {
	Branch    string
	OldSHA    string
	NewSHA    string
	Outcome   FFOutcome
	Reason    string // human-facing detail for FFBlocked / FFError
	BackupRef string // set only when Outcome == FFOk
}

// BranchFF fast-forwards refs/heads/<branch> to newSHA — safely and recoverably.
// It is the single ref-advance primitive shared by the wrap-up verbs (ship's
// base fast-forward, land --to, promote, pull --with-base), replacing the
// ad-hoc `git branch -f` / bare `update-ref` calls that each implemented only
// part of the contract.
//
// Steps (ordering is the safety invariant — do NOT reorder):
//
//  1. classify — resolve the branch's current tip (oldSHA) and compare to
//     newSHA: equal → FFUpToDate; oldSHA is an ancestor of newSHA → proceed;
//     otherwise → FFBlocked (not fast-forwardable).
//  2. guard — refuse (FFBlocked) when the branch is checked out in any
//     worktree. `git branch -f` refuses this for free; a bare `update-ref`
//     would silently desync that worktree's index, so the guard is restored
//     here explicitly.
//  3. backup — write refs/gk/<kind>-backup/<branch>/<unix> at oldSHA BEFORE the
//     move, so the previous tip is always recoverable via gk timemachine /
//     git reset.
//  4. move — `update-ref <ref> newSHA oldSHA`, a compare-and-swap that fails
//     loudly if a concurrent process moved the ref (no silent overwrite, unlike
//     `git branch -f`). On failure the backup ref is rolled back.
//
// It never moves a ref that is not a strict fast-forward, so no commit can be
// orphaned. The returned error is non-nil only for FFError; FFOk / FFUpToDate /
// FFBlocked all return a nil error — blocked is a result, not a failure.
//
// kind labels the backup ref ("ship", "land", "pull"). now supplies the backup
// timestamp; nil defaults to time.Now.
func BranchFF(ctx context.Context, runner git.Runner, now func() time.Time, kind, branch, newSHA string) (BranchFFResult, error) {
	if now == nil {
		now = time.Now
	}
	res := BranchFFResult{Branch: branch, NewSHA: newSHA}

	// 1. classify.
	oldSHA, err := ResolveRef(ctx, runner, "refs/heads/"+branch)
	if err != nil {
		res.Outcome = FFError
		res.Reason = fmt.Sprintf("resolve %s", branch)
		return res, fmt.Errorf("branch-ff: resolve %s: %w", branch, err)
	}
	res.OldSHA = oldSHA

	if oldSHA == newSHA {
		res.Outcome = FFUpToDate
		return res, nil
	}

	// oldSHA must be a strict ancestor of newSHA for a fast-forward.
	if _, _, aerr := runner.Run(ctx, "merge-base", "--is-ancestor", oldSHA, newSHA); aerr != nil {
		res.Outcome = FFBlocked
		res.Reason = fmt.Sprintf("%s (%s) is not fast-forwardable to %s — histories diverged or it is ahead",
			branch, shortSHA(oldSHA), shortSHA(newSHA))
		return res, nil
	}

	// 2. worktree guard — never move a ref another worktree has checked out.
	if path, werr := branchCheckoutPath(ctx, runner, branch); werr == nil && path != "" {
		res.Outcome = FFBlocked
		res.Reason = fmt.Sprintf("%s is checked out in %s", branch, path)
		return res, nil
	}

	// 3. backup before the move.
	backupRef := BackupRefName(kind, branch, now())
	if _, stderr, berr := runner.Run(ctx, "update-ref", backupRef, oldSHA); berr != nil {
		res.Outcome = FFError
		res.Reason = "write backup ref"
		return res, fmt.Errorf("branch-ff: backup %s: %s: %w", backupRef, strings.TrimSpace(string(stderr)), berr)
	}

	// 4. compare-and-swap move; roll the backup ref back if it fails.
	reason := fmt.Sprintf("gk %s: fast-forward to %s", kind, shortSHA(newSHA))
	if _, stderr, merr := runner.Run(ctx, "update-ref", "-m", reason, "refs/heads/"+branch, newSHA, oldSHA); merr != nil {
		_, _, _ = runner.Run(ctx, "update-ref", "-d", backupRef)
		res.Outcome = FFError
		res.Reason = "concurrent update or update-ref failed"
		return res, fmt.Errorf("branch-ff: update-ref %s: %s: %w", branch, strings.TrimSpace(string(stderr)), merr)
	}

	res.Outcome = FFOk
	res.BackupRef = backupRef
	// Keep the per-run backup family bounded. Wrap-up verbs (merge/land/promote)
	// call BranchFF on every integration, so without this the kind-namespaced
	// refs/gk/<kind>-backup/* refs would accumulate forever — they sit outside
	// git.PruneBackups, which only scans refs/gk/backup/*. Best-effort: a prune
	// failure never affects the (already completed) advance.
	_ = pruneBackupRefs(ctx, runner, now, kind, branch, 30*24*time.Hour, 10)
	return res, nil
}

// PruneKindBackups bounds the refs/gk/<kind>-backup/<branch>/<unix> family for
// callers outside BranchFF — notably aicommit's EnsureBackupRef, which writes a
// refs/gk/ai-commit-backup/* ref on every `gk commit` and otherwise has nothing
// to prune it (git.PruneBackups scans only refs/gk/backup/*, and BranchFF's prune
// runs only for its own integration kinds). Same conservative policy: keep the
// keepRecent newest, delete the rest older than maxAge. Best-effort; returns the
// number deleted.
func PruneKindBackups(ctx context.Context, runner git.Runner, kind, branch string, maxAge time.Duration, keepRecent int) int {
	return pruneBackupRefs(ctx, runner, time.Now, kind, branch, maxAge, keepRecent)
}

// pruneBackupRefs deletes BranchFF backup refs for (kind, branch) that are both
// beyond the keepRecent newest AND older than maxAge — the same conservative
// policy git.PruneBackups applies to refs/gk/backup/*, but for the
// kind-namespaced refs/gk/<kind>-backup/<branch>/<unix> family BranchFF writes.
// Best-effort: any enumeration/deletion error is swallowed and reported as 0.
func pruneBackupRefs(ctx context.Context, runner git.Runner, now func() time.Time, kind, branch string, maxAge time.Duration, keepRecent int) int {
	prefix := fmt.Sprintf("refs/gk/%s-backup/%s/", kind, SanitizeBranchSegment(branch))
	out, _, err := runner.Run(ctx, "for-each-ref", "--format=%(refname)", prefix+"*")
	if err != nil {
		return 0
	}

	type entry struct {
		ref string
		ts  int64
	}
	var entries []entry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		ts, perr := strconv.ParseInt(strings.TrimPrefix(line, prefix), 10, 64)
		if perr != nil {
			continue
		}
		entries = append(entries, entry{ref: line, ts: ts})
	}
	if len(entries) == 0 {
		return 0
	}

	// Newest first, so the first keepRecent entries are the ones to preserve.
	sort.Slice(entries, func(i, j int) bool { return entries[i].ts > entries[j].ts })

	cutoff := now().Add(-maxAge).Unix()
	deleted := 0
	for i, e := range entries {
		if i < keepRecent || e.ts >= cutoff {
			continue
		}
		if _, _, derr := runner.Run(ctx, "update-ref", "-d", e.ref); derr == nil {
			deleted++
		}
	}
	return deleted
}

// branchCheckoutPath returns the worktree path where branch is checked out, or
// "" when it is not checked out anywhere. Parses `git worktree list
// --porcelain`, whose records pair a `worktree <path>` line with a `branch
// refs/heads/<name>` line (detached worktrees emit `detached` instead and are
// skipped).
func branchCheckoutPath(ctx context.Context, runner git.Runner, branch string) (string, error) {
	out, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	target := "refs/heads/" + branch
	curPath := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			curPath = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch "):
			if strings.TrimSpace(strings.TrimPrefix(line, "branch ")) == target {
				return curPath, nil
			}
		}
	}
	return "", nil
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
