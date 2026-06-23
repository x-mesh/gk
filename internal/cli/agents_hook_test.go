package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
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
