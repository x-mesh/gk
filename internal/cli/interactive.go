package cli

import (
	"os"
	"strings"

	"github.com/x-mesh/gk/internal/ui"
)

// promptAllowed reports whether this invocation may open an interactive TUI.
// A TTY alone is not enough: agent/json/CI runs must stay machine-readable even
// when the host shell happens to allocate a terminal.
func promptAllowed() bool {
	return promptAllowedFor(ui.IsTerminal(), AgentOut(), JSONOut(), os.Getenv("CI"))
}

func promptAllowedFor(tty, agent, json bool, ci string) bool {
	return tty && !agent && !json && !envTruthy(ci)
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
