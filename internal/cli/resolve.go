package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/resolve"
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
	resolveCmd.Flags().String("strategy", "", "apply strategy to all conflicts: ours, theirs, ai")
	rootCmd.AddCommand(resolveCmd)
}

func runResolve(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	noAI, _ := cmd.Flags().GetBool("no-ai")
	noBackup, _ := cmd.Flags().GetBool("no-backup")
	strategy, _ := cmd.Flags().GetString("strategy")

	// Validate strategy value.
	switch resolve.Strategy(strategy) {
	case "", resolve.StrategyOurs, resolve.StrategyTheirs:
		// ok
	case "ai":
		// ok — validated later against provider availability
	default:
		return fmt.Errorf("gk resolve: invalid strategy %q: must be ours, theirs, or ai", strategy)
	}

	// Detect git state.
	state, err := gitstate.Detect(ctx, RepoFlag())
	if err != nil {
		return err
	}

	// Build runner and client.
	runner := &git.ExecRunner{Dir: RepoFlag(), ExtraEnv: os.Environ()}
	client := git.NewClient(runner)

	// Load config for AI settings.
	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("gk resolve: load config: %w", err)
	}

	// Build AI provider if enabled and not suppressed.
	var prov provider.Provider
	if cfg.AI.Enabled && !noAI {
		if cfg.AI.Provider == "" {
			fc, fcErr := buildFallbackChain(nil, provider.ExecRunner{})
			if fcErr == nil {
				prov = fc
			}
		} else {
			p, pErr := provider.NewProvider(ctx, provider.FactoryOptions{
				Name:   cfg.AI.Provider,
				Runner: provider.ExecRunner{},
			})
			if pErr == nil {
				prov = p
			}
		}
	}

	// Check AI availability for --strategy ai.
	if strategy == "ai" {
		aiOK := prov != nil
		if aiOK {
			if _, ok := prov.(provider.ConflictResolver); !ok {
				aiOK = false
			}
		}
		if !aiOK {
			return fmt.Errorf("gk resolve: --strategy ai requires an available AI provider with conflict resolution support")
		}
	}

	// TTY check for interactive mode.
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	if strategy == "" && !isTTY {
		return fmt.Errorf("gk resolve: --strategy is required in non-interactive mode")
	}

	opts := resolve.ResolveOptions{
		DryRun:   dryRun,
		NoAI:     noAI,
		NoBackup: noBackup,
		Strategy: resolve.Strategy(strategy),
		Files:    args,
		Lang:     cfg.AI.Lang,
	}

	r := &resolve.Resolver{
		Runner:   runner,
		Client:   client,
		Provider: prov,
		Stderr:   cmd.ErrOrStderr(),
		Stdout:   cmd.OutOrStdout(),
	}

	// Interactive TUI mode: TTY + no strategy.
	if strategy == "" && isTTY {
		return runResolveInteractive(ctx, cmd, r, state, opts)
	}

	// Batch mode: --strategy provided.
	result, err := r.Run(ctx, state, opts)
	if err != nil {
		return err
	}

	printResolveResult(cmd.OutOrStdout(), result)
	return nil
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
	// Validate conflict state.
	if state.Kind == gitstate.StateNone {
		return fmt.Errorf("gk resolve: no merge/rebase/cherry-pick conflict in progress")
	}

	opType := stateKindToOpType(state.Kind)

	// Collect conflicted files.
	conflicted, err := r.CollectConflictedFiles(ctx)
	if err != nil {
		return err
	}
	if len(conflicted) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "no conflicted files found")
		return nil
	}

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

	// AI resolution (best-effort).
	aiResolutions := make(map[string][]resolve.HunkResolution)
	if !opts.NoAI && r.Provider != nil {
		for _, cf := range parsed {
			res, _ := r.ResolveWithAI(ctx, cf, opType, opts.Lang)
			if res != nil {
				aiResolutions[cf.Path] = res
			}
		}
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

	printResolveResult(cmd.OutOrStdout(), result)
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

// printResolveResult outputs the resolution summary.
func printResolveResult(out interface{ Write(p []byte) (int, error) }, result *resolve.ResolveResult) {
	if result == nil {
		return
	}
	if len(result.Resolved) == result.Total && result.Total > 0 {
		fmt.Fprintln(out, "all conflicts resolved. run 'gk continue' to proceed")
	} else if len(result.Resolved) > 0 {
		fmt.Fprintf(out, "%d/%d conflicts resolved, %d remaining\n",
			len(result.Resolved), result.Total, result.Total-len(result.Resolved))
	}
}
