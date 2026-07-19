package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
)

// The Stop hook writes to git history, so a plain install must never register
// it — only an explicit --stop-commit does.
func TestAgentsHookInstall_StopIsOptIn(t *testing.T) {
	dir := t.TempDir()

	cmd := hookInstallCmd(t, dir)
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if n := gjson.GetBytes(data, "hooks.Stop.#").Int(); n != 0 {
		t.Errorf("Stop entries after plain install = %d, want 0", n)
	}
	if gkStopHookInstalled(data) {
		t.Error("gkStopHookInstalled true after a plain install")
	}
}

func TestAgentsHookInstall_StopCommitRegistersAndRefreshes(t *testing.T) {
	dir := t.TempDir()

	cmd := hookInstallCmd(t, dir)
	if err := cmd.Flags().Set("stop-commit", "true"); err != nil {
		t.Fatalf("set --stop-commit: %v", err)
	}
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if n := gjson.GetBytes(data, "hooks.Stop.#").Int(); n != 1 {
		t.Fatalf("Stop entries = %d, want 1", n)
	}
	if !gkStopHookInstalled(data) {
		t.Error("gkStopHookInstalled false right after --stop-commit install")
	}
	cmdStr := gjson.GetBytes(data, "hooks.Stop.0.hooks.0.command").String()
	if !strings.Contains(cmdStr, "agents hook run --stop") {
		t.Errorf("stored command = %q, want it to invoke `agents hook run --stop`", cmdStr)
	}
	// A hung provider must not hold the session open forever.
	if to := gjson.GetBytes(data, "hooks.Stop.0.hooks.0.timeout").Int(); to != stopHookTimeoutSeconds {
		t.Errorf("timeout = %d, want %d", to, stopHookTimeoutSeconds)
	}

	// Re-running must refresh in place, not stack a second checkpoint hook.
	cmd = hookInstallCmd(t, dir)
	if err := cmd.Flags().Set("stop-commit", "true"); err != nil {
		t.Fatalf("set --stop-commit: %v", err)
	}
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
	data, _ = os.ReadFile(agentsHookSettingsPath(dir))
	if n := gjson.GetBytes(data, "hooks.Stop.#").Int(); n != 1 {
		t.Errorf("Stop entries after re-install = %d, want 1", n)
	}
}

// Dropping --stop-commit on a later install is how a user turns the
// checkpoint off; it must remove the entry rather than leave it behind.
func TestAgentsHookInstall_WithoutStopCommitRemovesExisting(t *testing.T) {
	dir := t.TempDir()

	cmd := hookInstallCmd(t, dir)
	if err := cmd.Flags().Set("stop-commit", "true"); err != nil {
		t.Fatalf("set --stop-commit: %v", err)
	}
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install with stop: %v", err)
	}

	cmd = hookInstallCmd(t, dir) // --stop-commit defaults to false
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install without stop: %v", err)
	}
	data, _ := os.ReadFile(agentsHookSettingsPath(dir))
	if n := gjson.GetBytes(data, "hooks.Stop.#").Int(); n != 0 {
		t.Errorf("Stop entries after opting out = %d, want 0", n)
	}
}

func TestAgentsHookUninstall_RemovesStop(t *testing.T) {
	dir := t.TempDir()

	cmd := hookInstallCmd(t, dir)
	if err := cmd.Flags().Set("stop-commit", "true"); err != nil {
		t.Fatalf("set --stop-commit: %v", err)
	}
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	cmd = hookInstallCmd(t, dir)
	if err := runAgentsHookUninstall(cmd, nil); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	data, _ := os.ReadFile(agentsHookSettingsPath(dir))
	if gjson.GetBytes(data, "hooks.Stop.#").Int() != 0 {
		t.Error("Stop entry survived uninstall")
	}
	if gkStopHookInstalled(data) {
		t.Error("gkStopHookInstalled true after uninstall")
	}
}

// stop_hook_active means Claude Code is already in a Stop-driven continuation;
// checkpointing again would append one commit per loop iteration.
func TestAgentsHookStop_SkipsWhenStopHookActive(t *testing.T) {
	cmd := stopHandlerCmd(t, `{"session_id":"s","stop_hook_active":true}`)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runAgentsHookStop(cmd, nil); err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("handler wrote %q, want silence", out.String())
	}
}

// Fail-open: outside a repo there is nothing to checkpoint, and the handler
// must still exit cleanly so the session can end.
func TestAgentsHookStop_NoRepoIsSilentSuccess(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cmd := stopHandlerCmd(t, `{"session_id":"s","stop_hook_active":false}`)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runAgentsHookStop(cmd, nil); err != nil {
		t.Fatalf("handler returned an error outside a repo: %v", err)
	}
}

func TestAgentsHookStop_MalformedStdinIsSilentSuccess(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cmd := stopHandlerCmd(t, `not json at all`)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runAgentsHookStop(cmd, nil); err != nil {
		t.Fatalf("handler returned an error on malformed stdin: %v", err)
	}
}

// Claude merges PreToolUse across project + global settings, so a repo-scoped
// install of a hook that already lives globally would double-fire it.
// --stop-only exists to add the checkpoint without touching the other events.
func TestAgentsHookInstall_StopOnlyLeavesOtherEventsAlone(t *testing.T) {
	dir := t.TempDir()

	// A foreign PreToolUse hook plus a gk UserPromptSubmit entry: neither may
	// be stripped, rewritten, or duplicated by a --stop-only install.
	seed := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"node ./trace.mjs pre"}]}],` +
		`"UserPromptSubmit":[{"hooks":[{"type":"command","command":"\"/somewhere/git-kit\" agents hook run --prompt"}]}]}}`
	if err := os.WriteFile(agentsHookSettingsPath(dir), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	cmd := hookInstallCmd(t, dir)
	if err := cmd.Flags().Set("stop-only", "true"); err != nil {
		t.Fatalf("set --stop-only: %v", err)
	}
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	data, err := os.ReadFile(agentsHookSettingsPath(dir))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	// The checkpoint landed…
	if n := gjson.GetBytes(data, "hooks.Stop.#").Int(); n != 1 {
		t.Errorf("Stop entries = %d, want 1", n)
	}
	// …and nothing else moved.
	if n := gjson.GetBytes(data, "hooks.PreToolUse.#").Int(); n != 1 {
		t.Errorf("PreToolUse entries = %d, want the seeded 1", n)
	}
	if got := gjson.GetBytes(data, "hooks.PreToolUse.0.hooks.0.command").String(); got != "node ./trace.mjs pre" {
		t.Errorf("foreign PreToolUse command = %q, want it untouched", got)
	}
	if n := gjson.GetBytes(data, "hooks.UserPromptSubmit.#").Int(); n != 1 {
		t.Errorf("UserPromptSubmit entries = %d, want the seeded 1", n)
	}
	if got := gjson.GetBytes(data, "hooks.UserPromptSubmit.0.hooks.0.command").String(); !strings.Contains(got, "/somewhere/git-kit") {
		t.Errorf("UserPromptSubmit command = %q, want the seeded path preserved", got)
	}
}

// --stop-only is the checkpoint switch, so it must not require --stop-commit
// alongside it.
func TestAgentsHookInstall_StopOnlyImpliesStopCommit(t *testing.T) {
	dir := t.TempDir()

	cmd := hookInstallCmd(t, dir)
	if err := cmd.Flags().Set("stop-only", "true"); err != nil {
		t.Fatalf("set --stop-only: %v", err)
	}
	if err := runAgentsHookInstall(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, _ := os.ReadFile(agentsHookSettingsPath(dir))
	if !gkStopHookInstalled(data) {
		t.Error("--stop-only alone did not register the checkpoint hook")
	}
	// It must not have added a PreToolUse entry of its own either.
	if n := gjson.GetBytes(data, "hooks.PreToolUse.#").Int(); n != 0 {
		t.Errorf("PreToolUse entries = %d, want 0 under --stop-only", n)
	}
}

func TestStopHookCommandStringCarriesStopFlag(t *testing.T) {
	got := stopHookCommandString()
	if !strings.Contains(got, "agents hook run --stop") {
		t.Errorf("stopHookCommandString() = %q, want it to end in `agents hook run --stop`", got)
	}
	// The marker is what install/uninstall/status match on.
	if !strings.Contains(got, gkHookMarker) {
		t.Errorf("stopHookCommandString() = %q, missing the gk marker %q", got, gkHookMarker)
	}
}

// Stop must be in the managed-event list, or uninstall would walk past it and
// leave a live checkpoint hook behind.
func TestStopIsAManagedHookEvent(t *testing.T) {
	found := false
	for _, e := range gkHookEvents {
		if e == "Stop" {
			found = true
		}
	}
	if !found {
		t.Errorf("gkHookEvents = %v, want it to include \"Stop\"", gkHookEvents)
	}
}

func stopHandlerCmd(t *testing.T, stdin string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(stdin))
	return cmd
}
