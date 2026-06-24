package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// gk promote is the local half of gk land --promote: commit what's dirty,
// then forward-merge the current branch into its parent/base — without
// touching the network. It exists for worktree-centric flows where
// integration happens locally (merge into develop now, push later from
// develop) and `gk land`'s mandatory pull/push steps are exactly what the
// user does NOT want. land --promote reuses the same hop resolution and
// merge engine; promote is that step promoted to a first-class verb with
// push demoted to an opt-in.

func init() {
	cmd := &cobra.Command{
		Use:   "promote [target]",
		Short: "Commit, then merge the current branch into its parent/base — no push",
		Long: `Wraps up local work onto its base branch without touching the network:

  1. commit    gk commit -f                       (skipped when the tree is clean)
  2. promote   gk merge <branch> --into <target>  (one step per hop)

Bare ` + "`gk promote`" + ` climbs ONE hop: the branch's parent when gk-parent
metadata resolves (branch.<name>.gk-parent — the same resolution gk status
and gk land --promote use), else the configured base. ` + "`gk promote <branch>`" + `
walks the parent chain hop by hop until <branch> — feat→develop→main merges
each boundary so intermediate branches advance too. A target outside the
chain is an error (use gk merge --into for a one-off direct merge).

The receiving branch does not need a worktree: a fast-forward updates the
ref directly, a clean non-FF merge commits via merge-tree, and only a real
conflict requires a checkout. When the receiver IS checked out in a worktree
and that worktree has uncommitted changes, promote refuses by default; pass
--autostash to stash those changes around each merge and pop them after.
Nothing is pushed unless --push is set, which publishes each advanced branch
(push --from <target>) after its merge — for the full session close (pull,
push, promote) use gk land --promote instead.

A merge conflict pauses with gk's normal resolve/continue contract and the
failed hop is named with its resume path; re-running skips already-merged
hops (a clean tree skips commit, a merged source merges nothing).

With the global --json flag (or GK_AGENT=1) the result is a machine
contract: {steps:[{name,result}], failed_step?, resume?}; step progress
moves to stderr so stdout stays parseable.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPromote,
	}
	cmd.Flags().Bool("push", false, "after each hop's merge, also publish the advanced branch (push --from <target>)")
	cmd.Flags().Bool("autostash", false, "stash a dirty receiver worktree (the parent checkout) around each merge and pop it after, instead of refusing")
	rootCmd.AddCommand(cmd)
}

func runPromote(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}
	cfg, _ := config.Load(cmd.Flags())
	push, _ := cmd.Flags().GetBool("push")
	autostash := resolveAutostashFlag(cmd, cfg.Promote.Autostash)
	jsonMode := JSONOut()

	spec := landPromoteUseBase
	rerun := "gk promote"
	if len(args) == 1 {
		spec = args[0]
		rerun = "gk promote " + args[0]
	}

	dirty, err := landTreeDirty(ctx, runner)
	if err != nil {
		return err
	}
	_, target, hops, err := resolvePromoteHops(ctx, cmd.ErrOrStderr(), runner, cfg, spec, promoteFlavorPromote)
	if err != nil {
		return err
	}
	if len(hops) == 0 {
		// Already on the target — promoting a branch onto itself is a
		// no-op, not an error, so scripted use stays idempotent. The
		// dirty tree is left alone: without a merge to perform, an
		// auto-commit would be a surprise side effect.
		if jsonMode {
			return emitAgentResult(cmd.OutOrStdout(), landResultJSON{Schema: 1, Result: "nothing-to-promote"})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "already on %s — nothing to promote\n", target)
		return nil
	}

	steps := []landStep{
		{
			name: "commit", args: []string{"commit", "-f"},
			skip:   landSkipWhen(!dirty, "clean tree"),
			resume: "fix the commit (gk commit), then rerun: " + rerun,
		},
	}
	steps = append(steps, promoteHopSteps(repo, jsonMode, hops, push, autostash, rerun)...)

	return runLandPipeline(cmd, repo, jsonMode, steps, landPipelineOpts{
		planHeader:     "─── Promote plan ─────────────────────────────",
		stepHeaderFmt:  "─── promote: %s ──────────────────────────",
		completeHeader: "─── Promote complete ─────────────────────────",
		doneLine:       "promoted to " + target,
		okResult:       "promoted",
		errPrefix:      "promote",
		rerun:          rerun,
	})
}
