package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:     "log [revisions] [-- <path>...]",
		Aliases: []string{"slog"},
		Short:   "Show short, colorful commit log",
		RunE:    runLog,
	}
	cmd.Flags().String("since", "", "show commits since this time (e.g. 1w, 2d, \"last monday\")")
	cmd.Flags().String("format", "", "git pretty-format string (overrides config)")
	cmd.Flags().Bool("graph", false, "include topology graph")
	cmd.Flags().IntP("limit", "n", 0, "max number of commits (0 = unlimited)")
	cmd.Flags().Bool("pulse", false, "print commit-rhythm sparkline above the log")
	rootCmd.AddCommand(cmd)
}

func runLog(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())

	since, _ := cmd.Flags().GetString("since")
	format, _ := cmd.Flags().GetString("format")
	graph, _ := cmd.Flags().GetBool("graph")
	limit, _ := cmd.Flags().GetInt("limit")
	pulse, _ := cmd.Flags().GetBool("pulse")

	if format == "" {
		format = cfg.Log.Format
	}
	if format == "" {
		format = defaultLogFormat
	}
	if limit == 0 {
		limit = cfg.Log.Limit
	}
	if !graph {
		graph = cfg.Log.Graph
	}

	gitArgs := []string{"log"}
	if JSONOut() {
		// JSON 모드는 필드를 NUL로 나눠 안전 파싱
		gitArgs = append(gitArgs, "-z", "--pretty=format:"+jsonLogFormat, "--date=iso-strict", "--color=never")
	} else {
		// git은 stdout이 파이프일 때 %C(...) 포맷 코드를 스트립한다.
		// 우리는 버퍼로 캡처하므로 최종 출력 대상이 TTY이면 명시적으로 색상을 강제한다.
		if logUseColor() {
			gitArgs = append(gitArgs, "--color=always")
		} else {
			gitArgs = append(gitArgs, "--color=never")
		}
		gitArgs = append(gitArgs, "--pretty=format:"+format)
		if graph {
			gitArgs = append(gitArgs, "--graph", "--decorate", "--topo-order", "--abbrev-commit")
		} else {
			gitArgs = append(gitArgs, "--decorate", "--abbrev-commit")
		}
	}
	if limit > 0 {
		gitArgs = append(gitArgs, "-n", strconv.Itoa(limit))
	}
	if since != "" {
		sinceNorm := normalizeSince(since)
		gitArgs = append(gitArgs, "--since="+sinceNorm)
	}
	gitArgs = append(gitArgs, args...)

	runner := &git.ExecRunner{Dir: RepoFlag()}
	stdout, stderr, err := runner.Run(cmd.Context(), gitArgs...)
	if err != nil {
		return fmt.Errorf("git log failed: %s: %w", strings.TrimSpace(string(stderr)), err)
	}

	if JSONOut() {
		return writeJSONLog(cmd.OutOrStdout(), stdout)
	}
	if pulse && !JSONOut() {
		if line := renderPulse(cmd.Context(), runner, since, args); line != "" {
			fmt.Fprintln(cmd.OutOrStdout(), line)
		}
	}
	_, _ = cmd.OutOrStdout().Write(stdout)
	if len(stdout) > 0 && !strings.HasSuffix(string(stdout), "\n") {
		fmt.Fprintln(cmd.OutOrStdout())
	}
	return nil
}

// renderPulse queries the same revision scope used for the log and aggregates
// commit days into a compact sparkline. Returns empty string when git fails
// or there are no commits in the window.
//
//	pulse 2w ▁▁▂▅█▇▃▁▁▂▄▆█▅  (128 commits, peak Tue)
func renderPulse(ctx context.Context, runner *git.ExecRunner, since string, pathArgs []string) string {
	args := []string{"log", "--format=%cI"}
	if since != "" {
		args = append(args, "--since="+normalizeSince(since))
	}
	args = append(args, pathArgs...)
	out, _, err := runner.Run(ctx, args...)
	if err != nil || len(out) == 0 {
		return ""
	}
	dates := make([]time.Time, 0, 128)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, line)
		if err != nil {
			continue
		}
		dates = append(dates, t)
	}
	if len(dates) == 0 {
		return ""
	}
	return pulseLine(dates, since)
}

var pulseGlyphs = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// pulseLine is the pure-function core of --pulse: given commit timestamps and
// the --since label, it produces a single-line sparkline with a
// "(N commits, peak Weekday)" suffix. Zero-activity days render as '·'.
func pulseLine(dates []time.Time, since string) string {
	if len(dates) == 0 {
		return ""
	}
	minT, maxT := dates[0], dates[0]
	for _, d := range dates {
		if d.Before(minT) {
			minT = d
		}
		if d.After(maxT) {
			maxT = d
		}
	}
	startDay := time.Date(minT.Year(), minT.Month(), minT.Day(), 0, 0, 0, 0, minT.Location())
	endDay := time.Date(maxT.Year(), maxT.Month(), maxT.Day(), 0, 0, 0, 0, maxT.Location())
	days := int(endDay.Sub(startDay).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	if days > 180 {
		days = 180
	}

	buckets := make([]int, days)
	for _, d := range dates {
		day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
		idx := int(day.Sub(startDay).Hours() / 24)
		if idx < 0 || idx >= days {
			continue
		}
		buckets[idx]++
	}

	peakIdx, peakVal := 0, 0
	for i, v := range buckets {
		if v > peakVal {
			peakIdx, peakVal = i, v
		}
	}

	var spark strings.Builder
	for _, v := range buckets {
		if v == 0 {
			spark.WriteRune('·')
			continue
		}
		idx := 0
		if peakVal > 0 {
			idx = (v - 1) * (len(pulseGlyphs) - 1) / maxInt(peakVal-1, 1)
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= len(pulseGlyphs) {
			idx = len(pulseGlyphs) - 1
		}
		spark.WriteRune(pulseGlyphs[idx])
	}

	label := since
	if label == "" {
		label = fmt.Sprintf("%dd", days)
	}
	peakDay := startDay.Add(time.Duration(peakIdx) * 24 * time.Hour)
	faint := color.New(color.Faint).SprintFunc()
	return fmt.Sprintf("%s %s %s  %s",
		faint("pulse"),
		faint(label),
		color.CyanString(spark.String()),
		faint(fmt.Sprintf("(%d commits, peak %s)", len(dates), peakDay.Format("Mon"))),
	)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const (
	defaultLogFormat = "%C(yellow)%h%C(reset) %C(green)(%ar)%C(reset) %C(bold blue)<%an>%C(reset) %s%C(auto)%d%C(reset)"
	// JSON 모드는 %x00 구분자로 레코드, 필드. 파이프로 디코드하기 위해 레코드는 %x1e (RS).
	jsonLogFormat = "%H%x00%h%x00%an%x00%ae%x00%aI%x00%s%x00%b%x1e"
)

var shortSinceRE = regexp.MustCompile(`^(\d+)\s*([smhdwMy])$`)

// logUseColor decides whether git log output should include ANSI color codes.
// Order: --no-color flag → NO_COLOR env → stdout TTY check.
func logUseColor() bool {
	if NoColorFlag() {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return ui.IsTerminal()
}

// normalizeSince converts short forms like "1w", "3d" into git-friendly strings.
// Everything else is passed through unchanged.
func normalizeSince(s string) string {
	m := shortSinceRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return s
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "s":
		return fmt.Sprintf("%d.seconds.ago", n)
	case "m":
		return fmt.Sprintf("%d.minutes.ago", n)
	case "h":
		return fmt.Sprintf("%d.hours.ago", n)
	case "d":
		return fmt.Sprintf("%d.days.ago", n)
	case "w":
		return fmt.Sprintf("%d.weeks.ago", n)
	case "M":
		return fmt.Sprintf("%d.months.ago", n)
	case "y":
		return fmt.Sprintf("%d.years.ago", n)
	}
	return s
}

// LogEntry represents a single commit in JSON output mode.
type LogEntry struct {
	SHA      string `json:"sha"`
	ShortSHA string `json:"short_sha"`
	Author   string `json:"author"`
	Email    string `json:"email"`
	Date     string `json:"date"`
	Subject  string `json:"subject"`
	Body     string `json:"body,omitempty"`
}

func writeJSONLog(w io.Writer, raw []byte) error {
	entries := parseJSONLog(raw)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

// parseJSONLog splits raw output on %x1e (record sep) and %x00 (field sep).
func parseJSONLog(raw []byte) []LogEntry {
	records := strings.Split(strings.TrimRight(string(raw), "\x1e\n"), "\x1e")
	out := make([]LogEntry, 0, len(records))
	for _, rec := range records {
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "\x00")
		if len(fields) < 7 {
			continue
		}
		out = append(out, LogEntry{
			SHA:      fields[0],
			ShortSHA: fields[1],
			Author:   fields[2],
			Email:    fields[3],
			Date:     fields[4],
			Subject:  fields[5],
			Body:     fields[6],
		})
	}
	return out
}
