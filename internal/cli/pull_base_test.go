package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

const (
	ffOldSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ffNewSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// ffFake builds a FakeRunner primed for the happy fast-forward path; tests
// override individual responses to carve out each skip gate.
func ffFake() *git.FakeRunner {
	return &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify --quiet refs/heads/main": {Stdout: ffOldSHA + "\n"},
			"rev-parse --verify --quiet origin/main":     {Stdout: ffNewSHA + "\n"},
			// worktree list: only the primary checkout on develop.
			"worktree list --porcelain": {Stdout: "worktree /repo\nHEAD " + ffOldSHA + "\nbranch refs/heads/develop\n\n"},
		},
	}
}

func ffCalls(f *git.FakeRunner, verb string) []string {
	var out []string
	for _, c := range f.Calls {
		if len(c.Args) > 0 && c.Args[0] == verb {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

// hasBranchLine reports whether some output line starts with the branch-name
// column label and contains frag — column padding varies with the longest
// participating name, so substring checks on "name frag" would be brittle.
func hasBranchLine(out, branch, frag string) bool {
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, branch+" ") && strings.Contains(ln, frag) {
			return true
		}
	}
	return false
}

func TestFFSyncBranch_FastForwards(t *testing.T) {
	withNoColor(t)
	disableEasyForTest(t)
	fake := ffFake()
	buf := &bytes.Buffer{}

	ffSyncBranch(context.Background(), fake, buf, "origin", "main", true /* alreadyFetched */, branchLabeler("main"))

	if got := ffCalls(fake, "fetch"); len(got) != 0 {
		t.Errorf("alreadyFetched=true must not fetch again, got %v", got)
	}
	updates := ffCalls(fake, "update-ref")
	if len(updates) != 1 || !strings.HasSuffix(updates[0], "refs/heads/main "+ffNewSHA+" "+ffOldSHA) {
		t.Errorf("expected one CAS update-ref ending in 'refs/heads/main <new> <old>', got %v", updates)
	}
	if !hasBranchLine(buf.String(), "main", "✓ fast-forwarded") {
		t.Errorf("missing labeled success line, got:\n%s", buf.String())
	}
}

func TestFFSyncBranch_FetchesWhenNotAlreadyFetched(t *testing.T) {
	withNoColor(t)
	disableEasyForTest(t)
	fake := ffFake()
	buf := &bytes.Buffer{}

	ffSyncBranch(context.Background(), fake, buf, "origin", "main", false, branchLabeler("main"))

	wantFetch := "fetch origin +refs/heads/main:refs/remotes/origin/main"
	if got := ffCalls(fake, "fetch"); len(got) != 1 || got[0] != wantFetch {
		t.Errorf("expected exactly %q, got %v", wantFetch, got)
	}
	if len(ffCalls(fake, "update-ref")) != 1 {
		t.Error("expected the FF to proceed after fetching")
	}
}

func TestFFSyncBranch_SkipGates(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*git.FakeRunner)
		wantNote   string
		wantCmd    string // remedy substring that must appear (optional)
		wantNoCmd  string // command substring that must NOT appear (optional)
		wantResult string // ffSyncOutcome.Result the gate must set (the JSON contract)
	}{
		{
			// FF gate fails, but the remote tip is an ancestor of the local
			// tip → strictly ahead, just unpushed. Fix is push, not pull.
			// The stubbed count pins the singular phrasing and proves the
			// count comes from origin/main..main (local commits, not behind).
			name: "ahead-only local commits",
			mutate: func(f *git.FakeRunner) {
				f.Responses["merge-base --is-ancestor "+ffOldSHA+" "+ffNewSHA] = git.FakeResponse{ExitCode: 1}
				f.Responses["merge-base --is-ancestor "+ffNewSHA+" "+ffOldSHA] = git.FakeResponse{ExitCode: 0}
				f.Responses["rev-list --count origin/main..main"] = git.FakeResponse{Stdout: "1\n"}
			},
			wantNote:   "ahead of origin/main — 1 commit not pushed",
			wantCmd:    "gk sw main && gk push",
			wantNoCmd:  "gk sw main && gk pull",
			wantResult: "skipped-ahead",
		},
		{
			// Neither tip is an ancestor of the other → genuine divergence.
			// Asymmetric counts (2 local, 1 remote) catch any swap of the
			// ahead/behind fields in countAheadBehind.
			name: "diverged on both sides",
			mutate: func(f *git.FakeRunner) {
				f.Responses["merge-base --is-ancestor "+ffOldSHA+" "+ffNewSHA] = git.FakeResponse{ExitCode: 1}
				f.Responses["merge-base --is-ancestor "+ffNewSHA+" "+ffOldSHA] = git.FakeResponse{ExitCode: 1}
				f.Responses["rev-list --left-right --count main...origin/main"] = git.FakeResponse{Stdout: "2\t1\n"}
			},
			wantNote:   "diverged from origin/main — 2 local, 1 remote",
			wantCmd:    "gk sw main && gk pull",
			wantNoCmd:  "gk sw main && gk push",
			wantResult: "skipped-diverged",
		},
		{
			name: "checked out in a worktree",
			mutate: func(f *git.FakeRunner) {
				f.Responses["worktree list --porcelain"] = git.FakeResponse{
					Stdout: "worktree /repo\nHEAD " + ffOldSHA + "\nbranch refs/heads/develop\n\nworktree /repo-main\nHEAD " + ffOldSHA + "\nbranch refs/heads/main\n\n",
				}
			},
			wantNote:   "checked out in /repo-main",
			wantResult: "skipped-worktree",
		},
		{
			name: "no local branch",
			mutate: func(f *git.FakeRunner) {
				f.Responses["rev-parse --verify --quiet refs/heads/main"] = git.FakeResponse{ExitCode: 1}
			},
			wantNote:   "no local branch",
			wantResult: "skipped-no-local",
		},
		{
			name: "remote ref missing after fetch",
			mutate: func(f *git.FakeRunner) {
				f.Responses["rev-parse --verify --quiet origin/main"] = git.FakeResponse{ExitCode: 1}
			},
			wantNote:   "'origin/main' does not exist after fetch",
			wantResult: "skipped-no-remote",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoColor(t)
			disableEasyForTest(t)
			fake := ffFake()
			tc.mutate(fake)
			buf := &bytes.Buffer{}

			outcome := ffSyncBranch(context.Background(), fake, buf, "origin", "main", true, branchLabeler("main"))

			out := buf.String()
			if !strings.Contains(out, "█  NOTE") || !strings.Contains(out, tc.wantNote) {
				t.Errorf("want NOTE containing %q, got:\n%s", tc.wantNote, out)
			}
			if tc.wantCmd != "" && !strings.Contains(out, tc.wantCmd) {
				t.Errorf("want remedy %q, got:\n%s", tc.wantCmd, out)
			}
			if tc.wantNoCmd != "" && strings.Contains(out, tc.wantNoCmd) {
				t.Errorf("must not suggest %q, got:\n%s", tc.wantNoCmd, out)
			}
			if outcome.Result != tc.wantResult {
				t.Errorf("Result = %q, want %q (the --json machine contract)", outcome.Result, tc.wantResult)
			}
			if got := ffCalls(fake, "update-ref"); len(got) != 0 {
				t.Errorf("skip gate must not move the ref, got %v", got)
			}
		})
	}
}

func TestFFSyncBranch_AlreadyUpToDate(t *testing.T) {
	withNoColor(t)
	disableEasyForTest(t)
	fake := ffFake()
	fake.Responses["rev-parse --verify --quiet origin/main"] = git.FakeResponse{Stdout: ffOldSHA + "\n"}
	buf := &bytes.Buffer{}

	ffSyncBranch(context.Background(), fake, buf, "origin", "main", true, branchLabeler("main"))

	if !hasBranchLine(buf.String(), "main", "Already up to date") {
		t.Errorf("expected labeled up-to-date line, got:\n%s", buf.String())
	}
	if got := ffCalls(fake, "update-ref"); len(got) != 0 {
		t.Errorf("equal tips must not move the ref, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Integration — gk pull --with-base end to end
// ---------------------------------------------------------------------------

// makeWithBaseClone returns (upstream, downstream): downstream is on develop
// tracking origin/develop, local main exists, tracks origin/main, and both
// remote branches have moved ahead since downstream last fetched.
func makeWithBaseClone(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")
	upstream.RunGit("branch", "develop")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")
	downstream.RunGit("switch", "-q", "-c", "develop", "origin/develop")
	downstream.RunGit("branch", "--set-upstream-to=origin/develop", "develop")

	// Both remote branches advance.
	upstream.Checkout("develop")
	upstream.WriteFile("dev.txt", "dev\n")
	upstream.Commit("feat: develop work")
	upstream.Checkout("main")
	upstream.WriteFile("rel.txt", "rel\n")
	upstream.Commit("chore: release work")

	return upstream, downstream
}

func TestIntegration_PullWithBase_FastForwardsBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream, downstream := makeWithBaseClone(t)

	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull --with-base failed: %v\nstderr:\n%s", err, stderr.String())
	}

	// develop integrated…
	gotDev := downstream.RunGit("rev-parse", "develop")
	wantDev := upstream.RunGit("rev-parse", "develop")
	if gotDev != wantDev {
		t.Errorf("develop = %s, want %s", gotDev, wantDev)
	}
	// …and main fast-forwarded without checkout.
	gotMain := downstream.RunGit("rev-parse", "main")
	wantMain := upstream.RunGit("rev-parse", "main")
	if gotMain != wantMain {
		t.Errorf("main = %s, want %s (base not synced)", gotMain, wantMain)
	}
	if cur := downstream.RunGit("branch", "--show-current"); cur != "develop" {
		t.Errorf("current branch changed to %s — with-base must not check out", cur)
	}
	if !hasBranchLine(stderr.String(), "main", "✓ fast-forwarded") {
		t.Errorf("missing labeled base success line, stderr:\n%s", stderr.String())
	}
}

func TestIntegration_PullWithBase_UpdatesBaseWithNarrowFetchRefspec(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream, downstream := makeWithBaseClone(t)

	// Reproduce a single-branch clone shape: the local repo has a stale
	// origin/main and local main, but remote.origin.fetch only maps develop. A
	// plain `git fetch origin main` updates FETCH_HEAD, not
	// refs/remotes/origin/main, so pull must use an explicit destination refspec
	// for the base fetch.
	downstream.RunGit("config", "--unset-all", "remote.origin.fetch")
	downstream.RunGit("config", "--add", "remote.origin.fetch", "+refs/heads/develop:refs/remotes/origin/develop")

	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull --with-base failed: %v\nstderr:\n%s", err, stderr.String())
	}

	gotMain := downstream.RunGit("rev-parse", "main")
	wantMain := upstream.RunGit("rev-parse", "main")
	if gotMain != wantMain {
		t.Errorf("main = %s, want %s (base not synced with narrow fetch refspec)\nstderr:\n%s", gotMain, wantMain, stderr.String())
	}
}

func TestIntegration_PullWithBase_SkipsDivergedBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeWithBaseClone(t)

	// makeWithBaseClone advances origin/main by one commit; give local main two
	// of its own → genuine divergence with asymmetric counts (2 local, 1
	// remote), which catches any swap of the ahead/behind fields.
	downstream.Checkout("main")
	downstream.WriteFile("local-only-1.txt", "x\n")
	downstream.Commit("feat: local-only on main (1)")
	downstream.WriteFile("local-only-2.txt", "y\n")
	localMain := downstream.Commit("feat: local-only on main (2)")
	downstream.Checkout("develop")

	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull --with-base failed: %v\nstderr:\n%s", err, stderr.String())
	}

	if got := downstream.RunGit("rev-parse", "main"); got != localMain {
		t.Errorf("diverged main moved: %s → %s", localMain, got)
	}
	out := stderr.String()
	if !strings.Contains(out, "diverged from origin/main — 2 local, 1 remote") {
		t.Errorf("missing diverged skip NOTE with asymmetric counts, stderr:\n%s", out)
	}
	if !strings.Contains(out, "gk sw main && gk pull") {
		t.Errorf("missing remediation command, stderr:\n%s", out)
	}
	if strings.Contains(out, "gk sw main && gk push") {
		t.Errorf("diverged base must not suggest push, stderr:\n%s", out)
	}
}

// makeAheadBaseClone returns (upstream, downstream) where downstream's local
// main is strictly ahead of origin/main (one unpushed commit, origin/main
// unchanged), while origin/develop has advanced so the pull still does work.
func makeAheadBaseClone(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")
	upstream.RunGit("branch", "develop")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")
	downstream.RunGit("switch", "-q", "-c", "develop", "origin/develop")
	downstream.RunGit("branch", "--set-upstream-to=origin/develop", "develop")

	// Only develop advances on the remote — origin/main stays put.
	upstream.Checkout("develop")
	upstream.WriteFile("dev.txt", "dev\n")
	upstream.Commit("feat: develop work")

	return upstream, downstream
}

func TestIntegration_PullWithBase_SkipsAheadBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeAheadBaseClone(t)

	// Local main gains two commits not on origin/main → strictly ahead,
	// unpushed. Two commits also exercises the plural "commits" branch.
	downstream.Checkout("main")
	downstream.WriteFile("ahead1.txt", "a\n")
	downstream.Commit("feat: ahead on main (1)")
	downstream.WriteFile("ahead2.txt", "b\n")
	localMain := downstream.Commit("feat: ahead on main (2)")
	downstream.Checkout("develop")

	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull --with-base failed: %v\nstderr:\n%s", err, stderr.String())
	}

	if got := downstream.RunGit("rev-parse", "main"); got != localMain {
		t.Errorf("ahead main moved: %s → %s", localMain, got)
	}
	out := stderr.String()
	if !strings.Contains(out, "ahead of origin/main — 2 commits not pushed") {
		t.Errorf("missing ahead skip NOTE with plural count, stderr:\n%s", out)
	}
	if !strings.Contains(out, "gk sw main && gk push") {
		t.Errorf("ahead base must suggest push, stderr:\n%s", out)
	}
	if strings.Contains(out, "gk sw main && gk pull") {
		t.Errorf("ahead base must not suggest pull, stderr:\n%s", out)
	}
}

func TestIntegration_PullWithBase_SkipsWorktreeOwnedBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeWithBaseClone(t)

	// Check main out into a linked worktree — its ref must not move.
	wtPath := t.TempDir() + "/main-wt"
	downstream.RunGit("worktree", "add", wtPath, "main")
	before := downstream.RunGit("rev-parse", "main")

	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull --with-base failed: %v\nstderr:\n%s", err, stderr.String())
	}

	if got := downstream.RunGit("rev-parse", "main"); got != before {
		t.Errorf("worktree-owned main moved: %s → %s", before, got)
	}
	if !strings.Contains(stderr.String(), "checked out in") {
		t.Errorf("missing worktree skip NOTE, stderr:\n%s", stderr.String())
	}
}

// TestIntegration_PullWithBase_LabelsResultLines: with --with-base the output
// reports two branches, so every result line must name its branch — an
// unlabeled "already up to date" on develop read as if main was meant.
func TestIntegration_PullWithBase_LabelsResultLines(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeWithBaseClone(t)

	// First pull integrates both branches.
	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("first pull: %v", err)
	}

	// Morning rerun: everything current — both lines must carry names.
	cmd2 := pullCoreCmd(t, downstream.Dir)
	if err := cmd2.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd2.SetErr(stderr)
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("second pull: %v\nstderr:\n%s", err, stderr.String())
	}
	out := stderr.String()
	if !hasBranchLine(out, "main", "Already up to date") {
		t.Errorf("base line must carry the main label:\n%s", out)
	}
	if !hasBranchLine(out, "develop", "Already up to date") {
		t.Errorf("current-branch line must carry the develop label:\n%s", out)
	}

	// Ahead-only: the nothing-to-pull line names branch and upstream.
	downstream.WriteFile("local.txt", "x\n")
	downstream.Commit("feat: local work")
	cmd3 := pullCoreCmd(t, downstream.Dir)
	stderr3 := &bytes.Buffer{}
	cmd3.SetErr(stderr3)
	if err := cmd3.Execute(); err != nil {
		t.Fatalf("third pull: %v", err)
	}
	if !hasBranchLine(stderr3.String(), "develop", "no upstream changes — ahead by 1 commit") {
		t.Errorf("ahead-only line must carry the develop label:\n%s", stderr3.String())
	}
}

// TestIntegration_PullSurvivesBrokenConfig: a global config with duplicate
// `pull:` sections used to make config.Load return a nil Config that
// runPullCore dereferenced — a hard panic. Now the broken layer is skipped
// with a one-time warning and the pull proceeds on defaults.
func TestIntegration_PullSurvivesBrokenConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeWithBaseClone(t)

	cmd := pullCoreCmd(t, downstream.Dir) // sets a private XDG_CONFIG_HOME
	gkDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gk")
	if err := os.MkdirAll(gkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	broken := "pull:\n  strategy: rebase\npull:\n  with_base: true\n"
	if err := os.WriteFile(filepath.Join(gkDir, "config.yaml"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	var warnBuf bytes.Buffer
	restore := config.SetConfigWarnWriter(&warnBuf)
	defer restore()

	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull must survive a broken config: %v\nstderr:\n%s", err, stderr.String())
	}
	warn := warnBuf.String()
	if !strings.Contains(warn, "config error") || !strings.Contains(warn, "already defined") {
		t.Errorf("expected duplicate-key config warning, got: %q", warn)
	}
}

// TestIntegration_PullJSONResult: --json emits the machine-readable result on
// stdout (stderr keeps the human stream) — here the up-to-date + base
// outcome shape agents branch on.
func TestIntegration_PullJSONResult(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeWithBaseClone(t)

	// First pull integrates everything so the second is deterministic.
	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("first pull: %v", err)
	}

	prevJSON := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJSON })

	cmd2 := pullCoreCmd(t, downstream.Dir)
	if err := cmd2.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd2.SetOut(stdout)
	cmd2.SetErr(stderr)
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("json pull: %v\nstderr:\n%s", err, stderr.String())
	}

	var res pullResultJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	if res.Schema != 1 || res.Result != "up-to-date" || res.Branch != "develop" || res.Upstream != "origin/develop" {
		t.Errorf("result fields: %+v", res)
	}
	if len(res.Base) != 1 || res.Base[0].Branch != "main" || res.Base[0].Result != "up-to-date" {
		t.Errorf("base outcomes: %+v", res.Base)
	}
	if res.Pre == "" || res.Pre != res.Post {
		t.Errorf("pre/post: %q/%q", res.Pre, res.Post)
	}
}

// TestIntegration_PullJSONUpdated: the integrate path reports moved SHAs and
// the base fast-forward outcome.
func TestIntegration_PullJSONUpdated(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream, downstream := makeWithBaseClone(t)

	prevJSON := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJSON })

	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("with-base", "true"); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull: %v", err)
	}

	var res pullResultJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	if res.Result != "updated" || res.Pre == res.Post || res.Post == "" {
		t.Errorf("updated result: %+v", res)
	}
	if res.Post != upstream.RunGit("rev-parse", "develop") {
		t.Errorf("post = %s, want upstream develop tip", res.Post)
	}
	if len(res.Base) != 1 || res.Base[0].Result != "fast-forwarded" {
		t.Errorf("base outcomes: %+v", res.Base)
	}
}

// TestIntegration_PullJSONAutostashPopConflict: when the integration
// succeeds but the autostash pop conflicts, the success JSON must NOT have
// been emitted (Codex P2) — the command exits non-zero and a script that
// already read result:"updated" would never notice.
func TestIntegration_PullJSONAutostashPopConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("shared.txt", "base\n")
	upstream.Commit("seed: shared")

	down := testutil.NewRepo(t)
	down.AddRemote("origin", upstream.Dir)
	down.RunGit("fetch", "origin")
	down.SetRemoteHEAD("origin", "main")
	down.RunGit("reset", "--hard", "origin/main")
	down.RunGit("branch", "--set-upstream-to=origin/main", "main")

	// Upstream rewrites the line; local edits the same line uncommitted —
	// the pull fast-forwards, then the stash pop conflicts.
	upstream.WriteFile("shared.txt", "upstream\n")
	upstream.Commit("feat: upstream edit")
	down.WriteFile("shared.txt", "local uncommitted\n")

	prevJSON := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJSON })

	cmd := pullCoreCmd(t, down.Dir)
	if err := cmd.Flags().Set("autostash", "true"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "stash pop conflict") {
		t.Fatalf("want stash pop conflict, got %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), `"updated"`) {
		t.Errorf("success JSON must not precede a pop failure:\n%s", stdout.String())
	}
}

func TestIntegration_PullAutostashPreservesIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("local.txt", "base\n")
	upstream.Commit("seed: local")

	down := testutil.NewRepo(t)
	down.AddRemote("origin", upstream.Dir)
	down.RunGit("fetch", "origin")
	down.SetRemoteHEAD("origin", "main")
	down.RunGit("reset", "--hard", "origin/main")
	down.RunGit("branch", "--set-upstream-to=origin/main", "main")

	// Upstream advances on an unrelated path so the pull fast-forwards cleanly.
	upstream.WriteFile("remote.txt", "remote\n")
	upstream.Commit("feat: remote")

	// Local partial staging: index has "staged", worktree adds "unstaged".
	down.WriteFile("local.txt", "base\nstaged\n")
	down.RunGit("add", "local.txt")
	down.WriteFile("local.txt", "base\nstaged\nunstaged\n")

	prevJSON := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJSON })

	cmd := pullCoreCmd(t, down.Dir)
	if err := cmd.Flags().Set("autostash", "true"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull --autostash: %v\nstderr:\n%s", err, stderr.String())
	}

	status := down.RunGit("status", "--porcelain=v1", "--", "local.txt")
	if !strings.HasPrefix(status, "MM ") {
		t.Fatalf("local.txt status = %q, want MM after indexed autostash pop", status)
	}
	cached := down.RunGit("diff", "--cached", "--", "local.txt")
	if !strings.Contains(cached, "+staged") || strings.Contains(cached, "+unstaged") {
		t.Fatalf("cached diff did not preserve only staged content:\n%s", cached)
	}
	unstaged := down.RunGit("diff", "--", "local.txt")
	if !strings.Contains(unstaged, "+unstaged") {
		t.Fatalf("unstaged diff missing worktree-only content:\n%s", unstaged)
	}

	var res pullResultJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	if res.Autostash == nil || !res.Autostash.Stashed || !res.Autostash.StagingPreserved {
		t.Fatalf("autostash json = %+v", res.Autostash)
	}
}

// TestIntegration_PullAutostashDefault: a dirty tree pulls with NO --autostash
// flag — autostash is on by default, so pull stashes the local edit, lands the
// fast-forward, and restores the edit, narrating it with "stashed"/"restored"
// status lines instead of stopping at a prompt.
func TestIntegration_PullAutostashDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("shared.txt", "base\n")
	upstream.Commit("seed: shared")

	down := testutil.NewRepo(t)
	down.AddRemote("origin", upstream.Dir)
	down.RunGit("fetch", "origin")
	down.SetRemoteHEAD("origin", "main")
	down.RunGit("reset", "--hard", "origin/main")
	down.RunGit("branch", "--set-upstream-to=origin/main", "main")

	// Upstream advances on an unrelated path → clean fast-forward.
	upstream.WriteFile("other.txt", "v2\n")
	upstream.Commit("feat: other")

	// Local uncommitted edit on a different file → the pop applies cleanly.
	down.WriteFile("shared.txt", "base\nwip\n")

	cmd := pullCoreCmd(t, down.Dir) // deliberately no --autostash
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("default pull on a dirty tree should auto-stash, got %v\nstderr:\n%s", err, stderr.String())
	}

	// The local edit survived the stash round-trip.
	if got := down.RunGit("status", "--porcelain=v1", "--", "shared.txt"); !strings.HasPrefix(got, " M") {
		t.Fatalf("shared.txt status = %q, want restored local edit (' M')", got)
	}
	if got := down.RunGit("show", "HEAD:other.txt"); strings.TrimSpace(got) != "v2" {
		t.Fatalf("upstream commit did not land: other.txt = %q", got)
	}
	if !strings.Contains(stderr.String(), "stashed 1 local change") {
		t.Errorf("missing auto-stash status line:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "restored 1 local change") {
		t.Errorf("missing restore status line:\n%s", stderr.String())
	}
}

// TestIntegration_PullNoAutostashRefusesNonTTY: --no-autostash restores the old
// gate, so a dirty tree on a non-TTY refuses rather than stashing.
func TestIntegration_PullNoAutostashRefusesNonTTY(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("shared.txt", "base\n")
	upstream.Commit("seed: shared")

	down := testutil.NewRepo(t)
	down.AddRemote("origin", upstream.Dir)
	down.RunGit("fetch", "origin")
	down.SetRemoteHEAD("origin", "main")
	down.RunGit("reset", "--hard", "origin/main")
	down.RunGit("branch", "--set-upstream-to=origin/main", "main")

	upstream.WriteFile("other.txt", "v2\n")
	upstream.Commit("feat: other")
	down.WriteFile("shared.txt", "base\nwip\n")

	cmd := pullCoreCmd(t, down.Dir)
	if err := cmd.Flags().Set("no-autostash", "true"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("--no-autostash on a non-TTY should refuse, got %v\nstderr:\n%s", err, stderr.String())
	}
	// Nothing was stashed.
	if got := down.RunGit("stash", "list"); strings.TrimSpace(got) != "" {
		t.Errorf("--no-autostash must not stash, but stash list = %q", got)
	}
}
