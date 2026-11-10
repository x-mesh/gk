package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/policy/rules"
	"github.com/x-mesh/gk/internal/secrets/gitleaksrules"
)

func init() {
	guardCmd := &cobra.Command{
		Use:   "guard",
		Short: "Declarative repo policies (secret scan, signing, commit rules)",
		Long: `gk guard evaluates every registered policy rule against the repo and
surfaces violations. Rules live in .gk.yaml under the policies: block; the
v0.9 MVP ships with secret_patterns. By default it scans the current content
of the tracked files; --staged scans the staged changes, and --history scans
every commit reachable from any ref. It uses the gitleaks ruleset embedded in
gk (a pinned release), which follows the gitleaks detection steps and runs
faster than the gitleaks binary. gk runs the gitleaks binary instead when the
repo has .gitleaks.toml or .gitleaksignore (or GITLEAKS_CONFIG is set),
because the embedded ruleset does not read those files.
`,
	}

	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Run all policy rules and report violations",
		Long: `Evaluates every registered Rule in parallel and prints violations sorted
by severity (error → warn → info). Exit codes:

  0 — no violations, or info-only
  1 — one or more warnings
  2 — one or more errors
`,
		RunE: runGuardCheck,
	}
	checkCmd.Flags().Bool("json", false, "emit NDJSON violations (one per line) for scripting")
	checkCmd.Flags().Bool("staged", false, "scan only the staged changes (what the next commit adds); the pre-commit hook uses this")
	checkCmd.Flags().Bool("history", false, "scan every commit reachable from any ref instead of the current files")
	checkCmd.Flags().Bool("fetch", false, "fetch every remote first and scan the history, including branches that exist only on a remote (implies --history; network)")
	checkCmd.MarkFlagsMutuallyExclusive("staged", "history")
	checkCmd.MarkFlagsMutuallyExclusive("staged", "fetch")

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold .gk.yaml with a commented policies block",
		Long: `Creates .gk.yaml in the repository root (or --repo path) with a
fully-commented policies: block so you can opt in rules one by one.

Exits non-zero when .gk.yaml already exists; pass --force to overwrite.
`,
		RunE: runGuardInit,
	}
	initCmd.Flags().Bool("force", false, "overwrite existing .gk.yaml")
	initCmd.Flags().String("out", "", "write to this path instead of <repo>/.gk.yaml")

	guardCmd.AddCommand(checkCmd)
	guardCmd.AddCommand(initCmd)
	rootCmd.AddCommand(guardCmd)
}

// guardRegistry returns a freshly populated policy.Registry. Rules are
// constructed per-invocation so the binary stays safe for concurrent
// invocations and tests can swap rules via their own Registry when needed.
func guardRegistry() *policy.Registry {
	reg := policy.NewRegistry()
	secretRule := rules.NewSecretPatternsRule()
	secretRule.ScanDiff = gitleaksrules.ScanDiff
	secretRule.ScanFile = gitleaksrules.ScanFile
	secretRule.ScannerLabel = func() string {
		return fmt.Sprintf("the embedded gitleaks %s ruleset (%d rules)", gitleaksrules.Version(), gitleaksrules.RuleCount())
	}
	_ = reg.Register(secretRule)
	return reg
}

// guardFetch fetches every remote so the history scan reaches branches that
// exist only on a remote. It does not prune: a remote-tracking ref whose
// branch was deleted upstream still names commits worth scanning. A failed
// fetch becomes a warning instead of an error, because the local refs are
// still worth scanning, but the result must not read as a full scan.
func guardFetch(ctx context.Context, runner *git.ExecRunner) []policy.Violation {
	out, _, err := runner.Run(ctx, "remote")
	remotes := strings.Fields(string(out))
	if err != nil || len(remotes) == 0 {
		return []policy.Violation{{
			RuleID:   "fetch",
			Severity: policy.SeverityInfo,
			Message:  "no remote configured; scanned local refs only",
		}}
	}
	var vs []policy.Violation
	for _, res := range runContextFetch(ctx, runner, remotes) {
		if res.OK {
			continue
		}
		vs = append(vs, policy.Violation{
			RuleID:   "fetch",
			Severity: policy.SeverityWarn,
			Message:  fmt.Sprintf("fetch %s failed; branches only on %s were not scanned: %s", res.Remote, res.Remote, res.Error),
			Hint:     "check the remote URL and credentials, then rerun gk guard check --fetch",
		})
	}
	return vs
}

// violationJSON is the NDJSON shape for --json output. Keep stable.
type violationJSON struct {
	RuleID   string `json:"rule_id"`
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

func runGuardCheck(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	staged, _ := cmd.Flags().GetBool("staged")
	history, _ := cmd.Flags().GetBool("history")
	doFetch, _ := cmd.Flags().GetBool("fetch")
	var fetchViolations []policy.Violation
	if doFetch {
		fetchViolations = guardFetch(ctx, runner)
	}

	reg := guardRegistry()
	in := policy.Input{
		Runner:  runner,
		WorkDir: repo,
		Staged:  staged,
		History: history || doFetch,
	}

	violations, errs := reg.Evaluate(ctx, in)
	violations = append(fetchViolations, violations...)

	asJSON, _ := cmd.Flags().GetBool("json")
	asJSON = asJSON || JSONOut()
	w := cmd.OutOrStdout()
	if asJSON {
		// Agent mode wraps the violations as one enveloped array; the bare
		// --json NDJSON stream (one violation per line) stays for existing
		// line-oriented consumers.
		if AgentOut() {
			if err := emitAgentResult(w, violationsJSON(violations)); err != nil {
				return err
			}
		} else if err := printViolationsNDJSON(w, violations); err != nil {
			return err
		}
	} else {
		printViolationsHuman(w, violations)
	}

	// Surface rule infra errors after violations so the report is still useful.
	for _, e := range errs {
		fmt.Fprintf(cmd.ErrOrStderr(), "rule error: %v\n", e)
	}

	// Exit code derives from the worst severity.
	switch worstSeverity(violations) {
	case policy.SeverityError:
		return fmt.Errorf("%d error(s)", countSeverity(violations, policy.SeverityError))
	case policy.SeverityWarn:
		return fmt.Errorf("%d warning(s)", countSeverity(violations, policy.SeverityWarn))
	default:
		return nil
	}
}

func violationsJSON(vs []policy.Violation) []violationJSON {
	out := make([]violationJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, violationJSON{
			RuleID:   v.RuleID,
			Severity: v.Severity.String(),
			File:     v.File,
			Line:     v.Line,
			Message:  v.Message,
			Hint:     v.Hint,
		})
	}
	return out
}

func printViolationsNDJSON(w io.Writer, vs []policy.Violation) error {
	enc := json.NewEncoder(w)
	for _, v := range violationsJSON(vs) {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	return nil
}

func printViolationsHuman(w io.Writer, vs []policy.Violation) {
	if len(vs) == 0 {
		fmt.Fprintln(w, "gk guard: no violations")
		return
	}
	for _, v := range vs {
		loc := v.RuleID
		if v.File != "" {
			if v.Line > 0 {
				loc = fmt.Sprintf("%s (%s:%d)", v.RuleID, v.File, v.Line)
			} else {
				loc = fmt.Sprintf("%s (%s)", v.RuleID, v.File)
			}
		}
		fmt.Fprintf(w, "%-5s  %s  %s\n", v.Severity, loc, v.Message)
		if v.Hint != "" {
			fmt.Fprintf(w, "       hint: %s\n", v.Hint)
		}
	}
}

// worstSeverity finds the lowest numeric Severity in vs (most severe).
// Returns SeverityInfo when vs is empty.
func worstSeverity(vs []policy.Violation) policy.Severity {
	if len(vs) == 0 {
		return policy.SeverityInfo
	}
	worst := policy.SeverityInfo
	for _, v := range vs {
		if v.Severity < worst {
			worst = v.Severity
		}
	}
	return worst
}

func countSeverity(vs []policy.Violation, s policy.Severity) int {
	n := 0
	for _, v := range vs {
		if v.Severity == s {
			n++
		}
	}
	return n
}

// gkYAMLTemplate is the scaffold written by `gk guard init`. Every rule block
// is commented out so the file is valid YAML and the user opts in explicitly.
const gkYAMLTemplate = `# .gk.yaml — gk repository policy configuration
# Run: gk guard check
# Docs: https://github.com/x-mesh/gk/blob/main/docs/commands.md#gk-guard
#
# Uncomment the rules you want to enforce. Each rule maps to a built-in
# gk guard policy; unknown keys are silently ignored so future rule names
# can be added here before upgrading gk.

policies:

  # ── secret_patterns ────────────────────────────────────────────────────────
  # Scans the tracked files (or, with --staged / --history, the staged
  # changes or the full history) for secrets with the gitleaks ruleset
  # embedded in gk. If .gitleaks.toml or .gitleaksignore
  # exists, gk runs gitleaks instead, because the embedded ruleset does not
  # read those files.
  # Optional: brew install gitleaks  (gk doctor will confirm)
  #
  # secret_patterns:
  #   enabled: true
  #   mode: files      # files (tracked files) | staged | history
  #   redact: true     # replace the matched value with REDACTED in output

  # ── max_commit_size ────────────────────────────────────────────────────────
  # Rejects commits whose diff exceeds a line or file count threshold.
  # Helps keep PRs reviewable and avoids accidental large-file check-ins.
  #
  # max_commit_size:
  #   enabled: true
  #   max_lines: 1000
  #   max_files: 50

  # ── required_trailers ──────────────────────────────────────────────────────
  # Enforces that every commit message contains specified git trailers
  # (e.g., Signed-off-by, Change-Id, Jira-Ticket).
  #
  # required_trailers:
  #   enabled: true
  #   trailers:
  #     - Signed-off-by
  #     # - Jira-Ticket

  # ── forbid_force_push_to ───────────────────────────────────────────────────
  # Blocks force-pushes to the listed branches when used as a pre-push hook.
  #
  # forbid_force_push_to:
  #   enabled: true
  #   branches:
  #     - main
  #     - master
  #     - develop

  # ── require_signed ─────────────────────────────────────────────────────────
  # Verifies that every commit in the push range carries a GPG/SSH signature.
  #
  # require_signed:
  #   enabled: false


# ── Allow-list ─────────────────────────────────────────────────────────────
# To suppress a specific finding, add an entry to .gk/allow.yaml instead of
# disabling the rule entirely. Each entry requires a justification and an
# optional expiry date so suppressions self-audit over time.
#
# Example .gk/allow.yaml:
#
#   - rule: secret_patterns
#     file: scripts/seed.sh
#     line: 42
#     reason: "Test credential — rotated monthly, never reaches prod"
#     expires: 2026-12-31
`

func runGuardInit(cmd *cobra.Command, _ []string) error {
	outPath, _ := cmd.Flags().GetString("out")
	force, _ := cmd.Flags().GetBool("force")

	if outPath == "" {
		repo := RepoFlag()
		if repo == "" {
			var err error
			repo, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("guard init: cannot determine working directory: %w", err)
			}
		}
		outPath = filepath.Join(repo, ".gk.yaml")
	}

	if _, err := os.Stat(outPath); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("guard init: stat %s: %w", outPath, err)
	}

	if err := os.WriteFile(outPath, []byte(gkYAMLTemplate), 0o644); err != nil {
		return fmt.Errorf("guard init: write %s: %w", outPath, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", outPath)
	fmt.Fprintln(cmd.OutOrStdout(), stylizeHintLine("next: uncomment rules, then run `gk guard check`"))
	return nil
}
