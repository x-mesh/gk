package sessionaudit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readMsg(msgID string) string {
	return `{"type":"assistant","message":{"id":"` + msgID + `","role":"assistant","content":[{"type":"tool_use","id":"r_` + msgID + `","name":"Read","input":{"file_path":"/a.go"}}]}}`
}

func runsFor(data []byte) []CollapsibleRun {
	return DetectCollapsibleRuns(SessionTurns(data), collapseMaxGap)
}

func totalSaved(runs []CollapsibleRun) int {
	s := 0
	for _, r := range runs {
		s += r.TurnsSaved
	}
	return s
}

func savedForGroup(runs []CollapsibleRun, group string) int {
	s := 0
	for _, r := range runs {
		if r.Group == group {
			s += r.TurnsSaved
		}
	}
	return s
}

// A: three context probes across three turns collapse to one gk context → 2 saved.
func TestCollapse_A_SequentialProbesCollapse(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t1", "git status"),
		asst("m2", "t2", "git log --oneline -5"),
		asst("m3", "t3", "git diff --stat"),
	))
	if savedForGroup(runs, "context") != 2 {
		t.Fatalf("context saved = %d, want 2: %+v", savedForGroup(runs, "context"), runs)
	}
	if len(runs) != 1 || runs[0].GkCommand != "git-kit context" {
		t.Fatalf("want one context run, got %+v", runs)
	}
}

// B: a single &&-chain is already one turn → 0 saved (the core (A)/(B) fix).
func TestCollapse_B_ShellChainSavesNothing(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t1", "git status && git log --oneline -5 && git diff"),
	))
	if totalSaved(runs) != 0 {
		t.Fatalf("shell chain must save 0 turns, got %+v", runs)
	}
}

// C: parallel tool calls share one turn → 0 saved.
func TestCollapse_C_ParallelSavesNothing(t *testing.T) {
	parallel := `{"type":"assistant","message":{"id":"mP","role":"assistant","content":[` +
		`{"type":"tool_use","id":"p1","name":"Bash","input":{"command":"git status"}},` +
		`{"type":"tool_use","id":"p2","name":"Bash","input":{"command":"git log"}},` +
		`{"type":"tool_use","id":"p3","name":"Bash","input":{"command":"git diff"}}]}}`
	runs := runsFor(session(parallel))
	if totalSaved(runs) != 0 {
		t.Fatalf("parallel calls share a turn, must save 0, got %+v", runs)
	}
}

// D: a failed call (and its retry) must not inflate savings.
func TestCollapse_D_RetryExcluded(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t_add1", "git add ."),
		result("t_add1", true), // first add errored
		asst("m2", "t_add2", "git add ."),
		result("t_add2", false), // retried add
		asst("m3", "t_commit", "git commit -m x"),
		result("t_commit", false),
	))
	// turns: add(err,0) add(ok,1) commit(2). Dropping the errored turn leaves
	// distinct commit-group turns {1,2} → 1 saved (not 2).
	if savedForGroup(runs, "commit") != 1 {
		t.Fatalf("commit saved = %d, want 1 (errored turn excluded): %+v", savedForGroup(runs, "commit"), runs)
	}
}

// E: the same verb at unrelated, far-apart points is not a collapsible run.
func TestCollapse_E_UnrelatedFarApartProbes(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t1", "git status"),
		readMsg("r1"),
		readMsg("r2"),
		readMsg("r3"),
		asst("m2", "t2", "git status"),
	))
	if totalSaved(runs) != 0 {
		t.Fatalf("far-apart probes (gap > tolerance) must not collapse, got %+v", runs)
	}
}

// F: git calls in different repos/worktrees never merge into one run.
func TestCollapse_F_RepoBoundaryBreaksRun(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t1", "cd /work/repoA && git status"),
		asst("m2", "t2", "cd /work/repoB && git status"),
	))
	if totalSaved(runs) != 0 {
		t.Fatalf("cross-repo commands must not collapse, got %+v", runs)
	}
}

// A single interleaved non-git turn is within tolerance → still collapses.
func TestCollapse_InterleaveWithinGapMerges(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t1", "git status"),
		readMsg("r1"),
		asst("m2", "t2", "git log --oneline -5"),
	))
	if savedForGroup(runs, "context") != 1 {
		t.Fatalf("one interleaved turn is within tolerance, want 1 saved: %+v", runs)
	}
}

// G: the same verb inspecting different objects (git show A, B, C) is paging,
// not a collapsible run — one gk call cannot replace distinct targets.
func TestCollapse_G_PagingDifferentTargetsDoNotCollapse(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t1", "git show abc123 -- a.go"),
		asst("m2", "t2", "git show def456 -- b.go"),
		asst("m3", "t3", "git show develop:c.go"),
	))
	if totalSaved(runs) != 0 {
		t.Fatalf("git show of different objects must not collapse, got %+v", runs)
	}
}

// The paging guard must not over-break: the same verb with no/identical target
// repeated across turns is still a collapsible probe sequence.
func TestCollapse_RepeatedIdenticalProbeCollapses(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t1", "git status"),
		asst("m2", "t2", "git status"),
		asst("m3", "t3", "git status"),
	))
	if savedForGroup(runs, "context") != 2 {
		t.Fatalf("repeated identical status should collapse, want 2 saved: %+v", runs)
	}
}

func TestCollapseNudgeFor(t *testing.T) {
	recent := SessionTurns(session(
		asst("m1", "t1", "git status"),
		asst("m2", "t2", "git log --oneline -5"),
	))
	lookback := collapseMaxGap + 1

	// Pending `git diff --stat` continues the context run → nudge to gk context.
	if n := CollapseNudgeFor("git diff --stat", recent, lookback); n == nil || n.Group != "context" || n.GkCommand != "git-kit context" {
		t.Fatalf("expected context nudge, got %+v", n)
	}
	// A pending non-git command does not nudge.
	if n := CollapseNudgeFor("ls -la", recent, lookback); n != nil {
		t.Fatalf("non-git must not nudge, got %+v", n)
	}
	// A different group (commit) with no recent commit turn does not nudge.
	if n := CollapseNudgeFor("git commit -m x", recent, lookback); n != nil {
		t.Fatalf("no recent commit run → no nudge, got %+v", n)
	}
}

func TestCollapseNudgeFor_RepoAndPagingGuards(t *testing.T) {
	lookback := collapseMaxGap + 1

	// Different repo → no nudge.
	recent := SessionTurns(session(asst("m1", "t1", "cd /a && git status")))
	if n := CollapseNudgeFor("cd /b && git log", recent, lookback); n != nil {
		t.Fatalf("cross-repo must not nudge, got %+v", n)
	}
	// Same verb, different target (paging) → no nudge.
	recent = SessionTurns(session(asst("m1", "t1", "git show abc -- a.go")))
	if n := CollapseNudgeFor("git show def -- b.go", recent, lookback); n != nil {
		t.Fatalf("paging different targets must not nudge, got %+v", n)
	}
}

func codexExec(callID, cmd string) string {
	return `{"payload":{"type":"function_call","name":"exec_command","call_id":"` + callID +
		`","arguments":"{\"cmd\":\"` + cmd + `\",\"workdir\":\"/w\"}"}}`
}

func codexOut(callID string) string {
	return `{"payload":{"type":"function_call_output","call_id":"` + callID + `","output":"Process exited with code 0"}}`
}

// The turn metric is opt-in and additive (default Turns nil, occurrence fields
// unchanged) and now spans both Claude and Codex sessions.
func TestAudit_TurnMetricOptInAndBothSources(t *testing.T) {
	dir := t.TempDir()
	// The source classifier reads "claude"/"codex" from the path segment.
	claudeDir := filepath.Join(dir, ".claude", "projects")
	codexDir := filepath.Join(dir, ".codex", "sessions")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(claudeDir, "s.jsonl")
	if err := os.WriteFile(claude, session(
		asst("m1", "t1", "git status"),
		asst("m2", "t2", "git log --oneline -5"),
		asst("m3", "t3", "git branch -a"),
	), 0o644); err != nil {
		t.Fatal(err)
	}
	// Codex: two function_call batches, each a git status → 2 turns, collapse 1.
	codex := filepath.Join(codexDir, "c.jsonl")
	if err := os.WriteFile(codex, []byte(strings.Join([]string{
		codexExec("c1", "git status"), codexOut("c1"),
		codexExec("c2", "git status"), codexOut("c2"),
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Default: no turn metric, occurrence output intact.
	base, err := Audit(Options{Paths: []string{claude, codex}, Home: dir, MaxFiles: 10})
	if err != nil {
		t.Fatal(err)
	}
	if base.Turns != nil {
		t.Fatalf("default Audit must not compute Turns, got %+v", base.Turns)
	}

	got, err := Audit(Options{Paths: []string{claude, codex}, Home: dir, MaxFiles: 10, Metric: "turns"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns == nil {
		t.Fatal("--metric=turns must populate Turns")
	}
	// Claude 3-context run saves 2; Codex 2-turn status run saves 1.
	if got.Turns.EstimatedTurnsSaved != 3 {
		t.Fatalf("estimated turns saved = %d, want 3: %+v", got.Turns.EstimatedTurnsSaved, got.Turns)
	}
	// 3 Claude git turns + 2 Codex git turns.
	if got.Turns.GitTurns != 5 {
		t.Fatalf("git turns = %d, want 5 (Claude 3 + Codex 2)", got.Turns.GitTurns)
	}
	// Occurrence fields must match the default run exactly (additive only).
	if got.Adoption != base.Adoption || got.Totals != base.Totals {
		t.Fatalf("turn metric changed occurrence output:\n base=%+v %+v\n got=%+v %+v", base.Adoption, base.Totals, got.Adoption, got.Totals)
	}
}

// CodexSessionTurns: a function_call batch is one turn, parallel calls share it,
// workdir is the repo, and the exit code in the output drives IsError.
func TestCodexSessionTurns(t *testing.T) {
	data := []byte(strings.Join([]string{
		codexExec("c1", "git status"), // batch 1
		codexExec("c2", "git log"),    // same batch → same turn (no output yet)
		codexOut("c1"), codexOut("c2"),
		codexExec("c3", "git diff"), // batch 2 → new turn
		`{"payload":{"type":"function_call_output","call_id":"c3","output":"Process exited with code 1"}}`,
	}, "\n") + "\n")

	events := CodexSessionTurns(data)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}
	if events[0].Turn != events[1].Turn {
		t.Errorf("calls in one batch must share a turn, got %d and %d", events[0].Turn, events[1].Turn)
	}
	if events[2].Turn == events[0].Turn {
		t.Errorf("a new batch must be a new turn")
	}
	if events[0].Repo != "/w" {
		t.Errorf("repo from workdir = %q, want /w", events[0].Repo)
	}
	if events[0].IsError {
		t.Errorf("c1 exited 0, should not be error")
	}
	if !events[2].IsError {
		t.Errorf("c3 exited 1, should be error")
	}
}

// add then commit across two turns collapses to one gk commit.
func TestCollapse_CommitSequence(t *testing.T) {
	runs := runsFor(session(
		asst("m1", "t1", "git add -A"),
		asst("m2", "t2", "git commit -m wip"),
	))
	if savedForGroup(runs, "commit") != 1 {
		t.Fatalf("add+commit across turns want 1 saved: %+v", runs)
	}
}
