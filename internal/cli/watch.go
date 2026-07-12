package cli

import (
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:     "watch",
		Aliases: []string{"w"},
		Short:   "Live watch — fleet overview, or the single-worktree feed when there is just one",
		Long: `One entry point for live supervision that picks the right zoom level:
with several worktrees (or the multi-repo flags) it opens the 'gk fleet'
dashboard; with exactly one worktree it goes straight into the
'gk status --watch' change feed. Inside fleet, w zooms into a worktree's
feed in place (esc pops back, [ and ] hop between worktrees) — so
'gk watch' is the same live view at whatever altitude fits the repo.

'gk fleet' and 'gk status --watch' remain the explicit entry points; this
command only routes between them. Flags mirror 'gk fleet', and the
machine-readable modes — --json (or GK_AGENT) snapshot, --events NDJSON
stream — behave exactly like fleet's regardless of worktree count, so
orchestrators get one stable contract.`,
		Args: cobra.NoArgs,
		RunE: runWatch,
	}
	addFleetFlags(cmd)
	rootCmd.AddCommand(cmd)
}

func runWatch(cmd *cobra.Command, _ []string) error {
	return runFleetCore(cmd, true)
}
