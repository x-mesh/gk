package sessionaudit

import (
	"os"
	"path/filepath"
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

// The turn metric is opt-in and additive: default Audit leaves Turns nil and
// every occurrence field unchanged; --metric=turns adds the turn view.
func TestAudit_TurnMetricOptInAndCodexGated(t *testing.T) {
	dir := t.TempDir()
	// The source classifier reads "claude" from a "/.claude/" path segment.
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
	// A Codex session must be excluded from the turn metric.
	codex := filepath.Join(codexDir, "c.jsonl")
	if err := os.WriteFile(codex, []byte(`{"payload":{"arguments":"{\"cmd\":\"git status\"}"}}`+"\n"), 0o644); err != nil {
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

	// Opt-in: turn metric present, 3 context probes across 3 turns → 2 saved.
	got, err := Audit(Options{Paths: []string{claude, codex}, Home: dir, MaxFiles: 10, Metric: "turns"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns == nil {
		t.Fatal("--metric=turns must populate Turns")
	}
	if got.Turns.EstimatedTurnsSaved != 2 {
		t.Fatalf("estimated turns saved = %d, want 2: %+v", got.Turns.EstimatedTurnsSaved, got.Turns)
	}
	if got.Turns.GitTurns != 3 {
		t.Fatalf("git turns = %d, want 3 (Codex excluded)", got.Turns.GitTurns)
	}
	// Occurrence fields must match the default run exactly (additive only).
	if got.Adoption != base.Adoption || got.Totals != base.Totals {
		t.Fatalf("turn metric changed occurrence output:\n base=%+v %+v\n got=%+v %+v", base.Adoption, base.Totals, got.Adoption, got.Totals)
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
