package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// swapEasyShorts replaces mapped commands' Short with the plain-Korean
// version and restores them when the returned func runs.
func TestSwapEasyShorts_SwapAndRestore(t *testing.T) {
	root := &cobra.Command{Use: "gk"}
	status := &cobra.Command{Use: "status", Short: "Show working tree status"}
	branch := &cobra.Command{Use: "branch", Short: "Branch helpers"}
	other := &cobra.Command{Use: "obscure", Short: "Some unmapped command"}
	root.AddCommand(status, branch, other)

	restore := swapEasyShorts(root)

	if status.Short != easyShortKO["gk status"] {
		t.Errorf("status.Short not swapped: %q", status.Short)
	}
	if branch.Short != easyShortKO["gk branch"] {
		t.Errorf("branch.Short not swapped: %q", branch.Short)
	}
	if other.Short != "Some unmapped command" {
		t.Errorf("unmapped command must keep its Short, got %q", other.Short)
	}

	restore()

	if status.Short != "Show working tree status" {
		t.Errorf("status.Short not restored: %q", status.Short)
	}
	if branch.Short != "Branch helpers" {
		t.Errorf("branch.Short not restored: %q", branch.Short)
	}
}

// Leaf names shared across parents are keyed on the full path, so
// "gk stash push" and "gk push" get distinct text.
func TestSwapEasyShorts_PathDisambiguates(t *testing.T) {
	root := &cobra.Command{Use: "gk"}
	push := &cobra.Command{Use: "push", Short: "Guarded push"}
	stash := &cobra.Command{Use: "stash", Short: "Manage stashes"}
	stashPush := &cobra.Command{Use: "push", Short: "Stash the working tree"}
	stash.AddCommand(stashPush)
	root.AddCommand(push, stash)

	defer swapEasyShorts(root)()

	if push.Short != easyShortKO["gk push"] {
		t.Errorf("gk push not swapped: %q", push.Short)
	}
	if stashPush.Short != easyShortKO["gk stash push"] {
		t.Errorf("gk stash push not swapped: %q", stashPush.Short)
	}
	if push.Short == stashPush.Short {
		t.Error("gk push and gk stash push must differ")
	}
}
