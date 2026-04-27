package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestAICommitRegistered(t *testing.T) {
	// rootCmd should resolve "commit" directly.
	found, _, err := rootCmd.Find([]string{"commit"})
	if err != nil {
		t.Fatalf("rootCmd.Find(commit): %v", err)
	}
	if found.Use != "commit" {
		t.Errorf("Use: want %q, got %q", "commit", found.Use)
	}
}

func TestAICommitHelpListsFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	found, _, err := rootCmd.Find([]string{"commit"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	found.SetOut(buf)
	found.SetErr(buf)
	if err := found.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"--force", "--dry-run", "--provider", "--lang",
		"--staged-only", "--include-unstaged", "--abort",
		"--allow-secret-kind", "--ci", "--yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing flag %q\n%s", want, out)
		}
	}
}

func TestReadAICommitFlagsMutualExclusion(t *testing.T) {
	found, _, _ := rootCmd.Find([]string{"commit"})
	_ = found.Flags().Set("staged-only", "true")
	_ = found.Flags().Set("include-unstaged", "true")
	_, err := readAICommitFlags(found)
	if err == nil {
		t.Error("want error when both --staged-only and --include-unstaged are set")
	}
	// Reset for other tests.
	_ = found.Flags().Set("staged-only", "false")
	_ = found.Flags().Set("include-unstaged", "false")
}

func TestNewRunIDIsHex(t *testing.T) {
	id := newRunID()
	if len(id) < 8 {
		t.Errorf("runID too short: %q", id)
	}
	// Either hex (16 chars) or time-based fallback starting with 't'.
	if id[0] != 't' {
		for _, r := range id {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				t.Errorf("non-hex rune in runID: %q", id)
				break
			}
		}
	}
}
