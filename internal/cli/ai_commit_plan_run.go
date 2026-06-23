package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/commitlint"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// gk commit --plan is the deterministic, AI-free path through `gk commit`:
// instead of classify+compose, the caller hands gk a JSON plan that already
// groups working-tree files into commits with pre-written messages. gk's job
// is to validate that plan against the real tree + commitlint rules, run the
// same commit-time guards the AI path runs (gofmt advisory, secret scan), and
// apply it behind a backup ref. No provider is ever constructed on this path —
// runAICommit routes here BEFORE provider resolution.

// commitPlanStepRun is one commit's outcome in the result contract. SHA is the
// created commit (empty on a skipped/failed/dry-run entry); Detail carries the
// failure reason when Result is "failed".
type commitPlanStepRun struct {
	Message string   `json:"message"`
	Files   []string `json:"files"`
	// Result: ok | failed | dry-run
	Result string `json:"result"`
	SHA    string `json:"sha,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// commitPlanResultJSON is the machine-readable outcome of `gk commit --plan`
// (--json / agent mode). Fields are append-only — mirrors landResultJSON's
// shape so agent tooling reads the same {result, steps, failed_at} contract.
type commitPlanResultJSON struct {
	Schema int `json:"schema"`
	// Result: completed | partial | failed | dry-run
	Result    string              `json:"result"`
	Commits   []commitPlanStepRun `json:"commits"`
	FailedAt  string              `json:"failed_at,omitempty"`
	BackupRef string              `json:"backup_ref,omitempty"`
}

// runAICommitPlanTemplate emits the current working-tree changes as a
// commit-plan draft and exits — the AI-free counterpart to classify. The
// caller (LLM or human) edits the message(s) and feeds the result back via
// --plan.
//
// Shape decision: every dirty path lands in ONE entry with an empty message,
// mirroring rebase --plan-template's "every commit pre-filled as pick" — the
// template states the FACTS (which files changed), the caller supplies the
// JUDGMENT (how to group + message them). A per-file entry would prejudge the
// grouping into N one-file commits, the opposite of what we want the agent to
// decide. The informational Status/Kind on the single entry describe the first
// file as a representative hint; they round-trip back harmlessly (readCommitPlan
// accepts but ignores them).
func runAICommitPlanTemplate(cmd *cobra.Command, ctx context.Context, runner git.Runner, ai config.AIConfig) error {
	files, err := aicommit.GatherWIP(ctx, runner, aicommit.GatherOptions{
		Scope:     aicommit.ScopeAll,
		DenyPaths: ai.Commit.DenyPaths,
	})
	if err != nil {
		return err
	}

	paths := make([]string, 0, len(files))
	for _, f := range files {
		// Denied paths must never be forwarded; they are also not committable
		// through the plan, so leave them out of the draft entirely.
		if f.DeniedBy != "" {
			continue
		}
		paths = append(paths, f.Path)
	}
	if len(paths) == 0 {
		// Emit an empty plan rather than erroring: a template of nothing is a
		// valid (if useless) answer, and keeps the command non-failing for
		// scripts that always template-then-check.
		return emitAgentResult(cmd.OutOrStdout(), commitPlanJSON{Schema: 1, Commits: []commitPlanEntryJSON{}})
	}

	entry := commitPlanEntryJSON{
		Message: "",
		Files:   paths,
	}
	// Representative Status/Kind from the first file — informational only, so
	// the agent sees what KIND of changes are in scope without us pretending a
	// single label fits a mixed set.
	entry.Status = files[0].Status
	entry.Kind = aicommit.FileKind(files[0].Path)

	tpl := commitPlanJSON{Schema: 1, Commits: []commitPlanEntryJSON{entry}}
	return emitAgentResult(cmd.OutOrStdout(), tpl)
}

// runAICommitPlan applies a deterministic commit plan: read → validate against
// the real working tree + commitlint → gofmt advisory + secret guard → apply
// behind a backup ref. No provider, no LLM call. The result is the
// commitPlanResultJSON contract (JSON mode → stdout, progress → stderr; human
// mode → per-commit ✓ lines).
func runAICommitPlan(cmd *cobra.Command, ctx context.Context, runner git.Runner, cfg *config.Config, ai config.AIConfig, flags aiCommitFlags) error {
	// Read the plan: "-" = stdin, else a file path (rebase.go:155-163).
	var planReader io.Reader
	if flags.plan == "-" {
		planReader = cmd.InOrStdin()
	} else {
		f, oerr := os.Open(flags.plan)
		if oerr != nil {
			return fmt.Errorf("commit: open plan: %w", oerr)
		}
		defer f.Close()
		planReader = f
	}
	plan, err := readCommitPlan(planReader)
	if err != nil {
		return err
	}
	return applyCommitPlan(cmd, ctx, runner, cfg, ai, flags, plan)
}

// applyCommitPlan validates a commit plan against the real working tree +
// commitlint, runs the gofmt advisory + secret guard over the plan's files,
// then converts and applies it behind a backup ref. No provider, no LLM call.
// Shared by the `--plan` path (plan read from stdin/a file) and the
// `--interactive` TUI path (plan built from the user's file grouping), so both
// get the identical validation, secret gate, and apply semantics. The result is
// the commitPlanResultJSON contract (JSON mode → stdout, progress → stderr;
// human mode → per-commit ✓ lines).
func applyCommitPlan(cmd *cobra.Command, ctx context.Context, runner git.Runner, cfg *config.Config, ai config.AIConfig, flags aiCommitFlags, plan commitPlanJSON) error {
	jsonMode := JSONOut()

	// Progress goes to stderr in JSON mode so stdout carries only the result
	// document; in human mode everything shares stdout (same split as land).
	progress := cmd.OutOrStdout()
	if jsonMode {
		progress = cmd.ErrOrStderr()
	}

	// Build the dirty universe from the real tree, then validate the plan
	// against it + the repo's commitlint rules (lint_commit.go:37 selects the
	// same three fields).
	files, err := aicommit.GatherWIP(ctx, runner, aicommit.GatherOptions{
		Scope:     aicommit.ScopeAll,
		DenyPaths: ai.Commit.DenyPaths,
	})
	if err != nil {
		return err
	}
	dirty := make(map[string]bool, len(files))
	for _, f := range files {
		// A denied path is not part of the committable universe — treating it
		// as dirty would let a plan stage a secret-bearing file the deny glob
		// exists to keep out.
		if f.DeniedBy != "" {
			continue
		}
		dirty[f.Path] = true
	}
	rules := commitlint.Rules{
		AllowedTypes:     cfg.Commit.Types,
		ScopeRequired:    cfg.Commit.ScopeRequired,
		MaxSubjectLength: cfg.Commit.MaxSubjectLength,
	}
	if err := validateCommitPlan(plan, dirty, rules); err != nil {
		return err
	}

	// 3. Scope the guards to the files the plan actually commits — uncovered
	// dirty files stay in the tree untouched, so they don't need scanning here.
	planFiles := planScopeFiles(plan, files)

	// gofmt advisory (same gate as the AI path; bypassed by --no-verify).
	if !flags.noVerify {
		guardGofmt(ctx, cmd.ErrOrStderr(), RepoFlag(), planFiles)
	}

	// Secret guard. The AI path scans the payload before any commit
	// (ai_commit.go:208-250) and `gk commit -f` is no exception, so the
	// declarative path runs the identical gate — a hand-authored plan must not
	// become a hole that writes a credential into history. The scan ALWAYS runs
	// (like the AI path): --no-verify / --allow-secret-kind all turn a finding
	// from an abort into a loud-but-bypassed report, they do not skip the scan.
	// We reuse summariseForSecretScan / secretBypass / renderFindings verbatim
	// so the abort + bypass semantics match the AI path exactly; the only
	// difference is the scan scope (plan files, not the whole dirty set).
	payload := summariseForSecretScan(planFiles)
	findings, serr := aicommit.ScanPayload(ctx, payload, aicommit.SecretGateOptions{
		AllowKinds:  flags.allowSecretKinds,
		RunGitleaks: true,
	}, nil)
	if serr != nil {
		return serr
	}
	if len(findings) > 0 {
		if secretBypass(flags.noVerify, flags.allowSecretKinds) {
			via := "--allow-secret-kind all"
			if flags.noVerify {
				via = "--no-verify"
			}
			renderFindings(cmd.ErrOrStderr(), findings)
			fmt.Fprintf(cmd.ErrOrStderr(),
				"⚠️  commit: %d secret finding(s) BYPASSED via %s — these WILL be written into git history. Rotate any real credential.\n",
				len(findings), via)
		} else {
			renderFindings(cmd.ErrOrStderr(), findings)
			return fmt.Errorf("commit: aborted due to %d secret finding(s); fix, allow a kind with --allow-secret-kind <kind>, or bypass everything with --allow-secret-kind all / --no-verify",
				len(findings))
		}
	}

	// 4. Convert + apply. --dry-run validates and reports the plan without
	// touching git (ApplyOptions.DryRun stages/commits nothing).
	msgs := planToMessages(plan)
	applyOpts := aicommit.ApplyOptions{DryRun: flags.dryRun}
	result, applyErr := aicommit.ApplyMessages(ctx, runner, msgs, applyOpts)

	res := buildCommitPlanResult(plan, result, applyErr, flags.dryRun)

	// On apply failure, emit the partial result first (so an agent sees which
	// commits landed), then return a remedy-decorated error. Commits already
	// made remain in history; the backup ref + `gk commit --abort` roll back.
	if applyErr != nil {
		if jsonMode {
			_ = emitAgentResult(cmd.OutOrStdout(), res)
		} else {
			printCommitPlanSummary(progress, res)
		}
		printBackupHint(cmd.ErrOrStderr(), result.BackupRef)
		return WithRemedy(
			fmt.Errorf("commit: apply plan: %w", applyErr),
			"fix the plan or working tree, then rerun",
			errRemedy{Command: "gk commit --plan-template", Safety: "safe"},
		)
	}

	if jsonMode {
		return emitAgentResult(cmd.OutOrStdout(), res)
	}
	printCommitPlanSummary(progress, res)
	return nil
}

// planScopeFiles returns the FileChange entries the plan actually commits —
// the intersection of the plan's named files with the gathered dirty set.
// Used to scope the gofmt + secret guards to what is about to enter history.
func planScopeFiles(plan commitPlanJSON, files []aicommit.FileChange) []aicommit.FileChange {
	want := map[string]bool{}
	for _, e := range plan.Commits {
		for _, f := range e.Files {
			want[f] = true
		}
	}
	out := make([]aicommit.FileChange, 0, len(want))
	for _, f := range files {
		if want[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// buildCommitPlanResult assembles the result contract from the apply outcome.
// On a partial failure (some commits made, then ApplyMessages stopped) the
// entry whose SHA is missing is the failure point — named in FailedAt.
func buildCommitPlanResult(plan commitPlanJSON, result aicommit.ApplyResult, applyErr error, dryRun bool) commitPlanResultJSON {
	res := commitPlanResultJSON{Schema: 1, BackupRef: result.BackupRef}
	failedIdx := -1
	for i, e := range plan.Commits {
		step := commitPlanStepRun{Message: e.Message, Files: e.Files}
		sha := ""
		if i < len(result.CommitShas) {
			sha = result.CommitShas[i]
		}
		switch {
		case dryRun:
			step.Result = "dry-run"
		case sha != "":
			step.Result = "ok"
			step.SHA = sha
		default:
			// No SHA on a non-dry-run entry: this is where apply stopped (or a
			// later, un-attempted entry). Mark the first such one as the
			// failure point.
			step.Result = "failed"
			if applyErr != nil && failedIdx == -1 {
				failedIdx = i
				step.Detail = applyErr.Error()
			}
		}
		res.Commits = append(res.Commits, step)
	}
	switch {
	case dryRun:
		res.Result = "dry-run"
	case applyErr == nil:
		res.Result = "completed"
	case failedIdx > 0:
		// At least one commit landed before the failure.
		res.Result = "partial"
		res.FailedAt = plan.Commits[failedIdx].Message
	default:
		res.Result = "failed"
		if len(plan.Commits) > 0 {
			res.FailedAt = plan.Commits[0].Message
		}
	}
	return res
}

// printCommitPlanSummary renders the human-mode outcome: one line per commit
// (✓ <sha> <message-header> / dry-run / ✗), then a backup-ref footer. Kept
// simpler than printApplySummary — the plan already carries the messages, so
// there's no group re-render.
func printCommitPlanSummary(out io.Writer, res commitPlanResultJSON) {
	if res.Result == "dry-run" {
		fmt.Fprintf(out, "commit: dry-run — %d commit(s) would be made from the plan\n", len(res.Commits))
		for _, c := range res.Commits {
			fmt.Fprintf(out, "  %s  %s\n", "dry-run", firstLine(c.Message))
		}
		return
	}
	for _, c := range res.Commits {
		switch c.Result {
		case "ok":
			fmt.Fprintln(out, successLinef("commit", "%s  %s", shortSHA(c.SHA), firstLine(c.Message)))
		case "failed":
			fmt.Fprintf(out, "  %s  %s\n", "✗", firstLine(c.Message))
		}
	}
	if res.BackupRef != "" {
		fmt.Fprintln(out, cellFaint("(backup ref: "+res.BackupRef+")"))
	}
}
