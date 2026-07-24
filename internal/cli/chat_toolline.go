package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"os"

	"github.com/mattn/go-runewidth"
	"github.com/x-mesh/gk/internal/chat/tools"
	"golang.org/x/term"
)

// Column widths for the tool transparency line. The line is a table read
// vertically down a scrolling transcript, so the arrow has to land in the same
// column every time — that alignment is what makes "three git_log calls all
// returned none" visible at a glance instead of something you have to read for.
//
// "  ▸ " + name + " " + args + " → " + result + " " + duration
//
//	4  +  12  + 1 +  32  +  3  +   22   + 1 +    5     = 80 columns exactly,
//
// the narrowest terminal worth designing for. Errors reuse the result column,
// so a failing row is exactly as wide as a succeeding one.
const (
	toolNameWidth   = 12
	toolArgsWidth   = 32
	toolResultWidth = 22
	// toolFixedWidth is everything on the row EXCEPT the result cell: the
	// indent, both separators, the name and args cells, and the duration.
	toolFixedWidth = 4 + toolNameWidth + 1 + toolArgsWidth + 3 + 1 + 5
)

// resultWidth gives the result cell whatever the terminal has left over.
// The 80-column layout is a floor, not a target: at 22 columns real summaries
// clip mid-word ("4 changed · 8 untrack…"), which costs exactly the
// information the cell was added to show. A wide window has the room, and a
// narrow one (or a pipe, where GetSize fails) still gets the fixed layout.
func resultWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		// Piped or redirected output has no size to query. COLUMNS is the
		// conventional way to say how wide the consumer is anyway, and it is
		// what makes this width observable in a test.
		w, _ = strconv.Atoi(os.Getenv("COLUMNS"))
	}
	if avail := w - toolFixedWidth; avail > toolResultWidth {
		return avail
	}
	return toolResultWidth
}

// summarizeToolArgs renders a tool call's arguments as the phrase a person
// would use to describe the lookup — "develop · 5", not the JSON that produced
// it. Unknown tools and unparseable input fall back to compact JSON, so a tool
// added later degrades to today's behaviour instead of showing nothing.
//
// The input is unwrapped first because this runs BEFORE Dispatch: an
// openai-style {recipient_name, parameters} envelope would otherwise leak its
// packaging into the UI, which is exactly the noise this line exists to remove.
func summarizeToolArgs(name string, raw json.RawMessage) string {
	if unwrapped, err := tools.UnwrapEnvelope(name, raw); err == nil {
		raw = unwrapped
	}
	// An empty object still goes through the switch: "{}" means different
	// things per tool (file_list defaults to the repo root, git_context takes
	// nothing at all), and only the switch knows which.
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		if s := compactJSON(raw, toolArgsWidth); s != "{}" {
			return s
		}
		return ""
	}

	str := func(k string) string {
		s, _ := in[k].(string)
		return strings.TrimSpace(s)
	}
	num := func(k string) int {
		f, ok := in[k].(float64)
		if !ok {
			return 0
		}
		return int(f)
	}
	// paths and path are two spellings of the same argument (mergePathArg).
	pathList := func() string {
		if p := str("path"); p != "" {
			return p
		}
		raw, _ := in["paths"].([]any)
		var out []string
		for _, v := range raw {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return ""
		}
		if len(out) == 1 {
			return out[0]
		}
		return fmt.Sprintf("%s +%d", out[0], len(out)-1)
	}
	lineSpan := func(p string) string {
		start, end := num("start_line"), num("end_line")
		switch {
		case start > 0 && end > 0:
			return fmt.Sprintf("%s:%d-%d", p, start, end)
		case start > 0:
			return fmt.Sprintf("%s:%d-", p, start)
		default:
			return p
		}
	}

	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}

	switch name {
	case "git_log":
		// An absent range means HEAD; saying so beats an empty cell, because
		// "which line asked about main..develop" is the question being scanned.
		if r := str("range"); r != "" {
			add(r)
		} else {
			add("HEAD")
		}
		add(pathList())
		if a := str("author"); a != "" {
			add("@" + a)
		}
		add(str("since"))
		if n := num("limit"); n > 0 {
			add(strconv.Itoa(n))
		}
	case "git_show":
		add(str("ref"))
		add(pathList())
	case "git_diff":
		if b, _ := in["staged"].(bool); b {
			add("staged")
		}
		add(str("range"))
		add(pathList())
	case "git_blame":
		add(lineSpan(pathList()))
	case "git_grep":
		add(quoted(str("pattern")))
		add(pathList())
	case "file_read":
		add(lineSpan(str("path")))
	case "file_list":
		if p := str("path"); p != "" {
			add(p)
		} else {
			add(".")
		}
	case "gk_suggest":
		add(quoted(str("intent")))
	case "git_status", "git_context", "git_snapshot_list":
		if n := num("limit"); n > 0 {
			add(strconv.Itoa(n))
		}
	default:
		return compactJSON(raw, toolArgsWidth)
	}
	return strings.Join(parts, " · ")
}

// summarizeToolResult reports the SIZE of what came back. Every tool returns
// plain text (git's own output), so line count plus a per-tool noun covers
// almost everything, and the one case worth spending more on is emptiness:
// "none" is the signal that a lookup found nothing, which is invisible in a
// truncated JSON dump and is exactly what reveals a model circling the same
// dead end.
func summarizeToolResult(name, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "none"
	}
	lines := strings.Split(trimmed, "\n")
	truncated := strings.Contains(content, "[truncated ")

	suffix := ""
	if truncated {
		suffix = " (capped)"
	}

	switch name {
	case "git_log":
		return toolPlural(len(lines), "commit") + suffix
	case "git_grep":
		// "12 matches · 4 files" separates "one file mentions it a lot" from
		// "it is everywhere" — the distinction that decides where to read next.
		files := map[string]bool{}
		for _, ln := range lines {
			if i := strings.Index(ln, ":"); i > 0 {
				files[ln[:i]] = true
			}
		}
		if len(files) > 1 {
			return fmt.Sprintf("%s · %s%s", toolPlural(len(lines), "match"), toolPlural(len(files), "file"), suffix)
		}
		return toolPlural(len(lines), "match") + suffix
	case "file_read", "git_blame", "git_show", "git_diff":
		return toolPlural(len(lines), "line") + suffix
	case "file_list":
		return toolPlural(len(lines), "file") + suffix
	case "gk_suggest":
		return summarizeSuggestResult(trimmed) + suffix
	case "git_status":
		return summarizeStatusResult(trimmed) + suffix
	case "git_context":
		return summarizeContextResult(trimmed) + suffix
	case "git_snapshot_list", "git_snapshot_diff":
		// These return JSON; a line count would describe the formatting, not
		// the content, so report size in bytes instead of a misleading unit.
		return fmt.Sprintf("%d B%s", len(trimmed), suffix)
	default:
		return toolPlural(len(lines), "line") + suffix
	}
}

// summarizeSuggestResult names the command that was actually found — the one
// piece of tool output the user is likely to type next.
func summarizeSuggestResult(content string) string {
	var res struct {
		Matches []struct {
			Command string `json:"command"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(content), &res); err != nil {
		return "?"
	}
	if len(res.Matches) == 0 {
		return "none"
	}
	if len(res.Matches) == 1 {
		return res.Matches[0].Command
	}
	return fmt.Sprintf("%s +%d", res.Matches[0].Command, len(res.Matches)-1)
}

// summarizeStatusResult reports the working tree the way the answer will
// describe it. A byte count would be technically true and useless here: the
// point of the row is whether the tree is clean, and if not, how much is
// outstanding.
func summarizeStatusResult(content string) string {
	var st struct {
		Clean          bool `json:"clean"`
		UntrackedCount int  `json:"untracked_count"`
		Conflict       bool `json:"conflict"`
		Changed        []struct {
			Code string `json:"code"`
		} `json:"changed"`
		InProgress *struct {
			Kind string `json:"kind"`
		} `json:"in_progress"`
	}
	if err := json.Unmarshal([]byte(content), &st); err != nil {
		return fmt.Sprintf("%d B", len(content))
	}

	var parts []string
	// An interrupted rebase/merge outranks the counts: it is the one status
	// fact that changes what the user should do next.
	if st.InProgress != nil && st.InProgress.Kind != "" {
		parts = append(parts, st.InProgress.Kind)
	}
	if st.Conflict {
		parts = append(parts, "conflicts")
	}
	if n := len(st.Changed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", n))
	}
	if st.UntrackedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", st.UntrackedCount))
	}
	if len(parts) == 0 {
		if st.Clean {
			return "clean"
		}
		return "none"
	}
	return strings.Join(parts, " · ")
}

// summarizeContextResult renders the orientation snapshot as the one-line
// branch state a person would say out loud: "develop ↑0 ↓0 · 12 dirty".
//
// This row earns the most from a real summary of any tool, because the answer
// it feeds is usually a direct restatement of it ("is this branch stale?" is
// answered by ahead/behind). Reporting its byte count instead put the least
// information on screen exactly where the most was available.
func summarizeContextResult(content string) string {
	var cx struct {
		Branch   string `json:"branch"`
		Detached bool   `json:"detached"`
		Upstream string `json:"upstream"`
		Ahead    int    `json:"ahead"`
		Behind   int    `json:"behind"`
		Dirty    struct {
			Staged    int `json:"staged"`
			Unstaged  int `json:"unstaged"`
			Untracked int `json:"untracked"`
			Conflicts int `json:"conflicts"`
		} `json:"dirty"`
		InProgress *struct {
			Kind string `json:"kind"`
		} `json:"in_progress"`
	}
	if err := json.Unmarshal([]byte(content), &cx); err != nil {
		return fmt.Sprintf("%d B", len(content))
	}

	var parts []string
	switch {
	case cx.Detached:
		parts = append(parts, "detached")
	case cx.Branch != "":
		parts = append(parts, cx.Branch)
	}
	// Only show the divergence when there IS an upstream: "↑0 ↓0" on a branch
	// that was never pushed claims a sync that does not exist.
	if cx.Upstream != "" {
		parts = append(parts, fmt.Sprintf("↑%d ↓%d", cx.Ahead, cx.Behind))
	}
	if cx.InProgress != nil && cx.InProgress.Kind != "" {
		parts = append(parts, cx.InProgress.Kind)
	}
	if cx.Dirty.Conflicts > 0 {
		parts = append(parts, fmt.Sprintf("%d conflicts", cx.Dirty.Conflicts))
	}
	// One "dirty" count rather than three: the row answers "is there
	// uncommitted work", and the staged/unstaged/untracked split is what
	// git_status is for.
	if n := cx.Dirty.Staged + cx.Dirty.Unstaged + cx.Dirty.Untracked; n > 0 {
		parts = append(parts, fmt.Sprintf("%d dirty", n))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d B", len(content))
	}
	return strings.Join(parts, " · ")
}

// formatToolDuration keeps the column narrow: sub-second calls (nearly all of
// them) get two decimals, anything slower drops to one so a slow call stays
// the same width as a fast one.
func formatToolDuration(d time.Duration) string {
	switch s := d.Seconds(); {
	case s < 10:
		return fmt.Sprintf("%.2fs", s)
	case s < 100:
		return fmt.Sprintf("%.1fs", s)
	default:
		return fmt.Sprintf("%.0fs", s)
	}
}

func quoted(s string) string {
	if s == "" {
		return ""
	}
	return `"` + s + `"`
}

// toolPlural pluralizes the handful of nouns this line uses. "match" needs
// -es, which a bare +"s" gets wrong ("3 matchs") — a small thing that reads as
// sloppiness on every single grep call.
func toolPlural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	suffix := "s"
	if strings.HasSuffix(noun, "s") || strings.HasSuffix(noun, "x") ||
		strings.HasSuffix(noun, "ch") || strings.HasSuffix(noun, "sh") {
		suffix = "es"
	}
	return strconv.Itoa(n) + " " + noun + suffix
}

// padToolCell pads s to width display columns, or truncates it with an ellipsis
// when it overflows. runewidth is required rather than len: CJK path segments
// and the "·" separator are not one column wide, and getting that wrong tilts
// every row below it.
func padToolCell(s string, width int) string {
	w := runewidth.StringWidth(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return runewidth.Truncate(s, width, "…")
}
