package sessionaudit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestAudit_CloneAndFilterRepoAreCovered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git clone https://github.com/x/y\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git filter-repo --path secrets.txt --invert-paths\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}

	clone := findingByKind(report, "raw-clone")
	if clone == nil || clone.Status != "covered" || !containsString(clone.CoveredBy, "git-kit clone") {
		t.Fatalf("git clone should be covered by git-kit clone, got %+v", clone)
	}
	forget := findingByKind(report, "raw-forget")
	if forget == nil || forget.Status != "covered" || !containsString(forget.CoveredBy, "git-kit forget") {
		t.Fatalf("git filter-repo should be covered by git-kit forget, got %+v", forget)
	}
	// They must no longer pollute the uncovered-raw-git roadmap gap.
	if gap := findingByKind(report, "uncovered-raw-git"); gap != nil {
		if _, ok := gap.Subcommands["clone"]; ok {
			t.Errorf("clone must not appear in the gap: %v", gap.Subcommands)
		}
		if _, ok := gap.Subcommands["filter-repo"]; ok {
			t.Errorf("filter-repo must not appear in the gap: %v", gap.Subcommands)
		}
	}
}

func TestAudit_SuppressesPlumbingAndReadOnlyNonGap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		// Low-level plumbing git-kit never wraps — must not pollute the gap.
		`{"payload":{"arguments":"{\"cmd\":\"git merge-tree base a b\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git read-tree HEAD\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git commit-graph write\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git diff-tree --no-commit-id HEAD\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git checkout-index -a\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git update-index --refresh\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git cherry main\"}"}}`,
		// Dev-session probe of gk's own help — not a git subcommand, never a gap.
		`{"payload":{"arguments":"{\"cmd\":\"git kit --help\"}"}}`,
		// Read-only forms of mutation-capable subcommands — orientation, not a gap.
		`{"payload":{"arguments":"{\"cmd\":\"git remote -v\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git remote show origin\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git remote get-url origin\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git submodule status\"}"}}`,
		// Mutating forms — these MUST stay reportable as genuine missing-verb signals.
		`{"payload":{"arguments":"{\"cmd\":\"git remote add up https://example.com/x\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git submodule update --init\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}

	gap := findingByKind(report, "uncovered-raw-git")
	if gap == nil {
		t.Fatalf("missing uncovered-raw-git finding in %+v", report.Findings)
	}
	// Only the two mutating forms remain in the gap.
	if gap.Count != 2 {
		t.Errorf("gap count = %d, want 2 (subs %v)", gap.Count, gap.Subcommands)
	}
	if gap.Subcommands["remote"] != 1 {
		t.Errorf("remote add should stay a gap: %v", gap.Subcommands)
	}
	if gap.Subcommands["submodule"] != 1 {
		t.Errorf("submodule update should stay a gap: %v", gap.Subcommands)
	}
	for _, absent := range []string{
		"merge-tree", "read-tree", "commit-graph", "diff-tree",
		"checkout-index", "update-index", "cherry", "kit",
	} {
		if _, ok := gap.Subcommands[absent]; ok {
			t.Errorf("plumbing %q should not appear in the gap: %v", absent, gap.Subcommands)
		}
	}
}

func TestAudit_GapEvidencePerSubcommandAndOneShotLabel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		// Six apply hits — under a first-N cap these would claim every slot.
		`{"payload":{"arguments":"{\"cmd\":\"git apply --cached /tmp/a1.patch\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git apply --cached /tmp/a2.patch\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git apply --cached /tmp/a3.patch\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git apply --cached /tmp/a4.patch\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git apply --cached /tmp/a5.patch\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git apply --cached /tmp/a6.patch\"}"}}`,
		// Rarer subcommands must still get a sample each.
		`{"payload":{"arguments":"{\"cmd\":\"git init\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git mv old.go new.go\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}
	gap := findingByKind(report, "uncovered-raw-git")
	if gap == nil {
		t.Fatalf("missing uncovered-raw-git finding in %+v", report.Findings)
	}

	if len(gap.Evidence) != 3 {
		t.Errorf("evidence = %d entries, want 3 (one per subcommand): %+v", len(gap.Evidence), gap.Evidence)
	}
	prefixes := map[string]bool{}
	for _, ev := range gap.Evidence {
		for _, sub := range []string{"apply", "init", "mv"} {
			if strings.HasPrefix(ev.Command, "git "+sub) {
				prefixes[sub] = true
			}
		}
	}
	for _, sub := range []string{"apply", "init", "mv"} {
		if !prefixes[sub] {
			t.Errorf("no evidence sample for %q: %+v", sub, gap.Evidence)
		}
	}

	// init and mv are one-call ops (~0 turn leverage); apply is not labeled —
	// its cost shows up in multi-turn recovery arcs.
	want := []string{"init", "mv"}
	if len(gap.OneShot) != len(want) || gap.OneShot[0] != want[0] || gap.OneShot[1] != want[1] {
		t.Errorf("one_shot = %v, want %v", gap.OneShot, want)
	}
}

// Index-only `git reset` forms are covered by git-kit unstage; resets that
// move the branch (--soft/--hard, HEAD~1) remain uncovered gap signals.
func TestAudit_UnstageCoveredHistoryResetStaysGap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git reset\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git reset -q HEAD .\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git reset HEAD -- a.go b.go\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git reset --mixed HEAD\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git reset --soft HEAD~1\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git reset --mixed HEAD~1\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git reset --hard origin/main\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}

	unstage := findingByKind(report, "raw-unstage")
	if unstage == nil || unstage.Count != 4 {
		t.Fatalf("raw-unstage count = %+v, want 4 (--mixed HEAD is index-only)", unstage)
	}
	if unstage.Status != "covered" || len(unstage.CoveredBy) == 0 {
		t.Errorf("raw-unstage should be covered by git-kit unstage: %+v", unstage)
	}
	gap := findingByKind(report, "uncovered-raw-git")
	if gap == nil || gap.Subcommands["reset"] != 3 {
		t.Fatalf("history resets should stay in the gap (want reset x3, incl --mixed HEAD~1): %+v", gap)
	}
}

// Per-project rollup: Claude sessions attribute to their workspace
// directory, Codex sessions pool under "codex-sessions", chat-only
// sessions (no git) drop out, and ordering is most-raw-git-first.
func TestAudit_ProjectsRollup(t *testing.T) {
	dir := t.TempDir()
	alpha := filepath.Join(dir, ".claude", "projects", "-work-alpha")
	beta := filepath.Join(dir, ".claude", "projects", "-work-beta")
	codex := filepath.Join(dir, ".codex", "sessions", "2026", "07", "02")
	quiet := filepath.Join(dir, ".claude", "projects", "-work-quiet")
	for _, d := range []string{alpha, beta, codex, quiet} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitLine := `{"payload":{"arguments":"{\"cmd\":\"git status\"}"}}`
	gkLine := `{"payload":{"arguments":"{\"cmd\":\"git-kit context\"}"}}`
	writeLines(t, filepath.Join(alpha, "s.jsonl"), gitLine, gitLine, gkLine)
	writeLines(t, filepath.Join(beta, "s.jsonl"), gitLine)
	writeLines(t, filepath.Join(codex, "s.jsonl"), gitLine)
	writeLines(t, filepath.Join(quiet, "s.jsonl"), `{"payload":{"arguments":"{\"cmd\":\"ls -la\"}"}}`)

	report, err := Audit(Options{Paths: []string{dir}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Projects) != 3 {
		t.Fatalf("projects = %d, want 3 (chat-only session must drop out): %+v", len(report.Projects), report.Projects)
	}
	top := report.Projects[0]
	if top.Project != "-work-alpha" || top.RawGit != 2 || top.GitKit != 1 {
		t.Errorf("top project = %+v, want -work-alpha raw=2 gk=1", top)
	}
	if got := report.Projects[1].Project; got != "-work-beta" {
		t.Errorf("projects[1] = %q, want -work-beta (tie broken by name)", got)
	}
	if got := report.Projects[2].Project; got != "codex-sessions" {
		t.Errorf("projects[2] = %q, want codex-sessions", got)
	}
}

func TestAudit_SinceFilterSkipsOldSessions(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.jsonl")
	newPath := filepath.Join(dir, "new.jsonl")
	line := `{"payload":{"arguments":"{\"cmd\":\"git status\"}"}}`
	writeLines(t, oldPath, line)
	writeLines(t, newPath, line)
	cutoff := time.Now().Add(-24 * time.Hour)
	stale := cutoff.Add(-time.Hour)
	if err := os.Chtimes(oldPath, stale, stale); err != nil {
		t.Fatal(err)
	}

	report, err := Audit(Options{Paths: []string{dir}, Home: dir, MaxFiles: 10, Since: cutoff})
	if err != nil {
		t.Fatal(err)
	}

	if report.Totals.Files != 1 {
		t.Fatalf("files = %d, want 1 (old session should be windowed out): %+v", report.Totals.Files, report.Files)
	}
	if report.Files[0].Path != newPath {
		t.Errorf("scanned %s, want %s", report.Files[0].Path, newPath)
	}
	if report.Since == "" {
		t.Error("report.Since should echo the window cutoff")
	}
	foundNote := false
	for _, n := range report.Notes {
		if strings.Contains(n, "since filter") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Errorf("missing since-filter note in %v", report.Notes)
	}
}

func TestAudit_ZeroSinceScansAllSessions(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.jsonl")
	writeLines(t, oldPath, `{"payload":{"arguments":"{\"cmd\":\"git status\"}"}}`)
	stale := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, stale, stale); err != nil {
		t.Fatal(err)
	}

	report, err := Audit(Options{Paths: []string{dir}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Files != 1 {
		t.Fatalf("files = %d, want 1 (no window = all history)", report.Totals.Files)
	}
	if report.Since != "" {
		t.Errorf("report.Since = %q, want empty without a window", report.Since)
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
