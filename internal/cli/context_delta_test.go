package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// marshalCore serializes the core context exactly as runContextWithDelta does
// before include sections are fused in.
func marshalCore(t *testing.T, out contextJSON) []byte {
	t.Helper()
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal core: %v", err)
	}
	return b
}

// TestContextDelta_ThreeStageScenario walks the full lifecycle of the
// per-worktree ledger: first call is a baseline (no prior snapshot), an
// immediate re-call with the same core is "unchanged", and a call after a
// real working-tree change reports only the core fields that moved.
func TestContextDelta_ThreeStageScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	home := t.TempDir()
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "seed\n")
	repo.Commit("feat: seed")

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	ctx := context.Background()

	// Stage 1: first ever call → baseline.
	out1, err := collectContext(ctx, runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext stage 1: %v", err)
	}
	core1 := marshalCore(t, out1)
	o1 := computeContextDelta(home, repo.Dir, core1)
	if o1.kind != "baseline" {
		t.Fatalf("stage 1 kind = %q, want baseline", o1.kind)
	}

	// Stage 2: identical core → unchanged, with a base timestamp.
	o2 := computeContextDelta(home, repo.Dir, core1)
	if o2.kind != "unchanged" {
		t.Fatalf("stage 2 kind = %q, want unchanged", o2.kind)
	}
	if o2.base == "" {
		t.Errorf("stage 2 base should carry the baseline SavedAt")
	}
	if len(o2.changed) != 0 {
		t.Errorf("stage 2 changed should be empty, got %v", o2.changed)
	}

	// Stage 3: an untracked file moves dirty/next_actions → changed, and only
	// the fields that actually differ show up.
	repo.WriteFile("wip.txt", "wip\n")
	out3, err := collectContext(ctx, runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext stage 3: %v", err)
	}
	core3 := marshalCore(t, out3)
	o3 := computeContextDelta(home, repo.Dir, core3)
	if o3.kind != "changed" {
		t.Fatalf("stage 3 kind = %q, want changed", o3.kind)
	}
	if _, ok := o3.changed["dirty"]; !ok {
		t.Errorf("stage 3 changed should include dirty, got keys %v", rawKeysOf(o3.changed))
	}
	// Unchanged core fields must NOT be present in the changed set.
	if _, ok := o3.changed["schema"]; ok {
		t.Errorf("stage 3 changed leaked an unchanged field: schema")
	}
	if _, ok := o3.changed["branch"]; ok {
		t.Errorf("stage 3 changed leaked an unchanged field: branch")
	}
}

// TestContextDelta_CorruptLedgerFallsBackToBaseline covers both flavors of a
// broken baseline: a file that does not decode as a ledger entry at all, and
// a well-formed entry whose stored snapshot is not a JSON object. Either must
// degrade to the cold-start baseline rather than error or misclassify.
func TestContextDelta_CorruptLedgerFallsBackToBaseline(t *testing.T) {
	core := []byte(`{"schema":1,"branch":"main"}`)

	t.Run("undecodable file", func(t *testing.T) {
		home := t.TempDir()
		worktree := "/corrupt/one"
		path := contextLedgerPath(home, worktree)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if o := computeContextDelta(home, worktree, core); o.kind != "baseline" {
			t.Fatalf("kind = %q, want baseline for an undecodable ledger", o.kind)
		}
	})

	t.Run("valid entry, non-object snapshot", func(t *testing.T) {
		home := t.TempDir()
		worktree := "/corrupt/two"
		path := contextLedgerPath(home, worktree)
		entry := contextLedgerEntry{
			Schema:   contextLedgerSchema,
			Worktree: normalizeWorktreePath(worktree),
			SavedAt:  time.Now().UTC(),
			Snapshot: json.RawMessage(`"not an object"`),
		}
		if err := saveContextLedger(path, entry); err != nil {
			t.Fatalf("save: %v", err)
		}
		if o := computeContextDelta(home, worktree, core); o.kind != "baseline" {
			t.Fatalf("kind = %q, want baseline for a non-object snapshot", o.kind)
		}
	})
}

// TestContextDelta_VanishedFieldSurfacedAsNull verifies a top-level field that
// disappears from the core (an omitempty field clearing) is reported as JSON
// null instead of being silently dropped — the agent needs to see the
// transition (e.g. a rebase finishing clears in_progress).
func TestContextDelta_VanishedFieldSurfacedAsNull(t *testing.T) {
	home := t.TempDir()
	worktree := "/vanish/wt"

	withField := marshalCore(t, contextJSON{
		Schema:      1,
		Branch:      "main",
		InProgress:  &contextOpJSON{Kind: "rebase", Resume: "gk continue", Abort: "gk abort"},
		NextActions: []string{},
	})
	withoutField := marshalCore(t, contextJSON{
		Schema:      1,
		Branch:      "main",
		NextActions: []string{},
	})

	// Baseline carries in_progress...
	if o := computeContextDelta(home, worktree, withField); o.kind != "baseline" {
		t.Fatalf("seed kind = %q, want baseline", o.kind)
	}
	// ...then it clears.
	o := computeContextDelta(home, worktree, withoutField)
	if o.kind != "changed" {
		t.Fatalf("kind = %q, want changed", o.kind)
	}
	v, ok := o.changed["in_progress"]
	if !ok {
		t.Fatalf("in_progress should be surfaced as changed, got keys %v", rawKeysOf(o.changed))
	}
	if string(v) != "null" {
		t.Errorf("vanished in_progress = %s, want null", v)
	}
}

// TestContextDelta_UnchangedResponseIsCompact asserts the DoD's byte budget:
// an unchanged delta payload serializes to at most 20% of the full core
// document it replaces (≥80% smaller), so repeated orientation stays cheap.
func TestContextDelta_UnchangedResponseIsCompact(t *testing.T) {
	full := contextJSON{
		Schema:     1,
		Branch:     "feature/attention-ledger",
		Upstream:   "origin/feature/attention-ledger",
		Ahead:      3,
		Behind:     2,
		Dirty:      contextDirtyJSON{Staged: 2, Unstaged: 1, Untracked: 4},
		InProgress: &contextOpJSON{Kind: "rebase", Resume: "gk continue", Abort: "gk abort"},
		Base:       &contextBaseJSON{Name: "main", BehindRemote: 5, CheckedOutIn: "/repo/main-wt"},
		LatestTag:  "v0.117.0",
		Worktrees: []contextWorktreeJSON{
			{Path: "/repo/main-wt", Branch: "main", Behind: 5},
			{Path: "/repo/feature", Branch: "feature/attention-ledger", Current: true, Ahead: 3, Behind: 2,
				Dirty: &contextDirtyJSON{Staged: 2, Unstaged: 1, Untracked: 4}},
		},
		NextActions: []string{"gk commit", "gk pull", "gk push", "gk pull --with-base"},
	}

	fullBytes, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	unchanged := unchangedDeltaResponse(time.Now().UTC().Format(time.RFC3339), nil)
	unchangedBytes, err := json.Marshal(unchanged)
	if err != nil {
		t.Fatalf("marshal unchanged: %v", err)
	}

	if len(unchangedBytes)*5 > len(fullBytes) {
		t.Errorf("unchanged response not ≥80%% smaller: unchanged=%d bytes, full=%d bytes",
			len(unchangedBytes), len(fullBytes))
	}
}

// TestFreshIncludeFields verifies the include-section extraction pulls exactly
// the include keys (fresh, verbatim) and never the core fields — the compact
// delta responses must not accidentally re-diff or drop include sections.
func TestFreshIncludeFields(t *testing.T) {
	out := contextJSON{
		Schema:      1,
		Branch:      "main",
		NextActions: []string{"gk commit"},
		Log:         []contextLogJSON{{SHA: "abc123", Subject: "feat: x", Author: "j", Date: "2026-01-01T00:00:00Z"}},
		Notes:       []string{"precheck skipped: no upstream"},
	}
	inc, err := freshIncludeFields(out)
	if err != nil {
		t.Fatalf("freshIncludeFields: %v", err)
	}
	if _, ok := inc["log"]; !ok {
		t.Errorf("expected log among fresh include fields, got %v", rawKeysOf(inc))
	}
	if _, ok := inc["notes"]; !ok {
		t.Errorf("expected notes among fresh include fields, got %v", rawKeysOf(inc))
	}
	// Core fields must never leak into the include set.
	for _, coreKey := range []string{"schema", "branch", "next_actions", "dirty"} {
		if _, ok := inc[coreKey]; ok {
			t.Errorf("core field %q leaked into fresh include fields", coreKey)
		}
	}
}

// TestChangedDeltaResponse_CarriesChangedAndIncludes verifies the changed
// response merges the changed core fields and the fresh include sections
// under a "changed" marker with a base timestamp.
func TestChangedDeltaResponse_CarriesChangedAndIncludes(t *testing.T) {
	changed := map[string]json.RawMessage{
		"dirty": json.RawMessage(`{"staged":0,"unstaged":1,"untracked":0,"conflicts":0}`),
	}
	inc := map[string]json.RawMessage{
		"log": json.RawMessage(`[{"sha":"abc"}]`),
	}
	resp := changedDeltaResponse("2026-07-12T00:00:00Z", changed, inc)

	if string(resp["delta"]) != `"changed"` {
		t.Errorf("delta marker = %s, want \"changed\"", resp["delta"])
	}
	if string(resp["delta_base"]) != `"2026-07-12T00:00:00Z"` {
		t.Errorf("delta_base = %s", resp["delta_base"])
	}
	if _, ok := resp["dirty"]; !ok {
		t.Errorf("changed core field dirty missing from response: %v", rawKeysOf(resp))
	}
	if _, ok := resp["log"]; !ok {
		t.Errorf("fresh include field log missing from response: %v", rawKeysOf(resp))
	}
	if _, ok := resp["unchanged"]; ok {
		t.Errorf("changed response must not carry an unchanged flag")
	}
}

func rawKeysOf(m map[string]json.RawMessage) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
