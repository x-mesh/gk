package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/testutil"
)

// ─── t16: rewriteRebaseTodo (the hidden editor's whole job) ───

func TestRewriteRebaseTodoCopiesPrepared(t *testing.T) {
	dir := t.TempDir()
	prepared := filepath.Join(dir, "prepared")
	todo := filepath.Join(dir, "git-rebase-todo")
	if err := os.WriteFile(prepared, []byte("pick abc123\ndrop def456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(todo, []byte("pick abc123\npick def456\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteRebaseTodo(todo, prepared); err != nil {
		t.Fatalf("rewriteRebaseTodo: %v", err)
	}
	got, _ := os.ReadFile(todo)
	if string(got) != "pick abc123\ndrop def456\n" {
		t.Errorf("todo not replaced, got:\n%s", got)
	}
}

func TestRewriteRebaseTodoRequiresEnv(t *testing.T) {
	err := rewriteRebaseTodo(filepath.Join(t.TempDir(), "todo"), "")
	if err == nil || !strings.Contains(err.Error(), rebaseTodoEnv) {
		t.Errorf("want error naming %s, got %v", rebaseTodoEnv, err)
	}
}

func TestRewriteRebaseTodoMissingPrepared(t *testing.T) {
	err := rewriteRebaseTodo(filepath.Join(t.TempDir(), "todo"), filepath.Join(t.TempDir(), "absent"))
	if err == nil || !strings.Contains(err.Error(), "read prepared todo") {
		t.Errorf("want read error, got %v", err)
	}
}

// ─── t17: gk rebase --plan integration (real repos) ───

// newRebaseCmd mirrors the production flag set on a fresh command so tests
// drive runRebasePlan exactly like the CLI does.
func newRebaseCmd(t *testing.T, repoDir string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := &cobra.Command{Use: "rebase", Args: cobra.NoArgs, RunE: runRebasePlan, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().String("plan", "", "")
	cmd.Flags().Bool("plan-template", false, "")
	cmd.Flags().String("onto", "", "")
	cmd.Flags().Bool("allow-pushed", false, "")
	cmd.SetOut(out)
	cmd.SetErr(errBuf)
	prev := flagRepo
	flagRepo = repoDir
	t.Cleanup(func() { flagRepo = prev })
	return cmd, out, errBuf
}

// setRebaseTestEnv arms the TestMain helper-process branch (the test binary
// acts as GIT_SEQUENCE_EDITOR) and isolates the spawned git from user config.
func setRebaseTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GK_TEST_SEQUENCE_EDITOR", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "gk-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "gk-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

func writePlanFile(t *testing.T, entries []rebasePlanEntry) string {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRebasePlanTemplate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	base := repo.RunGit("rev-parse", "HEAD")
	repo.WriteFile("a.txt", "one\n")
	c1 := repo.Commit("feat: one")
	repo.WriteFile("b.txt", "b\n")
	c2 := repo.Commit("feat: two")

	cmd, out, _ := newRebaseCmd(t, repo.Dir)
	cmd.SetArgs([]string{"--plan-template", "--onto", base})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("plan-template: %v", err)
	}
	var tpl rebaseTemplateJSON
	if err := json.Unmarshal(out.Bytes(), &tpl); err != nil {
		t.Fatalf("template is not JSON: %v\n%s", err, out.String())
	}
	if tpl.Onto != base {
		t.Errorf("onto = %q, want %q", tpl.Onto, base)
	}
	if len(tpl.Commits) != 2 || tpl.Commits[0].Commit != c1 || tpl.Commits[1].Commit != c2 {
		t.Fatalf("commits = %+v, want [%s %s] oldest-first", tpl.Commits, c1, c2)
	}
	for _, c := range tpl.Commits {
		if c.Action != "pick" {
			t.Errorf("template action = %q, want pick", c.Action)
		}
	}
	if tpl.Commits[0].Subject != "feat: one" {
		t.Errorf("subject = %q, want %q", tpl.Commits[0].Subject, "feat: one")
	}
}

// The headline scenario: a mixed squash+reword+drop plan runs to completion
// without any editor session, and the resulting history is exactly what the
// plan declared.
func TestRebasePlanMixedExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	setRebaseTestEnv(t)
	repo := testutil.NewRepo(t)
	base := repo.RunGit("rev-parse", "HEAD")
	repo.WriteFile("a.txt", "one\n")
	c1 := repo.Commit("feat: one")
	repo.WriteFile("a.txt", "one\ntwo\n")
	c2 := repo.Commit("squash: noise")
	repo.WriteFile("b.txt", "b\n")
	c3 := repo.Commit("bad message")
	repo.WriteFile("c.txt", "c\n")
	c4 := repo.Commit("drop me")

	plan := writePlanFile(t, []rebasePlanEntry{
		{Action: "pick", Commit: c1},
		{Action: "squash", Commit: c2},
		{Action: "reword", Commit: c3, Message: "feat: better words\n\nrewritten by plan"},
		{Action: "drop", Commit: c4},
	})
	cmd, _, errBuf := newRebaseCmd(t, repo.Dir)
	cmd.SetArgs([]string{"--plan", plan, "--onto", base})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("rebase --plan: %v\nstderr:\n%s", err, errBuf.String())
	}

	subjects := repo.RunGit("log", "--reverse", "--format=%s", base+"..HEAD")
	want := "feat: one\nfeat: better words"
	if subjects != want {
		t.Errorf("history subjects = %q, want %q", subjects, want)
	}
	// squash kept both messages in the combined body
	combined := repo.RunGit("log", "--reverse", "--format=%B", base+"..HEAD")
	if !strings.Contains(combined, "squash: noise") {
		t.Errorf("squashed message lost, full bodies:\n%s", combined)
	}
	if !strings.Contains(combined, "rewritten by plan") {
		t.Errorf("reword body lost, full bodies:\n%s", combined)
	}
	// tree state: squash kept content, drop removed it
	if got := repo.RunGit("show", "HEAD:a.txt"); got != "one\ntwo" {
		t.Errorf("a.txt = %q, want squashed content", got)
	}
	repo.RunGit("cat-file", "-e", "HEAD:b.txt")
	if _, err := repo.TryGit("cat-file", "-e", "HEAD:c.txt"); err == nil {
		t.Error("c.txt should have been dropped from HEAD")
	}
	// safety net: backup ref points at the pre-rebase tip
	if got := repo.RunGit("for-each-ref", "refs/gk/backup", "--format=%(objectname)"); got != c4 {
		t.Errorf("backup ref = %q, want pre-rebase tip %s", got, c4)
	}
	if !strings.Contains(errBuf.String(), "rebase complete") {
		t.Errorf("missing completion line, stderr:\n%s", errBuf.String())
	}
}

func TestRebasePlanDryRunTouchesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	setRebaseTestEnv(t)
	repo := testutil.NewRepo(t)
	base := repo.RunGit("rev-parse", "HEAD")
	repo.WriteFile("a.txt", "one\n")
	c1 := repo.Commit("feat: one")
	repo.WriteFile("b.txt", "b\n")
	c2 := repo.Commit("drop me")

	prevDry := flagDryRun
	flagDryRun = true
	t.Cleanup(func() { flagDryRun = prevDry })

	plan := writePlanFile(t, []rebasePlanEntry{
		{Action: "reword", Commit: c1, Message: "feat: renamed"},
		{Action: "drop", Commit: c2},
	})
	cmd, _, errBuf := newRebaseCmd(t, repo.Dir)
	cmd.SetArgs([]string{"--plan", plan, "--onto", base})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if head := repo.RunGit("rev-parse", "HEAD"); head != c2 {
		t.Errorf("dry-run moved HEAD: %s, want %s", head, c2)
	}
	if got := repo.RunGit("for-each-ref", "refs/gk/backup"); got != "" {
		t.Errorf("dry-run created backup refs: %s", got)
	}
	stderr := errBuf.String()
	if !strings.Contains(stderr, "drop "+c2) || !strings.Contains(stderr, "pick "+c1) {
		t.Errorf("todo preview missing from dry-run output:\n%s", stderr)
	}
}

// A plan that drops a commit a later one depends on must pause on the
// conflict with the standard contract (exit-3 ConflictError, resumable
// state), and a second plan invocation must refuse to stack on top of it.
func TestRebasePlanConflictContract(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	setRebaseTestEnv(t)
	repo := testutil.NewRepo(t)
	base := repo.RunGit("rev-parse", "HEAD")
	repo.WriteFile("f.txt", "line1\n")
	c1 := repo.Commit("c1")
	repo.WriteFile("f.txt", "line1\nline2\n")
	c2 := repo.Commit("c2")

	planJSON, err := json.Marshal([]rebasePlanEntry{
		{Action: "drop", Commit: c1},
		{Action: "pick", Commit: c2},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd, _, errBuf := newRebaseCmd(t, repo.Dir)
	cmd.SetIn(bytes.NewReader(planJSON)) // exercise --plan - (stdin)
	cmd.SetArgs([]string{"--plan", "-", "--onto", base})
	runErr := cmd.ExecuteContext(context.Background())
	var ce *ConflictError
	if !errors.As(runErr, &ce) || ce.Code != 3 {
		t.Fatalf("want ConflictError{Code:3}, got %v\nstderr:\n%s", runErr, errBuf.String())
	}
	st, derr := gitstate.Detect(context.Background(), repo.Dir)
	if derr != nil || st.Kind == gitstate.StateNone {
		t.Fatalf("expected paused rebase state, got kind=%v err=%v", st.Kind, derr)
	}
	if !strings.Contains(errBuf.String(), "gk continue") {
		t.Errorf("conflict guidance missing:\n%s", errBuf.String())
	}

	// stacking guard: a new plan on a paused repo is refused
	cmd2, _, _ := newRebaseCmd(t, repo.Dir)
	cmd2.SetArgs([]string{"--plan-template", "--onto", base})
	if err := cmd2.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Errorf("want in-progress refusal, got %v", err)
	}

	repo.RunGit("rebase", "--abort")
	if head := repo.RunGit("rev-parse", "HEAD"); head != c2 {
		t.Errorf("abort did not restore HEAD: %s, want %s", head, c2)
	}
}

func TestRebasePlanPushedGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	setRebaseTestEnv(t)
	repo := testutil.NewRepo(t)
	base := repo.RunGit("rev-parse", "HEAD")
	repo.WriteFile("a.txt", "one\n")
	c1 := repo.Commit("feat: one")
	repo.WriteFile("b.txt", "b\n")
	c2 := repo.Commit("feat: two")
	// Simulate "already on a remote" without a network: a remote-tracking
	// ref covering the tip — collectPushedShas falls back to --remotes.
	repo.AddRemote("origin", repo.Dir)
	repo.RunGit("update-ref", "refs/remotes/origin/main", c2)

	plan := writePlanFile(t, []rebasePlanEntry{
		{Action: "reword", Commit: c1, Message: "feat: rewritten"},
		{Action: "pick", Commit: c2},
	})
	cmd, _, _ := newRebaseCmd(t, repo.Dir)
	cmd.SetArgs([]string{"--plan", plan, "--onto", base})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already on a remote") {
		t.Fatalf("want pushed-guard rejection, got %v", err)
	}
	if head := repo.RunGit("rev-parse", "HEAD"); head != c2 {
		t.Errorf("rejected plan moved HEAD: %s, want %s", head, c2)
	}

	// --allow-pushed lifts the guard and the rewrite goes through
	cmd2, _, errBuf2 := newRebaseCmd(t, repo.Dir)
	cmd2.SetArgs([]string{"--plan", plan, "--onto", base, "--allow-pushed"})
	if err := cmd2.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("--allow-pushed run: %v\nstderr:\n%s", err, errBuf2.String())
	}
	subjects := repo.RunGit("log", "--reverse", "--format=%s", base+"..HEAD")
	if subjects != "feat: rewritten\nfeat: two" {
		t.Errorf("history = %q, want reworded subjects", subjects)
	}
}
