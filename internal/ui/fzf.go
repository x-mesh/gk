package ui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ErrPickerAborted is returned when the user cancels the picker (Ctrl+C / ESC / empty input).
var ErrPickerAborted = errors.New("picker aborted")

// PickerItem represents one selectable row.
// Display is what's shown in the list. Preview is optional extra text
// shown in the fzf --preview pane (empty → no preview for this row).
// Key is a stable value returned by the picker (usually equal to Display
// but may be a hash/ref so the caller can look up richer data).
// Cells, when non-empty, lets table-style pickers (TablePicker) render
// the row split into columns. Pickers that don't support columns
// (FzfPicker, FallbackPicker) ignore Cells and fall back to Display.
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

// FzfAvailable reports whether an `fzf` binary is on PATH AND stdout is a TTY.
// (fzf without a TTY is useless.)
func FzfAvailable() bool {
	if !IsTerminal() {
		return false
	}
	_, err := exec.LookPath("fzf")
	return err == nil
}

// NewPicker returns the active picker for the current environment.
// Resolution order:
//  1. TablePicker — when stdout/stdin are a TTY (the common case)
//  2. FallbackPicker — non-TTY: numbered list on stderr
//
// FzfPicker remains in this package for callers that need a fuzzy
// search out of the box, but the default path now flows through
// bubbletea so picker UX stays consistent with the rest of gk.
func NewPicker() Picker {
	if IsTerminal() {
		return &TablePicker{}
	}
	return &FallbackPicker{In: os.Stdin, Out: os.Stderr}
}

// --- FzfPicker ---

// FzfPicker invokes the fzf binary for interactive item selection.
type FzfPicker struct {
	ExtraArgs []string // e.g., ["--height=40%", "--border"]
}

func (p *FzfPicker) Pick(ctx context.Context, title string, items []PickerItem) (PickerItem, error) {
	if len(items) == 0 {
		return PickerItem{}, errors.New("no items to pick")
	}

	// Build input: one line per item, TAB-separated Key <-> Display.
	// fzf will show both and return the whole line; we split on the first TAB.
	var in bytes.Buffer
	havePreview := false
	for _, it := range items {
		if it.Preview != "" {
			havePreview = true
		}
		key := it.Key
		if key == "" {
			key = it.Display
		}
		fmt.Fprintf(&in, "%s\t%s\n", key, it.Display)
	}

	args := []string{
		"--with-nth=2..",
		"--delimiter=\t",
		"--prompt=" + title + "> ",
		"--height=40%",
		"--reverse",
		"--ansi",
	}
	args = append(args, p.ExtraArgs...)

	if havePreview {
		// Use a temp file mapping key -> preview so we can render via `sh -c`
		// without shelling out to another go helper. Format: key\tpreview\n
		pf, err := writePreviewMap(items)
		if err == nil {
			defer os.Remove(pf)
			args = append(args, "--preview",
				`awk -F'\t' -v k="$(echo {} | cut -f1)" '$1==k { sub(/^[^\t]*\t/,""); gsub(/\\n/,"\n"); print; exit }' `+shellQuote(pf),
			)
			args = append(args, "--preview-window=right:50%")
		}
	}

	cmd := exec.CommandContext(ctx, "fzf", args...)
	cmd.Stdin = &in
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ee.ExitCode() == 130 { // Ctrl+C / ESC
				return PickerItem{}, ErrPickerAborted
			}
		}
		return PickerItem{}, fmt.Errorf("fzf: %w", err)
	}

	choice := strings.TrimRight(out.String(), "\n")
	if choice == "" {
		return PickerItem{}, ErrPickerAborted
	}
	parts := strings.SplitN(choice, "\t", 2)
	key := parts[0]
	for _, it := range items {
		k := it.Key
		if k == "" {
			k = it.Display
		}
		if k == key {
			return it, nil
		}
	}
	return PickerItem{Key: key, Display: key}, nil
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

// --- helpers ---

func writePreviewMap(items []PickerItem) (string, error) {
	f, err := os.CreateTemp("", "gk-preview-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, it := range items {
		if it.Preview == "" {
			continue
		}
		key := it.Key
		if key == "" {
			key = it.Display
		}
		// escape newlines as literal \n for single-line storage
		prev := strings.ReplaceAll(it.Preview, "\n", `\n`)
		fmt.Fprintf(f, "%s\t%s\n", key, prev)
	}
	return f.Name(), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
