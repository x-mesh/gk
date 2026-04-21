package git

import (
	"context"
	"fmt"
	"strings"
)

// Runner executes git commands in a controlled environment.
// Implementations must ensure deterministic, locale-independent output.
type Runner interface {
	// Run executes `git <args...>` and returns captured stdout/stderr.
	// Non-zero exit codes produce a non-nil error; stdout/stderr are still returned.
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// ExitError wraps a non-zero git exit with its code.
type ExitError struct {
	Code   int
	Args   []string
	Stderr string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("git %s: exit code %d: %s", strings.Join(e.Args, " "), e.Code, e.Stderr)
}
