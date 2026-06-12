package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/resolve"
)

// gk resolve finishes the operation it resolved for: after a full
// resolution it drives `git <op> --continue` itself instead of parking
// the user at "run 'gk continue'". In batch mode (--strategy/--ai) it
// loops — a later pick that conflicts again is re-resolved with the
// same strategy — so one command takes a multi-pick rebase from paused
// to done. This is the in-process version of the subprocess loop
// `gk pull --ai` pioneered (pull.go resolvePullConflictWithAI).

// continueOutcome classifies what one continue/skip step left behind.
type continueOutcome int

const (
	// continueDone — the whole operation finished.
	continueDone continueOutcome = iota
	// continueConflict — a later pick stopped on new conflicts.
	continueConflict
	// continueEmptyPick — git refused to commit because the pick became
	// empty (its content already exists upstream); next step skips it.
	continueEmptyPick
	// continuePausedOther — stopped without conflicts (edit/break step).
	continuePausedOther
)

// maxAutoResolveRounds bounds the drive loop. Every round either
// advances at least one pick or returns, so this is a tripwire for
// "git is not making progress", not a working limit.
const maxAutoResolveRounds = 50

// pickIsEmpty reports whether committing the current pick would create
// an empty commit: nothing unmerged and the index matches HEAD. This is
// the structural check that replaces parsing git's "nothing to commit"
// stderr.
func pickIsEmpty(ctx context.Context, runner git.Runner) bool {
	if len(listUnmergedFiles(ctx, runner)) > 0 {
		return false
	}
	_, _, err := runner.Run(ctx, "diff", "--cached", "--quiet")
	return err == nil
}

// continueStep runs `git <sub> <verb>` (verb is --continue or --skip)
// and classifies the aftermath. conflicts is non-nil only for
// continueConflict.
func continueStep(ctx context.Context, runner git.Runner, sub, verb string) (continueOutcome, []string, error) {
	_, stderr, err := runner.Run(ctx, sub, verb)
	if err != nil {
		if unmerged := listUnmergedFiles(ctx, runner); len(unmerged) > 0 {
			return continueConflict, unmerged, nil
		}
		// git stops a sequenced op itself when a later pick turns out
		// empty — surface that as a loopable outcome, not a failure.
		if state, derr := gitstate.Detect(ctx, RepoFlag()); derr == nil &&
			state.Kind != gitstate.StateNone && pickIsEmpty(ctx, runner) {
			return continueEmptyPick, nil, nil
		}
		return 0, nil, fmt.Errorf("git %s %s failed: %s: %w", sub, verb, strings.TrimSpace(string(stderr)), err)
	}

	state, derr := gitstate.Detect(ctx, RepoFlag())
	if derr != nil || state.Kind == gitstate.StateNone {
		return continueDone, nil, nil
	}
	return continuePausedOther, nil, nil
}

// driveToCompletion advances the in-progress operation until it
// finishes, pauses for a human, or (batch mode) needs a re-resolution.
// reResolve is nil in interactive mode — new conflicts then go back to
// the human, who already chose to decide hunk by hunk. In batch mode it
// re-runs the resolver with the caller's strategy and reports how many
// files the round cleared (full==false leaves the standard paused
// state).
func driveToCompletion(
	ctx context.Context,
	cmd *cobra.Command,
	runner git.Runner,
	kind gitstate.StateKind,
	rep *resolveReport,
	reResolve func(conflicts []string) (full bool, err error),
) error {
	w := cmd.OutOrStdout()
	sub, err := stateSubcommand(kind)
	if err != nil {
		return err
	}

	for i := 0; i < maxAutoResolveRounds; i++ {
		verb := "--continue"
		// merge is excluded from skipping: an empty merge commit still
		// records the ancestry join and must be committed, not skipped.
		if sub != "merge" && pickIsEmpty(ctx, runner) {
			verb = "--skip"
			rep.SkippedEmpty++
			if !JSONOut() {
				fmt.Fprintf(w, "%s pick became empty after resolution — skipped\n", color.YellowString("·"))
			}
		}

		outcome, conflicts, err := continueStep(ctx, runner, sub, verb)
		if err != nil {
			return err
		}
		switch outcome {
		case continueDone:
			rep.Done = true
			rep.State = "none"
			if !JSONOut() {
				fmt.Fprintf(w, "%s %s complete\n", color.GreenString("✓"), sub)
			}
			return nil

		case continueEmptyPick:
			continue // next round's pickIsEmpty check issues the --skip

		case continuePausedOther:
			rep.State = sub
			rep.Resume = selfCmd("continue")
			if !JSONOut() {
				printNote(cmd.ErrOrStderr(), fmt.Sprintf("%s paused (edit/break step) — finish it, then run `%s`", sub, selfRewrite("gk continue")))
			}
			return nil

		case continueConflict:
			if reResolve == nil {
				rep.State = sub
				rep.Resume = selfCmd("resolve")
				if !JSONOut() {
					fmt.Fprintf(w, "%s next pick conflicted (%d file%s): %s\n",
						color.YellowString("→"), len(conflicts), plural(len(conflicts)), strings.Join(conflicts, ", "))
					fmt.Fprintf(w, "  run `%s` again to resolve them (or `%s`)\n",
						selfRewrite("gk resolve"), selfRewrite("gk abort"))
				}
				return nil
			}
			rep.Rounds++
			full, rerr := reResolve(conflicts)
			if rerr != nil {
				return rerr
			}
			if !full {
				// The strategy could not clear everything (parse failures,
				// delete/modify conflicts). Leave the standard paused state.
				rep.State = sub
				rep.Resume = selfCmd("continue")
				return nil
			}
		}
	}
	return fmt.Errorf("gk resolve: no progress after %d continue rounds — inspect with `gk status`", maxAutoResolveRounds)
}

// resolveReport is the JSON payload for `gk resolve` (batch mode).
type resolveReport struct {
	Resolved     []string `json:"resolved"`                // files resolved across all rounds
	Total        int      `json:"total"`                   // conflicted files seen across all rounds
	Rounds       int      `json:"rounds"`                  // resolution rounds (1 unless later picks conflicted)
	SkippedEmpty int      `json:"skipped_empty,omitempty"` // picks dropped because resolution emptied them
	Done         bool     `json:"done"`                    // operation fully finished
	State        string   `json:"state"`                   // none | rebase | merge | cherry-pick | revert
	Resume       string   `json:"resume,omitempty"`        // next command when not done
}

// autoContinueBatch drives the operation to completion after a fully
// successful batch resolution, re-resolving later conflicts with the
// same options. A paused end state is a result, not an error.
func autoContinueBatch(
	ctx context.Context,
	cmd *cobra.Command,
	r *resolve.Resolver,
	kind gitstate.StateKind,
	opts resolve.ResolveOptions,
	first *resolve.ResolveResult,
) (*resolveReport, error) {
	rep := &resolveReport{
		Resolved: append([]string{}, first.Resolved...),
		Total:    first.Total,
		Rounds:   1,
	}
	reResolve := func(conflicts []string) (bool, error) {
		if !JSONOut() {
			fmt.Fprintf(cmd.OutOrStdout(), "%s next pick conflicted (%d file%s) — resolving (%s)…\n",
				color.YellowString("→"), len(conflicts), plural(len(conflicts)), opts.Strategy)
		}
		state, derr := gitstate.Detect(ctx, RepoFlag())
		if derr != nil {
			return false, derr
		}
		res, rerr := r.Run(ctx, state, opts)
		if rerr != nil {
			return false, rerr
		}
		rep.Total += res.Total
		rep.Resolved = append(rep.Resolved, res.Resolved...)
		if len(res.Resolved) != res.Total {
			if !JSONOut() {
				fmt.Fprintf(cmd.OutOrStdout(), "%d/%d conflicts resolved this round — finish the rest, then run `%s`\n",
					len(res.Resolved), res.Total, selfRewrite("gk continue"))
			}
			return false, nil
		}
		return true, nil
	}
	if err := driveToCompletion(ctx, cmd, r.Runner, kind, rep, reResolve); err != nil {
		return nil, err
	}
	return rep, nil
}

// autoContinueInteractive advances the operation after an interactive
// (TUI) resolution. Later conflicts go back to the human.
func autoContinueInteractive(
	ctx context.Context,
	cmd *cobra.Command,
	r *resolve.Resolver,
	kind gitstate.StateKind,
) error {
	rep := &resolveReport{} // narration only; interactive mode has no JSON
	return driveToCompletion(ctx, cmd, r.Runner, kind, rep, nil)
}
