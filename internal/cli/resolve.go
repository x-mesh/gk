package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/resolve"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	resolveCmd := &cobra.Command{
		Use:   "resolve [files...]",
		Short: "AI-powered conflict resolution",
		RunE:  runResolve,
	}
	resolveCmd.Flags().Bool("dry-run", false, "show resolution diff without modifying files")
	resolveCmd.Flags().Bool("no-ai", false, "disable AI analysis")
	resolveCmd.Flags().Bool("no-backup", false, "skip .orig backup file creation")
	resolveCmd.Flags().String("strategy", "", "apply strategy to all conflicts: ours, theirs, ai, safe")
	resolveCmd.Flags().Bool("ai", false, "shortcut for --strategy ai (resolve every conflict with AI)")
	resolveCmd.Flags().Bool("safe", false, "shortcut for --strategy safe (deterministic tier only: identical sides, trailing-whitespace/CRLF-only, one-side-unchanged-from-base, additive union files; the rest stay conflicted)")
	resolveCmd.Flags().Bool("no-continue", false, "stop after resolving; do not run the continue step")
	rootCmd.AddCommand(resolveCmd)
}

func runResolve(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	noAI, _ := cmd.Flags().GetBool("no-ai")
	noBackup, _ := cmd.Flags().GetBool("no-backup")
	strategy, _ := cmd.Flags().GetString("strategy")
	aiShortcut, _ := cmd.Flags().GetBool("ai")

	safeShortcut, _ := cmd.Flags().GetBool("safe")

	// --ai is sugar for --strategy ai. Reject combinations that contradict it
	// rather than silently picking a winner.
	if aiShortcut {
		if noAI {
			return fmt.Errorf("gk resolve: --ai and --no-ai are mutually exclusive")
		}
		if safeShortcut {
			return fmt.Errorf("gk resolve: --ai and --safe are mutually exclusive")
		}
		if strategy != "" && strategy != "ai" {
			return fmt.Errorf("gk resolve: --ai conflicts with --strategy %s", strategy)
		}
		strategy = "ai"
	}
	// --safe is sugar for --strategy safe.
	if safeShortcut {
		if strategy != "" && strategy != string(resolve.StrategySafe) {
			return fmt.Errorf("gk resolve: --safe conflicts with --strategy %s", strategy)
		}
		strategy = string(resolve.StrategySafe)
	}

	// Validate strategy value.
	switch resolve.Strategy(strategy) {
	case "", resolve.StrategyOurs, resolve.StrategyTheirs, resolve.StrategySafe:
		// ok
	case "ai":
		// ok — validated later against provider availability
	default:
		return fmt.Errorf("gk resolve: invalid strategy %q: must be ours, theirs, ai, or safe", strategy)
	}

	// Detect git state.
	state, err := gitstate.Detect(ctx, RepoFlag())
	if err != nil {
		return err
	}

	// Build runner and client. Conflict paths from git are repo-root
	// relative, so anchor every git call and all file IO at the worktree
	// top level — resolve then works from a repo subdirectory or with
	// --repo from outside the repo.
	runner := &git.ExecRunner{Dir: RepoFlag()}
	var repoRoot string
	if out, _, rerr := runner.Run(ctx, "rev-parse", "--show-toplevel"); rerr == nil {
		repoRoot = strings.TrimSpace(string(out))
	}
	if repoRoot != "" {
		runner.Dir = repoRoot
	}
	client := git.NewClient(runner)

	// Load config for AI settings.
	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("gk resolve: load config: %w", err)
	}

	// Interactive mode may use AI for
	// suggestions; explicit ours/theirs strategies must stay deterministic and
	// therefore do not build or consult a provider.
	interactive := promptAllowed()
	needsAIProvider := cfg.AI.Enabled && !noAI && (strategy == "ai" || (strategy == "" && interactive))

	// Build AI provider if enabled and not suppressed.
	var prov provider.Provider
	if needsAIProvider {
		if cfg.AI.Provider == "" {
			fc, fcErr := buildFallbackChain(nil, provider.ExecRunner{})
			if fcErr == nil {
				prov = fc
			}
		} else {
			p, pErr := provider.NewProvider(ctx, aiFactoryOptions(cfg))
			if pErr == nil {
				prov = p
			}
		}
		if err := ensureRemoteAllowed(prov, cfg.AI); err != nil {
			return err
		}
	}

	// Check AI availability for --strategy ai.
	if strategy == "ai" {
		if err := provider.ConflictResolverAvailable(ctx, prov); err != nil {
			return fmt.Errorf("gk resolve: --strategy ai requires an available AI provider with conflict resolution support")
		}
	}

	// TTY check for interactive mode.
	if strategy == "" && !interactive {
		return fmt.Errorf("gk resolve: --strategy is required in non-interactive mode")
	}

	// rerere: reuse recorded resolutions before classifying anything —
	// repeat conflicts (long-lived branch rebases) resolve at zero cost.
	// NOT for explicit ours/theirs: a recorded (possibly merged) resolution
	// would pre-empt the promised pure side-take.
	if cfg.Resolve.Rerere && !dryRun && (strategy == "ai" || strategy == string(resolve.StrategySafe) || strategy == "") {
		ensureRerere(ctx, runner, cmd.ErrOrStderr())
	}

	// resolve.verify and resolve.union_files are honored from the GLOBAL
	// config only — the repo being resolved must not be able to run shell
	// commands or widen the auto-merge surface (same trust boundary as
	// init.ai_gitignore). A repo-local attempt is ignored with a note.
	verifyCmds, unionFiles, unionSet := config.GlobalResolveSettings()
	if !unionSet {
		unionFiles = nil // nil → mechanical-tier defaults
	}
	if len(cfg.Resolve.Verify) > 0 && !slices.Equal(cfg.Resolve.Verify, verifyCmds) {
		fmt.Fprintln(cmd.ErrOrStderr(), "note: repo-local resolve.verify is ignored — set it in the global config ($XDG_CONFIG_HOME/gk/config.yaml)")
	}

	opts := resolve.ResolveOptions{
		DryRun:        dryRun,
		NoAI:          noAI,
		NoBackup:      noBackup,
		Strategy:      resolve.Strategy(strategy),
		Files:         args,
		Lang:          cfg.AI.Lang,
		UnionFiles:    unionFiles,
		MinConfidence: config.GlobalResolveMinConfidence(),
	}

	r := &resolve.Resolver{
		Runner:   runner,
		Client:   client,
		Provider: prov,
		Stderr:   cmd.ErrOrStderr(),
		Stdout:   cmd.OutOrStdout(),
		Root:     repoRoot,
	}

	// Interactive TUI mode: TTY + no strategy.
	if strategy == "" && interactive {
		return runResolveInteractive(ctx, cmd, r, state, opts)
	}

	// Batch mode defers staging so the verification gate below can restore
	// the conflicted state (`git checkout -m`) — index stages stay intact
	// until the gate passes.
	opts.DeferStage = true

	// Batch mode: --strategy provided. Only the `ai` strategy makes provider
	// calls (ours/theirs are local), so spin just for that — this is the path
	// `gk resolve --ai` and `gk pull --ai` actually take.
	stopSpin := func() {}
	if strategy == "ai" && !flagDebug {
		stopSpin = ui.StartBubbleSpinner(resolveSpinnerMessage(opts.Lang, prov.Name(), 0))
	}
	result, err := r.Run(ctx, state, opts)
	stopSpin()
	if err != nil {
		return err
	}

	// Verification gate: marker scan (always) + global resolve.verify
	// commands, for EVERY kind of resolution (deferred writes, accepted
	// markerless content, degenerate paths). Pass → stage the deferred
	// paths and proceed to continue as before. Fail → restore what gk
	// wrote and report paused; the attempt cost nothing.
	if !dryRun {
		if verr := applyResolveGate(ctx, runner, runner.Dir, verifyCmds, result, cmd.ErrOrStderr()); verr != nil {
			var ve *resolveVerifyError
			if !errors.As(verr, &ve) {
				return verr // staging failure, not a verification verdict
			}
			rep := plainResolveReport(result, state)
			rep.VerifyFailed = ve.Check
			// Rolled-back and unstaged paths are conflicts again; only
			// pre-gate staged resolutions (delete/modify) remain resolved.
			rep.Remaining = append(append([]string{}, result.PendingStage...), result.PendingAccept...)
			rep.Resolved = subtractPaths(result.Resolved, rep.Remaining)
			rep.Mechanical = nil
			if JSONOut() {
				fmt.Fprintf(cmd.ErrOrStderr(), "resolve: %v\n", verr)
				if err := emitAgentResult(cmd.OutOrStdout(), rep); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "resolve: verification failed — conflicted state restored\n  %v\n", verr)
			}
			return pausedExitIf(rep)
		}
	}

	noContinue, _ := cmd.Flags().GetBool("no-continue")
	if !canAutoContinue(ctx, runner, state, result, dryRun, noContinue) {
		rep := plainResolveReport(result, state)
		if JSONOut() && !dryRun {
			if err := emitAgentResult(cmd.OutOrStdout(), rep); err != nil {
				return err
			}
		} else {
			printResolveResult(cmd.OutOrStdout(), result, state.Kind != gitstate.StateNone)
			if len(result.Proposals) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%d hunk(s) held below confidence %.2f — kept conflicted, AI proposals attached (--json carries full lines):\n", len(result.Proposals), opts.MinConfidence)
				for _, pr := range result.Proposals {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s#%d  %s (%.2f)  %s\n", pr.File, pr.Hunk, pr.Strategy, pr.Confidence, pr.Rationale)
				}
			}
		}
		if dryRun {
			return nil // a dry-run simulation never pauses the process
		}
		return pausedExitIf(rep)
	}

	if !JSONOut() {
		printResolveResult(cmd.OutOrStdout(), result, false)
	}
	rep, err := autoContinueBatch(ctx, cmd, r, state.Kind, opts, result, verifyCmds)
	if err != nil {
		return err
	}
	if JSONOut() {
		if err := emitAgentResult(cmd.OutOrStdout(), rep); err != nil {
			return err
		}
	}
	// A still-paused resolution (later pick needs hand-resolution) exits 3.
	return pausedExitIf(rep)
}

// subtractPaths returns the entries of a not present in b, order preserved.
func subtractPaths(a, b []string) []string {
	drop := make(map[string]bool, len(b))
	for _, p := range b {
		drop[p] = true
	}
	var out []string
	for _, p := range a {
		if !drop[p] {
			out = append(out, p)
		}
	}
	return out
}

// canAutoContinue gates the continue step: only after a full resolution
// of every conflicted path, on a real in-progress operation, and only
// when the caller didn't opt out. A file-filtered run (gk resolve a.go)
// can fully resolve its own slice while other paths stay unmerged —
// the index check catches that, not the result counters.
func canAutoContinue(
	ctx context.Context,
	runner git.Runner,
	state *gitstate.State,
	result *resolve.ResolveResult,
	dryRun, noContinue bool,
) bool {
	if dryRun || noContinue || result == nil {
		return false
	}
	if result.Total == 0 || len(result.Resolved) != result.Total {
		return false
	}
	if state.Kind == gitstate.StateNone {
		return false // stash apply / 3-way apply: nothing to continue
	}
	return !pullHasUnmergedPaths(ctx, runner)
}

// plainResolveReport renders the no-continue / partial outcome as JSON.
func plainResolveReport(result *resolve.ResolveResult, state *gitstate.State) *resolveReport {
	rep := &resolveReport{Rounds: 1, State: "none"}
	if result != nil {
		rep.Resolved = append([]string{}, result.Resolved...)
		rep.Total = result.Total
		rep.Mechanical = append([]string{}, result.Mechanical...)
		rep.Remaining = append([]string{}, result.Remaining...)
		rep.Proposals = append([]resolve.HunkProposal{}, result.Proposals...)
	}
	if sub, err := stateSubcommand(state.Kind); err == nil {
		rep.State = sub
		rep.Resume = selfCmd("continue")
	}
	return rep
}

// runResolveInteractive handles the TTY interactive flow:
// collect files → parse → AI resolve → TUI → apply.
func runResolveInteractive(
	ctx context.Context,
	cmd *cobra.Command,
	r *resolve.Resolver,
	state *gitstate.State,
	opts resolve.ResolveOptions,
) error {
	// Collect conflicted files. We do this *before* checking
	// state.Kind because `git stash apply`, `git apply --3way`, and a
	// few partial-reset paths leave unmerged stages in the index
	// without writing any of the in-progress op markers gitstate.Detect
	// looks at — refusing those would force the user to fix conflicts
	// by hand even though gk resolve is otherwise perfectly capable.
	conflicted, err := r.CollectConflictedFiles(ctx)
	if err != nil {
		return err
	}
	if len(conflicted) == 0 {
		if state.Kind == gitstate.StateNone {
			return fmt.Errorf("gk resolve: no merge/rebase/cherry-pick conflict in progress and no unmerged paths")
		}
		if err := resolve.CheckStuck(state); err != nil {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "no conflicted files found")
		return nil
	}

	opType := stateKindToOpType(state.Kind)

	// Filter by user-specified files.
	filesToProcess := conflicted
	if len(opts.Files) > 0 {
		set := make(map[string]bool, len(conflicted))
		for _, f := range conflicted {
			set[f] = true
		}
		var filtered []string
		for _, f := range opts.Files {
			if set[f] {
				filtered = append(filtered, f)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: gk resolve: %s is not in conflict state\n", f)
			}
		}
		filesToProcess = filtered
	}
	if len(filesToProcess) == 0 {
		return nil
	}

	// Parse conflict files.
	parsed, _, err := r.ParseConflictFiles(filesToProcess)
	if err != nil {
		return err
	}

	// AI resolution (best-effort). One provider round-trip per conflicted
	// file, so spin during the whole batch — otherwise the terminal sits
	// frozen before the resolve TUI appears. Suppressed under --debug and a
	// no-op on non-TTY stderr (see ai_ask.go).
	aiResolutions := make(map[string][]resolve.HunkResolution)
	if !opts.NoAI && r.Provider != nil {
		stopSpin := func() {}
		if !flagDebug {
			stopSpin = ui.StartBubbleSpinner(resolveSpinnerMessage(opts.Lang, r.Provider.Name(), len(parsed)))
		}
		for _, cf := range parsed {
			res, _ := r.ResolveWithAI(ctx, cf, opType, opts.Lang)
			if res != nil {
				aiResolutions[cf.Path] = res
			}
		}
		stopSpin()
	}

	// Run TUI.
	fileResolutions, err := RunResolveTUI(parsed, aiResolutions)
	if err != nil {
		return err
	}

	// Apply resolutions.
	result := &resolve.ResolveResult{
		Failed: make(map[string]error),
		Total:  len(parsed),
	}
	for _, fr := range fileResolutions {
		// Find matching ConflictFile.
		var cf *resolve.ConflictFile
		for i := range parsed {
			if parsed[i].Path == fr.Path {
				cf = &parsed[i]
				break
			}
		}
		if cf == nil {
			continue
		}
		backup := !opts.NoBackup
		if err := r.ApplyFileResolution(ctx, *cf, fr.Resolutions, backup, opts.DryRun); err != nil {
			// Write/git-add failures are fatal — stop immediately.
			return err
		}
		result.Resolved = append(result.Resolved, fr.Path)
	}

	noContinue, _ := cmd.Flags().GetBool("no-continue")
	if canAutoContinue(ctx, r.Runner, state, result, opts.DryRun, noContinue) {
		printResolveResult(cmd.OutOrStdout(), result, false)
		return autoContinueInteractive(ctx, cmd, r, state.Kind)
	}
	printResolveResult(cmd.OutOrStdout(), result, state.Kind != gitstate.StateNone)
	return nil
}

// stateKindToOpType converts gitstate.StateKind to operation type string.
func stateKindToOpType(kind gitstate.StateKind) string {
	switch kind {
	case gitstate.StateMerge:
		return "merge"
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		return "rebase"
	case gitstate.StateCherryPick:
		return "cherry-pick"
	default:
		return "merge"
	}
}

// printResolveResult outputs the resolution summary. hintContinue adds
// the "run 'gk continue'" pointer — false when resolve drives the
// continue step itself or when there is no operation to continue.
func printResolveResult(out interface{ Write(p []byte) (int, error) }, result *resolve.ResolveResult, hintContinue bool) {
	if result == nil {
		return
	}
	if len(result.Resolved) == result.Total && result.Total > 0 {
		if hintContinue {
			fmt.Fprintln(out, "all conflicts resolved. run 'gk continue' to proceed")
		} else {
			fmt.Fprintln(out, "all conflicts resolved")
		}
	} else if len(result.Resolved) > 0 {
		fmt.Fprintf(out, "%d/%d conflicts resolved, %d remaining\n",
			len(result.Resolved), result.Total, result.Total-len(result.Resolved))
	}
}

func resolveSpinnerMessage(lang, providerName string, n int) string {
	if isKoLang(lang) {
		if n > 0 {
			return fmt.Sprintf("%s로 충돌 %d개 해결 중…", providerName, n)
		}
		return fmt.Sprintf("%s로 충돌 해결 중…", providerName)
	}
	if n > 0 {
		return fmt.Sprintf("resolving %d conflict(s) with %s…", n, providerName)
	}
	return fmt.Sprintf("resolving conflicts with %s…", providerName)
}
