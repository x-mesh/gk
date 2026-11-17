package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/ui"
)

// Auto-refresh period. One refresh costs up to 7 GraphQL points, so 30 s is
// at most 840 points an hour of the 5000 a token has. The search index lags
// behind GitHub, so a shorter period would rarely show newer data.
const (
	dashMinInterval     = 30 * time.Second
	dashDefaultInterval = dashMinInterval
)

type dashResultMsg struct {
	seq  int
	snap dashSnapshot
	err  error
}

// dashTickMsg asks for an auto-refresh. gen is the watch generation it was
// scheduled under: w and r bump the generation, so a tick from before is
// dropped instead of adding a second refresh loop.
type dashTickMsg struct{ gen int }

type dashClockMsg struct{}

type dashTUI struct {
	ctx      context.Context
	cancel   context.CancelFunc
	fetch    func(context.Context) (dashSnapshot, error)
	now      func() time.Time
	interval time.Duration

	watch     bool
	gen       int
	scheduled bool      // a tick of the current generation is pending
	nextAt    time.Time // when that tick fires
	inFlight  bool
	seq       int
	lastFetch time.Time // when the last read finished

	snap       *dashSnapshot
	lastOK     time.Time
	err        error
	limitUntil time.Time

	vp       viewport.Model
	width    int
	sections []int // content lines that start a section, for tab
}

func newDashTUI(ctx context.Context, fetch func(context.Context) (dashSnapshot, error), interval time.Duration, watch bool) *dashTUI {
	ctx, cancel := context.WithCancel(ctx)
	return &dashTUI{ctx: ctx, cancel: cancel, fetch: fetch, now: time.Now, interval: interval, watch: watch, vp: viewport.New(80, 20), width: 80}
}

func (m *dashTUI) Init() tea.Cmd {
	return tea.Batch(m.startFetch(), dashClock())
}

func dashClock() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return dashClockMsg{} })
}

// startFetch starts one read. Callers have checked that none is in flight.
func (m *dashTUI) startFetch() tea.Cmd {
	m.seq++
	m.inFlight = true
	seq, ctx, fetch := m.seq, m.ctx, m.fetch
	return func() tea.Msg {
		snap, err := fetch(ctx)
		return dashResultMsg{seq: seq, snap: snap, err: err}
	}
}

// schedule arms one tick of the current generation after d, or at the end of
// a rate limit when that is later.
func (m *dashTUI) schedule(d time.Duration) tea.Cmd {
	at := m.now().Add(d)
	if m.limitUntil.After(at) {
		at = m.limitUntil
	}
	m.scheduled, m.nextAt = true, at
	gen := m.gen
	return tea.Tick(at.Sub(m.now()), func(time.Time) tea.Msg { return dashTickMsg{gen: gen} })
}

func (m *dashTUI) limited() bool { return m.now().Before(m.limitUntil) }

func (m *dashTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case dashResultMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.inFlight = false
		m.lastFetch = m.now()
		if msg.err != nil {
			// Keep the last snapshot; the header says what failed.
			m.err = msg.err
			var rl *ghapi.RateLimitError
			if errors.As(msg.err, &rl) {
				m.limitUntil = rl.Reset
			}
		} else {
			m.snap, m.lastOK, m.err, m.limitUntil = &msg.snap, m.now(), nil, time.Time{}
			m.render()
		}
		if m.watch && !m.scheduled {
			return m, m.schedule(m.interval)
		}
		return m, nil

	case dashTickMsg:
		if msg.gen != m.gen || !m.watch {
			return m, nil
		}
		m.scheduled = false
		if m.inFlight {
			return m, nil
		}
		if m.limited() {
			return m, m.schedule(0)
		}
		return m, m.startFetch()

	case dashClockMsg:
		return m, dashClock()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.vp.Width, m.vp.Height = msg.Width, max(msg.Height-2, 1)
		m.render()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		case "w":
			return m, m.toggleWatch()
		case "r":
			if m.inFlight || m.limited() {
				return m, nil
			}
			// The result schedules the next tick, so drop the pending one.
			m.gen++
			m.scheduled = false
			return m, m.startFetch()
		case "tab":
			m.jumpSection(1)
			return m, nil
		case "shift+tab":
			m.jumpSection(-1)
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

// toggleWatch turns auto-refresh on or off. Turning it on reads at once when
// the last read is older than the interval, else waits for the rest of it.
func (m *dashTUI) toggleWatch() tea.Cmd {
	m.watch = !m.watch
	m.gen++
	m.scheduled = false
	if !m.watch || m.inFlight {
		return nil
	}
	elapsed := m.now().Sub(m.lastFetch)
	if elapsed >= m.interval && !m.limited() {
		return m.startFetch()
	}
	return m.schedule(m.interval - elapsed)
}

func (m *dashTUI) render() {
	if m.snap == nil {
		return
	}
	var buf bytes.Buffer
	renderDash(&buf, *m.snap, m.width)
	content := strings.TrimRight(buf.String(), "\n")
	m.sections = m.sections[:0]
	for i, line := range strings.Split(content, "\n") {
		plain := ui.StripANSI(line)
		if strings.HasPrefix(plain, "PRs") || strings.HasPrefix(plain, "Issues") || strings.HasPrefix(plain, "Forecast") {
			m.sections = append(m.sections, i)
		}
	}
	m.vp.SetContent(content)
}

func (m *dashTUI) jumpSection(dir int) {
	if len(m.sections) == 0 {
		return
	}
	cur := m.vp.YOffset
	target := m.sections[0]
	if dir > 0 {
		for _, s := range m.sections {
			if s > cur {
				target = s
				break
			}
		}
	} else {
		target = m.sections[len(m.sections)-1]
		for i := len(m.sections) - 1; i >= 0; i-- {
			if m.sections[i] < cur {
				target = m.sections[i]
				break
			}
		}
	}
	m.vp.SetYOffset(target)
}

func (m *dashTUI) status() string {
	var watch string
	switch {
	case !m.watch:
		watch = cellFaint("○ auto-refresh off (w)")
	case m.scheduled:
		watch = cellGreen("●") + fmt.Sprintf(" auto-refresh on · next in %ds", max(int(m.nextAt.Sub(m.now()).Round(time.Second)/time.Second), 0))
	default:
		watch = cellGreen("●") + " auto-refresh on"
	}
	var state string
	switch {
	case m.inFlight:
		state = cellYellow("⟳ refreshing…")
	case m.limited():
		state = cellYellow("⏸ rate limited · retry at " + m.limitUntil.Local().Format("15:04:05"))
	case m.err != nil:
		state = cellRed("⚠ refresh failed: "+m.err.Error()) + cellFaint(" · last success "+dashClockTime(m.lastOK))
	}
	if state == "" {
		return watch
	}
	return watch + "  " + state
}

func dashClockTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("15:04:05")
}

func (m *dashTUI) View() string {
	body := cellFaint("loading…")
	if m.snap != nil {
		body = m.vp.View()
	}
	footer := cellFaint("w auto-refresh  r refresh  tab section  ↑↓ scroll  q quit")
	return m.status() + "\n" + body + "\n" + footer
}

func runDashTUI(ctx context.Context, env dashEnv, interval time.Duration, watch bool) error {
	m := newDashTUI(ctx, func(ctx context.Context) (dashSnapshot, error) { return collectDash(ctx, env) }, interval, watch)
	defer m.cancel()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
