package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/testutil"
)

// benchSeedRepo builds a repo with a handful of clean text commits on top of
// testutil's root commit, and returns the number of NON-root commits (the
// ones that have a parent and are therefore replayable).
func benchSeedRepo(t *testing.T) (*testutil.Repo, int) {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "1\n2\n3\n")
	repo.Commit("add a")
	repo.WriteFile("a.txt", "1\nTWO\n3\n")
	repo.WriteFile("b.txt", "hello\n")
	repo.Commit("edit a, add b")
	repo.WriteFile("a.txt", "1\nTWO\n3\nfour\n")
	repo.Commit("extend a")
	return repo, 3
}

// TestBenchApplySanityAllPass: on clean text history every non-root commit
// replays to an identical tree via the plain rung; the root commit (no
// parent) is skipped. No regressions, no apply failures.
func TestBenchApplySanityAllPass(t *testing.T) {
	repo, nonRoot := benchSeedRepo(t)
	home := t.TempDir()

	res, cases, err := runApplySanity(context.Background(), repo.Dir, home, 200, 1)
	if err != nil {
		t.Fatalf("runApplySanity: %v", err)
	}

	if res.Regressions != 0 {
		t.Errorf("regressions = %d, want 0", res.Regressions)
	}
	if res.ApplyFailed != 0 {
		t.Errorf("apply_failed = %d, want 0", res.ApplyFailed)
	}
	if res.Pass != nonRoot {
		t.Errorf("pass = %d, want %d", res.Pass, nonRoot)
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the root commit)", res.Skipped)
	}
	if want := nonRoot + 1; res.Total != want {
		t.Errorf("total = %d, want %d", res.Total, want)
	}
	if res.Rungs[applyStrategyPlain] != nonRoot {
		t.Errorf("rungs[plain] = %d, want %d", res.Rungs[applyStrategyPlain], nonRoot)
	}

	for _, c := range cases {
		if c.Parent == "" {
			if c.Outcome != benchOutcomeSkipped {
				t.Errorf("root commit %s: outcome = %q, want skipped", c.Commit, c.Outcome)
			}
		} else {
			if c.Outcome != benchOutcomePass {
				t.Errorf("commit %s: outcome = %q, want pass (reason %q)", c.Commit, c.Outcome, c.Reason)
			}
			if c.Rung != applyStrategyPlain {
				t.Errorf("commit %s: rung = %q, want plain", c.Commit, c.Rung)
			}
			if c.WantTree == "" || c.WantTree != c.GotTree {
				t.Errorf("commit %s: want_tree %q != got_tree %q", c.Commit, c.WantTree, c.GotTree)
			}
		}
	}
}

// TestBenchClassifyCase covers the three classification branches directly —
// in particular the mismatch (regression) branch, which a well-behaved
// ladder never produces on real history and so cannot be reached end-to-end.
func TestBenchClassifyCase(t *testing.T) {
	t.Run("apply-failed", func(t *testing.T) {
		outcome, reason := classifyBenchCase(errors.New("patch does not apply"), "aaa", "")
		if outcome != benchOutcomeApplyFailed {
			t.Errorf("outcome = %q, want apply-failed", outcome)
		}
		if reason == "" {
			t.Error("apply-failed reason should carry the apply error")
		}
	})
	t.Run("mismatch-is-regression", func(t *testing.T) {
		outcome, reason := classifyBenchCase(nil, "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb")
		if outcome != benchOutcomeMismatch {
			t.Errorf("outcome = %q, want mismatch", outcome)
		}
		if !strings.Contains(reason, "want") || !strings.Contains(reason, "got") {
			t.Errorf("mismatch reason = %q, want want/got detail", reason)
		}
	})
	t.Run("pass", func(t *testing.T) {
		outcome, reason := classifyBenchCase(nil, "deadbeef", "deadbeef")
		if outcome != benchOutcomePass {
			t.Errorf("outcome = %q, want pass", outcome)
		}
		if reason != "" {
			t.Errorf("pass reason = %q, want empty", reason)
		}
	})
}

// TestBenchApplySanityNoTouch: a replay must leave the repo's working tree,
// index, and HEAD exactly as it found them — even when the repo starts dirty
// with both staged and unstaged changes.
func TestBenchApplySanityNoTouch(t *testing.T) {
	repo, _ := benchSeedRepo(t)

	// Dirty the repo: one staged change, one unstaged change, one untracked.
	repo.WriteFile("a.txt", "1\nTWO\n3\nfour\nstaged\n")
	repo.RunGit("add", "a.txt")
	repo.WriteFile("b.txt", "hello\nunstaged\n")
	repo.WriteFile("untracked.txt", "loose\n")

	statusBefore := repo.RunGit("status", "--porcelain")
	headBefore := repo.RunGit("rev-parse", "HEAD")
	indexBefore := repo.RunGit("write-tree")

	if _, _, err := runApplySanity(context.Background(), repo.Dir, t.TempDir(), 200, 7); err != nil {
		t.Fatalf("runApplySanity: %v", err)
	}

	if got := repo.RunGit("status", "--porcelain"); got != statusBefore {
		t.Errorf("status changed after replay:\nbefore:\n%s\nafter:\n%s", statusBefore, got)
	}
	if got := repo.RunGit("rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved: before %s after %s", headBefore, got)
	}
	if got := repo.RunGit("write-tree"); got != indexBefore {
		t.Errorf("index tree changed: before %s after %s", indexBefore, got)
	}
}

// TestBenchApplySanityCasesRoundTrip: the per-case JSONL file written by a run
// parses back into the identical records the run returned.
func TestBenchApplySanityCasesRoundTrip(t *testing.T) {
	repo, _ := benchSeedRepo(t)

	res, cases, err := runApplySanity(context.Background(), repo.Dir, t.TempDir(), 200, 42)
	if err != nil {
		t.Fatalf("runApplySanity: %v", err)
	}
	if res.CasesFile == "" {
		t.Fatal("result cases_file is empty")
	}

	got, err := readBenchCases(res.CasesFile)
	if err != nil {
		t.Fatalf("readBenchCases: %v", err)
	}
	if len(got) != len(cases) {
		t.Fatalf("round-trip len = %d, want %d", len(got), len(cases))
	}
	for i := range cases {
		if got[i] != cases[i] {
			t.Errorf("case %d round-trip mismatch:\n got %+v\nwant %+v", i, got[i], cases[i])
		}
	}
}

// TestBenchApplySanityEnvelope: under GK_AGENT the command emits the standard
// agent envelope — state "ok", ok true, and a result carrying the summary.
func TestBenchApplySanityEnvelope(t *testing.T) {
	repo, nonRoot := benchSeedRepo(t)
	t.Setenv("HOME", t.TempDir())
	withAgentMode(t, true)
	setRepoFlagForTest(t, repo.Dir)

	cmd := newBenchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"apply-sanity"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute bench apply-sanity: %v\noutput: %s", err, out.String())
	}

	var env struct {
		Schema int                    `json:"schema"`
		State  string                 `json:"state"`
		OK     bool                   `json:"ok"`
		Result benchApplySanityResult `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\noutput: %s", err, out.String())
	}
	if env.State != envStateOK || !env.OK {
		t.Errorf("envelope state = %q ok = %v, want ok/true", env.State, env.OK)
	}
	if env.Result.Schema != benchApplySanitySchema {
		t.Errorf("result schema = %d, want %d", env.Result.Schema, benchApplySanitySchema)
	}
	if env.Result.Pass != nonRoot || env.Result.Regressions != 0 {
		t.Errorf("result pass = %d regressions = %d, want %d/0", env.Result.Pass, env.Result.Regressions, nonRoot)
	}
	if env.Result.CasesFile == "" {
		t.Error("result cases_file is empty")
	}
}
