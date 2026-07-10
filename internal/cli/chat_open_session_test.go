package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/chat"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestOpenChatSession_NoFlagsCreatesFresh confirms the zero-flag path
// (no --continue, no --session) always creates a brand new session, never
// touching an existing one.
func TestOpenChatSession_NoFlagsCreatesFresh(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	existing, err := chat.NewSession(ctx, runner, "existing")
	if err != nil {
		t.Fatal(err)
	}

	sess, warn := openChatSession(ctx, runner, false, "")
	if sess == nil {
		t.Fatal("openChatSession returned nil session")
	}
	if warn != "" {
		t.Errorf("warn = %q, want empty for the plain-new-session path", warn)
	}
	if sess.ID == existing.ID {
		t.Errorf("openChatSession reused %q instead of creating a fresh session", sess.ID)
	}
}

// TestOpenChatSession_SessionFlagResumesSpecificID confirms --session opens
// the exact id requested (not necessarily the most recently touched one)
// and — like OpenSession always has — makes it the new --continue target.
func TestOpenChatSession_SessionFlagResumesSpecificID(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	older, err := chat.NewSession(ctx, runner, "older-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.NewSession(ctx, runner, "newer-session"); err != nil {
		t.Fatal(err)
	}
	// newer-session is now the last-session pointer; --session should still
	// let the caller reach back to the older one explicitly.

	sess, warn := openChatSession(ctx, runner, false, older.ID)
	if warn != "" {
		t.Errorf("warn = %q, want empty (the requested session exists)", warn)
	}
	if sess == nil || sess.ID != "older-session" {
		t.Fatalf("openChatSession(--session=older-session) = %+v, want the older session", sess)
	}
	if got := chat.LastSessionID(ctx, runner); got != "older-session" {
		t.Errorf("LastSessionID = %q, want --session to re-mark it as the --continue target", got)
	}
}

// TestOpenChatSession_SessionFlagMissingIDDegradesToFresh confirms a
// --session id that does not exist degrades to a brand new session with a
// warning — never a fatal error, same "never lose the conversation" policy
// --continue already follows for a missing/corrupt last-session.
func TestOpenChatSession_SessionFlagMissingIDDegradesToFresh(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	sess, warn := openChatSession(ctx, runner, false, "does-not-exist")
	if sess == nil {
		t.Fatal("openChatSession returned nil session")
	}
	if sess.ID == "does-not-exist" {
		t.Errorf("openChatSession must not report success for a missing session id")
	}
	if !strings.Contains(warn, "does-not-exist") {
		t.Errorf("warn = %q, want it to name the missing session id", warn)
	}
}

// TestOpenChatSession_ContinuePrefersLastSession confirms cont=true (with
// no --session) resumes the last-session pointer's target, the existing
// --continue contract this refactor must not disturb.
func TestOpenChatSession_ContinuePrefersLastSession(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	if _, err := chat.NewSession(ctx, runner, "first"); err != nil {
		t.Fatal(err)
	}
	last, err := chat.NewSession(ctx, runner, "second")
	if err != nil {
		t.Fatal(err)
	}

	sess, warn := openChatSession(ctx, runner, true, "")
	if warn != "" {
		t.Errorf("warn = %q, want empty", warn)
	}
	if sess == nil || sess.ID != last.ID {
		t.Fatalf("openChatSession(--continue) = %+v, want %q (the last-session pointer)", sess, last.ID)
	}
}

// TestOpenChatSession_ContinueNoPreviousSessionWarns confirms cont=true with
// no --session AND no last-session pointer at all (chat.LastSessionID
// returns "") degrades to a brand new session with the "no previous
// session" warning — distinct from the missing-id-on-an-existing-pointer
// warning TestOpenChatSession_ContinueCorruptedLastSessionDegradesToFresh
// covers below.
func TestOpenChatSession_ContinueNoPreviousSessionWarns(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	sess, warn := openChatSession(ctx, runner, true, "")
	if sess == nil {
		t.Fatal("openChatSession returned nil session")
	}
	if !strings.Contains(warn, "no previous chat session found") {
		t.Errorf("warn = %q, want the no-previous-session message", warn)
	}
}

// TestOpenChatSession_ContinueCorruptedLastSessionDegradesToFresh confirms
// --continue degrades to a fresh session (with a warning naming the id) when
// the last-session pointer names an id whose session file no longer exists
// on disk — chat.OpenSession's os.Stat failure, simulated here by deleting
// the underlying .jsonl after NewSession already recorded it as last-session.
func TestOpenChatSession_ContinueCorruptedLastSessionDegradesToFresh(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	if _, err := chat.NewSession(ctx, runner, "gone-session"); err != nil {
		t.Fatal(err)
	}
	if got := chat.LastSessionID(ctx, runner); got != "gone-session" {
		t.Fatalf("LastSessionID = %q, want %q before corrupting it", got, "gone-session")
	}
	// Remove the session file itself but leave the last-session pointer
	// intact — the corruption OpenSession's os.Stat catches.
	if rmErr := os.Remove(filepath.Join(repo.Dir, ".git", "gk-chat", "sessions", "gone-session.jsonl")); rmErr != nil {
		t.Fatalf("failed to remove session file backing the test: %v", rmErr)
	}

	sess, warn := openChatSession(ctx, runner, true, "")
	if sess == nil {
		t.Fatal("openChatSession returned nil session")
	}
	if sess.ID == "gone-session" {
		t.Errorf("openChatSession must not report success for a session whose file is gone")
	}
	if !strings.Contains(warn, "gone-session") {
		t.Errorf("warn = %q, want it to name the corrupted previous session id", warn)
	}
}

// TestRunChat_SessionAndContinueMutuallyExclusive confirms passing both
// --session and --continue is rejected before any provider/config work
// happens (the check runs immediately after flag parsing in runChat).
func TestRunChat_SessionAndContinueMutuallyExclusive(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("model", "", "")
	cmd.Flags().String("lang", "", "")
	cmd.Flags().Bool("continue", true, "")
	cmd.Flags().String("session", "some-id", "")

	err := runChat(cmd, []string{"one-shot question"})
	if err == nil {
		t.Fatal("want an error when --session and --continue are both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v, want a mutually-exclusive message", err)
	}
}
