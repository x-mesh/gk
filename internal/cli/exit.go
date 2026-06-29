package cli

import (
	"errors"
	"fmt"
)

// ExitError requests a specific process exit code without main.go printing an
// extra error message — the command has already rendered its result (e.g. a
// paused-state envelope). It is the exit-code channel, deliberately independent
// of the agent/human output mode: batch/land run their child commands in human
// mode (GK_AGENT stripped), so the pause signal cannot ride on the JSON
// envelope — it must be the process exit code.
type ExitError struct {
	Code int
	// err is an optional wrapped error carrying hint/remedy/message. main.go
	// suppresses extra output for an ExitError (rendered==true), but the chain is
	// preserved so HintFrom/RemediesFrom still work for callers and tests.
	err error
}

func (e *ExitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit %d", e.Code)
}

func (e *ExitError) Unwrap() error { return e.err }

// ExitCode lets callers (and exec.ExitError-style probes) read the code.
func (e *ExitError) ExitCode() int { return e.Code }

// ExitCodeFor maps an Execute() error to the process exit code and whether the
// command already rendered its output. rendered==true means main.go must NOT
// print an error envelope/prose on top (the paused result is already on stdout).
// A nil error is exit 0; a plain error is exit 1.
func ExitCodeFor(err error) (code int, rendered bool) {
	if err == nil {
		return 0, false
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code, true
	}
	return 1, false
}

// pausedExitIf returns an *ExitError{Code:3} when the just-emitted payload is a
// paused state, so the process exits 3 — the paused contract that gk batch/land
// detect from a child's exit code. Returns nil for any non-paused payload.
// Driven by the payload's own agentState() so the envelope state and the exit
// code share a single source of truth and can never drift.
func pausedExitIf(payload any) error {
	if s, ok := payload.(agentStater); ok && s.agentState() == envStatePaused {
		return &ExitError{Code: 3}
	}
	return nil
}
