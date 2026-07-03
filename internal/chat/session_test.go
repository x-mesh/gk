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
	if err := s.Append(SessionRecord{Role: "user", Text: "hi"}); err != nil {
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
	if len(msgs) != 1 || skipped != 1 {
		t.Errorf("msgs=%d skipped=%d, want 1/1", len(msgs), skipped)
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
