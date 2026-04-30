package easy

import (
	"github.com/spf13/cobra"
)

// aliasMap maps Korean alias names to their English command counterparts.
// These aliases are registered only when Easy Mode is active.
var aliasMap = map[string]string{
	"상태":   "status",
	"저장":   "commit",
	"올리기":  "push",
	"가져오기": "pull",
	"동기화":  "sync",
	"되돌리기": "undo",
	"갈래":   "branch",
	"검사":   "doctor",
	"안내":   "guide",
}

// RegisterAliases adds Korean subcommand aliases to the cobra command
// tree when Easy Mode is enabled. Each alias wraps the original English
// command, sharing its RunE handler and flag set.
//
// Aliases are skipped when:
//   - enabled is false (Easy Mode inactive)
//   - the original English command does not exist in the tree
//   - a command with the alias name already exists (English priority)
func RegisterAliases(root *cobra.Command, enabled bool) {
	if !enabled {
		return
	}

	for alias, englishName := range aliasMap {
		// Look up the original English command.
		original := findSubcommand(root, englishName)
		if original == nil {
			// Original command not registered — skip.
			continue
		}

		// Check for conflict: if a command with the alias name already
		// exists, skip registration (English commands take priority).
		if findSubcommand(root, alias) != nil {
			continue
		}

		// Build the alias command that delegates to the original.
		aliasCmd := &cobra.Command{
			Use:   alias,
			Short: original.Short + " (" + englishName + ")",
			Long:  original.Long,
			RunE:  original.RunE,
			Run:   original.Run,
			Args:  original.Args,

			// Inherit behavioural settings from the original.
			SilenceUsage:  original.SilenceUsage,
			SilenceErrors: original.SilenceErrors,

			// Alias commands share the original's valid args and
			// completion functions.
			ValidArgs:              original.ValidArgs,
			ValidArgsFunction:      original.ValidArgsFunction,
			DisableFlagParsing:     original.DisableFlagParsing,
			DisableFlagsInUseLine:  original.DisableFlagsInUseLine,
			DisableSuggestions:     original.DisableSuggestions,
			TraverseChildren:       original.TraverseChildren,
		}

		// Share the original command's flag set so all flags work
		// identically on the alias.
		aliasCmd.Flags().AddFlagSet(original.Flags())
		aliasCmd.Flags().AddFlagSet(original.InheritedFlags())

		// Copy subcommands if the original has any (e.g. "branch list").
		for _, sub := range original.Commands() {
			aliasCmd.AddCommand(sub)
		}

		root.AddCommand(aliasCmd)
	}
}

// findSubcommand searches root's direct children for a command whose
// Use field (first word) matches name. Returns nil if not found.
func findSubcommand(root *cobra.Command, name string) *cobra.Command {
	for _, cmd := range root.Commands() {
		if cmd.Name() == name {
			return cmd
		}
	}
	return nil
}
