package cli

import (
	"errors"
	"fmt"
	"strings"
)

// hintError wraps an error with a short "next step" hint rendered after the
// primary error line. The hint is advisory and does not affect errors.Is /
// errors.As chains — the wrapped error is always reachable via Unwrap.
type hintError struct {
	err  error
	hint string
}

func (e *hintError) Error() string { return e.err.Error() }
func (e *hintError) Unwrap() error { return e.err }

// WithHint decorates err with a one-line remediation hint. Passing a nil err
// returns nil. An empty hint is ignored (err is returned unchanged).
func WithHint(err error, hint string) error {
	if err == nil {
		return nil
	}
	if hint = strings.TrimSpace(hint); hint == "" {
		return err
	}
	return &hintError{err: err, hint: hint}
}

// HintFrom walks the error chain and returns the first hint found, or "".
func HintFrom(err error) string {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if he, ok := e.(*hintError); ok && strings.TrimSpace(he.hint) != "" {
			return he.hint
		}
	}
	return ""
}

// FormatError returns the user-facing representation of an error raised by
// cli.Execute. Renders as:
//
//	gk: <error message>
//	  hint: <hint>  (when present)
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	out := "gk: " + err.Error()
	if h := HintFrom(err); h != "" {
		out += "\n  hint: " + h
	}
	return out
}

// hintCommand is a compact helper so call sites read like:
//
//	return WithHint(err, hintCommand("gk continue"))
func hintCommand(cmd string) string { return fmt.Sprintf("try: %s", cmd) }
