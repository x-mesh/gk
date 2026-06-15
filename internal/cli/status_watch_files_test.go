package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestFetchHeadInfo verifies the compact-header orientation fetch resolves the
// branch and HEAD commit against a real repo (no upstream → ahead/behind 0).
func TestFetchHeadInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("f.txt", "x")
	repo.RunGit("add", "f.txt")
	repo.Commit("feat: the initial thing")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	h := fetchHeadInfo(cmd, &git.ExecRunner{Dir: repo.Dir})

	if h.branch == "" {
		t.Errorf("branch should be resolved, got empty")
	}
	if h.sha == "" {
		t.Errorf("HEAD short sha should be resolved, got empty")
	}
	if !strings.Contains(h.subject, "the initial thing") {
		t.Errorf("subject = %q, want it to contain the commit message", h.subject)
	}
}

// TestChangeSnapshot_RenameKeysNewPath drives changeSnapshot against a real
// repo with a staged rename + content change, proving the porcelain path and
// the --numstat -z stat both key by the NEW path (the bug Codex/the review
// flagged: the non-z numstat keys renames as "old => new" and loses the stat).
func TestChangeSnapshot_RenameKeysNewPath(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("old.go", "l1\nl2\n")
	repo.RunGit("add", "old.go")
	repo.Commit("init")
	repo.RunGit("mv", "old.go", "new.go")
	repo.WriteFile("new.go", "l1\nl2\nl3\n") // +1 line on top of the rename
	repo.RunGit("add", "new.go")

	sigs := changeSnapshot(context.Background(), &git.ExecRunner{Dir: repo.Dir}, repo.Dir)
	s, ok := sigs["new.go"]
	if !ok {
		t.Fatalf("snapshot missing new.go: %+v", sigs)
	}
	if s.added != 1 {
		t.Errorf("new.go added = %d, want 1 — rename stat must key by the new path", s.added)
	}
	if _, ok := sigs["old.go"]; ok {
		t.Errorf("old.go must not appear as a separate entry (it is the rename source)")
	}
}

func sig(xy string, a, r int) fileSig { return fileSig{xy: xy, added: a, removed: r} }

func eventsByPath(evs []changeEvent) map[string]changeEvent {
	m := map[string]changeEvent{}
	for _, e := range evs {
		m[e.path] = e
	}
	return m
}

func TestDiffChangeSnapshots_Baseline(t *testing.T) {
	ts := time.Unix(1000, 0)
	curr := map[string]fileSig{
		"a.go": sig(".M", 3, 1),
		"b.go": sig("??", 0, 0),
	}
	evs := diffChangeSnapshots(nil, curr, ts) // prev==nil → baseline
	if len(evs) != 2 {
		t.Fatalf("baseline should seed every dirty file, got %d", len(evs))
	}
	for _, e := range evs {
		if e.note != "" {
			t.Errorf("baseline events carry no note, got %q for %s", e.note, e.path)
		}
	}
	// Sorted by path for stable batch rendering.
	if evs[0].path != "a.go" || evs[1].path != "b.go" {
		t.Errorf("events must be path-sorted, got %q then %q", evs[0].path, evs[1].path)
	}
}

func TestDiffChangeSnapshots_NewRetouchedClearedUnchanged(t *testing.T) {
	ts := time.Unix(2000, 0)
	prev := map[string]fileSig{
		"keep.go": sig(".M", 5, 0), // unchanged → no event
		"edit.go": sig(".M", 2, 0), // will change → re-touched
		"gone.go": sig("??", 0, 0), // disappears → cleared
	}
	curr := map[string]fileSig{
		"keep.go": sig(".M", 5, 0),
		"edit.go": sig(".M", 9, 1), // counts changed
		"new.go":  sig("??", 0, 0), // brand new
	}
	got := eventsByPath(diffChangeSnapshots(prev, curr, ts))

	if _, ok := got["keep.go"]; ok {
		t.Errorf("unchanged file must not emit an event")
	}
	if e := got["edit.go"]; e.note != "re-touched" || e.added != 9 || e.removed != 1 {
		t.Errorf("edit.go: want re-touched +9 -1, got %+v", e)
	}
	if e := got["new.go"]; e.note != "new" {
		t.Errorf("new.go: want note 'new', got %q", e.note)
	}
	if e, ok := got["gone.go"]; !ok || !e.cleared || e.label != "cleared" {
		t.Errorf("gone.go: want a cleared event, got %+v (present=%v)", e, ok)
	}
	if len(got) != 3 {
		t.Errorf("want exactly 3 events (edit/new/gone), got %d", len(got))
	}
}

// TestDiffChangeSnapshots_MtimeReTouch: a re-save that leaves the porcelain
// code and +/- counts identical but bumps mtime must still emit "re-touched".
func TestDiffChangeSnapshots_MtimeReTouch(t *testing.T) {
	ts := time.Unix(3000, 0)
	prev := map[string]fileSig{"f.go": {xy: ".M", added: 2, removed: 1, mtime: 100}}
	curr := map[string]fileSig{"f.go": {xy: ".M", added: 2, removed: 1, mtime: 200}} // only mtime changed
	evs := diffChangeSnapshots(prev, curr, ts)
	if len(evs) != 1 || evs[0].note != "re-touched" {
		t.Fatalf("an mtime-only change must emit re-touched, got %+v", evs)
	}
	// Identical signature (same mtime) stays silent.
	if evs := diffChangeSnapshots(prev, prev, ts); len(evs) != 0 {
		t.Errorf("unchanged sig must emit nothing, got %+v", evs)
	}
}

func TestParseNumstatZ(t *testing.T) {
	// Normal record, a rename (empty path field + old/new NUL tokens, keyed
	// by NEW path), a binary file ("-"), and a binary rename — mixed in one
	// stream so cursor alignment after each form is exercised.
	in := "3\t1\tfile.go\x00" +
		"5\t2\t\x00old.go\x00new.go\x00" +
		"-\t-\timg.png\x00" +
		"-\t-\t\x00a.bin\x00b.bin\x00" +
		"7\t0\tlast.go\x00"
	got := parseNumstatZ([]byte(in))

	if s := got["file.go"]; s.added != 3 || s.removed != 1 {
		t.Errorf("file.go = %+v, want {3 1}", s)
	}
	if s := got["new.go"]; s.added != 5 || s.removed != 2 {
		t.Errorf("rename must key by NEW path: new.go = %+v, want {5 2}", s)
	}
	if _, ok := got["old.go"]; ok {
		t.Errorf("rename must NOT key by old path")
	}
	if _, ok := got["img.png"]; ok {
		t.Errorf("binary file must be skipped")
	}
	if _, ok := got["b.bin"]; ok {
		t.Errorf("binary rename must be skipped")
	}
	// last.go proves the cursor stayed aligned through the binary-rename form.
	if s := got["last.go"]; s.added != 7 || s.removed != 0 {
		t.Errorf("cursor misaligned after binary rename: last.go = %+v, want {7 0}", s)
	}
}

// TestChangeWatchView_CompactHeaderAndFeed locks the unified split layout:
// a compact status header (orientation + HEAD commit + dirty rollup), the
// "live changes" divider, then the feed. Asserts on literal text, which
// survives the ANSI styling lipgloss wraps around it.
func TestChangeWatchView_CompactHeaderAndFeed(t *testing.T) {
	m := &changeWatchModel{
		interval: 2 * time.Second,
		width:    100,
		height:   20,
		head: headInfo{
			repo: "gk", branch: "develop", upstream: "origin/develop",
			ahead: 8, sha: "0dbe763d", subject: "chore(scripts): add embedding probe",
		},
		files: 5, added: 228, removed: 79,
		events: []changeEvent{
			{ts: time.Unix(1000, 0), path: "app/core/database/base.py", label: "mod", added: 72, removed: 11},
			{ts: time.Unix(1001, 0), path: "tests/test_blue_green.py", label: "new", note: "new"},
		},
		now: func() time.Time { return time.Unix(2000, 0) },
	}
	v := m.View()
	for _, want := range []string{
		"WATCHING", "develop", "origin/develop", "↑8", // orientation line
		"5 files", "0dbe763d", "embedding probe", // rollup + HEAD commit
		"live changes",                                // divider
		"base.py", "+72", "test_blue_green.py", "new", // feed
	} {
		if !strings.Contains(v, want) {
			t.Errorf("View missing %q\n---\n%s", want, v)
		}
	}
	// The divider carries the live wall-clock (m.now is injected), with a
	// green live-dot — the only `●` in this fixtured frame.
	if clock := m.nowFn().Format("15:04:05"); !strings.Contains(v, clock) {
		t.Errorf("divider must show the live clock %q\n%s", clock, v)
	}
	if !strings.Contains(v, "●") {
		t.Errorf("divider must show the live dot\n%s", v)
	}
	t.Logf("rendered frame:\n%s", v) // visible with `go test -v`
}

// TestChangeWatchView_DashboardToggle verifies the [s] toggle swaps the feed
// for the captured full-status frame (and back).
func TestChangeWatchView_DashboardToggle(t *testing.T) {
	m := &changeWatchModel{
		width: 100, height: 30, showDash: true,
		dashFrame: "█  BRANCH\n   develop\n█  WORKING TREE\n   5 files",
		head:      headInfo{branch: "develop", sha: "abc1234", subject: "x"},
		events:    []changeEvent{{ts: time.Unix(1, 0), path: "x.go", label: "mod", added: 1}},
		now:       func() time.Time { return time.Unix(2, 0) },
	}
	v := m.View()
	if !strings.Contains(v, "BRANCH") || !strings.Contains(v, "WORKING TREE") {
		t.Errorf("dashboard view must show the captured status frame:\n%s", v)
	}
	if !strings.Contains(v, "back to live feed") {
		t.Errorf("dashboard view must show the toggle hint:\n%s", v)
	}
	if strings.Contains(v, "live changes") {
		t.Errorf("dashboard view must not render the feed divider:\n%s", v)
	}
}

func TestChangeGlyph(t *testing.T) {
	cases := []struct {
		e    changeEvent
		want string
	}{
		{changeEvent{label: "new"}, "+"},
		{changeEvent{label: "added"}, "+"}, // staged-add (xyLabel "A ") is also "+"
		{changeEvent{label: "deleted"}, "−"},
		{changeEvent{label: "conflict"}, "⚔"},
		{changeEvent{label: "renamed"}, "→"},
		{changeEvent{label: "mod"}, "~"},
		{changeEvent{label: "staged"}, "~"},
		{changeEvent{cleared: true, label: "cleared"}, "✓"},
	}
	for _, c := range cases {
		if got := changeGlyph(c.e); got != c.want {
			t.Errorf("changeGlyph(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestRollupSnapshot(t *testing.T) {
	curr := map[string]fileSig{
		"a": sig(".M", 10, 2),
		"b": sig(".M", 5, 3),
		"c": sig("??", 0, 0),
	}
	files, added, removed := rollupSnapshot(curr)
	if files != 3 || added != 15 || removed != 5 {
		t.Errorf("rollup = (%d files, +%d -%d), want (3, +15 -5)", files, added, removed)
	}
}

func TestPlainEventLine(t *testing.T) {
	ts := time.Date(2026, 6, 15, 12, 4, 33, 110000000, time.UTC) // .11s
	line := plainEventLine(changeEvent{
		ts: ts, path: "internal/cli/switch.go", label: "mod",
		added: 37, removed: 4, note: "re-touched",
	})
	for _, want := range []string{"12:04:33.11", "~", "internal/cli/switch.go", "+37 -4", "re-touched"} {
		if !strings.Contains(line, want) {
			t.Errorf("plain line %q missing %q", line, want)
		}
	}
	// A clean stat (no diff) omits the +/- segment.
	clean := plainEventLine(changeEvent{ts: ts, path: "x", label: "new", note: "new"})
	if strings.Contains(clean, "+0") {
		t.Errorf("zero-stat line must not print +0 -0: %q", clean)
	}
}
