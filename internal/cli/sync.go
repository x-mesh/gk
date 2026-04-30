package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// DivergedError is returned when the legacy --upstream-only path detects
// a branch that cannot be fast-forwarded because it has local commits not
// present upstream. The caller should exit with Code.
type DivergedError struct {
	Code   int
	Branch string
}

func (e *DivergedError) Error() string {
	return fmt.Sprintf("branch %q has diverged from upstream — use `gk pull` to rebase", e.Branch)
}

const deprecationEnvVar = "GK_SUPPRESS_DEPRECATION"

func init() {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Rebase the current branch onto its (local) base",
		Long: `Integrates the local base branch into the current branch.
Operates entirely on local refs by default — no network. Fast-forwards
when the current branch is already an ancestor of <base>; otherwise
rebases (configurable).

This is the "trunk-relative rebase" half of the gk pull/sync split:

  gk pull   ←  network: fetch <remote>/<self> and integrate
  gk sync   ←  local:   rebase HEAD onto local <base>

To refresh local <base> from the remote first, either:
  gk checkout <base> && gk pull           (explicit two-step)
  gk sync --fetch                         (fetch + ff local <base> + sync)

Strategy resolution order (first match wins):
  1. --strategy flag
  2. sync.strategy in .gk.yaml
  3. git config pull.rebase  (true→rebase, false→merge)
  4. default: rebase

Self-FF: when ` + "`origin/<self>`" + ` is strictly ahead of the local branch
(e.g., another machine pushed earlier), gk fast-forwards before integrating
the base. Diverged self refs are skipped silently.

When local <base> differs from <remote>/<base>, gk sync prints a hint but
does not block — the user explicitly asked for "from local <base>".

Legacy: --upstream-only preserves the v0.6 sync semantics (fetch + FF to
` + "`origin/<self>`" + ` only, never integrate base) for one release. It is
removed in v0.8; ` + "`gk pull`" + ` covers the same ground.

Exit codes:
  0  integration succeeded or already up to date
  1  general error (missing local base, dirty tree without --autostash, etc.)
  3  rebase/merge conflict (resume with gk continue / gk abort)
  4  diverged but --strategy ff-only refused divergence`,
		RunE: runSync,
	}
	cmd.Flags().String("base", "", "base branch (auto-detect if empty)")
	cmd.Flags().String("strategy", "", "integration strategy: rebase|merge|ff-only|auto")
	cmd.Flags().Bool("fetch", false, "fetch <remote>/<base> and fast-forward local <base> before integrating")
	cmd.Flags().Bool("fetch-only", false, "fetch <remote>/<base> and ff local <base>; skip integration")
	cmd.Flags().Bool("no-fetch", false, "deprecated: default behaviour is now no-fetch; flag retained as a no-op alias")
	cmd.Flags().Bool("autostash", false, "stash dirty changes before integration, pop after")
	cmd.Flags().Bool("upstream-only", false, "legacy v0.6 behaviour: FF current branch to origin/<self> only (deprecated, removed in v0.8)")
	rootCmd.AddCommand(cmd)
}

func runSync(cmd *cobra.Command, _ []string) error {
	err := runSyncCore(cmd)
	var de *DivergedError
	if errors.As(err, &de) {
		os.Exit(de.Code)
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		os.Exit(ce.Code)
	}
	return err
}

// runSyncCore is the new "catch up to base" logic. The legacy upstream-FF
// behaviour is preserved behind --upstream-only and lives in runSyncLegacy.
func runSyncCore(cmd *cobra.Command) error {
	upstreamOnly, _ := cmd.Flags().GetBool("upstream-only")
	if upstreamOnly {
		return runSyncLegacy(cmd)
	}

	cfg, _ := config.Load(cmd.Flags())
	if cfg == nil {
		d := config.Defaults()
		cfg = &d
	}

	base, _ := cmd.Flags().GetString("base")
	if base == "" {
		base = cfg.BaseBranch
	}
	strategyFlag, _ := cmd.Flags().GetString("strategy")
	fetchFlag, _ := cmd.Flags().GetBool("fetch")
	fetchOnly, _ := cmd.Flags().GetBool("fetch-only")
	autostash, _ := cmd.Flags().GetBool("autostash")
	// --no-fetch is now the default; the flag is a no-op kept only so
	// existing scripts don't error. Reading and discarding silently.
	_, _ = cmd.Flags().GetBool("no-fetch")

	if fetchOnly && fetchFlag {
		return errors.New("--fetch-only and --fetch are mutually exclusive")
	}

	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	client := git.NewClient(runner)
	ctx := cmd.Context()

	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}

	if base == "" {
		detected, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			return fmt.Errorf("could not determine base branch: %w (use --base)", err)
		}
		base = detected
	}
	if err := client.CheckRefFormat(ctx, base); err != nil {
		return fmt.Errorf("invalid base branch %q: %w", base, err)
	}

	currentBranch, err := client.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("cannot determine current branch: %w", err)
	}
	if currentBranch == base {
		return fmt.Errorf("current branch is the base branch (%s); nothing to sync", base)
	}

	// Local <base> is the integration source — without it there is
	// nothing to rebase onto. Direct the user to --fetch when no local
	// ref exists yet (fresh worktree, never checked out main).
	if !git.RefExists(ctx, runner, "refs/heads/"+base) {
		return WithHint(
			fmt.Errorf("local %s does not exist", base),
			hintCommand("gk sync --fetch"),
		)
	}

	// --fetch / --fetch-only: refresh <remote>/<base> and fast-forward
	// the local base ref. Fast-forward only — if local base has diverged
	// from the remote we leave it alone and let the stale-base hint
	// surface that.
	if fetchFlag || fetchOnly {
		fmt.Fprintf(os.Stderr, "fetching %s/%s...\n", remote, base)
		if err := client.Fetch(ctx, remote, base, false); err != nil {
			return fmt.Errorf("fetch failed: %w", err)
		}
		if pre, post, err := fastForwardLocalBase(ctx, runner, base, remote); err == nil && pre != post {
			fmt.Fprintf(os.Stderr, "local %s: %s → %s  (fast-forwarded from %s/%s)\n",
				base, pre, post, remote, base)
		}
	}

	if fetchOnly {
		renderSyncFetchOnly(cmd, runner, base, remote, currentBranch)
		return nil
	}

	// Stale-base detection — informational. Printed before integration
	// so the user understands what `local <base>` they're rebasing onto.
	staleHint := buildStaleBaseHint(ctx, runner, base, remote)
	if staleHint != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), staleHint)
	}

	dirty, err := client.IsDirty(ctx)
	if err != nil {
		return err
	}
	var stashed bool
	if dirty {
		switch {
		case autostash:
			if _, _, err := runner.Run(ctx, "stash", "push", "-m", "gk sync autostash"); err != nil {
				return fmt.Errorf("stash failed: %w", err)
			}
			stashed = true
		case ui.IsTerminal():
			ok, perr := promptStashDirty(ctx, runner, "gk sync autostash")
			if perr != nil {
				if errors.Is(perr, errSkipDirty) {
					return WithHint(
						errors.New("working tree has uncommitted changes"),
						hintCommand("gk sync --autostash"),
					)
				}
				return perr
			}
			stashed = ok
		default:
			return WithHint(
				errors.New("working tree has uncommitted changes"),
				hintCommand("gk sync --autostash"),
			)
		}
		if stashed {
			defer popStashBestEffort(ctx, runner)
		}
	}

	preHEAD := headRev(ctx, runner)

	// Self-FF (always-on): if origin/<self> is strictly ahead of local self,
	// fast-forward first. Catches the multi-machine push case without
	// requiring a separate command. Diverged refs are skipped silently.
	selfFFPre, selfFFPost := tryAdvanceSelfFF(ctx, runner, currentBranch)

	strategy, _ := resolveSyncStrategyWithSource(ctx, strategyFlag, cfg, runner)

	// Integration source is the *local* base ref. git resolves bare
	// `main` against refs/heads/main, never refs/remotes/origin/main —
	// keep it bare so the rest of executePullStrategy renders cleanly.
	integrationRef := base

	// Ancestor short-circuit: HEAD already an ancestor of base → any
	// strategy collapses to a fast-forward merge.
	requested := strategy
	if isFastForwardPossible(ctx, runner, integrationRef) {
		strategy = pullStrategyFFOnly
	}

	fmt.Fprintf(os.Stderr, "integrating %s into %s (%s)...\n", integrationRef, currentBranch, strategy)
	if err := executePullStrategy(ctx, client, runner, integrationRef, strategy, stashed); err != nil {
		return err
	}

	postHEAD := headRev(ctx, runner)
	renderSyncSummary(cmd, runner, currentBranch, base, integrationRef, preHEAD, postHEAD, requested, strategy, selfFFPre, selfFFPost)

	if stashed {
		if err := popStash(ctx, runner); err != nil {
			return fmt.Errorf("stash pop failed: %w", err)
		}
	}
	return nil
}

// fastForwardLocalBase moves refs/heads/<base> forward to <remote>/<base>
// when the remote ref is strictly ahead. Uses update-ref so the user
// doesn't need to be checked out on <base>; the working tree stays put
// on the current feature branch.
//
// Returns (preSHA, postSHA) — pre==post means no movement (already at
// remote, diverged, or remote ref missing). Errors are surfaced so the
// caller can decide whether to mention them; callers typically ignore
// non-critical fetch/ff failures since the integration step still runs.
func fastForwardLocalBase(ctx context.Context, runner git.Runner, base, remote string) (string, string, error) {
	upstream := remote + "/" + base
	if !git.RefExists(ctx, runner, "refs/remotes/"+upstream) {
		return "", "", fmt.Errorf("no cached %s ref", upstream)
	}
	pre := resolveShortSHA(ctx, runner, "refs/heads/"+base)
	if pre == "" {
		return "", "", fmt.Errorf("local %s not resolvable", base)
	}
	if equalRefs(ctx, runner, "refs/heads/"+base, upstream) {
		return pre, pre, nil
	}
	// Only ff if local base is strictly an ancestor of remote.
	if _, _, err := runner.Run(ctx, "merge-base", "--is-ancestor", "refs/heads/"+base, upstream); err != nil {
		return pre, pre, nil
	}
	fullSHA, _, rerr := runner.Run(ctx, "rev-parse", upstream)
	if rerr != nil {
		return pre, pre, rerr
	}
	if _, _, uerr := runner.Run(ctx, "update-ref", "refs/heads/"+base, strings.TrimSpace(string(fullSHA))); uerr != nil {
		return pre, pre, uerr
	}
	post := resolveShortSHA(ctx, runner, "refs/heads/"+base)
	return pre, post, nil
}

// buildStaleBaseHint returns a multi-line warning when local <base>
// differs from <remote>/<base>. Empty string means clean (or the remote
// ref is unknown, in which case there's nothing to compare against).
//
// Sync uses local <base> by design, so this is informational, not an
// error: the user might genuinely want their local base regardless of
// what's on the remote.
func buildStaleBaseHint(ctx context.Context, runner git.Runner, base, remote string) string {
	upstream := remote + "/" + base
	if !git.RefExists(ctx, runner, "refs/remotes/"+upstream) {
		return ""
	}
	out, _, err := runner.Run(ctx, "rev-list", "--left-right", "--count", "refs/heads/"+base+"..."+upstream)
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return ""
	}
	aheadLocal, _ := strconv.Atoi(fields[0])  // commits in local base not in upstream
	behindLocal, _ := strconv.Atoi(fields[1]) // commits in upstream not in local base
	if aheadLocal == 0 && behindLocal == 0 {
		return ""
	}
	yellow := color.YellowString
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()
	var b strings.Builder
	fmt.Fprintf(&b, "%s local %s differs from %s  (%s↑%d local · ↓%d %s%s)\n",
		yellow("⚠"), bold(base), bold(upstream),
		faint(""), aheadLocal, behindLocal, remote, faint(""))
	fmt.Fprintf(&b, "  %s\n", faint("hint:"))
	fmt.Fprintf(&b, "    %s   %s\n",
		bold("git checkout "+base+" && gk pull"),
		faint("(refresh local "+base+" from "+upstream+")"))
	fmt.Fprintf(&b, "    %s                       %s",
		bold("gk sync --fetch"),
		faint("(refresh + integrate in one step)"))
	return b.String()
}

// tryAdvanceSelfFF fast-forwards the current branch to origin/<self> when
// (a) the upstream ref exists and (b) HEAD is strictly an ancestor.
// Returns the (pre, post) short SHAs when an FF actually moved HEAD.
// On any failure or skip, returns empty strings.
func tryAdvanceSelfFF(ctx context.Context, runner git.Runner, branch string) (string, string) {
	upstream, _ := upstreamOf(ctx, runner, branch)
	if upstream == "" {
		return "", ""
	}
	if equalRefs(ctx, runner, "HEAD", upstream) {
		return "", ""
	}
	if _, _, err := runner.Run(ctx, "merge-base", "--is-ancestor", "HEAD", upstream); err != nil {
		return "", ""
	}
	pre := resolveShortSHA(ctx, runner, "HEAD")
	if _, _, err := runner.Run(ctx, "merge", "--ff-only", upstream); err != nil {
		return "", ""
	}
	post := resolveShortSHA(ctx, runner, "HEAD")
	return pre, post
}

// renderSyncSummary prints the post-integration block.
func renderSyncSummary(
	cmd *cobra.Command,
	runner git.Runner,
	branch, base, upstream, pre, post, requestedStrategy, strategy string,
	selfFFPre, selfFFPost string,
) {
	out := cmd.ErrOrStderr()
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()
	ctx := cmd.Context()

	if selfFFPre != "" && selfFFPost != "" && selfFFPre != selfFFPost {
		fmt.Fprintf(out, "self-ff: %s → %s  %s\n", bold(selfFFPre), bold(selfFFPost), faint("(origin/"+branch+" was ahead)"))
	}

	if pre == "" || post == "" {
		return
	}
	if pre == post {
		fmt.Fprintf(out, "already up to date with %s at %s\n", upstream, bold(shortSHA(post)))
		return
	}

	count := 0
	if n, _, err := runner.Run(ctx, "rev-list", "--count", pre+".."+post); err == nil {
		count, _ = strconv.Atoi(strings.TrimSpace(string(n)))
	}
	verb := "rebased"
	switch strategy {
	case pullStrategyFFOnly:
		verb = "fast-forwarded"
	case pullStrategyMerge:
		verb = "merged"
	}
	header := fmt.Sprintf("%s %s onto %s  %s → %s", verb, branch, base, bold(shortSHA(pre)), bold(shortSHA(post)))
	suffix := fmt.Sprintf("(+%d commit%s · %s)", count, plural(count), strategy)
	if requestedStrategy != strategy && requestedStrategy != "" {
		suffix = fmt.Sprintf("(+%d commit%s · %s → %s)", count, plural(count), requestedStrategy, strategy)
	}
	fmt.Fprintf(out, "%s  %s\n", header, faint(suffix))

	if stat, _, err := runner.Run(ctx, "diff", "--shortstat", pre+".."+post); err == nil {
		if s := strings.TrimSpace(string(stat)); s != "" {
			fmt.Fprintln(out, faint(s))
		}
	}
}

// renderSyncFetchOnly prints the --fetch-only summary. Compares HEAD to
// the *local* base (the integration source) since that's what a follow-up
// `gk sync` would use. The --fetch step has already advanced local base
// when it could, so this comparison is against the most up-to-date state
// available without doing the integration.
func renderSyncFetchOnly(cmd *cobra.Command, runner git.Runner, base, remote, currentBranch string) {
	ctx := cmd.Context()
	out := cmd.ErrOrStderr()
	faint := color.New(color.Faint).SprintFunc()
	raw, _, err := runner.Run(ctx, "rev-list", "--left-right", "--count", "HEAD..."+base)
	if err != nil {
		fmt.Fprintln(out, "fetched; integrate with `gk sync`")
		return
	}
	fields := strings.Fields(strings.TrimSpace(string(raw)))
	if len(fields) != 2 {
		fmt.Fprintln(out, "fetched; integrate with `gk sync`")
		return
	}
	ahead, _ := strconv.Atoi(fields[0])
	behind, _ := strconv.Atoi(fields[1])
	switch {
	case ahead == 0 && behind == 0:
		fmt.Fprintf(out, "already up to date with %s\n", base)
	case behind > 0 && ahead == 0:
		fmt.Fprintf(out, "fetched %s: %s %s waiting  %s\n",
			base,
			color.GreenString("+%d", behind),
			plural2(behind, "commit"),
			faint("(run `gk sync` to integrate)"))
	case behind > 0 && ahead > 0:
		fmt.Fprintf(out, "fetched %s: ↑%d local · ↓%d base  %s\n",
			base, ahead, behind,
			faint("(diverged — run `gk sync` to rebase/merge)"))
	case ahead > 0:
		fmt.Fprintf(out, "fetched %s: ↑%d local, base unchanged\n", base, ahead)
	}
	if hint := buildStaleBaseHint(ctx, runner, base, remote); hint != "" {
		fmt.Fprintln(out, hint)
	}
}

// runSyncLegacy preserves the v0.6 sync semantics (fetch + FF current branch
// to origin/<self> only) behind --upstream-only. Removed in v0.8.
func runSyncLegacy(cmd *cobra.Command) error {
	if os.Getenv(deprecationEnvVar) == "" {
		fmt.Fprintln(os.Stderr,
			"deprecated: gk sync --upstream-only will be removed in v0.8; use `gk pull` for the same effect")
	}

	cfg, _ := config.Load(cmd.Flags())
	noFetch, _ := cmd.Flags().GetBool("no-fetch")
	fetchOnly, _ := cmd.Flags().GetBool("fetch-only")
	autostash, _ := cmd.Flags().GetBool("autostash")

	if fetchOnly && noFetch {
		return errors.New("--fetch-only and --no-fetch are mutually exclusive")
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()

	if !noFetch {
		remote := "--all"
		if cfg != nil && cfg.Remote != "" {
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
		switch {
		case autostash:
			if _, _, err := runner.Run(ctx, "stash", "push", "-m", "gk sync autostash"); err != nil {
				return fmt.Errorf("stash failed: %w", err)
			}
			stashed = true
		case ui.IsTerminal():
			ok, perr := promptStashDirty(ctx, runner, "gk sync autostash")
			if perr != nil {
				if errors.Is(perr, errSkipDirty) {
					return WithHint(
						errors.New("working tree has uncommitted changes"),
						hintCommand("gk sync --autostash"),
					)
				}
				return perr
			}
			stashed = ok
		default:
			return WithHint(
				errors.New("working tree has uncommitted changes"),
				hintCommand("gk sync --autostash"),
			)
		}
		if stashed {
			defer popStashBestEffort(ctx, runner)
		}
	}

	up, _ := upstreamOf(ctx, runner, currentBranch)
	rep, err := syncOne(ctx, runner, currentBranch, up, true)
	if err != nil {
		var de *DivergedError
		if errors.As(err, &de) {
			writeSyncReport(cmd.OutOrStdout(), []syncReport{{Branch: currentBranch, Status: "diverged", Upstream: up}})
			return de
		}
		return fmt.Errorf("sync %s: %w", currentBranch, err)
	}
	if up == "" {
		writeSyncReport(cmd.OutOrStdout(), []syncReport{{Branch: currentBranch, Status: "no-upstream"}})
	} else {
		writeSyncReport(cmd.OutOrStdout(), []syncReport{rep})
	}

	if stashed {
		if err := popStash(ctx, runner); err != nil {
			return fmt.Errorf("stash pop failed: %w", err)
		}
	}
	return nil
}

// syncReport is the per-branch outcome row used by the legacy report.
type syncReport struct {
	Branch   string
	Status   string // "up-to-date" | "fast-forwarded" | "no-upstream" | "diverged"
	Upstream string
	From     string
	To       string
}

// fetchRemotes runs `git fetch --prune` (with optional --all) for the legacy path.
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

// upstreamOf returns the short upstream ref for a branch, or "" when unset.
func upstreamOf(ctx context.Context, r git.Runner, branch string) (string, error) {
	stdout, _, err := r.Run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(stdout)), nil
}

// resolveShortSHA returns a 7-char abbreviated SHA via git rev-parse.
func resolveShortSHA(ctx context.Context, r git.Runner, ref string) string {
	stdout, _, err := r.Run(ctx, "rev-parse", "--short=7", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(stdout))
}

// syncOne fast-forwards a single branch to its upstream (legacy path).
func syncOne(ctx context.Context, r git.Runner, branch, upstream string, isCurrent bool) (syncReport, error) {
	if upstream == "" {
		return syncReport{Branch: branch, Status: "no-upstream"}, nil
	}
	before := resolveShortSHA(ctx, r, branch)
	if equalRefs(ctx, r, branch, upstream) {
		return syncReport{Branch: branch, Status: "up-to-date", Upstream: upstream, From: before, To: before}, nil
	}
	if _, _, err := r.Run(ctx, "merge-base", "--is-ancestor", branch, upstream); err != nil {
		return syncReport{}, &DivergedError{Code: 4, Branch: branch}
	}
	if isCurrent {
		if _, stderr, err := r.Run(ctx, "merge", "--ff-only", upstream); err != nil {
			return syncReport{}, fmt.Errorf("merge --ff-only: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	} else {
		fullSHA, _, rerr := r.Run(ctx, "rev-parse", upstream)
		if rerr != nil {
			return syncReport{}, rerr
		}
		if _, stderr, err := r.Run(ctx, "update-ref", "refs/heads/"+branch, strings.TrimSpace(string(fullSHA))); err != nil {
			return syncReport{}, fmt.Errorf("update-ref: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	}
	after := resolveShortSHA(ctx, r, branch)
	return syncReport{Branch: branch, Status: "fast-forwarded", Upstream: upstream, From: before, To: after}, nil
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

// writeSyncReport renders the legacy report table (used by --upstream-only).
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
