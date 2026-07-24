package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

func TestSummarizeToolArgs(t *testing.T) {
	cases := []struct {
		name string
		tool string
		raw  string
		want string
	}{
		{"log with range and limit", "git_log", `{"range":"main..develop","limit":30}`, "main..develop · 30"},
		{"log without range says HEAD", "git_log", `{"limit":5}`, "HEAD · 5"},
		{"log with author", "git_log", `{"author":"jinwoo","limit":10}`, "HEAD · @jinwoo · 10"},
		{"log with path", "git_log", `{"range":"develop","path":"internal/cli"}`, "develop · internal/cli"},
		{"log with several paths", "git_log", `{"paths":["a.go","b.go","c.go"]}`, "HEAD · a.go +2"},
		{"grep quotes the pattern", "git_grep", `{"pattern":"IsError","paths":["internal/chat"]}`, `"IsError" · internal/chat`},
		{"file_read line span", "file_read", `{"path":"registry.go","start_line":1,"end_line":240}`, "registry.go:1-240"},
		{"file_read open-ended span", "file_read", `{"path":"registry.go","start_line":80}`, "registry.go:80-"},
		{"file_read whole file", "file_read", `{"path":"registry.go"}`, "registry.go"},
		{"file_list defaults to root", "file_list", `{}`, "."},
		{"diff staged", "git_diff", `{"staged":true}`, "staged"},
		{"show ref", "git_show", `{"ref":"e47b695","path":"x.go"}`, "e47b695 · x.go"},
		{"suggest quotes the intent", "gk_suggest", `{"intent":"clean up merged branches"}`, `"clean up merged branches"`},
		{"no-argument tool renders empty", "git_context", `{}`, ""},
		// The envelope is unwrapped before rendering, so its packaging never
		// reaches the terminal.
		{"openai envelope is unwrapped", "git_log", `{"recipient_name":"functions.git_log","parameters":{"range":"develop","limit":5}}`, "develop · 5"},
		// An unknown tool must still show something rather than a blank cell.
		{"unknown tool falls back to json", "future_tool", `{"a":1}`, `{"a":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeToolArgs(tc.tool, json.RawMessage(tc.raw)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSummarizeToolResult(t *testing.T) {
	logLines := "e47b695 2026-07-23 Jinwoo fix(watch): leak\n71ceff1 2026-07-22 Jinwoo release: v0.133.0"
	cases := []struct {
		name    string
		tool    string
		content string
		want    string
	}{
		// The signal the whole feature exists for: an empty result is the
		// thing a truncated JSON dump never showed.
		{"empty result", "git_log", "", "none"},
		{"whitespace-only result", "git_log", "  \n ", "none"},
		{"commit count", "git_log", logLines, "2 commits"},
		{"single commit is not pluralized", "git_log", "e47b695 2026-07-23 Jinwoo fix", "1 commit"},
		{"grep counts matches and files", "git_grep", "a.go:1:x\na.go:2:y\nb.go:9:z", "3 matches · 2 files"},
		{"grep in a single file omits the file count", "git_grep", "a.go:1:x\na.go:2:y", "2 matches"},
		{"file_read counts lines", "file_read", "one\ntwo\nthree", "3 lines"},
		{"context names the branch", "git_context", `{"branch":"develop"}`, "develop"},
		// git_status earns a real summary because "is the tree dirty" is the
		// question the row is being scanned for.
		{"clean tree", "git_status", `{"clean":true}`, "clean"},
		{"dirty tree", "git_status", `{"changed":[{"code":" M"},{"code":"M "}],"untracked_count":8}`, "2 changed · 8 untracked"},
		{"only untracked", "git_status", `{"untracked_count":3}`, "3 untracked"},
		// An interrupted operation outranks the counts.
		{"mid-rebase with conflicts", "git_status", `{"conflict":true,"changed":[{"code":"UU"}],"in_progress":{"kind":"rebase"}}`, "rebase · conflicts · 1 changed"},
		{"unparseable status falls back to bytes", "git_status", `not json`, "8 B"},
		{"capped result is marked", "git_log", logLines + "\n...[truncated 4096 bytes]", "3 commits (capped)"},
		{"suggest names the command", "gk_suggest", `{"matches":[{"command":"gk branch clean"}]}`, "gk branch clean"},
		{"suggest counts extra commands", "gk_suggest", `{"matches":[{"command":"gk branch clean"},{"command":"gk branch list"}]}`, "gk branch clean +1"},
		{"suggest with no match", "gk_suggest", `{"matches":[],"note":"..."}`, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeToolResult(tc.tool, tc.content); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The arrow must land in the same column on every row or the transcript stops
// being scannable, which is the entire premise of the layout.
func TestToolLineColumnsAlign(t *testing.T) {
	calls := []struct {
		tool string
		raw  string
	}{
		{"git_log", `{"range":"main..develop","limit":30}`},
		{"git_status", `{}`},
		{"git_grep", `{"pattern":"IsError","paths":["internal/chat"]}`},
		{"file_read", `{"path":"internal/chat/tools/registry.go","start_line":1,"end_line":240}`},
		// CJK and a very long path both have to land on the same column.
		{"git_grep", `{"pattern":"브랜치 정리","paths":["internal/cli"]}`},
		{"file_read", `{"path":"internal/cli/some/very/deeply/nested/directory/file_with_long_name.go"}`},
	}
	want := -1
	for _, c := range calls {
		line := padToolCell(c.tool, toolNameWidth) + " " + padToolCell(summarizeToolArgs(c.tool, json.RawMessage(c.raw)), toolArgsWidth)
		w := runewidth.StringWidth(line)
		if want == -1 {
			want = w
		}
		if w != want {
			t.Errorf("tool %s: line width %d, want %d (%q)", c.tool, w, want, line)
		}
	}
	// The full row must still fit an 80-column terminal alongside the result.
	if want+runewidth.StringWidth(" → 12 matches · 4 files 0.11s")+4 > 80 {
		t.Errorf("row width %d leaves no room for the result column in 80 cols", want)
	}
}

func TestPadToolCellTruncatesWithEllipsis(t *testing.T) {
	got := padToolCell("internal/cli/a/very/long/path/name.go", 12)
	if runewidth.StringWidth(got) != 12 {
		t.Errorf("width = %d, want 12 (%q)", runewidth.StringWidth(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated cell must end with an ellipsis: %q", got)
	}
}

func TestFormatToolDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Millisecond, "0.03s"},
		{1500 * time.Millisecond, "1.50s"},
		{42 * time.Second, "42.0s"},
		{150 * time.Second, "150s"},
	}
	for _, tc := range cases {
		if got := formatToolDuration(tc.d); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestSummarizeContextResult(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		// The shape that produced "215 B" before this summary existed.
		{"synced branch with dirty tree",
			`{"branch":"develop","upstream":"origin/develop","ahead":0,"behind":0,"dirty":{"staged":0,"unstaged":4,"untracked":8,"conflicts":0}}`,
			"develop · ↑0 ↓0 · 12 dirty"},
		{"clean and synced",
			`{"branch":"main","upstream":"origin/main","ahead":0,"behind":0,"dirty":{}}`,
			"main · ↑0 ↓0"},
		{"diverged",
			`{"branch":"feat/x","upstream":"origin/feat/x","ahead":3,"behind":7,"dirty":{}}`,
			"feat/x · ↑3 ↓7"},
		// No upstream means no divergence to report — "↑0 ↓0" would claim a
		// sync with a remote branch that does not exist.
		{"no upstream omits divergence",
			`{"branch":"feat/new","dirty":{"unstaged":2}}`,
			"feat/new · 2 dirty"},
		{"detached head",
			`{"detached":true,"dirty":{}}`,
			"detached"},
		{"mid-rebase with conflicts",
			`{"branch":"develop","upstream":"origin/develop","in_progress":{"kind":"rebase"},"dirty":{"conflicts":2,"unstaged":2}}`,
			"develop · ↑0 ↓0 · rebase · 2 conflicts · 2 dirty"},
		{"unparseable falls back to bytes", `not json`, "8 B"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeContextResult(tc.content); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Every column is fixed-width, so the whole row must land on exactly 80.
func TestToolRowFitsEightyColumns(t *testing.T) {
	row := "  ▸ " + padToolCell("git_context", toolNameWidth) + " " +
		padToolCell(summarizeToolArgs("git_context", json.RawMessage(`{}`)), toolArgsWidth) + " → " +
		padToolCell("develop · ↑0 ↓0 · 12 dirty", toolResultWidth) + fmt.Sprintf(" %5s", formatToolDuration(time.Second))
	if w := runewidth.StringWidth(row); w != 80 {
		t.Errorf("row width = %d, want exactly 80: %q", w, row)
	}
}

// The result cell grows with the terminal but never shrinks below the
// 80-column floor — including when stdout is a pipe and GetSize fails, which
// is how this runs under `go test`.
func TestResultWidthFloor(t *testing.T) {
	if got := resultWidth(); got < toolResultWidth {
		t.Errorf("resultWidth() = %d, must never go below the %d floor", got, toolResultWidth)
	}
}

// toolFixedWidth must stay in sync with what onToolResult actually prints, or
// the computed result cell overflows the terminal and every row wraps.
func TestToolFixedWidthMatchesRendering(t *testing.T) {
	rendered := "  ▸ " + padToolCell("git_log", toolNameWidth) + " " +
		padToolCell("develop", toolArgsWidth) + " → " + " " + "0.01s"
	if w := runewidth.StringWidth(rendered); w != toolFixedWidth {
		t.Errorf("fixed part renders as %d columns, constant says %d", w, toolFixedWidth)
	}
}

func TestResultWidthHonorsColumns(t *testing.T) {
	t.Setenv("COLUMNS", "120")
	if got, want := resultWidth(), 120-toolFixedWidth; got != want {
		t.Errorf("resultWidth() = %d, want %d for a 120-column consumer", got, want)
	}
	// A terminal too narrow for the layout still gets the floor rather than a
	// negative or wrapping cell.
	t.Setenv("COLUMNS", "40")
	if got := resultWidth(); got != toolResultWidth {
		t.Errorf("narrow terminal: resultWidth() = %d, want the %d floor", got, toolResultWidth)
	}
}
