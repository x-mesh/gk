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
	"sort"
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
	// recordRoleTitle carries a /rename title. Like aborted/clear it is a
	// control record, never a conversation turn: Replay skips it without
	// counting it toward the corrupted-line counter, and multiple title
	// records may accumulate across renames — the LAST one in the file
	// wins (readSessionMeta overwrites on each occurrence, in file order).
	// A binary predating this field's introduction still parses the file
	// fine: Role was already a free-form string (aborted/clear are prior
	// art for the same additive pattern), so an unrecognized "title" value
	// just falls through toMessage's default case and is skipped like any
	// other unknown role — one extra "corrupted line" in the old binary's
	// warning count, never a crash or a wedged replay.
	recordRoleTitle = "title"
	// recordRoleCompact marks a /compact summarization. Like title/aborted/
	// clear it is a control record, never replayed as a conversation
	// message: on Replay, everything accumulated so far collapses to the
	// synthetic intro+summary pair compactSummaryMessages builds from Text
	// (plus, when HistoryBudget > 0, the same hard-trim trimHistory pass a
	// live Compact call already applied — see compactReplayFold), and
	// replay then continues appending whatever comes after normally —
	// mirroring exactly what a live Engine.Compact call does to e.history,
	// so a --continue resumed after /compact sees the identical state the
	// live process had. Model/TokensUsed/HistoryBudget record the
	// summarizer call's own usage and budget for audit and replay parity.
	//
	// A binary predating this field's introduction still parses the file
	// fine: Role was already free-form (title is prior art for the same
	// pattern), so an unrecognized "compact" value falls through
	// toMessage's default case and is skipped like any other unknown role
	// — the ORIGINAL, uncompacted messages around it (still present on
	// disk; JSONL is append-only) replay normally, just less compactly.
	// One extra "corrupted line" in the old binary's warning count, never
	// a crash or a wedged replay.
	recordRoleCompact = "compact"
	// sessionTitleMaxLen bounds the display title derived from a
	// session's first user message when no explicit /rename exists.
	sessionTitleMaxLen = 60
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
	// HistoryBudget is Engine.HistoryBudget as it stood when a
	// recordRoleCompact record was written (0 for every other role, and
	// for any compact record written before this field existed) — see
	// RecordCompact and compactReplayFold. omitempty keeps every
	// non-compact record byte-identical to before this field was added.
	HistoryBudget int `json:"history_budget,omitempty"`
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

// validSessionID rejects any id that would not resolve to a plain file
// directly inside sessions/. Ids reach this package from two places that
// are not the code that minted them — the `--session` flag and the
// contents of the last-session pointer file — and both are joined with
// the sessions dir to form a path that is then opened for APPEND. Without
// this check `--session ../../../../tmp/x` (or the same string written
// into last-session) resolves outside the directory and gk would append
// JSONL records to whatever .jsonl file lives there.
//
// The accepted alphabet is what NewSession actually produces
// (timestamp-pid, e.g. "20260710-093012-4821") plus '_' for headroom.
func validSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// NewSession creates a fresh session file. The id folds in a timestamp
// passed by the caller so tests stay deterministic.
func NewSession(ctx context.Context, runner git.Runner, id string) (*Session, error) {
	dir, err := sessionDir(ctx, runner)
	if err != nil {
		return nil, err
	}
	if !validSessionID(id) {
		return nil, fmt.Errorf("chat session: invalid id %q", id)
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
	if !validSessionID(id) {
		return nil, fmt.Errorf("chat session: invalid id %q", id)
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

// SetTitle appends a /rename title record. It is a control record exactly
// like the /clear marker — never replayed as a conversation message — so
// renaming mid-session costs one JSONL line and never touches prior
// history. Safe to call repeatedly; the latest record wins for display.
func (s *Session) SetTitle(title string) error {
	if err := s.Append(SessionRecord{TS: time.Now().UTC(), Role: recordRoleTitle, Text: title}); err != nil {
		return fmt.Errorf("chat session: set title: %w", err)
	}
	return nil
}

// RecordCompact appends a /compact control record: summary is the
// summarizer's own output (the exact text compactSummaryMessages will wrap
// into the synthetic intro+summary pair on replay), model/tokensUsed are
// the summarize call's own usage, carried along for audit the same way a
// normal assistant record does. historyBudget is the Engine.HistoryBudget
// in effect at fold time — 0 when trimming is disabled — persisted so a
// later --continue replay can re-apply the identical hard-trim fallback
// Engine.Compact already applied in memory (see compactReplayFold); without
// it, replay would only ever see the untrimmed summary+kept shape, which
// silently diverges from live history whenever that fallback actually
// fired. Called by Engine.Compact ONLY after a summarize call has already
// succeeded and the in-memory history has already been folded — see that
// method's docstring for what a failure here does and does not undo.
func (s *Session) RecordCompact(summary, model string, tokensUsed, historyBudget int) error {
	rec := SessionRecord{TS: time.Now().UTC(), Role: recordRoleCompact, Text: summary, Model: model, TokensUsed: tokensUsed, HistoryBudget: historyBudget}
	if err := s.Append(rec); err != nil {
		return fmt.Errorf("chat session: record compact: %w", err)
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
		case recordRoleTitle:
			continue
		case recordRoleCompact:
			// A live Compact call does NOT discard everything — it folds
			// only the turns before the last compactKeepTurns, and keeps
			// those most-recent turns verbatim AFTER the summary (see
			// Engine.Compact). The session file never re-emits those kept
			// turns' records past this point (append-only; they are
			// already earlier in the file), so replay must re-derive the
			// SAME cut Compact used — via the SAME turnStarts/
			// compactKeepTurns logic — from msgs as accumulated so far,
			// rather than dropping them along with the folded prefix.
			msgs = compactReplayFold(msgs, rec)
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
	// Structural safety net: even if a rollback's turn_aborted marker was
	// never written (crash between the turn's records and the marker, or
	// the marker append itself failed), a replayed history must not end
	// mid-turn — a trailing dangling user message or unanswered tool_use
	// wedges the provider on the very first --continue round.
	msgs = msgs[:lastCompletedTurnEnd(msgs)]
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

// SessionMeta summarizes one session file for `gk chat sessions` — cheap
// enough to compute for every file in the directory without a full Replay
// reconstruction (no ToolCall/ToolResult shapes are rebuilt).
type SessionMeta struct {
	ID   string
	Path string
	// StartedAt is the first record's timestamp; the zero value means the
	// file had no parseable line at all (empty or fully corrupt).
	StartedAt time.Time
	// Title is the latest /rename record's text, or — when none exists —
	// the first non-empty user message, truncated to sessionTitleMaxLen
	// runes. Empty only when the session has no title and no user turn
	// yet (freshly created, untouched file).
	Title string
	// TurnCount counts every "user" record written to the file, including
	// one from a turn later rolled back by a turn_aborted marker — an
	// approximation cheap enough for a list view, not the exact
	// replay-surviving turn count Replay would report.
	TurnCount int
}

// ListSessions scans .git/gk-chat/sessions/*.jsonl and returns one
// SessionMeta per file, newest first (by StartedAt, tie-broken by ID
// descending — both monotonic for the timestamp-prefixed ids `gk chat`
// generates). No separate index file: this is a directory scan plus one
// lightweight pass per file. A missing sessions/ directory (no session
// created yet) returns (nil, nil), not an error. A single unreadable file
// is skipped rather than failing the whole listing — the same
// corruption-tolerant spirit as Replay.
func ListSessions(ctx context.Context, runner git.Runner) ([]SessionMeta, error) {
	dir, err := sessionDir(ctx, runner)
	if err != nil {
		return nil, err
	}
	sdir := filepath.Join(dir, "sessions")
	entries, err := os.ReadDir(sdir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("chat session: list sessions: %w", err)
	}

	metas := make([]SessionMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(sdir, e.Name())
		meta, mErr := readSessionMeta(path)
		if mErr != nil {
			continue // unreadable file: skip it, don't fail the listing
		}
		meta.ID = strings.TrimSuffix(e.Name(), ".jsonl")
		meta.Path = path
		metas = append(metas, meta)
	}

	sort.Slice(metas, func(i, j int) bool {
		if !metas[i].StartedAt.Equal(metas[j].StartedAt) {
			return metas[i].StartedAt.After(metas[j].StartedAt)
		}
		return metas[i].ID > metas[j].ID
	})
	return metas, nil
}

// readSessionMeta scans one session file for listing metadata. It tolerates
// exactly the corruption Replay does: an unparseable line is skipped and
// costs only that line's contribution (never the file). Unlike Replay it
// does not reconstruct provider.ChatMessage values or apply the
// turn_aborted/clear control markers — it only needs the first record's
// timestamp, the latest title, the first user message, and a turn count.
func readSessionMeta(path string) (SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, err
	}
	defer func() { _ = f.Close() }()

	var meta SessionMeta
	firstUser := ""
	haveFirst := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec SessionRecord
		if uErr := json.Unmarshal([]byte(line), &rec); uErr != nil {
			continue
		}
		if !haveFirst {
			meta.StartedAt = rec.TS
			haveFirst = true
		}
		switch rec.Role {
		case recordRoleTitle:
			meta.Title = rec.Text
		case "user":
			meta.TurnCount++
			if firstUser == "" && rec.Text != "" {
				firstUser = rec.Text
			}
		}
	}
	if err := sc.Err(); err != nil {
		return SessionMeta{}, fmt.Errorf("chat session: read: %w", err)
	}
	if meta.Title == "" {
		meta.Title = truncateSessionTitle(firstUser)
	}
	return meta, nil
}

// truncateSessionTitle collapses whitespace and cuts to sessionTitleMaxLen
// runes (not bytes, so multi-byte text like Korean is never split
// mid-character), appending an ellipsis when truncated.
func truncateSessionTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= sessionTitleMaxLen {
		return s
	}
	return string(r[:sessionTitleMaxLen]) + "…"
}
