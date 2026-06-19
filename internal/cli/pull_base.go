package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// syncPullBase implements `gk pull --with-base`: after pull's own fetch it
// fast-forwards the repo's base branch from the remote without checking it
// out, so the morning multi-branch sync ("pull develop, switch, pull main")
// collapses into one command. It runs before integration on purpose — a
// conflict on the current branch must not strand the base update.
//
// base is the already-resolved --base/.gk.yaml value ("" → auto-detect);
// current is the branch being pulled; fetchBranch names the branch pull has
// just fetched so the same round-trip is not repeated when it is the base.
//
// ffSyncOutcome reports what ffSyncBranch did to one branch — consumed by
// `gk pull --json` so agent tooling reads a field instead of scraping the
// human lines.
type ffSyncOutcome struct {
	Branch string `json:"branch"`
	// Result: fast-forwarded | up-to-date | skipped-ahead | skipped-diverged |
	// skipped-worktree | skipped-no-local | skipped-no-remote | fetch-failed |
	// error
	Result string `json:"result"`
	Pre    string `json:"pre,omitempty"`
	Post   string `json:"post,omitempty"`
}

// Returns the base outcomes (nil when the base did not participate) and the
// branch-column labeler sized to cover both branch names, so the caller's own
// result lines (already-up-to-date, integrating, updated) align with the base
// lines printed here.
func syncPullBase(ctx context.Context, client *git.Client, runner git.Runner, w io.Writer, remote, base, current, fetchBranch string) ([]ffSyncOutcome, func(string) string) {
	curLabel := current
	if curLabel == "" {
		curLabel = "HEAD"
	}
	name := base
	if name == "" {
		detected, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			printNote(w, "--with-base: could not detect the base branch — set it with --base or `base` in .gk.yaml")
			return nil, branchLabeler(curLabel)
		}
		name = detected
	}
	if current != "" && current == name {
		// Pulling the base itself — the regular pull already covers it.
		return nil, branchLabeler(curLabel)
	}
	labeler := branchLabeler(curLabel, name)
	outcomes := ffSyncBranches(ctx, runner, w, remote, []string{name}, fetchBranch, labeler)
	return outcomes, labeler
}

// branchLabeler renders the branch-name column that prefixes every per-branch
// status line of a pull. The column is padded to the longest participating
// name so multi-branch output (--with-base) aligns vertically:
//
//	main     ✓ fast-forwarded 4ce31df → 3c33feeb
//	develop  updated 6a84c6ad → 2527b1fd  (+1 commit · ff-only)
//
// Names render cyan-bold; padding is computed on the raw name so ANSI codes
// cannot skew the column.
func branchLabeler(names ...string) func(string) string {
	width := 0
	for _, n := range names {
		if l := utf8.RuneCountInString(n); l > width {
			width = l
		}
	}
	return func(name string) string {
		pad := width - utf8.RuneCountInString(name)
		if pad < 0 {
			pad = 0
		}
		return cellCyanBold(name) + strings.Repeat(" ", pad+2)
	}
}

// ffSyncBranches fast-forwards each named local branch to <remote>/<branch>
// without a checkout. Strictly FF-only: every ambiguous state — diverged
// branch, branch checked out in some worktree, missing local or remote ref —
// is skipped with a NOTE rather than resolved automatically, so no local
// commit can ever be lost and no working tree is ever touched.
//
// Kept list-shaped so a future `pull.sync_branches` config can reuse it
// unchanged; today the only caller passes the single base branch.
func ffSyncBranches(ctx context.Context, runner git.Runner, w io.Writer, remote string, branches []string, skipFetchFor string, label func(string) string) []ffSyncOutcome {
	outcomes := make([]ffSyncOutcome, 0, len(branches))
	for _, b := range branches {
		outcomes = append(outcomes, ffSyncBranch(ctx, runner, w, remote, b, b == skipFetchFor, label))
	}
	return outcomes
}

func ffSyncBranch(ctx context.Context, runner git.Runner, w io.Writer, remote, branch string, alreadyFetched bool, label func(string) string) ffSyncOutcome {
	remoteRef := remote + "/" + branch
	outcome := ffSyncOutcome{Branch: branch}

	if !alreadyFetched {
		stop := ui.StartBubbleSpinner(fmt.Sprintf("fetching %s (base)", remoteRef))
		_, stderr, err := fetchRemoteTrackingBranchWithStderr(ctx, runner, remote, branch)
		stop()
		if err != nil {
			printNote(w, fmt.Sprintf("base '%s' not synced — fetch failed: %s",
				branch, strings.TrimSpace(string(stderr))))
			outcome.Result = "fetch-failed"
			return outcome
		}
	}

	// The local branch must already exist — creating refs behind the
	// user's back is out of scope for a convenience sync.
	oldOut, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		printNote(w,
			fmt.Sprintf("base '%s' not synced — no local branch", branch),
			fmt.Sprintf("create it with: gk sw %s", branch),
		)
		outcome.Result = "skipped-no-local"
		return outcome
	}
	oldSHA := strings.TrimSpace(string(oldOut))
	outcome.Pre = oldSHA

	newOut, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", remoteRef)
	if err != nil {
		printNote(w, fmt.Sprintf("base '%s' not synced — '%s' does not exist after fetch", branch, remoteRef))
		outcome.Result = "skipped-no-remote"
		return outcome
	}
	newSHA := strings.TrimSpace(string(newOut))

	if oldSHA == newSHA {
		fmt.Fprintln(w, label(branch)+"Already up to date  "+tipSuffix(ctx, runner, oldSHA))
		outcome.Result = "up-to-date"
		outcome.Post = oldSHA
		return outcome
	}

	// A branch checked out anywhere must not have its ref moved under the
	// working tree. The current worktree holds the branch being pulled, so
	// a hit here means the primary checkout or a linked worktree owns it.
	if entry, werr := findWorktreeForBranch(ctx, runner, branch); werr == nil && entry != nil {
		printNote(w,
			fmt.Sprintf("base '%s' not synced — checked out in %s", branch, entry.Path),
			"run gk pull there to update it",
		)
		outcome.Result = "skipped-worktree"
		return outcome
	}

	// FF-only gate: the local tip must be a strict ancestor of the remote
	// tip, which makes losing local commits structurally impossible. When it
	// is not, the branch has one of two shapes that need opposite advice, so
	// distinguish them rather than lumping both under "diverged" with a pull
	// suggestion that does nothing for the ahead-only case.
	if _, _, err := runner.Run(ctx, "merge-base", "--is-ancestor", oldSHA, newSHA); err != nil {
		// Remote tip is an ancestor of the local tip → the branch is strictly
		// ahead: nothing to integrate, the commits simply are not pushed yet.
		// The fix is push, not pull.
		if _, _, rerr := runner.Run(ctx, "merge-base", "--is-ancestor", newSHA, oldSHA); rerr == nil {
			summary := fmt.Sprintf("base '%s' ahead of %s — local commits not pushed", branch, remoteRef)
			if n := countRevs(ctx, runner, remoteRef+".."+branch); n > 0 {
				summary = fmt.Sprintf("base '%s' ahead of %s — %d commit%s not pushed", branch, remoteRef, n, plural(n))
			}
			printNote(w, appendEasyLine([]string{
				summary,
				fmt.Sprintf("publish them with: gk sw %s && gk push", branch),
			}, "pull.note.with_base_ahead", branch, remoteRef)...)
			outcome.Result = "skipped-ahead"
			return outcome
		}
		// Neither tip is an ancestor of the other → genuine divergence:
		// commits on both sides, reconcile by switching to it and pulling.
		summary := fmt.Sprintf("base '%s' diverged from %s — local and remote both moved", branch, remoteRef)
		if a, b := countAheadBehind(ctx, runner, branch, remoteRef); a > 0 || b > 0 {
			summary = fmt.Sprintf("base '%s' diverged from %s — %d local, %d remote", branch, remoteRef, a, b)
		}
		printNote(w, appendEasyLine([]string{
			summary,
			fmt.Sprintf("reconcile them with: gk sw %s && gk pull", branch),
		}, "pull.note.with_base_diverged", branch, remoteRef)...)
		outcome.Result = "skipped-diverged"
		return outcome
	}

	// Compare-and-swap on the old SHA so a concurrent ref update (another
	// gk/git process) fails loudly instead of being silently overwritten.
	reason := fmt.Sprintf("gk pull --with-base: fast-forward to %s", remoteRef)
	if _, stderr, err := runner.Run(ctx, "update-ref", "-m", reason, "refs/heads/"+branch, newSHA, oldSHA); err != nil {
		printNote(w, fmt.Sprintf("base '%s' not synced — update-ref failed: %s",
			branch, strings.TrimSpace(string(stderr))))
		outcome.Result = "error"
		return outcome
	}
	fmt.Fprintln(w, label(branch)+successLinef("fast-forwarded", "%s → %s", shortSHA(oldSHA), shortSHA(newSHA)))
	outcome.Result = "fast-forwarded"
	outcome.Post = newSHA
	return outcome
}

// countRevs returns the number of commits in rangeExpr (e.g.
// "origin/main..main"), or 0 when the count cannot be determined. Best-effort
// only — it enriches a NOTE and never gates a ref update.
func countRevs(ctx context.Context, runner git.Runner, rangeExpr string) int {
	out, _, err := runner.Run(ctx, "rev-list", "--count", rangeExpr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// countAheadBehind reports how many commits local has beyond remote (ahead)
// and vice versa (behind) via a single rev-list, returning 0, 0 when it cannot
// be determined. Best-effort, like countRevs.
func countAheadBehind(ctx context.Context, runner git.Runner, local, remote string) (ahead, behind int) {
	out, _, err := runner.Run(ctx, "rev-list", "--left-right", "--count", local+"..."+remote)
	if err != nil {
		return 0, 0
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return 0, 0
	}
	ahead, _ = strconv.Atoi(fields[0])
	behind, _ = strconv.Atoi(fields[1])
	return ahead, behind
}

func fetchRemoteTrackingBranch(ctx context.Context, runner git.Runner, remote, branch string) error {
	_, _, err := fetchRemoteTrackingBranchWithStderr(ctx, runner, remote, branch)
	return err
}

func fetchRemoteTrackingBranchWithStderr(ctx context.Context, runner git.Runner, remote, branch string) ([]byte, []byte, error) {
	if remote == "" {
		remote = "origin"
	}
	if branch == "" {
		return runner.Run(ctx, "fetch", remote)
	}
	return runner.Run(ctx, "fetch", remote, remoteTrackingFetchSpec(remote, branch))
}

func remoteTrackingFetchSpec(remote, branch string) string {
	if remote == "" {
		remote = "origin"
	}
	return "+refs/heads/" + branch + ":refs/remotes/" + remote + "/" + branch
}
