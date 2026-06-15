package aicommit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/x-mesh/gk/internal/ui"
)

// fileDelta renders a FileStat's line delta in the digest's "+A −B"
// form (U+2212 minus, matching `gk diff --digest`).
func fileDelta(s FileStat) string {
	return fmt.Sprintf("+%d −%d", s.Added, s.Deleted)
}

// ReviewDecision is the per-group action a user picked in ReviewPlan.
type ReviewDecision struct {
	// Keep is true when the group should be applied as shown.
	Keep bool
	// Drop is true when the user chose to skip this group.
	Drop bool
	// Regenerate is true when the user asked for a retry; callers re-run
	// Compose on this single group with a fresh retry counter.
	Regenerate bool
	// EditedSubject / EditedBody capture in-place edits. When both are
	// empty the original message is kept.
	EditedSubject string
	EditedBody    string
}

// ReviewOptions shapes ReviewPlan rendering + behaviour.
//
// Out is where the summary is written (stdout by default). Force
// short-circuits review: every group's Decision comes back Keep=true
// without prompting. NonInteractive is for CI: when true and Force is
// false the function returns ErrNonInteractive immediately so callers
// can require --yes/--force on non-TTY sessions.
type ReviewOptions struct {
	Out            io.Writer
	Force          bool
	NonInteractive bool
	// Prompter is an optional hook for tests — when nil ReviewPlan
	// falls back to ui.Confirm for accept/drop prompts.
	Prompter ReviewPrompter
	// FileStats maps a changed path to its display-ready change stats.
	// When non-nil, printSummary annotates each file with its change
	// kind + line delta and prints a totals line; nil keeps the plain
	// "• path" preview (plan-run / tests / a digest that failed to
	// build). aicommit never parses a diff itself — the CLI builds this.
	FileStats map[string]FileStat
}

// FileStat is display-ready per-file change info for the plan preview,
// mirroring one row of `gk diff --digest`. All fields are presentation
// values the caller already computed (glyph mapped, symbols joined and
// truncated, docs/build symbols suppressed); printSummary only lays
// them out.
type FileStat struct {
	// Glyph is the single-char change kind (M/A/D/R/C/T). Empty defaults
	// to "M" at render time.
	Glyph   string
	Added   int
	Deleted int
	// Symbols is the pre-joined "fn, fn2, +N more" display string; empty
	// when the file has no informative symbols (or is docs/build).
	Symbols string
}

// ReviewPrompter abstracts the per-message accept/regenerate/drop
// prompt so tests can inject decisions without spawning huh.
type ReviewPrompter interface {
	Prompt(index, total int, m Message) (ReviewDecision, error)
}

// ErrReviewAborted is returned when the user bails out of the review
// (ctrl-C in huh, or an explicit "abort" pick).
var ErrReviewAborted = errors.New("review aborted by user")

// ReviewPlan prints a summary of all proposed commits, then asks the
// user for a per-group decision. The returned slice lines up 1:1 with
// messages; callers filter on Decision.Keep to build the final apply
// list.
func ReviewPlan(messages []Message, opts ReviewOptions) ([]ReviewDecision, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	_ = printSummary(out, messages, opts.FileStats)

	if opts.Force {
		decisions := make([]ReviewDecision, len(messages))
		for i := range decisions {
			decisions[i] = ReviewDecision{Keep: true}
		}
		return decisions, nil
	}
	if opts.NonInteractive {
		return nil, ui.ErrNonInteractive
	}
	prompter := opts.Prompter
	if prompter == nil {
		prompter = defaultPrompter{}
	}

	decisions := make([]ReviewDecision, 0, len(messages))
	for i, m := range messages {
		d, err := prompter.Prompt(i+1, len(messages), m)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}

// splitCommitMessage parses a textarea-edited message back into subject
// + body. The first line is the subject (trimmed of CR for Windows
// line endings); everything after the first blank line is the body.
// When there's no blank separator, anything past the first line is
// treated as body so the user's edits aren't silently dropped.
func splitCommitMessage(s string) (subject, body string) {
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return "", ""
	}
	subject = strings.TrimRight(lines[0], "\r")
	if len(lines) == 1 {
		return subject, ""
	}
	rest := lines[1:]
	// Skip leading blank lines so a single blank separator after the
	// subject doesn't leak into the body as a leading newline.
	for len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
		rest = rest[1:]
	}
	body = strings.TrimRight(strings.Join(rest, "\n"), "\n")
	return subject, body
}

// renderMessageBody composes the per-message detail block shown inside
// the review TUI's scrollable viewport: file list, then the body. Kept
// independent of printSummary so review can show one message at a time
// while printSummary still gives the user a top-of-loop overview.
func renderMessageBody(m Message) string {
	var b strings.Builder
	if len(m.Group.Files) > 0 {
		b.WriteString("Files:\n")
		for _, f := range m.Group.Files {
			fmt.Fprintf(&b, "  • %s\n", f)
		}
	}
	if m.Body != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(m.Body))
		b.WriteString("\n")
	}
	if len(m.Footers) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		for _, f := range m.Footers {
			fmt.Fprintf(&b, "%s: %s\n", f.Token, f.Value)
		}
	}
	if b.Len() == 0 {
		b.WriteString("(no body)")
	}
	return b.String()
}

// printSummary writes a tabular preview of the plan to out. Callers
// can tee this into a log before prompting. When stats carries per-file
// change info the file list gains a change-kind glyph + line delta and
// the header gains a "· F file(s) · +A −D" totals tail; a nil/empty map
// degrades to the plain count header and "• path" file list. Output is
// intentionally ANSI-free so a tee'd log stays readable.
func printSummary(out io.Writer, messages []Message, stats map[string]FileStat) error {
	if len(messages) == 0 {
		_, err := fmt.Fprintln(out, "no commit groups proposed")
		return err
	}

	// One pass over the plan's files to size the path + delta columns
	// and total the deltas — all only meaningful when stats is supplied.
	haveStats := len(stats) > 0
	var totalFiles, totalAdd, totalDel, pathW, deltaW int
	if haveStats {
		for _, m := range messages {
			for _, f := range m.Group.Files {
				totalFiles++
				if n := len(f); n > pathW {
					pathW = n
				}
				s, ok := stats[f]
				if !ok {
					continue
				}
				totalAdd += s.Added
				totalDel += s.Deleted
				// Rune count, not byte count: the "−" (U+2212) is 3 bytes
				// but one display column, so byte width would over-pad.
				if n := utf8.RuneCountInString(fileDelta(s)); n > deltaW {
					deltaW = n
				}
			}
		}
	}

	if haveStats {
		_, _ = fmt.Fprintf(out, "Proposed %d commit(s) · %d file(s) · +%d −%d\n\n",
			len(messages), totalFiles, totalAdd, totalDel)
	} else {
		_, _ = fmt.Fprintf(out, "Proposed %d commit(s):\n\n", len(messages))
	}

	for i, m := range messages {
		_, _ = fmt.Fprintf(out, "  [%d/%d] %s\n", i+1, len(messages), m.Header())
		for _, f := range m.Group.Files {
			s, ok := stats[f]
			if !haveStats || !ok {
				_, _ = fmt.Fprintf(out, "    • %s\n", f)
				continue
			}
			glyph := s.Glyph
			if glyph == "" {
				glyph = "M"
			}
			// Pad the delta to deltaW runes (not bytes) so the symbol
			// column lines up despite the multi-byte "−".
			delta := fileDelta(s)
			if pad := deltaW - utf8.RuneCountInString(delta); pad > 0 {
				delta += strings.Repeat(" ", pad)
			}
			line := fmt.Sprintf("    %s  %-*s  %s", glyph, pathW, f, delta)
			if s.Symbols != "" {
				line += "  " + s.Symbols
			}
			_, _ = fmt.Fprintln(out, strings.TrimRight(line, " "))
		}
		if m.Body != "" {
			indented := "    │ " + strings.ReplaceAll(strings.TrimSpace(m.Body), "\n", "\n    │ ")
			_, _ = fmt.Fprintln(out, indented)
		}
		if i != len(messages)-1 {
			_, _ = fmt.Fprintln(out)
		}
	}
	return nil
}

// defaultPrompter offers keep / edit / regen / drop on a TablePicker
// and surfaces a textarea editor when the user picks "edit". A single
// CLI flow now covers the path that used to need huh forms.
type defaultPrompter struct{}

func (defaultPrompter) Prompt(index, total int, m Message) (ReviewDecision, error) {
	current := m
	for {
		title := fmt.Sprintf("[%d/%d] %s: %s", index, total, current.Group.Type, current.Subject)
		body := renderMessageBody(current)
		options := []ui.ScrollSelectOption{
			{Key: "k", Value: "keep", Display: "keep — apply as proposed", IsDefault: true},
			{Key: "e", Value: "edit", Display: "edit — modify subject/body before applying"},
			{Key: "r", Value: "regen", Display: "regen — ask the model for a new draft"},
			{Key: "d", Value: "drop", Display: "drop — skip this commit group"},
		}
		choice, err := ui.ScrollSelectTUI(context.Background(), title, body, options)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return ReviewDecision{}, ErrReviewAborted
			}
			return ReviewDecision{}, err
		}

		switch choice {
		case "", "keep":
			if choice == "" {
				return ReviewDecision{}, ErrReviewAborted
			}
			// Edits applied in a previous loop iteration are surfaced
			// here via current.Subject / current.Body.
			d := ReviewDecision{Keep: true}
			if current.Subject != m.Subject {
				d.EditedSubject = current.Subject
			}
			if current.Body != m.Body {
				d.EditedBody = current.Body
			}
			return d, nil
		case "drop":
			return ReviewDecision{Drop: true}, nil
		case "regen":
			return ReviewDecision{Regenerate: true}, nil
		case "edit":
			initial := current.Subject
			if strings.TrimSpace(current.Body) != "" {
				initial += "\n\n" + current.Body
			}
			edited, err := ui.EditTextTUI(context.Background(),
				"edit commit message",
				"first line is the subject · blank line, then body · ctrl+d save · esc cancel",
				initial, 80, 16)
			if err != nil {
				if errors.Is(err, ui.ErrPickerAborted) {
					// User backed out of editing — return to the review
					// menu without committing the partial change.
					continue
				}
				return ReviewDecision{}, err
			}
			current.Subject, current.Body = splitCommitMessage(edited)
			// Loop back to the review screen so the user can confirm
			// (keep), drop, regen, or re-edit the new draft.
		}
	}
}
