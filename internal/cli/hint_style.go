package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/x-mesh/gk/internal/ui"
)

// successLine formats a "✓ <verb> <target>" success message with the
// project's standard color treatment: bold green check, plain verb,
// green target. Use for short post-action confirmation lines printed
// to stdout after a command succeeds.
//
// Example: `successLine("popped", "stash@{0}")` →
// `"✓ popped stash@{0}"` with the check bold-green and the ref green.
func successLine(verb, target string) string {
	if target == "" {
		return cellGreenBold("✓") + " " + verb
	}
	return cellGreenBold("✓") + " " + verb + " " + cellGreen(target)
}

// successLinef is the formatted-target variant: target portion is
// rendered through fmt.Sprintf so callers can embed multiple tokens
// (e.g. "main → feature") while still getting the standard ✓+green
// treatment around them.
func successLinef(verb, format string, args ...any) string {
	return successLine(verb, fmt.Sprintf(format, args...))
}

// stylizeHintLine paints the post-action nudge lines printed under a
// completed (or refused) gk command — `next:`, `also:`, `hint:`, and
// `try:`. The label is dimmed; when the body is a runnable command
// (starts with `gk `, `git `, etc.) it is highlighted in cyan and a
// trailing `"(...)"` annotation (like `(fully merged)`) is dimmed so
// it reads as metadata rather than part of the command. Bodies that
// are plain advisory text get the dim-label treatment but stay plain
// otherwise — coloring narrative prose feels noisy.
//
// Lines that don't match a known prefix fall through unchanged. This
// keeps Easy-mode wording (symbols like `↑`/`※`/`↓`) and structured
// multi-line forms (where the `next:` line stands alone above its
// command list) from being mis-styled.
//
// Multi-line callers should run stylizeHintLabel on the label line
// and pass each command line through stylizeHintCommand individually.
//
// All color routes through the cell_color helpers, so NoColor/CI
// captures degrade cleanly to plain text.
func stylizeHintLine(line string) string {
	for _, prefix := range []string{"next: ", "also: ", "hint: ", "try: "} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		body := line[len(prefix):]
		if !looksLikeCommand(body) {
			// Advisory wording — dim the label, leave the body alone.
			return cellFaint(strings.TrimRight(prefix, " ")) + " " + body
		}
		// Pull off a trailing "(...)" annotation so it can be dimmed
		// separately from the command itself.
		cmd, meta := body, ""
		if i := strings.LastIndex(body, " ("); i > 0 && strings.HasSuffix(body, ")") {
			cmd, meta = body[:i], body[i+1:]
		}
		out := cellFaint(prefix) + cellCyan(cmd)
		if meta != "" {
			out += " " + cellFaint(meta)
		}
		return out
	}
	return line
}

// renderAdvisory renders a gk-origin advisory block in the shared bar-section
// chrome so users can attribute the message to gk at a glance — pull/push
// interleave gk's guidance with raw git output on the same stderr stream,
// and a bare "note:" line reads as either:
//
//	█  NOTE
//	   'main' has no upstream configured — using origin/main
//	   set tracking with: git branch --set-upstream-to=origin/main main
//
// kind selects the label and chrome colour: "note" (informational, steel
// blue) or "hint" (actionable, orange). Body lines pass through
// stylizeHintLine / stylizeHintCommand so labelled nudges ("try: gk …") and
// bare commands keep the project-standard colouring. The trailing blank line
// RenderSection emits for section stacking is trimmed — advisories sit
// inline in command output, not in a section sequence.
func renderAdvisory(kind string, lines []string) string {
	chrome := ui.SectionInfo
	if kind == "hint" {
		chrome = ui.SectionAction
	}
	styled := make([]string, 0, len(lines))
	for _, ln := range lines {
		s := stylizeHintLine(ln)
		if s == ln {
			s = stylizeHintCommand(ln)
		}
		styled = append(styled, s)
	}
	out := ui.RenderSection(kind, "", styled, ui.SectionOpts{Color: chrome})
	return strings.TrimRight(out, "\n") + "\n"
}

// printNote writes a NOTE advisory block to w. Callers append an Easy-Mode
// elaboration line via easyNoteLine/appendEasyLine before calling so the
// block carries the beginner explanation when Easy Mode is active.
func printNote(w io.Writer, lines ...string) {
	fmt.Fprint(w, renderAdvisory("note", lines))
}

// easyNoteLine returns the Easy-Mode elaboration registered under key, or ""
// when Easy Mode is off or the catalog has no entry. Advisory notes are terse
// one-liners aimed at git-fluent users; Easy Mode appends one plain-language
// line explaining what the situation means and whether the user must act.
//
// args is a plain slice (not variadic) on purpose: catalog keys carry no
// formatting directives themselves — the directives live in the registered
// message — so a variadic (string, ...any) shape gets classified as a printf
// wrapper by go vet, which then flags every call site for "arguments but no
// formatting directives" in the key literal.
func easyNoteLine(key string, args []any) string {
	eng := EasyEngine()
	if eng == nil || !eng.IsEnabled() {
		return ""
	}
	s := eng.Format(key, args...)
	// Catalog miss echoes the raw key (plus fmt EXTRA noise when args are
	// present) — suppress rather than leak internals to the user.
	if s == "" || strings.HasPrefix(s, key) {
		return ""
	}
	return s
}

// appendEasyLine appends easyNoteLine(key, args) to lines when it yields
// content; no-op otherwise. Keeps note call sites to a single expression.
func appendEasyLine(lines []string, key string, args ...any) []string {
	if s := easyNoteLine(key, args); s != "" {
		return append(lines, s)
	}
	return lines
}

// stylizeHintLabel dims a bare hint label (e.g. "next:" on its own line
// above a list of follow-up commands). Returns the input unchanged when
// it doesn't look like one of the known labels.
func stylizeHintLabel(label string) string {
	trimmed := strings.TrimSpace(label)
	switch trimmed {
	case "next:", "also:", "hint:", "try:":
		return cellFaint(label)
	}
	return label
}

// stylizeHintCommand paints a single follow-up command line printed
// under a bare hint label. Lines beginning with a `#` comment are
// dimmed; runnable command lines are cyan. Leading whitespace is
// preserved so column alignment with the label survives.
func stylizeHintCommand(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return line
	}
	indent := line[:len(line)-len(trimmed)]
	if strings.HasPrefix(trimmed, "#") {
		return indent + cellFaint(trimmed)
	}
	if looksLikeCommand(trimmed) {
		return indent + cellCyan(trimmed)
	}
	return line
}

// looksLikeCommand returns true for lines that read as a runnable
// shell command — the heuristic is intentionally tight to avoid
// repainting prose ("uncomment rules, then run …").
func looksLikeCommand(body string) bool {
	for _, prefix := range []string{"gk ", "git ", "brew ", "go ", "npm ", "echo ", "rm ", "cd "} {
		if strings.HasPrefix(body, prefix) {
			return true
		}
	}
	return false
}
