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
	for _, want := range []string{"AI-powered", "nvidia", "gemini", "qwen", "kiro", "Privacy Gate", "Fallback Chain"} {
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

func TestAICmdShowPromptFlag(t *testing.T) {
	cmd := AICmd()
	f := cmd.PersistentFlags().Lookup("show-prompt")
	if f == nil {
		t.Fatal("--show-prompt persistent flag not found on ai command")
	}
	if f.DefValue != "false" {
		t.Errorf("--show-prompt default: want %q, got %q", "false", f.DefValue)
	}
}
