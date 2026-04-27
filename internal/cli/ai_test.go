package cli

import (
	"testing"
)

func TestShowPromptFlagOnRoot(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("show-prompt")
	if f == nil {
		t.Fatal("--show-prompt persistent flag not found on root command")
	}
	if f.DefValue != "false" {
		t.Errorf("--show-prompt default: want %q, got %q", "false", f.DefValue)
	}
}
