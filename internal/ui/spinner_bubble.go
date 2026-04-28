package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type bubbleSpinnerModel struct {
	sp  spinner.Model
	msg string
}

func (m bubbleSpinnerModel) Init() tea.Cmd { return m.sp.Tick }

func (m bubbleSpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.sp, cmd = m.sp.Update(msg)
	return m, cmd
}

func (m bubbleSpinnerModel) View() string {
	return m.sp.View() + " " + m.msg
}

// StartBubbleSpinner draws a bubbletea-driven inline spinner on stderr
// until stop() is called. Non-TTY stderr (pipes, CI, `2>file`) makes it
// a no-op so piped output streams stay clean. The first frame is delayed
// by SpinnerStartDelay so sub-150ms ops never paint anything to clear.
//
// Same idempotent-safe contract as StartSpinner: pair with defer for
// exception-safe cleanup.
func StartBubbleSpinner(msg string) (stop func()) {
	return startBubbleSpinnerTo(os.Stderr, msg)
}

func startBubbleSpinnerTo(w io.Writer, msg string) (stop func()) {
	if w == os.Stderr && !IsStderrTerminal() {
		return func() {}
	}

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	prog := tea.NewProgram(
		bubbleSpinnerModel{sp: sp, msg: msg},
		tea.WithOutput(w),
		tea.WithInput(strings.NewReader("")),
		tea.WithoutSignalHandler(),
	)

	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		select {
		case <-done:
			return // op finished before SpinnerStartDelay → never draw
		case <-time.After(SpinnerStartDelay):
		}
		_, _ = prog.Run()
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done) // unblock the start-delay select
			prog.Kill() // cancel ctx — graceful exit if prog.Run is in progress
			<-stopped   // wait for the goroutine
			// Belt-and-suspenders clear so legacy terminals that don't
			// parse `\x1b[2K` (serial consoles, some CI log viewers)
			// still end clean: overwrite with spaces first, then emit
			// the erase.
			pad := strings.Repeat(" ", len(msg)+4)
			fmt.Fprint(w, "\r"+pad+"\r\x1b[2K")
		})
	}
}
