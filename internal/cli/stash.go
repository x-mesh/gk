package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/stash"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	stashCmd := &cobra.Command{
		Use:   "stash",
		Short: "Manage stashes interactively (list, push, pop, apply, drop, show)",
		Long: `Inspect and act on the git stash list.

With no subcommand, opens an interactive picker:
  - choose a stash, then apply / pop / show / drop.

Subcommands:
  push   create a new stash (--include-untracked optional)
  list   print the stash table (matches the picker)
  pop    apply + drop a stash by ref (default: stash@{0})
  apply  apply without dropping
  drop   discard a stash`,
		RunE: runStashTUI,
	}

	pushCmd := &cobra.Command{
		Use:   "push [-m message]",
		Short: "Stash the current working tree",
		RunE:  runStashPush,
	}
	pushCmd.Flags().StringP("message", "m", "", "stash description")
	pushCmd.Flags().BoolP("include-untracked", "u", true, "include untracked files (default true)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List stashes",
		RunE:  runStashList,
	}

	popCmd := &cobra.Command{
		Use:   "pop [stash@{N}]",
		Short: "Apply and drop a stash",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runStashPop,
	}

	applyCmd := &cobra.Command{
		Use:   "apply [stash@{N}]",
		Short: "Apply a stash without dropping it",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runStashApply,
	}

	dropCmd := &cobra.Command{
		Use:   "drop [stash@{N}]",
		Short: "Discard a stash",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runStashDrop,
	}

	stashCmd.AddCommand(pushCmd, listCmd, popCmd, applyCmd, dropCmd)
	rootCmd.AddCommand(stashCmd)
}

func runStashList(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	entries, err := stash.List(cmd.Context(), runner)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no stashes")
		return nil
	}
	w := cmd.OutOrStdout()
	for _, e := range entries {
		fmt.Fprintf(w, "%-12s  %-12s  %s\n", e.Ref, stashRelative(e.Created), e.Message)
	}
	return nil
}

func runStashPush(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	msg, _ := cmd.Flags().GetString("message")
	includeUntracked, _ := cmd.Flags().GetBool("include-untracked")
	if err := stash.Push(cmd.Context(), runner, msg, includeUntracked); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "stashed")
	return nil
}

func runStashPop(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ref := "stash@{0}"
	if len(args) == 1 {
		ref = args[0]
	}
	if err := stash.Pop(cmd.Context(), runner, ref); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "popped %s\n", ref)
	return nil
}

func runStashApply(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ref := "stash@{0}"
	if len(args) == 1 {
		ref = args[0]
	}
	if err := stash.Apply(cmd.Context(), runner, ref); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "applied %s\n", ref)
	return nil
}

func runStashDrop(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ref := "stash@{0}"
	if len(args) == 1 {
		ref = args[0]
	}
	if err := stash.Drop(cmd.Context(), runner, ref); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "dropped %s\n", ref)
	return nil
}

// runStashTUI is the no-subcommand entry — picker → action menu.
func runStashTUI(cmd *cobra.Command, args []string) error {
	if !ui.IsTerminal() {
		return cmd.Help()
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()

	for {
		entries, err := stash.List(ctx, runner)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no stashes")
			return nil
		}

		items := make([]ui.PickerItem, 0, len(entries))
		for _, e := range entries {
			items = append(items, ui.PickerItem{
				Key:     e.Ref,
				Display: fmt.Sprintf("%-12s  %-12s  %s", e.Ref, stashRelative(e.Created), e.Message),
				Cells:   []string{e.Ref, stashRelative(e.Created), e.Message},
			})
		}

		picker := &ui.TablePicker{Headers: []string{"INDEX", "TIME", "MESSAGE"}}
		picked, err := picker.Pick(ctx, "stash", items)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return nil
			}
			return err
		}

		// Action submenu — show the stash diff scrollable + keystroke
		// actions, all in the same TUI affordance the rest of gk uses.
		diff, _ := stash.Show(ctx, runner, picked.Key)
		if strings.TrimSpace(diff) == "" {
			diff = "(empty diff)"
		}
		options := []ui.ScrollSelectOption{
			{Key: "a", Value: "apply", Display: "apply — restore changes (keep stash)", IsDefault: true},
			{Key: "p", Value: "pop", Display: "pop — apply, then drop the stash"},
			{Key: "d", Value: "drop", Display: "drop — discard this stash"},
			{Key: "b", Value: "back", Display: "back — pick a different stash"},
		}
		choice, err := ui.ScrollSelectTUI(ctx, picked.Key+" — "+picked.Cells[2], diff, options)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return nil
			}
			return err
		}

		switch choice {
		case "apply":
			if err := stash.Apply(ctx, runner, picked.Key); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", err)
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "applied %s\n", picked.Key)
			return nil
		case "pop":
			if err := stash.Pop(ctx, runner, picked.Key); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", err)
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "popped %s\n", picked.Key)
			return nil
		case "drop":
			if err := stash.Drop(ctx, runner, picked.Key); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", err)
				continue
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "dropped %s\n", picked.Key)
			// loop back to the picker so the user can act on more stashes
		case "back", "":
			// loop back to picker
		}
	}
}

// stashRelative renders a created timestamp as "5m ago" / "today" — the
// same vocabulary as branch clean's relative-time formatter so users
// don't have to translate between two formats.
func stashRelative(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return relativeTime(time.Since(t))
}
