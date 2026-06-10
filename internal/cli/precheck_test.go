package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// ---------------------------------------------------------------------------
// Unit — parseMergeTreeNames
// ---------------------------------------------------------------------------

func TestParseMergeTreeNames_Clean(t *testing.T) {
	// Clean merge: only the tree OID line.
	out := []byte("abcdef1234567890\n")
	got := parseMergeTreeNames(out)
	if len(got) != 0 {
		t.Fatalf("expected no conflicts, got %v", got)
	}
}

func TestParseMergeTreeNames_SingleConflict(t *testing.T) {
	out := []byte("deadbeef\nfile.txt\n")
	got := parseMergeTreeNames(out)
	want := []string{"file.txt"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseMergeTreeNames_Dedupe(t *testing.T) {
	// A conflict may list the same path multiple times (one per stage 1/2/3).
	out := []byte("deadbeef\nfile.txt\nfile.txt\nfile.txt\nother.go\n")
	got := parseMergeTreeNames(out)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique paths, got %v", got)
	}
	if got[0] != "file.txt" || got[1] != "other.go" {
		t.Fatalf("unexpected order: %v", got)
	}
}

func TestParseMergeTreeNames_EmptyTrailingLines(t *testing.T) {
	out := []byte("tree\n\na.txt\n\n")
	got := parseMergeTreeNames(out)
	if len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Unit — guardRef
// ---------------------------------------------------------------------------

func TestGuardRef(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		ok   bool
	}{
		{"plain branch", "main", true},
		{"remote tracking", "origin/main", true},
		{"tag", "v1.0.0", true},
		{"sha", "abc123", true},
		{"empty", "", false},
		{"leading dash", "-rm-rf", false},
		{"leading dash long flag", "--all", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardRef(c.ref)
			if c.ok && err != nil {
				t.Errorf("expected ok for %q, got %v", c.ref, err)
			}
			if !c.ok && err == nil {
				t.Errorf("expected error for %q", c.ref)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration — scanMergeConflicts against real git
// ---------------------------------------------------------------------------

func TestScanMergeConflicts_Clean(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	repo.WriteFile("a.txt", "hello\n")
	repo.Commit("init")

	repo.CreateBranch("feature")
	repo.WriteFile("b.txt", "feature\n")
	repo.Commit("feat: add b")

	repo.Checkout("main")
	repo.WriteFile("c.txt", "main\n")
	repo.Commit("chore: add c")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	mb := strings.TrimSpace(repo.RunGit("merge-base", "HEAD", "feature"))
	conflicts, err := scanMergeConflicts(ctx, runner, mb, "HEAD", "feature")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected clean merge, got conflicts %v", conflicts)
	}
}

func TestScanMergeConflicts_Conflict(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	repo.WriteFile("a.txt", "base\n")
	repo.Commit("init")

	repo.CreateBranch("feature")
	repo.WriteFile("a.txt", "feature edit\n")
	repo.Commit("feat: edit on feature")

	repo.Checkout("main")
	repo.WriteFile("a.txt", "main edit\n")
	repo.Commit("chore: edit on main")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	mb := strings.TrimSpace(repo.RunGit("merge-base", "HEAD", "feature"))
	conflicts, err := scanMergeConflicts(ctx, runner, mb, "HEAD", "feature")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatal("expected conflicts, got none")
	}
	found := false
	for _, p := range conflicts {
		if p == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a.txt in conflicts, got %v", conflicts)
	}
}

// ---------------------------------------------------------------------------
// Integration — gk precheck cobra command
// ---------------------------------------------------------------------------

func buildPrecheckCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	pre := &cobra.Command{
		Use:          "precheck <target>",
		Args:         cobra.ExactArgs(1),
		RunE:         runPrecheckCore,
		SilenceUsage: true,
	}
	pre.Flags().String("base", "", "explicit merge-base")
	pre.Flags().Bool("json", false, "emit JSON")
	testRoot.AddCommand(pre)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	allArgs := append([]string{"--repo", repoDir, "precheck"}, extraArgs...)
	testRoot.SetArgs(allArgs)
	return testRoot, buf
}

func TestPrecheckCmd_CleanTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")
	repo.CreateBranch("feature")
	repo.WriteFile("b.txt", "b\n")
	repo.Commit("feat: add b")
	repo.Checkout("main")

	root, buf := buildPrecheckCmd(repo.Dir, "feature")
	if err := root.Execute(); err != nil {
		t.Fatalf("expected clean merge, got err: %v\noutput: %s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "clean merge") {
		t.Errorf("expected 'clean merge' in output, got: %s", out)
	}
}

func TestPrecheckCmd_ConflictingTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "base\n")
	repo.Commit("init")
	repo.CreateBranch("feature")
	repo.WriteFile("a.txt", "feature\n")
	repo.Commit("feat: feature edit")
	repo.Checkout("main")
	repo.WriteFile("a.txt", "main\n")
	repo.Commit("chore: main edit")

	root, buf := buildPrecheckCmd(repo.Dir, "feature")
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected ConflictError, got nil\noutput: %s", buf.String())
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	if ce.Code != 3 {
		t.Errorf("expected exit code 3, got %d", ce.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "a.txt") {
		t.Errorf("expected 'a.txt' in conflict list, got: %s", out)
	}
	if !strings.Contains(out, "conflict") {
		t.Errorf("expected 'conflict' in output, got: %s", out)
	}
}

func TestPrecheckCmd_JSON(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "base\n")
	repo.Commit("init")
	repo.CreateBranch("feature")
	repo.WriteFile("a.txt", "feature\n")
	repo.Commit("feat: edit")
	repo.Checkout("main")
	repo.WriteFile("a.txt", "main\n")
	repo.Commit("chore: edit")

	root, buf := buildPrecheckCmd(repo.Dir, "feature", "--json")
	err := root.Execute()
	// Conflicts exist, so a ConflictError is expected — but JSON still printed.
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConflictError, got %v", err)
	}

	var res precheckResult
	if jerr := json.Unmarshal(buf.Bytes(), &res); jerr != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", jerr, buf.String())
	}
	if res.Target != "feature" {
		t.Errorf("target=%q, want feature", res.Target)
	}
	if res.Clean {
		t.Error("expected Clean=false")
	}
	if len(res.Conflicts) == 0 || res.Conflicts[0] != "a.txt" {
		t.Errorf("conflicts=%v, want [a.txt]", res.Conflicts)
	}
}

func TestPrecheckCmd_UnknownTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, buf := buildPrecheckCmd(repo.Dir, "does-not-exist")
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected error for unknown target, got nil\noutput: %s", buf.String())
	}
	if !strings.Contains(err.Error(), "unknown target") {
		t.Errorf("expected 'unknown target' in err, got: %v", err)
	}
}

func TestPrecheckCmd_RejectsDashPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, _ := buildPrecheckCmd(repo.Dir, "--", "-rm")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected guardRef to reject -rm")
	}
	if !strings.Contains(err.Error(), "invalid target") {
		t.Errorf("expected 'invalid target' in err, got: %v", err)
	}
}

func TestPrecheckCmd_ExplicitBase(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "base\n")
	baseSHA := repo.Commit("init")

	repo.WriteFile("b.txt", "later\n")
	repo.Commit("chore: later")

	repo.CreateBranch("feature")
	repo.WriteFile("c.txt", "feat\n")
	repo.Commit("feat: c")
	repo.Checkout("main")

	// Without --base, merge-base auto-computes to the latest common ancestor.
	// Passing --base <init-sha> forces the older ancestor; since feature still
	// diverges cleanly, merge should still be clean — we only verify the flag
	// plumbing works end-to-end.
	root, buf := buildPrecheckCmd(repo.Dir, "feature", "--base", baseSHA)
	if err := root.Execute(); err != nil {
		t.Fatalf("expected clean with explicit base, got: %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "clean merge") {
		t.Errorf("expected 'clean merge', got: %s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Conflict forecast — default target + agent envelope
// ---------------------------------------------------------------------------

func precheckForecastCmd(t *testing.T, dir string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := &cobra.Command{Use: "precheck", Args: cobra.MaximumNArgs(1), RunE: runPrecheckCore, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().String("base", "", "")
	cmd.Flags().Bool("json", false, "")
	cmd.SetContext(context.Background())
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	return cmd, out
}

// makeForecastClone: downstream tracks origin/main; both sides edit the same
// line so the next pull would conflict.
func makeForecastClone(t *testing.T, conflicting bool) *testutil.Repo {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("shared.txt", "base line\n")
	upstream.Commit("seed: shared")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")

	upstream.WriteFile("shared.txt", "upstream line\n")
	upstream.Commit("feat: upstream edit")
	downstream.RunGit("fetch", "origin") // upstream tip visible locally

	if conflicting {
		downstream.WriteFile("shared.txt", "local line\n")
	} else {
		downstream.WriteFile("other.txt", "independent\n")
	}
	downstream.Commit("feat: local edit")
	return downstream
}

func TestIntegration_PrecheckDefaultTargetUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	down := makeForecastClone(t, true)

	cmd, out := precheckForecastCmd(t, down.Dir)
	err := cmd.Execute() // no target → @{u}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError(3) forecast, got %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "origin/main") || !strings.Contains(out.String(), "shared.txt") {
		t.Errorf("forecast output:\n%s", out.String())
	}
}

func TestIntegration_PrecheckDefaultTargetClean(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	down := makeForecastClone(t, false)

	cmd, out := precheckForecastCmd(t, down.Dir)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean forecast must exit 0: %v", err)
	}
	if !strings.Contains(out.String(), "clean merge") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestIntegration_PrecheckAgentEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	down := makeForecastClone(t, true)

	prevA, prevJ := flagAgent, flagJSON
	flagAgent, flagJSON = true, true
	t.Cleanup(func() { flagAgent, flagJSON = prevA, prevJ })

	cmd, out := precheckForecastCmd(t, down.Dir)
	_ = cmd.Execute() // conflict → exit-3 path, JSON already on stdout

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Ours      string   `json:"ours"`
			Target    string   `json:"target"`
			Clean     bool     `json:"clean"`
			Conflicts []string `json:"conflicts"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not valid envelope JSON: %v\n%s", err, out.String())
	}
	if !env.OK || env.Result.Clean || env.Result.Ours != "HEAD" || env.Result.Target != "origin/main" {
		t.Errorf("envelope: %+v", env)
	}
	if len(env.Result.Conflicts) != 1 || env.Result.Conflicts[0] != "shared.txt" {
		t.Errorf("conflicts: %v", env.Result.Conflicts)
	}
}

// TestIntegration_PrecheckDefaultTargetSameName: with no upstream but a
// same-name remote ref, the forecast must follow pull's resolution (Codex
// P2) — origin/<current>, not the base branch.
func TestIntegration_PrecheckDefaultTargetSameName(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")
	upstream.RunGit("branch", "feature")

	down := testutil.NewRepo(t)
	down.AddRemote("origin", upstream.Dir)
	down.RunGit("fetch", "origin")
	down.SetRemoteHEAD("origin", "main")
	down.RunGit("reset", "--hard", "origin/main")
	// On feature WITHOUT upstream config; origin/feature exists.
	down.RunGit("switch", "-q", "-c", "feature", "origin/feature")
	down.RunGit("branch", "--unset-upstream", "feature")

	cmd, out := precheckForecastCmd(t, down.Dir)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("forecast: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "origin/feature") {
		t.Errorf("must forecast origin/feature (pull's target), got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "origin/main") {
		t.Errorf("must not fall back to base when same-name ref exists:\n%s", out.String())
	}
}

// TestIntegration_PrecheckTrackingCacheMissing: tracking config intact but
// the remote-tracking ref absent — precheck is read-only, so it must refuse
// with the fetch remedy instead of silently forecasting the wrong branch.
func TestIntegration_PrecheckTrackingCacheMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")

	down := testutil.NewRepo(t)
	down.AddRemote("origin", upstream.Dir)
	down.RunGit("fetch", "origin")
	down.SetRemoteHEAD("origin", "main")
	down.RunGit("reset", "--hard", "origin/main")
	down.RunGit("branch", "--set-upstream-to=origin/main", "main")
	down.RunGit("update-ref", "-d", "refs/remotes/origin/main") // cache gone

	cmd, _ := precheckForecastCmd(t, down.Dir)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "remote-tracking ref is missing") {
		t.Fatalf("want missing-cache refusal, got %v", err)
	}
	r := RemediesFrom(err)
	if len(r) != 1 || !strings.Contains(r[0].Command, "git fetch origin main") {
		t.Errorf("remedies: %+v", r)
	}
}
