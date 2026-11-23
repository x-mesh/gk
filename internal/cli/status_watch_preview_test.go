package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// previewRepo has a.go committed with two functions, then edited inside
// alpha. Go source is tab-indented, which is what the pane has to lay out.
func previewRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.go", "package a\n\nfunc alpha() int {\n\treturn 1\n}\n\nfunc beta() int {\n\treturn 2\n}\n")
	repo.Commit("init")
	repo.WriteFile("a.go", "package a\n\nfunc alpha() int {\n\treturn 10\n}\n\nfunc beta() int {\n\treturn 2\n}\n")
	return repo
}

func previewModel(t *testing.T, dir string, width, height int) *changeWatchModel {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	m := newChangeWatchModel(cmd, time.Second)
	m.runner = &git.ExecRunner{Dir: dir}
	m.root = dir
	m.width, m.height = width, height
	m.now = func() time.Time { return time.Unix(9000, 0) }
	return m
}

// drive runs cmd and every command its message leads to, as the program does.
func drive(m *changeWatchModel, cmd tea.Cmd) {
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
}

func joined(p changePreview) string { return strings.Join(p.rows, "\n") }

func TestLoadChangePreview(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	ctx := context.Background()
	load := func(repo *testutil.Repo, path, xy string) changePreview {
		return loadChangePreview(ctx, &git.ExecRunner{Dir: repo.Dir}, repo.Dir, path, fileSig{xy: xy}, "abc")
	}

	t.Run("a modified file shows its hunk, tabs laid out as spaces", func(t *testing.T) {
		p := load(previewRepo(t), "a.go", " M")
		if p.note != "" || p.added != 1 || p.removed != 1 || p.head != "abc" || len(p.hunkRows) != 1 {
			t.Fatalf("preview = %+v", p)
		}
		out := joined(p)
		for _, want := range []string{"@@", "alpha", "    return 10", "    return 1"} {
			if !strings.Contains(out, want) {
				t.Errorf("rows miss %q:\n%s", want, out)
			}
		}
		if strings.ContainsAny(out, "\t\r") {
			t.Errorf("rows hold a tab or a carriage return:\n%q", out)
		}
	})

	t.Run("a CRLF file leaves no carriage return in a row", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		repo.WriteFile("w.txt", "one\r\ntwo\r\n")
		repo.Commit("init")
		repo.WriteFile("w.txt", "one\r\nthree\r\n")
		p := load(repo, "w.txt", " M")
		if out := joined(p); !strings.Contains(out, "three") || strings.Contains(out, "\r") {
			t.Errorf("rows = %q", out)
		}
	})

	t.Run("an untracked file shows every line as added", func(t *testing.T) {
		repo := previewRepo(t)
		repo.WriteFile("new.go", "package a\n\nfunc gamma() {}\n")
		p := load(repo, "new.go", "??")
		if p.note != "" || p.added != 3 || len(p.rows) != 4 || !strings.Contains(joined(p), "func gamma() {}") {
			t.Errorf("preview = %+v", p)
		}
	})

	t.Run("what the pane cannot show is a note", func(t *testing.T) {
		repo := previewRepo(t)
		repo.WriteFile("dir/x.txt", "x\n")
		repo.WriteFile("blob.bin", "a\x00b")
		repo.WriteFile("empty.txt", "")
		for path, want := range map[string]string{"dir/": "untracked directory", "blob.bin": "binary file", "empty.txt": "empty file", "gone.txt": "not readable"} {
			if p := load(repo, path, "??"); !strings.Contains(p.note, want) || len(p.rows) != 0 {
				t.Errorf("%s: note %q, %d rows; want note %q", path, p.note, len(p.rows), want)
			}
		}
	})

	t.Run("a tracked binary file and a file with no change are notes", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		repo.WriteFile("blob.bin", "a\x00b")
		repo.WriteFile("same.txt", "same\n")
		repo.Commit("init")
		repo.WriteFile("blob.bin", "a\x00c")
		if p := load(repo, "blob.bin", " M"); p.note != "binary file" {
			t.Errorf("binary note = %q", p.note)
		}
		if p := load(repo, "same.txt", " M"); p.note != "no uncommitted change" {
			t.Errorf("unchanged note = %q", p.note)
		}
	})

	t.Run("a deleted file shows its lines as deleted", func(t *testing.T) {
		repo := previewRepo(t)
		if err := os.Remove(filepath.Join(repo.Dir, "a.go")); err != nil {
			t.Fatal(err)
		}
		p := load(repo, "a.go", " D")
		if p.note != "" || p.added != 0 || p.removed != 9 || !strings.Contains(joined(p), "func beta() int {") {
			t.Errorf("preview = %+v", p)
		}
	})

	t.Run("a file name that starts with a colon is a file, not pathspec magic", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		repo.WriteFile(":odd.txt", "one\n")
		repo.Commit("init")
		repo.WriteFile(":odd.txt", "two\n")
		if p := load(repo, ":odd.txt", " M"); p.note != "" || !strings.Contains(joined(p), "two") {
			t.Errorf("preview = %+v", p)
		}
	})

	t.Run("a diff past the row limit says how much it leaves out", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		repo.WriteFile("big.txt", "start\n")
		repo.Commit("init")
		var b strings.Builder
		for i := 0; i < changePreviewMaxRows+500; i++ {
			fmt.Fprintf(&b, "line %d\n", i)
		}
		repo.WriteFile("big.txt", b.String())
		p := load(repo, "big.txt", " M")
		if len(p.rows) != changePreviewMaxRows+1 || !strings.Contains(p.rows[len(p.rows)-1], "more lines not shown") {
			t.Errorf("%d rows, last %q", len(p.rows), p.rows[len(p.rows)-1])
		}
	})

	t.Run("a failed git diff is a note", func(t *testing.T) {
		dir := t.TempDir()
		p := loadChangePreview(ctx, &git.ExecRunner{Dir: dir}, dir, "a.go", fileSig{xy: " M"}, "")
		if !strings.Contains(p.note, "git diff failed") {
			t.Errorf("note = %q", p.note)
		}
	})
}

func TestPreviewText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":            "plain",
		"\treturn 1":       "    return 1",
		"ab\tc":            "ab  c",
		"한\t글":             "한  글", // a wide rune takes two columns
		"x\r":              "x",
		"\x1b[31mred\x07":  "[31mred",
		"a\tb\tc\r\x1b[0m": "a   b   c[0m",
		// U+009B is the one-rune form of ESC [ and starts an escape sequence.
		"a\u009b31mb\u007f": "a31mb",
		// A row keeps the first changePreviewMaxCols columns of a line.
		strings.Repeat("y", 3*changePreviewMaxCols): strings.Repeat("y", changePreviewMaxCols),
		strings.Repeat("한", changePreviewMaxCols):   strings.Repeat("한", changePreviewMaxCols/2),
		// A rune of two columns that starts at the last column does not fit:
		// the row ends before it, and takes nothing after it.
		strings.Repeat("y", changePreviewMaxCols-1) + "한z": strings.Repeat("y", changePreviewMaxCols-1),
		// A tab at the limit pads to the limit and no further.
		strings.Repeat("y", changePreviewMaxCols-2) + "\tz": strings.Repeat("y", changePreviewMaxCols-2) + "  ",
	} {
		if got := previewText(in); got != want {
			t.Errorf("previewText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCappedBuffer covers the bound on what the pane reads from git: the
// buffer keeps max bytes, and the write that goes over fails, which is what
// ends the git process.
func TestCappedBuffer(t *testing.T) {
	c := cappedBuffer{max: 10}
	if n, err := c.Write([]byte("123456")); n != 6 || err != nil || c.full {
		t.Fatalf("first write = %d, %v, full %v", n, err, c.full)
	}
	if n, err := c.Write([]byte("7890")); n != 4 || err != nil || c.full {
		t.Fatalf("a write that ends at the limit = %d, %v, full %v", n, err, c.full)
	}
	if n, err := c.Write([]byte("ab")); n != 0 || err == nil || !c.full {
		t.Errorf("a write over the limit = %d, %v, full %v", n, err, c.full)
	}
	c = cappedBuffer{max: 10}
	if n, err := c.Write([]byte("123456789012345")); n != 10 || err == nil || !c.full || c.buf.String() != "1234567890" {
		t.Errorf("a write across the limit = %d, %v, full %v, kept %q", n, err, c.full, c.buf.String())
	}
}

func TestPreviewTarget(t *testing.T) {
	old, last := time.Unix(1000, 0), time.Unix(2000, 0)
	m := &changeWatchModel{
		prev: map[string]fileSig{"a.go": {mtime: 1}, "b.go": {mtime: 5}, "c.go": {mtime: 9}},
		events: []changeEvent{
			{ts: old, path: "a.go"},
			{ts: last, path: "b.go"},
			{ts: last, path: "c.go"},
		},
	}
	if got := m.previewTarget(); got != "c.go" {
		t.Errorf("target = %q, want c.go: the file of the newest refresh written last", got)
	}
	// The events of one refresh are in path order, not in the order of the
	// edits: the file written last wins, wherever its row is.
	m.prev["b.go"] = fileSig{mtime: 12}
	if got := m.previewTarget(); got != "b.go" {
		t.Errorf("target = %q, want b.go", got)
	}
	m.prev["b.go"] = fileSig{mtime: 9}
	if got := m.previewTarget(); got != "c.go" {
		t.Errorf("target = %q, want c.go: the lowest row on a tie", got)
	}

	// A cleared event and a file with no change now are skipped, back to an
	// older refresh.
	m.events = append(m.events, changeEvent{ts: time.Unix(3000, 0), path: "c.go", label: "cleared", cleared: true})
	delete(m.prev, "c.go")
	delete(m.prev, "b.go")
	if got := m.previewTarget(); got != "a.go" {
		t.Errorf("target = %q, want a.go", got)
	}

	m.previewPin = "b.go"
	if got := m.previewTarget(); got != "b.go" {
		t.Errorf("target = %q, want the pinned b.go", got)
	}
	m.previewPin = ""

	// [c] empties the feed: the file on show stays while it has a change.
	m.events = nil
	m.preview.path = "a.go"
	if got := m.previewTarget(); got != "a.go" {
		t.Errorf("target = %q, want a.go kept", got)
	}
	delete(m.prev, "a.go")
	if got := m.previewTarget(); got != "" {
		t.Errorf("target = %q, want none", got)
	}
}

// TestChangeWatchPreviewFlow drives the pane against a repository: [d] opens
// it on the changed file, an unchanged file is not read again, an edit and a
// moved HEAD are, and [d] closes it.
func TestChangeWatchPreviewFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := previewRepo(t)
	m := previewModel(t, repo.Dir, 160, 30)
	drive(m, m.refreshCmd())
	if m.previewOn || m.preview.path != "" || strings.Contains(m.View(), "─── diff") {
		t.Fatalf("the pane shows before [d]:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "[d] diff") {
		t.Errorf("the keybar does not offer [d]:\n%s", m.View())
	}

	_, cmd := m.handleKey(keyMsg("d"))
	if cmd == nil || !m.previewLoading {
		t.Fatalf("[d] started no read")
	}
	if m.previewCmd() != nil {
		t.Errorf("a second read started while one is in flight")
	}
	drive(m, cmd)
	v := m.View()
	if m.preview.path != "a.go" || m.previewLoading {
		t.Fatalf("preview = %+v", m.preview)
	}
	for _, want := range []string{"─── diff a.go", "+1", "following", "    return 10", "[d] diff off"} {
		if !strings.Contains(v, want) {
			t.Errorf("view misses %q:\n%s", want, v)
		}
	}

	// A refresh that finds the same file reads no diff.
	drive(m, m.refreshCmd())
	if m.previewCmd() != nil || m.previewLoading {
		t.Errorf("an unchanged file was read again")
	}

	// An edit of the file on show is read on the refresh that sees it.
	repo.WriteFile("a.go", "package a\n\nfunc alpha() int {\n\treturn 100\n}\n\nfunc beta() int {\n\treturn 2\n}\n")
	drive(m, m.refreshCmd())
	if !strings.Contains(m.View(), "    return 100") {
		t.Errorf("the edit did not reach the pane:\n%s", m.View())
	}

	// HEAD is the base of the diff: a moved HEAD makes the pane stale though
	// the file's signature is the same.
	m.head.sha = "moved"
	if m.previewCmd() == nil {
		t.Errorf("a moved HEAD started no read")
	}
	m.previewLoading = false

	// A read of a file the pane does not want is dropped.
	m.preview.head = m.head.sha
	m.Update(changePreviewMsg{preview: changePreview{path: "other.go", rows: []string{"x"}}})
	if m.preview.path != "a.go" {
		t.Errorf("a read of another file replaced the pane: %q", m.preview.path)
	}

	m.handleKey(keyMsg("d"))
	if m.previewOn || m.preview.path != "" || strings.Contains(m.View(), "─── diff") {
		t.Errorf("[d] did not close the pane:\n%s", m.View())
	}
}

// TestChangeWatchPreviewScrollsToTheChange covers a second read of the file on
// show: a pane that follows the feed moves to the hunk that changed, and a
// pinned pane keeps the rows being read.
func TestChangeWatchPreviewScrollsToTheChange(t *testing.T) {
	sig := fileSig{xy: " M", mtime: 2}
	rows := make([]string, 60)
	next := changePreview{path: "a.go", sig: sig, rows: rows, hunkRows: []int{0, 40}, hunkKeys: []string{"+one\n", "+two\n"}}
	model := func() *changeWatchModel {
		return &changeWatchModel{
			previewOn: true, previewScroll: 5,
			prev:    map[string]fileSig{"a.go": sig},
			events:  []changeEvent{{ts: time.Unix(1000, 0), path: "a.go"}},
			preview: changePreview{path: "a.go", rows: rows, hunkRows: []int{0}, hunkKeys: []string{"+one\n"}},
		}
	}

	m := model()
	m.applyPreview(changePreviewMsg{preview: next})
	if m.previewScroll != 40 {
		t.Errorf("following: scroll = %d, want 40, the row of the new hunk", m.previewScroll)
	}

	m = model()
	m.previewPin = "a.go"
	m.applyPreview(changePreviewMsg{preview: next})
	if m.previewScroll != 5 {
		t.Errorf("pinned: scroll = %d, want 5 kept", m.previewScroll)
	}

	// A read that changed no hunk, a re-save for example, keeps the position.
	m = model()
	same := next
	same.hunkRows, same.hunkKeys = []int{0}, []string{"+one\n"}
	m.applyPreview(changePreviewMsg{preview: same})
	if m.previewScroll != 5 {
		t.Errorf("no changed hunk: scroll = %d, want 5 kept", m.previewScroll)
	}

	// Another file starts at its top.
	m = model()
	m.events = append(m.events, changeEvent{ts: time.Unix(2000, 0), path: "b.go"})
	m.prev["b.go"] = sig
	m.applyPreview(changePreviewMsg{preview: changePreview{path: "b.go", sig: sig, rows: rows}})
	if m.preview.path != "b.go" || m.previewScroll != 0 {
		t.Errorf("another file: path %q, scroll %d", m.preview.path, m.previewScroll)
	}
}

func TestChangeWatchPreviewKeys(t *testing.T) {
	m := previewModel(t, t.TempDir(), 160, 30)
	m.prev = map[string]fileSig{"a.go": {mtime: 1}, "b.go": {mtime: 2}, "c.go": {mtime: 3}}
	m.events = []changeEvent{{ts: time.Unix(1000, 0), path: "b.go"}}
	press := func(k tea.KeyMsg) {
		t.Helper()
		m.handleKey(k)
		m.previewLoading = false // the read is not run here
	}
	tab, shiftTab := tea.KeyMsg{Type: tea.KeyTab}, tea.KeyMsg{Type: tea.KeyShiftTab}

	// The pane's keys do nothing while it is closed.
	press(tab)
	press(keyMsg("f"))
	if m.previewPin != "" || m.previewOn {
		t.Fatalf("a pane key acted on a closed pane: %+v", m.previewPin)
	}

	press(keyMsg("d"))
	if !m.previewOn || m.previewTarget() != "b.go" {
		t.Fatalf("on = %v, target = %q", m.previewOn, m.previewTarget())
	}

	// [f] pins the file on show, and follows the feed again.
	press(keyMsg("f"))
	m.events = append(m.events, changeEvent{ts: time.Unix(2000, 0), path: "c.go"})
	if m.previewPin != "b.go" || m.previewTarget() != "b.go" || !strings.Contains(m.View(), "pinned") {
		t.Errorf("pin = %q, target = %q", m.previewPin, m.previewTarget())
	}
	press(keyMsg("f"))
	if m.previewPin != "" || m.previewTarget() != "c.go" {
		t.Errorf("after the second [f]: pin = %q, target = %q", m.previewPin, m.previewTarget())
	}

	// [tab] walks the changed files in path order, around the ends, and pins.
	for _, want := range []string{"a.go", "b.go", "c.go", "a.go"} {
		press(tab)
		if m.previewPin != want {
			t.Fatalf("tab: pin = %q, want %q", m.previewPin, want)
		}
	}
	press(shiftTab)
	if m.previewPin != "c.go" {
		t.Errorf("shift+tab: pin = %q, want c.go", m.previewPin)
	}
	// From a pinned file that has no change now, the step starts at an end.
	m.previewPin = "gone.go"
	press(tab)
	if m.previewPin != "a.go" {
		t.Errorf("tab from a file with no change: pin = %q, want a.go", m.previewPin)
	}

	// ctrl+d and ctrl+u move the diff by half a pane and stop at its ends.
	m.preview = changePreview{path: "a.go", sig: m.prev["a.go"], rows: make([]string, 100)}
	body := m.previewLayout().paneRows - 1
	press(tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.previewScroll != body/2 {
		t.Errorf("ctrl+d: scroll = %d, want %d", m.previewScroll, body/2)
	}
	for i := 0; i < 20; i++ {
		press(tea.KeyMsg{Type: tea.KeyCtrlD})
	}
	if m.previewScroll != 100-body {
		t.Errorf("ctrl+d at the end: scroll = %d, want %d", m.previewScroll, 100-body)
	}
	for i := 0; i < 20; i++ {
		press(tea.KeyMsg{Type: tea.KeyCtrlU})
	}
	if m.previewScroll != 0 {
		t.Errorf("ctrl+u at the top: scroll = %d, want 0", m.previewScroll)
	}

	// The status dashboard covers the pane: [d] waits for the feed.
	m.showDash = true
	press(keyMsg("d"))
	if !m.previewOn {
		t.Errorf("[d] closed the pane under the status dashboard")
	}
}

// previewFrame is a model with a long feed and a long, wide diff on show.
func previewFrame(width, height int) *changeWatchModel {
	m := &changeWatchModel{
		width: width, height: height, previewOn: true,
		head: headInfo{repo: "gk", branch: "develop", upstream: "origin/develop", sha: "abc1234", ago: "3m", subject: strings.Repeat("subject ", 30)},
		now:  func() time.Time { return time.Unix(9000, 0) },
		prev: map[string]fileSig{},
	}
	for i := 0; i < 80; i++ {
		path := fmt.Sprintf("internal/some/deeply/nested/package/of/the/repository/file_%02d.go", i)
		m.events = append(m.events, changeEvent{
			ts: time.Unix(int64(1000+i), 0), path: path, label: "mod", added: 120 + i, removed: 45,
			symbols: "firstFunction, secondFunction, thirdFunction", note: "re-touched",
		})
		m.prev[path] = fileSig{xy: " M"}
	}
	p := changePreview{path: m.events[len(m.events)-1].path, sig: fileSig{xy: " M"}, added: 300, removed: 20}
	for i := 0; i < 300; i++ {
		p.rows = append(p.rows, fmt.Sprintf("%4d  ▶ ▌ %s 한글 주석 %s", i, strings.Repeat("code ", 40), strings.Repeat("넓은글자", 20)))
	}
	m.preview = p
	return m
}

// TestChangeWatchPreviewLayout holds the frame to the terminal: never more
// rows than the height, never a row wider than the width, the pane beside the
// feed from 140 columns and below it under that.
func TestChangeWatchPreviewLayout(t *testing.T) {
	cases := []struct {
		width, height int
		side, pane    bool
	}{
		{160, 40, true, true},
		{140, 40, true, true},
		{139, 40, false, true},
		{100, 20, false, true},
		{100, 10, false, false}, // too few rows: the feed keeps the screen
		{0, 0, false, false},    // size not known yet
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			m := previewFrame(tc.width, tc.height)
			lay := m.previewLayout()
			if lay.side != tc.side || (lay.paneRows > 0) != tc.pane {
				t.Fatalf("layout = %+v, want side %v, pane %v", lay, tc.side, tc.pane)
			}
			rows := strings.Split(m.View(), "\n")
			if got := strings.Contains(m.View(), "─── diff"); got != tc.pane {
				t.Errorf("pane on screen = %v, want %v", got, tc.pane)
			}
			if tc.width == 0 {
				return
			}
			if tc.pane && len(rows) != tc.height {
				t.Errorf("%d rows, want %d: the frame fills the terminal", len(rows), tc.height)
			}
			if len(rows) > tc.height {
				t.Errorf("%d rows on a terminal of %d", len(rows), tc.height)
			}
			for i, row := range rows {
				if w := lipgloss.Width(row); w > tc.width {
					t.Errorf("row %d is %d columns wide, over %d: %q", i, w, tc.width, row)
				}
			}
			if tc.side {
				// The gutter is at one column on every body row.
				col := -1
				for _, row := range rows {
					if i := strings.Index(row, changePreviewGutter); i >= 0 {
						if c := lipgloss.Width(row[:i]); col >= 0 && c != col {
							t.Errorf("gutter at column %d and at %d", col, c)
						} else {
							col = c
						}
					}
				}
				if col != lay.feedWidth {
					t.Errorf("gutter at column %d, want %d", col, lay.feedWidth)
				}
			}
		})
	}

	// The feed scrolls by the rows it has on screen, not by the whole body.
	below := previewFrame(100, 30)
	if got, body := below.feedBudget(), below.bodyBudget(); got != body*2/5 {
		t.Errorf("feed budget below the feed = %d, want %d of a body of %d", got, body*2/5, body)
	}
	// A feed with few events leaves its rows to the pane.
	below.events = below.events[:2]
	if lay := below.previewLayout(); lay.feedRows != 2 || lay.paneRows != below.bodyBudget()-2 {
		t.Errorf("layout with two events = %+v", lay)
	}

	// The title says which rows are on show when the diff is longer than the pane.
	m := previewFrame(160, 40)
	m.previewScroll = 10
	if title := m.previewPane(80, 20)[0]; !strings.Contains(title, "11-29/300") || !strings.Contains(title, "file_79.go") {
		t.Errorf("title = %q", title)
	}
}

func TestFleetZoomPreview(t *testing.T) {
	m := zoomTestModel()
	m.entries = append(m.entries, fleetEntryJSON{Path: "/wt/d", Branch: "feat/other", Status: "dirty"})
	model, _ := m.openZoom("/wt/b")
	fm := model.(fleetModel)

	// [d] reaches the embedded view, and the fleet remembers it.
	model, _ = fm.handleZoomKey(keyMsg("d"))
	fm = model.(fleetModel)
	if !fm.zoom.previewOn || !fm.zoomDiff {
		t.Fatalf("[d] did not open the pane in the zoom: on %v, kept %v", fm.zoom.previewOn, fm.zoomDiff)
	}
	sig := fileSig{xy: " M", mtime: 3}
	fm.zoom.prev = map[string]fileSig{"x.go": sig, "y.go": sig}
	fm.zoom.events = []changeEvent{{ts: time.Unix(1000, 0), path: "x.go"}}

	// The pane's other keys reach it too.
	model, _ = fm.handleZoomKey(tea.KeyMsg{Type: tea.KeyTab})
	fm = model.(fleetModel)
	if fm.zoom.previewPin != "y.go" {
		t.Errorf("tab in the zoom: pin = %q, want y.go", fm.zoom.previewPin)
	}
	fm.zoom.previewPin, fm.zoom.previewLoading = "", false

	// A read from a replaced zoom is dropped; one from this zoom is shown.
	read := changePreview{path: "x.go", sig: sig, rows: []string{"   1  ▶ ▌ changed line"}}
	model, _ = fm.Update(changePreviewMsg{gen: fm.zoomGen - 1, preview: read})
	fm = model.(fleetModel)
	if fm.zoom.preview.path != "" {
		t.Errorf("a read of an older zoom reached the pane")
	}
	model, _ = fm.Update(changePreviewMsg{gen: fm.zoomGen, preview: read})
	fm = model.(fleetModel)
	if fm.zoom.preview.path != "x.go" || !strings.Contains(fm.View(), "changed line") {
		t.Errorf("the read did not reach the pane:\n%s", fm.View())
	}

	// The frame keeps to the terminal with the breadcrumb above it.
	fm.width, fm.height = 160, 40
	fm.zoom.width, fm.zoom.height = 160, zoomBodyHeight(40)
	rows := strings.Split(fm.View(), "\n")
	if len(rows) > 40 {
		t.Errorf("%d rows on a terminal of 40", len(rows))
	}
	for i, row := range rows {
		if w := lipgloss.Width(row); w > 160 {
			t.Errorf("row %d is %d columns wide: %q", i, w, row)
		}
	}

	// A hop to another worktree and a re-entry keep the pane open.
	model, _ = fm.handleZoomKey(keyMsg("]"))
	fm = model.(fleetModel)
	if fm.zoomPath == "/wt/b" || !fm.zoom.previewOn {
		t.Errorf("after ]: path %q, pane on %v", fm.zoomPath, fm.zoom.previewOn)
	}
	model, _ = fm.handleZoomKey(keyMsg("esc"))
	fm = model.(fleetModel)
	model, _ = fm.openZoom("/wt/b")
	fm = model.(fleetModel)
	if !fm.zoom.previewOn {
		t.Errorf("a re-entry closed the pane")
	}
	model, _ = fm.handleZoomKey(keyMsg("d"))
	fm = model.(fleetModel)
	if fm.zoom.previewOn || fm.zoomDiff {
		t.Errorf("[d] did not close the pane in the zoom")
	}
}

// TestLoadChangePreviewKinds covers the other kinds of change the feed marks:
// a conflict, a staged rename, a staged new file, and a mode change.
func TestLoadChangePreviewKinds(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("c.txt", "base\n")
	repo.WriteFile("old.txt", "one\ntwo\nthree\n")
	repo.WriteFile("mode.sh", "echo hi\n")
	repo.Commit("init")
	repo.RunGit("checkout", "-q", "-b", "side")
	repo.WriteFile("c.txt", "side\n")
	repo.Commit("side")
	repo.RunGit("checkout", "-q", "-")
	repo.WriteFile("c.txt", "main\n")
	repo.Commit("main")
	if _, err := repo.TryGit("merge", "side"); err == nil {
		t.Fatal("the merge did not conflict")
	}
	repo.RunGit("mv", "old.txt", "renamed.txt")
	repo.WriteFile("staged.go", "package a\n")
	repo.RunGit("add", "staged.go")
	if err := os.Chmod(filepath.Join(repo.Dir, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	load := func(path, xy string) changePreview {
		return loadChangePreview(context.Background(), &git.ExecRunner{Dir: repo.Dir}, repo.Dir, path, fileSig{xy: xy}, "")
	}

	// A conflicted file shows both sides between its markers.
	p := load("c.txt", "UU")
	for _, want := range []string{"<<<<<<< HEAD", "main", "=======", "side", ">>>>>>> side"} {
		if p.note != "" || !strings.Contains(joined(p), want) {
			t.Errorf("conflict: note %q, rows miss %q:\n%s", p.note, want, joined(p))
		}
	}
	// A staged rename shows under its new path as added lines: the diff of one
	// path cannot pair it with the old path.
	if p = load("renamed.txt", "R "); p.note != "" || p.added != 3 || !strings.Contains(joined(p), "three") {
		t.Errorf("rename: %+v", p)
	}
	if p = load("staged.go", "A "); p.note != "" || p.added != 1 || !strings.Contains(joined(p), "package a") {
		t.Errorf("staged new file: %+v", p)
	}
	// A mode change has no hunk: the pane says so, not nothing.
	if p = load("mode.sh", " M"); !strings.Contains(p.note, "no line changed") || !strings.Contains(p.note, "100755") {
		t.Errorf("mode change: note %q", p.note)
	}
}

// TestLoadChangePreviewBounds covers a diff larger than the pane reads: the
// read stops at changePreviewMaxBytes, and a row keeps the start of its line.
func TestLoadChangePreviewBounds(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	ctx := context.Background()

	t.Run("a diff past the byte limit is cut", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		repo.WriteFile("big.txt", "start\n")
		repo.Commit("init")
		line := strings.Repeat("x", 199) + "\n"
		lines := 4 * changePreviewMaxBytes / len(line)
		repo.WriteFile("big.txt", strings.Repeat(line, lines))
		p := loadChangePreview(ctx, &git.ExecRunner{Dir: repo.Dir}, repo.Dir, "big.txt", fileSig{xy: " M", added: lines, removed: 1}, "")
		if p.note != "" || len(p.rows) != changePreviewMaxRows+1 {
			t.Fatalf("note %q, %d rows", p.note, len(p.rows))
		}
		if last := p.rows[len(p.rows)-1]; !strings.Contains(last, "1 MB") {
			t.Errorf("last row = %q, want the byte limit", last)
		}
		// The part that was read has fewer lines than the file: the counts are
		// those of the feed.
		if p.added != lines || p.removed != 1 {
			t.Errorf("counts = +%d -%d, want +%d -1 from the signature", p.added, p.removed, lines)
		}
	})

	t.Run("a row keeps the start of a long line", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		repo.WriteFile("min.js", strings.Repeat("a", 3*changePreviewMaxBytes)+"\n")
		repo.Commit("init")
		repo.WriteFile("min.js", strings.Repeat("b", 3*changePreviewMaxBytes)+"\n")
		p := loadChangePreview(ctx, &git.ExecRunner{Dir: repo.Dir}, repo.Dir, "min.js", fileSig{xy: " M"}, "")
		if p.note != "" || len(p.rows) < 2 {
			t.Fatalf("note %q, %d rows", p.note, len(p.rows))
		}
		for i, row := range p.rows {
			if w := lipgloss.Width(row); w > changePreviewMaxCols+16 {
				t.Errorf("row %d is %d columns wide", i, w)
			}
		}
		if !strings.Contains(joined(p), strings.Repeat("a", 100)) {
			t.Errorf("the rows miss the start of the line")
		}
	})
}

// TestChangeWatchPreviewDoesNotShowAnotherFile covers the time between a new
// file to show and the end of its read: the pane names the new file and says
// that it reads, and it does not show the rows of the file before it.
func TestChangeWatchPreviewDoesNotShowAnotherFile(t *testing.T) {
	m := previewFrame(160, 40)
	before := m.preview.path
	m.events = append(m.events, changeEvent{ts: time.Unix(5000, 0), path: "next.go", label: "mod"})
	m.prev["next.go"] = fileSig{xy: " M", mtime: 9}
	m.previewLoading = true
	if m.previewTarget() != "next.go" {
		t.Fatalf("target = %q", m.previewTarget())
	}
	pane := strings.Join(m.previewPane(80, 20), "\n")
	if !strings.Contains(pane, "diff next.go") || !strings.Contains(pane, "loading…") {
		t.Errorf("the pane does not say that it reads next.go:\n%s", pane)
	}
	if strings.Contains(pane, filepath.Base(before)) || strings.Contains(pane, "code code") || strings.Contains(pane, "+300") {
		t.Errorf("the pane shows %s while it reads next.go:\n%s", before, pane)
	}

	// The same file with an older read on show keeps its rows: the title is
	// right, and the new read replaces them.
	m.applyPreview(changePreviewMsg{preview: changePreview{path: "next.go", sig: fileSig{xy: " M", mtime: 9}, head: m.head.sha, rows: []string{"   1  ▶ ▌ next line"}}})
	m.prev["next.go"] = fileSig{xy: " M", mtime: 10}
	m.previewLoading = true
	if pane = strings.Join(m.previewPane(80, 20), "\n"); !strings.Contains(pane, "next line") || strings.Contains(pane, "loading…") {
		t.Errorf("a new read of the file on show hid its rows:\n%s", pane)
	}
}
