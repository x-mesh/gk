package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// gk batch executes an ordered JSON plan of gk sub-commands as one
// transaction — the agent expresses a multi-step workflow ("commit, then
// pull, then push, then tag") as a single call instead of a turn per step.
// It is the generalized sibling of gk land: land is the fixed
// session-closing sequence, batch is whatever sequence the caller declares.
//
// The plan/contract follows the proven shapes already in the binary:
// `--plan -` / `--plan <file>` and `--plan-template` mirror gk rebase;
// per-step results, failed_step, and resume mirror gk land. Steps run as
// child gk processes with GK_AGENT stripped — batch owns the machine
// contract, children print human progress.

type batchStepJSON struct {
	// Name labels the step in results; defaults to the sub-command word.
	Name string `json:"name,omitempty"`
	// Args is the full gk argv for the step, e.g. ["pull", "--with-base"].
	Args []string `json:"args"`
	// OnFailure: "abort" (default) stops the plan; "continue" records the
	// failure and proceeds — for steps that are nice-to-have, not gating.
	OnFailure string `json:"on_failure,omitempty"`
}

type batchPlanJSON struct {
	Schema int             `json:"schema"`
	Steps  []batchStepJSON `json:"steps"`
}

type batchStepRun struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	// Result: ok | failed | paused | skipped
	Result   string `json:"result"`
	ExitCode int    `json:"exit_code,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// batchResultJSON is the agent contract for `gk batch`. Fields are
// append-only.
type batchResultJSON struct {
	Schema int `json:"schema"`
	// Result: completed (all steps ok) | partial (failures were marked
	// on_failure:continue and the plan ran to the end) | failed (a gating
	// step stopped the plan) | dry-run
	Result     string         `json:"result"`
	Steps      []batchStepRun `json:"steps"`
	FailedStep string         `json:"failed_step,omitempty"`
	Resume     string         `json:"resume,omitempty"`
}

// batchMaxSteps caps plan size: a plan beyond this is almost certainly a
// generated runaway, and each step is a child process.
const batchMaxSteps = 20

func init() {
	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Run an ordered JSON plan of gk commands as one transaction",
		Long: `Executes gk sub-commands in the order a JSON plan lists them, stopping at
the first gating failure and reporting per-step results — a multi-step
workflow becomes one call:

  1. gk batch --plan-template             # starter plan (JSON) to edit
  2. gk batch --plan - < plan.json        # validate + execute
     echo '{"steps":[{"args":["pull"]},{"args":["push"]}]}' | gk batch --plan -

Plan schema: {"steps":[{"args":["pull","--with-base"], "name?", "on_failure?"}]}
  args        full gk argv for the step (sub-command first)
  on_failure  "abort" (default) stops the plan; "continue" records and moves on

With the global --json flag (or GK_AGENT=1) the result is a machine
contract: {result, steps:[{name,command,result,exit_code}], failed_step?,
resume?}; child output moves to stderr so stdout stays parseable. A child
that pauses for conflict resolution (exit 3) stops the plan with
result:"failed" and resume:"gk context" — the paused state carries its own
resume commands.`,
		Args: cobra.NoArgs,
		RunE: runBatch,
	}
	cmd.Flags().String("plan", "", "JSON plan: a file path, or '-' for stdin")
	cmd.Flags().Bool("plan-template", false, "emit a starter plan (JSON) and exit")
	rootCmd.AddCommand(cmd)
}

func runBatch(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}

	if template, _ := cmd.Flags().GetBool("plan-template"); template {
		tpl := batchPlanJSON{Schema: 1, Steps: []batchStepJSON{
			{Name: "commit", Args: []string{"commit", "-f"}},
			{Name: "pull", Args: []string{"pull", "--with-base"}},
			{Name: "push", Args: []string{"push"}, OnFailure: "abort"},
		}}
		return emitAgentResult(cmd.OutOrStdout(), tpl)
	}

	planArg, _ := cmd.Flags().GetString("plan")
	if planArg == "" {
		return WithHint(
			fmt.Errorf("batch: no plan given"),
			"start from a draft: gk batch --plan-template, then feed it back with --plan - (stdin) or --plan <file>",
		)
	}
	plan, err := readBatchPlan(cmd, planArg)
	if err != nil {
		return err
	}
	if err := validateBatchPlan(plan); err != nil {
		return err
	}

	jsonMode := JSONOut()
	// Progress goes to stderr in JSON mode so stdout carries only the
	// result document; in human mode everything shares stdout like land.
	progress := cmd.OutOrStdout()
	if jsonMode {
		progress = cmd.ErrOrStderr()
	}

	if DryRun() {
		res := batchResultJSON{Schema: 1, Result: "dry-run"}
		fmt.Fprintln(progress, landHeader("─── Batch plan ───────────────────────────────"))
		for _, s := range plan.Steps {
			cmdline := "gk " + strings.Join(s.Args, " ")
			fmt.Fprintf(progress, "  %-12s %s%s\n", batchStepName(s), cmdline, batchPolicySuffix(s))
			res.Steps = append(res.Steps, batchStepRun{Name: batchStepName(s), Command: cmdline, Result: "dry-run"})
		}
		if jsonMode {
			return emitAgentResult(cmd.OutOrStdout(), res)
		}
		return nil
	}

	gkPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("batch: locate gk binary: %w", err)
	}

	good := color.New(color.FgGreen, color.Bold).SprintFunc()
	bad := color.New(color.FgRed, color.Bold).SprintFunc()
	res := batchResultJSON{Schema: 1, Result: "completed"}
	failures := 0
	for i, s := range plan.Steps {
		name := batchStepName(s)
		cmdline := "gk " + strings.Join(s.Args, " ")
		fmt.Fprintln(progress, landHeader(fmt.Sprintf("─── batch %d/%d: %s ─────────────────────────", i+1, len(plan.Steps), name)))

		exitCode, stepErr := batchRunChild(ctx, gkPath, repo, jsonMode, s.Args...)
		if stepErr == nil {
			fmt.Fprintf(progress, "  %s %-12s\n", good("✓"), name)
			res.Steps = append(res.Steps, batchStepRun{Name: name, Command: cmdline, Result: "ok"})
			continue
		}

		stepResult := "failed"
		detail := stepErr.Error()
		if exitCode == 3 {
			// Exit 3 is gk's paused-state contract (conflict stopped the
			// child): a result, not a crash — but the plan cannot proceed
			// over an unresolved pause regardless of on_failure.
			stepResult = "paused"
			detail = "paused (exit 3) — resolve, then resume the remaining steps manually"
		}
		res.Steps = append(res.Steps, batchStepRun{
			Name: name, Command: cmdline, Result: stepResult, ExitCode: exitCode, Detail: detail,
		})
		fmt.Fprintf(progress, "  %s %-12s %s\n", bad("✗"), name, detail)

		if s.OnFailure == "continue" && stepResult != "paused" {
			failures++
			continue
		}

		res.Result = "failed"
		res.FailedStep = name
		res.Resume = selfCmd("context")
		// Skip-mark the rest so the caller sees the full plan accounted for.
		for _, rest := range plan.Steps[i+1:] {
			res.Steps = append(res.Steps, batchStepRun{
				Name:    batchStepName(rest),
				Command: "gk " + strings.Join(rest.Args, " "),
				Result:  "skipped",
				Detail:  "previous step failed",
			})
		}
		if jsonMode {
			_ = emitAgentResult(cmd.OutOrStdout(), res)
		}
		return WithRemedy(
			fmt.Errorf("batch: step %q failed: %w", name, stepErr),
			"orient with gk context, fix the failure, then rerun the remaining steps",
			errRemedy{Command: selfCmd("context"), Safety: "safe"},
		)
	}

	if failures > 0 {
		res.Result = "partial"
	}
	fmt.Fprintln(progress, landHeader("─── Batch complete ───────────────────────────"))
	fmt.Fprintf(progress, "  %s %d/%d steps ok\n", good("✓"), len(plan.Steps)-failures, len(plan.Steps))
	if jsonMode {
		return emitAgentResult(cmd.OutOrStdout(), res)
	}
	return nil
}

func batchStepName(s batchStepJSON) string {
	if s.Name != "" {
		return s.Name
	}
	if len(s.Args) > 0 {
		return s.Args[0]
	}
	return "?"
}

func batchPolicySuffix(s batchStepJSON) string {
	if s.OnFailure == "continue" {
		return "  (on_failure: continue)"
	}
	return ""
}

// readBatchPlan loads the plan document from stdin ("-") or a file path.
func readBatchPlan(cmd *cobra.Command, planArg string) (batchPlanJSON, error) {
	var plan batchPlanJSON
	var raw []byte
	var err error
	if planArg == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(planArg)
	}
	if err != nil {
		return plan, fmt.Errorf("batch: read plan: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&plan); derr != nil {
		return plan, WithHint(
			fmt.Errorf("batch: invalid plan JSON: %v", derr),
			"expected {\"steps\":[{\"args\":[\"pull\",\"--with-base\"]}]} — draft one with gk batch --plan-template",
		)
	}
	return plan, nil
}

// validateBatchPlan rejects plans that could not run as written: every step
// must name a real gk sub-command, recursion is fenced off, and the size is
// capped. Validation failures happen before any step executes — a plan is
// all-or-nothing at the input boundary.
func validateBatchPlan(plan batchPlanJSON) error {
	if plan.Schema != 0 && plan.Schema != 1 {
		return fmt.Errorf("batch: unsupported plan schema %d (want 1)", plan.Schema)
	}
	if len(plan.Steps) == 0 {
		return fmt.Errorf("batch: plan has no steps")
	}
	if len(plan.Steps) > batchMaxSteps {
		return fmt.Errorf("batch: plan has %d steps (max %d)", len(plan.Steps), batchMaxSteps)
	}
	for i, s := range plan.Steps {
		if len(s.Args) == 0 {
			return fmt.Errorf("batch: step %d has no args", i+1)
		}
		sub := s.Args[0]
		if strings.HasPrefix(sub, "-") {
			return fmt.Errorf("batch: step %d: args must start with a sub-command, got flag %q", i+1, sub)
		}
		if sub == "batch" {
			return fmt.Errorf("batch: step %d: nested batch is not allowed", i+1)
		}
		if !batchKnownSubcommand(sub) {
			return fmt.Errorf("batch: step %d: unknown gk sub-command %q", i+1, sub)
		}
		switch s.OnFailure {
		case "", "abort", "continue":
		default:
			return fmt.Errorf("batch: step %d: on_failure must be \"abort\" or \"continue\", got %q", i+1, s.OnFailure)
		}
	}
	return nil
}

// batchKnownSubcommand checks the word against the live command tree, so the
// validation surface always matches the installed binary.
func batchKnownSubcommand(name string) bool {
	for _, c := range rootCmd.Commands() {
		if c.Name() == name {
			return true
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

// batchRunChild is swapped by tests to fake child executions.
var batchRunChild = runBatchChild

// runBatchChild executes one step as a child gk process and reports its
// exit code. Mirrors land's child contract: the child inherits the
// terminal, JSON mode reroutes its stdout to stderr so batch's stdout
// carries only the result document, and GK_AGENT is stripped — batch owns
// the machine contract.
func runBatchChild(ctx context.Context, gkPath, repo string, jsonMode bool, args ...string) (int, error) {
	c := exec.CommandContext(ctx, gkPath, args...)
	if repo != "" {
		c.Dir = repo
	}
	c.Stdin = os.Stdin
	c.Stderr = os.Stderr
	if jsonMode {
		c.Stdout = os.Stderr
	} else {
		c.Stdout = os.Stdout
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GK_AGENT=") {
			continue
		}
		env = append(env, kv)
	}
	c.Env = env
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), fmt.Errorf("exit %d", ee.ExitCode())
		}
		return -1, err
	}
	return 0, nil
}
