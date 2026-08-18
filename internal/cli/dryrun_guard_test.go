package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// Before the guard, the persistent --dry-run parsed on every command but was
// consumed by only some — `gk merge --dry-run` looked like a preview and ran
// the real merge. The guard must refuse the flag on non-consumers, as a
// blocked precondition with a stable code. Since the per-leaf re-keying
// (review F2), that includes non-consuming leaves INSIDE subtrees whose
// other leaves consume — the root-child granularity let
// `gk worktree remove --dry-run` delete for real.
func TestDryRunGuard_RefusesNonConsumers(t *testing.T) {
	t.Cleanup(func() { flagDryRun = false })
	flagDryRun = true
	for _, path := range [][]string{
		{"merge"}, {"sync"}, {"pull"}, {"push"}, {"switch"}, {"commit"},
		{"worktree", "acquire"}, {"worktree", "list"}, {"snapshot"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd == rootCmd {
			t.Fatalf("command %v not found in tree: %v", path, err)
		}
		gerr := rejectUnsupportedDryRun(cmd)
		if gerr == nil {
			t.Fatalf("%v --dry-run must be refused, got nil", path)
		}
		if stateFrom(gerr) != envStateBlocked {
			t.Errorf("%v: state = %q, want %q", path, stateFrom(gerr), envStateBlocked)
		}
		if codeFrom(gerr) != "dry-run-unsupported" {
			t.Errorf("%v: code = %q, want dry-run-unsupported", path, codeFrom(gerr))
		}
		if !strings.Contains(gerr.Error(), cmd.Name()) {
			t.Errorf("%v: error must name the command, got %q", path, gerr)
		}
	}
}

// Consumers of the global flag pass — keyed per leaf, so the destructive
// leaves that gained a plan-and-stop preview (worktree remove, wip) are
// asserted here alongside the pre-existing consumers.
func TestDryRunGuard_ConsumersPass(t *testing.T) {
	t.Cleanup(func() { flagDryRun = false })
	flagDryRun = true
	for _, path := range [][]string{
		{"wip"}, {"wip", "repair"}, {"land"}, {"promote"}, {"apply"}, {"update"},
		{"worktree", "add"}, {"worktree", "remove"}, {"worktree", "finish"},
		{"drivers", "install"}, {"agents", "install"},
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

// Every allowlist entry must name a real command by its canonical path —
// a rename would otherwise silently turn a supported verb into a refused one
// (dryRunPathKey compares canonical names, not aliases).
func TestDryRunGuard_AllowlistKeysAreCanonicalPaths(t *testing.T) {
	for name := range dryRunAware {
		path := strings.Split(name, " ")
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd == rootCmd || dryRunPathKey(cmd) != name {
			t.Errorf("dryRunAware entry %q does not match a command canonically (found %q, err %v)", name, dryRunPathKey(cmd), err)
		}
	}
}

// The guard's local-flag shadowing assumption, end to end (review F7): a verb
// with its own --dry-run (discard) must sail past the guard through the REAL
// root — cobra binds the flag locally, so flagDryRun stays false and the
// command runs its own preview.
func TestDryRunGuard_LocalFlagVerbPassesThroughRealRoot(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("v1")
	repo.WriteFile("a.txt", "dirty")
	t.Cleanup(func() {
		flagDryRun = false
		flagRepo = ""
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		if c, _, err := rootCmd.Find([]string{"discard"}); err == nil {
			_ = c.Flags().Set("dry-run", "false")
		}
	})
	var out strings.Builder
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"--repo", repo.Dir, "discard", "--dry-run", "a.txt"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("discard --dry-run must pass the guard, got %v (code %q)", err, codeFrom(err))
	}
	if !strings.Contains(out.String(), "would discard") {
		t.Errorf("the local dry-run preview did not run:\n%s", out.String())
	}
	if got, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt")); err != nil || string(got) != "dirty" {
		t.Errorf("preview touched the worktree: %q (err %v)", got, err)
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
