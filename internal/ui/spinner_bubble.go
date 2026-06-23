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
	// start and budget drive the live "elapsed / budget" timer. budget == 0
	// means no timer (the plain spinner): the elapsed clock is shown only when
	// the caller hands over a deadline to count down against.
	start  time.Time
	budget time.Duration
}

func (m bubbleSpinnerModel) Init() tea.Cmd { return m.sp.Tick }

func (m bubbleSpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.sp, cmd = m.sp.Update(msg)
	return m, cmd
}

func (m bubbleSpinnerModel) View() string {
	s := m.sp.View() + " " + m.msg
	if m.budget > 0 {
		s += " · " + renderBudgetTimer(time.Since(m.start), m.budget)
	}
	return s
}

// renderBudgetTimer formats the live "elapsed / budget" countdown and colours it
// by how close the elapsed time is to the deadline: dim normally, amber past 80%,
// red past 95% so an imminent timeout is visible before it fires.
func renderBudgetTimer(elapsed, budget time.Duration) string {
	text := fmt.Sprintf("%ds / %ds", int(elapsed.Seconds()), int(budget.Seconds()))
	return lipgloss.NewStyle().Foreground(budgetTimerColor(elapsed, budget)).Render(text)
}

// budgetTimerColor maps how far elapsed has eaten into budget onto the timer
// colour: dim normally, amber past 80%, red past 95% (about to time out).
func budgetTimerColor(elapsed, budget time.Duration) lipgloss.Color {
	if budget <= 0 {
		return lipgloss.Color("241")
	}
	switch ratio := float64(elapsed) / float64(budget); {
	case ratio >= 0.95:
		return lipgloss.Color("203") // red — about to time out
	case ratio >= 0.80:
		return lipgloss.Color("214") // amber — getting close
	default:
		return lipgloss.Color("241") // dim
	}
}

// StartBubbleSpinner draws a bubbletea-driven inline spinner on stderr
// until stop() is called. Non-TTY stderr (pipes, CI, `2>file`) makes it
// a no-op so piped output streams stay clean. The first frame is delayed
// by SpinnerStartDelay so sub-150ms ops never paint anything to clear.
//
// Same idempotent-safe contract as StartSpinner: pair with defer for
// exception-safe cleanup.
func StartBubbleSpinner(msg string) (stop func()) {
	return startBubbleSpinnerTo(os.Stderr, msg, 0)
}

// StartBubbleSpinnerWithBudget is StartBubbleSpinner plus a live "elapsed /
// budget" countdown that colours amber then red as it nears the deadline — for
// a provider call bounded by a known timeout, so the user sees how close it is
// to timing out rather than waiting blind. A zero budget degrades to the plain
// spinner.
func StartBubbleSpinnerWithBudget(msg string, budget time.Duration) (stop func()) {
	return startBubbleSpinnerTo(os.Stderr, msg, budget)
}

func startBubbleSpinnerTo(w io.Writer, msg string, budget time.Duration) (stop func()) {
	if w == os.Stderr && !IsStderrTerminal() {
		return func() {}
	}

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	prog := tea.NewProgram(
		bubbleSpinnerModel{sp: sp, msg: msg, start: time.Now(), budget: budget},
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
			padN := len(msg) + 4
			if budget > 0 {
				padN += 18 // room for the " · NNNs / NNNs" timer tail
			}
			fmt.Fprint(w, "\r"+strings.Repeat(" ", padN)+"\r\x1b[2K")
		})
	}
}
