package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Explain the current git state and next safe actions",
		Long: `Reads the current repository state and explains what to do next in
plain language. It uses the configured AI provider when available and falls
back to a local deterministic plan when AI is unavailable.`,
		RunE: runNext,
	}
	cmd.Flags().String("provider", "", "override ai.provider")
	cmd.Flags().String("lang", "", "override assistant language (en|ko|...)")
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
	return renderStatusAssist(ctx, cmd, cmd.OutOrStdout(), cmd.ErrOrStderr(), facts, cfg, providerOverride, langOverride)
}
