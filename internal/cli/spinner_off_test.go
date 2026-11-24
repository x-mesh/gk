package cli

import (
	"testing"

	"github.com/x-mesh/gk/internal/ui"
)

// Agent mode and --debug turn every spinner off: an agent reads the output,
// and debug logs would interleave with the frames.
func TestSpinnersOffInAgentModeAndDebug(t *testing.T) {
	prevDebug := flagDebug
	t.Cleanup(func() { flagDebug = prevDebug })

	withAgentMode(t, false)
	flagDebug = false
	if ui.SpinnerOff == nil || ui.SpinnerOff() {
		t.Fatal("spinners are off in a plain run")
	}
	flagDebug = true
	if !ui.SpinnerOff() {
		t.Error("--debug leaves spinners on")
	}
	flagDebug = false
	withAgentMode(t, true)
	if !ui.SpinnerOff() {
		t.Error("agent mode leaves spinners on")
	}
}
