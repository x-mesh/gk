package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestAICmdRegistered(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"ai"})
	if err != nil {
		t.Fatalf("rootCmd.Find(ai): %v", err)
	}
	if found.Use != "ai" {
		t.Errorf("Use: want %q, got %q", "ai", found.Use)
	}
	if found.Short == "" {
		t.Error("Short description should not be empty")
	}
}

func TestAICmdHelp(t *testing.T) {
	buf := &bytes.Buffer{}
	cmd := AICmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Help(); err != nil {
		t.Fatalf("ai.Help(): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"AI-powered", "gemini", "qwen", "kiro-cli"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q\n---\n%s", want, out)
		}
	}
}

func TestAICmdExportedAccessor(t *testing.T) {
	if AICmd() != aiCmd {
		t.Error("AICmd() should return the package-level aiCmd")
	}
}
