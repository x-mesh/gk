package aicommit

import (
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

// defaultPrompter uses ui.Confirm for a simple Keep-or-Drop decision.
// The richer keymap (regenerate / edit / view diff) lives behind a
// huh form invoked from the CLI wire-up; v1 ships with the minimal
// flow to get the feature out the door.
type defaultPrompter struct{}

func (defaultPrompter) Prompt(index, total int, m Message) (ReviewDecision, error) {
	title := fmt.Sprintf("[%d/%d] apply %s: %s ?", index, total, m.Group.Type, m.Subject)
	ok, err := ui.Confirm(title, true)
	if err != nil {
		return ReviewDecision{}, err
	}
	if !ok {
		return ReviewDecision{Drop: true}, nil
	}
	return ReviewDecision{Keep: true}, nil
}
