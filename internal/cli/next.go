package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Explain the current git state and next safe actions",
		Long: `Reads the current repository state and explains what to do next in
plain language. It uses the configured AI provider when available and falls
back to a local deterministic plan when AI is unavailable.

With --run, gk asks to execute the single top recommended next step (taken
from gk's deterministic action allowlist, not from free-form AI output) and
runs it after you confirm. Risky commands are never auto-run.`,
		RunE: runNext,
	}
	cmd.Flags().String("provider", "", "override ai.provider")
	cmd.Flags().String("lang", "", "override assistant language (en|ko|...)")
	cmd.Flags().BoolP("run", "r", false, "execute the top recommended next step after confirmation")
	rootCmd.AddCommand(cmd)
}

func runNext(cmd *cobra.Command, _ []string) error {
	if JSONOut() {
		return fmt.Errorf("next does not support --json")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := loadStatusConfig()
	if err != nil {
		return fmt.Errorf("next: load config: %w", err)
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	if cfg != nil && cfg.Status.AutoFetch {
		maybeFetchUpstream(ctx, RepoFlag())
	}
	client := git.NewClient(runner)
	st, err := client.Status(ctx)
	if err != nil {
		if isNotAGitRepoError(err) {
			return WithHint(
				fmt.Errorf("gk next: git 저장소가 아닙니다"),
				"git init 으로 저장소를 초기화하거나, 올바른 디렉토리로 이동하세요",
			)
		}
		return err
	}

	grouped := groupEntries(st.Entries)
	var baseRes BaseResolution
	if st.Branch != "" && st.Branch != "(detached)" {
		baseRes = resolveBaseForStatus(ctx, runner, client, cfg)
	}
	providerOverride, langOverride := readStatusAssistOverrides(cmd)
	facts := collectStatusAssistFacts(ctx, runner, cfg, st, grouped, baseRes)
	diff := ""
	if cfg != nil && cfg.AI.Assist.IncludeDiff {
		diff = collectStatusDiff(ctx, runner, statusAssistDiffBudget(cfg))
	}
	if err := renderStatusAssist(ctx, cmd, runner, cmd.OutOrStdout(), cmd.ErrOrStderr(), facts, cfg, providerOverride, langOverride, diff); err != nil {
		return err
	}

	if run, _ := cmd.Flags().GetBool("run"); run {
		return runRecommendedAction(ctx, cmd, runner, facts)
	}
	return nil
}

// runRecommendedAction closes the advise→act loop: it runs the single top
// recommended step from gk's deterministic action allowlist (facts.Actions),
// not from free-form AI text, after explicit confirmation. Risky commands
// are refused outright, and a TTY is required so the user can confirm.
func runRecommendedAction(ctx context.Context, cmd *cobra.Command, runner git.Runner, facts statusAssistFacts) error {
	if len(facts.Actions) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), stylizeHintLine("next: nothing to run — already in good shape"))
		return nil
	}
	cmdline := strings.TrimSpace(facts.Actions[0].Command)
	if cmdline == "" {
		return nil
	}
	// Defensive: never auto-run a destructive command even if one ever
	// slips into the allowlist.
	if d := flagDangerousMentions(cmdline); len(d) > 0 {
		return WithHint(
			fmt.Errorf("refusing to auto-run a hard-to-undo command: %s", cmdline),
			"review it and run it yourself",
		)
	}
	if !ui.IsTerminal() {
		return WithHint(
			errors.New("--run needs a terminal to confirm"),
			"run it yourself: "+cmdline,
		)
	}
	ok, err := ui.Confirm(fmt.Sprintf("Run recommended next step: %s ?", cmdline), true)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return execRecommendedCommand(ctx, cmd, runner, cmdline)
}

// execRecommendedCommand runs a gk/git command line. gk-native commands are
// re-executed via this binary (inheriting the real terminal so TUIs work);
// git commands go through the runner. Anything else is refused — the
// allowlist only ever produces gk/git commands.
func execRecommendedCommand(ctx context.Context, cmd *cobra.Command, runner git.Runner, cmdline string) error {
	parts := strings.Fields(cmdline)
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "gk":
		self, err := os.Executable()
		if err != nil || self == "" {
			self = "gk"
		}
		c := exec.CommandContext(ctx, self, parts[1:]...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	case "git":
		stdout, stderr, err := runner.Run(ctx, parts[1:]...)
		fmt.Fprint(cmd.OutOrStdout(), string(stdout))
		if len(stderr) > 0 {
			fmt.Fprint(cmd.ErrOrStderr(), string(stderr))
		}
		return err
	default:
		return fmt.Errorf("cannot run %q: only gk/git commands are supported", cmdline)
	}
}
