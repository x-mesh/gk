package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
	"github.com/x-mesh/gk/internal/ui"
)

// TestSwitch_RebaseInProgressHint verifies that a switch failing mid-rebase
// surfaces a gk abort/continue hint instead of git's own `git rebase --quit`
// advice (AC3).
func TestSwitch_RebaseInProgressHint(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("a")
	repo.RunGit("checkout", "--detach")
	if err := os.MkdirAll(filepath.Join(repo.Dir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatalf("mkdir rebase-merge: %v", err)
	}

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	err := doSwitch(context.Background(), runner, &bytes.Buffer{}, "main", false, false, false)
	if err == nil {
		t.Fatal("expected switch to fail mid-rebase")
	}
	hint := HintFrom(err)
	if !strings.Contains(hint, "gk abort") || !strings.Contains(hint, "gk continue") {
		t.Errorf("hint should suggest gk abort/continue, got %q", hint)
	}
	// The whole message — not just the hint — must be clean of git's own
	// (wrong-for-gk) advice, and must name the in-progress operation.
	if strings.Contains(err.Error(), "git rebase --quit") {
		t.Errorf("error body should not echo git's --quit advice, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "rebase is in progress") &&
		!strings.Contains(err.Error(), "a rebase is in progress") {
		t.Errorf("error body should name the in-progress rebase, got %q", err.Error())
	}
}

func TestIsBranchNotFound(t *testing.T) {
	t.Parallel()
	notFound := []string{
		"git switch failed: fatal: invalid reference: feat/x: git switch feat/x: exit code 128",
		"error: pathspec 'feat/y' did not match any file(s) known to git",
		"fatal: unknown revision feat/z",
	}
	other := []string{
		"git switch failed: fatal: cannot switch branch while rebasing",
		"error: Your local changes would be overwritten by checkout",
	}
	for _, s := range notFound {
		if !isBranchNotFound(errors.New(s)) {
			t.Errorf("isBranchNotFound(%q) = false, want true", s)
		}
	}
	for _, s := range other {
		if isBranchNotFound(errors.New(s)) {
			t.Errorf("isBranchNotFound(%q) = true, want false", s)
		}
	}
}

func TestRemoteHasBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	up := testutil.NewRepo(t)
	up.WriteFile("a.txt", "a")
	up.Commit("a")
	up.CreateBranch("feat/exists")
	up.Checkout("main")

	repo := testutil.NewRepo(t)
	repo.AddRemote("origin", up.Dir)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	if !remoteHasBranch(ctx, runner, "origin", "feat/exists") {
		t.Error("feat/exists should be reported present on the remote")
	}
	if remoteHasBranch(ctx, runner, "origin", "feat/nope") {
		t.Error("feat/nope should be reported absent")
	}
}

// TestSwitch_MissOffersCreateHint: a name absent locally and on the remote
// (here, no remote at all) yields a non-TTY hint pointing at `gk sw -c`.
func TestSwitch_MissOffersCreateHint(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("a")

	root, _ := buildSwitchCmd(repo.Dir, "feat/brand-new")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error switching to a non-existent branch")
	}
	if h := HintFrom(err); !strings.Contains(h, "gk sw -c feat/brand-new") {
		t.Errorf("expected create hint, got err=%q hint=%q", err, h)
	}
}

// TestSwitch_MissOffersTrackHint: a name absent locally but present on the
// (unfetched) remote yields a non-TTY hint pointing at `gk sw --fetch`.
func TestSwitch_MissOffersTrackHint(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	up := testutil.NewRepo(t)
	up.WriteFile("a.txt", "a")
	up.Commit("a")
	up.CreateBranch("feat/on-remote")
	up.Checkout("main")

	repo := testutil.NewRepo(t)
	repo.AddRemote("origin", up.Dir) // deliberately NOT fetched

	root, _ := buildSwitchCmd(repo.Dir, "feat/on-remote")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error switching to an unfetched remote branch")
	}
	if h := HintFrom(err); !strings.Contains(h, "gk sw --fetch feat/on-remote") {
		t.Errorf("expected track hint, got err=%q hint=%q", err, h)
	}
}

// buildSwitchCmd wires a minimal root with the switch subcommand for tests.
// It sets --no-color and forces --repo so tests don't depend on package globals.
func buildSwitchCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	sw := &cobra.Command{
		Use:  "switch [branch]",
		Args: cobra.MaximumNArgs(1),
		RunE: runSwitch,
	}
	sw.Flags().BoolP("create", "c", false, "")
	sw.Flags().BoolP("force", "f", false, "")
	sw.Flags().Bool("detach", false, "")
	sw.Flags().Bool("fetch", false, "")
	sw.Flags().BoolP("main", "m", false, "")
	sw.Flags().Bool("develop", false, "")

	testRoot.AddCommand(sw)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	allArgs := append([]string{"--repo", repoDir, "switch"}, extraArgs...)
	testRoot.SetArgs(allArgs)
	return testRoot, buf
}

// TestListRemoteOnlyBranches verifies the picker ingredient:
//   - HEAD aliases (refs/remotes/origin/HEAD) are filtered
//   - entries whose short name matches an existing local branch are hidden
//   - all other refs/remotes/* entries surface with a proper trackRef
func TestListRemoteOnlyBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	// Simulate remote refs without needing a real remote: write packed
	// refs under refs/remotes/origin/* pointing at the seed commit.
	head := strings.TrimSpace(repo.RunGit("rev-parse", "HEAD"))

	repo.RunGit("update-ref", "refs/remotes/origin/HEAD", head)
	repo.RunGit("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	repo.RunGit("update-ref", "refs/remotes/origin/main", head)
	repo.RunGit("update-ref", "refs/remotes/origin/feature/new", head)
	repo.RunGit("update-ref", "refs/remotes/origin/hotfix", head)
	// Locally create one that should dedupe with origin/hotfix.
	repo.CreateBranch("hotfix")
	repo.Checkout("main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	locals, err := listLocalBranches(context.Background(), runner)
	if err != nil {
		t.Fatalf("listLocalBranches: %v", err)
	}
	remotes, published, err := listRemoteOnlyBranches(context.Background(), runner, locals)
	if err != nil {
		t.Fatalf("listRemoteOnlyBranches: %v", err)
	}

	got := map[string]string{}
	for _, r := range remotes {
		got[r.Name] = r.TrackRef
	}
	// main exists locally → excluded. hotfix exists locally → excluded.
	// HEAD alias → excluded. Only feature/new remains.
	if len(got) != 1 || got["feature/new"] != "origin/feature/new" {
		t.Errorf("unexpected remote-only set: %+v", got)
	}
	// The excluded locals are exactly the published set — that's the signal
	// the ● (both sides) vs ◌ (local only) marker rides on.
	if !published["main"] || !published["hotfix"] {
		t.Errorf("main and hotfix are on the remote → published, got %+v", published)
	}
	if published["feature/new"] {
		t.Errorf("feature/new has no local branch → must not be in the published set: %+v", published)
	}
}

func TestFetchSwitchBranches(t *testing.T) {
	t.Parallel()
	runner := &git.FakeRunner{}
	if err := fetchSwitchBranches(context.Background(), runner, "upstream"); err != nil {
		t.Fatalf("fetchSwitchBranches: %v", err)
	}
	if !hasCall(runner, "fetch --quiet --prune --no-tags --no-recurse-submodules upstream") {
		t.Fatalf("expected bounded fetch call, got %+v", runner.Calls)
	}
}

func TestFetchSwitchBranches_DefaultRemoteAndHint(t *testing.T) {
	t.Parallel()
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"fetch --quiet --prune --no-tags --no-recurse-submodules origin": {
			ExitCode: 128,
			Stderr:   "fatal: offline\n",
		},
	}}
	err := fetchSwitchBranches(context.Background(), runner, "")
	if err == nil || !strings.Contains(err.Error(), "fetch origin failed") {
		t.Fatalf("expected fetch failure, got %v", err)
	}
	if h := HintFrom(err); !strings.Contains(h, "gk sw --fetch") {
		t.Fatalf("expected retry hint, got %q", h)
	}
}

func TestSwitch_FetchThenDirectRemoteBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.CreateBranch("feature/new")
	upstream.WriteFile("new.txt", "new\n")
	upstream.Commit("new branch")
	upstream.Checkout("main")

	repo := testutil.NewRepo(t)
	repo.AddRemote("origin", upstream.Dir)

	root, buf := buildSwitchCmd(repo.Dir, "--fetch", "feature/new")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch --fetch feature/new failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, err := client.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if cur != "feature/new" {
		t.Errorf("current = %q, want feature/new", cur)
	}
	upstreamRef := strings.TrimSpace(repo.RunGit("rev-parse", "--abbrev-ref", "feature/new@{upstream}"))
	if upstreamRef != "origin/feature/new" {
		t.Errorf("upstream = %q, want origin/feature/new", upstreamRef)
	}
}

// TestSwitch_DirectByName changes branch when a name is given.
func TestSwitch_DirectByName(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feature-x")
	repo.WriteFile("x.txt", "x")
	repo.Commit("x")
	repo.Checkout("main")

	root, buf := buildSwitchCmd(repo.Dir, "feature-x")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, err := client.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if cur != "feature-x" {
		t.Errorf("current = %q, want feature-x", cur)
	}
	if !strings.Contains(buf.String(), "switched to feature-x") {
		t.Errorf("missing confirmation in output: %s", buf.String())
	}
}

// TestSwitch_CreateNew with -c creates a new branch and switches to it.
func TestSwitch_CreateNew(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	root, buf := buildSwitchCmd(repo.Dir, "-c", "feat/fresh")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch -c failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, err := client.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if cur != "feat/fresh" {
		t.Errorf("current = %q, want feat/fresh", cur)
	}
}

// TestSwitch_CreateWithoutName errors cleanly when -c has no arg.
func TestSwitch_CreateWithoutName(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	root, _ := buildSwitchCmd(repo.Dir, "-c")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires a branch name") {
		t.Errorf("expected 'requires a branch name' error, got %v", err)
	}
}

// TestSwitch_UnknownBranch returns an error.
func TestSwitch_UnknownBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	root, _ := buildSwitchCmd(repo.Dir, "does-not-exist")
	if err := root.Execute(); err == nil {
		t.Fatal("expected error on unknown branch")
	}
}

// TestSwitch_MainFallsBackToLocal resolves --main via the local 'main' branch
// when no remote is configured.
func TestSwitch_MainFallsBackToLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feature-x")

	root, buf := buildSwitchCmd(repo.Dir, "--main")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch --main failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, _ := client.CurrentBranch(context.Background())
	if cur != "main" {
		t.Errorf("current = %q, want main", cur)
	}
}

// TestSwitch_DevelopFindsDevBranch picks 'dev' when 'develop' is absent.
func TestSwitch_DevelopFindsDevBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("dev")
	repo.Checkout("main")

	root, buf := buildSwitchCmd(repo.Dir, "--develop")
	if err := root.Execute(); err != nil {
		t.Fatalf("switch --develop failed: %v\nout: %s", err, buf.String())
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	cur, _ := client.CurrentBranch(context.Background())
	if cur != "dev" {
		t.Errorf("current = %q, want dev", cur)
	}
}

// TestSwitch_MainDevelopMutex rejects both flags together.
func TestSwitch_MainDevelopMutex(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	root, _ := buildSwitchCmd(repo.Dir, "--main", "--develop")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually-exclusive error, got %v", err)
	}
}

// TestSwitch_DevelopMissingErrors reports missing develop/dev branch.
func TestSwitch_DevelopMissingErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	root, _ := buildSwitchCmd(repo.Dir, "--develop")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "develop/dev") {
		t.Errorf("expected 'no develop/dev' error, got %v", err)
	}
}

// --- guardDelete table ---

func TestGuardDelete_Placeholder(t *testing.T) {
	t.Parallel()
	err := guardDelete(targetBranchInfo{Placeholder: true}, "main", "main", nil, nil, false)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("placeholder: want 'empty' error, got %v", err)
	}
}

func TestGuardDelete_Remote_Blocked(t *testing.T) {
	t.Parallel()
	err := guardDelete(targetBranchInfo{Name: "foo", IsRemote: true}, "main", "main", nil, nil, true)
	if err == nil || !strings.Contains(err.Error(), "remote") {
		t.Errorf("remote: want 'remote' error, got %v", err)
	}
}

func TestGuardDelete_Current_Blocked(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{false, true} {
		err := guardDelete(targetBranchInfo{Name: "main"}, "main", "main", nil, map[string]bool{"main": true}, force)
		if err == nil || !strings.Contains(err.Error(), "current") {
			t.Errorf("force=%v current: want 'current' error, got %v", force, err)
		}
	}
}

func TestGuardDelete_Default_BlockedThenForced(t *testing.T) {
	t.Parallel()
	merged := map[string]bool{"main": true}
	// without force: default is protected
	err := guardDelete(targetBranchInfo{Name: "main"}, "feat/x", "main", nil, merged, false)
	if err == nil || !strings.Contains(err.Error(), "default") {
		t.Errorf("default without force: want 'default' error, got %v", err)
	}
	// with force (D): default may be deleted
	if err := guardDelete(targetBranchInfo{Name: "main"}, "feat/x", "main", nil, merged, true); err != nil {
		t.Errorf("default with force: want pass, got %v", err)
	}
}

func TestGuardDelete_Protected_BlockedThenForced(t *testing.T) {
	t.Parallel()
	protected := map[string]bool{"develop": true}
	merged := map[string]bool{"develop": true}
	// without force: protected branch is blocked even when merged
	err := guardDelete(targetBranchInfo{Name: "develop"}, "feat/x", "main", protected, merged, false)
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Errorf("protected without force: want 'protected' error, got %v", err)
	}
	if !errors.Is(err, errDeleteNeedsForce) {
		t.Errorf("protected rejection should be forceable, got %v", err)
	}
	if h := HintFrom(err); !strings.Contains(h, "force prompt") {
		t.Errorf("protected hint should point at the force prompt, got %q", h)
	}
	// with force: protected branch may be deleted
	if err := guardDelete(targetBranchInfo{Name: "develop"}, "feat/x", "main", protected, merged, true); err != nil {
		t.Errorf("protected with force: want pass, got %v", err)
	}
}

func TestGuardDelete_Unmerged_BlockedWithoutForce(t *testing.T) {
	t.Parallel()
	merged := map[string]bool{} // feat/y NOT merged
	err := guardDelete(targetBranchInfo{Name: "feat/y"}, "main", "main", nil, merged, false)
	if err == nil || !strings.Contains(err.Error(), "unmerged") {
		t.Errorf("unmerged without force: want 'unmerged' error, got %v", err)
	}
	if !errors.Is(err, errDeleteNeedsForce) {
		t.Errorf("unmerged rejection should be forceable, got %v", err)
	}
	if h := HintFrom(err); !strings.Contains(h, "force prompt") {
		t.Errorf("unmerged hint should point at the force prompt, got %q", h)
	}
}

func TestGuardDelete_Unmerged_AllowedWithForce(t *testing.T) {
	t.Parallel()
	merged := map[string]bool{}
	if err := guardDelete(targetBranchInfo{Name: "feat/y"}, "main", "main", nil, merged, true); err != nil {
		t.Errorf("unmerged with force: want pass, got %v", err)
	}
}

// Hard rejections must NOT be forceable — promoting them to a force
// prompt would offer the user an operation git will refuse anyway (or,
// for the current branch, one that must never be offered).
func TestGuardDelete_HardRejections_NotForceable(t *testing.T) {
	t.Parallel()
	cases := map[string]error{
		"placeholder": guardDelete(targetBranchInfo{Placeholder: true}, "main", "main", nil, nil, false),
		"remote":      guardDelete(targetBranchInfo{Name: "foo", IsRemote: true}, "main", "main", nil, nil, false),
		"current":     guardDelete(targetBranchInfo{Name: "main"}, "main", "main", nil, nil, false),
		"noCursor":    guardDelete(targetBranchInfo{Name: ""}, "main", "main", nil, nil, false),
	}
	for name, err := range cases {
		if err == nil {
			t.Errorf("%s: want rejection, got nil", name)
			continue
		}
		if errors.Is(err, errDeleteNeedsForce) {
			t.Errorf("%s: must not be forceable, got %v", name, err)
		}
	}
}

// The force prompt shows the guard's own wording, so the user learns why
// force is needed instead of a generic "unmerged work will be lost".
func TestForceableError_CarriesPlainReason(t *testing.T) {
	t.Parallel()
	merged := map[string]bool{}
	err := guardDelete(targetBranchInfo{Name: "feat/y"}, "main", "main", nil, merged, false)
	if got := err.Error(); got != `branch "feat/y" has unmerged commits` {
		t.Errorf("reason should stay plain for the prompt, got %q", got)
	}
}

func TestGuardDelete_Merged_Allowed(t *testing.T) {
	t.Parallel()
	merged := map[string]bool{"feat/done": true}
	for _, force := range []bool{false, true} {
		if err := guardDelete(targetBranchInfo{Name: "feat/done"}, "main", "main", nil, merged, force); err != nil {
			t.Errorf("force=%v merged: want pass, got %v", force, err)
		}
	}
}

// --- decodeBranchTarget / decodeSwitchChoice ---

func TestDecodeBranchTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  string
		want targetBranchInfo
	}{
		{"local:foo", targetBranchInfo{Name: "foo"}},
		{"local:__placeholder__", targetBranchInfo{Placeholder: true}},
		{"remote:origin/feat/x", targetBranchInfo{Name: "feat/x", IsRemote: true}},
		{"bare-key", targetBranchInfo{Name: "bare-key"}},
	}
	for _, c := range cases {
		got := decodeBranchTarget(ui.PickerItem{Key: c.key})
		if got != c.want {
			t.Errorf("decode %q: got %+v, want %+v", c.key, got, c.want)
		}
	}
}

// --- integration: delete merged branch via handleDeleteAction ---

func TestSwitchAction_DeleteMerged(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/merged")
	repo.Checkout("main") // feat/merged points at the same commit as main → merged

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	merged, err := mergedBranches(ctx, runner, "main")
	if err != nil {
		t.Fatalf("mergedBranches: %v", err)
	}
	if !merged["feat/merged"] {
		t.Fatalf("setup: feat/merged should be merged into main, got %+v", merged)
	}

	target := targetBranchInfo{Name: "feat/merged"}
	if err := guardDelete(target, "main", "main", nil, merged, false); err != nil {
		t.Fatalf("guardDelete: unexpected error: %v", err)
	}
	if _, _, err := runner.Run(ctx, "branch", "-d", "feat/merged"); err != nil {
		t.Fatalf("git branch -d: %v", err)
	}
	out := repo.RunGit("branch", "--list", "feat/merged")
	if strings.TrimSpace(out) != "" {
		t.Errorf("feat/merged should be gone, got %q", out)
	}
}

// TestWorktreeDeleteTarget_RoutesParkedBranch guards the regression where
// pressing d/D on a branch checked out in another worktree ran `git branch
// -d`, which git refuses ("used by worktree at ..."), leaving the user with
// a confusing "delete failed". Such a row must instead be classified for
// the worktree-removal flow; everything else stays on branch deletion.
func TestWorktreeDeleteTarget_RoutesParkedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/wt")
	repo.CreateBranch("feat/plain")
	repo.Checkout("main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	// Park feat/wt in a linked worktree so a `git branch -d feat/wt`
	// would be refused by git. feat/plain stays a normal local branch.
	wtPath := filepath.Join(t.TempDir(), "wt-feat")
	if _, _, err := runner.Run(ctx, "worktree", "add", wtPath, "feat/wt"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	wt := loadSwitchWorktrees(ctx, runner)
	if _, ok := wt.byBranch["feat/wt"]; !ok {
		t.Fatalf("setup: feat/wt should map to a worktree, got %+v", wt.byBranch)
	}

	// Parked branch → routed to worktree removal, carrying its entry.
	entry, isWT := worktreeDeleteTarget(ui.PickerItem{Key: keyLocalPrefix + "feat/wt"}, wt)
	if !isWT {
		t.Fatalf("feat/wt is parked in a worktree, want worktree-removal routing")
	}
	if canonPath(entry.Path) != canonPath(wtPath) {
		t.Errorf("routed entry.Path = %q, want %q", entry.Path, wtPath)
	}

	// Normal local branch → branch deletion (not worktree removal).
	if _, isWT := worktreeDeleteTarget(ui.PickerItem{Key: keyLocalPrefix + "feat/plain"}, wt); isWT {
		t.Errorf("feat/plain is not parked anywhere; must not route to worktree removal")
	}
	// Remote-only and placeholder rows never route to worktree removal.
	if _, isWT := worktreeDeleteTarget(ui.PickerItem{Key: keyRemotePrefix + "origin/feat/wt"}, wt); isWT {
		t.Errorf("remote row must not route to worktree removal")
	}
	if _, isWT := worktreeDeleteTarget(ui.PickerItem{Key: keyLocalPrefix + "__placeholder__"}, wt); isWT {
		t.Errorf("placeholder row must not route to worktree removal")
	}
}

// TestMaybeDeleteOrphanBranch_SkipsProtected guards the footgun where
// removing a protected branch's worktree then offered a casual `branch -D`
// of trunk (main/master/develop). The offer must be skipped entirely.
func TestMaybeDeleteOrphanBranch_SkipsProtected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("develop")
	repo.Checkout("main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	buf := &bytes.Buffer{}

	// develop is on the (repo-correct) protected list and not in any
	// worktree. If the guard were absent the function would reach ui.Confirm
	// (a non-TTY no-op here) — but we assert it returns BEFORE prompting and
	// never deletes the branch. (A non-protected branch would reach the
	// prompt; the protected one must short-circuit.)
	protected := []string{"main", "master", "develop"}
	if err := maybeDeleteOrphanBranch(ctx, runner, buf, WorktreeEntry{Branch: "develop"}, protected); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out := strings.TrimSpace(repo.RunGit("branch", "--list", "develop")); out == "" {
		t.Errorf("protected branch develop must not be deleted")
	}
}

func TestFormatDirtyMarker(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   git.DirtyFlags
		want string
	}{
		{git.DirtyFlags{}, ""},
		{git.DirtyFlags{Modified: true}, "*"},
		{git.DirtyFlags{Staged: true}, "±"},
		{git.DirtyFlags{Conflict: true}, "!"},
		{git.DirtyFlags{Modified: true, Staged: true}, "*±"},
		{git.DirtyFlags{Modified: true, Conflict: true}, "*!"},
		{git.DirtyFlags{Modified: true, Staged: true, Conflict: true}, "*±!"},
	}
	for _, c := range cases {
		if got := formatDirtyMarker(c.in); got != c.want {
			t.Errorf("formatDirtyMarker(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildSwitchItems_DirtyMarkerInBranchCell(t *testing.T) {
	t.Parallel()
	local := []branchInfo{
		{Name: "main", Hash: "abc", LastCommit: now()},
		{Name: "feat/x", Hash: "def", LastCommit: now()},
	}
	dirty := map[string]git.DirtyFlags{
		"feat/x": {Modified: true, Staged: true},
	}
	items := buildSwitchItems(local, nil, "main", switchWorktreeMap{}, dirty)
	for _, it := range items {
		cell := stripANSI(it.Cells[0])
		switch it.Key {
		case "local:main":
			if strings.Contains(cell, "*") || strings.Contains(cell, "±") {
				t.Errorf("clean branch should have no marker, got %q", cell)
			}
		case "local:feat/x":
			if !strings.Contains(cell, "*±") {
				t.Errorf("dirty branch should have *± marker, got %q", cell)
			}
		}
	}
}

func TestBuildSwitchItems_RemoteOnlyFilterItemDecodesToTrackPick(t *testing.T) {
	t.Parallel()
	remotes := []remoteBranchInfo{{
		Name:       "tmux",
		TrackRef:   "origin/tmux",
		Remote:     "origin",
		LastCommit: now(),
		Hash:       "7264900",
	}}
	items := buildSwitchItems(nil, remotes, "main", switchWorktreeMap{}, nil)
	if len(items) != 1 {
		t.Fatalf("expected one remote filter item, got %d", len(items))
	}
	if !strings.Contains(stripANSI(items[0].Display), "tmux") {
		t.Fatalf("remote filter item should include branch name, got %q", items[0].Display)
	}
	pick, err := decodeSwitchChoice(items[0])
	if err != nil {
		t.Fatalf("decodeSwitchChoice: %v", err)
	}
	if !pick.Remote || pick.Name != "tmux" || pick.TrackRef != "origin/tmux" {
		t.Fatalf("remote filter pick = %+v, want remote tmux origin/tmux", pick)
	}
}

// --- subtitle / decode / fallback / fork helpers ---

func TestBuildSwitchSubtitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		cur         string
		wt          switchWorktreeMap
		allRemotes  []remoteBranchInfo
		showRemotes bool
		fetchAge    time.Duration
		fetched     bool
		fetchFailed bool
		want        string
	}{
		{"empty", "", switchWorktreeMap{}, nil, false, 0, false, false, ""},
		{"current only", "main", switchWorktreeMap{}, nil, false, 0, false, false, "on: main"},
		{"current + linked worktree", "feat/x",
			switchWorktreeMap{linked: true, current: WorktreeEntry{Path: "/wt/x"}},
			nil, false, 0, false, false,
			"on: feat/x  ·  worktree: /wt/x"},
		{"hidden remotes", "main", switchWorktreeMap{},
			[]remoteBranchInfo{{Name: "a"}, {Name: "b"}}, false, 0, false, false,
			"on: main  ·  hidden: 2 remote (r)"},
		{"showRemotes shows freshness", "main", switchWorktreeMap{},
			[]remoteBranchInfo{{Name: "a"}}, true, 3 * time.Minute, true, false,
			"on: main  ·  fetched 3m ago"},
		{"showRemotes never fetched", "main", switchWorktreeMap{},
			[]remoteBranchInfo{{Name: "a"}}, true, 0, false, false,
			"on: main  ·  never fetched"},
		{"showRemotes fetch failed", "main", switchWorktreeMap{},
			[]remoteBranchInfo{{Name: "a"}}, true, 0, false, true,
			"on: main  ·  fetch failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildSwitchSubtitle(c.cur, c.wt, c.allRemotes, c.showRemotes, c.fetchAge, c.fetched, c.fetchFailed)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestFetchAgeLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		age     time.Duration
		fetched bool
		want    string
	}{
		{0, false, "never fetched"},
		{30 * time.Second, true, "fetched just now"},
		{5 * time.Minute, true, "fetched 5m ago"},
		{2 * time.Hour, true, "fetched 2h ago"},
		{49 * time.Hour, true, "fetched 2d ago"},
	}
	for _, c := range cases {
		if got := fetchAgeLabel(c.age, c.fetched); got != c.want {
			t.Errorf("fetchAgeLabel(%v, %v) = %q, want %q", c.age, c.fetched, got, c.want)
		}
	}
}

func TestRemoteFetchInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("a")

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	fh := filepath.Join(repo.Dir, ".git", "FETCH_HEAD")

	// No FETCH_HEAD yet → not fetched, so r would treat the view as stale.
	if _, ok := remoteFetchInfo(ctx, runner); ok {
		t.Errorf("expected ok=false before any fetch")
	}

	// Empty FETCH_HEAD (what a *failed* fetch leaves behind) → still not-ok,
	// so a failed fetch never masquerades as fresh.
	if err := os.WriteFile(fh, []byte(""), 0o644); err != nil {
		t.Fatalf("write FETCH_HEAD: %v", err)
	}
	if _, ok := remoteFetchInfo(ctx, runner); ok {
		t.Errorf("empty FETCH_HEAD (failed fetch) should report ok=false")
	}

	// Non-empty FETCH_HEAD = a successful fetch wrote a ref list.
	if err := os.WriteFile(fh, []byte("abc123\t\tbranch 'main' of origin\n"), 0o644); err != nil {
		t.Fatalf("write FETCH_HEAD: %v", err)
	}

	// Old mtime → stale (would fetch).
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(fh, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	age, ok := remoteFetchInfo(ctx, runner)
	if !ok || age <= switchRemoteStaleAfter {
		t.Errorf("expected stale age, got ok=%v age=%v", ok, age)
	}

	// Fresh mtime → not stale (would skip fetch).
	now := time.Now()
	if err := os.Chtimes(fh, now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	age, ok = remoteFetchInfo(ctx, runner)
	if !ok || age > switchRemoteStaleAfter {
		t.Errorf("expected fresh age, got ok=%v age=%v", ok, age)
	}
}

func TestDecodeSwitchChoice(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key      string
		wantName string
		wantTrk  string
		wantRem  bool
		wantErr  bool
	}{
		{"local:foo", "foo", "", false, false},
		{"local:__placeholder__", "", "", false, true},
		{"remote:origin/feat/x", "feat/x", "origin/feat/x", true, false},
		{"bare-key", "bare-key", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			pick, err := decodeSwitchChoice(ui.PickerItem{Key: c.key})
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if pick.Name != c.wantName || pick.TrackRef != c.wantTrk || pick.Remote != c.wantRem {
				t.Errorf("got %+v, want Name=%q TrackRef=%q Remote=%v",
					pick, c.wantName, c.wantTrk, c.wantRem)
			}
		})
	}
}

func TestApplyUntrackedFallback(t *testing.T) {
	t.Parallel()
	t.Run("no fallback", func(t *testing.T) {
		local := []branchInfo{{Name: "a"}}
		applyUntrackedFallback(local, nil)
		if local[0].UpstreamInferred || local[0].Ahead != 0 {
			t.Errorf("nil fallback should not mutate, got %+v", local[0])
		}
	})
	t.Run("upstream already set is skipped", func(t *testing.T) {
		local := []branchInfo{{Name: "a", Upstream: "origin/a"}}
		applyUntrackedFallback(local, []untrackedDivergent{
			{Branch: "a", Implicit: "origin/a", Ahead: 3, Behind: 1},
		})
		if local[0].UpstreamInferred {
			t.Errorf("branch with upstream must not be marked inferred")
		}
	})
	t.Run("matches name and applies counts", func(t *testing.T) {
		local := []branchInfo{{Name: "feat/x"}}
		applyUntrackedFallback(local, []untrackedDivergent{
			{Branch: "feat/x", Implicit: "origin/feat/x", Ahead: 3, Behind: 1},
		})
		got := local[0]
		if !got.UpstreamInferred || got.Upstream != "origin/feat/x" ||
			got.Ahead != 3 || got.Behind != 1 {
			t.Errorf("expected inferred upstream + counts, got %+v", got)
		}
	})
}

// computeForkPoints integration test — uses real git via testutil.NewRepo.
func TestComputeForkPoints_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	// main has the seed commit.
	mainHead := strings.TrimSpace(repo.RunGit("rev-parse", "HEAD"))
	repo.CreateBranch("feat/y")
	repo.WriteFile("y.txt", "y")
	repo.Commit("y commit on feat/y")
	repo.Checkout("main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	local := []branchInfo{
		{Name: "main"},
		{Name: "feat/y"},
	}
	computeForkPoints(context.Background(), runner, "main", local)

	// main is the default branch → not annotated.
	if local[0].ForkPoint != "" {
		t.Errorf("default branch should not get fork annotation, got %+v", local[0])
	}
	// feat/y forks at the seed commit (mainHead).
	if local[1].ForkPoint == "" {
		t.Errorf("feat/y missing fork point, got %+v", local[1])
	}
	if !strings.HasPrefix(mainHead, local[1].ForkPoint) {
		t.Errorf("fork point %q should be prefix of mainHead %q",
			local[1].ForkPoint, mainHead)
	}
	if local[1].ForkBranch != "main" {
		t.Errorf("ForkBranch = %q, want main", local[1].ForkBranch)
	}
}

// A recorded gk-parent overrides the trunk anchor; a recorded parent
// whose ref is gone falls back to the trunk (same policy as Resolver).
func TestComputeForkPoints_ParentAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	seedHead := strings.TrimSpace(repo.RunGit("rev-parse", "HEAD"))
	// Stack: main → feat/base (+1 commit) → feat/sub (+1 commit).
	repo.CreateBranch("feat/base")
	repo.WriteFile("b.txt", "b")
	repo.Commit("base commit")
	baseHead := strings.TrimSpace(repo.RunGit("rev-parse", "HEAD"))
	repo.CreateBranch("feat/sub")
	repo.WriteFile("s.txt", "s")
	repo.Commit("sub commit")
	repo.RunGit("config", "branch.feat/sub.gk-parent", "feat/base")
	// Write a dangling parent directly (bypasses set-parent validation).
	repo.RunGit("config", "branch.feat/base.gk-parent", "ghost")
	repo.Checkout("main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	local := []branchInfo{
		{Name: "feat/base"},
		{Name: "feat/sub"},
	}
	computeForkPoints(context.Background(), runner, "main", local)

	if local[1].ForkBranch != "feat/base" {
		t.Errorf("ForkBranch = %q, want feat/base", local[1].ForkBranch)
	}
	if local[1].ForkPoint == "" || !strings.HasPrefix(baseHead, local[1].ForkPoint) {
		t.Errorf("fork point %q should be prefix of feat/base head %q",
			local[1].ForkPoint, baseHead)
	}
	if local[0].ForkBranch != "main" {
		t.Errorf("dangling parent should fall back to main, got %+v", local[0])
	}
	if local[0].ForkPoint == "" || !strings.HasPrefix(seedHead, local[0].ForkPoint) {
		t.Errorf("fallback fork point %q should be prefix of main head %q",
			local[0].ForkPoint, seedHead)
	}
}

func TestComputeForkPoints_EmptyDefaultIsNoOp(t *testing.T) {
	t.Parallel()
	local := []branchInfo{{Name: "feat"}}
	// No runner needed — defaultBr=="" short-circuits before any git call.
	computeForkPoints(context.Background(), nil, "", local)
	if local[0].ForkPoint != "" || local[0].ForkBranch != "" {
		t.Errorf("empty defaultBr should be no-op, got %+v", local[0])
	}
}

// --- dirty-state integration ---

func TestLoadWorktreeDirtyStates_Modified(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("seed.txt", "hello")
	repo.RunGit("add", "seed.txt")
	repo.Commit("seed")

	// Modify the tracked file (no add).
	repo.WriteFile("seed.txt", "hello world")

	runner := &git.ExecRunner{Dir: repo.Dir}
	wt := loadSwitchWorktrees(context.Background(), runner)
	dirty := loadWorktreeDirtyStates(context.Background(), wt)

	flags, ok := dirty["main"]
	if !ok {
		t.Fatalf("expected 'main' in dirty map, got %+v", dirty)
	}
	if !flags.Modified || flags.Staged || flags.Conflict {
		t.Errorf("expected modified-only, got %+v", flags)
	}
}

func TestLoadWorktreeDirtyStates_StagedAndModified(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "1")
	repo.RunGit("add", "a.txt")
	repo.Commit("seed")

	// Stage one change, modify it again (becomes MM).
	repo.WriteFile("a.txt", "2")
	repo.RunGit("add", "a.txt")
	repo.WriteFile("a.txt", "3")

	runner := &git.ExecRunner{Dir: repo.Dir}
	wt := loadSwitchWorktrees(context.Background(), runner)
	dirty := loadWorktreeDirtyStates(context.Background(), wt)

	flags := dirty["main"]
	if !flags.Modified || !flags.Staged {
		t.Errorf("expected modified+staged, got %+v", flags)
	}
}

func TestLoadWorktreeDirtyStates_CleanIsAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "1")
	repo.RunGit("add", "a.txt")
	repo.Commit("seed")

	runner := &git.ExecRunner{Dir: repo.Dir}
	wt := loadSwitchWorktrees(context.Background(), runner)
	dirty := loadWorktreeDirtyStates(context.Background(), wt)

	if _, ok := dirty["main"]; ok {
		t.Errorf("clean worktree should be absent from dirty map, got %+v", dirty)
	}
}

// TestBuildSwitchItems_MarkerEncodesWhereBranchLives pins the marker
// vocabulary. It is the only signal that survives a terminal narrow enough
// to drop the UPSTREAM column, so each state must map to its own glyph.
func TestBuildSwitchItems_MarkerEncodesWhereBranchLives(t *testing.T) {
	t.Parallel()
	local := []branchInfo{
		{Name: "main", Hash: "aaa1111", LastCommit: now(), Upstream: "origin/main"},
		{Name: "tracked", Hash: "bbb2222", LastCommit: now(), Upstream: "origin/tracked"},
		// Pushed without --set-upstream: no upstream, but origin has it.
		{Name: "pushed-untracked", Hash: "ccc3333", LastCommit: now(), Published: true},
		{Name: "never-pushed", Hash: "ddd4444", LastCommit: now()},
		{Name: "orphaned", Hash: "eee5555", LastCommit: now(), Upstream: "origin/orphaned", Gone: true},
	}
	remotes := []remoteBranchInfo{
		{Name: "theirs", TrackRef: "origin/theirs", Remote: "origin", Hash: "fff6666", LastCommit: now()},
	}
	want := map[string]string{
		"local:main":             markerCurrent,
		"local:tracked":          markerBothSides,
		"local:pushed-untracked": markerBothSides,
		"local:never-pushed":     markerLocalOnly,
		"local:orphaned":         markerGone,
		"remote:origin/theirs":   markerRemoteOnly,
	}
	items := buildSwitchItems(local, remotes, "main", switchWorktreeMap{}, nil)
	if len(items) != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), len(items))
	}
	for _, it := range items {
		marker, ok := want[it.Key]
		if !ok {
			t.Fatalf("unexpected row %q", it.Key)
		}
		if got := stripANSI(it.Cells[0]); !strings.HasPrefix(got, marker+" ") {
			t.Errorf("%s: marker = %q, want %q", it.Key, got, marker)
		}
	}
}

func TestBuildSwitchLegend_OnlyMarkersOnScreen(t *testing.T) {
	t.Parallel()
	local := []branchInfo{
		{Name: "main", Upstream: "origin/main"},
		{Name: "feat/x", Upstream: "origin/feat/x"},
		{Name: "scratch"},
	}
	got, compact := buildSwitchLegend(local, nil, "main")
	for _, want := range []string{markerCurrent, markerBothSides, markerLocalOnly} {
		if !strings.Contains(got, want) {
			t.Errorf("legend %q should explain %q", got, want)
		}
		if !strings.Contains(compact, want) {
			t.Errorf("compact legend %q should explain %q", compact, want)
		}
	}
	// No gone branch and no remote rows → those keys stay out.
	for _, absent := range []string{markerGone, markerRemoteOnly} {
		if strings.Contains(got, absent) {
			t.Errorf("legend %q must not explain %q — no such row is listed", got, absent)
		}
	}
	// The compact form exists to fit narrower terminals.
	if len([]rune(compact)) >= len([]rune(got)) {
		t.Errorf("compact legend %q should be shorter than %q", compact, got)
	}
	// Remote rows revealed → the ○ key joins.
	withRemotes, _ := buildSwitchLegend(local, []remoteBranchInfo{{Name: "theirs"}}, "main")
	if !strings.Contains(withRemotes, markerRemoteOnly) {
		t.Errorf("legend %q should explain %q once remote rows are shown", withRemotes, markerRemoteOnly)
	}
}

func TestBuildSwitchLegend_SingleStateIsSilent(t *testing.T) {
	t.Parallel()
	// Only the current branch: nothing to tell apart, so no key line.
	got, compact := buildSwitchLegend([]branchInfo{{Name: "main"}}, nil, "main")
	if got != "" || compact != "" {
		t.Errorf("one marker state → no legend, got %q / %q", got, compact)
	}
}

// stripANSI removes ANSI SGR escape sequences so cell-content
// assertions are robust to fatih/color.NoColor flips by other tests
// in the package (log_test/pull_test cleanups reset to false instead
// of restoring the original value, leaking state into parallel runs).
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// --- worktree integration ---

func TestFormatSwitchDiff(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ahead, behind int
		want          string
	}{
		{0, 0, ""},
		{3, 0, "↑3"},
		{0, 5, "↓5"},
		{3, 5, "↑3 ↓5"},
	}
	for _, c := range cases {
		if got := formatSwitchDiff(c.ahead, c.behind); got != c.want {
			t.Errorf("formatSwitchDiff(%d,%d) = %q, want %q", c.ahead, c.behind, got, c.want)
		}
	}
}

func TestParseUpstreamTrack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in            string
		ahead, behind int
	}{
		{"", 0, 0},
		{"[gone]", 0, 0},
		{"[ahead 3]", 3, 0},
		{"[behind 5]", 0, 5},
		{"[ahead 3, behind 5]", 3, 5},
	}
	for _, c := range cases {
		a, b := parseUpstreamTrack(c.in)
		if a != c.ahead || b != c.behind {
			t.Errorf("parseUpstreamTrack(%q) = (%d,%d), want (%d,%d)", c.in, a, b, c.ahead, c.behind)
		}
	}
}

func TestBuildSwitchItems_DivergenceInBranchCell(t *testing.T) {
	t.Parallel()
	local := []branchInfo{
		{Name: "main", Hash: "aaa", LastCommit: now()},
		{Name: "feat/x", Hash: "bbb", LastCommit: now(), Upstream: "origin/feat/x", Ahead: 3, Behind: 1},
	}
	items := buildSwitchItems(local, nil, "main", switchWorktreeMap{}, nil)
	for _, it := range items {
		c0 := stripANSI(it.Cells[0])
		c1 := stripANSI(it.Cells[1])
		switch it.Key {
		case "local:main":
			if !strings.Contains(c1, "(local)") && !strings.Contains(c1, "↑ ") {
				// main has no upstream in this fixture → "(local)"
				if !strings.Contains(c1, "(local)") {
					t.Errorf("main UPSTREAM cell: got %q, want (local)", c1)
				}
			}
		case "local:feat/x":
			if !strings.Contains(c0, "↑3 ↓1") {
				t.Errorf("feat/x BRANCH cell should embed ↑3 ↓1, got %q", c0)
			}
			if strings.Contains(c1, "↑3") || strings.Contains(c1, "↓1") {
				t.Errorf("UPSTREAM cell should NOT have diff, got %q", c1)
			}
		}
	}
}

func TestBuildSwitchItems_ForkPointInSource(t *testing.T) {
	t.Parallel()
	local := []branchInfo{
		{Name: "feat/local", Hash: "aaa", LastCommit: now(),
			ForkBranch: "main", ForkPoint: "cbdce8b"},
	}
	items := buildSwitchItems(local, nil, "main", switchWorktreeMap{}, nil)
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if !strings.Contains(stripANSI(items[0].Cells[1]), "from main@cbdce8b") {
		t.Errorf("expected 'from main@cbdce8b' in source cell, got %q", items[0].Cells[1])
	}
}

// A worktree-locked branch keeps UPSTREAM as a pure source descriptor and
// surfaces the holding worktree's basename in the separate WORKTREE column.
func TestBuildSwitchItems_LockedSeparatesWorktreeColumn(t *testing.T) {
	t.Parallel()
	local := []branchInfo{
		{Name: "feat/wt", Hash: "bbb", LastCommit: now(),
			ForkBranch: "main", ForkPoint: "cbdce8b"},
	}
	wt := switchWorktreeMap{
		byBranch: map[string]WorktreeEntry{
			"feat/wt": {Path: "/tmp/wt/feat-wt", Branch: "feat/wt"},
		},
	}
	items := buildSwitchItems(local, nil, "main", wt, nil)
	if got := stripANSI(items[0].Cells[1]); got != "from main@cbdce8b" {
		t.Errorf("UPSTREAM should be pure (no wt: prefix), got %q", got)
	}
	if len(items[0].Cells) != 5 {
		t.Fatalf("expected 5 cells (WORKTREE column present), got %d: %v", len(items[0].Cells), items[0].Cells)
	}
	if got := stripANSI(items[0].Cells[2]); got != "feat-wt" {
		t.Errorf("WORKTREE cell should be the worktree basename, got %q", got)
	}
}

// With no branch locked to another worktree, the WORKTREE column is absent
// — a plain repo keeps the original four-column layout.
func TestBuildSwitchItems_NoWorktreeColumnWhenNoneLocked(t *testing.T) {
	t.Parallel()
	local := []branchInfo{
		{Name: "main", Hash: "aaa", LastCommit: now(), Upstream: "origin/main"},
	}
	items := buildSwitchItems(local, nil, "main", switchWorktreeMap{}, nil)
	if len(items[0].Cells) != 4 {
		t.Fatalf("expected 4 cells (no WORKTREE column), got %d: %v", len(items[0].Cells), items[0].Cells)
	}
	if got := stripANSI(items[0].Cells[1]); got != "↑ origin/main" {
		t.Errorf("UPSTREAM: want '↑ origin/main', got %q", got)
	}
}

func TestPickBranchForSwitch_CurrentPinnedFirst(t *testing.T) {
	t.Parallel()
	// Sort happens inside pickBranchForSwitch — verify the comparator
	// directly by replicating it on a known input.
	branches := []branchInfo{
		{Name: "feat/older", LastCommit: time.Now().Add(-72 * time.Hour)},
		{Name: "main", LastCommit: time.Now().Add(-1 * time.Hour)},
		{Name: "feat/newest", LastCommit: time.Now()},
	}
	cur := "main"
	sortStable := func(arr []branchInfo) {
		// mirrors the comparator in pickBranchForSwitch
		for i := 1; i < len(arr); i++ {
			for j := i; j > 0; j-- {
				less := func(a, b branchInfo) bool {
					if a.Name == cur {
						return true
					}
					if b.Name == cur {
						return false
					}
					return a.LastCommit.After(b.LastCommit)
				}
				if less(arr[j], arr[j-1]) {
					arr[j], arr[j-1] = arr[j-1], arr[j]
				} else {
					break
				}
			}
		}
	}
	sortStable(branches)
	if branches[0].Name != "main" {
		t.Errorf("expected main at index 0, got %q (full order: %+v)", branches[0].Name, branches)
	}
	if branches[1].Name != "feat/newest" {
		t.Errorf("expected feat/newest at index 1, got %q", branches[1].Name)
	}
}

func TestBuildSwitchItems_AllBranchesVisible_CurrentMarked(t *testing.T) {
	t.Parallel()
	local := []branchInfo{
		{Name: "main", LastCommit: now(), Hash: "abc1234"},
		{Name: "feat/free", LastCommit: now(), Hash: "def5678"},
		{Name: "feat/locked", LastCommit: now(), Hash: "ghi9012"},
	}
	wt := switchWorktreeMap{
		byBranch: map[string]WorktreeEntry{
			"feat/locked": {Path: "/tmp/wt/locked-tree", Branch: "feat/locked"},
		},
	}
	items := buildSwitchItems(local, nil, "main", wt, nil)
	if len(items) != 3 {
		t.Fatalf("expected all 3 local branches, got %d: %+v", len(items), items)
	}
	// One branch is locked → the WORKTREE column is present: 5 cells, with
	// HASH/AGE shifted to indices 3/4.
	for _, it := range items {
		if len(it.Cells) != 5 {
			t.Errorf("expected 5 cells (WORKTREE column present), got %d: %+v", len(it.Cells), it.Cells)
		}
		c0 := stripANSI(it.Cells[0])
		c1 := stripANSI(it.Cells[1])
		c2 := stripANSI(it.Cells[2])
		switch it.Key {
		case "local:main":
			if !strings.Contains(c0, "★") {
				t.Errorf("current branch should have ★ marker, got %q", c0)
			}
			if it.Cells[3] != "abc1234" {
				t.Errorf("expected hash abc1234 in cell 3, got %q", it.Cells[3])
			}
			if c2 != "" {
				t.Errorf("main is not worktree-locked → WORKTREE cell should be empty, got %q", c2)
			}
		case "local:feat/free":
			// No upstream and not published → local-only marker.
			if !strings.Contains(c0, "◌") {
				t.Errorf("unpushed branch should have ◌ marker, got %q", c0)
			}
		case "local:feat/locked":
			// UPSTREAM is now a pure source descriptor; worktree
			// occupancy moved to the dedicated WORKTREE column.
			if c1 != "(local)" {
				t.Errorf("UPSTREAM should be pure '(local)', got %q", c1)
			}
			if c2 != "locked-tree" {
				t.Errorf("WORKTREE cell should be the basename 'locked-tree', got %q", c2)
			}
		}
	}
}

func TestIsProtectedBranchName(t *testing.T) {
	t.Parallel()
	prot := []string{"main", "master", "develop"}
	if !isProtectedBranchName("main", prot) {
		t.Error("main should be protected")
	}
	if isProtectedBranchName("feat/x", prot) {
		t.Error("feat/x should not be protected")
	}
}

// Moving a protected branch into a linked worktree is refused on a
// non-interactive stream (the worktree-trap guard).
func TestDoSwitch_RefusesProtectedIntoLinkedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "none"))
	repo := testutil.NewRepo(t) // default branch: main (protected)
	repo.RunGit("branch", "feat")
	wtDir := filepath.Join(t.TempDir(), "wt")
	repo.RunGit("worktree", "add", wtDir, "feat")

	// From the linked worktree (on feat), try to pull main here.
	r := &git.ExecRunner{Dir: wtDir}
	var buf bytes.Buffer
	err := doSwitch(context.Background(), r, &buf, "main", false, false, false)
	if err == nil {
		t.Fatal("expected refusal moving protected main into a linked worktree")
	}
	if !strings.Contains(err.Error(), "protected") {
		t.Errorf("error should mention the protected branch, got: %v", err)
	}
}

// --detach and --force both bypass the guard (view-only / explicit override),
// and the primary worktree is exempt entirely.
func TestDoSwitch_GuardBypassAndPrimaryExempt(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "none"))

	// Primary worktree: switching to protected main is allowed.
	repo := testutil.NewRepo(t)
	repo.RunGit("branch", "feat")
	repo.RunGit("checkout", "feat")
	r := &git.ExecRunner{Dir: repo.Dir}
	var buf bytes.Buffer
	if err := doSwitch(context.Background(), r, &buf, "main", false, false, false); err != nil {
		t.Fatalf("primary-worktree switch to protected main should be allowed: %v", err)
	}

	// Linked worktree but --detach: allowed (a detached checkout can't trap
	// the branch).
	wtDir := filepath.Join(t.TempDir(), "wt")
	repo.RunGit("worktree", "add", wtDir, "feat")
	rl := &git.ExecRunner{Dir: wtDir}
	var buf2 bytes.Buffer
	if err := doSwitch(context.Background(), rl, &buf2, "main", false, false, true); err != nil {
		t.Fatalf("--detach into linked worktree should be allowed: %v", err)
	}
}

func TestLoadSwitchWorktrees_Topology(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/wt-a")
	repo.WriteFile("a.txt", "a")
	repo.Commit("seed wt-a")
	repo.Checkout("main")

	// Add a linked worktree at $TMP/wt-a holding feat/wt-a.
	wtDir := t.TempDir() + "/wt-a"
	repo.RunGit("worktree", "add", wtDir, "feat/wt-a")

	runner := &git.ExecRunner{Dir: repo.Dir}
	wt := loadSwitchWorktrees(context.Background(), runner)
	if len(wt.byBranch) != 1 {
		t.Fatalf("expected 1 occupied branch, got %d: %+v", len(wt.byBranch), wt.byBranch)
	}
	if e, ok := wt.byBranch["feat/wt-a"]; !ok || e.Path == "" {
		t.Errorf("expected feat/wt-a in byBranch, got %+v", wt.byBranch)
	}
	// We're in repo.Dir (main worktree), so linked must be false.
	if wt.linked {
		t.Errorf("expected linked=false from main worktree")
	}
}

func now() time.Time { return time.Now() }

func TestSwitchAction_DeleteUnmerged_Flow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/diverged")
	repo.WriteFile("d.txt", "x")
	repo.Commit("diverged")
	repo.Checkout("main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	merged, _ := mergedBranches(ctx, runner, "main")
	if merged["feat/diverged"] {
		t.Fatalf("setup: feat/diverged should NOT be merged into main")
	}

	target := targetBranchInfo{Name: "feat/diverged"}
	// `d` should be blocked.
	if err := guardDelete(target, "main", "main", nil, merged, false); err == nil {
		t.Errorf("guard d: expected unmerged error, got nil")
	}
	// `D` should pass the guard.
	if err := guardDelete(target, "main", "main", nil, merged, true); err != nil {
		t.Errorf("guard D: unexpected error: %v", err)
	}
	if _, _, err := runner.Run(ctx, "branch", "-D", "feat/diverged"); err != nil {
		t.Fatalf("git branch -D: %v", err)
	}
}
