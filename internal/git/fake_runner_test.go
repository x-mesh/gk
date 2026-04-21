package git

import (
	"context"
	"errors"
	"testing"
)

// TestFakeRunner_ExactMatch verifies that a registered response is returned
// when args match exactly.
func TestFakeRunner_ExactMatch(t *testing.T) {
	r := &FakeRunner{
		Responses: map[string]FakeResponse{
			"status": {Stdout: "On branch main", ExitCode: 0},
		},
	}
	stdout, _, err := r.Run(context.Background(), "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(stdout) != "On branch main" {
		t.Errorf("unexpected stdout: %q", string(stdout))
	}
}

// TestFakeRunner_DefaultResp verifies fallback to DefaultResp when no key matches.
func TestFakeRunner_DefaultResp(t *testing.T) {
	r := &FakeRunner{
		Responses:   map[string]FakeResponse{},
		DefaultResp: FakeResponse{Stdout: "default output"},
	}
	stdout, _, err := r.Run(context.Background(), "log", "--oneline")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(stdout) != "default output" {
		t.Errorf("unexpected stdout: %q", string(stdout))
	}
}

// TestFakeRunner_ZeroDefault verifies that a zero DefaultResp gives exit 0 + empty output.
func TestFakeRunner_ZeroDefault(t *testing.T) {
	r := &FakeRunner{}
	stdout, stderr, err := r.Run(context.Background(), "diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Errorf("expected empty output, got stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestFakeRunner_NonZeroExitCode verifies that ExitCode != 0 produces *ExitError.
func TestFakeRunner_NonZeroExitCode(t *testing.T) {
	r := &FakeRunner{
		Responses: map[string]FakeResponse{
			"push origin main": {Stderr: "permission denied", ExitCode: 128},
		},
	}
	_, _, err := r.Run(context.Background(), "push", "origin", "main")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *ExitError, got %T: %v", err, err)
	}
	if exitErr.Code != 128 {
		t.Errorf("expected code 128, got %d", exitErr.Code)
	}
	if exitErr.Stderr != "permission denied" {
		t.Errorf("unexpected Stderr: %q", exitErr.Stderr)
	}
}

// TestFakeRunner_CallsRecorded verifies every invocation is appended to Calls.
func TestFakeRunner_CallsRecorded(t *testing.T) {
	r := &FakeRunner{}
	r.Run(context.Background(), "status")
	r.Run(context.Background(), "log", "--oneline")
	r.Run(context.Background(), "diff", "--stat")

	if len(r.Calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(r.Calls))
	}
	if r.Calls[0].Args[0] != "status" {
		t.Errorf("call[0]: expected 'status', got %v", r.Calls[0].Args)
	}
	if r.Calls[1].Args[0] != "log" {
		t.Errorf("call[1]: expected 'log', got %v", r.Calls[1].Args)
	}
	if r.Calls[2].Args[0] != "diff" {
		t.Errorf("call[2]: expected 'diff', got %v", r.Calls[2].Args)
	}
}

// TestFakeRunner_CallsOrder verifies calls are recorded in invocation order.
func TestFakeRunner_CallsOrder(t *testing.T) {
	r := &FakeRunner{}
	cmds := [][]string{
		{"fetch", "--all"},
		{"rebase", "origin/main"},
		{"push"},
	}
	for _, args := range cmds {
		r.Run(context.Background(), args...)
	}
	for i, call := range r.Calls {
		if call.Args[0] != cmds[i][0] {
			t.Errorf("call[%d]: expected %q, got %q", i, cmds[i][0], call.Args[0])
		}
	}
}

// TestFakeRunner_CustomErr verifies that a non-nil Err in FakeResponse is returned directly.
func TestFakeRunner_CustomErr(t *testing.T) {
	sentinel := errors.New("network timeout")
	r := &FakeRunner{
		Responses: map[string]FakeResponse{
			"fetch": {Err: sentinel},
		},
	}
	_, _, err := r.Run(context.Background(), "fetch")
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// TestFakeRunner_CallArgsIsolated verifies that mutations to args after Run
// do not affect the recorded Calls slice.
func TestFakeRunner_CallArgsIsolated(t *testing.T) {
	r := &FakeRunner{}
	args := []string{"status"}
	r.Run(context.Background(), args...)
	args[0] = "mutated"

	if r.Calls[0].Args[0] != "status" {
		t.Errorf("expected isolated copy 'status', got %q", r.Calls[0].Args[0])
	}
}
