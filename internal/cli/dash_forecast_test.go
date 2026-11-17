package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func resetForecastMemo() {
	dashForecastMemo.Lock()
	dashForecastMemo.m = map[string]dashPairResult{}
	dashForecastMemo.Unlock()
}

// forecastRepo builds origin/main and four PR heads cut from the first
// commit: A and B change the same CHANGELOG.md line, C changes another file,
// and D changes a line that origin/main changed after the cut.
func forecastRepo(t *testing.T) (*testutil.Repo, map[string]string) {
	t.Helper()
	r := testutil.NewRepo(t)
	r.WriteFile("CHANGELOG.md", "# Changes\n\n- one\n\n- five\n")
	r.Commit("changelog")
	heads := map[string]string{}
	for _, b := range []struct{ name, file, content string }{
		{"a", "CHANGELOG.md", "# Changes\n\n- one A\n\n- five\n"},
		{"b", "CHANGELOG.md", "# Changes\n\n- one B\n\n- five\n"},
		{"c", "other.txt", "c\n"},
		{"d", "CHANGELOG.md", "# Changes\n\n- one\n\n- five D\n"},
	} {
		r.RunGit("checkout", "-q", "-b", b.name, "main")
		r.WriteFile(b.file, b.content)
		heads[b.name] = r.Commit(b.name)
	}
	r.Checkout("main")
	r.WriteFile("CHANGELOG.md", "# Changes\n\n- one\n\n- five base\n")
	r.Commit("base moves")
	r.RunGit("update-ref", "refs/remotes/origin/main", "main")
	for name := range heads {
		heads[name] = strings.TrimSpace(r.RunGit("rev-parse", name))
	}
	return r, heads
}

func forecastPRs(heads map[string]string) []dashForecastPR {
	now := time.Now()
	return []dashForecastPR{
		{Number: 1, HeadOID: heads["a"], Base: "main", UpdatedAt: now},
		{Number: 2, HeadOID: heads["b"], Base: "main", UpdatedAt: now.Add(-time.Minute)},
		{Number: 3, HeadOID: heads["c"], Base: "main", UpdatedAt: now.Add(-2 * time.Minute)},
		{Number: 4, HeadOID: heads["d"], Base: "main", UpdatedAt: now.Add(-3 * time.Minute)},
	}
}

func TestForecastPRConflicts(t *testing.T) {
	resetForecastMemo()
	r, heads := forecastRepo(t)
	before := r.RunGit("for-each-ref") + r.RunGit("status", "--porcelain")
	prs := append(forecastPRs(heads), dashForecastPR{Number: 5, HeadOID: strings.Repeat("e", 40), Base: "main"})

	groups := forecastPRConflicts(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", prs, dashForecastLimit, dashForecastBudget)
	if after := r.RunGit("for-each-ref") + r.RunGit("status", "--porcelain"); after != before {
		t.Errorf("refs or status changed:\n%s\n---\n%s", before, after)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	g := groups[0]
	if g.Note != "" || g.Uncomputed != 0 || g.Total != 5 || g.Considered != 5 {
		t.Fatalf("group = %+v", g)
	}
	if len(g.Pairs) != 1 || g.Pairs[0].A != 1 || g.Pairs[0].B != 2 || strings.Join(g.Pairs[0].Files, ",") != "CHANGELOG.md" {
		t.Errorf("pairs = %+v, want only #1 ↔ #2 on CHANGELOG.md", g.Pairs)
	}
	if len(g.Clean) != 3 || g.Clean[0] != 1 || g.Clean[1] != 2 || g.Clean[2] != 3 {
		t.Errorf("clean = %v, want [1 2 3]", g.Clean)
	}
	if len(g.BaseConflicts) != 1 || g.BaseConflicts[0].Number != 4 {
		t.Errorf("base conflicts = %+v, want #4", g.BaseConflicts)
	}
	if len(g.Unknown) != 1 || g.Unknown[0].A != 5 || !strings.Contains(g.Unknown[0].Reason, "head not found") {
		t.Errorf("unknown = %+v, want #5 head not found", g.Unknown)
	}
}

func TestForecastPRConflictsCapsPRsPerBase(t *testing.T) {
	resetForecastMemo()
	r, heads := forecastRepo(t)
	now := time.Now()
	var prs []dashForecastPR
	for i := 1; i <= dashForecastLimit+1; i++ {
		prs = append(prs, dashForecastPR{Number: i, HeadOID: heads["c"], Base: "main", UpdatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	g := forecastPRConflicts(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", prs, dashForecastLimit, dashForecastBudget)[0]
	if g.Total != dashForecastLimit+1 || g.Considered != dashForecastLimit {
		t.Errorf("total %d considered %d, want %d and %d", g.Total, g.Considered, dashForecastLimit+1, dashForecastLimit)
	}
	if len(g.Pairs) != 0 || len(g.Unknown) != 0 || g.Uncomputed != 0 {
		t.Errorf("group = %+v", g)
	}
}

// Pairs not computed in time are counted, never reported as clean.
func TestForecastPRConflictsTimeBudget(t *testing.T) {
	resetForecastMemo()
	r, heads := forecastRepo(t)
	g := forecastPRConflicts(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", forecastPRs(heads), dashForecastLimit, 0)[0]
	if len(g.Pairs) != 0 || len(g.BaseConflicts) != 0 {
		t.Fatalf("group = %+v, want nothing computed", g)
	}
	if len(g.Unknown) != 4 {
		t.Fatalf("unknown = %+v, want all 4 PRs", g.Unknown)
	}
	for _, u := range g.Unknown {
		if u.Reason != "time limit" {
			t.Errorf("unknown #%d reason = %q, want time limit", u.A, u.Reason)
		}
	}
}

func TestForecastPRConflictsMissingBase(t *testing.T) {
	resetForecastMemo()
	r, heads := forecastRepo(t)
	g := forecastPRConflicts(context.Background(), &git.ExecRunner{Dir: r.Dir}, "upstream", forecastPRs(heads), dashForecastLimit, dashForecastBudget)[0]
	if !strings.Contains(g.Note, "upstream/main not found") || len(g.Unknown) != 4 || len(g.Pairs) != 0 {
		t.Errorf("group = %+v", g)
	}
}

// The temporary commit needs no user identity, and the same inputs give the
// same commit.
func TestForecastPairWithoutIdentityIsDeterministic(t *testing.T) {
	r, heads := forecastRepo(t)
	r.RunGit("config", "--unset", "user.name")
	r.RunGit("config", "--unset", "user.email")
	r.RunGit("config", "user.useConfigOnly", "true")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	resetForecastMemo()
	g := forecastPRConflicts(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", forecastPRs(heads), dashForecastLimit, dashForecastBudget)[0]
	if len(g.Pairs) != 1 || len(g.Unknown) != 0 {
		t.Fatalf("group = %+v, want the #1 ↔ #2 pair and no unknown", g)
	}

	ctx := context.Background()
	runner := &git.ExecRunner{Dir: r.Dir}
	commitRunner := &git.ExecRunner{Dir: r.Dir, ExtraEnv: dashForecastEnv}
	base, _ := dashRevParse(ctx, runner, "refs/remotes/origin/main")
	mb, _ := dashMergeBase(ctx, runner, base, heads["a"])
	scan, err := scanMergeConflictsTree(ctx, runner, mb, base, heads["a"])
	if err != nil || scan.treeOID == "" {
		t.Skipf("merge-tree wrote no tree (git < 2.40?): %v", err)
	}
	var oids []string
	for range 2 {
		resetForecastMemo()
		merged := map[int]string{}
		res := forecastPair(ctx, runner, commitRunner, merged, base, dashForecastPR{Number: 1, HeadOID: heads["a"]}, scan.treeOID, dashForecastPR{Number: 2, HeadOID: heads["b"]})
		if res.err != nil {
			t.Fatalf("forecastPair: %v", res.err)
		}
		oids = append(oids, merged[1])
	}
	if oids[0] == "" || oids[0] != oids[1] {
		t.Errorf("temporary commits = %v, want one OID twice", oids)
	}
}
