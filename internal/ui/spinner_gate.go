package ui

import (
	"io"
	"os"
	"strings"
)

// SpinnerOff, when set, turns every spinner off while it returns true. The
// cli sets it for agent mode and --debug, which this package cannot see.
var SpinnerOff func() bool

func spinnerOffNow() bool { return SpinnerOff != nil && SpinnerOff() }

// spinnerAllowed reports whether a spinner may draw on w. A writer other
// than stderr is a test buffer and always draws.
func spinnerAllowed(w io.Writer) bool {
	if w != os.Stderr {
		return true
	}
	return spinnerAllowedFor(IsStderrTerminal(), os.Getenv("CI"), os.Getenv("TERM"), spinnerOffNow())
}

// spinnerAllowedFor allows a spinner only where its frames are erased. A
// pipe or a file keeps every frame. A CI job can give the command a terminal
// (docker run -t, script) and still keep a log. TERM=dumb does not move the
// cursor back, so the frames run together on one line.
func spinnerAllowedFor(stderrTTY bool, ci, term string, off bool) bool {
	return stderrTTY && !EnvTruthy(ci) && term != "dumb" && !off
}

// EnvTruthy reads an environment flag such as CI: empty, 0, false, no, and
// off are false, and any other value is true.
func EnvTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
