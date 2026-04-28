package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "merge <target|source>",
		Short: "Precheck and merge a target branch into the current branch",
		Long: `Runs a dry merge precheck before invoking git merge.

Direction matches git merge: gk merge <target> merges <target> into the
current branch. It does not land the current branch into <target>.

To land the current branch into another checked-out worktree, use
gk merge --into <branch>. This finds the worktree for <branch> and merges
the current branch into it.

By default gk refuses to start with tracked working-tree changes. Use
--autostash to stash tracked changes before merging and pop them afterwards.`,
		Args: validateMergeArgs,
		RunE: runMerge,
	}
	cmd.Flags().Bool("ff-only", false, "allow only fast-forward merges")
	cmd.Flags().Bool("no-ff", false, "create a merge commit even when fast-forward is possible")
	cmd.Flags().Bool("no-commit", false, "perform the merge but stop before creating the commit")
	cmd.Flags().Bool("squash", false, "squash changes from target without creating a merge commit")
	cmd.Flags().Bool("skip-precheck", false, "skip the merge-tree conflict precheck")
	cmd.Flags().Bool("autostash", false, "stash tracked changes before merge and pop afterwards")
	cmd.Flags().Bool("no-ai", false, "skip the merge plan summary")
	cmd.Flags().Bool("plan-only", false, "print the merge plan without running git merge")
	cmd.Flags().String("into", "", "merge the source branch into this branch's checked-out worktree")
	cmd.Flags().String("provider", "", "override ai.provider for the merge plan")
	rootCmd.AddCommand(cmd)
}

type mergeFlags struct {
	ffOnly       bool
	noFF         bool
	noCommit     bool
	squash       bool
	skipPrecheck bool
	autostash    bool
	noAI         bool
	planOnly     bool
	into         string
	provider     string
}

type mergeDeps struct {
	Runner      git.Runner
	Config      *config.Config
	Provider    provider.Provider
	ProviderErr error
	Confirm     func(string, bool) (bool, error)
	Out         io.Writer
	ErrOut      io.Writer
}

func runMerge(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cmd.Flags())
	if err != nil || cfg == nil {
		d := config.Defaults()
		cfg = &d
	}
	flags := readMergeFlags(cmd)
	var prov provider.Provider
	var providerErr error
	if !flags.noAI {
		ai := cfg.AI
		if flags.provider != "" {
			ai.Provider = flags.provider
		}
		prov, providerErr = resolveMergePlanProvider(cmd.Context(), ai)
		if providerErr != nil {
			Dbg("merge plan provider unavailable: %v", providerErr)
		}
	}
	deps := mergeDeps{
		Runner:      &git.ExecRunner{Dir: RepoFlag()},
		Config:      cfg,
		Provider:    prov,
		ProviderErr: providerErr,
		Confirm:     ui.Confirm,
		Out:         os.Stdout,
		ErrOut:      os.Stderr,
	}
	if flags.into != "" {
		err = runMergeInto(cmd.Context(), deps, args, flags, func(path string) git.Runner {
			return &git.ExecRunner{Dir: path}
		})
	} else {
		err = runMergeCore(cmd.Context(), deps, args[0], flags)
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		if h := HintFrom(err); h != "" {
			fmt.Fprintln(os.Stderr, "  hint: "+h)
		}
		os.Exit(ce.Code)
	}
	return err
}

func readMergeFlags(cmd *cobra.Command) mergeFlags {
	ffOnly, _ := cmd.Flags().GetBool("ff-only")
	noFF, _ := cmd.Flags().GetBool("no-ff")
	noCommit, _ := cmd.Flags().GetBool("no-commit")
	squash, _ := cmd.Flags().GetBool("squash")
	skipPrecheck, _ := cmd.Flags().GetBool("skip-precheck")
	autostash, _ := cmd.Flags().GetBool("autostash")
	noAI, _ := cmd.Flags().GetBool("no-ai")
	planOnly, _ := cmd.Flags().GetBool("plan-only")
	into, _ := cmd.Flags().GetString("into")
	providerName, _ := cmd.Flags().GetString("provider")
	return mergeFlags{
		ffOnly:       ffOnly,
		noFF:         noFF,
		noCommit:     noCommit,
		squash:       squash,
		skipPrecheck: skipPrecheck,
		autostash:    autostash,
		noAI:         noAI,
		planOnly:     planOnly,
		into:         into,
		provider:     providerName,
	}
}

func validateMergeArgs(cmd *cobra.Command, args []string) error {
	into, _ := cmd.Flags().GetString("into")
	if into != "" {
		if len(args) > 1 {
			return fmt.Errorf("merge: --into accepts at most one source branch")
		}
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("accepts 1 arg(s), received %d", len(args))
	}
	return nil
}

func runMergeCore(ctx context.Context, deps mergeDeps, target string, flags mergeFlags) error {
	if err := guardRef(target); err != nil {
		return fmt.Errorf("invalid target: %w", err)
	}
	if deps.Runner == nil {
		deps.Runner = &git.ExecRunner{Dir: RepoFlag()}
	}
	if deps.Config == nil {
		d := config.Defaults()
		deps.Config = &d
	}
	client := git.NewClient(deps.Runner)
	current := currentMergeBranch(ctx, client)

	if err := validateMergeFlags(flags); err != nil {
		return err
	}
	if _, _, err := deps.Runner.Run(ctx, "rev-parse", "--verify", target+"^{commit}"); err != nil {
		return WithHint(
			fmt.Errorf("unknown target %q: not a commit", target),
			"run `git fetch` if the ref lives on a remote, or spell-check the branch name",
		)
	}

	stashed := false

	var conflicts []string
	if !flags.skipPrecheck {
		baseOut, _, err := deps.Runner.Run(ctx, "merge-base", "HEAD", target)
		if err != nil {
			popMergeStashBestEffort(ctx, deps.Runner, stashed)
			return fmt.Errorf("cannot find merge-base between HEAD and %s", target)
		}
		base := strings.TrimSpace(string(baseOut))
		conflicts, err = scanMergeConflicts(ctx, deps.Runner, base, "HEAD", target)
		if err != nil {
			popMergeStashBestEffort(ctx, deps.Runner, stashed)
			return fmt.Errorf("merge-tree scan: %w", err)
		}
	}

	if !flags.noAI || flags.planOnly {
		if err := renderMergePlan(ctx, deps, target, current, conflicts); err != nil && deps.ErrOut != nil {
			fmt.Fprintf(deps.ErrOut, "merge plan unavailable: %v\n", err)
		}
	}
	if len(conflicts) > 0 {
		popMergeStashBestEffort(ctx, deps.Runner, stashed)
		return WithHint(
			fmt.Errorf("precheck found %d conflict(s) merging %s", len(conflicts), target),
			hintCommand("gk precheck "+target),
		)
	}
	if flags.planOnly {
		return nil
	}

	dirty, err := client.IsDirty(ctx)
	if err != nil {
		return err
	}
	if dirty {
		if !flags.autostash {
			return WithHint(
				errors.New("working tree has tracked changes"),
				hintCommand("gk merge "+target+" --autostash"),
			)
		}
		if _, _, err := deps.Runner.Run(ctx, "stash", "push", "-m", "gk merge autostash"); err != nil {
			return fmt.Errorf("stash failed: %w", err)
		}
		stashed = true
	}

	preHEAD := headRev(ctx, deps.Runner)
	stdout, stderr, err := deps.Runner.Run(ctx, mergeArgs(target, flags)...)
	if err != nil {
		combined := string(stdout) + string(stderr)
		if strings.Contains(combined, "CONFLICT") || strings.Contains(combined, "Merge conflict") {
			fmt.Fprintln(os.Stderr, "conflict detected. resolve manually, then `gk continue` or `gk abort`.")
			return &ConflictError{Code: 3, Stashed: stashed}
		}
		popMergeStashBestEffort(ctx, deps.Runner, stashed)
		return fmt.Errorf("merge: %w\n%s", err, strings.TrimSpace(combined))
	}

	postHEAD := headRev(ctx, deps.Runner)
	if deps.ErrOut != nil {
		renderMergeSummary(ctx, deps.ErrOut, deps.Runner, preHEAD, postHEAD, target, current, flags)
	}
	if stashed {
		if err := popStash(ctx, deps.Runner); err != nil {
			return fmt.Errorf("stash pop failed: %w", err)
		}
	}
	return nil
}

func runMergeInto(ctx context.Context, deps mergeDeps, args []string, flags mergeFlags, runnerForPath func(string) git.Runner) error {
	if flags.into == "" {
		return fmt.Errorf("merge: --into requires a receiver branch")
	}
	if err := guardRef(flags.into); err != nil {
		return fmt.Errorf("invalid --into branch: %w", err)
	}
	if deps.Runner == nil {
		deps.Runner = &git.ExecRunner{Dir: RepoFlag()}
	}
	current, currentErr := git.NewClient(deps.Runner).CurrentBranch(ctx)
	current = strings.TrimSpace(current)
	source := ""
	sourceDefaulted := false
	if len(args) > 0 {
		source = args[0]
	} else {
		if currentErr != nil || current == "" {
			return WithHint(
				fmt.Errorf("merge: --into needs a source branch when HEAD is detached"),
				"pass the source explicitly, e.g. `gk merge feature --into "+flags.into+"`",
			)
		}
		source = current
		sourceDefaulted = true
	}
	if err := guardRef(source); err != nil {
		return fmt.Errorf("invalid source branch: %w", err)
	}
	if source == flags.into {
		return fmt.Errorf("merge: source and --into branch are both %q", source)
	}
	if (sourceDefaulted || source == current) && !flags.planOnly {
		dirty, err := git.NewClient(deps.Runner).IsDirty(ctx)
		if err != nil {
			return err
		}
		if dirty {
			ok, promptErr := confirmSourceWipCommit(deps, source, flags.into)
			if promptErr != nil {
				return WithHint(
					fmt.Errorf("source worktree has tracked changes not included in branch %q", source),
					"commit or stash the source worktree before `gk merge --into "+flags.into+"`",
				)
			}
			if !ok {
				return WithHint(
					fmt.Errorf("source worktree has tracked changes not included in branch %q", source),
					"commit or stash the source worktree before `gk merge --into "+flags.into+"`",
				)
			}
			if err := createWipCommit(ctx, deps.Runner, deps.Out); err != nil {
				return fmt.Errorf("source wip commit: %w", err)
			}
		}
	}

	entry, err := findWorktreeForBranch(ctx, deps.Runner, flags.into)
	if err != nil {
		return err
	}
	if runnerForPath == nil {
		runnerForPath = func(path string) git.Runner { return &git.ExecRunner{Dir: path} }
	}
	targetDeps := deps
	targetDeps.Runner = runnerForPath(entry.Path)
	targetFlags := flags
	targetFlags.into = ""
	if err := runMergeCore(ctx, targetDeps, source, targetFlags); err != nil {
		return WithHint(err, "receiver worktree: cd "+entry.Path+" or pass `--repo "+entry.Path+"` to inspect/continue")
	}
	return nil
}

func confirmSourceWipCommit(deps mergeDeps, source, receiver string) (bool, error) {
	confirm := deps.Confirm
	if confirm == nil {
		confirm = ui.Confirm
	}
	return confirm(
		fmt.Sprintf("source %q has uncommitted tracked changes. Create a WIP commit before merging into %q?", source, receiver),
		true,
	)
}

func findWorktreeForBranch(ctx context.Context, runner git.Runner, branch string) (WorktreeEntry, error) {
	stdout, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return WorktreeEntry{}, fmt.Errorf("worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	for _, entry := range parseWorktreePorcelain(string(stdout)) {
		if entry.Branch == branch && !entry.Bare {
			return entry, nil
		}
	}
	return WorktreeEntry{}, WithHint(
		fmt.Errorf("no worktree has branch %q checked out", branch),
		"create one with `gk worktree add <path> "+branch+"` or run from that branch's worktree",
	)
}

func validateMergeFlags(flags mergeFlags) error {
	if flags.ffOnly && flags.noFF {
		return fmt.Errorf("merge: --ff-only and --no-ff are mutually exclusive")
	}
	if flags.ffOnly && flags.squash {
		return fmt.Errorf("merge: --ff-only and --squash are mutually exclusive")
	}
	if flags.noFF && flags.squash {
		return fmt.Errorf("merge: --no-ff and --squash are mutually exclusive")
	}
	return nil
}

func mergeArgs(target string, flags mergeFlags) []string {
	args := []string{"merge"}
	switch {
	case flags.ffOnly:
		args = append(args, "--ff-only")
	case flags.noFF:
		args = append(args, "--no-ff")
	}
	if flags.noCommit {
		args = append(args, "--no-commit")
	}
	if flags.squash {
		args = append(args, "--squash")
	} else if !flags.noCommit {
		args = append(args, "--no-edit")
	}
	return append(args, target)
}

func renderMergeSummary(ctx context.Context, out io.Writer, runner git.Runner, pre, post, target, current string, flags mergeFlags) {
	if pre == "" || post == "" {
		return
	}
	if flags.noCommit || flags.squash {
		fmt.Fprintf(out, "merged %s into %s index/worktree (HEAD %s)\n", target, current, shortSHA(post))
		return
	}
	if pre == post {
		fmt.Fprintf(out, "%s already contains %s at %s\n", current, target, shortSHA(post))
		return
	}
	count := 0
	if n, _, err := runner.Run(ctx, "rev-list", "--count", pre+".."+post); err == nil {
		count, _ = strconv.Atoi(strings.TrimSpace(string(n)))
	}
	fmt.Fprintf(out, "merged %s into %s: %s → %s (+%d commit%s)\n", target, current, shortSHA(pre), shortSHA(post), count, plural(count))
}

func resolveMergePlanProvider(ctx context.Context, ai config.AIConfig) (provider.Provider, error) {
	if ai.Provider == "" {
		return buildFallbackChain(nil, provider.ExecRunner{})
	}
	return provider.NewProvider(ctx, provider.FactoryOptions{
		Name:   ai.Provider,
		Runner: provider.ExecRunner{},
	})
}

func renderMergePlan(ctx context.Context, deps mergeDeps, target, current string, conflicts []string) error {
	payload, commits := buildMergePlanPayload(ctx, deps.Runner, target, current, conflicts)
	reason := "AI provider unavailable or disabled"
	if deps.ProviderErr != nil {
		reason = deps.ProviderErr.Error()
	}
	text := fallbackMergePlan(target, current, conflicts, payload, reason, !NoColorFlag())
	if deps.Provider != nil {
		redacted, _, err := applyPrivacyGate(deps.Provider, payload, deps.Config.AI)
		if err != nil {
			reason = fmt.Sprintf("privacy gate: %v", err)
			text = fallbackMergePlan(target, current, conflicts, payload, reason, !NoColorFlag())
			goto writePlan
		}
		sum, ok := deps.Provider.(provider.Summarizer)
		if !ok {
			reason = fmt.Sprintf("provider %q does not support merge-plan summaries", deps.Provider.Name())
			text = fallbackMergePlan(target, current, conflicts, payload, reason, !NoColorFlag())
			goto writePlan
		}
		stopSpinner := ui.StartBubbleSpinner(fmt.Sprintf("merge plan — %s", deps.Provider.Name()))
		result, err := sum.Summarize(ctx, provider.SummarizeInput{
			Kind:    "merge-plan",
			Diff:    redacted,
			Commits: commits,
			Lang:    fallbackLang(deps.Config.AI.Lang),
		})
		stopSpinner()
		if err != nil {
			reason = fmt.Sprintf("provider %q failed: %v", deps.Provider.Name(), err)
			text = fallbackMergePlan(target, current, conflicts, payload, reason, !NoColorFlag())
		} else if strings.TrimSpace(result.Text) != "" {
			providerName := deps.Provider.Name()
			if result.Provider != "" {
				providerName = result.Provider
			}
			text = renderAIMergePlanHeader(target, current, providerName, len(conflicts), !NoColorFlag()) + "\n" + cleanMergePlanSummary(result.Text)
		}
	}
writePlan:
	if deps.ErrOut != nil {
		fmt.Fprintln(deps.ErrOut, text)
	}
	return nil
}

func buildMergePlanPayload(ctx context.Context, r git.Runner, target, current string, conflicts []string) (string, []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Operation: git merge %s\n", target)
	fmt.Fprintf(&b, "Current branch receiving changes: %s\n", current)
	fmt.Fprintf(&b, "Target: %s\n", target)
	fmt.Fprintf(&b, "Direction: %s -> %s\n", target, current)
	fmt.Fprintf(&b, "Important: this merges %s into %s; it does NOT merge %s into %s.\n", target, current, current, target)
	if len(conflicts) == 0 {
		fmt.Fprintln(&b, "Precheck: clean")
	} else {
		fmt.Fprintf(&b, "Precheck: %d conflict(s)\n", len(conflicts))
		fmt.Fprintln(&b, "Conflicts:")
		for _, path := range conflicts {
			fmt.Fprintf(&b, "- %s\n", path)
		}
	}

	commitLines := []string{}
	if out, _, err := r.Run(ctx, "log", "--oneline", "HEAD.."+target); err == nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			commitLines = strings.Split(trimmed, "\n")
			fmt.Fprintln(&b, "\nIncoming commits:")
			for _, line := range commitLines {
				fmt.Fprintf(&b, "- %s\n", line)
			}
		}
	}
	if out, _, err := r.Run(ctx, "diff", "--stat", "HEAD.."+target); err == nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			fmt.Fprintln(&b, "\nDiff stat:")
			b.WriteString(trimmed)
			fmt.Fprintln(&b)
		}
	}
	if out, _, err := r.Run(ctx, "diff", "--name-status", "HEAD.."+target); err == nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			fmt.Fprintln(&b, "\nChanged files:")
			b.WriteString(trimmed)
			fmt.Fprintln(&b)
		}
	}
	return b.String(), commitLines
}

func fallbackMergePlan(target, current string, conflicts []string, payload string, reason string, colorize bool) string {
	var b strings.Builder
	title := "Merge plan (local): "
	cleanYes := "yes"
	cleanNo := "no"
	if colorize {
		title = color.New(color.FgCyan, color.Bold).Sprint(title)
		cleanYes = color.New(color.FgGreen).Sprint(cleanYes)
		cleanNo = color.New(color.FgRed).Sprint(cleanNo)
	}
	fmt.Fprintf(&b, "%s%s -> %s\n", title, target, current)
	fmt.Fprintf(&b, "Source: local git facts (%s)\n", reason)
	fmt.Fprintf(&b, "Direction: merge %s into %s\n", target, current)
	if len(conflicts) == 0 {
		fmt.Fprintf(&b, "Clean: %s\n", cleanYes)
	} else {
		fmt.Fprintf(&b, "Clean: %s (%d conflict(s))\n", cleanNo, len(conflicts))
	}
	if strings.TrimSpace(payload) != "" {
		fmt.Fprintln(&b)
		b.WriteString(strings.TrimSpace(payload))
		fmt.Fprintln(&b)
	}
	if len(conflicts) > 0 {
		fmt.Fprintln(&b, "\nNext: inspect conflicts with `gk precheck <target>` before merging.")
	}
	return b.String()
}

func renderAIMergePlanHeader(target, current, providerName string, conflictCount int, colorize bool) string {
	title := "Merge plan (AI): "
	clean := "yes"
	if colorize {
		title = color.New(color.FgCyan, color.Bold).Sprint(title)
		clean = color.New(color.FgGreen).Sprint(clean)
	}
	if conflictCount > 0 {
		clean = fmt.Sprintf("no (%d conflict(s))", conflictCount)
		if colorize {
			clean = color.New(color.FgRed).Sprint(clean)
		}
	}
	return fmt.Sprintf("%s%s -> %s\nSource: AI summary via %s\nDirection: merge %s into %s\nClean: %s",
		title,
		target,
		current,
		providerName,
		target,
		current,
		clean,
	)
}

func currentMergeBranch(ctx context.Context, client *git.Client) string {
	if client == nil {
		return "HEAD"
	}
	current, err := client.CurrentBranch(ctx)
	if err != nil || strings.TrimSpace(current) == "" {
		return "HEAD"
	}
	return strings.TrimSpace(current)
}

func cleanMergePlanSummary(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "", trimmed == ">":
			continue
		case strings.HasPrefix(trimmed, "```"):
			continue
		case strings.HasPrefix(trimmed, "# "):
			out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")))
		case strings.HasPrefix(trimmed, "## "):
			out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
		default:
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func popMergeStashBestEffort(ctx context.Context, r git.Runner, stashed bool) {
	if stashed {
		popStashBestEffort(ctx, r)
	}
}
