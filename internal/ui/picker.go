package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ErrPickerAborted is returned when the user cancels the picker (Ctrl+C / ESC / empty input).
var ErrPickerAborted = errors.New("picker aborted")

// PickerItem represents one selectable row.
// Display is what's shown in the list. Preview is optional extra text
// shown alongside the row (currently rendered only by TablePicker).
// Key is a stable value returned by the picker (usually equal to Display
// but may be a hash/ref so the caller can look up richer data).
// Cells, when non-empty, lets table-style pickers (TablePicker) render
// the row split into columns. Pickers that don't support columns
// (FallbackPicker) ignore Cells and fall back to Display.
// ExtraAction is set by TablePicker when an Extra hotkey with Exit=true
// fires — it carries the key letter so callers can dispatch on it. The
// rest of the PickerItem reflects the row under the cursor at exit.
type PickerItem struct {
	Display     string
	Preview     string
	Key         string
	Cells       []string
	ExtraAction string
}

// Picker selects one item from a list.
type Picker interface {
	Pick(ctx context.Context, title string, items []PickerItem) (PickerItem, error)
}

// NewPicker returns the active picker for the current environment.
// Resolution order:
//  1. TablePicker — when stdout/stdin are a TTY (the common case)
//  2. FallbackPicker — non-TTY: numbered list on stderr
func NewPicker() Picker {
	if IsTerminal() {
		return &TablePicker{}
	}
	return &FallbackPicker{In: os.Stdin, Out: os.Stderr}
}

// --- FallbackPicker ---

// FallbackPicker provides a simple numbered list prompt for non-TTY environments.
type FallbackPicker struct {
	In  io.Reader
	Out io.Writer
}

func (p *FallbackPicker) Pick(ctx context.Context, title string, items []PickerItem) (PickerItem, error) {
	if len(items) == 0 {
		return PickerItem{}, errors.New("no items to pick")
	}
	fmt.Fprintln(p.Out, title)
	for i, it := range items {
		fmt.Fprintf(p.Out, "%2d) %s\n", i+1, it.Display)
	}
	fmt.Fprint(p.Out, "> ")
	scanner := bufio.NewScanner(p.In)
	if !scanner.Scan() {
		return PickerItem{}, ErrPickerAborted
	}
	s := strings.TrimSpace(scanner.Text())
	if s == "" || s == "q" || s == "quit" {
		return PickerItem{}, ErrPickerAborted
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > len(items) {
		return PickerItem{}, fmt.Errorf("invalid selection %q", s)
	}
	return items[n-1], nil
}
