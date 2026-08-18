package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// dryRunAware names the root subcommands whose tree actually reads the
// persistent --dry-run flag — via the DryRun() accessor or an inherited-flag
// lookup (agents install, init, wt init). The map is kept at root-child
// granularity on purpose: consumption is spread across whole subtrees
// (worktree add/agent/init, agents install/hook, land's pipeline shared with
// promote), and a per-leaf map would drift the first time a subtree gains a
// verb. The cost of that coarseness is bounded — a read-only leaf like
// `worktree list` still accepts and ignores the flag, which loses nothing.
//
// Commands with a LOCAL --dry-run flag (ship, reset, branch clean, resolve,
// pr create, …) are not listed and do not need to be: cobra binds --dry-run
// to the local flag wherever one exists, so flagDryRun stays false and the
// guard never fires for them. (ship appears anyway: it also folds the global
// flag into its own.)
var dryRunAware = map[string]bool{
	"agents":   true, // agents install writes instruction files; dry-run previews
	"apply":    true,
	"batch":    true,
	"clone":    true,
	"drivers":  true,
	"forget":   true,
	"init":     true,
	"land":     true,
	"promote":  true, // shares runLandPipeline's dry-run plan branch with land
	"rebase":   true, // rebase --plan prints the todo without rewriting
	"ship":     true,
	"update":   true,
	"wip":      true, // wip repair plans without executing
	"worktree": true, // add/agent/init/rename/remove subtrees
}

// rejectUnsupportedDryRun blocks --dry-run on commands that never read it.
// The persistent flag parses everywhere, so before this guard
// `gk merge --dry-run` LOOKED like a preview and then ran the real merge —
// the worst possible reading of a safety flag. Refusing fails closed: a
// rejected preview costs a retry without the flag, a silently-executed
// "preview" can cost work. The refusal is a blocked precondition (nothing
// ran, dropping the flag clears it), so agent mode gets state:"blocked" with
// a stable code instead of a hard error.
func rejectUnsupportedDryRun(cmd *cobra.Command) error {
	if !flagDryRun {
		return nil
	}
	switch top := rootChildName(cmd); {
	case dryRunAware[top]:
		return nil
	// cobra's own plumbing: help/completion never mutate anything, and the
	// hidden __complete commands run on every <TAB> — refusing there would
	// break shell completion for a command line the user is still editing.
	case top == "help", top == "completion",
		top == cobra.ShellCompRequestCmd, top == cobra.ShellCompNoDescRequestCmd:
		return nil
	}
	return WithBlocked(
		fmt.Errorf("%s does not support --dry-run — the flag would be ignored and the command would run for real", cmd.CommandPath()),
		"dry-run-unsupported",
		"drop --dry-run to run the command; verbs with a real preview document it in --help (gk ship, gk land, gk rebase --plan, gk reset, gk branch clean, …)",
	)
}

// rootChildName returns the name of the root-level subcommand cmd belongs to
// (cmd itself when it sits directly under root; the root's own name for the
// bare root, which is never runnable so the guard cannot fire there).
func rootChildName(cmd *cobra.Command) string {
	c := cmd
	for c.HasParent() && c.Parent().HasParent() {
		c = c.Parent()
	}
	return c.Name()
}
