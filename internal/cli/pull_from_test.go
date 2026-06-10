package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestResolvePullFrom(t *testing.T) {
	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"remote": {Stdout: "origin\ntape42\n"},
	}}
	ctx := context.Background()

	t.Run("remote only infers current branch", func(t *testing.T) {
		upstream, remote, branch, err := resolvePullFrom(ctx, fake, "tape42", "main")
		if err != nil {
			t.Fatal(err)
		}
		if upstream != "tape42/main" || remote != "tape42" || branch != "main" {
			t.Fatalf("got %s %s %s", upstream, remote, branch)
		}
	})
	t.Run("remote/branch explicit", func(t *testing.T) {
		upstream, remote, branch, err := resolvePullFrom(ctx, fake, "tape42/develop", "main")
		if err != nil {
			t.Fatal(err)
		}
		if upstream != "tape42/develop" || remote != "tape42" || branch != "develop" {
			t.Fatalf("got %s %s %s", upstream, remote, branch)
		}
	})
	t.Run("branch with slashes", func(t *testing.T) {
		upstream, _, branch, err := resolvePullFrom(ctx, fake, "tape42/feature/x", "main")
		if err != nil {
			t.Fatal(err)
		}
		if upstream != "tape42/feature/x" || branch != "feature/x" {
			t.Fatalf("got %s %s", upstream, branch)
		}
	})
	t.Run("unknown remote rejected with hint", func(t *testing.T) {
		_, _, _, err := resolvePullFrom(ctx, fake, "tape24/main", "main")
		if err == nil || !strings.Contains(err.Error(), "unknown remote") {
			t.Fatalf("want unknown-remote error, got %v", err)
		}
		if hint := HintFrom(err); !strings.Contains(hint, "origin, tape42") {
			t.Errorf("hint must list registered remotes, got %q", hint)
		}
	})
	t.Run("detached HEAD needs explicit branch", func(t *testing.T) {
		_, _, _, err := resolvePullFrom(ctx, fake, "tape42", "")
		if err == nil || !strings.Contains(err.Error(), "detached") {
			t.Fatalf("want detached error, got %v", err)
		}
	})
}

// TestIntegration_PullFrom reproduces the asymmetric-remote situation: the
// upstream chain (origin) is current, while a second remote has commits the
// tracking remote will never serve. --from must integrate them without
// touching the tracking config.
func TestIntegration_PullFrom(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}

	origin := testutil.NewRepo(t)
	origin.WriteFile("seed.txt", "seed\n")
	origin.Commit("seed: initial")

	local := testutil.NewRepo(t)
	local.AddRemote("origin", origin.Dir)
	local.RunGit("fetch", "origin")
	local.SetRemoteHEAD("origin", "main")
	local.RunGit("reset", "--hard", "origin/main")
	local.RunGit("branch", "--set-upstream-to=origin/main", "main")

	// The second remote: same history plus one commit origin lacks —
	// the "merged on the other side" shape.
	mirror := testutil.NewRepo(t)
	mirror.AddRemote("seedsrc", origin.Dir)
	mirror.RunGit("fetch", "seedsrc")
	mirror.RunGit("reset", "--hard", "seedsrc/main")
	mirror.WriteFile("mirror.txt", "merged on mirror\n")
	mirrorSHA := mirror.Commit("feat: mirror-side work")
	local.AddRemote("tape42", mirror.Dir)

	cmd := pullCoreCmd(t, local.Dir)
	if err := cmd.Flags().Set("from", "tape42"); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull --from failed: %v\nstderr:\n%s", err, stderr.String())
	}

	if got := local.RunGit("rev-parse", "HEAD"); got != mirrorSHA {
		t.Errorf("HEAD = %s, want mirror commit %s", got, mirrorSHA)
	}
	// Tracking config must be untouched: @{u} still origin/main.
	if got := local.RunGit("rev-parse", "--abbrev-ref", "@{u}"); got != "origin/main" {
		t.Errorf("upstream changed to %s — --from must not retarget tracking", got)
	}
	if !strings.Contains(stderr.String(), "pulling from tape42/main") {
		t.Errorf("missing --from note, stderr:\n%s", stderr.String())
	}
}
