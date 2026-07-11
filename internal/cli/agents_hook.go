package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// Hook enforcement levels, from least to most strict:
//
//	warn     — never deny; surface an advisory note the agent may ignore.
//	collapse — deny only a confirmed re-probe (a second same-group raw-git
//	           command that git-kit context/… would have folded into one call),
//	           but keep a lone covered command advisory. The targeted lever for
//	           the biggest turn sink (repeated orientation probes) without
//	           blocking a legitimate one-off `git status`.
//	block    — deny any covered raw-git command (and any re-probe).
const (
	hookModeWarn     = "warn"
	hookModeCollapse = "collapse"
	hookModeBlock    = "block"
)

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
pending command with the same mapping ` + "`gk session audit`" + ` uses. Three modes:
warn (default — the tool still runs, a note is surfaced to the agent), collapse
(a lone covered command is only advised, but a second same-group probe — the
repeated orientation the audit shows is the biggest turn sink — is denied so the
agent folds it into one git-kit call), and block (any covered raw-git call is
denied). Read-only plumbing (rev-parse, config, …) and commands already on
git-kit pass through.

  gk agents hook install                 register in the repo's .claude/settings.json (warn)
  gk agents hook install --mode collapse deny only a repeated same-group probe
  gk agents hook install --mode block    deny every covered raw git
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
	run.Flags().String("mode", "", "block | collapse | warn — enforcement level (overrides --warn)")
	hook.AddCommand(run)

	install := &cobra.Command{
		Use:   "install",
		Short: "Register the PreToolUse hook in settings.json",
		RunE:  runAgentsHookInstall,
	}
	install.Flags().Bool("global", false, "install into ~/.claude/settings.json instead of the repo's .claude/settings.json")
	install.Flags().String("mode", "warn", "block | collapse | warn — deny all covered raw git, deny only a repeated probe, or just surface a note")
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
	mode := hookRunMode(cmd)
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
	tail, nudge := hookTailAndNudge(data, command, res.Covered)
	hintSeen := res.Covered && len(tail) > 0 && bytes.Contains(tail, []byte(hookHintMarker(res)))
	decision, reason, addl := hookDecide(mode, res, nudge, hintSeen)
	if decision == "" {
		return nil // nothing to say → defer to the normal permission flow
	}
	return emitHookDecision(cmd.OutOrStdout(), decision, reason, addl)
}

// hookRunMode resolves the enforcement level from the stored hook command.
// --mode wins; a legacy --warn maps to warn; a bare invocation defaults to
// block (the pre-mode behavior). Reading an undefined flag is harmless — it
// yields the zero value, so callers that only wire --warn still work.
func hookRunMode(cmd *cobra.Command) string {
	if m, _ := cmd.Flags().GetString("mode"); m != "" {
		return m
	}
	if warn, _ := cmd.Flags().GetBool("warn"); warn {
		return hookModeWarn
	}
	return hookModeBlock
}

// hookDecide computes the PreToolUse decision for a resolved mode given the
// single-command hint, the optional real-time collapse signal, and whether this
// kind's guidance is already in the session transcript (hintSeen). It is the
// one place the three modes' semantics live, kept pure so it is unit-testable
// without stdin/transcript plumbing. Returns ("","","") when there is nothing
// to emit (fail-open: the command is fine, or needs no nudge).
//
// hintSeen suppresses only the hint-only advisory — the same kind was already
// nudged once this session, so repeating the identical text just burns context
// tokens. A collapse nudge is turn-local and actionable, so it is never
// deduped; deny decisions are enforcement, not advice, so they always carry a
// reason.
func hookDecide(mode string, res sessionaudit.HintResult, nudge *sessionaudit.CollapseNudge, hintSeen bool) (decision, reason, additionalContext string) {
	denyCollapse := nudge != nil && (mode == hookModeCollapse || mode == hookModeBlock)
	denySingle := res.Covered && mode == hookModeBlock
	if denyCollapse || denySingle {
		return "deny", hookDenyReason(res, nudge), ""
	}
	if nudge != nil {
		return "defer", "", hookNoteText(res, nudge)
	}
	if res.Covered && !hintSeen {
		return "defer", "", hookNoteText(res, nil)
	}
	return "", "", ""
}

// hookDenyReason is the short permissionDecisionReason for a deny. Unlike the
// advisory (which teaches), a deny only needs to name the replacement — the
// full hookNoteText would be re-injected on every blocked retry, so keep it
// tight (~120 B).
func hookDenyReason(res sessionaudit.HintResult, nudge *sessionaudit.CollapseNudge) string {
	if nudge != nil {
		return fmt.Sprintf("Blocked repeated %s probe — run %s instead.", nudge.Group, nudge.GkCommand)
	}
	if len(res.CoveredBy) > 0 {
		return fmt.Sprintf("Blocked covered raw git — run %s instead.", res.CoveredBy[0])
	}
	return "Blocked covered raw git — use git-kit instead."
}

// hookNoteText joins the single-command hint and the collapse nudge into one
// message (either may be empty), shared by the deny reason and the advisory
// additionalContext so both modes phrase it identically.
func hookNoteText(res sessionaudit.HintResult, nudge *sessionaudit.CollapseNudge) string {
	var parts []string
	if res.Covered {
		parts = append(parts, hookMessage(res))
	}
	if nudge != nil {
		parts = append(parts, collapseMessage(nudge))
	}
	return strings.Join(parts, " ")
}

// hookTranscriptTailBytes bounds how much of the live transcript the hook
// reads: only the recent tail matters, and the hook fires on every Bash call,
// so it must stay cheap on a multi-megabyte session log.
const hookTranscriptTailBytes = 256 * 1024

// hookRaceRetryWindow/hookRaceRetryDelay/hookRaceRetryAttempts absorb a narrow
// race with the harness's own transcript writer: Claude Code invokes this
// hook immediately before running the pending Bash call, but the immediately
// preceding turn's JSONL append can still be in flight when this fire's first
// tail read lands — a covered command then sees no prior turn and a real
// reprobe silently passes as a lone command (observed live: two raw git
// context probes fired back-to-back went unblocked; the same pair replayed
// from the settled transcript denied correctly). Retrying is gated on the
// transcript having been touched moments ago, so an old/settled transcript —
// the overwhelming majority of fires, and every fire for an uncovered command
// — never pays the delay.
const (
	hookRaceRetryWindow   = 200 * time.Millisecond
	hookRaceRetryDelay    = 8 * time.Millisecond
	hookRaceRetryAttempts = 3
)

// hookShouldRetryRace reports whether a transcript modified at modTime is
// recent enough that a missing prior turn might be an in-flight write rather
// than a genuine absence.
func hookShouldRetryRace(modTime, now time.Time) bool {
	return !modTime.IsZero() && now.Sub(modTime) <= hookRaceRetryWindow
}

// hookTailAndNudge reads the transcript tail (Claude passes its path on
// stdin) and derives the collapse nudge for command — both the collapse nudge
// and the advisory dedupe share the returned tail. When the pending command
// is covered and the first read finds no nudge, it retries briefly IF the
// transcript was modified within hookRaceRetryWindow (see above); otherwise
// it returns immediately. Fail-open throughout: absent path or read error →
// (nil, nil).
func hookTailAndNudge(stdin []byte, command string, covered bool) ([]byte, *sessionaudit.CollapseNudge) {
	tp := strings.TrimSpace(gjson.GetBytes(stdin, "transcript_path").String())
	if tp == "" {
		return nil, nil
	}
	tail, nudge := tailAndNudge(tp, command)
	if !covered || nudge != nil {
		return tail, nudge
	}
	for attempt := 0; attempt < hookRaceRetryAttempts; attempt++ {
		fi, err := os.Stat(tp)
		if err != nil || !hookShouldRetryRace(fi.ModTime(), time.Now()) {
			break
		}
		time.Sleep(hookRaceRetryDelay)
		tail, nudge = tailAndNudge(tp, command)
		if nudge != nil {
			break
		}
	}
	return tail, nudge
}

// tailAndNudge reads the transcript tail once and derives the collapse nudge
// from it. Fail-open: a read error yields (nil, nil).
func tailAndNudge(tp, command string) ([]byte, *sessionaudit.CollapseNudge) {
	tail, err := tailFile(tp, hookTranscriptTailBytes)
	if err != nil {
		return nil, nil
	}
	return tail, collapseNudgeFromTail(tail, command)
}

// collapseNudgeFromTail reports whether the pending command continues a recent
// same-group raw run recorded in the transcript tail. The last allocated turn
// rides along so the nudge honors real turn distance — probes separated by
// Read/Edit turns (which emit no events) must not read as adjacent.
func collapseNudgeFromTail(tail []byte, command string) *sessionaudit.CollapseNudge {
	if len(tail) == 0 {
		return nil
	}
	recent, lastTurn := sessionaudit.SessionTurnsWithLast(tail)
	return sessionaudit.CollapseNudgeFor(command, recent, lastTurn, sessionaudit.CollapseLookback)
}

// collapseMessage phrases the real-time nudge for the agent.
func collapseMessage(n *sessionaudit.CollapseNudge) string {
	turns := "the last turn"
	if n.PriorTurns > 1 {
		turns = fmt.Sprintf("the last %d turns", n.PriorTurns)
	}
	return fmt.Sprintf("You ran a raw %s command in %s — fold it and this one into a single %s call to save a turn.",
		n.Group, turns, n.GkCommand)
}

// tailFile returns up to the last n bytes of a file, dropping a partial leading
// line so the result is whole JSONL records.
func tailFile(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if fi.Size() > n {
		start = fi.Size() - n
	}
	buf := make([]byte, fi.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	if start > 0 {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, nil
}

func hookMessage(res sessionaudit.HintResult) string {
	m := fmt.Sprintf("git-kit covers %q — %s", res.Matched, hookHintMarker(res))
	if res.Suggestion != "" {
		m += " " + res.Suggestion
	}
	return m
}

// hookHintMarker is the kind-stable sentence of hookMessage (CoveredBy comes
// from the kind's finding spec, unlike Matched which varies per command). Once
// an advisory is injected, this sentence is recorded in the transcript, so a
// plain substring probe over the tail detects "this kind was already nudged
// this session" without any state file. It deliberately contains no
// double-quote characters, so JSONL escaping in the transcript cannot break
// the match.
func hookHintMarker(res sessionaudit.HintResult) string {
	return fmt.Sprintf("use %s instead of raw git.", strings.Join(res.CoveredBy, " / "))
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
	if mode != hookModeBlock && mode != hookModeCollapse && mode != hookModeWarn {
		return fmt.Errorf("gk agents hook install: --mode must be block, collapse, or warn, got %q", mode)
	}

	path, scope, err := claudeSettingsPath(cmd, global)
	if err != nil {
		return err
	}
	data, err := readSettings(path)
	if err != nil {
		return err
	}

	hookCmd := hookCommandString(mode)
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
			switch {
			case strings.Contains(found, "--mode "+hookModeCollapse):
				return true, hookModeCollapse
			case strings.Contains(found, "--mode "+hookModeWarn), strings.Contains(found, "--warn"):
				return true, hookModeWarn
			default: // "--mode block" or a legacy bare install
				return true, hookModeBlock
			}
		}
	}
	return false, ""
}

// hookCommandString is the settings.json command that invokes this binary's
// handler. The absolute path (os.Executable) makes it robust to PATH/aliases;
// it falls back to the bare name only if the path cannot be resolved. The mode
// is written as an explicit --mode flag so status readback (gkHookState) can
// report it and a re-install can round-trip it.
func hookCommandString(mode string) string {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "git-kit"
	}
	if mode == "" {
		mode = hookModeBlock
	}
	return fmt.Sprintf("%q agents hook run --mode %s", self, mode)
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
