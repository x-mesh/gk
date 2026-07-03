// Package chat implements the gk chat engine: a multi-turn, tool-calling
// conversation over the repository, built on provider.ToolCaller and the
// sandboxed read-only tool registry in internal/chat/tools.
package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

// Control-record roles: not conversation messages, but replay directives.
// The session file is append-only, so state changes that would "remove"
// history (a rolled-back turn, /clear) are recorded as markers and
// applied at Replay time instead of rewriting the file.
const (
	// recordRoleAborted marks a rolled-back turn: everything after the
	// last completed turn is discarded on replay, mirroring RunTurn's
	// in-memory truncation.
	recordRoleAborted = "turn_aborted"
	// recordRoleClear marks a /clear: replay restarts from empty context
	// (the file keeps the full record for audit).
	recordRoleClear = "clear"
)

// SessionRecord is one persisted conversation event — one JSON object per
// line (the aicommit audit-log shape). Tool results are stored
// POST-redaction: what's on disk is exactly what the provider saw, so a
// --continue replay never re-handles raw secrets.
type SessionRecord struct {
	TS         time.Time            `json:"ts"`
	Role       string               `json:"role"`
	Text       string               `json:"text,omitempty"`
	ToolCalls  []provider.ToolCall  `json:"tool_calls,omitempty"`
	ToolResult *provider.ToolResult `json:"tool_result,omitempty"`
	Model      string               `json:"model,omitempty"`
	TokensUsed int                  `json:"tokens_used,omitempty"`
}

// sessionDir resolves .git/gk-chat via `git rev-parse --git-path` — never
// a hardcoded .git/ join, so worktrees and GIT_DIR overrides land in the
// right place (same as aiCacheDir). --path-format=absolute is required:
// the plain form returns a path relative to the REPO's cwd, and resolving
// that against this process's cwd (which may differ, e.g. tests or -C)
// lands the session directory in the wrong tree.
func sessionDir(ctx context.Context, runner git.Runner) (string, error) {
	if runner == nil {
		return "", fmt.Errorf("chat session: nil git runner")
	}
	out, _, err := runner.Run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "gk-chat")
	if err != nil {
		return "", fmt.Errorf("chat session: locate git dir: %w", err)
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("chat session: empty git path")
	}
	return p, nil
}

// Session persists one conversation as an append-only JSONL file under
// .git/gk-chat/sessions/<id>.jsonl and tracks the latest id in
// .git/gk-chat/last-session for --continue.
type Session struct {
	ID   string
	path string
	dir  string
}

// NewSession creates a fresh session file. The id folds in a timestamp
// passed by the caller so tests stay deterministic.
func NewSession(ctx context.Context, runner git.Runner, id string) (*Session, error) {
	dir, err := sessionDir(ctx, runner)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("chat session: empty id")
	}
	sdir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		return nil, fmt.Errorf("chat session: %w", err)
	}
	s := &Session{ID: id, path: filepath.Join(sdir, id+".jsonl"), dir: dir}
	// Touch the file so --continue right after an empty session works.
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("chat session: %w", err)
	}
	_ = f.Close()
	if err := s.markLast(); err != nil {
		return nil, err
	}
	return s, nil
}

// LastSessionID returns the id recorded by the most recent session, or ""
// when none exists.
func LastSessionID(ctx context.Context, runner git.Runner) string {
	dir, err := sessionDir(ctx, runner)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "last-session"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// OpenSession opens an existing session for appending and replay.
func OpenSession(ctx context.Context, runner git.Runner, id string) (*Session, error) {
	dir, err := sessionDir(ctx, runner)
	if err != nil {
		return nil, err
	}
	s := &Session{ID: id, path: filepath.Join(dir, "sessions", id+".jsonl"), dir: dir}
	if _, err := os.Stat(s.path); err != nil {
		return nil, fmt.Errorf("chat session %q: %w", id, err)
	}
	if err := s.markLast(); err != nil {
		return nil, err
	}
	return s, nil
}

// markLast records this session as the --continue target, via
// write-then-rename so a concurrent reader never sees a torn file.
func (s *Session) markLast() error {
	tmp := filepath.Join(s.dir, "last-session.tmp")
	if err := os.WriteFile(tmp, []byte(s.ID+"\n"), 0o600); err != nil {
		return fmt.Errorf("chat session: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, "last-session")); err != nil {
		return fmt.Errorf("chat session: %w", err)
	}
	return nil
}

// Append writes one record. Each line is a self-contained JSON object
// written with O_APPEND, so a crash mid-write corrupts at most the final
// line — which Replay tolerates by design.
func (s *Session) Append(rec SessionRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("chat session: marshal: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("chat session: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("chat session: write: %w", err)
	}
	return nil
}

// Replay reads the session back as provider messages. Corruption is
// contained, never fatal: an unparseable line (torn tail from a crash) is
// skipped and reported via the second return so the caller can warn —
// losing one line of history must not lose the session.
func (s *Session) Replay() ([]provider.ChatMessage, int, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, 0, fmt.Errorf("chat session: %w", err)
	}
	defer func() { _ = f.Close() }()

	var msgs []provider.ChatMessage
	skipped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec SessionRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			skipped++
			continue
		}
		switch rec.Role {
		case recordRoleAborted:
			msgs = msgs[:lastCompletedTurnEnd(msgs)]
			continue
		case recordRoleClear:
			msgs = nil
			continue
		}
		msg, ok := rec.toMessage()
		if !ok {
			skipped++
			continue
		}
		msgs = append(msgs, msg)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("chat session: read: %w", err)
	}
	return msgs, skipped, nil
}

// lastCompletedTurnEnd returns the index just past the last assistant
// message that finished a turn (text, no tool calls) — the exact position
// RunTurn's rollback truncates to, so replay and live memory agree.
func lastCompletedTurnEnd(msgs []provider.ChatMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) == 0 {
			return i + 1
		}
	}
	return 0
}

// toMessage converts a persisted record back to the provider shape,
// rejecting records that would produce an invalid conversation.
func (r SessionRecord) toMessage() (provider.ChatMessage, bool) {
	switch r.Role {
	case "user":
		if r.Text == "" {
			return provider.ChatMessage{}, false
		}
		return provider.ChatMessage{Role: "user", Text: r.Text}, true
	case "assistant":
		if r.Text == "" && len(r.ToolCalls) == 0 {
			return provider.ChatMessage{}, false
		}
		return provider.ChatMessage{Role: "assistant", Text: r.Text, ToolCalls: r.ToolCalls}, true
	case "tool":
		if r.ToolResult == nil {
			return provider.ChatMessage{}, false
		}
		return provider.ChatMessage{Role: "tool", ToolResult: r.ToolResult}, true
	default:
		return provider.ChatMessage{}, false
	}
}
