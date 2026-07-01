package cli

import "testing"

func TestPromptAllowedFor(t *testing.T) {
	tests := []struct {
		name       string
		tty        bool
		agent      bool
		json       bool
		ci         string
		wantPrompt bool
	}{
		{name: "plain TTY", tty: true, wantPrompt: true},
		{name: "non TTY", tty: false, wantPrompt: false},
		{name: "agent TTY", tty: true, agent: true, wantPrompt: false},
		{name: "json TTY", tty: true, json: true, wantPrompt: false},
		{name: "CI TTY", tty: true, ci: "true", wantPrompt: false},
		{name: "CI false TTY", tty: true, ci: "false", wantPrompt: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promptAllowedFor(tt.tty, tt.agent, tt.json, tt.ci); got != tt.wantPrompt {
				t.Fatalf("promptAllowedFor(...) = %v, want %v", got, tt.wantPrompt)
			}
		})
	}
}
