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
		// Covered by git-kit apply — must not land in the gap.
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
	// `git stash clear` alone stays in the gap: `git stash`/`git stash pop` map
	// to git-kit stash, `git apply` to git-kit apply, `git reset --hard HEAD~1`
	// to git-kit reset --to, and rev-parse/diff are suppressed plumbing.
	if f.Count != 1 {
		t.Errorf("gap count = %d, want 1 (subs %v)", f.Count, f.Subcommands)
	}
	if f.Subcommands["stash"] != 1 {
		t.Errorf("stash gap count = %d, want 1 (subs %v)", f.Subcommands["stash"], f.Subcommands)
	}
	for _, absent := range []string{"rev-parse", "diff", "apply", "reset"} {
		if _, ok := f.Subcommands[absent]; ok {
			t.Errorf("%q should not appear in the gap, got %v", absent, f.Subcommands)
		}
	}
	if report.Adoption.UncoveredRawHits != 1 {
		t.Errorf("UncoveredRawHits = %d, want 1", report.Adoption.UncoveredRawHits)
	}
	// stash x2 + apply x1 + reset --hard x1 are covered raw git.
	if report.Adoption.CoveredRawHits != 4 {
		t.Errorf("CoveredRawHits = %d, want 4", report.Adoption.CoveredRawHits)
	}
	// the supported stash subcommands surface as covered raw-stash, not a gap.
	if s := findingByKind(report, "raw-stash"); s == nil || s.Count != 2 {
		t.Fatalf("raw-stash = %+v, want count 2", s)
	}
	if a := findingByKind(report, "raw-apply"); a == nil || a.Status != "covered" || !containsString(a.CoveredBy, "git-kit apply") {
		t.Fatalf("git apply should be covered by git-kit apply, got %+v", a)
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
		// Six clean hits — under a first-N cap these would claim every slot.
		`{"payload":{"arguments":"{\"cmd\":\"git clean -fd build1\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git clean -fd build2\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git clean -fd build3\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git clean -fd build4\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git clean -fd build5\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git clean -fd build6\"}"}}`,
		// Rarer subcommands must still get a sample each.
		`{"payload":{"arguments":"{\"cmd\":\"git revert HEAD\"}"}}`,
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
		for _, sub := range []string{"clean", "revert", "mv"} {
			if strings.HasPrefix(ev.Command, "git "+sub) {
				prefixes[sub] = true
			}
		}
	}
	for _, sub := range []string{"clean", "revert", "mv"} {
		if !prefixes[sub] {
			t.Errorf("no evidence sample for %q: %+v", sub, gap.Evidence)
		}
	}

	// clean and mv are one-call ops (~0 turn leverage); revert is not labeled —
	// its cost shows up in multi-turn recovery arcs.
	want := []string{"clean", "mv"}
	if len(gap.OneShot) != len(want) || gap.OneShot[0] != want[0] || gap.OneShot[1] != want[1] {
		t.Errorf("one_shot = %v, want %v", gap.OneShot, want)
	}
}

// The reset family splits three ways, by what gk can actually offer:
//   - index-only forms → git-kit unstage (covered)
//   - `--hard <ref>` → git-kit reset --to <ref> (covered: same target, backed up)
//   - everything else that moves the branch (--soft, --mixed <commit>) → gap,
//     because gk's only uncommit-but-keep-work path is an interactive picker.
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
	// --hard origin/main is exactly what `gk reset --to origin/main` does.
	hard := findingByKind(report, "raw-reset-hard")
	if hard == nil || hard.Count != 1 {
		t.Fatalf("raw-reset-hard = %+v, want count 1 (--hard origin/main)", hard)
	}
	// --soft HEAD~1 and --mixed HEAD~1 move the branch with no gk equivalent.
	gap := findingByKind(report, "uncovered-raw-git")
	if gap == nil || gap.Subcommands["reset"] != 2 {
		t.Fatalf("soft/mixed history resets must stay in the gap (want reset x2): %+v", gap)
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

// Heredoc BODY lines are data, not shell segments: a Korean prose line
// starting with "git" inside a commit-message heredoc must not classify as a
// raw git command (it used to file as git subcommand "도구는").
func TestSplitShellSegments_HeredocBodyIsData(t *testing.T) {
	cmd := "git commit -F - <<'EOF'\nfeat: 세션 감사 개선\n\ngit 도구는 세션을 정리한다\nEOF"
	parts, chained := splitShellSegments(cmd)
	if len(parts) != 1 || chained {
		t.Fatalf("parts = %v (chained=%v), want the single commit segment", parts, chained)
	}
	if !strings.HasPrefix(parts[0], "git commit") {
		t.Errorf("segment = %q, want the commit command", parts[0])
	}

	// A command after the terminator is still a separate, chained segment.
	parts, chained = splitShellSegments("git commit -F - <<EOF\ngit 도구는 본문\nEOF\ngit status")
	if len(parts) != 2 || !chained {
		t.Fatalf("parts = %v (chained=%v), want commit + status", parts, chained)
	}
	if parts[1] != "git status" {
		t.Errorf("parts[1] = %q, want git status", parts[1])
	}

	class := classifyCommand(cmd)
	if class.RawGit != 1 {
		t.Errorf("heredoc command RawGit = %d, want 1", class.RawGit)
	}
}

// Shell arithmetic's `<<` (bit shift) is not a heredoc: `$((1<<16))` must not
// swallow the rest of a multi-line tool call — the trailing git commands
// stay visible to classification (audit counts, collapse, digest, hook).
func TestSplitShellSegments_ArithmeticShiftIsNotHeredoc(t *testing.T) {
	parts, chained := splitShellSegments("dd bs=$((1<<16)) if=a of=b\ngit add -A\ngit commit -m x")
	if len(parts) != 3 || !chained {
		t.Fatalf("parts = %v (chained=%v), want dd + add + commit", parts, chained)
	}
	if parts[1] != "git add -A" || parts[2] != "git commit -m x" {
		t.Errorf("trailing commands swallowed: %v", parts)
	}
	if class := classifyCommand("x=$((n<<2))\ngit push --force"); class.RawGit != 1 {
		t.Errorf("RawGit = %d, want 1 (push after arithmetic shift)", class.RawGit)
	}
}

// A CRLF-encoded heredoc still terminates: the terminator line arrives as
// "EOF\r" and must match the queued "EOF", or every following command is
// consumed as heredoc body and escapes classification.
func TestSplitShellSegments_HeredocCRLFTerminates(t *testing.T) {
	parts, chained := splitShellSegments("printf x <<EOF\r\nbody line\r\nEOF\r\ngit push --force")
	if len(parts) != 2 || !chained {
		t.Fatalf("parts = %v (chained=%v), want printf + push", parts, chained)
	}
	if parts[1] != "git push --force" {
		t.Errorf("parts[1] = %q, want the trailing push", parts[1])
	}
	if class := classifyCommand("printf x <<EOF\r\nbody\r\nEOF\r\ngit push --force"); class.RawGit != 1 {
		t.Errorf("RawGit = %d, want 1", class.RawGit)
	}
}

// Only the <<- form permits a tab-indented terminator; for plain << a
// tab-indented line is body, exactly like the shell.
func TestSplitShellSegments_HeredocTabTerminatorOnlyForDashForm(t *testing.T) {
	// <<-: the indented terminator ends the body, the tail is a segment.
	parts, _ := splitShellSegments("cat <<-EOF\n\tbody\n\tEOF\ngit status")
	if len(parts) != 2 || parts[1] != "git status" {
		t.Fatalf("<<- parts = %v, want cat + git status", parts)
	}
	// plain <<: the tab-indented "\tEOF" is body; the unindented one ends it.
	parts, _ = splitShellSegments("cat <<EOF\n\tEOF\nEOF\ngit status")
	if len(parts) != 2 || parts[1] != "git status" {
		t.Fatalf("<< parts = %v, want cat + git status (only the unindented terminator counts)", parts)
	}
}

// Tokens that are not shaped like a git subcommand (^[a-z0-9][a-z0-9-]*$)
// must not classify — prose, expansions, uppercase.
func TestGitSubcommand_RejectsNonSubcommandTokens(t *testing.T) {
	cases := []struct {
		segment string
		wantOK  bool
		wantSub string
	}{
		{"git status", true, "status"},
		{"git cherry-pick abc", true, "cherry-pick"},
		{"git 도구는 세션을 정리한다", false, ""},
		{"git $(cat cmd.txt)", false, ""},
		{"git STATUS", false, ""},
	}
	for _, tc := range cases {
		sub, _, ok := gitSubcommand(tc.segment)
		if ok != tc.wantOK || sub != tc.wantSub {
			t.Errorf("gitSubcommand(%q) = (%q,%v), want (%q,%v)", tc.segment, sub, ok, tc.wantSub, tc.wantOK)
		}
	}
}

// The conflict-probe classifier must win over the context-probe shape:
// `git diff --name-only --diff-filter=U` is a conflict probe, in both the
// finding mapping and the synthesized batch step.
func TestGitSegmentFinding_ConflictProbeBeforeContext(t *testing.T) {
	args := []string{"--name-only", "--diff-filter=U"}
	if kind := gitSegmentFinding("diff", args); kind != "raw-conflict-probes" {
		t.Errorf("kind = %q, want raw-conflict-probes", kind)
	}
	step, ok := batchStepForGit("diff", args)
	if !ok || strings.Join(step, " ") != "context --include=conflict" {
		t.Errorf("batch step = %v (%v), want context --include=conflict", step, ok)
	}
}

// SHA archaeology is not answerable by gk context: probes with an explicit
// hex commit operand are excluded; HEAD/branch-name operands stay context.
func TestIsRawContextProbe_ShaOperandGuard(t *testing.T) {
	cases := []struct {
		name   string
		subcmd string
		args   []string
		want   bool
	}{
		{"bare status", "status", nil, true},
		{"log HEAD", "log", []string{"--oneline", "-5", "HEAD"}, true},
		{"log branch name", "log", []string{"--oneline", "develop"}, true},
		{"log sha", "log", []string{"--oneline", "8b7a4f21c"}, false},
		{"show sha stat", "show", []string{"--stat", "4f21c8b7a"}, false},
		{"show sha colon path", "show", []string{"--name-only", "4f21c8b7a:main.go"}, false},
		{"merge-base branches", "merge-base", []string{"main", "develop"}, true},
		{"merge-base shas", "merge-base", []string{"8b7a4f21c", "1c8b7a4f2"}, false},
		{"branch contains sha", "branch", []string{"-r", "--contains", "8b7a4f21c"}, false},
		{"log sha range", "log", []string{"--oneline", "8b7a4f21c..HEAD"}, false},
		{"log sha suffix", "log", []string{"-1", "8b7a4f21c~2"}, false},
		// Path-scoped log is no longer a context probe at all — gk log takes no
		// pathspec, so it routes to raw-history-search regardless of what the
		// path is named. The sha-vs-path distinction it used to exercise is
		// asserted directly below, on hasHexCommitOperand.
		{"sha after pathspec dashdash", "log", []string{"--oneline", "--", "8b7a4f21c"}, false},
		{"hex word without digit", "log", []string{"--oneline", "deadbeef"}, true},
		{"short hex ref", "log", []string{"-1", "abc12"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRawContextProbe(tc.subcmd, tc.args); got != tc.want {
				t.Errorf("isRawContextProbe(%s %v) = %v, want %v", tc.subcmd, tc.args, got, tc.want)
			}
		})
	}
	// A sha-looking token AFTER `--` is a path, not a commit operand.
	if hasHexCommitOperand([]string{"--oneline", "--", "8b7a4f21c"}) {
		t.Error("a token after -- is a pathspec, not a commit sha")
	}
}

// The sha-guarded probes are read-only inspection, never gap noise.
func TestAudit_ShaArchaeologyNotContextNotGap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git merge-base 8b7a4f21c 1c8b7a4f2\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git log --oneline 8b7a4f21c..HEAD\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git branch -r --contains 8b7a4f21c\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}
	if hasFinding(report, "raw-context-probes") {
		t.Errorf("sha archaeology must not count as context probes: %+v", report.Findings)
	}
	if hasFinding(report, "uncovered-raw-git") {
		t.Errorf("sha archaeology must not surface as a gap: %+v", report.Findings)
	}
}

// `git restore --staged <paths>` is the unstage spelling git-kit unstage
// covers; adding --worktree (touches contents) or omitting --staged stays in
// the uncovered gap.
func TestAudit_RestoreStagedIsUnstageCovered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git restore --staged a.go b.go\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git restore --staged --worktree a.go\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git restore a.go\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}
	unstage := findingByKind(report, "raw-unstage")
	if unstage == nil || unstage.Count != 1 || unstage.Status != "covered" {
		t.Fatalf("raw-unstage = %+v, want the --staged form covered once", unstage)
	}
	gap := findingByKind(report, "uncovered-raw-git")
	if gap == nil || gap.Subcommands["restore"] != 2 {
		t.Fatalf("worktree/plain restore should stay a gap x2, got %+v", gap)
	}
}

// init/help/gc/archive/commit-tree are noise, not roadmap signals: init is
// dominated by temp test fixtures (and gk init runs git init when needed),
// help/archive are read-only, gc/commit-tree are maintenance/plumbing.
func TestAudit_NonGapMaintenanceSubcommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path,
		`{"payload":{"arguments":"{\"cmd\":\"git init\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git help commit\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git gc --prune=now\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git archive -o out.tar HEAD\"}"}}`,
		`{"payload":{"arguments":"{\"cmd\":\"git commit-tree HEAD^{tree} -m x\"}"}}`,
	)

	report, err := Audit(Options{Paths: []string{path}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}
	if gap := findingByKind(report, "uncovered-raw-git"); gap != nil {
		t.Errorf("maintenance subcommands must not surface as gaps: %+v", gap.Subcommands)
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

// --- reset/fsck mapping -------------------------------------------------------
//
// gk gained `reset --to <ref>` (and `restore --lost`) after the classifier was
// written, so raw forms with an exact gk equivalent were still being reported as
// "no gk verb exists". Mapping is by TARGET, not by verb name: the destructive
// forms that share gk's destination map; the ones that do not stay a gap, because
// a wrong recommendation here throws away work.
func TestGitSegmentFinding_ResetAndFsck(t *testing.T) {
	cases := []struct {
		name   string
		subcmd string
		args   []string
		want   string
	}{
		// gk reset --to takes the same target (and backs it up first).
		{"hard to remote ref", "reset", []string{"--hard", "origin/main"}, "raw-reset-hard"},
		{"hard to commit", "reset", []string{"--hard", "HEAD~1"}, "raw-reset-hard"},
		{"hard to sha", "reset", []string{"--hard", "8b7a4f21c"}, "raw-reset-hard"},

		// BARE --hard discards the working tree AT HEAD; a bare `gk reset` resets
		// to the upstream remote. Same spelling, different destination — mapping
		// it would move a branch the user never asked to move.
		{"bare hard", "reset", []string{"--hard"}, ""},
		{"bare hard quiet", "reset", []string{"-q", "--hard"}, ""},

		// --soft keeps the work staged. gk's only equivalent is the interactive
		// `gk undo` picker, which an agent cannot drive — leave it a gap.
		{"soft uncommit", "reset", []string{"--soft", "HEAD~1"}, ""},

		// Index-only spellings still belong to unstage.
		{"unstage paths", "reset", []string{"HEAD", "file.go"}, "raw-unstage"},
		{"bare reset", "reset", nil, "raw-unstage"},

		// gk restore --lost IS the fsck dangling-work hunt.
		{"fsck unreachable", "fsck", []string{"--unreachable", "--no-reflog"}, "raw-lost-found"},
		{"fsck lost-found", "fsck", []string{"--lost-found"}, "raw-lost-found"},
		{"fsck dangling", "fsck", []string{"--dangling"}, "raw-lost-found"},
		// A bare fsck is an integrity check, not a recovery hunt.
		{"bare fsck", "fsck", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitSegmentFinding(tc.subcmd, tc.args); got != tc.want {
				t.Errorf("gitSegmentFinding(%q, %v) = %q, want %q", tc.subcmd, tc.args, got, tc.want)
			}
		})
	}
}

// Per-path discard has NO gk verb — gk wipe is whole-tree and also removes
// untracked files, so it is not this. The gap is the finding; do not "fix" it by
// mapping it to something destructive.
func TestGitSegmentFinding_PerPathDiscardStaysAGap(t *testing.T) {
	for _, tc := range []struct {
		subcmd string
		args   []string
	}{
		{"checkout", []string{"--", "lib.rs"}},
		{"restore", []string{"src/main.go"}},
		{"restore", []string{"--worktree", "src/main.go"}},
	} {
		if got := gitSegmentFinding(tc.subcmd, tc.args); got != "" {
			t.Errorf("gitSegmentFinding(%q, %v) = %q, want \"\" (no gk verb discards single paths)",
				tc.subcmd, tc.args, got)
		}
	}
}

// --- context must not over-claim ---------------------------------------------
//
// log/branch probes were ALL credited to `gk context`, which reports the CURRENT
// repo's state. It cannot answer "which commit mentions X" or "which branches are
// merged into main" — so the turn metric counted savings gk cannot deliver, and
// the inline hint pushed agents toward a command that does not answer them.
func TestGitSegmentFinding_ContextVsSearchVsSurvey(t *testing.T) {
	cases := []struct {
		name   string
		subcmd string
		args   []string
		want   string
	}{
		// gk context DOES answer these: where am I, what is dirty, what is recent.
		{"status", "status", nil, "raw-context-probes"},
		{"status short", "status", []string{"--short"}, "raw-context-probes"},
		{"recent log", "log", []string{"--oneline", "-5"}, "raw-context-probes"},

		// gk branch list answers the survey — gk context never did.
		{"branch verbose", "branch", []string{"-vv"}, "raw-branch-list"},
		{"branch all merged", "branch", []string{"-a", "--merged", "main"}, "raw-branch-list"},
		{"branch remotes", "branch", []string{"-r"}, "raw-branch-list"},

		// Nothing in gk answers these — they are a capability gap, not adoption.
		{"log grep", "log", []string{"--all", "--grep=ship"}, "raw-history-search"},
		{"log pickaxe", "log", []string{"-S", "tildePath"}, "raw-history-search"},
		{"log path scoped", "log", []string{"--oneline", "--", "internal/cli/x.go"}, "raw-history-search"},
		{"log patch", "log", []string{"-p"}, "raw-history-search"},
		{"log follow", "log", []string{"--follow", "x.go"}, "raw-history-search"},
		{"branch contains", "branch", []string{"--contains", "abc1234"}, "raw-history-search"},

		// A range is NOT a search — gk find cannot answer "what is in B that is
		// not in A", so it stays its own gap rather than being folded in.
		{"log range", "log", []string{"--oneline", "origin/main..HEAD"}, "raw-range-compare"},
		{"log sha range", "log", []string{"--oneline", "ce6cd4a~1..ce6cd4a"}, "raw-range-compare"},

		// A branch mutation is neither a survey nor a search.
		{"branch delete", "branch", []string{"-d", "feature"}, ""},
		{"branch create", "branch", []string{"feature"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitSegmentFinding(tc.subcmd, tc.args); got != tc.want {
				t.Errorf("gitSegmentFinding(%q, %v) = %q, want %q", tc.subcmd, tc.args, got, tc.want)
			}
		})
	}
}

// A gap has no replacement to suggest, so the hint (and the PreToolUse hook built
// on it) must stay silent rather than nag with an empty "use  instead".
func TestHint_GapKindsStaySilent(t *testing.T) {
	// A ref-range comparison has no gk verb — gk find searches, it does not diff
	// two refs, and saying otherwise is the over-claim this split exists to kill.
	for _, cmd := range []string{
		"git log --oneline origin/main..HEAD",
		"git log --oneline ce6cd4a~1..ce6cd4a",
	} {
		if res := Hint(cmd); res.Covered {
			t.Errorf("Hint(%q).Covered = true (CoveredBy=%v) — a capability gap has nothing to recommend",
				cmd, res.CoveredBy)
		}
	}
	// The search family now DOES have an answer, and the hint must name it.
	for _, cmd := range []string{
		"git log --all --grep=ship",
		"git log -S tildePath",
		"git branch --contains abc1234",
	} {
		res := Hint(cmd)
		if !res.Covered || !containsString(res.CoveredBy, "git-kit find") {
			t.Errorf("Hint(%q) = %+v, want covered by git-kit find", cmd, res)
		}
	}
	if res := Hint("git branch -vv"); !res.Covered || !containsString(res.CoveredBy, "git-kit branch list") {
		t.Errorf("Hint(git branch -vv) = %+v, want covered by git-kit branch list", res)
	}
}
