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
// tree when Easy Mode is enabled. Aliases are appended to cobra's
// native `Aliases` field on the original command — this is the
// idiomatic way to give a command a second name and keeps the entire
// subcommand tree intact.
//
// The previous implementation built a *new* alias command and called
// `aliasCmd.AddCommand(sub)` for each child, which reparented every
// subcommand to the alias and silently broke the original command's
// `CommandPath()` (so `gk branch list --help` would show the path as
// `gk 갈래 list`). Cobra's native Aliases avoid that footgun
// completely — `gk 갈래 list` resolves to the same `*cobra.Command`
// as `gk branch list` without a duplicate subtree.
//
// Aliases are skipped when:
//   - enabled is false (Easy Mode inactive)
//   - the original English command does not exist in the tree
//   - the alias is already registered (idempotent re-runs)
//   - a different command at the same level already uses the alias
//     name (English priority)
func RegisterAliases(root *cobra.Command, enabled bool) {
	if !enabled {
		return
	}

	for alias, englishName := range aliasMap {
		original := findSubcommand(root, englishName)
		if original == nil {
			continue
		}

		// Idempotent: if the alias is already in the list, leave it.
		if containsString(original.Aliases, alias) {
			continue
		}

		// English-priority conflict: if a sibling command uses the
		// same name, skip — never shadow an English command.
		if other := findSubcommand(root, alias); other != nil && other != original {
			continue
		}

		original.Aliases = append(original.Aliases, alias)
	}
}

// containsString reports whether haystack contains needle. Internal
// helper — kept here rather than pulling in a slices-package dep.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// findSubcommand searches root's direct children for a command whose
// primary name OR any alias matches. Returns nil if not found.
//
// Aliases are checked because RegisterAliases now appends Korean
// aliases to the original command's `Aliases` slice rather than
// creating duplicate commands; without this lookup the post-register
// state would falsely report aliases as unregistered.
func findSubcommand(root *cobra.Command, name string) *cobra.Command {
	for _, cmd := range root.Commands() {
		if cmd.Name() == name {
			return cmd
		}
		for _, a := range cmd.Aliases {
			if a == name {
				return cmd
			}
		}
	}
	return nil
}
