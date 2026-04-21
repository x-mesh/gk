package reflog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// buildRaw constructs a raw reflog byte slice in the expected format:
// fields delimited by \x00, records delimited by \x1e.
func buildRaw(records ...string) []byte {
	return []byte(strings.Join(records, "\x1e") + "\x1e")
}

func record(newSHA, shortNew, ref, msg, unixAt string) string {
	return strings.Join([]string{newSHA, shortNew, ref, msg, unixAt}, "\x00")
}

// ── Parse tests ──────────────────────────────────────────────────────────────

func TestParse_EmptyInput(t *testing.T) {
	entries, err := Parse([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestParse_SingleRecord(t *testing.T) {
	const (
		sha     = "abc1234567890abcdef1234567890abcdef123456"
		short   = "abc1234"
		ref     = "HEAD@{0}"
		msg     = "commit: fix typo"
		unixAt  = "1700000000"
	)

	raw := buildRaw(record(sha, short, ref, msg, unixAt))
	entries, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.NewSHA != sha {
		t.Errorf("NewSHA = %q, want %q", e.NewSHA, sha)
	}
	if e.ShortNew != short {
		t.Errorf("ShortNew = %q, want %q", e.ShortNew, short)
	}
	if e.Ref != ref {
		t.Errorf("Ref = %q, want %q", e.Ref, ref)
	}
	if e.Message != msg {
		t.Errorf("Message = %q, want %q", e.Message, msg)
	}
	if e.Summary != "fix typo" {
		t.Errorf("Summary = %q, want %q", e.Summary, "fix typo")
	}
	if e.Action != ActionCommit {
		t.Errorf("Action = %q, want %q", e.Action, ActionCommit)
	}
	wantTime := time.Unix(1700000000, 0)
	if !e.When.Equal(wantTime) {
		t.Errorf("When = %v, want %v", e.When, wantTime)
	}
	if e.OldSHA != "" {
		t.Errorf("OldSHA should be empty, got %q", e.OldSHA)
	}
}

func TestParse_MultipleRecords(t *testing.T) {
	recs := []string{
		record("aaa0000000000000000000000000000000000000", "aaa0000", "HEAD@{0}", "commit: first", "1700000002"),
		record("bbb0000000000000000000000000000000000000", "bbb0000", "HEAD@{1}", "reset: moving to HEAD~1", "1700000001"),
		record("ccc0000000000000000000000000000000000000", "ccc0000", "HEAD@{2}", "checkout: moving from main to feat/x", "1700000000"),
	}
	raw := buildRaw(recs...)
	entries, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// order preserved
	if entries[0].ShortNew != "aaa0000" {
		t.Errorf("entries[0].ShortNew = %q, want aaa0000", entries[0].ShortNew)
	}
	if entries[1].Action != ActionReset {
		t.Errorf("entries[1].Action = %q, want reset", entries[1].Action)
	}
	if entries[2].Action != ActionCheckout {
		t.Errorf("entries[2].Action = %q, want checkout", entries[2].Action)
	}

	// timestamps parsed correctly and in order
	if !entries[0].When.After(entries[1].When) {
		t.Errorf("entries[0].When should be after entries[1].When")
	}
}

func TestParse_MalformedRecord(t *testing.T) {
	// Only 3 fields — should be skipped, no error.
	bad := "sha\x00short\x00ref"
	good := record("ddd0000000000000000000000000000000000000", "ddd0000", "HEAD@{0}", "commit: ok", "1700000000")
	raw := buildRaw(bad, good)

	entries, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (malformed skipped), got %d", len(entries))
	}
	if entries[0].ShortNew != "ddd0000" {
		t.Errorf("ShortNew = %q, want ddd0000", entries[0].ShortNew)
	}
}

// ── Read tests ────────────────────────────────────────────────────────────────

func TestRead_FakeRunner(t *testing.T) {
	sha := "eee0000000000000000000000000000000000000"
	short := "eee0000"
	msg := "commit: add feature"
	unixAt := "1700000042"
	stubOutput := buildRaw(record(sha, short, "HEAD@{0}", msg, unixAt))

	// The key must match the exact args joined by space.
	expectedKey := "reflog show " + reflogFormat + " HEAD"
	fr := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			expectedKey: {Stdout: string(stubOutput)},
		},
	}

	entries, err := Read(context.Background(), fr, "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify args contain --format flag.
	if len(fr.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fr.Calls))
	}
	args := fr.Calls[0].Args
	foundFormat := false
	for _, a := range args {
		if strings.HasPrefix(a, "--format=") {
			foundFormat = true
			break
		}
	}
	if !foundFormat {
		t.Errorf("expected --format= in args, got %v", args)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].NewSHA != sha {
		t.Errorf("NewSHA = %q, want %q", entries[0].NewSHA, sha)
	}
	if entries[0].Action != ActionCommit {
		t.Errorf("Action = %q, want commit", entries[0].Action)
	}
}

func TestRead_WithLimit(t *testing.T) {
	fr := &git.FakeRunner{
		DefaultResp: git.FakeResponse{Stdout: ""},
	}

	_, _ = Read(context.Background(), fr, "HEAD", 5)

	if len(fr.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fr.Calls))
	}
	args := fr.Calls[0].Args

	foundN := false
	for i, a := range args {
		if a == "-n" && i+1 < len(args) && args[i+1] == "5" {
			foundN = true
			break
		}
	}
	if !foundN {
		t.Errorf("expected -n 5 in args, got %v", args)
	}
}

func TestRead_DefaultRef(t *testing.T) {
	fr := &git.FakeRunner{
		DefaultResp: git.FakeResponse{Stdout: ""},
	}

	_, _ = Read(context.Background(), fr, "", 0)

	if len(fr.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fr.Calls))
	}
	args := fr.Calls[0].Args

	// Last positional arg should be HEAD (before any -n flags).
	found := false
	for _, a := range args {
		if a == "HEAD" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected HEAD in args when ref is empty, got %v", args)
	}
}
