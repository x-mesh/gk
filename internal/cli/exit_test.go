package cli

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeFor(t *testing.T) {
	if c, r := ExitCodeFor(nil); c != 0 || r {
		t.Errorf("nil → (%d,%v), want (0,false)", c, r)
	}
	if c, r := ExitCodeFor(&ExitError{Code: 3}); c != 3 || !r {
		t.Errorf("ExitError{3} → (%d,%v), want (3,true)", c, r)
	}
	if c, r := ExitCodeFor(fmt.Errorf("wrapped: %w", &ExitError{Code: 3})); c != 3 || !r {
		t.Errorf("wrapped ExitError{3} → (%d,%v), want (3,true)", c, r)
	}
	if c, r := ExitCodeFor(errors.New("boom")); c != 1 || r {
		t.Errorf("plain error → (%d,%v), want (1,false)", c, r)
	}
}

type fakeAgentState string

func (f fakeAgentState) agentState() string { return string(f) }

func TestPausedExitIf(t *testing.T) {
	err := pausedExitIf(fakeAgentState(envStatePaused))
	if err == nil {
		t.Fatal("paused payload → nil, want ExitError{3}")
	}
	if c, _ := ExitCodeFor(err); c != 3 {
		t.Errorf("paused exit code = %d, want 3", c)
	}
	if err := pausedExitIf(fakeAgentState(envStateOK)); err != nil {
		t.Errorf("ok payload → %v, want nil", err)
	}
	if err := pausedExitIf(struct{}{}); err != nil {
		t.Errorf("non-agentStater payload → %v, want nil", err)
	}
}
