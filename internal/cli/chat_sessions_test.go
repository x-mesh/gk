package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/chat"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// setupChatSessionsRepo builds an isolated repo for `gk chat sessions`
// tests. It chdirs into the repo (t.Chdir auto-restores) rather than
// setting flagRepo: our test cmd.Flags() has no persistent "--repo" flag
// (that only exists once cobra merges it from rootCmd during a real
// Execute()), so config.Load's repo-root lookup falls back to cwd — the
// same path RepoFlag()=="" makes our own git.ExecRunner take. Both must
// agree on the repo for the config-driven retention test to see the right
// .gk.yaml. XDG_CONFIG_HOME is redirected to a throwaway dir so config.Load
// never picks up the developer's real global config.
func setupChatSessionsRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return repo
}

// chatSessionsCmd builds a bare *cobra.Command wired the way runChat's
// init() wires "sessions"/"prune": context set, output captured, and (for
// prune) the keep-days flag registered so cmd.Flags().GetInt/Changed work.
func chatSessionsListCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	return cmd, buf
}

func chatSessionsPruneCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd, buf := chatSessionsListCmd(t)
	cmd.Flags().Int("keep-days", 0, "")
	return cmd, buf
}

// newFixtureSession creates a session file directly (bypassing the chat
// engine — no provider needed) with one user turn, for list/prune fixtures.
func newFixtureSession(t *testing.T, repo *testutil.Repo, id, userText string) *chat.Session {
	t.Helper()
	runner := &git.ExecRunner{Dir: repo.Dir}
	s, err := chat.NewSession(context.Background(), runner, id)
	if err != nil {
		t.Fatalf("NewSession(%s): %v", id, err)
	}
	if err := s.Append(chat.SessionRecord{Role: "user", Text: userText}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := s.Append(chat.SessionRecord{Role: "assistant", Text: "ok"}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	return s
}

func TestChatSessionsList_NoSessions(t *testing.T) {
	setupChatSessionsRepo(t)
	cmd, buf := chatSessionsListCmd(t)
	if err := runChatSessionsList(cmd, nil); err != nil {
		t.Fatalf("runChatSessionsList: %v", err)
	}
	if !strings.Contains(buf.String(), "no gk chat sessions yet") {
		t.Errorf("output = %q, want the empty-state message", buf.String())
	}
}

func TestChatSessionsList_JSONEmpty(t *testing.T) {
	setupChatSessionsRepo(t)
	withAgentMode(t, false)
	prevJSON := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJSON })

	cmd, buf := chatSessionsListCmd(t)
	if err := runChatSessionsList(cmd, nil); err != nil {
		t.Fatalf("runChatSessionsList: %v", err)
	}
	var got []chatSessionJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, buf.String())
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want an empty array", got)
	}
}

// TestChatSessionsList_HumanAndJSON creates two sessions — one renamed via
// SetTitle, one left to fall back to its first user message — and checks
// both the human table and the agent-enveloped JSON: newest-first order,
// the "current" (last-session pointer) marker, title precedence, and turn
// counts.
func TestChatSessionsList_HumanAndJSON(t *testing.T) {
	repo := setupChatSessionsRepo(t)

	older := newFixtureSession(t, repo, "20260101-000000-1", "오래된 질문")
	if err := older.Append(chat.SessionRecord{Role: "user", Text: "두번째 턴"}); err != nil {
		t.Fatal(err)
	}
	newer := newFixtureSession(t, repo, "20260601-000000-2", "최신 질문")
	if err := newer.SetTitle("커스텀 제목"); err != nil {
		t.Fatal(err)
	}

	// newer was opened last, so it is the --continue (last-session) target.
	runner := &git.ExecRunner{Dir: repo.Dir}
	current := chat.LastSessionID(context.Background(), runner)
	if current != "20260601-000000-2" {
		t.Fatalf("LastSessionID = %q, want the most recently opened session", current)
	}

	// Human table.
	cmd, buf := chatSessionsListCmd(t)
	if err := runChatSessionsList(cmd, nil); err != nil {
		t.Fatalf("runChatSessionsList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "커스텀 제목") {
		t.Errorf("table missing custom title: %s", out)
	}
	if !strings.Contains(out, "오래된 질문") {
		t.Errorf("table missing fallback title: %s", out)
	}
	// newest-first: the renamed (newer) session's line precedes the older one.
	if strings.Index(out, "커스텀 제목") > strings.Index(out, "오래된 질문") {
		t.Errorf("expected newer session listed before older one: %s", out)
	}

	// Agent-enveloped JSON.
	withAgentMode(t, true)
	cmd2, buf2 := chatSessionsListCmd(t)
	if err := runChatSessionsList(cmd2, nil); err != nil {
		t.Fatalf("runChatSessionsList (agent): %v", err)
	}
	var env struct {
		State  string            `json:"state"`
		Result []chatSessionJSON `json:"result"`
	}
	if err := json.Unmarshal(buf2.Bytes(), &env); err != nil {
		t.Fatalf("agent output not valid JSON: %v\n%s", err, buf2.String())
	}
	if env.State != envStateOK {
		t.Fatalf("state = %q, want ok", env.State)
	}
	if len(env.Result) != 2 {
		t.Fatalf("result = %+v, want 2 entries", env.Result)
	}
	if env.Result[0].ID != "20260601-000000-2" || env.Result[1].ID != "20260101-000000-1" {
		t.Errorf("order = [%s, %s], want [newer, older]", env.Result[0].ID, env.Result[1].ID)
	}
	if env.Result[0].Title != "커스텀 제목" {
		t.Errorf("Result[0].Title = %q, want the /rename title", env.Result[0].Title)
	}
	if !env.Result[0].Current {
		t.Errorf("Result[0].Current = false, want true (it is the last-session pointer)")
	}
	if env.Result[1].Current {
		t.Errorf("Result[1].Current = true, want false")
	}
	if env.Result[1].Turns != 2 {
		t.Errorf("Result[1].Turns = %d, want 2", env.Result[1].Turns)
	}
}

// TestChatSessionsPrune_DefaultOff confirms prune is a no-op — files kept,
// zero error — when neither --keep-days nor ai.chat.session_retention_days
// is set. This is the "off by default" decision the task calls for.
func TestChatSessionsPrune_DefaultOff(t *testing.T) {
	repo := setupChatSessionsRepo(t)
	newFixtureSession(t, repo, "20200101-000000-1", "old")
	backdateSessionFile(t, repo, "20200101-000000-1", 365*24*time.Hour)

	cmd, buf := chatSessionsPruneCmd(t)
	if err := runChatSessionsPrune(cmd, nil); err != nil {
		t.Fatalf("runChatSessionsPrune: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing pruned") {
		t.Errorf("output = %q, want the opt-in hint", buf.String())
	}
	assertSessionExists(t, repo, "20200101-000000-1")
}

// TestChatSessionsPrune_KeepDaysFlag confirms an explicit --keep-days
// deletes sessions whose file is inactive past the window, while a fresh
// session survives.
func TestChatSessionsPrune_KeepDaysFlag(t *testing.T) {
	repo := setupChatSessionsRepo(t)
	newFixtureSession(t, repo, "20200101-000000-old", "old")
	backdateSessionFile(t, repo, "20200101-000000-old", 30*24*time.Hour)
	newFixtureSession(t, repo, "20260601-000000-fresh", "fresh")

	cmd, buf := chatSessionsPruneCmd(t)
	if err := cmd.Flags().Set("keep-days", "7"); err != nil {
		t.Fatal(err)
	}
	if err := runChatSessionsPrune(cmd, nil); err != nil {
		t.Fatalf("runChatSessionsPrune: %v", err)
	}
	if !strings.Contains(buf.String(), "pruned") {
		t.Errorf("output = %q, want a pruned summary", buf.String())
	}
	assertSessionGone(t, repo, "20200101-000000-old")
	assertSessionExists(t, repo, "20260601-000000-fresh")
}

// TestChatSessionsPrune_ProtectsCurrentSession backdates the session the
// last-session pointer targets past the retention window and confirms
// prune still leaves it alone — deleting the file a bare `gk chat
// --continue` is about to reopen would be a worse surprise than one
// stale-looking entry.
func TestChatSessionsPrune_ProtectsCurrentSession(t *testing.T) {
	repo := setupChatSessionsRepo(t)
	newFixtureSession(t, repo, "20200101-000000-current", "current")
	backdateSessionFile(t, repo, "20200101-000000-current", 30*24*time.Hour)

	runner := &git.ExecRunner{Dir: repo.Dir}
	if got := chat.LastSessionID(context.Background(), runner); got != "20200101-000000-current" {
		t.Fatalf("LastSessionID = %q, want the only session created", got)
	}

	cmd, _ := chatSessionsPruneCmd(t)
	if err := cmd.Flags().Set("keep-days", "7"); err != nil {
		t.Fatal(err)
	}
	if err := runChatSessionsPrune(cmd, nil); err != nil {
		t.Fatalf("runChatSessionsPrune: %v", err)
	}
	assertSessionExists(t, repo, "20200101-000000-current")
}

// TestChatSessionsPrune_ConfigRetentionDays confirms ai.chat.session_retention_days
// in .gk.yaml supplies the default window when --keep-days is not passed —
// the same fallback shape as gk snapshot prune's retention_days.
func TestChatSessionsPrune_ConfigRetentionDays(t *testing.T) {
	repo := setupChatSessionsRepo(t)
	newFixtureSession(t, repo, "20200101-000000-old", "old")
	backdateSessionFile(t, repo, "20200101-000000-old", 30*24*time.Hour)
	// A second, fresher session moves the last-session (protected) pointer
	// off "old" — otherwise, being the only session created, it would be
	// the --continue target and never eligible for pruning regardless of
	// the retention window (see TestChatSessionsPrune_ProtectsCurrentSession).
	newFixtureSession(t, repo, "20260601-000000-fresh", "fresh")
	if err := os.WriteFile(filepath.Join(repo.Dir, ".gk.yaml"), []byte("ai:\n  chat:\n    session_retention_days: 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, buf := chatSessionsPruneCmd(t)
	if err := runChatSessionsPrune(cmd, nil); err != nil {
		t.Fatalf("runChatSessionsPrune: %v", err)
	}
	if !strings.Contains(buf.String(), "pruned") {
		t.Errorf("output = %q, want a pruned summary (config-driven retention)", buf.String())
	}
	assertSessionGone(t, repo, "20200101-000000-old")
}

// backdateSessionFile pushes a session file's mtime back by age, the
// signal runChatSessionsPrune uses to decide staleness.
func backdateSessionFile(t *testing.T, repo *testutil.Repo, id string, age time.Duration) {
	t.Helper()
	path := filepath.Join(repo.Dir, ".git", "gk-chat", "sessions", id+".jsonl")
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("backdate %s: %v", path, err)
	}
}

func assertSessionExists(t *testing.T, repo *testutil.Repo, id string) {
	t.Helper()
	path := filepath.Join(repo.Dir, ".git", "gk-chat", "sessions", id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected session %s to still exist: %v", id, err)
	}
}

func assertSessionGone(t *testing.T, repo *testutil.Repo, id string) {
	t.Helper()
	path := filepath.Join(repo.Dir, ".git", "gk-chat", "sessions", id+".jsonl")
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected session %s to be pruned", id)
	}
}

// TestChatSessionsCmd_QuestionArgsNotRejected pins the v2 finding: adding
// the `sessions` subcommand must not turn a one-shot question that starts
// with "sessions" into a cobra "accepts no args" error. The subcommand's
// Args validator must accept extra tokens (they get delegated to the
// one-shot path), and a bare invocation (list) must still be valid.
func TestChatSessionsCmd_QuestionArgsNotRejected(t *testing.T) {
	cmd := newChatSessionsCmd()
	if cmd.Args == nil {
		t.Fatal("sessions cmd has nil Args validator")
	}
	if err := cmd.Args(cmd, []string{"where", "are", "they", "stored"}); err != nil {
		t.Errorf("Args rejected a question-shaped invocation: %v — must be accepted and delegated, not errored", err)
	}
	if err := cmd.Args(cmd, nil); err != nil {
		t.Errorf("Args rejected the bare list invocation: %v", err)
	}
}
