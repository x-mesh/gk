package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/commitlint"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// runAICommitInteractive is the human-facing counterpart to `gk commit --plan`:
// instead of writing the plan JSON by hand, the user groups working-tree files
// into commits in a TUI. The TUI builds the same commitPlanJSON the declarative
// path consumes, so validation, the secret gate, and the apply behind a backup
// ref are identical (applyCommitPlan). No provider, no LLM call.
func runAICommitInteractive(cmd *cobra.Command, ctx context.Context, runner git.Runner, cfg *config.Config, ai config.AIConfig, flags aiCommitFlags) error {
	if !ui.IsTerminal() {
		return WithHint(
			fmt.Errorf("commit --interactive needs a terminal"),
			"for non-interactive grouping write the plan yourself: gk commit --plan-template | … | gk commit --plan -",
		)
	}

	// Gather the same committable universe the --plan path validates against,
	// so what the picker shows is exactly what can be committed. Denied paths
	// (secret-bearing globs) are never offered.
	files, err := aicommit.GatherWIP(ctx, runner, aicommit.GatherOptions{
		Scope:     aicommit.ScopeAll,
		DenyPaths: ai.Commit.DenyPaths,
	})
	if err != nil {
		return err
	}
	committable := make([]aicommit.FileChange, 0, len(files))
	for _, f := range files {
		if f.DeniedBy != "" {
			continue
		}
		committable = append(committable, f)
	}
	if len(committable) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "commit: no working-tree changes to commit")
		return nil
	}

	rules := commitlint.Rules{
		AllowedTypes:     cfg.Commit.Types,
		ScopeRequired:    cfg.Commit.ScopeRequired,
		MaxSubjectLength: cfg.Commit.MaxSubjectLength,
	}

	plan, err := runCommitGroupTUI(ctx, committable, rules)
	if err != nil {
		// A user abort (esc/ctrl+c) is not an error — nothing was committed.
		if errors.Is(err, ui.ErrPickerAborted) {
			fmt.Fprintln(cmd.OutOrStdout(), "commit: interactive grouping aborted; nothing committed")
			return nil
		}
		return err
	}
	if len(plan.Commits) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "commit: no files selected; nothing committed")
		return nil
	}

	return applyCommitPlan(cmd, ctx, runner, cfg, ai, flags, plan)
}

// runCommitGroupTUI drives the interactive grouping loop: each round the user
// selects a set of files for the next commit (live preview of the selection)
// and types its Conventional-Commit message (re-prompted until commitlint
// passes), and the chosen files leave the remaining set. Confirming an empty
// selection finishes; esc/ctrl+c aborts the whole session (ui.ErrPickerAborted).
// The result is a commitPlanJSON ready for applyCommitPlan — files left
// unselected simply stay in the working tree (validateCommitPlan permits it).
func runCommitGroupTUI(ctx context.Context, files []aicommit.FileChange, rules commitlint.Rules) (commitPlanJSON, error) {
	remaining := append([]aicommit.FileChange(nil), files...)
	var commits []commitPlanEntryJSON

	for len(remaining) > 0 {
		items := make([]ui.MultiSelectItem, 0, len(remaining))
		for _, f := range remaining {
			status := f.Status
			if status == "" {
				status = "?"
			}
			items = append(items, ui.MultiSelectItem{
				Key:     f.Path,
				Display: fmt.Sprintf("%-2s %s", status, f.Path),
			})
		}

		title := fmt.Sprintf("commit %d — pick files for this commit (space toggle · enter confirm · confirm empty to finish)", len(commits)+1)
		preview := func(sel []string) string {
			if len(sel) == 0 {
				return "(nothing selected — confirm to finish; esc to abort)"
			}
			return fmt.Sprintf("%d file(s): %s", len(sel), strings.Join(sel, ", "))
		}

		selected, err := ui.MultiSelectPreviewTUI(ctx, title, items, nil, preview)
		if err != nil {
			return commitPlanJSON{}, err
		}
		if len(selected) == 0 {
			break // confirmed an empty selection → done
		}

		msg, err := promptCommitMessage(ctx, rules, len(commits)+1)
		if err != nil {
			return commitPlanJSON{}, err
		}

		commits = append(commits, commitPlanEntryJSON{Message: msg, Files: selected})
		remaining = removeByPath(remaining, selected)
	}

	return commitPlanJSON{Schema: 1, Commits: commits}, nil
}

// promptCommitMessage asks for one commit message and re-prompts (carrying the
// last attempt forward for editing) until it parses clean against the repo's
// commitlint rules. Returns ui.ErrPickerAborted if the user escapes.
func promptCommitMessage(ctx context.Context, rules commitlint.Rules, n int) (string, error) {
	title := fmt.Sprintf("commit %d message (Conventional Commits)", n)
	last := ""
	for {
		got, err := ui.PromptTextTUI(ctx, title, "feat(scope): subject", last)
		if err != nil {
			return "", err // ErrPickerAborted on esc, ErrNonInteractive guarded upstream
		}
		msg := strings.TrimSpace(got)
		// PromptTextTUI rejects empty input itself, so msg is non-empty here.
		parsed := commitlint.Parse(msg)
		if issues := commitlint.Lint(parsed, rules); len(issues) > 0 {
			last = msg
			title = "commit message — ✗ " + issues[0].Message
			continue
		}
		return msg, nil
	}
}

// removeByPath returns files whose Path is not in the drop set.
func removeByPath(files []aicommit.FileChange, drop []string) []aicommit.FileChange {
	gone := make(map[string]bool, len(drop))
	for _, p := range drop {
		gone[p] = true
	}
	out := files[:0:0]
	for _, f := range files {
		if !gone[f.Path] {
			out = append(out, f)
		}
	}
	return out
}
