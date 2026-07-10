package cli

import (
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/chat"
)

// TestChatHistorySeed pins the extraction chatLineReader's seed relies on:
// only non-empty "user" turns survive, in the same order Replay produced
// them, and assistant/tool records (and an empty user text, which Replay
// itself never persists but a corrupted line could still yield) are
// skipped rather than seeding a blank history entry.
func TestChatHistorySeed(t *testing.T) {
	msgs := []provider.ChatMessage{
		{Role: "user", Text: "첫 질문"},
		{Role: "assistant", Text: "첫 답변"},
		{Role: "tool", ToolResult: &provider.ToolResult{Content: "ignored"}},
		{Role: "user", Text: ""}, // defensive: never persisted by Replay, must not seed ""
		{Role: "user", Text: "두 번째 질문"},
		{Role: "assistant", Text: "두 번째 답변", ToolCalls: []provider.ToolCall{{Name: "git_log"}}},
	}
	got := chatHistorySeed(msgs)
	want := []string{"첫 질문", "두 번째 질문"}
	if len(got) != len(want) {
		t.Fatalf("chatHistorySeed() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chatHistorySeed()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestChatHistorySeedSkipsSyntheticCompact pins the v2 finding: the
// /compact intro is a user-role message the engine synthesized, not one the
// human typed, so a --continue replay must NOT seed it into ↑/↓ history.
func TestChatHistorySeedSkipsSyntheticCompact(t *testing.T) {
	msgs := []provider.ChatMessage{
		{Role: "user", Text: "real question"},
		{Role: "user", Text: chat.CompactSummaryIntro},
		{Role: "assistant", Text: "folded summary"},
		{Role: "user", Text: "later real question"},
	}
	got := chatHistorySeed(msgs)
	want := []string{"real question", "later real question"}
	if len(got) != len(want) {
		t.Fatalf("chatHistorySeed() = %v, want %v (synthetic /compact intro must be skipped)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestChatHistorySeedEmpty confirms an empty/no-user replay seeds nothing
// (nil, not a slice of blanks) — chatLineReader's seed loop must have
// zero iterations, not one over an empty string.
func TestChatHistorySeedEmpty(t *testing.T) {
	msgs := []provider.ChatMessage{
		{Role: "assistant", Text: "only assistant, no prior user turn"},
	}
	if got := chatHistorySeed(msgs); len(got) != 0 {
		t.Errorf("chatHistorySeed() = %v, want empty", got)
	}
	if got := chatHistorySeed(nil); len(got) != 0 {
		t.Errorf("chatHistorySeed(nil) = %v, want empty", got)
	}
}

// TestCompleteChatCommand covers the candidate-computation rules the PRD
// asks to be unit-tested directly, without a live terminal: cursor must be
// at line end, the line must start with "/", and completion only fires on
// an unambiguous single match that isn't already the full command.
func TestCompleteChatCommand(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		pos      int
		wantLine string
		wantPos  int
		wantOK   bool
	}{
		{"unique prefix completes", "/h", 2, "/help", 5, true},
		{"another unique prefix", "/ex", 3, "/exit", 5, true},
		{"two-letter unique prefix", "/cl", 3, "/clear", 6, true},
		{"single-letter prefix now ambiguous (/clear vs /compact)", "/c", 2, "/c", 2, false},
		{"already-complete command", "/help", 5, "/help", 5, false},
		{"ambiguous bare slash", "/", 1, "/", 1, false},
		{"no match", "/nope", 5, "/nope", 5, false},
		{"cursor not at end", "/help", 2, "/help", 2, false},
		{"trailing text after command", "/help extra", 11, "/help extra", 11, false},
		{"not a slash command", "hello", 5, "hello", 5, false},
		{"empty line", "", 0, "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotLine, gotPos, gotOK := completeChatCommand(c.line, c.pos)
			if gotLine != c.wantLine || gotPos != c.wantPos || gotOK != c.wantOK {
				t.Errorf("completeChatCommand(%q, %d) = (%q, %d, %v), want (%q, %d, %v)",
					c.line, c.pos, gotLine, gotPos, gotOK, c.wantLine, c.wantPos, c.wantOK)
			}
		})
	}
}

// TestChatAutoComplete pins the key gate around completeChatCommand: only
// Tab (x/term's AutoCompleteCallback fires on every otherwise-unhandled
// keypress, not just Tab) may trigger completion — every other key must
// fall through with ok=false so normal typing is untouched.
func TestChatAutoComplete(t *testing.T) {
	if _, _, ok := chatAutoComplete("/h", 2, 'a'); ok {
		t.Error("chatAutoComplete with a non-Tab key must not complete")
	}
	gotLine, gotPos, ok := chatAutoComplete("/h", 2, '\t')
	if !ok || gotLine != "/help" || gotPos != 5 {
		t.Errorf("chatAutoComplete(Tab) = (%q, %d, %v), want (\"/help\", 5, true)", gotLine, gotPos, ok)
	}
}

// TestChatRenameArg pins /rename's line parsing: a bare "/rename" is a
// recognized invocation with an empty title (usage case), "/rename <title>"
// extracts and trims the title, and anything else — including a line that
// merely starts with the substring "/rename" without the required
// separating space — is NOT a /rename line at all (ok=false), so the REPL's
// "unknown command" fallback handles it instead of misparsing a title.
func TestChatRenameArg(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantTitle string
		wantOK    bool
	}{
		{"bare command, no argument", "/rename", "", true},
		{"simple title", "/rename my session", "my session", true},
		{"title with surrounding whitespace trimmed", "/rename   spaced out  ", "spaced out", true},
		{"korean title", "/rename 이 함수 리뷰", "이 함수 리뷰", true},
		{"not a rename line", "/help", "", false},
		{"prefix without separating space", "/renamed", "", false},
		{"empty line", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotTitle, gotOK := chatRenameArg(c.line)
			if gotTitle != c.wantTitle || gotOK != c.wantOK {
				t.Errorf("chatRenameArg(%q) = (%q, %v), want (%q, %v)", c.line, gotTitle, gotOK, c.wantTitle, c.wantOK)
			}
		})
	}
}

// TestChatMetaCommandsIncludesRename guards the Tab-completion source list
// against silently losing /rename on a future edit.
func TestChatMetaCommandsIncludesRename(t *testing.T) {
	for _, c := range chatMetaCommands {
		if c == "/rename" {
			return
		}
	}
	t.Errorf("chatMetaCommands = %v, want it to include /rename", chatMetaCommands)
}

// TestChatMetaCommandsIncludesCompactAndTokens guards the Tab-completion
// source list against silently losing /compact or /tokens on a future
// edit — the same regression TestChatMetaCommandsIncludesRename guards
// for /rename.
func TestChatMetaCommandsIncludesCompactAndTokens(t *testing.T) {
	want := []string{"/compact", "/tokens"}
	for _, w := range want {
		found := false
		for _, c := range chatMetaCommands {
			if c == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("chatMetaCommands = %v, want it to include %q", chatMetaCommands, w)
		}
	}
}
