package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// newPlanTestCmd builds a minimal cobra.Command wired for the deterministic
// plan paths: stdin/stdout/stderr buffers and a context. flagRepo is pointed at
// the test repo so RepoFlag() resolves correctly; callers restore it via the
// returned cleanup (t.Cleanup).
func newPlanTestCmd(t *testing.T, dir string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })

	cmd := &cobra.Command{Use: "commit", SilenceUsage: true, SilenceErrors: true}
	cmd.SetContext(context.Background())
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd, stdout, stderr
}

// withPlanJSON flips the global --json flag for the duration of a test so the
// plan path emits its machine contract to stdout.
func withPlanJSON(t *testing.T) {
	t.Helper()
	prev := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prev })
}

// planTestAI is the AIConfig the plan paths read for DenyPaths — empty deny
// list keeps every test file in scope.
func planTestAI() config.AIConfig { return config.AIConfig{} }

// planTestCfg returns a config whose commit rules accept the conventional
// types used across the table (feat/fix/chore/docs).
func planTestCfg() *config.Config {
	return &config.Config{
		Commit: config.CommitConfig{
			Types:            []string{"feat", "fix", "chore", "docs", "refactor"},
			MaxSubjectLength: 72,
		},
	}
}

// runPlan invokes runAICommitPlan with the plan fed on stdin ("-").
func runPlan(t *testing.T, repo *testutil.Repo, plan string, mutate func(*aiCommitFlags)) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	cmd, stdout, stderr := newPlanTestCmd(t, repo.Dir)
	cmd.SetIn(strings.NewReader(plan))
	runner := &git.ExecRunner{Dir: repo.Dir}
	flags := aiCommitFlags{plan: "-"}
	if mutate != nil {
		mutate(&flags)
	}
	err := runAICommitPlan(cmd, context.Background(), runner, planTestCfg(), planTestAI(), flags)
	return stdout, stderr, err
}

// dirtyThreeFiles writes three tracked-then-modified files plus is on a feature
// branch, returning the repo. Files: a.txt, b.txt, c.txt all "modified".
func dirtyThreeFiles(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	// Seed the three files in a base commit so they show up as "modified".
	repo.WriteFile("a.txt", "a0\n")
	repo.WriteFile("b.txt", "b0\n")
	repo.WriteFile("c.txt", "c0\n")
	repo.Commit("seed three files")
	// Dirty them.
	repo.WriteFile("a.txt", "a1\n")
	repo.WriteFile("b.txt", "b1\n")
	repo.WriteFile("c.txt", "c1\n")
	return repo
}

func commitCount(t *testing.T, repo *testutil.Repo) int {
	t.Helper()
	out := repo.RunGit("rev-list", "--count", "HEAD")
	n := 0
	for _, c := range out {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func TestRunAICommitPlan_HappyPath(t *testing.T) {
	withPlanJSON(t)
	repo := dirtyThreeFiles(t)
	before := commitCount(t, repo)

	// Two commits covering a.txt and b.txt; c.txt is intentionally left out.
	plan := `{"schema":1,"commits":[
		{"message":"feat(a): change a","files":["a.txt"]},
		{"message":"fix(b): change b","files":["b.txt"]}
	]}`
	stdout, _, err := runPlan(t, repo, plan, nil)
	if err != nil {
		t.Fatalf("runAICommitPlan: %v", err)
	}

	if got := commitCount(t, repo) - before; got != 2 {
		t.Fatalf("created %d commit(s), want 2", got)
	}

	// c.txt must still be a dirty (uncovered) working-tree change.
	status := repo.RunGit("status", "--porcelain")
	if !strings.Contains(status, "c.txt") {
		t.Fatalf("c.txt should remain dirty; status:\n%s", status)
	}
	if strings.Contains(status, "a.txt") || strings.Contains(status, "b.txt") {
		t.Fatalf("a.txt/b.txt should be committed; status:\n%s", status)
	}

	// Result JSON: completed, 2 ok commits each with a SHA.
	var res commitPlanResultJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &res); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if res.Result != "completed" || len(res.Commits) != 2 {
		t.Fatalf("result = %+v, want completed with 2 commits", res)
	}
	for i, c := range res.Commits {
		if c.Result != "ok" || c.SHA == "" {
			t.Errorf("commit %d = %+v, want ok with SHA", i, c)
		}
	}
	if res.BackupRef == "" {
		t.Error("expected a backup_ref in the result")
	}
}

func TestRunAICommitPlan_DuplicateFileRejected(t *testing.T) {
	repo := dirtyThreeFiles(t)
	before := commitCount(t, repo)

	plan := `{"schema":1,"commits":[
		{"message":"feat(a): one","files":["a.txt"]},
		{"message":"fix(a): two","files":["a.txt"]}
	]}`
	_, _, err := runPlan(t, repo, plan, nil)
	if err == nil {
		t.Fatal("want error on duplicate file across commits")
	}
	if !strings.Contains(err.Error(), "more than one commit") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := commitCount(t, repo) - before; got != 0 {
		t.Fatalf("created %d commit(s) on a rejected plan, want 0", got)
	}
}

func TestRunAICommitPlan_DryRunMakesNoCommits(t *testing.T) {
	withPlanJSON(t)
	repo := dirtyThreeFiles(t)
	before := commitCount(t, repo)
	headBefore := repo.RunGit("rev-parse", "HEAD")

	plan := `{"schema":1,"commits":[
		{"message":"feat(a): change a","files":["a.txt"]}
	]}`
	stdout, _, err := runPlan(t, repo, plan, func(f *aiCommitFlags) { f.dryRun = true })
	if err != nil {
		t.Fatalf("dry-run plan: %v", err)
	}
	if got := commitCount(t, repo) - before; got != 0 {
		t.Fatalf("dry-run created %d commit(s), want 0", got)
	}
	if head := repo.RunGit("rev-parse", "HEAD"); head != headBefore {
		t.Fatalf("HEAD moved during dry-run: %s -> %s", headBefore, head)
	}

	var res commitPlanResultJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &res); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if res.Result != "dry-run" {
		t.Fatalf("result = %s, want dry-run", res.Result)
	}
	if len(res.Commits) != 1 || res.Commits[0].Result != "dry-run" {
		t.Fatalf("commits = %+v", res.Commits)
	}
}

func TestRunAICommitPlan_MissingFileRejected(t *testing.T) {
	repo := dirtyThreeFiles(t)
	plan := `{"schema":1,"commits":[
		{"message":"feat(x): ghost","files":["ghost.txt"]}
	]}`
	_, _, err := runPlan(t, repo, plan, nil)
	if err == nil {
		t.Fatal("want error on a file with no working-tree change")
	}
	if !strings.Contains(err.Error(), "no working-tree change") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRunAICommitPlanTemplate covers the draft emission: dirty files become a
// single plan entry whose files list and informational status/kind are filled.
func TestRunAICommitPlanTemplate(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("svc.go", "package svc\n")
	repo.WriteFile("README.md", "# docs\n")

	cmd, stdout, _ := newPlanTestCmd(t, repo.Dir)
	runner := &git.ExecRunner{Dir: repo.Dir}
	if err := runAICommitPlanTemplate(cmd, context.Background(), runner, planTestAI()); err != nil {
		t.Fatalf("runAICommitPlanTemplate: %v", err)
	}

	var tpl commitPlanJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &tpl); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if tpl.Schema != 1 || len(tpl.Commits) != 1 {
		t.Fatalf("template = %+v, want one entry", tpl)
	}
	entry := tpl.Commits[0]
	if len(entry.Files) != 2 {
		t.Fatalf("entry.Files = %v, want 2 dirty files", entry.Files)
	}
	if entry.Message != "" {
		t.Errorf("template message should be blank for the agent to fill, got %q", entry.Message)
	}
	if entry.Status == "" {
		t.Errorf("expected an informational status on the entry")
	}
	// Both files are untracked here, so Status should be "untracked".
	if entry.Status != "untracked" {
		t.Errorf("status = %q, want untracked", entry.Status)
	}
	// The template must round-trip back through readCommitPlan (status/kind are
	// accepted-but-ignored, not rejected by DisallowUnknownFields).
	if _, err := readCommitPlan(bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("template does not round-trip as a plan: %v", err)
	}
}

// TestRunAICommitPlan_AgentEnvelope: under GK_AGENT (flagAgent+flagJSON) the
// result is wrapped in the {ok,result} envelope and stdout parses cleanly.
func TestRunAICommitPlan_AgentEnvelope(t *testing.T) {
	prevAgent, prevJSON := flagAgent, flagJSON
	flagAgent, flagJSON = true, true
	t.Cleanup(func() { flagAgent, flagJSON = prevAgent, prevJSON })

	repo := dirtyThreeFiles(t)
	plan := `{"schema":1,"commits":[
		{"message":"feat(a): change a","files":["a.txt"]}
	]}`
	stdout, _, err := runPlan(t, repo, plan, nil)
	if err != nil {
		t.Fatalf("runAICommitPlan: %v", err)
	}

	// The envelope wraps the payload in {schema, ok, result}.
	var env struct {
		OK     bool                 `json:"ok"`
		Result commitPlanResultJSON `json:"result"`
	}
	if jerr := json.Unmarshal(stdout.Bytes(), &env); jerr != nil {
		t.Fatalf("stdout not a valid envelope: %v\n%s", jerr, stdout.String())
	}
	if !env.OK {
		t.Fatalf("envelope ok = false:\n%s", stdout.String())
	}
	if env.Result.Result != "completed" || len(env.Result.Commits) != 1 {
		t.Fatalf("envelope result = %+v", env.Result)
	}
	if env.Result.Commits[0].SHA == "" {
		t.Error("expected a commit SHA in the enveloped result")
	}
}

// TestRunAICommitPlanTemplate_Empty: a clean tree yields an empty (non-failing)
// plan.
func TestRunAICommitPlanTemplate_Empty(t *testing.T) {
	repo := testutil.NewRepo(t) // clean after init
	cmd, stdout, _ := newPlanTestCmd(t, repo.Dir)
	runner := &git.ExecRunner{Dir: repo.Dir}
	if err := runAICommitPlanTemplate(cmd, context.Background(), runner, planTestAI()); err != nil {
		t.Fatalf("runAICommitPlanTemplate (clean): %v", err)
	}
	var tpl commitPlanJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &tpl); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if len(tpl.Commits) != 0 {
		t.Fatalf("clean tree should emit an empty plan, got %+v", tpl)
	}
}
