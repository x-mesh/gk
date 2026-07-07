package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// snapshotHookCommand is the command the managed Stop hook runs. Matching is
// prefix-based (see snapshotHookOwned) so a user-tuned variant like
// "gk snapshot -q -m note" still counts as installed and gets removed on
// uninstall, while unrelated hooks are never touched.
const snapshotHookCommand = "gk snapshot -q"

// newSnapshotHookCmd wires the trigger-automation phase of the snapshot
// safety net: a Claude Code Stop hook that runs `gk snapshot -q` after every
// AI turn, so agent-driven edits are checkpointed without polluting branch
// history. The installer only ever APPENDS to the Stop hook array — existing
// hooks (a user's own Stop automation) must keep running.
func newSnapshotHookCmd() *cobra.Command {
	hook := &cobra.Command{
		Use:   "hook",
		Short: "Manage the Claude Code Stop hook that auto-snapshots after each AI turn",
		Long: `Installs 'gk snapshot -q' as a Claude Code Stop hook so every finished AI
turn checkpoints the working tree into refs/wip/<branch> — an automatic
safety net with zero branch-history pollution.

By default the hook goes into the user-level ~/.claude/settings.json, so it
covers every repository. --project targets this repository's
.claude/settings.json instead. Existing hooks are preserved: install appends,
uninstall removes only the gk-managed entry.`,
	}
	hook.PersistentFlags().Bool("project", false, "target this repo's .claude/settings.json instead of ~/.claude/settings.json")
	hook.PersistentFlags().String("settings", "", "explicit settings.json path (overrides --project)")

	hook.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Add the auto-snapshot Stop hook (idempotent)",
		Args:  cobra.NoArgs,
		RunE:  runSnapshotHookInstall,
	})
	hook.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Report whether the auto-snapshot Stop hook is installed",
		Args:  cobra.NoArgs,
		RunE:  runSnapshotHookStatus,
	})
	hook.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Remove the auto-snapshot Stop hook, leaving other hooks alone",
		Args:  cobra.NoArgs,
		RunE:  runSnapshotHookUninstall,
	})
	return hook
}

// snapshotHookSettingsPath resolves which settings.json the subcommand
// operates on: --settings verbatim, --project → <repo root>/.claude/, else
// the user-level ~/.claude/.
func snapshotHookSettingsPath(cmd *cobra.Command) (string, error) {
	if p, _ := cmd.Flags().GetString("settings"); strings.TrimSpace(p) != "" {
		return p, nil
	}
	if project, _ := cmd.Flags().GetBool("project"); project {
		runner := &git.ExecRunner{Dir: RepoFlag()}
		out, _, err := runner.Run(cmd.Context(), "rev-parse", "--show-toplevel")
		if err != nil {
			return "", fmt.Errorf("locate repo root: %w", err)
		}
		return filepath.Join(strings.TrimSpace(string(out)), ".claude", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// loadHookSettings reads a settings.json into a generic map. A missing file
// is an empty settings object; malformed JSON is a hard error — we never
// risk rewriting (and thereby destroying) a file we could not parse.
// Numbers decode as json.Number so re-marshalling cannot mangle them into
// float notation.
func loadHookSettings(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var settings map[string]any
	if err := dec.Decode(&settings); err != nil {
		return nil, WithHint(fmt.Errorf("parse %s: %w", path, err),
			"fix the JSON by hand first — gk refuses to rewrite a file it cannot parse")
	}
	return settings, nil
}

func saveHookSettings(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// stopHookGroups returns settings.hooks.Stop as a slice, tolerating any
// missing level. The bool reports whether the path existed with the right
// shapes; false with a non-nil settings still means "not installed".
func stopHookGroups(settings map[string]any) []any {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return nil
	}
	stop, ok := hooks["Stop"].([]any)
	if !ok {
		return nil
	}
	return stop
}

// snapshotHookOwned reports whether a single hook entry (one element of a
// group's "hooks" array) is the gk-managed auto-snapshot hook.
func snapshotHookOwned(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	cmdStr, _ := m["command"].(string)
	return strings.HasPrefix(strings.TrimSpace(cmdStr), "gk snapshot")
}

func snapshotHookInstalled(settings map[string]any) bool {
	for _, group := range stopHookGroups(settings) {
		g, ok := group.(map[string]any)
		if !ok {
			continue
		}
		inner, ok := g["hooks"].([]any)
		if !ok {
			continue
		}
		for _, entry := range inner {
			if snapshotHookOwned(entry) {
				return true
			}
		}
	}
	return false
}

func runSnapshotHookInstall(cmd *cobra.Command, _ []string) error {
	w := cmd.OutOrStdout()
	path, err := snapshotHookSettingsPath(cmd)
	if err != nil {
		return err
	}
	settings, err := loadHookSettings(path)
	if err != nil {
		return err
	}
	if snapshotHookInstalled(settings) {
		fmt.Fprintf(w, "auto-snapshot Stop hook already installed in %s\n", path)
		return nil
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		if _, exists := settings["hooks"]; exists {
			return fmt.Errorf("%s: \"hooks\" is not an object — fix it by hand first", path)
		}
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	stop, ok := hooks["Stop"].([]any)
	if !ok {
		if _, exists := hooks["Stop"]; exists {
			return fmt.Errorf("%s: hooks.Stop is not an array — fix it by hand first", path)
		}
		stop = nil
	}
	// Append a new matcher group rather than injecting into an existing one:
	// existing groups belong to the user and may carry matchers or ordering
	// assumptions we must not disturb.
	stop = append(stop, map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": snapshotHookCommand,
				"timeout": json.Number("10"),
			},
		},
	})
	hooks["Stop"] = stop

	if err := saveHookSettings(path, settings); err != nil {
		return err
	}
	fmt.Fprintln(w, successLinef("installed", "auto-snapshot Stop hook → %s", path))
	fmt.Fprintln(w, stylizeHintLine("hint: every finished Claude Code turn now runs `gk snapshot -q`; undo with gk snapshot hook uninstall"))
	return nil
}

func runSnapshotHookStatus(cmd *cobra.Command, _ []string) error {
	w := cmd.OutOrStdout()
	path, err := snapshotHookSettingsPath(cmd)
	if err != nil {
		return err
	}
	settings, err := loadHookSettings(path)
	if err != nil {
		return err
	}
	if snapshotHookInstalled(settings) {
		fmt.Fprintf(w, "installed — %s runs \"%s\" on Stop\n", path, snapshotHookCommand)
		return nil
	}
	fmt.Fprintf(w, "not installed in %s\n", path)
	fmt.Fprintln(w, stylizeHintLine("hint: gk snapshot hook install   # snapshot after every Claude Code turn"))
	return nil
}

func runSnapshotHookUninstall(cmd *cobra.Command, _ []string) error {
	w := cmd.OutOrStdout()
	path, err := snapshotHookSettingsPath(cmd)
	if err != nil {
		return err
	}
	settings, err := loadHookSettings(path)
	if err != nil {
		return err
	}
	if !snapshotHookInstalled(settings) {
		fmt.Fprintf(w, "auto-snapshot Stop hook is not installed in %s\n", path)
		return nil
	}

	hooks := settings["hooks"].(map[string]any)
	var kept []any
	for _, group := range stopHookGroups(settings) {
		g, ok := group.(map[string]any)
		if !ok {
			kept = append(kept, group)
			continue
		}
		inner, ok := g["hooks"].([]any)
		if !ok {
			kept = append(kept, group)
			continue
		}
		var keptInner []any
		for _, entry := range inner {
			if !snapshotHookOwned(entry) {
				keptInner = append(keptInner, entry)
			}
		}
		switch {
		case len(keptInner) == len(inner):
			kept = append(kept, group) // untouched group, keep verbatim
		case len(keptInner) > 0:
			g["hooks"] = keptInner
			kept = append(kept, g)
			// Groups left empty by the removal are dropped entirely.
		}
	}
	if len(kept) > 0 {
		hooks["Stop"] = kept
	} else {
		delete(hooks, "Stop")
	}

	if err := saveHookSettings(path, settings); err != nil {
		return err
	}
	fmt.Fprintln(w, successLinef("uninstalled", "auto-snapshot Stop hook removed from %s", path))
	return nil
}
