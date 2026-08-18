package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Before the guard, the persistent --dry-run parsed on every command but was
// consumed by only some — `gk merge --dry-run` looked like a preview and ran
// the real merge. The guard must refuse the flag on non-consumers, as a
// blocked precondition with a stable code.
func TestDryRunGuard_RefusesNonConsumers(t *testing.T) {
	t.Cleanup(func() { flagDryRun = false })
	flagDryRun = true
	for _, verb := range []string{"merge", "sync", "pull", "push", "switch", "commit"} {
		cmd, _, err := rootCmd.Find([]string{verb})
		if err != nil || cmd == rootCmd {
			t.Fatalf("command %q not found in tree: %v", verb, err)
		}
		gerr := rejectUnsupportedDryRun(cmd)
		if gerr == nil {
			t.Fatalf("%s --dry-run must be refused, got nil", verb)
		}
		if stateFrom(gerr) != envStateBlocked {
			t.Errorf("%s: state = %q, want %q", verb, stateFrom(gerr), envStateBlocked)
		}
		if codeFrom(gerr) != "dry-run-unsupported" {
			t.Errorf("%s: code = %q, want dry-run-unsupported", verb, codeFrom(gerr))
		}
		if !strings.Contains(gerr.Error(), verb) {
			t.Errorf("%s: error must name the command, got %q", verb, gerr)
		}
	}
}

// Consumers of the global flag pass, including leaves of allowlisted
// subtrees (worktree add) whose consumption lives on the parent's tree.
func TestDryRunGuard_ConsumersPass(t *testing.T) {
	t.Cleanup(func() { flagDryRun = false })
	flagDryRun = true
	for _, path := range [][]string{
		{"wip"}, {"land"}, {"promote"}, {"apply"}, {"update"},
		{"worktree", "add"}, {"agents", "install"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd == rootCmd {
			t.Fatalf("command %v not found in tree: %v", path, err)
		}
		if gerr := rejectUnsupportedDryRun(cmd); gerr != nil {
			t.Errorf("%v --dry-run must pass the guard, got %v", path, gerr)
		}
	}
}

// Without the flag the guard is inert everywhere.
func TestDryRunGuard_NoFlagNoGuard(t *testing.T) {
	flagDryRun = false
	cmd, _, err := rootCmd.Find([]string{"merge"})
	if err != nil {
		t.Fatal(err)
	}
	if gerr := rejectUnsupportedDryRun(cmd); gerr != nil {
		t.Errorf("guard fired without --dry-run: %v", gerr)
	}
}

// cobra's own plumbing must bypass the guard — the hidden __complete commands
// run on every <TAB>, so refusing there breaks completion for a command line
// the user is still editing.
func TestDryRunGuard_CobraInternalsBypass(t *testing.T) {
	t.Cleanup(func() { flagDryRun = false })
	flagDryRun = true
	root := &cobra.Command{Use: "gk"}
	for _, name := range []string{"help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		c := &cobra.Command{Use: name}
		root.AddCommand(c)
		if err := rejectUnsupportedDryRun(c); err != nil {
			t.Errorf("%s must bypass the guard: %v", name, err)
		}
	}
}

// Every allowlist entry must name a real root subcommand by its canonical
// name — a rename would otherwise silently turn a supported verb into a
// refused one (rootChildName compares canonical names, not aliases).
func TestDryRunGuard_AllowlistNamesAreCanonical(t *testing.T) {
	for name := range dryRunAware {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil || cmd == rootCmd || cmd.Name() != name {
			t.Errorf("dryRunAware entry %q does not match a root subcommand canonically (found %q, err %v)", name, cmd.Name(), err)
		}
	}
}

// End to end: Execute stops in PersistentPreRunE, before any RunE — proven by
// pointing --repo at a directory that is not a repository, where the verb
// itself could only fail differently (or, without the guard, run for real).
func TestDryRunGuard_ExecuteBlocksBeforeRun(t *testing.T) {
	t.Cleanup(func() {
		flagDryRun = false
		flagRepo = ""
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	tmp := t.TempDir()
	var out strings.Builder
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"--repo", tmp, "sync", "--dry-run"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("sync --dry-run must be refused, got nil")
	}
	if codeFrom(err) != "dry-run-unsupported" {
		t.Errorf("code = %q, want dry-run-unsupported (err: %v)", codeFrom(err), err)
	}
}
