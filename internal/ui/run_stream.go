package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// streamChunkMsg carries one chunk of bytes (typically a line) from the
// child process's stdout/stderr.
type streamChunkMsg string

// streamDoneMsg signals the child has exited; Err is nil on exit code 0.
type streamDoneMsg struct{ Err error }

type streamModel struct {
	title     string
	vp        viewport.Model
	buf       strings.Builder
	done      bool
	err       error
	cancelled bool
	ready     bool
}

func (m streamModel) Init() tea.Cmd { return nil }

func (m streamModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case streamChunkMsg:
		m.buf.WriteString(string(msg))
		m.vp.SetContent(m.buf.String())
		m.vp.GotoBottom()
		return m, nil
	case streamDoneMsg:
		m.done = true
		m.err = msg.Err
		return m, tea.Quit
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		// Reserve title (1) + spacer (1) + help (1) + 2 border rows.
		h := msg.Height - 5
		if h < 5 {
			h = 5
		}
		w := msg.Width - 2
		if w < 20 {
			w = 20
		}
		if !m.ready {
			m.vp = viewport.New(w, h)
			m.vp.SetContent(m.buf.String())
			m.ready = true
		} else {
			m.vp.Width = w
			m.vp.Height = h
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m streamModel) View() string {
	if !m.ready {
		return ""
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("99"))

	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n")
	b.WriteString(box.Render(m.vp.View()))
	b.WriteString("\n")
	b.WriteString(hint.Render("j/k scroll · esc cancel"))
	return b.String()
}

// RunCommandStreamTUI launches `name` with `args`, streams its combined
// stdout/stderr into a scrollable viewport in real time, and returns
// the process's exit error (or ErrPickerAborted on user cancel).
//
// On non-TTY environments the call degrades to a normal exec.Cmd that
// inherits the parent's stdout/stderr — no TUI is drawn.
func RunCommandStreamTUI(ctx context.Context, title, name string, args ...string) error {
	if !IsTerminal() {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Independent context so cancelling the TUI ("esc") kills the child
	// without aborting the whole gk command's parent context.
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(childCtx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stream pipe stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stream pipe stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}

	prog := tea.NewProgram(
		streamModel{title: title},
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
		tea.WithAltScreen(),
	)

	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for s.Scan() {
			prog.Send(streamChunkMsg(s.Text() + "\n"))
		}
	}
	go pipe(stdout)
	go pipe(stderr)

	go func() {
		wg.Wait()
		err := cmd.Wait()
		prog.Send(streamDoneMsg{Err: err})
	}()

	final, runErr := prog.Run()
	if runErr != nil {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("stream tui: %w", runErr)
	}
	m := final.(streamModel)
	if m.cancelled {
		cancel()
		_ = cmd.Wait()
		return ErrPickerAborted
	}
	return m.err
}
