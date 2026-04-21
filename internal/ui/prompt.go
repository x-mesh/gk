package ui

import (
	"errors"
	"os"

	"github.com/charmbracelet/huh"
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

// NewPrompter returns TermPrompter if stdout is a TTY, otherwise AutoPrompter(def).
func NewPrompter(def ConflictChoice) Prompter {
	if IsTerminal() {
		return &TermPrompter{}
	}
	return &AutoPrompter{Default: def}
}

// TermPrompter renders interactive huh prompts on a real TTY.
type TermPrompter struct{}

func (p *TermPrompter) ConflictChoice(title, context string, allowed []ConflictChoice) (ConflictChoice, error) {
	if !IsTerminal() {
		return "", ErrNonInteractive
	}
	var picked string
	options := make([]huh.Option[string], 0, len(allowed))
	for _, c := range allowed {
		options = append(options, huh.NewOption(string(c), string(c)))
	}
	form := huh.NewSelect[string]().
		Title(title).
		Description(context).
		Options(options...).
		Value(&picked)
	if err := form.Run(); err != nil {
		return "", err
	}
	return ConflictChoice(picked), nil
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
