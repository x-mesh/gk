package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/commitlint"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "ship [status|dry-run|squash|auto|patch|minor|major]",
		Short: "Run the release ship gate and publish a tag",
		Long: `Runs the final ship gate for a release: verify a clean release branch,
infer or accept the next SemVer tag, run preflight checks, create an annotated
tag, and push the branch plus tag. Pushing the tag triggers the release workflow
when the repository has tag-based release automation.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runShip,
	}
	cmd.Flags().String("version", "", "release version to tag (v prefix optional)")
	cmd.Flags().Bool("major", false, "bump the latest tag by one major version")
	cmd.Flags().Bool("minor", false, "bump the latest tag by one minor version")
	cmd.Flags().Bool("patch", false, "bump the latest tag by one patch version")
	cmd.Flags().Bool("no-release", false, "push the branch without creating or pushing a release tag")
	cmd.Flags().Bool("push", true, "push the branch and release tag")
	cmd.Flags().Bool("skip-preflight", false, "skip configured preflight checks")
	cmd.Flags().Bool("allow-dirty", false, "allow shipping with a dirty working tree")
	cmd.Flags().Bool("allow-non-base", false, "allow release tags from a non-base branch")
	cmd.Flags().BoolP("yes", "y", false, "skip the final confirmation prompt")
	cmd.Flags().Bool("dry-run", false, "print the ship plan without tagging or pushing")
	cmd.Flags().Bool("no-fetch", false, "skip the up-front `git fetch --tags` (use a stale local view)")
	rootCmd.AddCommand(cmd)
}

type shipFlags struct {
	version       string
	bump          string
	mode          shipMode
	noRelease     bool
	push          bool
	skipPreflight bool
	allowDirty    bool
	allowNonBase  bool
	yes           bool
	dryRun        bool
	noFetch       bool
}

type shipMode string

const (
	shipModeInteractive shipMode = "interactive"
	shipModeAuto        shipMode = "auto"
	shipModeStatus      shipMode = "status"
	shipModeDryRun      shipMode = "dry-run"
	shipModeSquash      shipMode = "squash"
)

type shipDeps struct {
	Runner git.Runner
	Config *config.Config
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

type shipPlan struct {
	Branch      string
	Base        string
	Remote      string
	LatestTag   string
	NextTag     string
	Bump        string
	CommitCount int
	RepoRoot    string
	VersionFile string
	Changelog   string
}

func runShip(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cmd.Flags())
	if err != nil || cfg == nil {
		d := config.Defaults()
		cfg = &d
	}

	flags, err := readShipFlags(cmd, args)
	if err != nil {
		return err
	}
	return runShipCore(cmd.Context(), shipDeps{
		Runner: &git.ExecRunner{Dir: RepoFlag()},
		Config: cfg,
		In:     cmd.InOrStdin(),
		Out:    cmd.OutOrStdout(),
		ErrOut: cmd.ErrOrStderr(),
	}, flags)
}

func readShipFlags(cmd *cobra.Command, args []string) (shipFlags, error) {
	f := shipFlags{mode: shipModeInteractive}
	f.version, _ = cmd.Flags().GetString("version")
	f.noRelease, _ = cmd.Flags().GetBool("no-release")
	f.push, _ = cmd.Flags().GetBool("push")
	f.skipPreflight, _ = cmd.Flags().GetBool("skip-preflight")
	f.allowDirty, _ = cmd.Flags().GetBool("allow-dirty")
	f.allowNonBase, _ = cmd.Flags().GetBool("allow-non-base")
	f.yes, _ = cmd.Flags().GetBool("yes")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.noFetch, _ = cmd.Flags().GetBool("no-fetch")
	if DryRun() {
		f.dryRun = true
	}
	if len(args) == 1 {
		switch args[0] {
		case "status":
			f.mode = shipModeStatus
		case "dry-run":
			f.mode = shipModeDryRun
			f.dryRun = true
		case "squash":
			f.mode = shipModeSquash
			f.noRelease = true
			f.push = false
		case "auto":
			f.mode = shipModeAuto
			f.yes = true
		case "patch", "minor", "major":
			f.bump = args[0]
		default:
			return f, fmt.Errorf("ship: unknown mode %q", args[0])
		}
	}

	var bumps []string
	for _, name := range []string{"major", "minor", "patch"} {
		on, _ := cmd.Flags().GetBool(name)
		if on {
			bumps = append(bumps, name)
		}
	}
	if len(bumps) > 1 {
		return f, fmt.Errorf("ship: choose only one of --major, --minor, --patch")
	}
	if len(bumps) == 1 {
		if f.bump != "" && f.bump != bumps[0] {
			return f, fmt.Errorf("ship: choose only one bump source")
		}
		f.bump = bumps[0]
	}
	if f.version != "" && f.bump != "" {
		return f, fmt.Errorf("ship: --version cannot be combined with --%s", f.bump)
	}
	return f, nil
}

func runShipCore(ctx context.Context, deps shipDeps, flags shipFlags) error {
	if deps.Out == nil {
		deps.Out = io.Discard
	}
	if deps.ErrOut == nil {
		deps.ErrOut = io.Discard
	}
	if deps.In == nil {
		deps.In = strings.NewReader("")
	}
	if deps.Config == nil {
		d := config.Defaults()
		deps.Config = &d
	}

	plan, err := buildShipPlan(ctx, deps.Runner, deps.Config, flags)
	if err != nil {
		return err
	}
	if flags.mode == shipModeStatus {
		renderShipStatus(deps.Out, plan)
		return nil
	}
	renderShipPlan(deps.Out, plan, flags)

	if flags.dryRun || flags.mode == shipModeDryRun {
		return nil
	}

	if flags.mode == shipModeSquash {
		if err := confirmShip(deps, plan, flags); err != nil {
			return err
		}
		return runShipSquash(ctx, deps.Runner, plan)
	}

	if !flags.skipPreflight {
		cleaned, err := autoCleanShipCommitLint(ctx, deps, plan)
		if err != nil {
			return err
		}
		if cleaned {
			fmt.Fprintln(deps.Out, color.New(color.FgYellow).Sprint("ship: cleaned release commits before preflight"))
			plan, err = buildShipPlan(ctx, deps.Runner, deps.Config, flags)
			if err != nil {
				return err
			}
			renderShipPlan(deps.Out, plan, flags)
		}
		if err := runShipPreflight(ctx, deps, plan, flags); err != nil {
			return err
		}
	}

	if err := confirmShip(deps, plan, flags); err != nil {
		return err
	}

	changed, err := applyShipReleaseFiles(plan)
	if err != nil {
		return err
	}
	if changed {
		if _, stderr, err := deps.Runner.Run(ctx, "add", "-A"); err != nil {
			return fmt.Errorf("ship: stage release files: %s: %w", strings.TrimSpace(string(stderr)), err)
		}
		if _, stderr, err := deps.Runner.Run(ctx, "commit", "-m", "release: "+plan.NextTag); err != nil {
			return fmt.Errorf("ship: release commit: %s: %w", strings.TrimSpace(string(stderr)), err)
		}
		ok := color.New(color.FgGreen, color.Bold).SprintFunc()
		tag := color.New(color.FgMagenta, color.Bold).SprintFunc()
		fmt.Fprintf(deps.Out, "%s committed release metadata: %s\n", ok("✓"), tag(plan.NextTag))
	}

	if !flags.noRelease {
		if _, stderr, err := deps.Runner.Run(ctx, "tag", "-a", plan.NextTag, "-m", "Release "+plan.NextTag); err != nil {
			return fmt.Errorf("ship: create tag: %s: %w", strings.TrimSpace(string(stderr)), err)
		}
		ok := color.New(color.FgGreen, color.Bold).SprintFunc()
		tag := color.New(color.FgMagenta, color.Bold).SprintFunc()
		fmt.Fprintf(deps.Out, "%s tagged %s\n", ok("✓"), tag(plan.NextTag))
	}

	if flags.push {
		if err := runShipPush(ctx, deps.Runner, deps.Out, deps.ErrOut, plan, flags); err != nil {
			return err
		}
	}

	header := color.New(color.FgCyan, color.Bold).SprintFunc()
	good := color.New(color.FgGreen, color.Bold).SprintFunc()
	tag := color.New(color.FgMagenta, color.Bold).SprintFunc()
	fmt.Fprintln(deps.Out, header("─── Ship complete ────────────────────────────"))
	fmt.Fprintf(deps.Out, "  %s shipped %s on %s\n", good("✓"), tag(plan.NextTag), color.New(color.Bold).Sprint(plan.Branch))
	if !flags.noRelease && flags.push {
		fmt.Fprintf(deps.Out, "  %s release workflow triggered by tag push: %s\n", good("→"), tag(plan.NextTag))
	}
	return nil
}

func buildShipPlan(ctx context.Context, r git.Runner, cfg *config.Config, flags shipFlags) (shipPlan, error) {
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}

	// Sync remote tags up front so the next-tag inference and the
	// duplicate-tag check both see whatever someone else may have
	// already pushed. Without this, a stale local view bumps to
	// (e.g.) v0.2.0 only to be rejected later when push tries to
	// publish a tag that already exists on origin. Failures here are
	// not fatal — offline / restricted setups still need to ship.
	if !flags.noFetch {
		stop := ui.StartBubbleSpinner(fmt.Sprintf("ship: fetching tags from %s", remote))
		_, _, fetchErr := r.Run(ctx, "fetch", "--tags", "--prune-tags", remote)
		stop()
		if fetchErr != nil {
			Dbg("ship: fetch --tags failed (continuing): %v", fetchErr)
		}
	}

	statusOut, _, err := r.Run(ctx, "status", "--porcelain")
	if err != nil {
		return shipPlan{}, fmt.Errorf("ship: git status: %w", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" && !flags.allowDirty {
		return shipPlan{}, fmt.Errorf("ship: working tree is dirty; commit/stash changes or pass --allow-dirty")
	}

	branchOut, _, err := r.Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return shipPlan{}, fmt.Errorf("ship: current branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" || branch == "HEAD" {
		return shipPlan{}, fmt.Errorf("ship: refusing to ship from detached HEAD")
	}

	client := git.NewClient(r)
	base := cfg.BaseBranch
	if base == "" {
		if detected, derr := client.DefaultBranch(ctx, remote); derr == nil {
			base = detected
		}
	}
	if base != "" && branch != base && !flags.allowNonBase {
		return shipPlan{}, fmt.Errorf("ship: current branch %q is not base branch %q; pass --allow-non-base to override", branch, base)
	}

	repoRoot := ""
	if out, _, err := r.Run(ctx, "rev-parse", "--show-toplevel"); err == nil {
		repoRoot = strings.TrimSpace(string(out))
	}

	latestTag := "v0.0.0"
	if out, _, err := r.Run(ctx, "describe", "--tags", "--abbrev=0"); err == nil {
		if tag := strings.TrimSpace(string(out)); tag != "" {
			latestTag = tag
		}
	}

	rangeSpec := "HEAD"
	if latestTag != "v0.0.0" {
		rangeSpec = latestTag + "..HEAD"
	}
	logOut, _, err := r.Run(ctx, "log", "--format=%B%x1e", rangeSpec)
	if err != nil {
		return shipPlan{}, fmt.Errorf("ship: git log: %w", err)
	}
	commits := parseShipCommitMessages(string(logOut))
	if len(commits) == 0 && !flags.noRelease {
		return shipPlan{}, fmt.Errorf("ship: no commits since %s", latestTag)
	}

	bump := flags.bump
	if bump == "" {
		bump = inferShipBump(commits)
	}

	nextTag := ""
	if !flags.noRelease {
		if flags.version != "" {
			nextTag = normalizeShipVersion(flags.version)
		} else {
			nextTag, err = bumpShipVersion(latestTag, bump)
			if err != nil {
				return shipPlan{}, err
			}
		}
		if err := validateShipVersion(nextTag); err != nil {
			return shipPlan{}, err
		}
		if _, _, err := r.Run(ctx, "rev-parse", "--verify", "refs/tags/"+nextTag); err == nil {
			return shipPlan{}, WithHint(
				fmt.Errorf("ship: tag %s already exists locally", nextTag),
				"delete with `git tag -d "+nextTag+"` (only if it hasn't been pushed) or pick a different version with --version")
		}
		// Also check the remote — catching this in plan saves the user
		// from a mid-ship push rejection that leaves the local repo
		// with an orphan tag and main already pushed.
		if remoteHasTag(ctx, r, remote, nextTag) {
			return shipPlan{}, WithHint(
				fmt.Errorf("ship: tag %s already exists on remote %q", nextTag, remote),
				"someone else already shipped "+nextTag+" — pull first, then bump (e.g. `gk ship --patch`) or pick a specific version with --version")
		}
	}

	versionFile, changelog := detectShipReleaseFiles(repoRoot)
	return shipPlan{
		Branch:      branch,
		Base:        base,
		Remote:      remote,
		LatestTag:   latestTag,
		NextTag:     nextTag,
		Bump:        bump,
		CommitCount: len(commits),
		RepoRoot:    repoRoot,
		VersionFile: versionFile,
		Changelog:   changelog,
	}, nil
}

// remoteHasTag returns true when the remote already advertises the
// given tag. Empty / errored ls-remote output is treated as "no" — the
// later push will surface real network failures, this check is only a
// soft guard to fail fast on the duplicate-tag case.
func remoteHasTag(ctx context.Context, r git.Runner, remote, tag string) bool {
	if remote == "" || tag == "" {
		return false
	}
	out, _, err := r.Run(ctx, "ls-remote", "--tags", "--exit-code", remote, "refs/tags/"+tag)
	if err != nil {
		// `ls-remote --exit-code` returns 2 when no matching ref is
		// found; either way "absent" is the safe interpretation here.
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

func runShipPreflight(ctx context.Context, deps shipDeps, plan shipPlan, flags shipFlags) error {
	if len(deps.Config.Preflight.Steps) == 0 {
		fmt.Fprintln(deps.Out, "preflight: no steps configured")
		return nil
	}
	for _, step := range deps.Config.Preflight.Steps {
		name := step.Name
		if name == "" {
			name = step.Command
		}
		if flags.dryRun {
			fmt.Fprintf(deps.Out, "  %-22s %s\n", name, resolveDescription(step.Command))
			continue
		}
		ok := color.New(color.FgGreen, color.Bold).SprintFunc()
		if step.Command == "commit-lint" {
			if err := runShipCommitLint(ctx, deps.Runner, deps.Config, plan); err != nil {
				return fmt.Errorf("ship: preflight failed at step %q: %w", name, err)
			}
			fmt.Fprintf(deps.Out, "  %s %-22s\n", ok("✓"), name)
			continue
		}
		if err := runStep(ctx, deps.Runner, deps.Config, step); err != nil {
			return fmt.Errorf("ship: preflight failed at step %q: %w", name, err)
		}
		fmt.Fprintf(deps.Out, "  %s %-22s\n", ok("✓"), name)
	}
	return nil
}

func autoCleanShipCommitLint(ctx context.Context, deps shipDeps, plan shipPlan) (bool, error) {
	if !shipHasPreflightStep(deps.Config, "commit-lint") {
		return false, nil
	}
	if err := runShipCommitLint(ctx, deps.Runner, deps.Config, plan); err == nil {
		return false, nil
	}
	fmt.Fprintln(deps.Out, "ship: commit-lint failed in release range; squashing local release commits")
	if err := runShipSquash(ctx, deps.Runner, plan); err != nil {
		return false, WithHint(
			fmt.Errorf("ship: auto cleanup failed: %w", err),
			"choose one:\n"+
				"    • gk ship --skip-preflight     # ship now, leave the lint violations in history\n"+
				"    • gk ship squash               # rewrite local release commits (force-push needed if already pushed)\n"+
				"    • gk commit --amend            # tidy the offending commit messages by hand, then retry `gk ship`",
		)
	}
	return true, nil
}

func shipHasPreflightStep(cfg *config.Config, command string) bool {
	if cfg == nil {
		return false
	}
	for _, step := range cfg.Preflight.Steps {
		if step.Command == command {
			return true
		}
	}
	return false
}

func runShipCommitLint(ctx context.Context, r git.Runner, cfg *config.Config, plan shipPlan) error {
	rangeSpec := "HEAD"
	if plan.LatestTag != "" && plan.LatestTag != "v0.0.0" {
		rangeSpec = plan.LatestTag + "..HEAD"
	}
	stdout, stderr, err := r.Run(ctx, "log", "--format=%H%x00%B%x1e", rangeSpec)
	if err != nil {
		return fmt.Errorf("git log: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	rules := commitlint.Rules{
		AllowedTypes:     cfg.Commit.Types,
		ScopeRequired:    cfg.Commit.ScopeRequired,
		MaxSubjectLength: cfg.Commit.MaxSubjectLength,
	}
	fails := 0
	var first string
	for _, rec := range strings.Split(strings.TrimRight(string(stdout), "\x1e\n"), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x00", 2)
		if len(parts) < 2 {
			continue
		}
		sha := parts[0]
		if len(sha) > 8 {
			sha = sha[:8]
		}
		issues := commitlint.Lint(commitlint.Parse(parts[1]), rules)
		if len(issues) == 0 {
			continue
		}
		fails++
		if first == "" {
			first = fmt.Sprintf("%s [%s] %s", sha, issues[0].Code, issues[0].Message)
		}
	}
	if fails > 0 {
		return fmt.Errorf("%d release commit message(s) failed linting; first: %s", fails, first)
	}
	return nil
}

func confirmShip(deps shipDeps, plan shipPlan, flags shipFlags) error {
	if flags.yes {
		return nil
	}
	if !ui.IsTerminal() {
		return fmt.Errorf("ship: confirmation required in non-interactive mode; pass --yes or --dry-run")
	}

	target := "branch only"
	if flags.mode == shipModeSquash {
		target = "squash commits since " + plan.LatestTag
	}
	if !flags.noRelease {
		target = plan.NextTag
	}
	fmt.Fprintf(deps.ErrOut, "ship %s from %s and push=%v? [y/N] ", target, plan.Branch, flags.push)
	sc := bufio.NewScanner(deps.In)
	if !sc.Scan() {
		return fmt.Errorf("ship: confirmation aborted")
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("ship: aborted")
	}
	return nil
}

func runShipPush(ctx context.Context, r git.Runner, out, errOut io.Writer, plan shipPlan, flags shipFlags) error {
	findings, err := scanCommitsToPush(ctx, r, plan.Remote, plan.Branch)
	if err != nil {
		return fmt.Errorf("ship: secret scan: %w", err)
	}
	if len(findings) > 0 {
		renderShipFindings(errOut, findings)
		return fmt.Errorf("ship: aborting push due to %d secret finding(s)", len(findings))
	}

	stdout, stderr, err := r.Run(ctx, "push", plan.Remote, plan.Branch)
	if err != nil {
		fmt.Fprint(errOut, string(stderr))
		return fmt.Errorf("ship: push branch: %w", err)
	}
	fmt.Fprint(out, string(stdout))
	fmt.Fprint(errOut, string(stderr))

	if !flags.noRelease {
		stdout, stderr, err = r.Run(ctx, "push", plan.Remote, plan.NextTag)
		if err != nil {
			fmt.Fprint(errOut, string(stderr))
			return fmt.Errorf("ship: push tag: %w", err)
		}
		fmt.Fprint(out, string(stdout))
		fmt.Fprint(errOut, string(stderr))
	}
	return nil
}

func renderShipPlan(w io.Writer, plan shipPlan, flags shipFlags) {
	header := color.New(color.FgCyan, color.Bold).SprintFunc()
	label := color.New(color.Faint).SprintFunc()
	value := color.New(color.FgWhite, color.Bold).SprintFunc()
	tag := color.New(color.FgMagenta, color.Bold).SprintFunc()
	skip := color.New(color.FgYellow).SprintFunc()

	fmt.Fprintln(w, header("─── Ship plan ────────────────────────────────"))
	fmt.Fprintf(w, "  %s   %s\n", label("Branch:   "), value(plan.Branch))
	if plan.Base != "" {
		fmt.Fprintf(w, "  %s   %s\n", label("Base:     "), value(plan.Base))
	}
	fmt.Fprintf(w, "  %s   %s\n", label("Remote:   "), value(plan.Remote))
	fmt.Fprintf(w, "  %s   %s\n", label("Commits:  "), value(fmt.Sprintf("%d since %s", plan.CommitCount, plan.LatestTag)))
	if flags.noRelease {
		fmt.Fprintf(w, "  %s   %s\n", label("Release:  "), skip("no"))
	} else {
		fmt.Fprintf(w, "  %s   %s\n", label("Bump:     "), value(plan.Bump))
		fmt.Fprintf(w, "  %s   %s\n", label("Next tag: "), tag(plan.NextTag))
	}
	if plan.VersionFile != "" {
		fmt.Fprintf(w, "  %s   %s\n", label("Version:  "), value(plan.VersionFile))
	} else {
		fmt.Fprintf(w, "  %s   %s\n", label("Version:  "), label("tag-only"))
	}
	if plan.Changelog != "" {
		fmt.Fprintf(w, "  %s   %s\n", label("Changelog:"), label(plan.Changelog))
	}
	pushVal := value("true")
	if !flags.push {
		pushVal = skip("false")
	}
	fmt.Fprintf(w, "  %s   %s\n", label("Push:     "), pushVal)
	if flags.skipPreflight {
		fmt.Fprintf(w, "  %s   %s\n", label("Preflight:"), skip("skipped"))
	}
}

func renderShipStatus(w io.Writer, plan shipPlan) {
	fmt.Fprintln(w, "Ship status")
	fmt.Fprintf(w, "  Branch:    %s\n", plan.Branch)
	fmt.Fprintf(w, "  Latest:    %s\n", plan.LatestTag)
	fmt.Fprintf(w, "  Commits:   %d\n", plan.CommitCount)
	if plan.NextTag != "" {
		fmt.Fprintf(w, "  Next tag:  %s (%s)\n", plan.NextTag, plan.Bump)
	}
	if plan.VersionFile != "" {
		fmt.Fprintf(w, "  Version:   %s\n", plan.VersionFile)
	} else {
		fmt.Fprintln(w, "  Version:   tag-only")
	}
	if plan.Changelog != "" {
		fmt.Fprintf(w, "  Changelog: %s\n", plan.Changelog)
	}
}

func renderShipFindings(w io.Writer, findings []secrets.Finding) {
	fmt.Fprintln(w, "potential secrets detected:")
	for _, f := range findings {
		fmt.Fprintf(w, "  [%s] line %d: %s\n", f.Kind, f.Line, f.Sample)
	}
}

func runShipSquash(ctx context.Context, r git.Runner, plan shipPlan) error {
	if plan.LatestTag == "v0.0.0" {
		return fmt.Errorf("ship: cannot squash without a previous tag")
	}
	if branchHasUpstream(ctx, r, plan.Branch) {
		releaseOut, _, err := r.Run(ctx, "log", "--format=%H", plan.LatestTag+"..HEAD")
		if err != nil {
			return fmt.Errorf("ship: list release commits: %w", err)
		}
		aheadOut, _, err := r.Run(ctx, "log", "--format=%H", plan.Branch+"@{upstream}..HEAD")
		if err != nil {
			return fmt.Errorf("ship: list unpushed commits: %w", err)
		}
		if !shipCommitsSubset(linesSet(string(releaseOut)), linesSet(string(aheadOut))) {
			return fmt.Errorf("ship: refusing to squash commits that may already be pushed")
		}
	}

	preOut, _, err := r.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("ship: capture pre-squash HEAD: %w", err)
	}
	pre := strings.TrimSpace(string(preOut))
	if _, stderr, err := r.Run(ctx, "reset", "--soft", plan.LatestTag); err != nil {
		return fmt.Errorf("ship: squash reset: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	msg := "chore: release changes"
	switch plan.Bump {
	case "major":
		msg = "feat!: release changes"
	case "minor":
		msg = "feat: release changes"
	}
	if _, stderr, err := r.Run(ctx, "commit", "-m", msg); err != nil {
		return fmt.Errorf("ship: squash commit: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if diffOut, _, err := r.Run(ctx, "diff", pre, "HEAD"); err != nil {
		return fmt.Errorf("ship: verify squash: %w", err)
	} else if strings.TrimSpace(string(diffOut)) != "" {
		_, _, _ = r.Run(ctx, "reset", "--hard", pre)
		return fmt.Errorf("ship: squash verification failed; restored %s", pre)
	}
	return nil
}

func detectShipReleaseFiles(repoRoot string) (versionFile, changelog string) {
	if repoRoot == "" {
		return "", ""
	}
	for _, name := range []string{"VERSION", "package.json", "marketplace.json"} {
		path := filepath.Join(repoRoot, name)
		if _, err := os.Stat(path); err == nil {
			versionFile = path
			break
		}
	}
	if path := filepath.Join(repoRoot, "CHANGELOG.md"); fileExists(path) {
		changelog = path
	}
	return versionFile, changelog
}

func applyShipReleaseFiles(plan shipPlan) (bool, error) {
	if plan.NextTag == "" {
		return false, nil
	}
	changed := false
	version := strings.TrimPrefix(plan.NextTag, "v")
	if plan.VersionFile != "" {
		ok, err := bumpShipVersionFile(plan.VersionFile, version)
		if err != nil {
			return false, err
		}
		changed = changed || ok
	}
	if plan.Changelog != "" {
		ok, err := promoteShipChangelog(plan.Changelog, version)
		if err != nil {
			return false, err
		}
		changed = changed || ok
	}
	return changed, nil
}

func bumpShipVersionFile(path, version string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("ship: read version file: %w", err)
	}
	before := string(b)
	var after string
	switch filepath.Base(path) {
	case "VERSION":
		after = version + "\n"
	case "package.json", "marketplace.json":
		re := regexp.MustCompile(`(?m)("version"\s*:\s*")([^"]+)(")`)
		if !re.MatchString(before) {
			return false, fmt.Errorf("ship: %s has no version field", filepath.Base(path))
		}
		after = re.ReplaceAllString(before, `${1}`+version+`${3}`)
	default:
		return false, nil
	}
	if after == before {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		return false, fmt.Errorf("ship: write version file: %w", err)
	}
	return true, nil
}

func promoteShipChangelog(path, version string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("ship: read changelog: %w", err)
	}
	before := string(b)
	marker := "## [Unreleased]"
	idx := strings.Index(before, marker)
	if idx < 0 {
		return false, nil
	}
	afterMarker := idx + len(marker)
	rest := before[afterMarker:]
	nextIdx := strings.Index(rest, "\n## [")
	if nextIdx < 0 {
		nextIdx = len(rest)
	}
	unreleasedBody := rest[:nextIdx]
	if strings.TrimSpace(unreleasedBody) == "" {
		return false, nil
	}
	date := time.Now().Format("2006-01-02")
	replacement := marker + "\n\n## [" + version + "] - " + date + unreleasedBody
	after := before[:idx] + replacement + rest[nextIdx:]
	if after == before {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		return false, fmt.Errorf("ship: write changelog: %w", err)
	}
	return true, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func linesSet(raw string) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			set[line] = true
		}
	}
	return set
}

func shipCommitsSubset(needles, haystack map[string]bool) bool {
	for sha := range needles {
		if !haystack[sha] {
			return false
		}
	}
	return true
}

func parseShipCommitMessages(raw string) []string {
	parts := strings.Split(raw, "\x1e")
	commits := make([]string, 0, len(parts))
	for _, part := range parts {
		msg := strings.TrimSpace(part)
		if msg != "" {
			commits = append(commits, msg)
		}
	}
	return commits
}

func inferShipBump(commits []string) string {
	bump := "patch"
	for _, commit := range commits {
		firstLine := strings.TrimSpace(strings.Split(commit, "\n")[0])
		if strings.Contains(firstLine, "!:") || strings.Contains(commit, "BREAKING CHANGE:") {
			return "major"
		}
		if strings.HasPrefix(firstLine, "feat:") || strings.HasPrefix(firstLine, "feat(") {
			bump = "minor"
		}
	}
	return bump
}

func normalizeShipVersion(version string) string {
	version = strings.TrimSpace(version)
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

var shipVersionPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

func validateShipVersion(version string) error {
	if !shipVersionPattern.MatchString(version) {
		return fmt.Errorf("ship: version must look like vMAJOR.MINOR.PATCH")
	}
	return nil
}

func bumpShipVersion(latestTag, bump string) (string, error) {
	version := strings.TrimPrefix(strings.TrimSpace(latestTag), "v")
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("ship: latest tag %q is not SemVer; pass --version", latestTag)
	}
	nums := make([]int, 3)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return "", fmt.Errorf("ship: latest tag %q is not SemVer; pass --version", latestTag)
		}
		nums[i] = n
	}

	switch bump {
	case "major":
		nums[0]++
		nums[1] = 0
		nums[2] = 0
	case "minor":
		nums[1]++
		nums[2] = 0
	case "patch":
		nums[2]++
	default:
		return "", fmt.Errorf("ship: unknown bump %q", bump)
	}
	return fmt.Sprintf("v%d.%d.%d", nums[0], nums[1], nums[2]), nil
}
