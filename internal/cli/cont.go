package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

func init() {
	cmd := &cobra.Command{
		Use:   "continue",
		Short: "Continue the current rebase/merge/cherry-pick after resolving conflicts",
		RunE:  runContinue,
	}
	cmd.Flags().Bool("yes", false, "skip prompt and continue immediately")
	rootCmd.AddCommand(cmd)
}

func runContinue(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	state, err := gitstate.Detect(ctx, RepoFlag())
	if err != nil {
		return err
	}
	if state.Kind == gitstate.StateNone {
		return fmt.Errorf("no rebase/merge/cherry-pick in progress")
	}

	runner := &git.ExecRunner{
		Dir:      RepoFlag(),
		ExtraEnv: os.Environ(),
	}
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		client := git.NewClient(runner)
		dirty, err := client.IsDirty(ctx)
		if err != nil {
			return err
		}
		if dirty {
			fmt.Fprintf(os.Stderr, "note: working tree still has changes (they will be included).\n")
		}
	}

	var sub string
	switch state.Kind {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		sub = "rebase"
	case gitstate.StateMerge:
		sub = "merge"
	case gitstate.StateCherryPick:
		sub = "cherry-pick"
	case gitstate.StateRevert:
		sub = "revert"
	default:
		return fmt.Errorf("unsupported state %s", state.Kind)
	}

	_, stderr, err := runner.Run(ctx, sub, "--continue")
	if err != nil {
		return fmt.Errorf("git %s --continue failed: %s: %w", sub, string(stderr), err)
	}
	return nil
}
