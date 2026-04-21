package git

import (
	"context"
	"strings"
)

// FakeResponse defines the canned response returned by FakeRunner for a given call.
type FakeResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// FakeCall records a single invocation made to FakeRunner.
type FakeCall struct {
	Args []string
}

// FakeRunner implements Runner for use in tests.
// Responses are keyed by strings.Join(args, " ").
// When no key matches, DefaultResp is returned (zero value means exit 0 + empty output).
// All calls are appended to Calls for later assertion.
type FakeRunner struct {
	Responses   map[string]FakeResponse
	DefaultResp FakeResponse
	Calls       []FakeCall
}

// Run looks up args in Responses, falls back to DefaultResp, records the call,
// and returns an *ExitError when ExitCode != 0.
func (f *FakeRunner) Run(_ context.Context, args ...string) (stdout, stderr []byte, err error) {
	f.Calls = append(f.Calls, FakeCall{Args: append([]string(nil), args...)})

	key := strings.Join(args, " ")
	resp, ok := f.Responses[key]
	if !ok {
		resp = f.DefaultResp
	}

	if resp.Err != nil {
		return []byte(resp.Stdout), []byte(resp.Stderr), resp.Err
	}

	if resp.ExitCode != 0 {
		return []byte(resp.Stdout), []byte(resp.Stderr), &ExitError{
			Code:   resp.ExitCode,
			Args:   args,
			Stderr: resp.Stderr,
		}
	}

	return []byte(resp.Stdout), []byte(resp.Stderr), nil
}
