package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// DivergedError is returned when a branch cannot be fast-forwarded because
// it has local commits not present upstream. The caller should exit with Code.
type DivergedError struct {
	Code   int
	Branch string
}

func (e *DivergedError) Error() string {
	return fmt.Sprintf("branch %q has diverged from upstream — use `gk pull` to rebase", e.Branch)
}

func init() {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Fetch remotes and fast-forward local branches to their upstreams",
		Long: `Fetches from remotes and fast-forwards the current branch (or every
local branch with --all) against its configured upstream. Never creates merge
commits; never rebases. If a branch has diverged, sync fails and suggests
` + "`gk pull`" + ` instead.

Exit codes:
  0  fast-forwarded or already up to date
  1  general error (fetch failure, dirty tree without --autostash)
  4  diverged branch (cannot fast-forward)`,
		RunE: runSync,
	}
	cmd.Flags().Bool("all", false, "sync every local branch with a configured upstream")
	cmd.Flags().Bool("fetch-only", false, "fetch remotes, skip fast-forward step")
	cmd.Flags().Bool("no-fetch", false, "skip fetch, FF from already-fetched upstream refs")
	cmd.Flags().Bool("autostash", false, "stash dirty changes before FF, pop after")
	rootCmd.AddCommand(cmd)
}

func runSync(cmd *cobra.Command, _ []string) error {
	err := runSyncCore(cmd)
	var de *DivergedError
	if errors.As(err, &de) {
		os.Exit(de.Code)
	}
	return err
}

// syncReport is the per-branch outcome for output assembly.
type syncReport struct {
	Branch   string
	Status   string // "up-to-date" | "fast-forwarded" | "no-upstream" | "diverged"
	Upstream string
	From     string // short SHA before
	To       string // short SHA after
}

func runSyncCore(cmd *cobra.Command) error {
	cfg, _ := config.Load(cmd.Flags())
	all, _ := cmd.Flags().GetBool("all")
	fetchOnly, _ := cmd.Flags().GetBool("fetch-only")
	noFetch, _ := cmd.Flags().GetBool("no-fetch")
	autostash, _ := cmd.Flags().GetBool("autostash")

	if fetchOnly && noFetch {
		return errors.New("--fetch-only and --no-fetch are mutually exclusive")
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()

	// 1) fetch
	if !noFetch {
		remote := "--all"
		if cfg != nil && cfg.Remote != "" && !all {
			remote = cfg.Remote
		}
		fmt.Fprintf(os.Stderr, "fetching %s...\n", strings.TrimPrefix(remote, "--"))
		if err := fetchRemotes(ctx, runner, remote); err != nil {
			return fmt.Errorf("fetch failed: %w", err)
		}
	}

	if fetchOnly {
		fmt.Fprintln(os.Stderr, "done (fetch only)")
		return nil
	}

	// 2) dirty check (only matters for current-branch FF; update-ref is safe for others)
	dirty, err := client.IsDirty(ctx)
	if err != nil {
		return err
	}
	currentBranch, err := client.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("cannot determine current branch: %w", err)
	}

	var stashed bool
	if dirty {
		if !autostash {
			return WithHint(
				errors.New("working tree has uncommitted changes"),
				hintCommand("gk sync --autostash"),
			)
		}
		if _, _, err := runner.Run(ctx, "stash", "push", "-m", "gk sync autostash"); err != nil {
			return fmt.Errorf("stash failed: %w", err)
		}
		stashed = true
		defer func() {
			if stashed {
				popStashBestEffort(ctx, runner)
			}
		}()
	}

	// 3) decide which branches to sync
	type branchTarget struct{ Name, Upstream string }
	var targets []branchTarget
	if all {
		bs, err := listLocalBranches(ctx, runner)
		if err != nil {
			return err
		}
		for _, b := range bs {
			targets = append(targets, branchTarget{Name: b.Name, Upstream: b.Upstream})
		}
	} else {
		up, _ := upstreamOf(ctx, runner, currentBranch)
		targets = []branchTarget{{Name: currentBranch, Upstream: up}}
	}

	// 4) sync each
	var reports []syncReport
	var divergedCount int
	for _, bt := range targets {
		if bt.Upstream == "" {
			reports = append(reports, syncReport{Branch: bt.Name, Status: "no-upstream"})
			continue
		}
		rep, err := syncOne(ctx, runner, bt.Name, bt.Upstream, bt.Name == currentBranch)
		if err != nil {
			var de *DivergedError
			if errors.As(err, &de) {
				divergedCount++
				reports = append(reports, syncReport{Branch: bt.Name, Status: "diverged", Upstream: bt.Upstream})
				continue
			}
			return fmt.Errorf("sync %s: %w", bt.Name, err)
		}
		reports = append(reports, rep)
	}

	// 5) pop stash (only if still stashed; defer handles error paths)
	if stashed {
		stashed = false
		if err := popStash(ctx, runner); err != nil {
			return fmt.Errorf("stash pop failed: %w", err)
		}
	}

	// 6) render report
	writeSyncReport(cmd.OutOrStdout(), reports)

	if divergedCount > 0 {
		return &DivergedError{Code: 4, Branch: firstDiverged(reports)}
	}
	return nil
}

// fetchRemotes runs `git fetch --all --prune` or `git fetch <remote> --prune`.
func fetchRemotes(ctx context.Context, r git.Runner, remote string) error {
	args := []string{"fetch", "--prune"}
	if remote == "--all" {
		args = append(args, "--all")
	} else {
		args = append(args, remote)
	}
	_, stderr, err := r.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// upstreamOf returns the short upstream ref (e.g., "origin/main") for a branch,
// or "" if none is configured.
func upstreamOf(ctx context.Context, r git.Runner, branch string) (string, error) {
	stdout, _, err := r.Run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	if err != nil {
		return "", nil // no upstream — treat as soft skip
	}
	return strings.TrimSpace(string(stdout)), nil
}

// resolveShortSHA resolves a ref to a 7-char abbreviated SHA via git rev-parse.
// Returns "" on error. Distinct from the string-truncation `shortSHA` in undo.go.
func resolveShortSHA(ctx context.Context, r git.Runner, ref string) string {
	stdout, _, err := r.Run(ctx, "rev-parse", "--short=7", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(stdout))
}

// syncOne fast-forwards a single branch to its upstream.
// For the current branch uses `merge --ff-only`; for others uses `update-ref`
// after verifying the FF relationship via `merge-base --is-ancestor`.
func syncOne(ctx context.Context, r git.Runner, branch, upstream string, isCurrent bool) (syncReport, error) {
	before := resolveShortSHA(ctx, r, branch)

	// equal heads → up-to-date
	if equalRefs(ctx, r, branch, upstream) {
		return syncReport{Branch: branch, Status: "up-to-date", Upstream: upstream, From: before, To: before}, nil
	}

	// branch ancestor of upstream? → FF possible
	if _, _, err := r.Run(ctx, "merge-base", "--is-ancestor", branch, upstream); err != nil {
		return syncReport{}, &DivergedError{Code: 4, Branch: branch}
	}

	if isCurrent {
		if _, stderr, err := r.Run(ctx, "merge", "--ff-only", upstream); err != nil {
			return syncReport{}, fmt.Errorf("merge --ff-only: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	} else {
		upstreamSHA := resolveShortSHA(ctx, r, upstream)
		if upstreamSHA == "" {
			return syncReport{}, fmt.Errorf("cannot resolve upstream %s", upstream)
		}
		fullSHA, _, rerr := r.Run(ctx, "rev-parse", upstream)
		if rerr != nil {
			return syncReport{}, rerr
		}
		if _, stderr, err := r.Run(ctx, "update-ref", "refs/heads/"+branch, strings.TrimSpace(string(fullSHA))); err != nil {
			return syncReport{}, fmt.Errorf("update-ref: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	}

	after := resolveShortSHA(ctx, r, branch)
	return syncReport{
		Branch: branch, Status: "fast-forwarded", Upstream: upstream, From: before, To: after,
	}, nil
}

// equalRefs returns true when two refs point at the same commit.
func equalRefs(ctx context.Context, r git.Runner, a, b string) bool {
	aSHA, _, errA := r.Run(ctx, "rev-parse", a)
	bSHA, _, errB := r.Run(ctx, "rev-parse", b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.TrimSpace(string(aSHA)) == strings.TrimSpace(string(bSHA))
}

// firstDiverged returns the name of the first diverged branch for error assembly.
func firstDiverged(reports []syncReport) string {
	for _, r := range reports {
		if r.Status == "diverged" {
			return r.Branch
		}
	}
	return ""
}

// writeSyncReport renders the report table to w.
func writeSyncReport(w interface{ Write(p []byte) (int, error) }, reports []syncReport) {
	for _, r := range reports {
		switch r.Status {
		case "up-to-date":
			fmt.Fprintf(w, "= %-28s up to date (%s)\n", r.Branch, r.Upstream)
		case "fast-forwarded":
			fmt.Fprintf(w, "→ %-28s %s → %s  (%s)\n", r.Branch, r.From, r.To, r.Upstream)
		case "no-upstream":
			fmt.Fprintf(w, "? %-28s no upstream configured — skipped\n", r.Branch)
		case "diverged":
			fmt.Fprintf(w, "! %-28s diverged from %s — use `gk pull`\n", r.Branch, r.Upstream)
		}
	}
}
