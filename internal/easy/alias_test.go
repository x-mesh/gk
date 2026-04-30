package easy

import (
	"testing"

	"github.com/spf13/cobra"
	"pgregory.net/rapid"
)

// Feature: easy-mode, Property 8: 별칭 등록 동작 — For any 한국어 별칭 맵에 대해:
// - Easy Mode 활성화 시 RegisterAliases 후 모든 별칭이 cobra 명령어 트리에서 찾을 수 있어야 한다
// - Easy Mode 비활성화 시 별칭이 cobra 명령어 트리에 등록되지 않아야 한다
// - 기존 영어 서브커맨드와 동일한 이름의 별칭은 등록되지 않아야 한다 (영어 우선)
//
// **Validates: Requirements 7.1, 7.3, 7.5**
func TestProperty_AliasRegistration(t *testing.T) {
	// Collect all English command names referenced in aliasMap.
	englishNames := make(map[string]bool)
	for _, eng := range aliasMap {
		englishNames[eng] = true
	}
	englishNameSlice := make([]string, 0, len(englishNames))
	for name := range englishNames {
		englishNameSlice = append(englishNameSlice, name)
	}

	// Collect all Korean alias names from aliasMap.
	aliasNames := make([]string, 0, len(aliasMap))
	for alias := range aliasMap {
		aliasNames = append(aliasNames, alias)
	}

	// genAliasMap generates a random subset of English commands to register
	// on the root command, plus a random enabled bool. This simulates
	// different cobra command tree configurations.
	type aliasTestInput struct {
		enabledCmds []string // English commands present in the tree
		enabled     bool     // Easy Mode enabled/disabled
		conflictCmd string   // alias name to also register as an English command (may be empty)
	}

	genAliasMap := rapid.Custom(func(rt *rapid.T) aliasTestInput {
		// Pick a random subset of English commands to register.
		n := rapid.IntRange(1, len(englishNameSlice)).Draw(rt, "numCmds")
		cmds := rapid.SliceOfNDistinct(
			rapid.SampledFrom(englishNameSlice),
			n, n,
			rapid.ID[string],
		).Draw(rt, "cmds")

		enabled := rapid.Bool().Draw(rt, "enabled")

		// Optionally create a conflict: register an alias name as an
		// existing English command.
		var conflict string
		if rapid.Bool().Draw(rt, "hasConflict") && len(aliasNames) > 0 {
			conflict = rapid.SampledFrom(aliasNames).Draw(rt, "conflictAlias")
		}

		return aliasTestInput{
			enabledCmds: cmds,
			enabled:     enabled,
			conflictCmd: conflict,
		}
	})

	// newRootWithCmds creates a fresh cobra root command with the given
	// English subcommands and an optional conflict command.
	newRootWithCmds := func(cmds []string, conflict string) *cobra.Command {
		root := &cobra.Command{Use: "gk"}
		for _, name := range cmds {
			root.AddCommand(&cobra.Command{
				Use:   name,
				Short: "mock " + name,
				RunE: func(cmd *cobra.Command, args []string) error {
					return nil
				},
			})
		}
		// Add conflict command: a command whose name matches a Korean alias.
		if conflict != "" {
			root.AddCommand(&cobra.Command{
				Use:   conflict,
				Short: "english priority " + conflict,
				RunE: func(cmd *cobra.Command, args []string) error {
					return nil
				},
			})
		}
		return root
	}

	t.Run("enabled_registers_aliases", func(t *testing.T) {
		// Property: When enabled=true, all aliases whose English counterpart
		// exists in the tree are registered as subcommands.
		rapid.Check(t, func(rt *rapid.T) {
			input := genAliasMap.Draw(rt, "input")
			if !input.enabled {
				// Only test enabled case here.
				return
			}

			root := newRootWithCmds(input.enabledCmds, "")
			RegisterAliases(root, true)

			// Build set of registered English commands.
			registeredEnglish := make(map[string]bool)
			for _, name := range input.enabledCmds {
				registeredEnglish[name] = true
			}

			for alias, eng := range aliasMap {
				if !registeredEnglish[eng] {
					// English command not in tree — alias should NOT be registered.
					if findSubcommand(root, alias) != nil {
						rt.Fatalf("alias %q registered but English command %q not in tree",
							alias, eng)
					}
					continue
				}
				// English command exists — alias MUST be registered.
				cmd := findSubcommand(root, alias)
				if cmd == nil {
					rt.Fatalf("alias %q not registered but English command %q exists in tree",
						alias, eng)
				}
			}
		})
	})

	t.Run("disabled_no_aliases", func(t *testing.T) {
		// Property: When enabled=false, NO aliases are registered regardless
		// of which English commands exist.
		rapid.Check(t, func(rt *rapid.T) {
			input := genAliasMap.Draw(rt, "input")

			root := newRootWithCmds(input.enabledCmds, "")
			RegisterAliases(root, false)

			// Count commands before and after — should be the same.
			for alias := range aliasMap {
				if findSubcommand(root, alias) != nil {
					rt.Fatalf("alias %q registered when enabled=false", alias)
				}
			}
		})
	})

	t.Run("conflict_english_priority", func(t *testing.T) {
		// Property: When an alias name conflicts with an existing command,
		// the original command is preserved (English priority) and the
		// alias is NOT registered over it.
		rapid.Check(t, func(rt *rapid.T) {
			input := genAliasMap.Draw(rt, "input")
			if input.conflictCmd == "" {
				// No conflict in this run — skip.
				return
			}

			// Ensure the English counterpart of the conflicting alias is
			// in the command tree so the alias would normally be registered.
			eng, hasMapping := aliasMap[input.conflictCmd]
			if !hasMapping {
				return
			}

			// Make sure the English command is in the enabled list.
			cmds := append([]string{}, input.enabledCmds...)
			found := false
			for _, c := range cmds {
				if c == eng {
					found = true
					break
				}
			}
			if !found {
				cmds = append(cmds, eng)
			}

			root := newRootWithCmds(cmds, input.conflictCmd)

			// Record the original command's Short description before registration.
			originalCmd := findSubcommand(root, input.conflictCmd)
			if originalCmd == nil {
				rt.Fatalf("conflict command %q not found in tree", input.conflictCmd)
				return
			}
			originalShort := originalCmd.Short

			RegisterAliases(root, true)

			// The command with the alias name should still be the original
			// English-priority command, not the alias.
			afterCmd := findSubcommand(root, input.conflictCmd)
			if afterCmd == nil {
				rt.Fatalf("command %q disappeared after RegisterAliases", input.conflictCmd)
				return
			}
			if afterCmd.Short != originalShort {
				rt.Fatalf("command %q was overwritten: Short=%q, want original %q",
					input.conflictCmd, afterCmd.Short, originalShort)
			}
		})
	})
}

// TestAlias_RegisteredAsCobraAliases verifies that each Korean alias
// lands in the original command's `Aliases` slice rather than being
// installed as a separate command. This is the cobra-native pattern
// that keeps the subcommand tree intact (a previous implementation
// built duplicate commands and reparented every subcommand to the
// alias, silently breaking `CommandPath()` for the original).
func TestAlias_RegisteredAsCobraAliases(t *testing.T) {
	root := &cobra.Command{Use: "gk"}
	for _, eng := range aliasMap {
		root.AddCommand(&cobra.Command{
			Use:   eng,
			Short: "mock " + eng,
			RunE: func(cmd *cobra.Command, args []string) error {
				return nil
			},
		})
	}

	// Pre-condition: no aliases set on any command.
	for _, cmd := range root.Commands() {
		if len(cmd.Aliases) != 0 {
			t.Fatalf("unexpected pre-existing aliases on %q: %v", cmd.Name(), cmd.Aliases)
		}
	}

	RegisterAliases(root, true)

	for alias, eng := range aliasMap {
		// findSubcommand checks both primary name and Aliases — both
		// queries should now land on the same *cobra.Command.
		viaAlias := findSubcommand(root, alias)
		viaEnglish := findSubcommand(root, eng)
		if viaAlias == nil {
			t.Errorf("alias %q not registered", alias)
			continue
		}
		if viaAlias != viaEnglish {
			t.Errorf("alias %q resolves to a different *Command than %q — duplicate tree", alias, eng)
		}
		if !containsString(viaEnglish.Aliases, alias) {
			t.Errorf("alias %q missing from %q.Aliases (got %v)", alias, eng, viaEnglish.Aliases)
		}
	}
}

// TestAlias_Idempotent verifies that calling RegisterAliases twice
// does not duplicate aliases on the original command's Aliases slice.
func TestAlias_Idempotent(t *testing.T) {
	root := &cobra.Command{Use: "gk"}
	for _, eng := range aliasMap {
		root.AddCommand(&cobra.Command{Use: eng, RunE: func(*cobra.Command, []string) error { return nil }})
	}
	RegisterAliases(root, true)
	RegisterAliases(root, true)

	for _, eng := range aliasMap {
		cmd := findSubcommand(root, eng)
		seen := map[string]int{}
		for _, a := range cmd.Aliases {
			seen[a]++
		}
		for a, n := range seen {
			if n > 1 {
				t.Errorf("alias %q duplicated on %q (count %d)", a, eng, n)
			}
		}
	}
}

// TestAlias_DisabledNotRegistered verifies that when enabled=false,
// aliases are not registered and attempting to find them returns nil.
//
// **Validates: Requirements 7.3**
func TestAlias_DisabledNotRegistered(t *testing.T) {
	// Build a root command with all English commands from aliasMap.
	root := &cobra.Command{Use: "gk"}
	for _, eng := range aliasMap {
		root.AddCommand(&cobra.Command{
			Use:   eng,
			Short: "mock " + eng,
			RunE: func(cmd *cobra.Command, args []string) error {
				return nil
			},
		})
	}

	RegisterAliases(root, false)

	for alias := range aliasMap {
		cmd := findSubcommand(root, alias)
		if cmd != nil {
			t.Errorf("alias %q should not be registered when enabled=false, but found command with Short=%q",
				alias, cmd.Short)
		}
	}
}
