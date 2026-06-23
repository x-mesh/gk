package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/sessionaudit"
)

func init() {
	sessionCmd := &cobra.Command{
		Use:   "session",
		Short: "Audit local AI agent sessions for git/gk usage patterns",
		Long: `Inspect local Codex and Claude JSONL session logs and report where
agents still fall back to raw git or shell chains that git-kit can absorb.

By default it scans local session roots:
  ~/.codex/sessions
  ~/.claude/projects
  ~/.claude/sessions

Pass files or directories to audit a specific subset.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	auditCmd := &cobra.Command{
		Use:   "audit [session-file-or-dir...]",
		Short: "Find raw git fallback patterns in Codex/Claude sessions",
		Long: `Reads JSONL session logs, extracts shell commands from common Codex
and Claude tool-call shapes, then classifies raw git, git-kit, short gk alias,
shell-chain, conflict-probe, release, and commit-flow patterns.

Each shell-chain finding carries a synthesized git-kit batch --plan - payload
that replaces the observed chain, and the report prints a git-kit adoption rate
(plus the count of raw-git hits that already have a git-kit path) so guidance
regressions can be tracked across reruns.

The command is local and read-only. With --json or GK_AGENT=1 it emits the
standard machine-readable envelope.`,
		Args: cobra.ArbitraryArgs,
		RunE: runSessionAudit,
	}
	auditCmd.Flags().Int("max-files", 200, "maximum newest JSONL session files to scan")
	sessionCmd.AddCommand(auditCmd)
	rootCmd.AddCommand(sessionCmd)
}

func runSessionAudit(cmd *cobra.Command, args []string) error {
	maxFiles, _ := cmd.Flags().GetInt("max-files")
	report, err := sessionaudit.Audit(sessionaudit.Options{
		Paths:    args,
		MaxFiles: maxFiles,
	})
	if err != nil {
		return err
	}
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), report)
	}
	renderSessionAudit(cmd.OutOrStdout(), report)
	return nil
}

func renderSessionAudit(w io.Writer, report sessionaudit.Report) {
	fmt.Fprintf(w, "session audit: %d files, %d shell command(s)\n",
		report.Totals.Files, report.Totals.Commands)
	fmt.Fprintf(w, "usage: raw git %d, git-kit %d, gk(short) %d, shell chains %d\n",
		report.Totals.RawGit, report.Totals.GitKit, report.Totals.GKShort, report.Totals.ShellChains)
	if a := report.Adoption; a.GitInvocations > 0 {
		fmt.Fprintf(w, "adoption: git-kit %d of %d git calls (%.1f%%); %d raw calls had a git-kit path\n",
			a.GitKit, a.GitInvocations, a.Rate*100, a.CoveredRawHits)
	}

	if len(report.Findings) == 0 {
		fmt.Fprintln(w, "findings: none")
	} else {
		fmt.Fprintln(w, "findings:")
		for _, f := range report.Findings {
			label := strings.ToUpper(f.Severity)
			if f.Status != "" {
				label += "/" + strings.ToUpper(f.Status)
			}
			fmt.Fprintf(w, "  [%s] %s x%d\n", label, f.Kind, f.Count)
			fmt.Fprintf(w, "    %s\n", f.Recommendation)
			if len(f.CoveredBy) > 0 {
				fmt.Fprintf(w, "    covered by: %s\n", strings.Join(f.CoveredBy, "; "))
			}
			if f.Gap != "" {
				fmt.Fprintf(w, "    gap: %s\n", f.Gap)
			}
			if len(f.Evidence) > 0 {
				ev := f.Evidence[0]
				fmt.Fprintf(w, "    e.g. %s\n", ev.Command)
				if ev.Plan != nil {
					if js := sessionBatchPlanWire(ev.Plan); js != "" {
						fmt.Fprintf(w, "    batch plan: git-kit batch --plan - <<< '%s'\n", js)
					}
					if len(ev.Plan.Omitted) > 0 {
						fmt.Fprintf(w, "    omitted (not git-kit): %s\n", strings.Join(ev.Plan.Omitted, ", "))
					}
				}
			}
		}
	}

	for _, note := range report.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
}

// sessionBatchPlanWire renders a synthesized plan in the exact git-kit batch
// --plan wire shape ({"steps":[{"args":[...]}]}), dropping the audit-only From
// and Omitted fields so the line is copy-paste runnable.
func sessionBatchPlanWire(plan *sessionaudit.BatchPlan) string {
	if plan == nil || len(plan.Steps) == 0 {
		return ""
	}
	type step struct {
		Args []string `json:"args"`
	}
	wire := struct {
		Steps []step `json:"steps"`
	}{}
	for _, s := range plan.Steps {
		wire.Steps = append(wire.Steps, step{Args: s.Args})
	}
	b, err := json.Marshal(wire)
	if err != nil {
		return ""
	}
	return string(b)
}
