package aicommit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/x-mesh/gk/internal/ui"
)

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
	_ = printSummary(out, messages)

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
// can tee this into a log before prompting.
func printSummary(out io.Writer, messages []Message) error {
	if len(messages) == 0 {
		_, err := fmt.Fprintln(out, "no commit groups proposed")
		return err
	}
	_, _ = fmt.Fprintf(out, "Proposed %d commit(s):\n\n", len(messages))
	for i, m := range messages {
		header := m.Group.Type
		if m.Group.Scope != "" {
			header += "(" + m.Group.Scope + ")"
		}
		header += ": " + m.Subject
		_, _ = fmt.Fprintf(out, "  [%d/%d] %s\n", i+1, len(messages), header)
		for _, f := range m.Group.Files {
			_, _ = fmt.Fprintf(out, "         • %s\n", f)
		}
		if m.Body != "" {
			indented := "         │ " + strings.ReplaceAll(strings.TrimSpace(m.Body), "\n", "\n         │ ")
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
