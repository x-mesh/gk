package config

import "testing"

// TestConfigDefaults_Output verifies that Defaults() returns the correct
// default values for the Output section (Easy Mode configuration).
//
// Validates: Requirements 9.2
func TestConfigDefaults_Output(t *testing.T) {
	cfg := Defaults()

	if cfg.Output.Easy != false {
		t.Errorf("Output.Easy: got %v, want false", cfg.Output.Easy)
	}
	if cfg.Output.Lang != "ko" {
		t.Errorf("Output.Lang: got %q, want %q", cfg.Output.Lang, "ko")
	}
	if cfg.Output.Emoji != true {
		t.Errorf("Output.Emoji: got %v, want true", cfg.Output.Emoji)
	}
	if cfg.Output.Hints != "verbose" {
		t.Errorf("Output.Hints: got %q, want %q", cfg.Output.Hints, "verbose")
	}
}
