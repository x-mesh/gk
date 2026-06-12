package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

func init() {
	cmd := &cobra.Command{
		Use:   "abort",
		Short: "Abort the current rebase/merge/cherry-pick and restore the previous state",
		RunE:  runAbort,
	}
	cmd.Flags().Bool("yes", false, "skip prompt and abort immediately")
	rootCmd.AddCommand(cmd)
}

func runAbort(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	state, err := gitstate.Detect(ctx, RepoFlag())
	if err != nil {
		return err
	}
	if state.Kind == gitstate.StateNone {
		return fmt.Errorf("no rebase/merge/cherry-pick in progress")
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}

	sub, err := stateSubcommand(state.Kind)
	if err != nil {
		return err
	}

	_, stderr, err := runner.Run(ctx, sub, "--abort")
	if err != nil {
		return fmt.Errorf("git %s --abort failed: %s: %w", sub, string(stderr), err)
	}
	return nil
}
