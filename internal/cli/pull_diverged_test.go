package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// ---------------------------------------------------------------------------
// tryTrackingUpstream — unit
// ---------------------------------------------------------------------------

func TestTryTrackingUpstream_HitParsesRef(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref --symbolic-full-name @{u}": {Stdout: "origin/feat/x\n"},
		},
	}
	upstream, fr, fb, ok := tryTrackingUpstream(context.Background(), fake)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if upstream != "origin/feat/x" || fr != "origin" || fb != "feat/x" {
		t.Errorf("got upstream=%q remote=%q branch=%q", upstream, fr, fb)
	}
}

func TestTryTrackingUpstream_MissReturnsFalse(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref --symbolic-full-name @{u}": {ExitCode: 128, Stderr: "fatal: no upstream\n"},
		},
	}
	_, _, _, ok := tryTrackingUpstream(context.Background(), fake)
	if ok {
		t.Error("expected ok=false on no-upstream")
	}
}

func TestTryTrackingUpstream_MalformedReturnsFalse(t *testing.T) {
	cases := []struct {
		name, stdout string
	}{
		{"no slash", "weird\n"},
		{"empty branch", "origin/\n"},
		{"empty remote", "/foo\n"},
		{"empty tracking", "\n"},
		{"literal @{u}", "@{u}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &git.FakeRunner{
				Responses: map[string]git.FakeResponse{
					"rev-parse --abbrev-ref --symbolic-full-name @{u}": {Stdout: tc.stdout},
				},
			}
			_, _, _, ok := tryTrackingUpstream(context.Background(), fake)
			if ok {
				t.Errorf("ok=true for %q, expected false", tc.stdout)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// computeAheadBehind — unit
// ---------------------------------------------------------------------------

func TestComputeAheadBehind_Parses(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-list --left-right --count HEAD...origin/main": {Stdout: "2\t11\n"},
		},
	}
	a, b, err := computeAheadBehind(context.Background(), fake, "origin/main")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a != 2 || b != 11 {
		t.Errorf("ahead=%d behind=%d, want 2/11", a, b)
	}
}

func TestComputeAheadBehind_PropagatesGitError(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-list --left-right --count HEAD...origin/main": {ExitCode: 128, Stderr: "bad ref\n"},
		},
	}
	if _, _, err := computeAheadBehind(context.Background(), fake, "origin/main"); err == nil {
		t.Error("expected error")
	}
}

// ---------------------------------------------------------------------------
// Integration tests — diverged refusal & explicit consent
// ---------------------------------------------------------------------------

// pullCoreCmd builds a real cobra.Command wired to runPullCore, scoped to
// the given working directory by overriding the package-global flagRepo.
// flagRepo is restored on test cleanup so test order doesn't leak state.
//
// XDG_CONFIG_HOME is also redirected to an empty tmp dir so the developer's
// real ~/.config/gk/config.yaml cannot leak `pull.strategy` into the test;
// otherwise the resolver would report source != "default" and the
// diverged-refusal path under test would never fire.
func pullCoreCmd(t *testing.T, dir string) *cobra.Command {
	t.Helper()
	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := &cobra.Command{Use: "pull", RunE: runPull}
	cmd.Flags().String("base", "", "")
	cmd.Flags().String("strategy", "", "")
	cmd.Flags().Bool("rebase", false, "")
	cmd.Flags().Bool("merge", false, "")
	cmd.Flags().Bool("fetch-only", false, "")
	cmd.Flags().Bool("no-rebase", false, "")
	cmd.Flags().Bool("autostash", false, "")
	cmd.Flags().CountVarP(&pullVerbose, "verbose", "v", "")
	cmd.SetContext(context.Background())
	return cmd
}

// makeDivergedClone returns (upstream, downstream) where downstream tracks
// origin/main, has 2 unpushed local commits, and origin has 3 commits the
// downstream lacks. Both branches share the same merge-base. This is the
// classic "diverged" shape gk pull must refuse to auto-rebase.
func makeDivergedClone(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")

	// Local: 2 unpushed commits.
	downstream.WriteFile("local-a.txt", "a\n")
	downstream.Commit("feat: local a")
	downstream.WriteFile("local-b.txt", "b\n")
	downstream.Commit("feat: local b")

	// Upstream: 3 new commits.
	upstream.WriteFile("up-1.txt", "1\n")
	upstream.Commit("feat: up 1")
	upstream.WriteFile("up-2.txt", "2\n")
	upstream.Commit("feat: up 2")
	upstream.WriteFile("up-3.txt", "3\n")
	upstream.Commit("feat: up 3")

	return upstream, downstream
}

func TestPull_DivergedRefusesWithoutExplicitStrategy(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeDivergedClone(t)

	cmd := pullCoreCmd(t, downstream.Dir)
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	preHEAD := downstream.RunGit("rev-parse", "HEAD")
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected refusal error on diverged history without --rebase/--merge")
	}
	if !strings.Contains(err.Error(), "diverged") {
		t.Errorf("error = %v, want 'diverged'", err)
	}

	// HEAD must not have moved.
	postHEAD := downstream.RunGit("rev-parse", "HEAD")
	if preHEAD != postHEAD {
		t.Errorf("HEAD moved on refusal: %s → %s", preHEAD, postHEAD)
	}

	out := stderr.String()
	if !strings.Contains(out, "histories diverged") {
		t.Errorf("stderr missing divergence banner:\n%s", out)
	}
	if !strings.Contains(out, "--rebase") || !strings.Contains(out, "--merge") || !strings.Contains(out, "--fetch-only") {
		t.Errorf("stderr missing choice hints:\n%s", out)
	}
}

func TestPull_DivergedProceedsWithRebaseFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeDivergedClone(t)

	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("rebase", "true"); err != nil {
		t.Fatal(err)
	}
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("rebase pull failed: %v", err)
	}

	// Verify a backup ref now exists for main.
	got := downstream.RunGit("for-each-ref", "--format=%(refname)", "refs/gk/backup/main/*")
	if !strings.Contains(got, "refs/gk/backup/main/") {
		t.Errorf("expected backup ref under refs/gk/backup/main/, got:\n%s", got)
	}
}

func TestPull_DivergedProceedsWithFetchOnlyFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeDivergedClone(t)

	cmd := pullCoreCmd(t, downstream.Dir)
	if err := cmd.Flags().Set("fetch-only", "true"); err != nil {
		t.Fatal(err)
	}
	cmd.SetErr(&bytes.Buffer{})

	preHEAD := downstream.RunGit("rev-parse", "HEAD")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("fetch-only pull failed: %v", err)
	}
	postHEAD := downstream.RunGit("rev-parse", "HEAD")
	if preHEAD != postHEAD {
		t.Errorf("HEAD should not move on fetch-only: %s → %s", preHEAD, postHEAD)
	}

	// No backup ref should be written when no integration happened.
	got := downstream.RunGit("for-each-ref", "--format=%(refname)", "refs/gk/backup/main/*")
	if strings.TrimSpace(got) != "" {
		t.Errorf("did not expect backup ref on fetch-only path, got:\n%s", got)
	}
}

// makeBehindOnlyClone returns a downstream that's strictly behind origin/main.
func makeBehindOnlyClone(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")

	upstream.WriteFile("up.txt", "u\n")
	upstream.Commit("feat: up")
	return upstream, downstream
}

func TestPull_BehindOnlyAutoFastForwards(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream, downstream := makeBehindOnlyClone(t)

	cmd := pullCoreCmd(t, downstream.Dir)
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("ff pull failed: %v", err)
	}

	wantHead := upstream.RunGit("rev-parse", "HEAD")
	gotHead := downstream.RunGit("rev-parse", "HEAD")
	if gotHead != wantHead {
		t.Errorf("HEAD = %s, want %s (ff to upstream)", gotHead, wantHead)
	}
	// FF must not create a backup (no history rewrite).
	got := downstream.RunGit("for-each-ref", "--format=%(refname)", "refs/gk/backup/main/*")
	if strings.TrimSpace(got) != "" {
		t.Errorf("did not expect backup ref on ff path, got:\n%s", got)
	}
}

// makeAheadOnlyClone returns a downstream with unpushed local commits only.
// Upstream has nothing past the seed.
func makeAheadOnlyClone(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")

	downstream.WriteFile("local.txt", "l\n")
	downstream.Commit("feat: local")
	return upstream, downstream
}

func TestPull_AheadOnlyReportsNoUpstreamChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, downstream := makeAheadOnlyClone(t)

	cmd := pullCoreCmd(t, downstream.Dir)
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	preHEAD := downstream.RunGit("rev-parse", "HEAD")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ahead-only pull failed: %v", err)
	}
	postHEAD := downstream.RunGit("rev-parse", "HEAD")
	if preHEAD != postHEAD {
		t.Errorf("HEAD should not move on ahead-only: %s → %s", preHEAD, postHEAD)
	}
	out := stderr.String()
	if !strings.Contains(out, "no upstream changes") {
		t.Errorf("stderr missing 'no upstream changes' line:\n%s", out)
	}
}

// makeUpstreamWithoutOriginHEAD returns a downstream where @{u} is set but
// refs/remotes/origin/HEAD is NOT — the exact case the issue-1 fix targets.
// Branch name 'feat/x' is intentionally NOT main/master/develop so the
// DefaultBranch fallback chain has nothing to grab.
func makeTrackingOnlyNoDefault(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")
	upstream.RunGit("branch", "-m", "main", "feat/x")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.RunGit("checkout", "-b", "feat/x", "origin/feat/x")
	// Note: no SetRemoteHEAD — origin/HEAD intentionally absent.

	upstream.WriteFile("up.txt", "u\n")
	upstream.Commit("feat: up")
	return upstream, downstream
}

func TestPull_TrackingUpstreamWorksWithoutOriginHEAD(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream, downstream := makeTrackingOnlyNoDefault(t)

	cmd := pullCoreCmd(t, downstream.Dir)
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull should succeed via @{u}, got: %v", err)
	}

	wantHead := upstream.RunGit("rev-parse", "HEAD")
	gotHead := downstream.RunGit("rev-parse", "HEAD")
	if gotHead != wantHead {
		t.Errorf("HEAD = %s, want %s", gotHead, wantHead)
	}
}
