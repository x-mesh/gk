package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// gitCallCounter tallies ExecRunner invocations by command shape while a test
// runs. ExecHook is a process global, so counting tests cannot be parallel.
type gitCallCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newGitCallCounter(t *testing.T) *gitCallCounter {
	t.Helper()
	c := &gitCallCounter{counts: map[string]int{}}
	git.ExecHook = func(args []string, _ time.Duration, _ error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.counts[strings.Join(args, " ")]++
	}
	t.Cleanup(func() { git.ExecHook = nil })
	return c
}

// get counts the recorded calls whose argument line contains frag.
func (c *gitCallCounter) get(frag string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for line, count := range c.counts {
		if strings.Contains(line, frag) {
			n += count
		}
	}
	return n
}

func (c *gitCallCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = map[string]int{}
}

// TestGatherFleetRepo_SecondPollSkipsSettledProbes pins the poll-cost work: a
// dashboard poll re-asks git only for what can have changed since the previous
// one. The probes below are each a pure function of state the poll already has
// (two commit tips, a ref file, a config file, a set of file fingerprints), and
// re-forking them per worktree per poll was most of what `gk watch` cost.
//
// The assertion is "zero on an unchanged repo", not "fewer": anything above
// zero means a probe re-ran for an answer nothing could have moved.
func TestGatherFleetRepo_SecondPollSkipsSettledProbes(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	base := strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD"))
	repo.AddRemote("origin", repo.Dir)
	repo.SetRemoteHEAD("origin", base)
	repo.CreateBranch("feature")
	repo.Checkout("feature")
	repo.WriteFile("b.txt", "b")
	repo.Commit("feature work")
	repo.WriteFile("c.txt", "dirty") // untracked, so the stats path runs

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	sem := newFleetLimiter(fleetConcurrency())

	counter := newGitCallCounter(t)
	if _, err := gatherFleetRepo(ctx, runner, "repo", repo.Dir, repo.Dir, sem, true); err != nil {
		t.Fatalf("first gather: %v", err)
	}
	// The first poll must actually pay for these, or the second poll's zero
	// would prove nothing.
	for _, probe := range []string{"symbolic-ref", "--get-regexp", "diff"} {
		if counter.get(probe) == 0 {
			t.Fatalf("first poll never ran %q — the test no longer exercises it", probe)
		}
	}

	counter.reset()
	if _, err := gatherFleetRepo(ctx, runner, "repo", repo.Dir, repo.Dir, sem, true); err != nil {
		t.Fatalf("second gather: %v", err)
	}

	for _, probe := range []struct{ arg, why string }{
		{"symbolic-ref", "the trunk is keyed on origin/HEAD and the ref files"},
		{"--get-regexp", "the gk-parent map is keyed on the config files"},
		{"diff", "the diff stats are keyed on the changed files' fingerprints"},
		{"rev-list", "parent divergence is keyed on the two commit tips"},
		{"--is-ancestor", "land-readiness is keyed on the two commit tips"},
	} {
		if n := counter.get(probe.arg); n != 0 {
			t.Errorf("second poll ran %q %d times; %s, so it cannot have changed", probe.arg, n, probe.why)
		}
	}
}

// TestGatherFleetRepo_MovedTipReprobes is the other half: a memoised answer has
// to stop being served the moment its inputs move. Without this, the test above
// is satisfied by a cache that never invalidates.
func TestGatherFleetRepo_MovedTipReprobes(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	base := strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD"))
	repo.AddRemote("origin", repo.Dir)
	repo.SetRemoteHEAD("origin", base)
	repo.CreateBranch("feature")
	repo.Checkout("feature")
	repo.WriteFile("b.txt", "b")
	repo.Commit("feature work")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	sem := newFleetLimiter(fleetConcurrency())

	if _, err := gatherFleetRepo(ctx, runner, "repo", repo.Dir, repo.Dir, sem, false); err != nil {
		t.Fatalf("first gather: %v", err)
	}

	repo.WriteFile("d.txt", "d")
	repo.Commit("moves the branch tip")

	counter := newGitCallCounter(t)
	if _, err := gatherFleetRepo(ctx, runner, "repo", repo.Dir, repo.Dir, sem, false); err != nil {
		t.Fatalf("second gather: %v", err)
	}
	if n := counter.get("--is-ancestor"); n == 0 {
		t.Error("land-readiness was served from cache after the branch tip moved")
	}
}

// TestChangeSnapshot_CleanTreeRunsNoDiff covers the single-worktree feed: with
// nothing dirty there is no diff to take, yet this path used to fork two on
// every tick — once per keystroke's worth of fs events.
func TestChangeSnapshot_CleanTreeRunsNoDiff(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")

	counter := newGitCallCounter(t)
	sigs := changeSnapshot(context.Background(), &git.ExecRunner{Dir: repo.Dir}, repo.Dir)
	if len(sigs) != 0 {
		t.Fatalf("clean tree produced %d signatures: %v", len(sigs), sigs)
	}
	if n := counter.get("diff"); n != 0 {
		t.Errorf("clean tree ran git diff %d times, want 0", n)
	}
}

// TestChangeSnapshot_UnchangedDirtySetReusesStats is the dirty counterpart: the
// fingerprints the snapshot already collected decide whether the counts can
// have moved, so an untouched dirty set costs the status call and nothing else.
func TestChangeSnapshot_UnchangedDirtySetReusesStats(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a\n")
	repo.Commit("init")
	repo.WriteFile("a.txt", "a\nb\n")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	first := changeSnapshot(ctx, runner, repo.Dir)
	if first["a.txt"].added != 1 {
		t.Fatalf("first snapshot stats = %+v, want added=1", first["a.txt"])
	}

	counter := newGitCallCounter(t)
	second := changeSnapshot(ctx, runner, repo.Dir)
	if n := counter.get("diff"); n != 0 {
		t.Errorf("unchanged dirty set ran git diff %d times, want 0", n)
	}
	if second["a.txt"].added != first["a.txt"].added || second["a.txt"].removed != first["a.txt"].removed {
		t.Errorf("reused stats = %+v, want %+v", second["a.txt"], first["a.txt"])
	}
}

// TestPollGate pins the spacing rule itself: the first poll is never delayed,
// and later ones wait out whatever remains of the gap.
func TestPollGate(t *testing.T) {
	g := pollGate{gap: time.Second}
	base := time.Now()
	if w := g.wait(base); w != 0 {
		t.Fatalf("a gate that has never started must not delay, got %v", w)
	}
	g.start(base)
	if w := g.wait(base.Add(400 * time.Millisecond)); w != 600*time.Millisecond {
		t.Errorf("wait 400ms into a 1s gap = %v, want 600ms", w)
	}
	if w := g.wait(base.Add(time.Second)); w != 0 {
		t.Errorf("wait at the gap boundary = %v, want 0", w)
	}
	if w := g.wait(base.Add(3 * time.Second)); w != 0 {
		t.Errorf("wait past the gap = %v, want 0", w)
	}
}

// TestRunFleetEvents_FSChurnDoesNotOutpaceInterval covers the --events stream,
// which used to re-gather the instant a file landed. The interval is the user's
// cost budget; filesystem events buy latency inside it, never extra polls. One
// worktree under a steady stream of edits was enough to chain full fleet scans back to
// back, and a stream has no watching human to notice.
//
// The heartbeat is minutes away while a watcher is live, so every poll after
// the baseline here is necessarily fs-driven — which is what makes the spacing
// assertion mean something.
func TestRunFleetEvents_FSChurnDoesNotOutpaceInterval(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	entries := []fleetEntryJSON{{Path: repo.Dir, Branch: "main", Current: true}}

	// Filesystem delivery is environment-dependent, and the watcher coalesces
	// with a debounce, so prove an edit actually surfaces as one signal before
	// asserting on signal-driven behaviour.
	probe, ok := newFSWatcher(context.Background(), &git.ExecRunner{Dir: repo.Dir}, fsWatchDebounce, 0)
	if !ok {
		t.Skip("no filesystem watcher available here — the fs-driven path cannot be exercised")
	}
	repo.WriteFile("churn.txt", "probe")
	select {
	case <-probe.events:
		probe.Close()
	case <-time.After(3 * time.Second):
		probe.Close()
		t.Skip("filesystem events are not delivered here — the fs-driven path cannot be exercised")
	}

	const interval = 700 * time.Millisecond
	var mu sync.Mutex
	var starts []time.Time
	gather := func(context.Context) ([]fleetEntryJSON, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return entries, nil
	}

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- runFleetEvents(ctx, cmd, gather, interval, nil) }()

	// Edit slower than the watcher's debounce: every write inside it resets the
	// timer, so a tighter loop emits no signal at all and the churn would never
	// reach the loop being measured. Each write then lands as its own wake-up,
	// well inside the interval.
	edit := fsWatchDebounce + 50*time.Millisecond
	for i := 0; i < 10; i++ {
		repo.WriteFile("churn.txt", strconv.Itoa(i))
		time.Sleep(edit)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(starts) < 2 {
		t.Fatalf("only %d gather(s) — the churn never reached the loop, so nothing was measured", len(starts))
	}
	// Timer granularity, not a tolerance for extra polls: with the rate limit
	// gone the gaps collapse to the duration of a gather, far below this.
	const slack = 50 * time.Millisecond
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < interval-slack {
			t.Fatalf("poll %d started %v after the previous one, want at least %v (%d polls in %v)",
				i, gap, interval, len(starts), starts[len(starts)-1].Sub(starts[0]))
		}
	}
	t.Logf("%d polls over %v under a steady stream of edits", len(starts), starts[len(starts)-1].Sub(starts[0]))
}

// TestFetchHeadInfo_UnmovedRefsReuseHeader covers the single-worktree feed's
// header: branch, upstream, divergence and HEAD subject move only when a ref
// moves, while the feed refreshes at the rate someone types.
func TestFetchHeadInfo_UnmovedRefsReuseHeader(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	runner := &git.ExecRunner{Dir: repo.Dir}

	// Two warm-up reads: the first has no previous branch name to stamp into
	// the key, so the settled key only appears on the second.
	settled := fetchHeadInfo(cmd, runner, fetchHeadInfo(cmd, runner, headInfo{}))
	if settled.branch == "" || settled.sha == "" {
		t.Fatalf("header did not resolve: %+v", settled)
	}

	counter := newGitCallCounter(t)
	reused := fetchHeadInfo(cmd, runner, settled)
	for _, probe := range []string{"symbolic-ref", "rev-parse", "log"} {
		if n := counter.get(probe); n != 0 {
			t.Errorf("unmoved refs still ran %q %d times, want 0", probe, n)
		}
	}
	if reused.branch != settled.branch || reused.sha != settled.sha || reused.subject != settled.subject {
		t.Errorf("reused header = %+v, want %+v", reused, settled)
	}

	repo.WriteFile("b.txt", "b")
	repo.Commit("moves HEAD")
	counter.reset()
	moved := fetchHeadInfo(cmd, runner, reused)
	if counter.get("symbolic-ref") == 0 {
		t.Error("header was served from cache after HEAD moved")
	}
	if moved.sha == settled.sha {
		t.Errorf("header sha stayed %q across a commit", moved.sha)
	}

	// A branch switch rewrites HEAD and touches no ref file, so HEAD is the
	// only part of the fingerprint that can notice it.
	repo.CreateBranch("other")
	repo.Checkout("other")
	counter.reset()
	switched := fetchHeadInfo(cmd, runner, moved)
	if switched.branch != "other" {
		t.Errorf("header branch = %q after switching to other, want other", switched.branch)
	}
}

// TestChangeWatchModel_FSBurstsRespectInterval is the same rule on the
// single-worktree feed: a burst of filesystem signals inside the interval is
// one refresh, not one per signal.
func TestChangeWatchModel_FSBurstsRespectInterval(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	m := newChangeWatchModel(cmd, time.Second)
	base := time.Now()
	m.now = func() time.Time { return base }

	m.Update(changeFSMsg{})
	if !m.refreshing {
		t.Fatal("the first fs signal must start a refresh")
	}

	m.refreshing = false // that refresh landed
	m.Update(changeFSMsg{})
	if m.refreshing {
		t.Error("a second fs signal inside the interval started another refresh")
	}

	m.now = func() time.Time { return base.Add(2 * time.Second) }
	m.Update(changeFSMsg{})
	if !m.refreshing {
		t.Error("past the interval the next fs signal must refresh")
	}
}

// TestChangeSnapshot_SameMtimeDifferentSizeReprobes guards the one input the
// diff-stats fingerprint cannot read directly: file content. mtime alone is
// only as fine as the filesystem's timestamps, so two saves inside one tick
// would otherwise serve the first one's counts forever. Size is what separates
// them, and os.Chtimes reproduces the coarse-timestamp case on a filesystem
// that is too precise to hit it naturally.
func TestChangeSnapshot_SameMtimeDifferentSizeReprobes(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a\n")
	repo.Commit("init")
	path := filepath.Join(repo.Dir, "a.txt")

	repo.WriteFile("a.txt", "a\nb\n")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	frozen := fi.ModTime()

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	first := changeSnapshot(ctx, runner, repo.Dir)
	if first["a.txt"].added != 1 {
		t.Fatalf("first snapshot = %+v, want added=1", first["a.txt"])
	}

	repo.WriteFile("a.txt", "a\nb\nc\nd\n")
	if err := os.Chtimes(path, frozen, frozen); err != nil {
		t.Fatal(err)
	}
	second := changeSnapshot(ctx, runner, repo.Dir)
	if second["a.txt"].added != 3 {
		t.Errorf("after a same-mtime edit the stats read %+v, want added=3 — the fingerprint missed the content change",
			second["a.txt"])
	}
}

// TestFetchHeadInfo_UpstreamChangeReprobes covers the one header input that
// lives in config rather than in a ref: `git branch --set-upstream-to` rewrites
// branch.<name>.remote/.merge and touches nothing under refs/, so a fingerprint
// built only from ref files would keep reporting the old upstream.
func TestFetchHeadInfo_UpstreamChangeReprobes(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	base := strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD"))
	repo.AddRemote("origin", repo.Dir)
	repo.RunGit("update-ref", "refs/remotes/origin/"+base, "HEAD")
	repo.RunGit("update-ref", "refs/remotes/origin/other", "HEAD")
	repo.RunGit("branch", "--set-upstream-to=origin/"+base)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	runner := &git.ExecRunner{Dir: repo.Dir}

	settled := fetchHeadInfo(cmd, runner, fetchHeadInfo(cmd, runner, headInfo{}))
	if settled.upstream != "origin/"+base {
		t.Fatalf("upstream = %q, want origin/%s", settled.upstream, base)
	}

	repo.RunGit("branch", "--set-upstream-to=origin/other")
	after := fetchHeadInfo(cmd, runner, settled)
	if after.upstream != "origin/other" {
		t.Errorf("upstream = %q after re-pointing it, want origin/other", after.upstream)
	}
}

// countMemoScopes reports how many of a memo's scopes belong to one repository.
// Reaching into the sync.Map is the point: the entry count IS the property
// under test. The filter matters because these memos are process-global, so an
// unfiltered count would also see every other test in the package.
func countMemoScopes(m *sync.Map, mark string) int {
	n := 0
	m.Range(func(k, _ any) bool {
		if scope, ok := k.(string); ok && strings.Contains(scope, mark) {
			n++
		}
		return true
	})
	return n
}

// TestGatherFleetRepo_CachesStayBoundedAcrossCommits is the property that makes
// these caches safe in a process meant to stay up for days: a moved commit tip
// REPLACES a scope's entry rather than adding one beside it. Keyed by the tips
// themselves, every commit would leave its predecessor behind forever.
func TestGatherFleetRepo_CachesStayBoundedAcrossCommits(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	base := strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD"))
	repo.AddRemote("origin", repo.Dir)
	repo.SetRemoteHEAD("origin", base)
	repo.CreateBranch("feature")
	repo.Checkout("feature")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	sem := newFleetLimiter(fleetConcurrency())

	// t.TempDir() embeds this test's name, so the directory segment is a mark
	// no other test's scopes can carry.
	mark := filepath.Base(filepath.Dir(repo.Dir))

	const commits = 6
	for i := range commits {
		repo.WriteFile("b.txt", strconv.Itoa(i))
		repo.Commit("move the tip")
		if _, err := gatherFleetRepo(ctx, runner, "repo", repo.Dir, repo.Dir, sem, true); err != nil {
			t.Fatalf("gather %d: %v", i, err)
		}
	}

	for _, c := range []struct {
		name string
		n    int
	}{
		{"fleetParentBehind", countMemoScopes(&fleetParentBehind.m, mark)},
		{"fleetLandReady", countMemoScopes(&fleetLandReady.m, mark)},
		{"defaultBranchCache", countMemoScopes(&defaultBranchCache.m, mark)},
		{"forkPointCache", countMemoScopes(&forkPointCache.m, mark)},
	} {
		// One worktree and one repo here, so every scope this repo can produce
		// is a small constant. Anything near the commit count means entries are
		// accumulating per commit instead of being replaced.
		if c.n >= commits {
			t.Errorf("%s holds %d entries after %d commits — scopes are accumulating, not replacing",
				c.name, c.n, commits)
		}
	}
}

// TestDefaultBranchKeyIgnoresCommits pins the fingerprint's sensitivity. A
// commit rewrites a ref through a lockfile and a rename, which moves the
// refs/heads directory's mtime — fingerprinting that directory discarded the
// trunk on every commit and re-forked symbolic-ref for a name that never moved.
func TestDefaultBranchKeyIgnoresCommits(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	repo.AddRemote("origin", repo.Dir)
	repo.SetRemoteHEAD("origin", strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD")))

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	_, before := defaultBranchKey(ctx, runner)

	repo.WriteFile("b.txt", "b")
	repo.Commit("a commit that renames nothing")
	if _, after := defaultBranchKey(ctx, runner); after != before {
		t.Errorf("a commit changed the trunk fingerprint:\n before %q\n after  %q", before, after)
	}

	// Re-pointing origin/HEAD is the change that must still be seen.
	repo.CreateBranch("trunk2")
	repo.SetRemoteHEAD("origin", "trunk2")
	if _, after := defaultBranchKey(ctx, runner); after == before {
		t.Error("re-pointing origin/HEAD left the trunk fingerprint unchanged")
	}
}

// TestRepoRootAndCommonDir_ReplacedRepo checks that the fleet's layout cache
// answers for the repository actually at a path. The scenario is a linked
// worktree removed while its common dir survives in the main repo, then a
// different repository created at that same path — the fleet would otherwise
// report the old repo's layout for the rest of the process.
func TestRepoRootAndCommonDir_ReplacedRepo(t *testing.T) {
	main := testutil.NewRepo(t)
	main.WriteFile("a.txt", "a")
	main.Commit("init")

	linked := filepath.Join(t.TempDir(), "linked")
	main.RunGit("worktree", "add", "-q", linked, "-b", "feat")

	ctx := context.Background()
	_, firstCommon, ok := repoRootAndCommonDir(ctx, linked)
	if !ok {
		t.Fatal("the linked worktree did not resolve")
	}

	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	replacement := testutil.NewRepo(t) // its own repo, elsewhere
	replacement.WriteFile("b.txt", "b")
	replacement.Commit("init")
	if err := os.Rename(replacement.Dir, linked); err != nil {
		t.Skipf("could not move a repo onto the old path: %v", err)
	}

	_, secondCommon, ok := repoRootAndCommonDir(ctx, linked)
	if !ok {
		t.Fatal("the replacement repo did not resolve")
	}
	if secondCommon == firstCommon {
		t.Errorf("still reporting the old common dir %q for a replaced repo", secondCommon)
	}
}

// TestResolveDefaultBranch_FailureIsNotMemoised is the counterpart to the
// fingerprint being deliberately insensitive to commits: nothing in it moves
// when a probe merely FAILS, so memoising a failure keeps it for the life of
// the process. gatherFleetMulti gives each repo a 3s budget, and an empty trunk
// switches off the fork column, ParentBehind and land-readiness for that whole
// repository — one slow poll must not cost the rest of the session.
func TestResolveDefaultBranch_FailureIsNotMemoised(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	want := strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD"))
	repo.AddRemote("origin", repo.Dir)
	repo.SetRemoteHEAD("origin", want)
	runner := &git.ExecRunner{Dir: repo.Dir}

	// Prime the layout cache first, so the dead context can only fail the trunk
	// probe itself rather than the lookup that builds its key.
	repoRootAndCommonDir(context.Background(), repo.Dir)

	dead, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if got := resolveDefaultBranchForWorktree(dead, runner); got != "" {
		t.Fatalf("a dead context resolved a trunk %q — the test no longer exercises a failure", got)
	}

	if got := resolveDefaultBranchForWorktree(context.Background(), runner); got != want {
		t.Errorf("trunk = %q after a healthy poll, want %q — the failure was memoised", got, want)
	}
}

// TestResolveDefaultBranch_TrunklessRepoIsMemoised is the other half: a
// repository that genuinely has no trunk is a real answer and must be kept, or
// every poll re-forks all three probes for it forever.
func TestResolveDefaultBranch_TrunklessRepoIsMemoised(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	repo.RunGit("branch", "-m", "trunkless") // no origin/HEAD, no main, no master
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	if got := resolveDefaultBranchForWorktree(ctx, runner); got != "" {
		t.Fatalf("trunk = %q, want empty for a repo with no origin/HEAD and no main/master", got)
	}

	counter := newGitCallCounter(t)
	if got := resolveDefaultBranchForWorktree(ctx, runner); got != "" {
		t.Errorf("trunk = %q on the second call, want empty", got)
	}
	for _, probe := range []string{"symbolic-ref", "rev-parse"} {
		if n := counter.get(probe); n != 0 {
			t.Errorf("a settled trunkless answer re-ran %q %d times, want 0", probe, n)
		}
	}
}

// TestDefaultBranchKeySeesTrunkRenamedIntoADirectory covers the one transition
// plain existence cannot see. Renaming main away and creating main/x turns
// refs/heads/main from a loose ref FILE into a DIRECTORY holding only the
// sub-ref — os.Stat succeeds either way, so the fingerprint would not move
// while the answer had become "no trunk".
func TestDefaultBranchKeySeesTrunkRenamedIntoADirectory(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	if strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "HEAD")) != "main" {
		repo.RunGit("branch", "-m", "main")
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	if got := resolveDefaultBranchForWorktree(ctx, runner); got != "main" {
		t.Fatalf("trunk = %q, want main", got)
	}
	_, before := defaultBranchKey(ctx, runner)

	// Both halves inside one poll interval: main goes away, main/x arrives.
	repo.RunGit("branch", "-m", "main", "other")
	repo.RunGit("branch", "main/x")

	if _, after := defaultBranchKey(ctx, runner); after == before {
		t.Error("the fingerprint did not move when refs/heads/main became a directory")
	}
	if got := resolveDefaultBranchForWorktree(ctx, runner); got == "main" {
		t.Error("trunk still reads main, but that branch no longer exists")
	}
}
