package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/testutil"
)

// setupTwoPickRebaseConflict builds a rebase where BOTH picks conflict.
// l1 and l2 sit far apart so git treats them as independent hunks —
// adjacent lines would collapse into one conflict region and the whole
// rebase would finish in a single round.
//
//	main:  changes l1→main1 AND l2→main2  (one commit)
//	feat:  commit1 changes l1→feat1, commit2 changes l2→feat2
//
// Pick1 conflicts on l1 (l2 merges clean as main2); after a theirs
// resolution pick2 conflicts on l2 (main2 vs feat2) — exercising the
// re-resolve loop.
const twoPickFiller = "f1\nf2\nf3\nf4\nf5\nf6\nf7\nf8\n"

func setupTwoPickRebaseConflict(t *testing.T) *testutil.Repo {
	t.Helper()
	r := testutil.NewRepo(t)

	r.WriteFile("file.txt", "l1\n"+twoPickFiller+"l2\n")
	r.Commit("base")

	r.CreateBranch("feat")
	r.WriteFile("file.txt", "feat1\n"+twoPickFiller+"l2\n")
	r.Commit("feat: change l1")
	r.WriteFile("file.txt", "feat1\n"+twoPickFiller+"feat2\n")
	r.Commit("feat: change l2")

	r.Checkout("main")
	r.WriteFile("file.txt", "main1\n"+twoPickFiller+"main2\n")
	r.Commit("main: change both")

	r.Checkout("feat")
	if _, err := r.TryGit("rebase", "main"); err == nil {
		t.Skip("expected rebase conflict but none occurred")
	}
	return r
}

// runResolveCmd runs the registered resolve command against repo with
// the given flag overrides, restoring flags afterwards.
func runResolveCmd(t *testing.T, repoDir string, out *bytes.Buffer, flags map[string]string) error {
	t.Helper()
	cmd, _, _ := rootCmd.Find([]string{"resolve"})
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s: %v", k, err)
		}
	}
	t.Cleanup(func() {
		for k := range flags {
			f := cmd.Flags().Lookup(k)
			_ = cmd.Flags().Set(k, f.DefValue)
		}
		cmd.SetOut(nil)
	})
	if out != nil {
		cmd.SetOut(out)
	}
	// Deliberately NO chdir into the repo: resolve must anchor file IO at
	// the worktree root on its own (--repo from outside the repo).
	flagRepo = repoDir
	cmd.SetContext(context.Background())
	return cmd.RunE(cmd, nil)
}

func TestResolveAutoContinue_MultiPickRebase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	r := setupTwoPickRebaseConflict(t)
	t.Setenv("GIT_EDITOR", "false") // would fail the commit if git ever opened it

	var out bytes.Buffer
	if err := runResolveCmd(t, r.Dir, &out, map[string]string{"strategy": "theirs"}); err != nil {
		t.Fatalf("resolve --strategy theirs: %v", err)
	}

	state, err := gitstate.Detect(context.Background(), r.Dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Kind != gitstate.StateNone {
		t.Fatalf("expected rebase fully finished, still in %s\noutput:\n%s", state.Kind, out.String())
	}
	got := r.RunGit("show", "HEAD:file.txt")
	if !strings.HasPrefix(got, "feat1\n") || !strings.Contains(got, "feat2") {
		t.Errorf("final content = %q, want both feat lines (theirs applied per pick)", got)
	}
	if !strings.Contains(out.String(), "next pick conflicted") {
		t.Errorf("expected the re-resolve round to be narrated, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "rebase complete") {
		t.Errorf("expected completion line, got:\n%s", out.String())
	}
}

func TestResolveAutoContinue_EmptyPickSkipped(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	// --strategy ours resolves the pick to the upstream content, so the
	// pick stages nothing and must be skipped, not committed empty.
	r := setupRebaseConflict(t)
	t.Setenv("GIT_EDITOR", "false")

	var out bytes.Buffer
	if err := runResolveCmd(t, r.Dir, &out, map[string]string{"strategy": "ours"}); err != nil {
		t.Fatalf("resolve --strategy ours: %v", err)
	}

	state, err := gitstate.Detect(context.Background(), r.Dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Kind != gitstate.StateNone {
		t.Fatalf("expected rebase finished, still in %s\noutput:\n%s", state.Kind, out.String())
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("expected empty-pick skip narration, got:\n%s", out.String())
	}
	if log := r.RunGit("log", "--oneline"); strings.Contains(log, "feat: change file") {
		t.Errorf("emptied pick should have been dropped from history:\n%s", log)
	}
}

func TestResolveNoContinue_StopsAfterResolving(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	r := setupRebaseConflict(t)
	t.Setenv("GIT_EDITOR", "false")

	var out bytes.Buffer
	if err := runResolveCmd(t, r.Dir, &out, map[string]string{
		"strategy": "theirs", "no-continue": "true",
	}); err != nil {
		t.Fatalf("resolve --no-continue: %v", err)
	}

	state, err := gitstate.Detect(context.Background(), r.Dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Kind == gitstate.StateNone {
		t.Fatal("--no-continue must leave the rebase paused")
	}
	if !strings.Contains(out.String(), "run 'gk continue'") {
		t.Errorf("expected the manual continue hint, got:\n%s", out.String())
	}
}

func TestResolveAutoContinue_JSONReport(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	r := setupTwoPickRebaseConflict(t)
	t.Setenv("GIT_EDITOR", "false")

	flagJSON = true
	t.Cleanup(func() { flagJSON = false })

	var out bytes.Buffer
	if err := runResolveCmd(t, r.Dir, &out, map[string]string{"strategy": "theirs"}); err != nil {
		t.Fatalf("resolve --strategy theirs: %v", err)
	}

	var rep resolveReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("output is not a resolveReport: %v\n%s", err, out.String())
	}
	if !rep.Done || rep.State != "none" {
		t.Errorf("report = %+v, want done=true state=none", rep)
	}
	if rep.Rounds != 2 {
		t.Errorf("rounds = %d, want 2 (pick2 re-resolved)", rep.Rounds)
	}
	if rep.Total != 2 || len(rep.Resolved) != 2 {
		t.Errorf("total=%d resolved=%v, want 2 files across rounds", rep.Total, rep.Resolved)
	}
}
