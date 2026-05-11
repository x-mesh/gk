package cli

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestClampWatchInterval(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{0, watchMinInterval},
		{50 * time.Millisecond, watchMinInterval},
		{watchMinInterval, watchMinInterval},
		{2 * time.Second, 2 * time.Second},
		{watchMaxInterval, watchMaxInterval},
		{2 * watchMaxInterval, watchMaxInterval},
	}
	for _, tc := range cases {
		if got := clampWatchInterval(tc.in); got != tc.want {
			t.Errorf("clampWatchInterval(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWatchModelKeyboard(t *testing.T) {
	m := &watchModel{interval: 2 * time.Second}

	// `+` doubles
	mUpdated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	if got := mUpdated.(*watchModel).interval; got != 4*time.Second {
		t.Errorf("+ key: interval = %v, want 4s", got)
	}

	// `-` halves
	mUpdated, _ = mUpdated.(*watchModel).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'-'}})
	if got := mUpdated.(*watchModel).interval; got != 2*time.Second {
		t.Errorf("- key: interval = %v, want 2s", got)
	}

	// `p` toggles paused
	mUpdated, _ = mUpdated.(*watchModel).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !mUpdated.(*watchModel).paused {
		t.Error("p key: paused should be true")
	}
	mUpdated, _ = mUpdated.(*watchModel).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if mUpdated.(*watchModel).paused {
		t.Error("p key (toggle back): paused should be false")
	}

	// `q` returns the quit cmd
	_, cmd := mUpdated.(*watchModel).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q key: expected tea.Quit cmd, got nil")
	}
	// tea.Quit is a func that returns tea.QuitMsg{}; invoke and assert
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q key: cmd did not return QuitMsg")
	}
}

func TestWatchModelClampOnRepeatedDecrease(t *testing.T) {
	m := &watchModel{interval: 1 * time.Second}
	for i := 0; i < 10; i++ {
		out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'-'}})
		m = out.(*watchModel)
	}
	if m.interval != watchMinInterval {
		t.Errorf("repeated -: interval = %v, want floor %v", m.interval, watchMinInterval)
	}
}

func TestWatchModelFrameMsgSuppressed(t *testing.T) {
	m := &watchModel{interval: 2 * time.Second, refreshing: true}

	// First frame establishes baseline; not a "change".
	t0 := time.Date(2026, 5, 11, 14, 0, 0, 0, time.UTC)
	first := watchFrameMsg{text: "hello", ts: t0, hash: 42}
	out, cmd := m.Update(first)
	m = out.(*watchModel)
	if m.frame != "hello" {
		t.Fatalf("frame = %q, want %q", m.frame, "hello")
	}
	if m.suppressed {
		t.Error("first frame should not be suppressed")
	}
	if !m.lastChange.IsZero() {
		t.Error("first frame must not set lastChange")
	}
	if cmd != nil {
		t.Error("first frame must not schedule a pulse-end tick")
	}

	// Identical hash → keep prior frame, mark suppressed, no change ts.
	m.refreshing = true
	second := watchFrameMsg{text: "world", ts: t0.Add(time.Second), hash: 42}
	out, cmd = m.Update(second)
	m = out.(*watchModel)
	if m.frame != "hello" {
		t.Errorf("suppressed update should keep old frame, got %q", m.frame)
	}
	if !m.suppressed {
		t.Error("identical hash should set suppressed=true")
	}
	if !m.lastChange.IsZero() {
		t.Error("identical hash must not advance lastChange")
	}
	if cmd != nil {
		t.Error("identical hash must not schedule a pulse-end tick")
	}

	// Different hash → adopt new frame, set lastChange, schedule pulse end.
	m.refreshing = true
	t2 := t0.Add(2 * time.Second)
	third := watchFrameMsg{text: "world", ts: t2, hash: 99}
	out, cmd = m.Update(third)
	m = out.(*watchModel)
	if m.frame != "world" {
		t.Errorf("changed frame should be adopted, got %q", m.frame)
	}
	if m.suppressed {
		t.Error("changed frame should clear suppressed")
	}
	if !m.lastChange.Equal(t2) {
		t.Errorf("lastChange = %v, want %v", m.lastChange, t2)
	}
	if cmd == nil {
		t.Error("changed frame must schedule pulse-end tick")
	}
}

func TestWatchModelTickRespectsPause(t *testing.T) {
	m := &watchModel{interval: 250 * time.Millisecond, paused: true}
	_, cmd := m.Update(watchTickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("paused tick should still arm the next tick")
	}
	if m.refreshing {
		t.Error("paused tick must not trigger a refresh")
	}
}

func TestTruncateForHeader(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"abcdef", 6, "abcdef"},
		{"abcdefg", 6, "abcde…"},
		{"x", 1, "x"},
		{"xy", 1, "…"},
	}
	for _, tc := range cases {
		if got := truncateForHeader(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateForHeader(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestWatchHeaderLineContent(t *testing.T) {
	m := &watchModel{interval: 2 * time.Second, lastUpdate: time.Date(2026, 5, 11, 14, 23, 1, 0, time.UTC)}
	line := watchHeaderLine(m)
	for _, want := range []string{"gk watch", "every 2s", "last 14:23:01", "[q] quit"} {
		if !strings.Contains(line, want) {
			t.Errorf("header missing %q\nline: %s", want, line)
		}
	}

	m.paused = true
	line = watchHeaderLine(m)
	if !strings.Contains(line, "paused") {
		t.Errorf("paused header missing 'paused' marker\nline: %s", line)
	}

	m.paused = false
	m.suppressed = true
	line = watchHeaderLine(m)
	if !strings.Contains(line, "no change") {
		t.Errorf("suppressed header missing 'no change'\nline: %s", line)
	}
}

func TestWatchHeaderPulseAndCalmStates(t *testing.T) {
	change := time.Date(2026, 5, 11, 14, 22, 58, 0, time.UTC)
	base := &watchModel{
		interval:   2 * time.Second,
		lastUpdate: change.Add(time.Second),
		lastChange: change,
	}

	// Pulse window: now is 500ms after change → "● just changed".
	pulseM := *base
	pulseM.now = func() time.Time { return change.Add(500 * time.Millisecond) }
	pulseLine := watchHeaderLine(&pulseM)
	if !strings.Contains(pulseLine, "just changed") {
		t.Errorf("pulse header missing 'just changed'\nline: %s", pulseLine)
	}
	if strings.Contains(pulseLine, "changed 14:22:58") {
		t.Errorf("pulse header should not show calm timestamp yet\nline: %s", pulseLine)
	}

	// Calm window: now is well past pulse end → "changed HH:MM:SS".
	calmM := *base
	calmM.now = func() time.Time { return change.Add(5 * time.Second) }
	calmLine := watchHeaderLine(&calmM)
	if strings.Contains(calmLine, "just changed") {
		t.Errorf("calm header should not show pulse\nline: %s", calmLine)
	}
	if !strings.Contains(calmLine, "changed 14:22:58") {
		t.Errorf("calm header missing 'changed 14:22:58'\nline: %s", calmLine)
	}

	// Pre-change: lastChange zero → no cue at all.
	preM := *base
	preM.lastChange = time.Time{}
	preLine := watchHeaderLine(&preM)
	if strings.Contains(preLine, "just changed") || strings.Contains(preLine, "changed ") {
		t.Errorf("pre-change header should omit change cue\nline: %s", preLine)
	}
}

func TestWatchPulseEndMsgIsNoOp(t *testing.T) {
	m := &watchModel{interval: 2 * time.Second, frame: "keep"}
	out, cmd := m.Update(watchPulseEndMsg{})
	got := out.(*watchModel)
	if cmd != nil {
		t.Error("pulse-end must not chain commands")
	}
	if got.frame != "keep" {
		t.Errorf("pulse-end must not mutate frame, got %q", got.frame)
	}
}

func TestMarkChangedLinesAddedAndRemoved(t *testing.T) {
	prev := "branch: main\n  modified file.go\n"
	curr := "branch: main\n  modified file.go\n  new added.txt\n"
	marked := markChangedLines(prev, curr)
	want := []bool{false, false, true, false} // last empty after final \n
	if len(marked) != len(want) {
		t.Fatalf("len=%d, want %d", len(marked), len(want))
	}
	for i := range want {
		if marked[i] != want[i] {
			t.Errorf("line %d: marked=%v, want %v", i, marked[i], want[i])
		}
	}
}

func TestMarkChangedLinesFirstFrame(t *testing.T) {
	curr := "branch: main\n  modified file.go\n"
	marked := markChangedLines("", curr)
	for i, m := range marked {
		if m {
			t.Errorf("first frame line %d should not be marked", i)
		}
	}
}

func TestMarkChangedLinesMultisetSemantics(t *testing.T) {
	// Two identical "modified" lines exist. If prev had only one, the
	// second occurrence in curr must be marked, not both.
	prev := "  modified\n"
	curr := "  modified\n  modified\n"
	marked := markChangedLines(prev, curr)
	if marked[0] {
		t.Error("first occurrence should not be marked (matched prev)")
	}
	if !marked[1] {
		t.Error("second occurrence should be marked (no prev match left)")
	}
}

func TestDecorateFrameGutterStableAcrossPulse(t *testing.T) {
	frame := "█ HEADER\n  detail one\n  detail two"
	marked := []bool{false, true, false}
	pulse := decorateFrame(frame, marked, true)
	calm := decorateFrame(frame, marked, false)
	pulseLines := strings.Split(pulse, "\n")
	calmLines := strings.Split(calm, "\n")
	if len(pulseLines) != len(calmLines) {
		t.Fatalf("line count differs: pulse=%d calm=%d", len(pulseLines), len(calmLines))
	}
	// All lines in calm must start with two spaces (no marker, no shift).
	for i, ln := range calmLines {
		if !strings.HasPrefix(ln, "  ") {
			t.Errorf("calm line %d missing 2-space gutter: %q", i, ln)
		}
	}
	// Pulse line at index 1 must contain a ▎ marker (cyan-styled).
	if !strings.Contains(pulseLines[1], "▎") {
		t.Errorf("pulse line 1 missing ▎ marker: %q", pulseLines[1])
	}
	// Other pulse lines must keep the plain 2-space gutter.
	if !strings.HasPrefix(pulseLines[0], "  ") {
		t.Errorf("pulse line 0 should keep plain gutter: %q", pulseLines[0])
	}
}

func TestDecorateFrameSkipsBlankMarks(t *testing.T) {
	// A blank line that's flagged should not get a marker — pure
	// whitespace pulses are visual noise.
	frame := "\n  detail"
	marked := []bool{true, true}
	out := decorateFrame(frame, marked, true)
	lines := strings.Split(out, "\n")
	if strings.Contains(lines[0], "▎") {
		t.Errorf("blank line should not be marked: %q", lines[0])
	}
	if !strings.Contains(lines[1], "▎") {
		t.Errorf("non-blank line should be marked: %q", lines[1])
	}
}

func TestHashFrameIgnoresANSIAndTrailingSpaces(t *testing.T) {
	cases := []struct {
		name   string
		a, b   string
		wantEq bool
	}{
		{
			name:   "identical plaintext",
			a:      "branch: main\n  modified file.go",
			b:      "branch: main\n  modified file.go",
			wantEq: true,
		},
		{
			name:   "ansi colour added",
			a:      "branch: main",
			b:      "branch: \x1b[32mmain\x1b[0m",
			wantEq: true,
		},
		{
			name:   "trailing spaces vary",
			a:      "branch: main   \n  modified",
			b:      "branch: main\n  modified  ",
			wantEq: true,
		},
		{
			name:   "real text change",
			a:      "branch: main",
			b:      "branch: develop",
			wantEq: false,
		},
		{
			name:   "ansi noise but real change",
			a:      "\x1b[1mfoo\x1b[0m",
			b:      "\x1b[1mbar\x1b[0m",
			wantEq: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ha, hb := hashFrame(tc.a), hashFrame(tc.b)
			if tc.wantEq && ha != hb {
				t.Errorf("expected equal hashes, got %x vs %x", ha, hb)
			}
			if !tc.wantEq && ha == hb {
				t.Errorf("expected different hashes, both %x", ha)
			}
		})
	}
}
