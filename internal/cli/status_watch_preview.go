package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/x-mesh/gk/internal/diff"
	"github.com/x-mesh/gk/internal/git"
)

// --- Diff preview pane ([d] in the change feed) -------------------------------
//
// The feed names the file and the function that changed; the pane shows the
// code. It holds the uncommitted diff of ONE file against HEAD: the file the
// user pinned, or the one the newest feed event names. One `git diff` runs for
// that file, and only when the pane is open and the file, its signature, or
// HEAD moved — the feed's per-poll cost does not change.

const (
	// changePreviewSideMinWidth is the terminal width from which the pane sits
	// beside the feed. Under it a feed row and a diff row do not both fit, so
	// the pane goes below the feed.
	changePreviewSideMinWidth = 140
	// changePreviewMinRows is the body height (feed rows and pane rows
	// together) the pane needs. Under it the feed keeps the screen.
	changePreviewMinRows = 8
	// changePreviewMaxRows bounds the rendered diff: a generated file or a
	// lockfile can change by tens of thousands of lines, and every re-touch
	// renders the diff again.
	changePreviewMaxRows  = 2000
	changePreviewTabWidth = 4
)

// changePreview is the diff of one file as the pane shows it.
type changePreview struct {
	path string
	// sig and head are what the diff was read at. The pane is stale when the
	// file's signature or HEAD is another one: HEAD is the base of the diff,
	// so a branch switch changes the diff of a file it leaves untouched.
	sig  fileSig
	head string
	// rows are the rendered diff rows, not yet cut to the pane width.
	rows []string
	// hunkRows is the row of each hunk header and hunkKeys the changed lines
	// of each hunk, to find the hunk that a later read changed.
	hunkRows []int
	hunkKeys []string
	added    int
	removed  int
	// note is shown in place of rows: a binary file, an untracked directory,
	// a read that failed, or a file with no uncommitted change.
	note string
}

// changePreviewMsg carries one read of the pane's diff. gen is the producing
// model's generation, as in changeFrameMsg.
type changePreviewMsg struct {
	gen     int
	preview changePreview
}

// loadChangePreview reads the uncommitted diff of path against HEAD. An
// untracked file is not in `git diff`, so its content is read and shown as
// added lines.
func loadChangePreview(ctx context.Context, runner git.Runner, root, path string, sig fileSig, head string) changePreview {
	p := changePreview{path: path, sig: sig, head: head}
	if sig.xy == "??" {
		f, note := untrackedPreviewFile(root, path)
		if f == nil {
			p.note = note
			return p
		}
		p.render(f)
		return p
	}
	// --literal-pathspecs: a path that starts with ':' is a file name here, not
	// pathspec magic. --no-color and --no-ext-diff keep the output parseable
	// whatever color.diff and diff.external say.
	out, stderr, err := runner.Run(ctx, "--no-optional-locks", "--literal-pathspecs",
		"diff", "--no-color", "--no-ext-diff", "HEAD", "--", path)
	if err != nil {
		reason := firstLine(string(stderr))
		if reason == "" {
			reason = err.Error()
		}
		p.note = "git diff failed: " + stripControlChars(reason)
		return p
	}
	res, perr := diff.ParseUnifiedDiff(bytes.NewReader(out))
	if perr != nil {
		p.note = "diff not readable: " + perr.Error()
		return p
	}
	if len(res.Files) == 0 {
		p.note = "no uncommitted change"
		return p
	}
	f := res.Files[0]
	if f.IsBinary {
		p.note = "binary file"
		return p
	}
	if len(f.Hunks) == 0 {
		p.note = "no line changed"
		if f.OldMode != f.NewMode && f.OldMode != "" && f.NewMode != "" {
			p.note = fmt.Sprintf("mode %s → %s, no line changed", f.OldMode, f.NewMode)
		}
		return p
	}
	p.render(&f)
	return p
}

// untrackedPreviewFile builds a diff of an untracked file: every line added.
// It returns a note and no file for what the pane cannot show.
func untrackedPreviewFile(root, rel string) (*diff.DiffFile, string) {
	full := filepath.Join(root, rel)
	fi, err := os.Stat(full)
	switch {
	case err != nil:
		return nil, "file not readable: " + err.Error()
	case fi.IsDir():
		return nil, "untracked directory"
	case fi.Size() > untrackedProfileMaxBytes:
		return nil, fmt.Sprintf("untracked file too large to show (%d KB)", fi.Size()/1024)
	}
	data, err := os.ReadFile(full)
	switch {
	case err != nil:
		return nil, "file not readable: " + err.Error()
	case len(data) == 0:
		return nil, "empty file"
	case bytes.IndexByte(data, 0) >= 0:
		return nil, "binary file"
	}
	text := strings.TrimSuffix(string(data), "\n")
	lines := strings.Split(text, "\n")
	h := diff.Hunk{NewStart: 1, NewCount: len(lines), Header: fmt.Sprintf("@@ -0,0 +1,%d @@", len(lines))}
	for i, l := range lines {
		h.Lines = append(h.Lines, diff.DiffLine{Kind: diff.LineAdded, Content: l, NewNum: i + 1})
	}
	return &diff.DiffFile{NewPath: rel, Status: diff.StatusAdded, AddedLines: len(lines), Hunks: []diff.Hunk{h}}, ""
}

// render fills the rows from one file's hunks, up to changePreviewMaxRows.
func (p *changePreview) render(f *diff.DiffFile) {
	p.added, p.removed = f.AddedLines, f.DeletedLines
	header := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	left := 0
	for i, h := range f.Hunks {
		if len(p.rows) >= changePreviewMaxRows {
			left += len(h.Lines)
			continue
		}
		if i > 0 {
			p.rows = append(p.rows, "")
		}
		p.hunkRows = append(p.hunkRows, len(p.rows))
		p.hunkKeys = append(p.hunkKeys, hunkChangeKey(h))
		p.rows = append(p.rows, header.Render(previewText(h.Header)))
		for _, l := range h.Lines {
			if len(p.rows) >= changePreviewMaxRows {
				left++
				continue
			}
			l.Content = previewText(l.Content)
			p.rows = append(p.rows, diff.RenderLine(l, false))
		}
	}
	if left > 0 {
		p.rows = append(p.rows, lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf("… %d more lines not shown", left)))
	}
}

// previewText makes one line of file content safe for a pane cell. A tab has
// no width of its own and a carriage return moves the cursor back over the
// feed column, so tabs become spaces up to the next tab stop and the other
// control characters, ESC among them, are dropped.
func previewText(s string) string {
	s = stripControlChars(s)
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := changePreviewTabWidth - col%changePreviewTabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += runewidth.RuneWidth(r)
	}
	return b.String()
}

// hunkChangeKey is the added and deleted lines of a hunk. Line numbers are
// left out: an edit above a hunk moves it and does not change it.
func hunkChangeKey(h diff.Hunk) string {
	var b strings.Builder
	for _, l := range h.Lines {
		switch l.Kind {
		case diff.LineAdded:
			b.WriteByte('+')
		case diff.LineDeleted:
			b.WriteByte('-')
		default:
			continue
		}
		b.WriteString(l.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// changedHunkRow returns the row of the first hunk of next that prev does not
// have: where the edit between the two reads is.
func changedHunkRow(prev, next changePreview) (int, bool) {
	had := make(map[string]bool, len(prev.hunkKeys))
	for _, k := range prev.hunkKeys {
		had[k] = true
	}
	for i, k := range next.hunkKeys {
		if !had[k] {
			return next.hunkRows[i], true
		}
	}
	return 0, false
}

// previewTarget is the file the pane shows: the pinned file, or the file of
// the newest feed event that still has a change. The events of one refresh
// share a timestamp and are sorted by path, so among them the file written
// last wins, and the lowest feed row on a tie. With no such event, as after
// [c], the file on show stays while it has a change.
func (m *changeWatchModel) previewTarget() string {
	if m.previewPin != "" {
		return m.previewPin
	}
	best := ""
	var bestMtime int64
	var batch time.Time
	for i := len(m.events) - 1; i >= 0; i-- {
		e := m.events[i]
		if best != "" && !e.ts.Equal(batch) {
			break
		}
		if e.cleared {
			continue
		}
		sig, dirty := m.prev[e.path]
		if !dirty {
			continue
		}
		if best == "" || sig.mtime > bestMtime {
			best, bestMtime, batch = e.path, sig.mtime, e.ts
		}
	}
	if best == "" {
		if _, dirty := m.prev[m.preview.path]; dirty {
			return m.preview.path
		}
	}
	return best
}

// previewCmd reads the diff of the pane's file when the pane is open and what
// it shows is stale. One read runs at a time; the result asks again, so a
// change during a read is not lost.
func (m *changeWatchModel) previewCmd() tea.Cmd {
	if !m.previewOn || m.previewLoading {
		return nil
	}
	target := m.previewTarget()
	if target == "" {
		m.preview = changePreview{}
		m.previewScroll = 0
		return nil
	}
	sig := m.prev[target]
	if m.preview.path == target && m.preview.sig == sig && m.preview.head == m.head.sha {
		return nil
	}
	m.previewLoading = true
	runner, root, head, gen := m.runner, m.root, m.head.sha, m.gen
	ctx := m.cmd.Context()
	return func() tea.Msg {
		return changePreviewMsg{gen: gen, preview: loadChangePreview(ctx, runner, root, target, sig, head)}
	}
}

// applyPreview takes one read. A read of a file the pane no longer wants is
// dropped. While the pane follows the feed, a new read of the same file
// scrolls to the hunk that changed; a pinned pane keeps the rows being read.
func (m *changeWatchModel) applyPreview(msg changePreviewMsg) tea.Cmd {
	m.previewLoading = false
	if !m.previewOn {
		return nil
	}
	if msg.preview.path == m.previewTarget() {
		old := m.preview
		m.preview = msg.preview
		switch {
		case old.path != m.preview.path:
			m.previewScroll = 0
		case m.previewPin == "":
			if row, ok := changedHunkRow(old, m.preview); ok {
				m.previewScroll = row
			}
		}
	}
	return m.previewCmd()
}

// handlePreviewKey handles the pane's keys. ok is false for a key the pane
// does not own, which is every key but [d] while the pane is closed.
func (m *changeWatchModel) handlePreviewKey(key string) (cmd tea.Cmd, ok bool) {
	if key == "d" {
		m.previewOn = !m.previewOn
		if !m.previewOn {
			m.preview = changePreview{}
			m.previewScroll = 0
			return nil, true
		}
		return m.previewCmd(), true
	}
	if !m.previewOn {
		return nil, false
	}
	switch key {
	case "f":
		if m.previewPin != "" {
			m.previewPin = ""
		} else {
			m.previewPin = m.previewTarget()
		}
		return m.previewCmd(), true
	case "tab":
		m.stepPreviewFile(1)
		return m.previewCmd(), true
	case "shift+tab":
		m.stepPreviewFile(-1)
		return m.previewCmd(), true
	case "ctrl+d":
		m.scrollPreview(1)
		return nil, true
	case "ctrl+u":
		m.scrollPreview(-1)
		return nil, true
	}
	return nil, false
}

// stepPreviewFile pins the next (+1) or the previous (-1) changed file, in
// path order and around the ends.
func (m *changeWatchModel) stepPreviewFile(dir int) {
	if len(m.prev) == 0 {
		return
	}
	paths := make([]string, 0, len(m.prev))
	for p := range m.prev {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	at := sort.SearchStrings(paths, m.previewTarget())
	if at == len(paths) || paths[at] != m.previewTarget() {
		// The file on show has no change now: start from the end the step
		// moves away from.
		at = -1
		if dir < 0 {
			at = len(paths)
		}
	}
	m.previewPin = paths[(at+dir+len(paths))%len(paths)]
}

// scrollPreview moves the diff by half a pane (dir -1 or +1).
func (m *changeWatchModel) scrollPreview(dir int) {
	body := m.previewLayout().paneRows - 1
	step := body / 2
	if step < 1 {
		step = 1
	}
	m.previewScroll = clampPreviewScroll(m.previewScroll+dir*step, len(m.preview.rows), body)
}

func clampPreviewScroll(scroll, rows, body int) int {
	if last := rows - body; scroll > last {
		scroll = last
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}

// changePreviewLayout is where the feed and the pane go. paneRows counts the
// pane's title rule and is zero when the pane does not show.
type changePreviewLayout struct {
	side      bool
	feedWidth int
	feedRows  int
	paneWidth int
	paneRows  int
}

// previewLayout puts the pane beside the feed on a wide terminal and below it
// on a narrow one. Below, the feed takes the rows its events need, up to two
// fifths of the body, and the pane takes the rest.
func (m *changeWatchModel) previewLayout() changePreviewLayout {
	body := m.bodyBudget()
	l := changePreviewLayout{feedWidth: m.width, feedRows: body}
	if !m.previewOn || m.width <= 0 || body < changePreviewMinRows {
		return l
	}
	if m.width >= changePreviewSideMinWidth {
		l.side = true
		l.feedWidth = m.width * 45 / 100
		if l.feedWidth > 80 {
			l.feedWidth = 80
		}
		l.paneWidth = m.width - l.feedWidth - lipgloss.Width(changePreviewGutter)
		l.paneRows = body + 1 // the title rule is on the row of the feed divider
		return l
	}
	l.paneWidth = m.width
	l.feedRows = body * 2 / 5
	if n := len(m.events); n < l.feedRows {
		l.feedRows = n
	}
	if l.feedRows < 1 {
		l.feedRows = 1
	}
	l.paneRows = body - l.feedRows
	return l
}

const changePreviewGutter = " │ "

// joinPreview puts the pane beside or below the feed block (the divider and
// the feed rows).
func (m *changeWatchModel) joinPreview(feed string, l changePreviewLayout) string {
	pane := m.previewPane(l.paneWidth, l.paneRows)
	if !l.side {
		return feed + "\n" + strings.Join(pane, "\n")
	}
	left := strings.Split(feed, "\n")
	gutter := lipgloss.NewStyle().Faint(true).Render(changePreviewGutter)
	rows := make([]string, len(pane))
	for i := range pane {
		cell := ""
		if i < len(left) {
			cell = left[i]
		}
		rows[i] = fitCell(cell, l.feedWidth) + gutter + pane[i]
	}
	return strings.Join(rows, "\n")
}

// fitCell cuts s to w columns and pads it to w, so the cell beside it starts
// at the same column on every row.
func fitCell(s string, w int) string {
	s = cutCell(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func cutCell(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// previewPane renders the pane as exactly rows rows of at most w columns: the
// title rule, then the diff rows from the scroll position.
func (m *changeWatchModel) previewPane(w, rows int) []string {
	body := rows - 1
	m.previewScroll = clampPreviewScroll(m.previewScroll, len(m.preview.rows), body)
	out := make([]string, 0, rows)
	out = append(out, m.previewTitle(w, body))

	dim := lipgloss.NewStyle().Faint(true)
	p := m.preview
	switch {
	case p.path == "" && m.previewLoading:
		out = append(out, cutCell("   "+dim.Render("loading…"), w))
	case p.path == "":
		out = append(out, cutCell("   "+dim.Render("no changed file to show"), w))
	case p.note != "":
		out = append(out, cutCell("   "+dim.Render(p.note), w))
	default:
		end := m.previewScroll + body
		if end > len(p.rows) {
			end = len(p.rows)
		}
		for _, row := range p.rows[m.previewScroll:end] {
			out = append(out, cutCell(row, w))
		}
	}
	for len(out) < rows {
		out = append(out, "")
	}
	return out[:rows]
}

// previewTitle is the pane's rule: the file, its +/- counts, whether the pane
// follows the feed or is pinned, and the rows on show when the diff is longer
// than the pane.
func (m *changeWatchModel) previewTitle(w, body int) string {
	dim := lipgloss.NewStyle().Faint(true)
	bold := lipgloss.NewStyle().Bold(true)
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))

	p := m.preview
	name := p.path
	if name == "" {
		name = m.previewTarget()
	}
	name = stripControlChars(name)

	const label = "─── diff "
	tail := "following"
	if m.previewPin != "" {
		tail = "pinned"
	}
	if n := len(p.rows); n > body && p.note == "" {
		last := m.previewScroll + body
		if last > n {
			last = n
		}
		tail += fmt.Sprintf(" · %d-%d/%d", m.previewScroll+1, last, n)
	}
	stat, statW := "", 0
	if p.added > 0 {
		s := fmt.Sprintf(" +%d", p.added)
		stat, statW = stat+green.Render(s), statW+runewidth.StringWidth(s)
	}
	if p.removed > 0 {
		s := fmt.Sprintf(" −%d", p.removed)
		stat, statW = stat+red.Render(s), statW+runewidth.StringWidth(s)
	}

	// label + name + stat + " " + fill + " " + tail
	room := w - runewidth.StringWidth(label) - statW - runewidth.StringWidth(tail) - 2
	name = cutPathLeft(name, room-3)
	fill := room - runewidth.StringWidth(name)
	if fill < 0 {
		fill = 0
	}
	return cutCell(dim.Render(label)+bold.Render(name)+stat+" "+dim.Render(strings.Repeat("─", fill)+" "+tail), w)
}

// cutPathLeft cuts a path to w columns from the left: the file name at the
// end says more than the directories at the start.
func cutPathLeft(p string, w int) string {
	if runewidth.StringWidth(p) <= w {
		return p
	}
	if w < 2 {
		return ""
	}
	r := []rune(p)
	for len(r) > 0 && runewidth.StringWidth(string(r))+1 > w {
		r = r[1:]
	}
	return "…" + string(r)
}
