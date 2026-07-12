package cli

import (
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:     "watch",
		Aliases: []string{"w"},
		Short:   "Live supervision — multi-worktree dashboard, or the single-worktree feed",
		Long: `Live supervision at whatever altitude fits the repo: with several
worktrees (or the multi-repo flags) it opens the dashboard — every worktree
at once: branch, ahead/behind, dirty/conflict state, the last-changed file,
last activity, any paused operation, and which one is current. Built for
supervising parallel work (e.g. several AI agents each in their own
worktree) — answers "who is dirty / stuck / stale / ready to land" without
a per-worktree status probe. With exactly one worktree it goes straight
into the 'gk status --watch' change feed.

A merged change feed below the dashboard table shows which files — and
which functions — changed in which worktree as they happen, in the same
file · function · ± form as 'gk status --watch' ('e' toggles the pane;
--feed-stats=false drops the per-poll diff runs and shows file names only).
When filesystem watches can be established the dashboard reacts to edits
instantly and the poll drops to a slow heartbeat; otherwise it polls on
--interval. j/k move, enter cycles the cursor panel (status fields → that
worktree's own live change feed → off), w zooms into that worktree's live
feed in place (esc pops back, [ and ] hop between worktrees), f/s cycle
the view filter (all→busy→stuck) and sort (default→activity→status),
r refreshes, q quits.

Under --json (or GK_AGENT) it instead emits a one-shot machine-readable
snapshot — always the dashboard contract, regardless of worktree count.
--events streams changes as NDJSON instead — file-changed / status-changed /
op-start / op-end / land-ready events an orchestrator can subscribe to
rather than polling; fleet.notify config maps conflict / paused /
land_ready transitions to a shell hook.

'gk fleet' is the deprecated former name (kept one release as an alias;
it never auto-routes to the single-worktree feed). Config keys stay under
fleet.* for now.`,
		Args: cobra.NoArgs,
		RunE: runWatch,
	}
	addFleetFlags(cmd)
	rootCmd.AddCommand(cmd)
}

func runWatch(cmd *cobra.Command, _ []string) error {
	return runFleetCore(cmd, true)
}
