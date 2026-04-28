package ui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

// ConflictChoice represents a conflict resolution action.
type ConflictChoice string

const (
	ChoiceContinue ConflictChoice = "continue"
	ChoiceAbort    ConflictChoice = "abort"
	ChoiceSkip     ConflictChoice = "skip"
	ChoiceEdit     ConflictChoice = "edit"
	ChoiceOurs     ConflictChoice = "ours"
	ChoiceTheirs   ConflictChoice = "theirs"
	ChoiceQuit     ConflictChoice = "quit"
)

// ErrNonInteractive is returned when a prompt is requested but stdin/stdout is not a TTY
// and no default has been provided.
var ErrNonInteractive = errors.New("no TTY: cannot prompt")

// Prompter is the interface consumed by CLI commands for interactive decisions.
// Implementations: TermPrompter (huh), AutoPrompter (non-interactive with fixed default).
type Prompter interface {
	// ConflictChoice asks the user to pick a conflict resolution action.
	// title: prompt title; context: summary text to print before the prompt.
	ConflictChoice(title, context string, allowed []ConflictChoice) (ConflictChoice, error)
}

// IsTerminal reports whether both stdin and stdout are TTYs.
func IsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stdin.Fd()))
}

// IsStderrTerminal reports whether stderr is a TTY. Used when a command
// wants to draw progress output without polluting a piped stdout —
// `gk status` prints porcelain to stdout but a fetch spinner to stderr.
func IsStderrTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// NewPrompter returns TermPrompter if stdout is a TTY, otherwise AutoPrompter(def).
func NewPrompter(def ConflictChoice) Prompter {
	if IsTerminal() {
		return &TermPrompter{}
	}
	return &AutoPrompter{Default: def}
}

// TermPrompter renders interactive bubbletea prompts on a real TTY.
type TermPrompter struct{}

func (p *TermPrompter) ConflictChoice(promptTitle, promptContext string, allowed []ConflictChoice) (ConflictChoice, error) {
	if !IsTerminal() {
		return "", ErrNonInteractive
	}
	if promptContext != "" {
		fmt.Fprintln(os.Stderr, promptContext)
	}
	items := make([]PickerItem, 0, len(allowed))
	for _, c := range allowed {
		items = append(items, PickerItem{Key: string(c), Display: string(c)})
	}
	picker := &TablePicker{}
	pick, err := picker.Pick(context.Background(), promptTitle, items)
	if err != nil {
		return "", err
	}
	return ConflictChoice(pick.Key), nil
}

// Confirm shows a yes/no prompt and returns the user's choice. On a non-TTY
// session it returns ErrNonInteractive so callers can require --yes flags.
func Confirm(title string, defaultYes bool) (bool, error) {
	return ConfirmTUI(context.Background(), title, "", defaultYes)
}

// AutoPrompter returns a fixed default choice without user interaction.
// Used in non-TTY environments (CI, piped input).
type AutoPrompter struct {
	Default ConflictChoice
}

func (p *AutoPrompter) ConflictChoice(title, context string, allowed []ConflictChoice) (ConflictChoice, error) {
	if p.Default == "" {
		return "", ErrNonInteractive
	}
	for _, c := range allowed {
		if c == p.Default {
			return p.Default, nil
		}
	}
	return "", ErrNonInteractive
}
