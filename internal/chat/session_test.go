package chat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

func sessionFixture(t *testing.T) (git.Runner, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return &git.ExecRunner{Dir: root}, root
}

func TestSessionRoundTrip(t *testing.T) {
	runner, _ := sessionFixture(t)
	ctx := context.Background()

	s, err := NewSession(ctx, runner, "20260704-abc")
	if err != nil {
		t.Fatal(err)
	}
	records := []SessionRecord{
		{TS: time.Now(), Role: "user", Text: "무엇이 바뀌었지?"},
		{TS: time.Now(), Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "git_log"}}},
		{TS: time.Now(), Role: "tool", ToolResult: &provider.ToolResult{ToolCallID: "c1", Content: "abc123 fix things"}},
		{TS: time.Now(), Role: "assistant", Text: "최근 커밋은 abc123입니다.", Model: "m", TokensUsed: 10},
	}
	for _, r := range records {
		if err := s.Append(r); err != nil {
			t.Fatal(err)
		}
	}

	if got := LastSessionID(ctx, runner); got != "20260704-abc" {
		t.Errorf("LastSessionID = %q", got)
	}

	re, err := OpenSession(ctx, runner, "20260704-abc")
	if err != nil {
		t.Fatal(err)
	}
	msgs, skipped, err := re.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].ToolCalls[0].ID != "c1" ||
		msgs[2].ToolResult.ToolCallID != "c1" || msgs[3].Text == "" {
		t.Errorf("replayed shape wrong: %+v", msgs)
	}
}

// A torn final line (crash mid-append) must cost exactly that line, and
// junk lines must not kill the session.
func TestSessionReplayTolerantOfCorruption(t *testing.T) {
	runner, root := sessionFixture(t)
	ctx := context.Background()

	s, err := NewSession(ctx, runner, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	// A COMPLETE turn — replay's structural sanitizer strips trailing
	// incomplete turns, so the survivable prefix must end on an assistant
	// answer.
	if err := s.Append(SessionRecord{Role: "user", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(SessionRecord{Role: "assistant", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write: torn JSON tail.
	path := filepath.Join(root, ".git", "gk-chat", "sessions", "corrupt.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"ts":"2026-07-04T00:00:00Z","role":"assist`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	msgs, skipped, err := s.Replay()
	if err != nil {
		t.Fatalf("Replay must tolerate torn tail: %v", err)
	}
	if len(msgs) != 2 || skipped != 1 {
		t.Errorf("msgs=%d skipped=%d, want 2/1", len(msgs), skipped)
	}
}

// Empty file (created then crashed) and unknown roles degrade gracefully.
func TestSessionReplayDegenerate(t *testing.T) {
	runner, _ := sessionFixture(t)
	ctx := context.Background()

	s, err := NewSession(ctx, runner, "empty")
	if err != nil {
		t.Fatal(err)
	}
	msgs, skipped, err := s.Replay()
	if err != nil || len(msgs) != 0 || skipped != 0 {
		t.Errorf("empty session: msgs=%d skipped=%d err=%v", len(msgs), skipped, err)
	}

	if err := s.Append(SessionRecord{Role: "alien", Text: "??"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(SessionRecord{Role: "tool"}); err != nil { // missing result
		t.Fatal(err)
	}
	msgs, skipped, err = s.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 || skipped != 2 {
		t.Errorf("degenerate records: msgs=%d skipped=%d", len(msgs), skipped)
	}
}

func TestOpenSessionMissing(t *testing.T) {
	runner, _ := sessionFixture(t)
	if _, err := OpenSession(context.Background(), runner, "nope"); err == nil {
		t.Error("OpenSession on missing id must error")
	}
}

// The session dir must come from rev-parse --git-path (worktree-safe),
// which for a normal repo lands under .git/gk-chat.
func TestSessionLocation(t *testing.T) {
	runner, root := sessionFixture(t)
	if _, err := NewSession(context.Background(), runner, "loc"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".git", "gk-chat", "sessions", "loc.jsonl")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("session file not at %s: %v", want, err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".git", "gk-chat", "last-session"))
	if err != nil || strings.TrimSpace(string(b)) != "loc" {
		t.Errorf("last-session = %q err=%v", b, err)
	}
}

// TestSessionSetTitleWinsOverFirstUserMessage confirms an explicit /rename
// (SetTitle) takes priority over the first-user-message fallback in
// ListSessions, and that renaming twice keeps the LAST title — SetTitle is
// append-only, never a rewrite.
func TestSessionSetTitleWinsOverFirstUserMessage(t *testing.T) {
	runner, _ := sessionFixture(t)
	ctx := context.Background()

	s, err := NewSession(ctx, runner, "titled")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(SessionRecord{Role: "user", Text: "이 함수 언제 왜 바뀌었지?"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(SessionRecord{Role: "assistant", Text: "..."}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle("first title"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle("final title"); err != nil {
		t.Fatal(err)
	}

	metas, err := ListSessions(ctx, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1", len(metas))
	}
	if metas[0].Title != "final title" {
		t.Errorf("Title = %q, want %q (latest rename should win)", metas[0].Title, "final title")
	}
	if metas[0].TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", metas[0].TurnCount)
	}
}

// TestListSessionsFallsBackToFirstUserMessage covers a session with no
// /rename at all — the title must fall back to the first user message,
// truncated to sessionTitleMaxLen runes (not bytes, so Korean text is not
// split mid-character).
func TestListSessionsFallsBackToFirstUserMessage(t *testing.T) {
	runner, _ := sessionFixture(t)
	ctx := context.Background()

	s, err := NewSession(ctx, runner, "untitled")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("가", sessionTitleMaxLen+10)
	if err := s.Append(SessionRecord{Role: "user", Text: long}); err != nil {
		t.Fatal(err)
	}

	metas, err := ListSessions(ctx, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1", len(metas))
	}
	want := string([]rune(long)[:sessionTitleMaxLen]) + "…"
	if metas[0].Title != want {
		t.Errorf("Title = %q, want %q", metas[0].Title, want)
	}
}

// TestListSessionsBackwardCompatibleWithV1File parses a session file
// hand-authored in the pre-title-record shape — no "title" role ever
// existed in it, exactly what a v1 gk binary would have written. It must
// list cleanly via the first-user-message fallback, proving the listing
// code does not require every file to carry a title record, and it must
// still Replay without any skipped lines.
func TestListSessionsBackwardCompatibleWithV1File(t *testing.T) {
	runner, root := sessionFixture(t)
	ctx := context.Background()

	dir := filepath.Join(root, ".git", "gk-chat", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	v1 := `{"ts":"2026-01-02T03:04:05Z","role":"user","text":"v1 질문입니다"}
{"ts":"2026-01-02T03:04:06Z","role":"assistant","text":"v1 답변입니다","model":"m","tokens_used":42}
`
	path := filepath.Join(dir, "v1-session.jsonl")
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, err := ListSessions(ctx, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1", len(metas))
	}
	if metas[0].ID != "v1-session" {
		t.Errorf("ID = %q, want v1-session", metas[0].ID)
	}
	if metas[0].Title != "v1 질문입니다" {
		t.Errorf("Title = %q, want fallback to first user message", metas[0].Title)
	}
	if metas[0].TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", metas[0].TurnCount)
	}

	s, err := OpenSession(ctx, runner, "v1-session")
	if err != nil {
		t.Fatal(err)
	}
	msgs, skipped, err := s.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 || len(msgs) != 2 {
		t.Errorf("Replay of v1 file: msgs=%d skipped=%d, want 2/0", len(msgs), skipped)
	}
}

// TestListSessionsSkipsCorruptedMiddleLine mirrors
// TestSessionReplayTolerantOfCorruption's skip-and-continue contract for
// the metadata scan: a broken line mid-file costs only that line, never
// the file.
func TestListSessionsSkipsCorruptedMiddleLine(t *testing.T) {
	runner, root := sessionFixture(t)
	ctx := context.Background()

	dir := filepath.Join(root, ".git", "gk-chat", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	broken := `{"ts":"2026-01-02T03:04:05Z","role":"user","text":"첫 질문"}
not even json
{"ts":"2026-01-02T03:04:07Z","role":"user","text":"두 번째 질문"}
`
	path := filepath.Join(dir, "broken.jsonl")
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, err := ListSessions(ctx, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1", len(metas))
	}
	if metas[0].TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2 (corrupted line must not stop the scan)", metas[0].TurnCount)
	}
	if metas[0].Title != "첫 질문" {
		t.Errorf("Title = %q, want first user message", metas[0].Title)
	}
}

// TestListSessionsOrdering confirms newest-first ordering by StartedAt.
func TestListSessionsOrdering(t *testing.T) {
	runner, _ := sessionFixture(t)
	ctx := context.Background()

	older, err := NewSession(ctx, runner, "older")
	if err != nil {
		t.Fatal(err)
	}
	if err := older.Append(SessionRecord{TS: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Role: "user", Text: "old"}); err != nil {
		t.Fatal(err)
	}
	newer, err := NewSession(ctx, runner, "newer")
	if err != nil {
		t.Fatal(err)
	}
	if err := newer.Append(SessionRecord{TS: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Role: "user", Text: "new"}); err != nil {
		t.Fatal(err)
	}

	metas, err := ListSessions(ctx, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].ID != "newer" || metas[1].ID != "older" {
		t.Fatalf("ListSessions order = %+v, want [newer, older]", metas)
	}
}

// TestReplaySkipsTitleRecordsWithoutCountingAsCorrupted confirms a /rename
// title record never becomes a provider message and never inflates
// Replay's corrupted-line counter — a resumed session must not report
// "N corrupted line(s)" for what is actually a clean rename.
func TestReplaySkipsTitleRecordsWithoutCountingAsCorrupted(t *testing.T) {
	runner, _ := sessionFixture(t)
	ctx := context.Background()

	s, err := NewSession(ctx, runner, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(SessionRecord{Role: "user", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(SessionRecord{Role: "assistant", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle("my title"); err != nil {
		t.Fatal(err)
	}

	msgs, skipped, err := s.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0 (title record must not count as corrupted)", skipped)
	}
	if len(msgs) != 2 {
		t.Errorf("msgs = %d, want 2 (title record must not become a message)", len(msgs))
	}
}

// TestListSessionsNoSessionsDir confirms a repo that never ran gk chat (no
// sessions/ directory at all) returns an empty, error-free list.
func TestListSessionsNoSessionsDir(t *testing.T) {
	runner, _ := sessionFixture(t)
	metas, err := ListSessions(context.Background(), runner)
	if err != nil {
		t.Fatalf("ListSessions on a repo with no sessions dir: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("metas = %+v, want empty", metas)
	}
}

// TestValidSessionID pins the traversal guard. Session ids reach this
// package from the --session flag and from the last-session pointer file,
// and both are joined with sessions/ to form a path opened for APPEND —
// so anything that escapes the directory must be rejected before the join.
func TestValidSessionID(t *testing.T) {
	valid := []string{"20260710-093012-4821", "abc_DEF-123"}
	for _, id := range valid {
		if !validSessionID(id) {
			t.Errorf("validSessionID(%q) = false, want true", id)
		}
	}
	invalid := []string{
		"",
		"../../../../tmp/evil",
		"..",
		"a/b",
		`a\b`,
		"a.jsonl",
		strings.Repeat("a", 129),
	}
	for _, id := range invalid {
		if validSessionID(id) {
			t.Errorf("validSessionID(%q) = true, want false", id)
		}
	}
}

// TestOpenSessionRejectsTraversal is the end-to-end form: a poisoned id
// must fail before any file outside sessions/ is touched.
func TestOpenSessionRejectsTraversal(t *testing.T) {
	runner, _ := sessionFixture(t)

	outside := filepath.Join(t.TempDir(), "evil.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel := strings.TrimSuffix(outside, ".jsonl")

	if _, err := OpenSession(context.Background(), runner, rel); err == nil {
		t.Fatalf("OpenSession(%q) succeeded, want rejection", rel)
	}
	if _, err := NewSession(context.Background(), runner, "../../pwned"); err == nil {
		t.Fatal("NewSession with traversal id succeeded, want rejection")
	}
}
