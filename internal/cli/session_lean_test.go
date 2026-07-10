package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/sessionaudit"
)

// leanFixtureReport builds a report exercising every lean cap: files present,
// a run with more commands than the cap, a finding with more evidence than the
// cap, and strings longer than the truncation budget.
func leanFixtureReport() sessionaudit.Report {
	long := strings.Repeat("g", sessionLeanTruncateLen+30)
	// 119 ASCII bytes then a 3-byte rune spanning the 120-byte cut point.
	multibyte := strings.Repeat("a", sessionLeanTruncateLen-1) + strings.Repeat("한", 20)
	projects := make([]sessionaudit.ProjectAdoption, 7)
	for i := range projects {
		projects[i] = sessionaudit.ProjectAdoption{Project: fmt.Sprintf("p%d", i), Files: 1, RawGit: 7 - i}
	}
	return sessionaudit.Report{
		Schema:   1,
		Files:    []sessionaudit.FileReport{{Path: "a.jsonl", Source: "claude", Commands: 5, RawGit: 5}},
		Totals:   sessionaudit.Totals{Files: 1, Commands: 5, RawGit: 5},
		Adoption: sessionaudit.Adoption{GitInvocations: 5, GitKit: 0},
		Projects: projects,
		Turns: &sessionaudit.TurnMetrics{
			Source: "claude,codex", GitTurns: 5, EstimatedTurnsSaved: 4, Rate: 0.8,
			ByGroup: map[string]int{"context": 4},
			Runs: []sessionaudit.CollapsibleRun{{
				Group: "context", GkCommand: "git-kit context",
				Turns:      []int{1, 2, 3, 4, 5},
				Commands:   []string{long, multibyte, "git status", "git log", "git diff"},
				TurnsSaved: 4,
			}},
		},
		Findings: []sessionaudit.Finding{{
			Kind: "raw-context-probes", Severity: "high", Status: "covered", Count: 5,
			Recommendation: "use git-kit context",
			CoveredBy:      []string{"git-kit context"},
			Evidence: []sessionaudit.Evidence{
				{Command: long}, {Command: "git status"}, {Command: "git log"},
			},
		}},
		Notes: []string{"n1"},
	}
}

func TestLeanSessionReport_CapsAndOmitsFiles(t *testing.T) {
	report := leanFixtureReport()
	long := report.Turns.Runs[0].Commands[0]

	lean := leanSessionReport(report, false)
	b, err := json.Marshal(lean)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["files"]; ok {
		t.Errorf("lean payload must omit files[] entirely, got key in:\n%s", b)
	}

	cmds := lean.Report.Turns.Runs[0].Commands
	if len(cmds) != sessionLeanCommandCap+1 {
		t.Fatalf("run commands = %d entries, want %d + marker", len(cmds), sessionLeanCommandCap)
	}
	if want := long[:sessionLeanTruncateLen] + "…"; cmds[0] != want {
		t.Errorf("command[0] not truncated: %q", cmds[0])
	}
	if !utf8.ValidString(cmds[1]) {
		t.Errorf("multibyte truncation produced invalid UTF-8: %q", cmds[1])
	}
	if want := "(+2 more)"; cmds[sessionLeanCommandCap] != want {
		t.Errorf("marker = %q, want %q", cmds[sessionLeanCommandCap], want)
	}

	ev := lean.Report.Findings[0].Evidence
	if len(ev) != sessionLeanEvidenceCap {
		t.Fatalf("evidence = %d entries, want %d", len(ev), sessionLeanEvidenceCap)
	}
	if !strings.HasSuffix(ev[0].Command, "…") || len(ev[0].Command) > sessionLeanTruncateLen+len("…") {
		t.Errorf("evidence[0] not truncated: %q", ev[0].Command)
	}

	// The original report must be untouched — --record/--trend and the human
	// renderer read it after the wire copy is shaped.
	if len(report.Turns.Runs[0].Commands) != 5 || report.Turns.Runs[0].Commands[0] != long {
		t.Error("lean shaping mutated the original run commands")
	}
	if len(report.Findings[0].Evidence) != 3 || report.Findings[0].Evidence[0].Command != long {
		t.Error("lean shaping mutated the original evidence")
	}
	if len(report.Files) != 1 {
		t.Error("lean shaping mutated the original files")
	}
}

func TestLeanSessionReport_WithFiles(t *testing.T) {
	lean := leanSessionReport(leanFixtureReport(), true)
	b, err := json.Marshal(lean)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Files []sessionaudit.FileReport `json:"files"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || m.Files[0].Path != "a.jsonl" {
		t.Errorf("--files must restore the per-file breakdown, got %+v", m.Files)
	}
}

// TestSummarizeSessionReport pins the --summary wire shape: exactly the
// decision-grade keys, top-5 projects, turns without runs, findings without
// evidence/covered_by.
func TestSummarizeSessionReport(t *testing.T) {
	s := summarizeSessionReport(leanFixtureReport())
	if len(s.Projects) != sessionSummaryTopProjects {
		t.Errorf("projects = %d, want top %d", len(s.Projects), sessionSummaryTopProjects)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"schema": true, "totals": true, "adoption": true, "projects": true,
		"turns": true, "findings": true, "notes": true,
	}
	for k := range top {
		if !allowed[k] {
			t.Errorf("summary emitted unexpected key %q", k)
		}
	}
	for _, k := range []string{"schema", "totals", "adoption", "projects", "turns", "findings", "notes"} {
		if _, ok := top[k]; !ok {
			t.Errorf("summary missing key %q", k)
		}
	}

	var turns map[string]json.RawMessage
	if err := json.Unmarshal(top["turns"], &turns); err != nil {
		t.Fatal(err)
	}
	if _, ok := turns["runs"]; ok {
		t.Error("summary turns must not carry runs[]")
	}

	var findings []map[string]json.RawMessage
	if err := json.Unmarshal(top["findings"], &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	for _, banned := range []string{"evidence", "covered_by"} {
		if _, ok := findings[0][banned]; ok {
			t.Errorf("summary finding must not carry %q", banned)
		}
	}
	for _, kept := range []string{"kind", "severity", "status", "count", "recommendation"} {
		if _, ok := findings[0][kept]; !ok {
			t.Errorf("summary finding missing %q", kept)
		}
	}
}

func TestEmitAgentResultCompactOver(t *testing.T) {
	withAgentMode(t, false)
	flagJSON = true
	payload := map[string]string{"k": strings.Repeat("v", 64)}

	// Under the threshold: byte-identical to the pretty default.
	var pretty, small bytes.Buffer
	if err := emitAgentResult(&pretty, payload); err != nil {
		t.Fatal(err)
	}
	if err := emitAgentResultCompactOver(&small, payload, agentCompactThresholdBytes); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(small.Bytes(), pretty.Bytes()) {
		t.Errorf("small payload must stay pretty:\ngot:  %q\nwant: %q", small.String(), pretty.String())
	}

	// Over the threshold: compact single line.
	var compact bytes.Buffer
	if err := emitAgentResultCompactOver(&compact, payload, 10); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimRight(compact.String(), "\n"); strings.Contains(got, "\n") || strings.Contains(got, agentJSONIndent+`"`) {
		t.Errorf("large payload must be compact, got %q", compact.String())
	}
	var m map[string]string
	if err := json.Unmarshal(compact.Bytes(), &m); err != nil {
		t.Fatalf("compact output not valid JSON: %v", err)
	}

	// The real threshold kicks in for a payload over 16 KiB.
	big := map[string]string{"k": strings.Repeat("v", agentCompactThresholdBytes)}
	var out bytes.Buffer
	if err := emitAgentResultCompactOver(&out, big, agentCompactThresholdBytes); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.TrimRight(out.String(), "\n"), "\n") {
		t.Error("payload over agentCompactThresholdBytes must emit compact")
	}
}

// newSessionAuditTestCmd mirrors the flag set init registers on `session audit`.
func newSessionAuditTestCmd(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Int("max-files", 200, "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().String("metric", "occurrences", "")
	cmd.Flags().Bool("viz", false, "")
	cmd.Flags().Bool("record", false, "")
	cmd.Flags().Bool("trend", false, "")
	cmd.Flags().Bool("files", false, "")
	cmd.Flags().Bool("full", false, "")
	cmd.Flags().Bool("summary", false, "")
	cmd.SetOut(out)
	return cmd
}

func writeSessionFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	line := `{"payload":{"arguments":"{\"cmd\":\"git status --short && git log --oneline -5\"}"}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func withJSONOut(t *testing.T) {
	t.Helper()
	prev := flagJSON
	t.Cleanup(func() { flagJSON = prev })
	flagJSON = true
}

func TestRunSessionAudit_JSONLeanByDefault(t *testing.T) {
	path := writeSessionFixture(t)
	withJSONOut(t)

	var out bytes.Buffer
	if err := runSessionAudit(newSessionAuditTestCmd(&out), []string{path}); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if _, ok := m["files"]; ok {
		t.Errorf("default JSON must omit files[]:\n%s", out.String())
	}
	if _, ok := m["findings"]; !ok {
		t.Errorf("lean JSON must keep findings:\n%s", out.String())
	}

	// --files restores the per-file breakdown.
	out.Reset()
	cmd := newSessionAuditTestCmd(&out)
	if err := cmd.Flags().Set("files", "true"); err != nil {
		t.Fatal(err)
	}
	if err := runSessionAudit(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	var withFiles struct {
		Files []sessionaudit.FileReport `json:"files"`
	}
	if err := json.Unmarshal(out.Bytes(), &withFiles); err != nil {
		t.Fatal(err)
	}
	if len(withFiles.Files) != 1 {
		t.Errorf("--files must restore files[], got %d entries:\n%s", len(withFiles.Files), out.String())
	}
}

// TestRunSessionAudit_JSONFull: --full must be bit-equivalent to emitting the
// untouched sessionaudit.Report — today's exact payload.
func TestRunSessionAudit_JSONFull(t *testing.T) {
	path := writeSessionFixture(t)
	withJSONOut(t)

	report, err := sessionaudit.Audit(sessionaudit.Options{Paths: []string{path}, MaxFiles: 200, Metric: "occurrences"})
	if err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := emitAgentResultCompactOver(&want, report, agentCompactThresholdBytes); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newSessionAuditTestCmd(&out)
	if err := cmd.Flags().Set("full", "true"); err != nil {
		t.Fatal(err)
	}
	if err := runSessionAudit(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want.Bytes()) {
		t.Errorf("--full output diverged from the raw report:\ngot:  %s\nwant: %s", out.String(), want.String())
	}
}

func TestRunSessionAudit_SummaryFullExclusive(t *testing.T) {
	withJSONOut(t)
	var out bytes.Buffer
	cmd := newSessionAuditTestCmd(&out)
	for _, f := range []string{"full", "summary"} {
		if err := cmd.Flags().Set(f, "true"); err != nil {
			t.Fatal(err)
		}
	}
	err := runSessionAudit(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("--full --summary must error, got %v", err)
	}
}

func TestRunSessionAudit_SummaryJSON(t *testing.T) {
	path := writeSessionFixture(t)
	withJSONOut(t)

	var out bytes.Buffer
	cmd := newSessionAuditTestCmd(&out)
	if err := cmd.Flags().Set("summary", "true"); err != nil {
		t.Fatal(err)
	}
	if err := runSessionAudit(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	for _, banned := range []string{"files", "trend", "since"} {
		if _, ok := m[banned]; ok {
			t.Errorf("summary must not carry %q:\n%s", banned, out.String())
		}
	}
	var findings []map[string]json.RawMessage
	if err := json.Unmarshal(m["findings"], &findings); err != nil {
		t.Fatalf("summary findings: %v\n%s", err, out.String())
	}
	if len(findings) == 0 {
		t.Fatalf("fixture must yield findings:\n%s", out.String())
	}
	if _, ok := findings[0]["evidence"]; ok {
		t.Errorf("summary findings must not carry evidence:\n%s", out.String())
	}
}

// --trend --summary must carry the recorded history in the summary payload
// instead of silently dropping the flag (the lean/full payloads embed the
// Report, which already carries Trend; the summary struct needs its own field).
func TestRunSessionAudit_SummaryTrendCarried(t *testing.T) {
	path := writeSessionFixture(t)
	withJSONOut(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := sessionaudit.HistoryEntry{Timestamp: "2026-07-01T00:00:00Z", GitTurns: 10, EstimatedTurnsSaved: 4}
	if err := sessionaudit.AppendHistory(sessionaudit.HistoryPath(home), e); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newSessionAuditTestCmd(&out)
	for _, f := range []string{"summary", "trend"} {
		if err := cmd.Flags().Set(f, "true"); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSessionAudit(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Trend []sessionaudit.HistoryEntry `json:"trend"`
	}
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if len(m.Trend) != 1 || m.Trend[0].GitTurns != 10 {
		t.Errorf("summary must carry the trend history, got %+v\n%s", m.Trend, out.String())
	}
}

// TestRunSessionAudit_RecordUsesFullReport: --record must write history from
// the ORIGINAL report even while the wire payload is lean.
func TestRunSessionAudit_RecordUsesFullReport(t *testing.T) {
	path := writeSessionFixture(t)
	withJSONOut(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	var out bytes.Buffer
	cmd := newSessionAuditTestCmd(&out)
	if err := cmd.Flags().Set("record", "true"); err != nil {
		t.Fatal(err)
	}
	if err := runSessionAudit(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if _, ok := m["files"]; ok {
		t.Errorf("recorded run's wire payload must still be lean:\n%s", out.String())
	}

	entries, err := sessionaudit.ReadHistory(sessionaudit.HistoryPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Files != 1 {
		t.Errorf("history must record the full report's totals, got %+v", entries)
	}
}
