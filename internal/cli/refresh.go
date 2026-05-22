package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:     "refresh [branch...]",
		Aliases: []string{"re"},
		Short:   "Fast-forward long-lived branches (main, develop) to their remotes",
		Long: `Mirrors local long-lived branches onto their remote counterparts.

Each tracked branch is fast-forwarded to origin/<branch> independently —
refresh NEVER rebases or merges across branches. A branch only moves to its
own remote, and only as a fast-forward, so it is safe on shared branches:

  main    ←ff──  origin/main
  develop ←ff──  origin/develop

This replaces the manual "checkout main, pull, checkout develop, pull"
dance with a single command that does not leave your current branch:
branches you are not standing on move via update-ref (working tree
untouched), so refresh works even on a feature branch with a dirty tree.

Targets resolve in order:
  1. positional args     (gk refresh main release/1.x)
  2. refresh.tracked      in .gk.yaml
  3. dynamic              main/master + develop/dev that exist locally

A branch that has diverged from its remote (local commits not on the
remote) is skipped with a hint, never rewritten — use 'gk pull' there.

Exit codes:
  0  every resolvable branch is up to date or fast-forwarded
  1  general error (no targets, fetch failed and no cached refs)`,
		RunE: runRefresh,
	}
	cmd.Flags().Bool("no-fetch", false, "skip the network fetch; fast-forward against already-cached remote refs")
	rootCmd.AddCommand(cmd)
}

func runRefresh(cmd *cobra.Command, args []string) error {
	noFetch, _ := cmd.Flags().GetBool("no-fetch")

	cfg, _ := config.Load(cmd.Flags())
	if cfg == nil {
		d := config.Defaults()
		cfg = &d
	}
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	targets, err := resolveRefreshTargets(ctx, runner, client, cfg, args)
	if err != nil {
		return err
	}

	// Reject malformed refs early so a typo can't reach git plumbing.
	for _, t := range targets {
		if err := client.CheckRefFormat(ctx, t); err != nil {
			return fmt.Errorf("invalid branch %q: %w", t, err)
		}
	}

	// Current branch decides per-branch FF mechanics: the checked-out
	// branch must move via `merge --ff-only` (updates the working tree),
	// every other branch via `update-ref` (refs only, tree untouched).
	// Detached HEAD / errors → "" so no target is treated as current.
	currentBranch, _ := client.CurrentBranch(ctx)

	fetched := false
	if !noFetch {
		stop := ui.StartBubbleSpinner(fmt.Sprintf("fetching %s", remote))
		ferr := client.Fetch(ctx, remote, "", true)
		stop()
		if ferr != nil {
			// Non-fatal: cached remote refs may still let us fast-forward.
			// Warn and fall through rather than dead-ending the command.
			fmt.Fprintf(w, "%s fetch %s failed: %v  %s\n",
				cellYellow("!"), remote, ferr, cellFaint("(using cached refs)"))
		} else {
			fetched = true
		}
	}

	results := make([]refreshResult, 0, len(targets))
	for _, t := range targets {
		results = append(results, refreshOne(ctx, runner, remote, t, t == currentBranch))
	}

	renderRefresh(w, remote, results, fetched)
	return nil
}

// resolveRefreshTargets picks the branch list to fast-forward, in order:
// explicit positional args → configured refresh.tracked → dynamic
// (main/master + develop/dev that exist locally). The dynamic fallback
// mirrors `gk switch --main/--develop` resolution so master-based and
// dev-based repos work without configuration. Duplicates are removed
// while preserving order.
func resolveRefreshTargets(ctx context.Context, runner git.Runner, client *git.Client, cfg *config.Config, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		return dedupStrings(explicit), nil
	}
	if cfg != nil && len(cfg.Refresh.Tracked) > 0 {
		return dedupStrings(cfg.Refresh.Tracked), nil
	}

	var dynamic []string
	if main, err := resolveMainBranch(ctx, runner, client, cfg.Remote); err == nil && main != "" {
		dynamic = append(dynamic, main)
	}
	if dev, err := resolveDevelopBranch(ctx, runner); err == nil && dev != "" {
		dynamic = append(dynamic, dev)
	}
	dynamic = dedupStrings(dynamic)
	if len(dynamic) == 0 {
		return nil, WithHint(
			fmt.Errorf("no long-lived branches found to refresh"),
			"name them explicitly: gk refresh <branch> ...  (or set refresh.tracked in .gk.yaml)",
		)
	}
	return dynamic, nil
}

// refreshResult is the per-branch outcome of a refresh pass.
type refreshResult struct {
	Branch string
	Status refreshStatus
	From   string // short SHA before (when known)
	To     string // short SHA after (when moved)
	Count  int    // commits gained on fast-forward
	Note   string // error/skip detail
}

type refreshStatus string

const (
	refreshUpToDate refreshStatus = "up-to-date"
	refreshFF       refreshStatus = "fast-forwarded"
	refreshNoLocal  refreshStatus = "no-local"
	refreshNoRemote refreshStatus = "no-remote"
	refreshDiverged refreshStatus = "diverged"
	refreshError    refreshStatus = "error"
)

// refreshOne fast-forwards a single branch to <remote>/<branch>. It only
// ever advances refs as a fast-forward — never rebase/merge — so a diverged
// branch is reported, not rewritten. The checked-out branch (isCurrent)
// moves via `merge --ff-only` so the working tree follows; any other branch
// moves via `update-ref`, which is safe regardless of the working tree.
func refreshOne(ctx context.Context, r git.Runner, remote, branch string, isCurrent bool) refreshResult {
	res := refreshResult{Branch: branch}
	localRef := "refs/heads/" + branch
	if !git.RefExists(ctx, r, localRef) {
		res.Status = refreshNoLocal
		return res
	}
	upstream := remote + "/" + branch
	if !git.RefExists(ctx, r, "refs/remotes/"+upstream) {
		res.Status = refreshNoRemote
		return res
	}

	before := resolveShortSHA(ctx, r, localRef)
	res.From = before
	if equalRefs(ctx, r, localRef, upstream) {
		res.Status = refreshUpToDate
		res.To = before
		return res
	}
	// Fast-forward only: local must be a strict ancestor of the remote.
	if _, _, err := r.Run(ctx, "merge-base", "--is-ancestor", localRef, upstream); err != nil {
		res.Status = refreshDiverged
		return res
	}

	if isCurrent {
		if _, stderr, err := r.Run(ctx, "merge", "--ff-only", upstream); err != nil {
			res.Status = refreshError
			res.Note = firstLine(string(stderr))
			return res
		}
	} else {
		full, _, err := r.Run(ctx, "rev-parse", upstream)
		if err != nil {
			res.Status = refreshError
			res.Note = err.Error()
			return res
		}
		if _, stderr, err := r.Run(ctx, "update-ref", localRef, strings.TrimSpace(string(full))); err != nil {
			res.Status = refreshError
			res.Note = firstLine(string(stderr))
			return res
		}
	}

	res.To = resolveShortSHA(ctx, r, localRef)
	if n, _, err := r.Run(ctx, "rev-list", "--count", before+".."+res.To); err == nil {
		res.Count, _ = strconv.Atoi(strings.TrimSpace(string(n)))
	}
	res.Status = refreshFF
	return res
}

// renderRefresh prints the per-branch report plus a one-line summary.
func renderRefresh(w interface{ Write(p []byte) (int, error) }, remote string, results []refreshResult, fetched bool) {
	faint := color.New(color.Faint).SprintFunc()
	if fetched {
		fmt.Fprintf(w, "fetched %s %s\n", remote, faint("(--prune)"))
	}

	width := 0
	for _, r := range results {
		if len(r.Branch) > width {
			width = len(r.Branch)
		}
	}

	var advanced, current, skipped int
	for _, r := range results {
		name := fmt.Sprintf("%-*s", width, r.Branch)
		switch r.Status {
		case refreshFF:
			advanced++
			fmt.Fprintf(w, "  %s %s  %s → %s  %s\n",
				cellGreen("✓"), name, r.From, r.To,
				cellGreen(fmt.Sprintf("+%d", r.Count)))
		case refreshUpToDate:
			current++
			fmt.Fprintf(w, "  %s %s  %s\n", cellFaint("="), name, faint("up to date"))
		case refreshDiverged:
			skipped++
			fmt.Fprintf(w, "  %s %s  %s\n", cellYellow("!"), name,
				cellYellow(fmt.Sprintf("diverged from %s/%s — use `gk pull`", remote, r.Branch)))
		case refreshNoLocal:
			skipped++
			fmt.Fprintf(w, "  %s %s  %s\n", cellFaint("·"), name, faint("no local branch"))
		case refreshNoRemote:
			skipped++
			fmt.Fprintf(w, "  %s %s  %s\n", cellFaint("·"), name,
				faint(fmt.Sprintf("not on %s", remote)))
		case refreshError:
			skipped++
			detail := r.Note
			if detail == "" {
				detail = "failed"
			}
			fmt.Fprintf(w, "  %s %s  %s\n", cellRed("✗"), name, cellRed(detail))
		}
	}

	parts := make([]string, 0, 3)
	parts = append(parts, fmt.Sprintf("%d fast-forwarded", advanced))
	if current > 0 {
		parts = append(parts, fmt.Sprintf("%d up to date", current))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	fmt.Fprintln(w, faint(strings.Join(parts, " · ")))
}

// dedupStrings returns xs with duplicates removed, preserving first-seen
// order. Empty strings are dropped.
func dedupStrings(xs []string) []string {
	seen := make(map[string]struct{}, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

// firstLine returns the first non-empty trimmed line of s, for compact
// one-line error reporting from multi-line git stderr.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}
