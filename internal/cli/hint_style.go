package cli

import (
	"fmt"
	"strings"
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
