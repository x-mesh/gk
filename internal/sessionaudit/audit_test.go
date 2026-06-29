package sessionaudit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractCommands_CodexArgumentsAndClaudeCommand(t *testing.T) {
	codex := []byte(`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"git status --short && git log --oneline -5\"}"}}`)
	claude := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git add a.go; git commit -m test"}}]}}`)

	got := append(ExtractCommands(codex), ExtractCommands(claude)...)
	if len(got) != 2 {
		t.Fatalf("commands = %d, want 2: %#v", len(got), got)
	}
	if got[0] != "git status --short && git log --oneline -5" {
		t.Fatalf("codex command = %q", got[0])
	}
	if got[1] != "git add a.go; git commit -m test" {
		t.Fatalf("claude command = %q", got[1])
	}
}

func TestAudit_ClassifiesAgentGitPatterns(t *testing.T) {
	dir := t.TempDir()
	codex := filepath.Join(dir, "codex.jsonl")
	claude := filepath.Join(dir, "claude.jsonl")
	writeLines(t, codex,
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"git status --short && git log --oneline -5\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"GK_AGENT=1 git-kit context --include=diff,log\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"git diff --name-only --diff-filter=U && git diff --cc\"}"}}`,
	)
	writeLines(t, claude,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git add CHANGELOG.md; git commit -m release && git tag -a v1.0.0 -m v1.0.0 && git push origin main && git push origin v1.0.0"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gk pull --with-base"}}]}}`,
	)

	report, err := Audit(Options{Paths: []string{codex, claude}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Files != 2 {
		t.Fatalf("files = %d, want 2", report.Totals.Files)
	}
	if report.Totals.RawGit == 0 || report.Totals.GitKit == 0 || report.Totals.GKShort == 0 {
		t.Fatalf("unexpected totals: %+v", report.Totals)
	}
	for _, kind := range []string{
		"raw-context-probes",
		"raw-conflict-probes",
		"raw-release-sequence",
		"raw-commit-sequence",
		"gk-short-alias",
		"shell-chain",
	} {
		if !hasFinding(report, kind) {
			t.Fatalf("missing finding %q in %+v", kind, report.Findings)
		}
	}
}

func TestAudit_MarksCoveredFindings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git merge-base main develop\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git diff -- src/app.go\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git diff --check docs/commands.md\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir})
	if err != nil {
		t.Fatal(err)
	}

	contextProbe := findingByKind(report, "raw-context-probes")
	if contextProbe == nil || contextProbe.Status != "covered" {
		t.Fatalf("context finding = %+v", contextProbe)
	}
	fullDiff := findingByKind(report, "raw-full-diff")
	if fullDiff == nil || fullDiff.Status != "covered" || !containsString(fullDiff.CoveredBy, "git-kit diff --raw-patch --json") {
		t.Fatalf("full diff finding = %+v", fullDiff)
	}
	diffCheck := findingByKind(report, "raw-diff-check")
	if diffCheck == nil || diffCheck.Status != "covered" || !containsString(diffCheck.CoveredBy, "git-kit diff --check --json") {
		t.Fatalf("diff check finding = %+v", diffCheck)
	}
	if hasFinding(report, "raw-integration") {
		t.Fatalf("merge-base must not be classified as integration: %+v", report.Findings)
	}
}

func TestAudit_DoesNotCountGitWordsInsideQuotedSearchPatterns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"rg -n \\\"git status|git pull|gk promote\\\" README.md\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git status --short && rg -n \\\"git commit\\\" docs\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.RawGit != 1 {
		t.Fatalf("raw git count = %d, want 1", report.Totals.RawGit)
	}
	if report.Totals.GKShort != 0 {
		t.Fatalf("gk short count = %d, want 0", report.Totals.GKShort)
	}
}

func TestAudit_UsesDefaultSessionRoots(t *testing.T) {
	home := t.TempDir()
	sessionDir := filepath.Join(home, ".codex", "sessions", "2026")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "one.jsonl")
	writeLines(t, path, `{"payload":{"arguments":"{\"cmd\":\"git status --short\"}"}}`)

	report, err := Audit(Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Files != 1 || report.Totals.RawGit == 0 {
		t.Fatalf("default roots not scanned: %+v", report.Totals)
	}
}

func TestAudit_SynthesizesBatchPlanForShellChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"echo head && git status --short && git add x && git commit -m y\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir})
	if err != nil {
		t.Fatal(err)
	}

	sc := findingByKind(report, "shell-chain")
	if sc == nil {
		t.Fatalf("missing shell-chain finding: %+v", report.Findings)
	}
	if sc.Status != "covered" || sc.Gap != "" {
		t.Fatalf("shell-chain should be covered with no gap: %+v", sc)
	}
	if len(sc.Evidence) == 0 || sc.Evidence[0].Plan == nil {
		t.Fatalf("shell-chain evidence missing plan: %+v", sc.Evidence)
	}
	plan := sc.Evidence[0].Plan
	want := [][]string{{"context", "--include=diff,log"}, {"commit"}}
	if len(plan.Steps) != len(want) {
		t.Fatalf("plan steps = %+v, want %d", plan.Steps, len(want))
	}
	for i, w := range want {
		if strings.Join(plan.Steps[i].Args, " ") != strings.Join(w, " ") {
			t.Fatalf("step %d = %v, want %v", i, plan.Steps[i].Args, w)
		}
	}
	if !containsString(plan.Omitted, "echo") {
		t.Fatalf("plan omitted = %v, want echo", plan.Omitted)
	}
}

func TestAudit_ComputesAdoption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git status --short\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git log --oneline -5\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git-kit context\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir})
	if err != nil {
		t.Fatal(err)
	}

	a := report.Adoption
	if a.GitInvocations != 3 || a.GitKit != 1 {
		t.Fatalf("adoption totals = %+v", a)
	}
	if a.CoveredRawHits != 2 {
		t.Fatalf("covered raw hits = %d, want 2", a.CoveredRawHits)
	}
	if a.Rate < 0.33 || a.Rate > 0.34 {
		t.Fatalf("rate = %f, want ~0.333", a.Rate)
	}
}

func TestAudit_SurfacesUncoveredRawGitGap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git stash\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git stash pop\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git stash clear\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git reset --hard HEAD~1\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git apply fix.patch\"}"}}`,
		// Suppressed: read-only plumbing and a diff flag variant must NOT be
		// reported as gaps, or the roadmap signal drowns in noise.
		`{"payload":{"arguments":"{\"cmd\":\"git rev-parse HEAD\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git diff --numstat\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}

	f := findingByKind(report, "uncovered-raw-git")
	if f == nil {
		t.Fatalf("missing uncovered-raw-git finding in %+v", report.Findings)
	}
	if f.Status != "gap" {
		t.Errorf("status = %q, want gap", f.Status)
	}
	// stash clear x1 + reset x1 + apply x1 = 3; `git stash` and `git stash pop`
	// map to git-kit stash (covered), `git stash clear` does not, and
	// rev-parse/diff are suppressed plumbing.
	if f.Count != 3 {
		t.Errorf("gap count = %d, want 3 (subs %v)", f.Count, f.Subcommands)
	}
	// git stash clear has no git-kit verb, so it alone stays in the gap.
	if f.Subcommands["stash"] != 1 {
		t.Errorf("stash gap count = %d, want 1 (subs %v)", f.Subcommands["stash"], f.Subcommands)
	}
	for _, absent := range []string{"rev-parse", "diff"} {
		if _, ok := f.Subcommands[absent]; ok {
			t.Errorf("%q should not appear in the gap, got %v", absent, f.Subcommands)
		}
	}
	if report.Adoption.UncoveredRawHits != 3 {
		t.Errorf("UncoveredRawHits = %d, want 3", report.Adoption.UncoveredRawHits)
	}
	// git stash + git stash pop are covered raw git → they count toward CoveredRawHits.
	if report.Adoption.CoveredRawHits != 2 {
		t.Errorf("CoveredRawHits = %d, want 2", report.Adoption.CoveredRawHits)
	}
	// the supported stash subcommands surface as covered raw-stash, not a gap.
	if s := findingByKind(report, "raw-stash"); s == nil || s.Count != 2 {
		t.Fatalf("raw-stash = %+v, want count 2", s)
	}
}

func TestAudit_PromotesBranchSwitchAndWorktree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git checkout -b feat/x\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git switch main\"}"}}`,
		// `checkout -- <path>` is a file restore, not a branch switch — it must
		// stay a gap, not get folded into raw-branch-switch.
		`{"payload":{"arguments":"{\"cmd\":\"git checkout -- app.go\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git worktree add ../wt feat/x\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git worktree list\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}

	if f := findingByKind(report, "raw-branch-switch"); f == nil || f.Count != 2 {
		t.Fatalf("raw-branch-switch = %+v, want count 2 (checkout -b + switch)", f)
	}
	if f := findingByKind(report, "raw-worktree"); f == nil || f.Count != 2 {
		t.Fatalf("raw-worktree = %+v, want count 2", f)
	}
	// The file-restore checkout is the only thing left uncovered.
	gap := findingByKind(report, "uncovered-raw-git")
	if gap == nil || gap.Subcommands["checkout"] != 1 {
		t.Fatalf("expected checkout restore as the sole gap, got %+v", gap)
	}
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasFinding(report Report, kind string) bool {
	return findingByKind(report, kind) != nil
}

func findingByKind(report Report, kind string) *Finding {
	for _, f := range report.Findings {
		if f.Kind == kind {
			return &f
		}
	}
	return nil
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
