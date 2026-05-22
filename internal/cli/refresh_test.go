package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// buildRefreshCmd wires a cobra root + `refresh` subcommand targeting
// repoDir. Test-local because the real init() registers against the global
// rootCmd. NoColor is forced so any buffer assertions stay clean.
func buildRefreshCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	refresh := &cobra.Command{
		Use:          "refresh",
		RunE:         runRefresh,
		SilenceUsage: true,
	}
	refresh.Flags().Bool("no-fetch", false, "skip the network fetch")
	testRoot.AddCommand(refresh)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	allArgs := append([]string{"--repo", repoDir, "refresh"}, extraArgs...)
	testRoot.SetArgs(allArgs)
	return testRoot, buf
}

// setupMainDevelopClone builds an upstream with main + develop and a
// downstream tracking both, currently parked on a feature branch off main.
// Returns (upstream, downstream). Tests advance upstream to construct the
// scenario, then run `gk refresh` on the downstream.
func setupMainDevelopClone(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	up := testutil.NewRepo(t)
	up.WriteFile("a.txt", "1\n")
	up.Commit("m0")
	up.RunGit("branch", "develop") // develop starts at m0

	down := testutil.NewRepo(t)
	down.AddRemote("origin", up.Dir)
	down.RunGit("fetch", "origin")
	down.SetRemoteHEAD("origin", "main")
	// Materialise local main/develop at the fetched remote tips, then park
	// on a feature branch so neither tracked branch is the current branch.
	down.RunGit("checkout", "-B", "main", "origin/main")
	down.RunGit("checkout", "-B", "develop", "origin/develop")
	down.RunGit("checkout", "-b", "feat/x", "main")
	return up, down
}

func refSHA(t *testing.T, r *testutil.Repo, ref string) string {
	t.Helper()
	return r.RunGit("rev-parse", ref)
}

func TestRefresh_FastForwardsTrackedBranchesViaUpdateRef(t *testing.T) {
	up, down := setupMainDevelopClone(t)

	// Advance both upstream branches past what the downstream has.
	up.WriteFile("a.txt", "2\n")
	up.Commit("m1")
	up.Checkout("develop")
	up.WriteFile("d.txt", "d\n")
	up.Commit("d1")
	up.Checkout("main")

	root, _ := buildRefreshCmd(down.Dir)
	if err := root.Execute(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Both tracked branches must now match their remote tips.
	if got, want := refSHA(t, down, "refs/heads/main"), refSHA(t, down, "refs/remotes/origin/main"); got != want {
		t.Errorf("local main = %s, want origin/main %s", got, want)
	}
	if got, want := refSHA(t, down, "refs/heads/develop"), refSHA(t, down, "refs/remotes/origin/develop"); got != want {
		t.Errorf("local develop = %s, want origin/develop %s", got, want)
	}
	// The current branch must be untouched — refresh never leaves it.
	if cur := down.RunGit("rev-parse", "--abbrev-ref", "HEAD"); cur != "feat/x" {
		t.Errorf("current branch = %q, want feat/x", cur)
	}
}

func TestRefresh_CurrentBranchFastForwardsWorkingTree(t *testing.T) {
	up, down := setupMainDevelopClone(t)

	// Advance develop upstream; park downstream ON develop (clean tree).
	up.Checkout("develop")
	up.WriteFile("d.txt", "d\n")
	up.Commit("d1")
	up.Checkout("main")
	down.Checkout("develop")

	root, _ := buildRefreshCmd(down.Dir)
	if err := root.Execute(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if got, want := refSHA(t, down, "refs/heads/develop"), refSHA(t, down, "refs/remotes/origin/develop"); got != want {
		t.Errorf("local develop = %s, want origin/develop %s", got, want)
	}
	// merge --ff-only must have updated the working tree, not just the ref.
	if _, err := down.TryGit("cat-file", "-e", "HEAD:d.txt"); err != nil {
		t.Errorf("d.txt missing from HEAD tree after current-branch ff: %v", err)
	}
}

func TestRefresh_DivergedBranchIsSkippedNotRewritten(t *testing.T) {
	up, down := setupMainDevelopClone(t)

	// Local main gains a commit the remote never sees…
	down.Checkout("main")
	down.WriteFile("local.txt", "x\n")
	localMain := down.Commit("local-on-main")
	down.Checkout("feat/x")
	// …and the remote main advances independently → diverged.
	up.WriteFile("a.txt", "2\n")
	up.Commit("m1")

	root, buf := buildRefreshCmd(down.Dir)
	if err := root.Execute(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Diverged branch must be left exactly where it was.
	if got := refSHA(t, down, "refs/heads/main"); got != localMain {
		t.Errorf("diverged main was moved: got %s, want unchanged %s", got, localMain)
	}
	if !strings.Contains(stripped(buf.String()), "diverged") {
		t.Errorf("expected a diverged notice, got:\n%s", buf.String())
	}
}

func TestRefresh_ExplicitArgsAndMissingRefsSkipCleanly(t *testing.T) {
	_, down := setupMainDevelopClone(t)

	// A local-only branch (no remote) and a non-existent branch must both
	// be reported as skips without failing the command.
	down.RunGit("branch", "local-only", "feat/x")

	root, _ := buildRefreshCmd(down.Dir, "local-only", "ghost")
	if err := root.Execute(); err != nil {
		t.Fatalf("refresh with unrefreshable targets should not error: %v", err)
	}
}

func TestRefresh_NoTargetsResolvedErrors(t *testing.T) {
	// A bare repo with neither main/master nor develop/dev and no explicit
	// targets has nothing to refresh — that is a usage error, not a silent
	// no-op.
	r := testutil.NewRepo(t)
	r.RunGit("checkout", "-b", "scratch")
	r.RunGit("branch", "-D", "main")

	root, _ := buildRefreshCmd(r.Dir)
	if err := root.Execute(); err == nil {
		t.Fatal("expected an error when no tracked branches resolve")
	}
}

// stripped removes ANSI escape sequences so assertions survive coloured
// output regardless of TTY detection.
func stripped(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
