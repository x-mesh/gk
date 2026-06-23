package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/sessionaudit"
)

// gkHookMarker identifies the gk-managed PreToolUse hook inside settings.json:
// the install command always ends in `agents hook run`, so install (idempotent
// refresh) and uninstall (revert) can find exactly our entry and never touch
// the user's other hooks.
const gkHookMarker = "agents hook run"

// newAgentsHookCmd builds `gk agents hook`, the Claude Code enforcement lever
// that complements the `gk agents install` instruction block: it registers a
// PreToolUse(Bash) hook which routes raw git through git-kit at the point of
// action. Unlike the contract block (CLAUDE.md / AGENTS.md, every agent), the
// hook is Claude Code specific (settings.json).
func newAgentsHookCmd() *cobra.Command {
	hook := &cobra.Command{
		Use:   "hook",
		Short: "Manage the Claude Code PreToolUse hook that steers raw git to git-kit",
		Long: `Install, remove, or inspect a Claude Code PreToolUse(Bash) hook that nudges
raw git toward git-kit at the moment a command runs — the enforcement companion
to the instruction block from ` + "`gk agents install`" + `.

The hook invokes ` + "`gk agents hook run`" + ` (this binary), which classifies the
pending command with the same mapping ` + "`gk session audit`" + ` uses. Two modes:
warn (default — the tool still runs, a note is surfaced to the agent) and block
(the raw-git call is denied so the agent retries with git-kit). Read-only
plumbing (rev-parse, config, …) and commands already on git-kit pass through.

  gk agents hook install                 register in the repo's .claude/settings.json (warn)
  gk agents hook install --mode block    deny covered raw git instead of warning
  gk agents hook install --global        register in ~/.claude/settings.json (all projects)
  gk agents hook uninstall               remove the gk-managed hook (revert)
  gk agents hook status                  report install state for local + global

settings.json edits are surgical: only the gk entry is added or removed, the
rest of the file (other hooks, all settings) is preserved byte-for-byte, a .bak
is written first, and --dry-run previews without writing.`,
	}

	run := &cobra.Command{
		Use:    "run",
		Short:  "PreToolUse hook handler — reads the tool call on stdin, emits the decision",
		Hidden: true,
		RunE:   runAgentsHookRun,
	}
	run.Flags().Bool("warn", false, "warn instead of block (surface a note but let the command run)")
	hook.AddCommand(run)

	install := &cobra.Command{
		Use:   "install",
		Short: "Register the PreToolUse hook in settings.json",
		RunE:  runAgentsHookInstall,
	}
	install.Flags().Bool("global", false, "install into ~/.claude/settings.json instead of the repo's .claude/settings.json")
	install.Flags().String("mode", "warn", "block | warn — deny covered raw git, or just surface a note")
	install.Flags().Bool("dry-run", false, "preview the change without writing")
	hook.AddCommand(install)

	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the gk-managed PreToolUse hook (revert)",
		RunE:  runAgentsHookUninstall,
	}
	uninstall.Flags().Bool("global", false, "uninstall from ~/.claude/settings.json instead of the repo root")
	uninstall.Flags().Bool("dry-run", false, "preview the change without writing")
	hook.AddCommand(uninstall)

	hook.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Report whether the hook is installed (local + global)",
		RunE:  runAgentsHookStatus,
	})
	return hook
}

// --- handler (gk agents hook run) ---

// runAgentsHookRun is the hook handler Claude Code invokes before each Bash
// call. It is fail-open by contract: any problem (unreadable stdin, non-Bash
// tool, empty command, command git-kit does not cover) emits nothing and exits
// 0, so it never blocks real work.
func runAgentsHookRun(cmd *cobra.Command, _ []string) error {
	warn, _ := cmd.Flags().GetBool("warn")
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil
	}
	if gjson.GetBytes(data, "tool_name").String() != "Bash" {
		return nil
	}
	command := strings.TrimSpace(gjson.GetBytes(data, "tool_input.command").String())
	if command == "" {
		return nil
	}
	res := sessionaudit.Hint(command)
	if !res.Covered {
		return nil // defer to the normal permission flow
	}
	msg := hookMessage(res)
	if warn {
		return emitHookDecision(cmd.OutOrStdout(), "defer", "", msg)
	}
	return emitHookDecision(cmd.OutOrStdout(), "deny", msg, "")
}

func hookMessage(res sessionaudit.HintResult) string {
	m := fmt.Sprintf("git-kit covers %q — use %s instead of raw git.", res.Matched, strings.Join(res.CoveredBy, " / "))
	if res.Suggestion != "" {
		m += " " + res.Suggestion
	}
	return m
}

// emitHookDecision writes the Claude Code PreToolUse decision JSON to stdout.
// permissionDecision "deny" blocks with the reason; "defer" leaves the normal
// flow intact and only injects additionalContext for the agent to read.
func emitHookDecision(w io.Writer, decision, reason, additionalContext string) error {
	type spec struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
		AdditionalContext        string `json:"additionalContext,omitempty"`
	}
	payload := struct {
		HookSpecificOutput spec `json:"hookSpecificOutput"`
	}{spec{
		HookEventName:            "PreToolUse",
		PermissionDecision:       decision,
		PermissionDecisionReason: reason,
		AdditionalContext:        additionalContext,
	}}
	return json.NewEncoder(w).Encode(payload)
}

// --- install / uninstall / status ---

type hookActionJSON struct {
	Schema  int    `json:"schema"`
	Path    string `json:"path"`
	Scope   string `json:"scope"`  // local | global
	Action  string `json:"action"` // installed | updated | unchanged | removed | absent | dry-run
	Mode    string `json:"mode,omitempty"`
	Command string `json:"command,omitempty"`
}

func runAgentsHookInstall(cmd *cobra.Command, _ []string) error {
	global, _ := cmd.Flags().GetBool("global")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	mode, _ := cmd.Flags().GetString("mode")
	if mode != "block" && mode != "warn" {
		return fmt.Errorf("gk agents hook install: --mode must be block or warn, got %q", mode)
	}

	path, scope, err := claudeSettingsPath(cmd, global)
	if err != nil {
		return err
	}
	data, err := readSettings(path)
	if err != nil {
		return err
	}

	hookCmd := hookCommandString(mode == "warn")
	stripped, existed := stripGKHookEntries(data)
	entry := map[string]any{
		"matcher": "Bash",
		"hooks":   []map[string]any{{"type": "command", "command": hookCmd}},
	}
	updated, err := sjson.SetBytes(stripped, "hooks.PreToolUse.-1", entry)
	if err != nil {
		return fmt.Errorf("gk agents hook install: edit settings: %w", err)
	}

	action := "installed"
	if existed > 0 {
		action = "updated"
	}
	if dryRun {
		action = "dry-run"
	} else if err := writeSettings(path, updated); err != nil {
		return err
	}

	return emitAgentResultHuman(cmd, hookActionJSON{
		Schema: 1, Path: path, Scope: scope, Action: action, Mode: mode, Command: hookCmd,
	}, fmt.Sprintf("%s: %s PreToolUse hook (%s mode) in %s", action, scopeLabel(scope), mode, path))
}

func runAgentsHookUninstall(cmd *cobra.Command, _ []string) error {
	global, _ := cmd.Flags().GetBool("global")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	path, scope, err := claudeSettingsPath(cmd, global)
	if err != nil {
		return err
	}
	data, err := readSettings(path)
	if err != nil {
		return err
	}

	stripped, removed := stripGKHookEntries(data)
	action := "removed"
	if removed == 0 {
		action = "absent"
	}
	if action == "removed" && !dryRun {
		if err := writeSettings(path, stripped); err != nil {
			return err
		}
	}
	if dryRun && action == "removed" {
		action = "dry-run"
	}

	return emitAgentResultHuman(cmd, hookActionJSON{
		Schema: 1, Path: path, Scope: scope, Action: action,
	}, fmt.Sprintf("%s: gk PreToolUse hook in %s", action, path))
}

type hookStatusFileJSON struct {
	Path      string `json:"path"`
	Scope     string `json:"scope"`
	Installed bool   `json:"installed"`
	Mode      string `json:"mode,omitempty"`
}

func runAgentsHookStatus(cmd *cobra.Command, _ []string) error {
	out := struct {
		Schema int                  `json:"schema"`
		Files  []hookStatusFileJSON `json:"files"`
	}{Schema: 1}

	targets := []struct {
		global bool
	}{{false}, {true}}
	for _, t := range targets {
		path, scope, err := claudeSettingsPath(cmd, t.global)
		if err != nil {
			continue
		}
		data, _ := readSettings(path) // absent → "{}"
		installed, mode := gkHookState(data)
		out.Files = append(out.Files, hookStatusFileJSON{Path: path, Scope: scope, Installed: installed, Mode: mode})
	}

	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), out)
	}
	w := cmd.OutOrStdout()
	for _, f := range out.Files {
		state := "not installed"
		if f.Installed {
			state = "installed (" + f.Mode + ")"
		}
		fmt.Fprintf(w, "%-7s %s — %s\n", scopeLabel(f.Scope), f.Path, state)
	}
	return nil
}

// --- settings.json helpers ---

// claudeSettingsPath resolves the settings.json to edit: the repo's
// .claude/settings.json by default (checked into git, scoped to this project)
// or ~/.claude/settings.json (or $CLAUDE_CONFIG_DIR) with --global.
func claudeSettingsPath(cmd *cobra.Command, global bool) (path, scope string, err error) {
	if global {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", "", fmt.Errorf("gk agents hook: cannot resolve home directory: %w", herr)
		}
		dir := os.Getenv("CLAUDE_CONFIG_DIR")
		if dir == "" {
			dir = filepath.Join(home, ".claude")
		}
		return filepath.Join(dir, "settings.json"), "global", nil
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	rootOut, _, rerr := runner.Run(cmd.Context(), "rev-parse", "--show-toplevel")
	if rerr != nil {
		return "", "", fmt.Errorf("gk agents hook: not inside a git repository (use --global for ~/.claude/settings.json)")
	}
	root := strings.TrimSpace(string(rootOut))
	return filepath.Join(root, ".claude", "settings.json"), "local", nil
}

// readSettings returns the file bytes, or "{}" when the file does not exist.
// An existing but unparseable settings.json is an error: we never clobber a
// file we cannot understand.
func readSettings(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []byte("{}"), nil
	}
	if err != nil {
		return nil, fmt.Errorf("gk agents hook: read %s: %w", path, err)
	}
	if !gjson.ValidBytes(data) {
		return nil, fmt.Errorf("gk agents hook: %s is not valid JSON — refusing to edit", path)
	}
	return data, nil
}

// writeSettings backs up the existing file, preserves its permissions, and
// writes the new content. Parent directories are created for a fresh install.
func writeSettings(path string, data []byte) error {
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
		if existing, rerr := os.ReadFile(path); rerr == nil {
			_ = os.WriteFile(path+".bak", existing, perm)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gk agents hook: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("gk agents hook: write %s: %w", path, err)
	}
	return nil
}

// stripGKHookEntries removes every PreToolUse entry whose command is the
// gk-managed hook, deleting from the highest index down so earlier indices stay
// valid. It also prunes a now-empty PreToolUse array (and hooks object) so an
// uninstall leaves no stray scaffolding. Returns the modified bytes and how
// many entries were removed.
func stripGKHookEntries(data []byte) ([]byte, int) {
	pre := gjson.GetBytes(data, "hooks.PreToolUse")
	if !pre.IsArray() {
		return data, 0
	}
	arr := pre.Array()
	removed := 0
	for i := len(arr) - 1; i >= 0; i-- {
		if entryIsGKHook(arr[i]) {
			if next, err := sjson.DeleteBytes(data, fmt.Sprintf("hooks.PreToolUse.%d", i)); err == nil {
				data = next
				removed++
			}
		}
	}
	if removed > 0 {
		if rest := gjson.GetBytes(data, "hooks.PreToolUse"); rest.IsArray() && len(rest.Array()) == 0 {
			data, _ = sjson.DeleteBytes(data, "hooks.PreToolUse")
			if hooks := gjson.GetBytes(data, "hooks"); hooks.IsObject() && len(hooks.Map()) == 0 {
				data, _ = sjson.DeleteBytes(data, "hooks")
			}
		}
	}
	return data, removed
}

func entryIsGKHook(entry gjson.Result) bool {
	gk := false
	entry.Get("hooks").ForEach(func(_, h gjson.Result) bool {
		if strings.Contains(h.Get("command").String(), gkHookMarker) {
			gk = true
			return false
		}
		return true
	})
	return gk
}

// gkHookState reports whether the gk hook is present and, if so, its mode
// (block/warn, read back from the `--warn` flag in the stored command).
func gkHookState(data []byte) (installed bool, mode string) {
	pre := gjson.GetBytes(data, "hooks.PreToolUse")
	if !pre.IsArray() {
		return false, ""
	}
	for _, entry := range pre.Array() {
		found := ""
		entry.Get("hooks").ForEach(func(_, h gjson.Result) bool {
			c := h.Get("command").String()
			if strings.Contains(c, gkHookMarker) {
				found = c
				return false
			}
			return true
		})
		if found != "" {
			if strings.Contains(found, "--warn") {
				return true, "warn"
			}
			return true, "block"
		}
	}
	return false, ""
}

// hookCommandString is the settings.json command that invokes this binary's
// handler. The absolute path (os.Executable) makes it robust to PATH/aliases;
// it falls back to the bare name only if the path cannot be resolved.
func hookCommandString(warn bool) string {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "git-kit"
	}
	cmd := fmt.Sprintf("%q agents hook run", self)
	if warn {
		cmd += " --warn"
	}
	return cmd
}

func scopeLabel(scope string) string {
	if scope == "global" {
		return "global"
	}
	return "local"
}

// emitAgentResultHuman emits the agent envelope under --json/GK_AGENT, else the
// human line — the shared shape for the hook install/uninstall results.
func emitAgentResultHuman(cmd *cobra.Command, payload any, human string) error {
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), payload)
	}
	fmt.Fprintln(cmd.OutOrStdout(), human)
	return nil
}
