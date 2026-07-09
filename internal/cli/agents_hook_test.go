package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		wantDecision string
	}{
		{"warn/single", hookModeWarn, covered, nil, "defer"},
		{"warn/reprobe", hookModeWarn, covered, nudge, "defer"},
		{"collapse/single-advisory", hookModeCollapse, covered, nil, "defer"},
		{"collapse/reprobe-denied", hookModeCollapse, covered, nudge, "deny"},
		{"block/single-denied", hookModeBlock, covered, nil, "deny"},
		{"block/reprobe-denied", hookModeBlock, covered, nudge, "deny"},
		{"any/nothing", hookModeBlock, uncovered, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, reason, addl := hookDecide(tc.mode, tc.res, tc.nudge)
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

func TestAgentsHookRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"agents", "hook", "install"})
	if err != nil {
		t.Fatalf("find agents hook install: %v", err)
	}
	if cmd.Name() != "install" {
		t.Errorf("resolved to %q, want install", cmd.Name())
	}
}
