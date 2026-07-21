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

// gkHookEvents lists the Claude Code hook events gk manages: PreToolUse (raw
// git steering, matcher "Bash"), UserPromptSubmit (git-orientation prefetch,
// no matcher), and Stop (the opt-in `gk commit --wip` checkpoint). All carry
// gkHookMarker in their command string, so install/uninstall/status can walk
// the arrays uniformly.
var gkHookEvents = []string{"PreToolUse", "UserPromptSubmit", "Stop"}

// Hook timeouts keep Claude responsive if a hook binary or its filesystem
// probe stalls. The handlers are normally millisecond-scale.
const (
	hookPreToolUseTimeoutSeconds = 5
	hookPromptTimeoutSeconds     = 5
)

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
  gk agents hook install --no-prompt     skip the UserPromptSubmit prefetch hook
  gk agents hook install --stop-commit   also checkpoint the session on Stop
  gk agents hook install --stop-only     ONLY the Stop checkpoint; leave the rest as-is
  gk agents hook install --global        register in ~/.claude/settings.json (all projects)
  gk agents hook uninstall               remove the gk-managed hooks (revert)
  gk agents hook status                  report install state for local + global

install also registers a UserPromptSubmit hook (` + "`gk agents hook run --prompt`" + `)
that prefetches git orientation for a detected git-action prompt, so the agent
finds it already in context instead of probing for it — opt out with
--no-prompt.

--stop-commit adds a third hook (` + "`gk agents hook run --stop`" + `) that runs
` + "`gk commit --wip`" + ` when a session ends with uncommitted work, leaving one
` + "`WIP(scope): <summary>`" + ` checkpoint a later ` + "`gk commit`" + ` folds into real
commits. It is opt-in because, unlike the other two, it writes to git history.
The handler is fail-open (no repo, no provider, a timeout → it does nothing)
and skips itself when stop_hook_active is set, so it cannot loop.

--stop-only registers the checkpoint and NOTHING else, leaving any existing
PreToolUse/UserPromptSubmit entries exactly as they are. Use it when the
steering hook already lives in another scope: Claude merges PreToolUse across
project and global settings, so installing it in both double-fires it.

All three hooks share one gk-managed marker, so uninstall removes them together.

settings.json edits are surgical: only the gk entries are added or removed, the
rest of the file (other hooks, all settings) is preserved byte-for-byte, a .bak
is written first, and --dry-run previews without writing.`,
	}

	run := &cobra.Command{
		Use:    "run",
		Short:  "PreToolUse/UserPromptSubmit hook handler — reads the event on stdin, emits the decision",
		Hidden: true,
		RunE:   runAgentsHookRunDispatch,
	}
	run.Flags().Bool("warn", false, "warn instead of block (surface a note but let the command run)")
	run.Flags().String("mode", "", "block | collapse | warn — enforcement level (overrides --warn)")
	run.Flags().Bool("prompt", false, "UserPromptSubmit mode: prefetch git orientation for a detected git-action prompt")
	run.Flags().Bool("stop", false, "Stop mode: write a `gk commit --wip` checkpoint when the session ends with uncommitted work")
	hook.AddCommand(run)

	install := &cobra.Command{
		Use:   "install",
		Short: "Register the PreToolUse hook in settings.json",
		RunE:  runAgentsHookInstall,
	}
	install.Flags().Bool("global", false, "install into ~/.claude/settings.json instead of the repo's .claude/settings.json")
	install.Flags().String("mode", "warn", "block | collapse | warn — deny all covered raw git, deny only a repeated probe, or just surface a note")
	install.Flags().Bool("no-prompt", false, "skip the UserPromptSubmit prefetch hook (PreToolUse only)")
	install.Flags().Bool("stop-commit", false, "also register a Stop hook that runs `gk commit --wip` when a session ends with uncommitted work (opt-in: it writes to history)")
	install.Flags().Bool("stop-only", false, "register ONLY the Stop checkpoint hook, leaving any existing PreToolUse/UserPromptSubmit entries untouched (implies --stop-commit; use when those already live in another scope)")
	install.Flags().Bool("dry-run", false, "preview the change without writing")
	hook.AddCommand(install)

	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the gk-managed PreToolUse and UserPromptSubmit hooks (revert)",
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

// runAgentsHookRunDispatch routes `gk agents hook run` to the PreToolUse
// handler (default) or the UserPromptSubmit handler (--prompt). Both Claude
// Code hook events share this one binary entry point (see hookCommandString
// for the settings.json command line the PreToolUse install writes; the
// UserPromptSubmit registration adds --prompt the same way), so the flag is
// what decides which payload shape stdin holds.
func runAgentsHookRunDispatch(cmd *cobra.Command, args []string) error {
	if prompt, _ := cmd.Flags().GetBool("prompt"); prompt {
		return runAgentsHookPrompt(cmd, args)
	}
	if stop, _ := cmd.Flags().GetBool("stop"); stop {
		return runAgentsHookStop(cmd, args)
	}
	return runAgentsHookRun(cmd, args)
}

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
	if decision == "" && addl == "" {
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
		return "", "", hookNoteText(res, nudge)
	}
	if res.Covered && !hintSeen {
		return "", "", hookNoteText(res, nil)
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

// emitHookDecision writes the Claude Code PreToolUse output JSON to stdout.
// Only deny carries permissionDecision: an advisory must not trigger a
// permission flow, especially in a headless/background agent. Its
// additionalContext is enough to surface the guidance while allowing the
// command to proceed.
func emitHookDecision(w io.Writer, decision, reason, additionalContext string) error {
	type spec struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision,omitempty"`
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
	Schema        int    `json:"schema"`
	Path          string `json:"path"`
	Scope         string `json:"scope"`  // local | global
	Action        string `json:"action"` // installed | updated | unchanged | removed | absent | dry-run
	Mode          string `json:"mode,omitempty"`
	Command       string `json:"command,omitempty"`
	PromptAction  string `json:"promptAction,omitempty"`  // installed | updated | removed | skipped | dry-run
	PromptCommand string `json:"promptCommand,omitempty"` // set only when the prefetch hook is (re)installed
	StopAction    string `json:"stopAction,omitempty"`    // installed | updated | removed | skipped | dry-run
	StopCommand   string `json:"stopCommand,omitempty"`   // set only when the checkpoint hook is (re)installed
}

func runAgentsHookInstall(cmd *cobra.Command, _ []string) error {
	global, _ := cmd.Flags().GetBool("global")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	mode, _ := cmd.Flags().GetString("mode")
	noPrompt, _ := cmd.Flags().GetBool("no-prompt")
	stopCommit, _ := cmd.Flags().GetBool("stop-commit")
	stopOnly, _ := cmd.Flags().GetBool("stop-only")
	// --stop-only is "add the checkpoint, change nothing else": Claude merges
	// PreToolUse across project + global settings, so a repo-scoped install
	// would double-fire a steering hook that already lives globally.
	if stopOnly {
		stopCommit = true
	}
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

	// Under --stop-only the other two events are never stripped, so whatever
	// is registered for them (gk's own entry, or nothing) survives verbatim.
	stripped := data
	var preExisted, promptExisted int
	if !stopOnly {
		stripped, preExisted = stripGKHookEntriesForEvent(data, "PreToolUse")
		stripped, promptExisted = stripGKHookEntriesForEvent(stripped, "UserPromptSubmit")
	}
	stripped, stopExisted := stripGKHookEntriesForEvent(stripped, "Stop")

	updated := stripped
	var hookCmd, action string
	if stopOnly {
		action = "skipped"
	} else {
		hookCmd = hookCommandString(mode)
		updated, err = sjson.SetBytes(stripped, "hooks.PreToolUse.-1", map[string]any{
			"matcher": "Bash",
			"hooks":   []map[string]any{{"type": "command", "command": hookCmd, "timeout": hookPreToolUseTimeoutSeconds}},
		})
		if err != nil {
			return fmt.Errorf("gk agents hook install: edit settings: %w", err)
		}
		action = "installed"
		if preExisted > 0 {
			action = "updated"
		}
	}

	var promptCmd, promptAction string
	switch {
	case stopOnly:
		promptAction = "skipped"
	case noPrompt && promptExisted > 0:
		promptAction = "removed"
	case noPrompt:
		promptAction = "skipped"
	default:
		promptCmd = promptHookCommandString()
		updated, err = sjson.SetBytes(updated, "hooks.UserPromptSubmit.-1", map[string]any{
			"hooks": []map[string]any{{"type": "command", "command": promptCmd, "timeout": hookPromptTimeoutSeconds}},
		})
		if err != nil {
			return fmt.Errorf("gk agents hook install: edit settings: %w", err)
		}
		promptAction = "installed"
		if promptExisted > 0 {
			promptAction = "updated"
		}
	}

	// The Stop checkpoint hook is opt-in: unlike the other two it WRITES to
	// git history, so it must never appear from a plain `hook install`.
	var stopCmd, stopAction string
	switch {
	case !stopCommit && stopExisted > 0:
		stopAction = "removed"
	case !stopCommit:
		stopAction = "skipped"
	default:
		stopCmd = stopHookCommandString()
		updated, err = sjson.SetBytes(updated, "hooks.Stop.-1", map[string]any{
			"hooks": []map[string]any{{"type": "command", "command": stopCmd, "timeout": stopHookTimeoutSeconds}},
		})
		if err != nil {
			return fmt.Errorf("gk agents hook install: edit settings: %w", err)
		}
		stopAction = "installed"
		if stopExisted > 0 {
			stopAction = "updated"
		}
	}

	if dryRun {
		if action != "skipped" {
			action = "dry-run"
		}
		if promptAction != "skipped" {
			promptAction = "dry-run"
		}
		if stopAction != "skipped" {
			stopAction = "dry-run"
		}
	} else if err := writeSettings(path, updated); err != nil {
		return err
	}

	var human string
	if stopOnly {
		human = fmt.Sprintf("%s: %s Stop checkpoint hook (gk commit --wip) in %s; PreToolUse/UserPromptSubmit left untouched (--stop-only)",
			stopAction, scopeLabel(scope), path)
		return emitAgentResultHuman(cmd, hookActionJSON{
			Schema: 1, Path: path, Scope: scope, Action: action,
			PromptAction: promptAction,
			StopAction:   stopAction, StopCommand: stopCmd,
		}, human)
	}

	human = fmt.Sprintf("%s: %s PreToolUse hook (%s mode) in %s", action, scopeLabel(scope), mode, path)
	switch promptAction {
	case "installed", "updated", "dry-run":
		human += fmt.Sprintf("; %s UserPromptSubmit prefetch hook", promptAction)
	case "removed":
		human += "; removed the existing UserPromptSubmit prefetch hook (--no-prompt)"
	case "skipped":
		human += "; UserPromptSubmit prefetch hook skipped (--no-prompt)"
	}
	switch stopAction {
	case "installed", "updated", "dry-run":
		human += fmt.Sprintf("; %s Stop checkpoint hook (gk commit --wip)", stopAction)
	case "removed":
		human += "; removed the existing Stop checkpoint hook (--stop-commit not set)"
	}

	return emitAgentResultHuman(cmd, hookActionJSON{
		Schema: 1, Path: path, Scope: scope, Action: action, Mode: mode, Command: hookCmd,
		PromptAction: promptAction, PromptCommand: promptCmd,
		StopAction: stopAction, StopCommand: stopCmd,
	}, human)
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
	}, fmt.Sprintf("%s: gk hooks (PreToolUse + UserPromptSubmit + Stop) in %s", action, path))
}

type hookStatusFileJSON struct {
	Path            string `json:"path"`
	Scope           string `json:"scope"`
	Installed       bool   `json:"installed"`
	Mode            string `json:"mode,omitempty"`
	PromptInstalled bool   `json:"promptInstalled"`
	StopInstalled   bool   `json:"stopInstalled"`
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
		promptInstalled := gkPromptHookInstalled(data)
		stopInstalled := gkStopHookInstalled(data)
		out.Files = append(out.Files, hookStatusFileJSON{
			Path: path, Scope: scope, Installed: installed, Mode: mode,
			PromptInstalled: promptInstalled, StopInstalled: stopInstalled,
		})
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
		promptState := "not installed"
		if f.PromptInstalled {
			promptState = "installed"
		}
		stopState := "not installed"
		if f.StopInstalled {
			stopState = "installed"
		}
		fmt.Fprintf(w, "%-7s %s — PreToolUse: %s, UserPromptSubmit: %s, Stop: %s\n",
			scopeLabel(f.Scope), f.Path, state, promptState, stopState)
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

// stripGKHookEntriesForEvent removes every hooks.<event> entry whose command
// is the gk-managed hook, deleting from the highest index down so earlier
// indices stay valid. It also prunes a now-empty <event> array. Returns the
// modified bytes and how many entries were removed from this event only.
func stripGKHookEntriesForEvent(data []byte, event string) ([]byte, int) {
	path := "hooks." + event
	pre := gjson.GetBytes(data, path)
	if !pre.IsArray() {
		return data, 0
	}
	arr := pre.Array()
	removed := 0
	for i := len(arr) - 1; i >= 0; i-- {
		if entryIsGKHook(arr[i]) {
			if next, err := sjson.DeleteBytes(data, fmt.Sprintf("%s.%d", path, i)); err == nil {
				data = next
				removed++
			}
		}
	}
	if removed > 0 {
		if rest := gjson.GetBytes(data, path); rest.IsArray() && len(rest.Array()) == 0 {
			data, _ = sjson.DeleteBytes(data, path)
		}
	}
	return data, removed
}

// stripGKHookEntries removes gk-managed entries from every event gk manages
// (see gkHookEvents — PreToolUse and UserPromptSubmit), then prunes a
// now-empty hooks object so an uninstall leaves no stray scaffolding. Returns
// the modified bytes and the total number of entries removed across events.
func stripGKHookEntries(data []byte) ([]byte, int) {
	total := 0
	for _, event := range gkHookEvents {
		var removed int
		data, removed = stripGKHookEntriesForEvent(data, event)
		total += removed
	}
	if total > 0 {
		if hooks := gjson.GetBytes(data, "hooks"); hooks.IsObject() && len(hooks.Map()) == 0 {
			data, _ = sjson.DeleteBytes(data, "hooks")
		}
	}
	return data, total
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

// gkHookState reports whether the gk PreToolUse hook is present and, if so,
// its mode (block/warn/collapse, read back from the stored command).
func gkHookState(data []byte) (installed bool, mode string) {
	return gkEventHookMode(data, "PreToolUse")
}

// gkPromptHookInstalled reports whether the gk UserPromptSubmit prefetch hook
// is registered. Unlike PreToolUse it carries no enforcement mode — the
// prefetch handler only ever injects additionalContext, so presence is all
// status needs.
func gkPromptHookInstalled(data []byte) bool {
	installed, _ := gkEventHookMode(data, "UserPromptSubmit")
	return installed
}

// gkEventHookMode scans hooks.<event> for the gk-managed entry and, if found,
// reads its mode back from the `--mode`/`--warn` flags in the stored command
// (meaningless for UserPromptSubmit, whose command carries neither — callers
// there only look at the bool).
func gkEventHookMode(data []byte, event string) (installed bool, mode string) {
	pre := gjson.GetBytes(data, "hooks."+event)
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
			default: // "--mode block", a legacy bare install, or a --prompt entry
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

// promptHookCommandString is the settings.json command that invokes this
// binary's UserPromptSubmit handler (the git-orientation prefetch). Unlike
// hookCommandString it carries no --mode: the prefetch handler only ever
// injects additionalContext, it never denies.
func promptHookCommandString() string {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "git-kit"
	}
	return fmt.Sprintf("%q agents hook run --prompt", self)
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
