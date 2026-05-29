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

	if status.Short != easyShortKO["status"] {
		t.Errorf("status.Short not swapped: %q", status.Short)
	}
	if branch.Short != easyShortKO["branch"] {
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

// Nested subcommands (e.g. branch clean) are walked too.
func TestSwapEasyShorts_Nested(t *testing.T) {
	root := &cobra.Command{Use: "gk"}
	branch := &cobra.Command{Use: "branch", Short: "Branch helpers"}
	worktree := &cobra.Command{Use: "worktree", Short: "Worktree helpers"}
	branch.AddCommand(worktree)
	root.AddCommand(branch)

	defer swapEasyShorts(root)()

	if worktree.Short != easyShortKO["worktree"] {
		t.Errorf("nested worktree.Short not swapped: %q", worktree.Short)
	}
}
