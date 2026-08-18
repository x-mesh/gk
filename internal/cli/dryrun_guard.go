package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// dryRunAware lists, by full command path under the root, the commands that
// actually read the persistent --dry-run flag — via the DryRun() accessor or
// an inherited-flag lookup (agents install, init, worktree init). Keyed per
// LEAF because consumption is a per-leaf fact: the first version keyed by
// root child, and that granularity let `gk worktree remove --dry-run` pass
// the guard and delete the worktree for real — the exact hole the guard
// exists to close. Unlisted leaves fail closed, including read-only ones
// (refusing a meaningless preview loses nothing; running under a flag the
// user believes is a preview can lose work).
//
// Commands with a LOCAL --dry-run flag (discard, ship, reset, branch clean,
// resolve, pr create, …) are not listed and do not need to be: cobra binds
// --dry-run to the local flag wherever one exists, so flagDryRun stays false
// and the guard never fires for them. (ship appears anyway: it also folds
// the global flag into its own.)
var dryRunAware = map[string]bool{
	"agents install":    true, // writes instruction files; dry-run previews
	"apply":             true,
	"batch":             true,
	"clone":             true,
	"drivers install":   true,
	"drivers uninstall": true,
	"forget":            true,
	"init":              true,
	"land":              true,
	"promote":           true, // shares runLandPipeline's dry-run plan branch
	"rebase":            true, // rebase --plan prints the todo without rewriting
	"ship":              true,
	"update":            true,
	"wip":               true, // plan-and-stop preview in runWip
	"wip repair":        true,
	"worktree add":      true,
	"worktree cleanup":  true, // dry-run report is its default mode
	"worktree finish":   true,
	"worktree init":     true,
	"worktree remove":   true, // plan-and-stop preview in runWorktreeRemove
	"worktree rename":   true,
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
	if dryRunAware[dryRunPathKey(cmd)] {
		return nil
	}
	switch rootChildName(cmd) {
	// cobra's own plumbing: help/completion never mutate anything, and the
	// hidden __complete commands run on every <TAB> — refusing there would
	// break shell completion for a command line the user is still editing.
	case "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
		return nil
	}
	return WithBlocked(
		fmt.Errorf("%s does not support --dry-run — the flag would be ignored and the command would run for real", cmd.CommandPath()),
		"dry-run-unsupported",
		"drop --dry-run to run the command; verbs with a real preview document it in --help (gk ship, gk land, gk rebase --plan, gk reset, gk branch clean, …)",
	)
}

// dryRunPathKey returns cmd's path below the root ("worktree add"), built
// from canonical names so aliases (wt, mv) resolve to their map entries.
func dryRunPathKey(cmd *cobra.Command) string {
	var parts []string
	for c := cmd; c.HasParent(); c = c.Parent() {
		parts = append([]string{c.Name()}, parts...)
	}
	return strings.Join(parts, " ")
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
