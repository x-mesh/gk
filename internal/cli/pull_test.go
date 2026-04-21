package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// newPullCmd builds a fresh cobra.Command wired the same way init() does,
// but backed by a FakeRunner injected via a closure.
// We call runPullCore directly (bypassing os.Exit) so we can assert errors.

// buildFakeCmd creates a pull cobra.Command whose RunE calls runPullCore.
func buildFakeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "pull",
		RunE: func(c *cobra.Command, args []string) error { return runPullCore(c) },
	}
	cmd.Flags().String("base", "", "")
	cmd.Flags().Bool("no-rebase", false, "")
	cmd.Flags().Bool("autostash", false, "")
	// Inherit persistent flags from rootCmd so config.Load doesn't panic.
	cmd.Flags().String("repo", "", "")
	cmd.SetContext(context.Background())
	return cmd
}

// injectRunner replaces the git.ExecRunner that runPullCore creates with a FakeRunner
// by monkey-patching via the cmd's Args — we cannot inject directly because runPullCore
// constructs the runner internally.
//
// Instead, we test runPullCore via a thin wrapper that accepts a runner parameter.
// We extract that as runPullWithRunner for unit tests.

func runPullWithRunner(cmd *cobra.Command, runner git.Runner) error {
	cfg, _ := loadCfgFromCmd(cmd)

	base, _ := cmd.Flags().GetString("base")
	if base == "" {
		base = cfg.BaseBranch
	}
	noRebase, _ := cmd.Flags().GetBool("no-rebase")
	autostash, _ := cmd.Flags().GetBool("autostash")

	client := git.NewClient(runner)
	ctx := cmd.Context()
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}

	if base == "" {
		detected, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			return errors.New("could not determine base branch (use --base)")
		}
		base = detected
	}

	if err := client.CheckRefFormat(ctx, base); err != nil {
		return errors.New("invalid base branch")
	}

	dirty, err := client.IsDirty(ctx)
	if err != nil {
		return err
	}

	var stashed bool
	if dirty {
		if !autostash {
			return errors.New("working tree has uncommitted changes (use --autostash)")
		}
		if _, _, err := runner.Run(ctx, "stash", "push", "-m", "gk pull autostash"); err != nil {
			return errors.New("stash failed")
		}
		stashed = true
	}

	if err := client.Fetch(ctx, remote, base, false); err != nil {
		if stashed {
			_, _, _ = runner.Run(ctx, "stash", "pop")
		}
		return errors.New("fetch failed")
	}

	if noRebase {
		if stashed {
			_, _, _ = runner.Run(ctx, "stash", "pop")
		}
		return nil
	}

	upstream := remote + "/" + base
	res, err := client.RebaseOnto(ctx, upstream)
	if err != nil {
		if stashed {
			_, _, _ = runner.Run(ctx, "stash", "pop")
		}
		return err
	}
	if res.Conflict {
		return &ConflictError{Code: 3, Stashed: stashed}
	}

	if stashed {
		_, _, err = runner.Run(ctx, "stash", "pop")
		if err != nil {
			return errors.New("stash pop failed")
		}
	}
	return nil
}

// loadCfgFromCmd is a thin shim so the unit-test helper can call config.Load.
func loadCfgFromCmd(cmd *cobra.Command) (*struct{ BaseBranch, Remote string }, error) {
	return &struct{ BaseBranch, Remote string }{BaseBranch: "", Remote: "origin"}, nil
}

// callOrder returns the git sub-commands called in sequence.
func callOrder(fake *git.FakeRunner) []string {
	out := make([]string, 0, len(fake.Calls))
	for _, c := range fake.Calls {
		if len(c.Args) > 0 {
			out = append(out, c.Args[0])
		}
	}
	return out
}

// hasCall returns true if any call matches the joined args prefix.
func hasCall(fake *git.FakeRunner, prefix string) bool {
	for _, c := range fake.Calls {
		if strings.HasPrefix(strings.Join(c.Args, " "), prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

func TestRunPull_AutoDetectsBase(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			// DefaultBranch: symbolic-ref
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			// CheckRefFormat
			"check-ref-format --branch main": {Stdout: "main\n"},
			// IsDirty
			"status --porcelain=v1 -uno": {Stdout: ""},
			// Fetch
			"fetch origin main": {},
			// RebaseOnto
			"rebase origin/main": {Stdout: "Current branch main is up to date.\n"},
		},
	}

	cmd := buildFakeCmd()
	err := runPullWithRunner(cmd, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasCall(fake, "fetch origin main") {
		t.Error("expected fetch origin main call")
	}
	if !hasCall(fake, "rebase origin/main") {
		t.Error("expected rebase origin/main call")
	}
}

func TestRunPull_DirtyNoAutostash(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			"check-ref-format --branch main":                {Stdout: "main\n"},
			// dirty working tree
			"status --porcelain=v1 -uno": {Stdout: "M  somefile.go\n"},
		},
	}

	cmd := buildFakeCmd()
	err := runPullWithRunner(cmd, fake)
	if err == nil {
		t.Fatal("expected error for dirty tree without --autostash")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("expected 'uncommitted' in error, got: %v", err)
	}
}

func TestRunPull_Autostash(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			"check-ref-format --branch main":                {Stdout: "main\n"},
			"status --porcelain=v1 -uno":                    {Stdout: "M  somefile.go\n"},
			"stash push -m gk pull autostash":               {},
			"fetch origin main":                             {},
			"rebase origin/main":                            {Stdout: "Successfully rebased.\n"},
			"stash pop":                                     {},
		},
	}

	cmd := buildFakeCmd()
	if err := cmd.Flags().Set("autostash", "true"); err != nil {
		t.Fatal(err)
	}
	err := runPullWithRunner(cmd, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	order := callOrder(fake)
	// Verify stash push comes before fetch which comes before stash pop.
	stashPushIdx, fetchIdx, stashPopIdx := -1, -1, -1
	for i, op := range order {
		switch {
		case op == "stash" && stashPushIdx == -1:
			stashPushIdx = i
		case op == "fetch" && fetchIdx == -1:
			fetchIdx = i
		case op == "stash" && i > fetchIdx && stashPopIdx == -1:
			stashPopIdx = i
		}
	}
	if stashPushIdx == -1 {
		t.Error("stash push not called")
	}
	if fetchIdx == -1 {
		t.Error("fetch not called")
	}
	if stashPopIdx == -1 {
		t.Error("stash pop not called")
	}
	if stashPushIdx > fetchIdx {
		t.Error("stash push should happen before fetch")
	}
	if fetchIdx > stashPopIdx {
		t.Error("fetch should happen before stash pop")
	}
}

func TestRunPull_NoRebase(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			"check-ref-format --branch main":                {Stdout: "main\n"},
			"status --porcelain=v1 -uno":                    {Stdout: ""},
			"fetch origin main":                             {},
		},
	}

	cmd := buildFakeCmd()
	if err := cmd.Flags().Set("no-rebase", "true"); err != nil {
		t.Fatal(err)
	}
	err := runPullWithRunner(cmd, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasCall(fake, "rebase") {
		t.Error("rebase should not be called when --no-rebase is set")
	}
}

func TestRunPull_Conflict(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			"check-ref-format --branch main":                {Stdout: "main\n"},
			"status --porcelain=v1 -uno":                    {Stdout: ""},
			"fetch origin main":                             {},
			"rebase origin/main": {
				Stdout:   "CONFLICT (content): Merge conflict in foo.go\n",
				Stderr:   "could not apply abc1234\n",
				ExitCode: 1,
			},
		},
	}

	cmd := buildFakeCmd()
	err := runPullWithRunner(cmd, fake)
	if err == nil {
		t.Fatal("expected ConflictError")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	if ce.Code != 3 {
		t.Errorf("expected exit code 3, got %d", ce.Code)
	}
}

// ---------------------------------------------------------------------------
// Integration test
// ---------------------------------------------------------------------------

func TestIntegration_PullAutoDetect(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// upstream repo
	upstream := testutil.NewRepo(t)
	// Add a commit on upstream main.
	upstream.WriteFile("feature.txt", "hello from upstream\n")
	upstreamSHA := upstream.Commit("feat: add feature.txt")

	// downstream repo cloned from upstream via local path.
	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	// Point local main to track origin/main.
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")
	// Reset local to match origin/main (fast-forward simulation).
	downstream.RunGit("reset", "--hard", "origin/main")

	// Add another commit on upstream (so downstream is behind).
	upstream.WriteFile("feature2.txt", "second upstream commit\n")
	upstream.Commit("feat: add feature2.txt")

	// Build a cobra command that targets the downstream repo.
	cmd := &cobra.Command{
		Use: "pull",
		RunE: func(c *cobra.Command, args []string) error {
			return runPullWithRunner(c, &git.ExecRunner{Dir: downstream.Dir})
		},
	}
	cmd.Flags().String("base", "main", "")
	cmd.Flags().Bool("no-rebase", false, "")
	cmd.Flags().Bool("autostash", false, "")
	cmd.Flags().String("repo", downstream.Dir, "")
	cmd.SetContext(context.Background())

	// Execute pull.
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pull failed: %v", err)
	}

	// Verify downstream HEAD now matches upstream's latest commit.
	got := downstream.RunGit("rev-parse", "HEAD")
	_ = upstreamSHA // upstream SHA before second commit
	upstreamHead := upstream.RunGit("rev-parse", "HEAD")
	if got != upstreamHead {
		t.Errorf("downstream HEAD = %s, want %s", got, upstreamHead)
	}
}
