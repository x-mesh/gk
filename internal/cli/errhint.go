package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/ui"
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
//	gk: <error message>          (red)
//	  hint: <hint>                (magenta label, faint body)
//
// When Easy Mode is active and --json is not set, the error is formatted
// with emoji and beginner-friendly language via the EasyFormatter.
func FormatError(err error) string {
	if err == nil {
		return ""
	}

	// Easy Mode branch: use EasyFormatter for friendlier output.
	// Skip when --json is active (Property 9: JSON Mode Bypass).
	if eng := EasyEngine(); eng != nil && eng.IsEnabled() && !JSONOut() {
		fmtr := ui.NewEasyFormatter(nil, NoColorFlag())
		if eng.Hints() != nil && eng.Hints().Level() != "off" {
			fmtr = ui.NewEasyFormatter(
				// Re-create with emoji from engine internals is not
				// exposed; use a nil emoji mapper — FormatError adds
				// its own prefixes.
				nil,
				NoColorFlag(),
			)
		}
		translated := eng.TranslateTerms(err.Error())
		hint := HintFrom(err)
		if hint != "" {
			hint = eng.TranslateTerms(hint)
		}
		return fmtr.FormatError(fmt.Errorf("%s", translated), hint)
	}

	prefix := color.New(color.FgRed, color.Bold).Sprint("gk:")
	out := prefix + " " + err.Error()
	if h := HintFrom(err); h != "" {
		hintLabel := color.New(color.FgMagenta, color.Bold).Sprint("hint:")
		hintBody := color.New(color.Faint).Sprint(h)
		out += "\n  " + hintLabel + " " + hintBody
	}
	return out
}

// hintCommand is a compact helper so call sites read like:
//
//	return WithHint(err, hintCommand("gk continue"))
func hintCommand(cmd string) string { return fmt.Sprintf("try: %s", cmd) }
