package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/x-mesh/gk/internal/sessionaudit"
)

func hookRunOutput(t *testing.T, stdin string, warn bool) string {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().Bool("warn", warn, "")
	cmd.SetIn(strings.NewReader(stdin))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runAgentsHookRun(cmd, nil); err != nil {
		t.Fatalf("runAgentsHookRun: %v", err)
	}
	return buf.String()
}

func hookRunOutputMode(t *testing.T, stdin, mode string) string {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("mode", mode, "")
	cmd.SetIn(strings.NewReader(stdin))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runAgentsHookRun(cmd, nil); err != nil {
		t.Fatalf("runAgentsHookRun: %v", err)
	}
	return buf.String()
}

func TestAgentsHookRun_Decisions(t *testing.T) {
	// Covered raw git, block mode → deny with a reason.
	out := hookRunOutput(t, `{"tool_name":"Bash","tool_input":{"command":"git status --short"}}`, false)
	if gjson.Get(out, "hookSpecificOutput.permissionDecision").String() != "deny" {
		t.Errorf("block decision = %q, want deny: %s", gjson.Get(out, "hookSpecificOutput.permissionDecision").String(), out)
	}
	if !strings.Contains(gjson.Get(out, "hookSpecificOutput.permissionDecisionReason").String(), "git-kit context") {
		t.Errorf("deny reason missing suggestion: %s", out)
	}

	// Covered raw git, warn mode → defer with additionalContext, never denies.
	out = hookRunOutput(t, `{"tool_name":"Bash","tool_input":{"command":"git add ."}}`, true)
	if gjson.Get(out, "hookSpecificOutput.permissionDecision").String() != "defer" {
		t.Errorf("warn decision = %q, want defer: %s", gjson.Get(out, "hookSpecificOutput.permissionDecision").String(), out)
	}
	if !strings.Contains(gjson.Get(out, "hookSpecificOutput.additionalContext").String(), "git-kit commit") {
		t.Errorf("warn context missing suggestion: %s", out)
	}
}

func TestAgentsHookRun_CollapseNudge(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git status"}}]}}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"git log --oneline -5"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(tp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pending git diff --stat continues the recent context run → warn-mode defer
	// carries the real-time collapse nudge alongside the single-command hint.
	stdin := fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git diff --stat"}}`, tp)
	out := hookRunOutput(t, stdin, true)
	ctx := gjson.Get(out, "hookSpecificOutput.additionalContext").String()
	if !strings.Contains(ctx, "fold it and this one") || !strings.Contains(ctx, "git-kit context") {
		t.Errorf("expected collapse nudge in additionalContext, got: %s", out)
	}

	// A command with no recent same-group turn gets no collapse nudge.
	stdin = fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git worktree list"}}`, tp)
	out = hookRunOutput(t, stdin, true)
	if strings.Contains(gjson.Get(out, "hookSpecificOutput.additionalContext").String(), "fold it and this one") {
		t.Errorf("unrelated command must not get a collapse nudge: %s", out)
	}
}

func TestHookDecide(t *testing.T) {
	covered := sessionaudit.HintResult{
		Covered: true, CoveredBy: []string{"git-kit context"},
		Suggestion: "Use git-kit context …", Matched: "git status",
	}
	uncovered := sessionaudit.HintResult{}
	nudge := &sessionaudit.CollapseNudge{
		Group: "context", GkCommand: "git-kit context", PriorTurns: 1,
		Recent: []string{"git status"},
	}

	cases := []struct {
		name         string
		mode         string
		res          sessionaudit.HintResult
		nudge        *sessionaudit.CollapseNudge
		hintSeen     bool
		wantDecision string
	}{
		{"warn/single", hookModeWarn, covered, nil, false, "defer"},
		{"warn/reprobe", hookModeWarn, covered, nudge, false, "defer"},
		{"collapse/single-advisory", hookModeCollapse, covered, nil, false, "defer"},
		{"collapse/reprobe-denied", hookModeCollapse, covered, nudge, false, "deny"},
		{"block/single-denied", hookModeBlock, covered, nil, false, "deny"},
		{"block/reprobe-denied", hookModeBlock, covered, nudge, false, "deny"},
		{"any/nothing", hookModeBlock, uncovered, nil, false, ""},
		// hintSeen dedupes only the hint-only advisory; nudges and denies are
		// unaffected.
		{"warn/hint-seen-silent", hookModeWarn, covered, nil, true, ""},
		{"warn/hint-seen-nudge-still-fires", hookModeWarn, covered, nudge, true, "defer"},
		{"collapse/hint-seen-silent", hookModeCollapse, covered, nil, true, ""},
		{"block/hint-seen-still-denies", hookModeBlock, covered, nil, true, "deny"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, reason, addl := hookDecide(tc.mode, tc.res, tc.nudge, tc.hintSeen)
			if decision != tc.wantDecision {
				t.Fatalf("decision = %q, want %q", decision, tc.wantDecision)
			}
			switch decision {
			case "deny":
				if reason == "" || addl != "" {
					t.Errorf("deny must carry reason, no additionalContext: reason=%q addl=%q", reason, addl)
				}
			case "defer":
				if addl == "" || reason != "" {
					t.Errorf("defer must carry additionalContext, no reason: reason=%q addl=%q", reason, addl)
				}
			case "":
				if reason != "" || addl != "" {
					t.Errorf("no-op must be empty: reason=%q addl=%q", reason, addl)
				}
			}
		})
	}
}

func TestAgentsHookRun_CollapseModeDeniesReprobe(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git status"}}]}}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"git log --oneline -5"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(tp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// A lone covered command in collapse mode is advisory (defer), not blocked —
	// a one-off `git status` must stay cheap.
	single := `{"tool_name":"Bash","tool_input":{"command":"git status --short"}}`
	if out := hookRunOutputMode(t, single, hookModeCollapse); gjson.Get(out, "hookSpecificOutput.permissionDecision").String() != "defer" {
		t.Errorf("collapse mode, lone command should defer, got: %s", out)
	}

	// The second same-group probe (pending git diff --stat continues the context
	// run) is the wasteful pattern — collapse mode denies it.
	reprobe := fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git diff --stat"}}`, tp)
	out := hookRunOutputMode(t, reprobe, hookModeCollapse)
	if gjson.Get(out, "hookSpecificOutput.permissionDecision").String() != "deny" {
		t.Fatalf("collapse mode, re-probe should deny, got: %s", out)
	}
	if !strings.Contains(gjson.Get(out, "hookSpecificOutput.permissionDecisionReason").String(), "git-kit context") {
		t.Errorf("deny reason should point to git-kit context: %s", out)
	}
}

// TestAgentsHookRun_CollapseRecoversRaceyTranscript reproduces the live
// failure this test was added for: the harness can invoke the hook for the
// pending command before it has finished flushing the immediately preceding
// turn to the transcript file. A naive single read sees no prior turn and
// silently waves the reprobe through. The retry in hookTailAndNudge must
// recover once the writer catches up within the race window.
func TestAgentsHookRun_CollapseRecoversRaceyTranscript(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(hookRaceRetryDelay)
		content := `{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git status"}}]}}` + "\n"
		if err := os.WriteFile(tp, []byte(content), 0o644); err != nil {
			t.Errorf("concurrent write: %v", err)
		}
	}()
	t.Cleanup(func() { <-done })

	reprobe := fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git log --oneline -5"}}`, tp)
	out := hookRunOutputMode(t, reprobe, hookModeCollapse)
	if got := gjson.Get(out, "hookSpecificOutput.permissionDecision").String(); got != "deny" {
		t.Fatalf("retry should recover the race and deny, got decision=%q out=%s", got, out)
	}
}

// TestAgentsHookRun_NoRetryOnSettledTranscript guards the fast path: a
// covered command with genuinely no prior same-group turn must not pay the
// retry delay when the transcript is old (settled), only when it was touched
// moments ago.
func TestAgentsHookRun_NoRetryOnSettledTranscript(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := `{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo hi"}}]}}` + "\n"
	if err := os.WriteFile(tp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(tp, old, old); err != nil {
		t.Fatal(err)
	}

	lone := fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git status --short"}}`, tp)
	start := time.Now()
	out := hookRunOutputMode(t, lone, hookModeCollapse)
	if elapsed := time.Since(start); elapsed > hookRaceRetryDelay {
		t.Errorf("settled transcript should skip retry entirely, took %v", elapsed)
	}
	if got := gjson.Get(out, "hookSpecificOutput.permissionDecision").String(); got != "defer" {
		t.Fatalf("lone covered command should advise, got decision=%q out=%s", got, out)
	}
}

// hookDedupeTranscript fabricates a transcript line carrying the injected
// advisory for `cmd`'s kind — the shape the dedupe probes for. extra lines are
// appended verbatim (e.g. tool_use turns to also trigger a collapse nudge).
func hookDedupeTranscript(t *testing.T, cmd string, extra ...string) string {
	t.Helper()
	res := sessionaudit.Hint(cmd)
	if !res.Covered {
		t.Fatalf("fixture command %q is not covered", cmd)
	}
	lines := append([]string{
		fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"%s"}]}}`, hookHintMarker(res)),
	}, extra...)
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return tp
}

func TestAgentsHookRun_AdvisoryDedupe(t *testing.T) {
	tp := hookDedupeTranscript(t, "git status --short")

	// Second same-kind fire: the kind's guidance is already in the transcript
	// tail → emit nothing, like an uncovered command.
	stdin := fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git status --short"}}`, tp)
	if out := hookRunOutput(t, stdin, true); strings.TrimSpace(out) != "" {
		t.Errorf("same-kind advisory should be deduped, got: %q", out)
	}

	// A different kind has no marker in the tail → advisory still emitted.
	stdin = fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git add ."}}`, tp)
	out := hookRunOutput(t, stdin, true)
	if !strings.Contains(gjson.Get(out, "hookSpecificOutput.additionalContext").String(), "git-kit commit") {
		t.Errorf("different kind must still emit its advisory, got: %q", out)
	}

	// Unreadable transcript → fail-open to today's behavior (advisory emitted).
	stdin = fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git status --short"}}`,
		filepath.Join(t.TempDir(), "missing.jsonl"))
	out = hookRunOutput(t, stdin, true)
	if !strings.Contains(gjson.Get(out, "hookSpecificOutput.additionalContext").String(), "git-kit context") {
		t.Errorf("missing transcript must fail open to the advisory, got: %q", out)
	}

	// Block mode still denies even when the hint was already seen — dedupe is
	// advisory-only.
	stdin = fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git status --short"}}`, tp)
	if out := hookRunOutput(t, stdin, false); gjson.Get(out, "hookSpecificOutput.permissionDecision").String() != "deny" {
		t.Errorf("block mode must keep denying regardless of dedupe, got: %q", out)
	}
}

func TestAgentsHookRun_CollapseNudgeNotDeduped(t *testing.T) {
	// Tail carries both the already-injected context hint AND a recent
	// same-group probe run — the turn-local collapse nudge must still fire.
	tp := hookDedupeTranscript(t, "git status --short",
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git status"}}]}}`,
		`{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"git log --oneline -5"}}]}}`,
	)
	stdin := fmt.Sprintf(`{"tool_name":"Bash","transcript_path":%q,"tool_input":{"command":"git diff --stat"}}`, tp)
	out := hookRunOutput(t, stdin, true)
	if !strings.Contains(gjson.Get(out, "hookSpecificOutput.additionalContext").String(), "fold it and this one") {
		t.Errorf("collapse nudge must not be deduped, got: %q", out)
	}
}

// maxDenyReasonBytes is the budget for the short deny form — the reason is
// re-injected on every blocked retry, so it must stay far below the full
// advisory text.
const maxDenyReasonBytes = 200

func TestHookDenyReason_ShortForm(t *testing.T) {
	res := sessionaudit.Hint("git status --short")
	nudge := &sessionaudit.CollapseNudge{Group: "context", GkCommand: "git-kit context", PriorTurns: 2}

	got := hookDenyReason(res, nudge)
	want := "Blocked repeated context probe — run git-kit context instead."
	if got != want {
		t.Errorf("collapse deny reason = %q, want %q", got, want)
	}
	if len(got) > maxDenyReasonBytes {
		t.Errorf("collapse deny reason is %d bytes, want <= %d", len(got), maxDenyReasonBytes)
	}

	got = hookDenyReason(res, nil)
	if !strings.HasPrefix(got, "Blocked covered raw git — run "+res.CoveredBy[0]) {
		t.Errorf("single deny reason = %q, want CoveredBy[0] short form", got)
	}
	if len(got) > maxDenyReasonBytes {
		t.Errorf("single deny reason is %d bytes, want <= %d", len(got), maxDenyReasonBytes)
	}
}

func TestAgentsHookRun_FailOpen(t *testing.T) {
	// Each of these must emit nothing (defer to the normal flow), never block.
	cases := []struct {
		name, stdin string
	}{
		{"not covered", `{"tool_name":"Bash","tool_input":{"command":"git rev-parse HEAD"}}`},
		{"non-bash tool", `{"tool_name":"Edit","tool_input":{"file_path":"x"}}`},
		{"empty command", `{"tool_name":"Bash","tool_input":{"command":"   "}}`},
		{"garbage stdin", `not json at all`},
		{"empty stdin", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if out := hookRunOutput(t, tc.stdin, false); strings.TrimSpace(out) != "" {
				t.Errorf("expected no output (fail-open), got: %q", out)
			}
		})
	}
}

func TestStripGKHookEntries_PreservesOthers(t *testing.T) {
	base := []byte(`{"model":"opus","hooks":{` +
		`"PreToolUse":[` +
		`{"matcher":"Skill","hooks":[{"type":"command","command":"/keep.sh"}]},` +
		`{"matcher":"Bash","hooks":[{"type":"command","command":"\"/x/git-kit\" agents hook run --warn"}]}` +
		`],` +
		`"Stop":[{"matcher":"*","hooks":[{"type":"command","command":"stop.sh"}]}]}}`)

	out, removed := stripGKHookEntries(base)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if n := gjson.GetBytes(out, "hooks.PreToolUse.#").Int(); n != 1 {
		t.Fatalf("PreToolUse left %d entries, want 1", n)
	}
	if got := gjson.GetBytes(out, "hooks.PreToolUse.0.matcher").String(); got != "Skill" {
		t.Errorf("surviving entry matcher = %q, want Skill", got)
	}
	if !gjson.GetBytes(out, "hooks.Stop").Exists() {
		t.Error("Stop hook was dropped")
	}
	if gjson.GetBytes(out, "model").String() != "opus" {
		t.Error("unrelated top-level key was dropped")
	}
}

func TestStripGKHookEntries_PrunesEmptyScaffolding(t *testing.T) {
	base := []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"git-kit agents hook run"}]}]}}`)
	out, removed := stripGKHookEntries(base)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if gjson.GetBytes(out, "hooks").Exists() {
		t.Errorf("empty hooks object should be pruned: %s", out)
	}
}

func TestStripGKHookEntries_NoneWhenAbsent(t *testing.T) {
	base := []byte(`{"hooks":{"PreToolUse":[{"matcher":"Skill","hooks":[{"type":"command","command":"x"}]}]}}`)
	if _, removed := stripGKHookEntries(base); removed != 0 {
		t.Errorf("removed = %d, want 0 (no gk hook present)", removed)
	}
}

func TestGKHookState(t *testing.T) {
	warn := []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"git-kit agents hook run --warn"}]}]}}`)
	if inst, mode := gkHookState(warn); !inst || mode != "warn" {
		t.Errorf("warn state = (%v,%q), want (true,warn)", inst, mode)
	}
	block := []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"git-kit agents hook run"}]}]}}`)
	if inst, mode := gkHookState(block); !inst || mode != "block" {
		t.Errorf("block state = (%v,%q), want (true,block)", inst, mode)
	}
	if inst, _ := gkHookState([]byte(`{}`)); inst {
		t.Error("empty settings reported as installed")
	}
}

// hookInstallCmd builds a bare cobra.Command wired for
// runAgentsHookInstall/runAgentsHookUninstall/runAgentsHookStatus, scoped to
// --global with CLAUDE_CONFIG_DIR redirected to dir via t.Setenv — this must
// never resolve to the real ~/.claude/settings.json.
func hookInstallCmd(t *testing.T, dir string) *cobra.Command {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("global", true, "")
	cmd.Flags().String("mode", hookModeWarn, "")
	cmd.Flags().Bool("no-prompt", false, "")
	cmd.Flags().Bool("stop-commit", false, "")
	cmd.Flags().Bool("stop-only", false, "")
	cmd.Flags().Bool("dry-run", false, "")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	return cmd
}

func agentsHookSettingsPath(dir string) string {
	return filepath.Join(dir, "settings.json")
}

func TestAgentsHookInstall_Idempotent(t *testing.T) {
	dir := t.TempDir()

	cmd := hookInstallCmd(t, dir)
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}
	data, err := os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings after first install: %v", err)
	}
	if n := gjson.GetBytes(data, "hooks.PreToolUse.#").Int(); n != 1 {
		t.Fatalf("PreToolUse entries after first install = %d, want 1", n)
	}
	if n := gjson.GetBytes(data, "hooks.UserPromptSubmit.#").Int(); n != 1 {
		t.Fatalf("UserPromptSubmit entries after first install = %d, want 1", n)
	}

	// Re-running install must refresh in place, not duplicate.
	cmd = hookInstallCmd(t, dir)
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
	data, err = os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings after second install: %v", err)
	}
	if n := gjson.GetBytes(data, "hooks.PreToolUse.#").Int(); n != 1 {
		t.Fatalf("PreToolUse entries after second install = %d, want 1 (no duplicate)", n)
	}
	if n := gjson.GetBytes(data, "hooks.UserPromptSubmit.#").Int(); n != 1 {
		t.Fatalf("UserPromptSubmit entries after second install = %d, want 1 (no duplicate)", n)
	}
	promptCmd := gjson.GetBytes(data, "hooks.UserPromptSubmit.0.hooks.0.command").String()
	if !strings.Contains(promptCmd, "--prompt") {
		t.Errorf("UserPromptSubmit command = %q, want --prompt", promptCmd)
	}
	if to := gjson.GetBytes(data, "hooks.UserPromptSubmit.0.hooks.0.timeout").Int(); to != hookPromptTimeoutSeconds {
		t.Errorf("UserPromptSubmit timeout = %d, want %d", to, hookPromptTimeoutSeconds)
	}
	if gjson.GetBytes(data, "hooks.UserPromptSubmit.0.matcher").Exists() {
		t.Error("UserPromptSubmit entry should carry no matcher field")
	}
}

func TestAgentsHookInstall_NoPromptFlag(t *testing.T) {
	dir := t.TempDir()
	cmd := hookInstallCmd(t, dir)
	if err := cmd.Flags().Set("no-prompt", "true"); err != nil {
		t.Fatal(err)
	}
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install --no-prompt: %v", err)
	}
	data, err := os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if n := gjson.GetBytes(data, "hooks.PreToolUse.#").Int(); n != 1 {
		t.Fatalf("PreToolUse entries = %d, want 1 (still installed)", n)
	}
	if gjson.GetBytes(data, "hooks.UserPromptSubmit").Exists() {
		t.Errorf("UserPromptSubmit must not be installed with --no-prompt: %s", data)
	}
}

func TestAgentsHookInstall_PreservesOtherToolHooks(t *testing.T) {
	dir := t.TempDir()
	seed := []byte(`{"model":"opus","hooks":{` +
		`"UserPromptSubmit":[{"hooks":[{"type":"command","command":"node mem-mesh-hook.mjs"}]}],` +
		`"Stop":[{"matcher":"*","hooks":[{"type":"command","command":"stop.sh"}]}]}}`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsHookSettingsPath(dir), seed, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := hookInstallCmd(t, dir)
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	if got := gjson.GetBytes(data, "hooks.UserPromptSubmit.0").Raw; got != `{"hooks":[{"type":"command","command":"node mem-mesh-hook.mjs"}]}` {
		t.Errorf("mem-mesh UserPromptSubmit entry not preserved byte-for-byte: %s", got)
	}
	if n := gjson.GetBytes(data, "hooks.UserPromptSubmit.#").Int(); n != 2 {
		t.Fatalf("UserPromptSubmit entries = %d, want 2 (mem-mesh + gk)", n)
	}
	if !gjson.GetBytes(data, "hooks.Stop").Exists() {
		t.Error("Stop hook was dropped")
	}
	if gjson.GetBytes(data, "model").String() != "opus" {
		t.Error("unrelated top-level key was dropped")
	}

	// Uninstalling must remove only the gk entries, leaving mem-mesh and Stop intact.
	if err := runAgentsHookUninstall(cmd, nil); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	data, err = os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings after uninstall: %v", err)
	}
	if got := gjson.GetBytes(data, "hooks.UserPromptSubmit.0").Raw; got != `{"hooks":[{"type":"command","command":"node mem-mesh-hook.mjs"}]}` {
		t.Errorf("mem-mesh UserPromptSubmit entry not preserved after uninstall: %s", got)
	}
	if n := gjson.GetBytes(data, "hooks.UserPromptSubmit.#").Int(); n != 1 {
		t.Fatalf("UserPromptSubmit entries after uninstall = %d, want 1 (mem-mesh only)", n)
	}
	if !gjson.GetBytes(data, "hooks.Stop").Exists() {
		t.Error("Stop hook was dropped by uninstall")
	}
}

func TestAgentsHookUninstall_NoResidue(t *testing.T) {
	dir := t.TempDir()
	cmd := hookInstallCmd(t, dir)
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	cmd = hookInstallCmd(t, dir)
	if err := runAgentsHookUninstall(cmd, nil); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	data, err := os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings after uninstall: %v", err)
	}
	if gjson.GetBytes(data, "hooks").Exists() {
		t.Errorf("empty hooks object should be pruned after uninstall: %s", data)
	}

	// A second uninstall on an already-clean file is a no-op ("absent"), not an error.
	cmd = hookInstallCmd(t, dir)
	if err := runAgentsHookUninstall(cmd, nil); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestAgentsHookStatus_ReportsBothEvents(t *testing.T) {
	dir := t.TempDir()
	cmd := hookInstallCmd(t, dir)
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	statusCmd := hookInstallCmd(t, dir)
	var buf bytes.Buffer
	statusCmd.SetOut(&buf)
	if err := runAgentsHookStatus(statusCmd, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := buf.String()
	globalLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "global") {
			globalLine = line
		}
	}
	if globalLine == "" {
		t.Fatalf("no global status line in output: %q", out)
	}
	if !strings.Contains(globalLine, "PreToolUse: installed (warn)") {
		t.Errorf("global status line = %q, want PreToolUse installed (warn)", globalLine)
	}
	if !strings.Contains(globalLine, "UserPromptSubmit: installed") {
		t.Errorf("global status line = %q, want UserPromptSubmit installed", globalLine)
	}

	// After uninstall, both must report not installed.
	cmd = hookInstallCmd(t, dir)
	if err := runAgentsHookUninstall(cmd, nil); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	statusCmd = hookInstallCmd(t, dir)
	buf.Reset()
	statusCmd.SetOut(&buf)
	if err := runAgentsHookStatus(statusCmd, nil); err != nil {
		t.Fatalf("status after uninstall: %v", err)
	}
	globalLine = ""
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "global") {
			globalLine = line
		}
	}
	if !strings.Contains(globalLine, "PreToolUse: not installed") || !strings.Contains(globalLine, "UserPromptSubmit: not installed") {
		t.Errorf("global status line after uninstall = %q, want both not installed", globalLine)
	}
}

func TestAgentsHookRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"agents", "hook", "install"})
	if err != nil {
		t.Fatalf("find agents hook install: %v", err)
	}
	if cmd.Name() != "install" {
		t.Errorf("resolved to %q, want install", cmd.Name())
	}
}
