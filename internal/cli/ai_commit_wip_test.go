package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/x-mesh/gk/internal/aicommit"
)

func TestWIPCheckpointScopePicksDominantDir(t *testing.T) {
	files := []aicommit.FileChange{
		{Path: "internal/remote/dial.go"},
		{Path: "internal/remote/close.go"},
		{Path: "docs/notes.md"},
	}
	if got, want := wipCheckpointScope(files), "internal"; got != want {
		t.Errorf("scope = %q, want %q", got, want)
	}
}

// Ties must be deterministic: the same file set has to yield the same scope
// on every run, or a checkpoint's scope would flicker between sessions.
func TestWIPCheckpointScopeBreaksTiesAlphabetically(t *testing.T) {
	files := []aicommit.FileChange{
		{Path: "zeta/a.go"},
		{Path: "alpha/b.go"},
	}
	for i := 0; i < 5; i++ {
		if got, want := wipCheckpointScope(files), "alpha"; got != want {
			t.Fatalf("run %d: scope = %q, want %q", i, got, want)
		}
	}
}

func TestWIPCheckpointScopeEmptyForRootOnlyFiles(t *testing.T) {
	files := []aicommit.FileChange{{Path: "README.md"}, {Path: "go.mod"}}
	if got := wipCheckpointScope(files); got != "" {
		t.Errorf("scope = %q, want empty for root-level files", got)
	}
}

func TestWIPCheckpointGroupCoversEveryFile(t *testing.T) {
	files := []aicommit.FileChange{
		{Path: "internal/a.go"},
		{Path: "internal/b.go"},
	}
	g := wipCheckpointGroup(files, "internal")
	if len(g.Files) != 2 {
		t.Errorf("group covers %d files, want 2", len(g.Files))
	}
	// The placeholder type must survive commitlint until MarkAsWIP swaps it.
	if g.Type != "chore" {
		t.Errorf("placeholder type = %q, want %q", g.Type, "chore")
	}
	if g.Scope != "internal" {
		t.Errorf("scope = %q, want %q", g.Scope, "internal")
	}
}

// --wip runs unattended, so it must not stop at a review prompt, and it must
// not rewrite the very chain it is appending to.
func TestWIPFlagImpliesForceAndNoUnwrap(t *testing.T) {
	found := commitCmdWithFlags(t, map[string]string{"wip": "true"})

	f, err := readAICommitFlags(found)
	if err != nil {
		t.Fatalf("readAICommitFlags: %v", err)
	}
	if !f.wip {
		t.Error("wip flag did not round-trip")
	}
	if !f.force {
		t.Error("--wip must imply --force (no reviewer is present)")
	}
	if !f.noWIPUnwrap {
		t.Error("--wip must imply --no-wip-unwrap (writing to the chain, not folding it)")
	}
}

func TestWIPFlagRejectsMessageWritingModes(t *testing.T) {
	for _, conflict := range []string{"plan-template", "interactive"} {
		t.Run(conflict, func(t *testing.T) {
			found := commitCmdWithFlags(t, map[string]string{"wip": "true", conflict: "true"})
			if _, err := readAICommitFlags(found); err == nil {
				t.Errorf("--wip with --%s was accepted, want an error", conflict)
			}
		})
	}
}

// commitCmdWithFlags resolves the real `gk commit` command (so flag names and
// defaults stay in sync with init()) and sets the given flags, restoring each
// to "false" afterwards — rootCmd is process-global and shared across tests.
func commitCmdWithFlags(t *testing.T, set map[string]string) *cobra.Command {
	t.Helper()
	found, _, err := rootCmd.Find([]string{"commit"})
	if err != nil {
		t.Fatalf("find commit: %v", err)
	}
	for name, val := range set {
		if err := found.Flags().Set(name, val); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
		t.Cleanup(func() { _ = found.Flags().Set(name, "false") })
	}
	return found
}
