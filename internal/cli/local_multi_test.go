package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestLocalMulti_TextOnlyStranded(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })

	res := localMultiResult{
		Scope: "multi", Root: "/work", Depth: 2, Auto: true,
		Repos: []localMultiRepo{
			{Repo: "group/quiet", Report: &localReport{UnpushedKnown: true, Clean: true}},
			{Repo: "group/busy", Report: &localReport{UnpushedKnown: true, Branches: []localBranchReport{
				{Branch: "feat/x", Unpushed: 2},
				{Branch: "(detached)", Detached: true, Operation: "rebase 1/3", Resume: "gk continue"},
			}}},
			{Repo: "slow", Error: "timed out after 10s"},
		},
		Counts: localMultiCounts{Repos: 3, NeedAction: 1, Clean: 1, Unknown: 1},
	}
	var buf bytes.Buffer
	renderLocalMulti(&buf, res)
	out := buf.String()
	for _, want := range []string{
		"outside a repo — scanned /work (depth 2)",
		"group/busy", "feat/x", "2 unpushed", "rebase 1/3 → gk continue",
		"slow", "state unknown: timed out after 10s",
		"3 repos scanned · 1 need action · 1 clean · 1 unknown",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "group/quiet") {
		t.Errorf("a clean repo must be counted, not listed:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("no-color output must carry no escape codes:\n%q", out)
	}
}
