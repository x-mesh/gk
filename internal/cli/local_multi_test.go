package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// newLocalFlagsCmd carries the flags resolveLocalRepos and localExitCode read.
func newLocalFlagsCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "local"}
	cmd.Flags().StringSlice("scan", nil, "")
	cmd.Flags().StringSlice("repos", nil, "")
	cmd.Flags().Int("depth", 0, "")
	cmd.Flags().Bool("exit-code", false, "")
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// isolateLocalConfig keeps the user's fleet.* settings out of the test.
func isolateLocalConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func initRepoAt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}

func targetLabels(set *localRepoSet) []string {
	var out []string
	for _, tg := range set.Targets {
		out = append(out, tg.Label)
	}
	sort.Strings(out)
	return out
}

func TestResolveLocalRepos_DepthAndAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	isolateLocalConfig(t)
	root := t.TempDir()
	for _, rel := range []string{"b", "a/x", "dup1/app", "dup2/app", "c/d/e"} {
		initRepoAt(t, filepath.Join(root, rel))
	}
	prevRepo := flagRepo
	t.Cleanup(func() { flagRepo = prevRepo })
	flagRepo = root
	withAgentMode(t, false)
	ctx := context.Background()

	// Outside a repo with no flag: scan the current directory, depth 2.
	set, err := resolveLocalRepos(ctx, newLocalFlagsCmd(t))
	if err != nil {
		t.Fatal(err)
	}
	if set == nil || !set.Auto || set.Depth != 2 {
		t.Fatalf("want an automatic depth-2 scan, got %+v", set)
	}
	want := []string{"a/x", "b", "dup1/app", "dup2/app"}
	if got := targetLabels(set); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("labels: want %v, got %v (same-named repos must stay apart, depth 3 must be skipped)", want, got)
	}

	// --depth reaches the deeper repo.
	set, err = resolveLocalRepos(ctx, newLocalFlagsCmd(t, "--depth", "3"))
	if err != nil {
		t.Fatal(err)
	}
	if got := targetLabels(set); len(got) != 5 {
		t.Errorf("--depth 3: want 5 repos, got %v", got)
	}

	// Inside a repo with no flag: stays single-repo.
	flagRepo = filepath.Join(root, "b")
	if set, err = resolveLocalRepos(ctx, newLocalFlagsCmd(t)); err != nil || set != nil {
		t.Errorf("inside a repo: want single-repo (nil, nil), got %+v, %v", set, err)
	}

	// --repos naming a non-repo is an error, not a silent skip.
	if _, err = resolveLocalRepos(ctx, newLocalFlagsCmd(t, "--repos", t.TempDir())); err == nil {
		t.Error("--repos with a non-repo path must fail")
	}

	// Agent mode outside a repo: the not-a-repo error, with the scan remedy.
	flagRepo = root
	withAgentMode(t, true)
	_, err = resolveLocalRepos(ctx, newLocalFlagsCmd(t))
	if err == nil {
		t.Fatal("agent mode outside a repo must not scan implicitly")
	}
	if code := errorCodeFromError(err); code != "not-a-repo" {
		t.Errorf("want error code not-a-repo, got %q", code)
	}
	rem := RemediesFrom(err)
	if len(rem) == 0 || !strings.Contains(rem[0].Command, "local --scan .") || rem[0].Safety != "safe" {
		t.Errorf("want a safe `local --scan .` remedy, got %+v", rem)
	}
	// An explicit --scan still works for an agent.
	if set, err = resolveLocalRepos(ctx, newLocalFlagsCmd(t, "--scan", root)); err != nil || set == nil || set.Auto {
		t.Errorf("agent mode with --scan: want an explicit scan, got %+v, %v", set, err)
	}
}

// newPushedRepo is a repo with a remote and nothing local-only.
func newPushedRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	r := testutil.NewRepo(t)
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	r.RunGit("remote", "add", "origin", bareDir)
	r.RunGit("push", "-q", "-u", "origin", "main")
	return r
}

func TestLocalMulti_CountsAndExitCode(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	clean := newPushedRepo(t)
	stranded := newPushedRepo(t)
	stranded.WriteFile("n.txt", "n\n")
	stranded.Commit("not pushed")

	set := &localRepoSet{Targets: []localRepoTarget{
		{Label: "clean", Root: clean.Dir},
		{Label: "stranded", Root: stranded.Dir},
	}}
	var done atomic.Int64
	res, err := collectLocalMulti(context.Background(), set, 10, &done)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts.Repos != 2 || res.Counts.NeedAction != 1 || res.Counts.Clean != 1 || res.Clean {
		t.Errorf("want 2 repos, 1 need action, 1 clean, overall not clean; got %+v clean=%v", res.Counts, res.Clean)
	}
	if done.Load() != 2 {
		t.Errorf("progress counter: want 2, got %d", done.Load())
	}

	on := newLocalFlagsCmd(t, "--exit-code")
	if code, rendered := ExitCodeFor(localExitCode(on, res.Clean)); code != 1 || !rendered {
		t.Errorf("--exit-code with stranded work: want exit 1 without an extra message, got %d rendered=%v", code, rendered)
	}
	if err := localExitCode(on, true); err != nil {
		t.Errorf("--exit-code when clean: want exit 0, got %v", err)
	}
	if err := localExitCode(newLocalFlagsCmd(t), false); err != nil {
		t.Errorf("without --exit-code the exit code must not change, got %v", err)
	}
}

// The collectors turn per-branch errors into zero counts, so a deadline that
// fires midway can return a report that says clean. The run must not.
func TestLocalMulti_TimeoutIsNotClean(t *testing.T) {
	prevFn, prevTimeout := localCollectFn, localRepoTimeout
	t.Cleanup(func() { localCollectFn, localRepoTimeout = prevFn, prevTimeout })
	localRepoTimeout = 20 * time.Millisecond
	localCollectFn = func(ctx context.Context, _ string, _ bool, _ int) (localReport, error) {
		<-ctx.Done()
		return localReport{Clean: true}, nil
	}

	set := &localRepoSet{Targets: []localRepoTarget{{Label: "slow", Root: t.TempDir()}}}
	var done atomic.Int64
	res, err := collectLocalMulti(context.Background(), set, 10, &done)
	if err != nil {
		t.Fatal(err)
	}
	if res.Clean || res.Counts.Unknown != 1 || res.Repos[0].Report != nil ||
		!strings.Contains(res.Repos[0].Error, "timed out") {
		t.Errorf("a repo past its deadline must be an unknown error entry, got %+v", res)
	}
}

// A scan stopped from outside, such as by Ctrl-C, has no verdict: the
// repositories it checked must not come back as a partial, clean-looking
// result.
func TestLocalMulti_InterruptedScanFails(t *testing.T) {
	prevFn := localCollectFn
	t.Cleanup(func() { localCollectFn = prevFn })
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	localCollectFn = func(rctx context.Context, _ string, _ bool, _ int) (localReport, error) {
		// The first repository is clean; the scan is stopped during the
		// second one, and the ones after it wait for the stop.
		switch calls.Add(1) {
		case 1:
		case 2:
			cancel()
			<-rctx.Done()
		default:
			<-rctx.Done()
		}
		return localReport{UnpushedKnown: true, Clean: true}, nil
	}

	set := &localRepoSet{}
	for i := 0; i < 20; i++ {
		set.Targets = append(set.Targets, localRepoTarget{Label: "r", Root: t.TempDir()})
	}
	var done atomic.Int64
	res, err := collectLocalMulti(ctx, set, 10, &done)
	if err == nil || !strings.Contains(err.Error(), "scan interrupted") {
		t.Fatalf("an interrupted scan must fail with scan interrupted, got %v", err)
	}
	if res.Repos != nil || res.Clean {
		t.Errorf("an interrupted scan must return no result, got %+v", res)
	}
	if n := calls.Load(); n >= 20 {
		t.Errorf("the scan went on to every repository after the interrupt: %d calls", n)
	}
}

// The goroutines of a scan stay at the concurrency limit, whatever the number
// of repositories it found.
func TestLocalMulti_GoroutinesStayAtTheLimit(t *testing.T) {
	prevFn := localCollectFn
	t.Cleanup(func() { localCollectFn = prevFn })
	release := make(chan struct{})
	var running atomic.Int64
	localCollectFn = func(context.Context, string, bool, int) (localReport, error) {
		running.Add(1)
		<-release
		return localReport{UnpushedKnown: true, Clean: true}, nil
	}

	const repos = 500
	set := &localRepoSet{}
	for i := 0; i < repos; i++ {
		set.Targets = append(set.Targets, localRepoTarget{Label: "r", Root: "/nonexistent"})
	}
	before := runtime.NumGoroutine()
	errc := make(chan error, 1)
	go func() {
		var done atomic.Int64
		_, err := collectLocalMulti(context.Background(), set, 10, &done)
		errc <- err
	}()
	limit := int64(fleetConcurrency())
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() < limit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if running.Load() != limit {
		t.Fatalf("want %d repositories checked at once, got %d", limit, running.Load())
	}
	// The scan itself, one goroutine per slot, and their timers; far from
	// one per repository.
	if extra := runtime.NumGoroutine() - before; extra > 4*int(limit)+8 {
		t.Errorf("a scan of %d repositories with a limit of %d started %d goroutines", repos, limit, extra)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

// `gk local --json` outside a repository prints the multi-repo result, and
// --exit-code still follows its verdict.
func TestRunLocalMulti_JSON(t *testing.T) {
	prevFn := localCollectFn
	t.Cleanup(func() { localCollectFn = prevFn })
	localCollectFn = func(_ context.Context, dir string, all bool, _ int) (localReport, error) {
		if !all {
			t.Errorf("a multi-repo run must check the repo scope")
		}
		if filepath.Base(dir) == "busy" {
			return localReport{Scope: "repo", UnpushedKnown: true, Branches: []localBranchReport{{Branch: "feat/x", Unpushed: 2}}}, nil
		}
		return localReport{Scope: "repo", UnpushedKnown: true, Clean: true}, nil
	}
	withAgentMode(t, false)
	flagJSON = true

	root := t.TempDir()
	set := &localRepoSet{Root: root, Depth: 2, Auto: true, Targets: []localRepoTarget{
		{Label: "quiet", Root: filepath.Join(root, "quiet")},
		{Label: "busy", Root: filepath.Join(root, "busy")},
	}}
	cmd := newLocalFlagsCmd(t, "--exit-code")
	cmd.Flags().Int("limit", 10, "")
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runLocalMulti(cmd, set)
	if code, _ := ExitCodeFor(err); code != 1 {
		t.Errorf("--exit-code with stranded work: want exit 1, got %v", err)
	}
	var got localMultiResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("the output is not the bare JSON result: %v\n%s", err, buf.String())
	}
	if got.Scope != "multi" || got.Root != root || got.Depth != 2 || !got.Auto || got.Clean {
		t.Errorf("header fields: got %+v", got)
	}
	if got.Counts != (localMultiCounts{Repos: 2, NeedAction: 1, Clean: 1}) {
		t.Errorf("counts: got %+v", got.Counts)
	}
	// Every repository is in the JSON, the clean one too, in the order found.
	if len(got.Repos) != 2 || got.Repos[0].Repo != "quiet" || got.Repos[1].Repo != "busy" ||
		got.Repos[1].Report == nil || len(got.Repos[1].Report.Branches) != 1 {
		t.Errorf("repos: got %+v", got.Repos)
	}
}

func TestLocalMulti_TextOnlyStranded(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })

	res := localMultiResult{
		Scope: "multi", Root: "/work", Depth: 2, Auto: true,
		Repos: []localMultiRepo{
			{Repo: "group/busy", Path: "/work/group/busy", Report: &localReport{UnpushedKnown: true, Branches: []localBranchReport{
				{Branch: "feat/x", Unpushed: 2},
				{Branch: "(detached)", Detached: true, Operation: "rebase 1/3", Resume: "gk continue", Worktree: "/wt/busy-wt"},
			}}},
			{Repo: "group/quiet", Path: "/work/group/quiet", Report: &localReport{UnpushedKnown: true, Clean: true}},
			{Repo: "noremote", Path: "/work/noremote", Report: &localReport{Branches: []localBranchReport{
				{Branch: "main", NoUpstream: true, Unstaged: 5, Worktree: "/work/noremote"},
			}}},
			{Repo: "slow", Path: "/work/slow", Error: "timed out after 10s"},
			{Repo: "solo", Path: "/work/solo", Report: &localReport{UnpushedKnown: true, Branches: []localBranchReport{
				{Branch: "main", Unstaged: 1, Worktree: "/work/solo"},
			}}},
		},
		Counts: localMultiCounts{Repos: 5, NeedAction: 3, Clean: 1, Unknown: 1},
	}
	var buf bytes.Buffer
	renderLocalMulti(&buf, res, 10)
	out := buf.String()
	lines := strings.Split(out, "\n")

	if !strings.HasPrefix(lines[0], "LOCAL  5 repos · 3 need action · 1 clean · 1 unknown") ||
		!strings.Contains(lines[0], "(outside a repo — /work, depth 2)") {
		t.Errorf("the counts and the scan root lead the output, got %q", lines[0])
	}
	lineWith := func(subs ...string) string {
		for _, l := range lines {
			ok := true
			for _, s := range subs {
				ok = ok && strings.Contains(l, s)
			}
			if ok {
				return l
			}
		}
		return ""
	}
	// A repo with one row fits on one line: marker, repo, branch, state.
	if l := lineWith("● solo", "main", "1 unstaged"); l == "" || strings.Contains(l, "(solo)") {
		t.Errorf("solo: want one line without the redundant worktree name, got %q\n%s", l, out)
	}
	// Several rows: the repo line counts the branches, the rows follow.
	if lineWith("● group/busy", "2 branches") == "" || lineWith("feat/x", "2 unpushed") == "" ||
		lineWith("(detached)", "rebase 1/3 → gk continue", "(busy-wt)") == "" {
		t.Errorf("busy: want a branch count line and both rows:\n%s", out)
	}
	// No remote at all: say so once, not "no upstream" on every branch.
	if l := lineWith("noremote", "no remote · 5 unstaged"); l == "" || strings.Contains(l, "no upstream") {
		t.Errorf("noremote: want `no remote · 5 unstaged`, got %q\n%s", l, out)
	}
	if lineWith("● slow", "state unknown: timed out after 10s") == "" {
		t.Errorf("slow: want the error on the repo line:\n%s", out)
	}
	// Path order is kept: the scan order, not a regrouping by severity.
	if strings.Index(out, "group/busy") > strings.Index(out, "noremote") ||
		strings.Index(out, "noremote") > strings.Index(out, "slow") ||
		strings.Index(out, "slow") > strings.Index(out, "solo") {
		t.Errorf("repos must keep scan order:\n%s", out)
	}
	if strings.Contains(out, "group/quiet") {
		t.Errorf("a clean repo must be counted, not listed:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("no-color output must carry no escape codes:\n%q", out)
	}
}

// A repo with more stranded rows than --limit shows the first rows and counts
// the rest. With no remote, "no remote" is said once on the repo line, not on
// every row.
func TestLocalMulti_RowLimitAndNoRemoteOnce(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })

	res := localMultiResult{
		Scope: "multi",
		Repos: []localMultiRepo{
			{Repo: "many", Path: "/work/many", Report: &localReport{Stash: 1, Branches: []localBranchReport{
				{Branch: "b1", NoUpstream: true, Unstaged: 1},
				{Branch: "b2", NoUpstream: true, Staged: 2},
				{Branch: "b3", NoUpstream: true, Unstaged: 3},
				{Branch: "b4", NoUpstream: true, Unstaged: 4},
			}}},
		},
		Counts: localMultiCounts{Repos: 1, NeedAction: 1},
	}
	var buf bytes.Buffer
	renderLocalMulti(&buf, res, 2)
	out := buf.String()

	if !strings.Contains(out, "● many") || !strings.Contains(out, "4 branches · no remote") {
		t.Errorf("want the repo line to count the branches and say no remote once:\n%s", out)
	}
	if n := strings.Count(out, "no remote"); n != 1 {
		t.Errorf("want `no remote` once, got %d times:\n%s", n, out)
	}
	if strings.Contains(out, "no upstream") {
		t.Errorf("no upstream must not show in a repo with no remote:\n%s", out)
	}
	// Five rows (four branches and the stash) with a limit of 2: two are
	// shown, three are counted, and nothing after the limit is printed.
	for _, want := range []string{"b1", "1 unstaged", "b2", "2 staged", "… and 3 more"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"b3", "b4", "(stash)"} {
		if strings.Contains(out, gone) {
			t.Errorf("row %q is past the limit and must not print:\n%s", gone, out)
		}
	}

	// With no limit every row prints.
	buf.Reset()
	renderLocalMulti(&buf, res, 0)
	if out := buf.String(); !strings.Contains(out, "b4") || !strings.Contains(out, "(stash)") || strings.Contains(out, "more") {
		t.Errorf("a limit of 0 prints every row:\n%s", out)
	}
}

func TestLocalLevelOf(t *testing.T) {
	cases := []struct {
		name string
		repo localMultiRepo
		want localRepoLevel
	}{
		{"error", localMultiRepo{Error: "x"}, localLevelUnverified},
		{"stash", localMultiRepo{Report: &localReport{Stash: 1}}, localLevelRisk},
		{"unpushed", localMultiRepo{Report: &localReport{Branches: []localBranchReport{{Unpushed: 1}}}}, localLevelRisk},
		{"paused", localMultiRepo{Report: &localReport{Branches: []localBranchReport{{Operation: "merge"}}}}, localLevelRisk},
		{"uncommitted", localMultiRepo{Report: &localReport{Branches: []localBranchReport{{Unstaged: 2}}}}, localLevelUncommitted},
		{"missing worktree", localMultiRepo{Report: &localReport{Branches: []localBranchReport{{Unknown: true}}}}, localLevelUnverified},
	}
	for _, c := range cases {
		if got := localLevelOf(c.repo); got != c.want {
			t.Errorf("%s: want level %d, got %d", c.name, c.want, got)
		}
	}
}
