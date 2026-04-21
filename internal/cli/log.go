package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
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
	rootCmd.AddCommand(cmd)
}

func runLog(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())

	since, _ := cmd.Flags().GetString("since")
	format, _ := cmd.Flags().GetString("format")
	graph, _ := cmd.Flags().GetBool("graph")
	limit, _ := cmd.Flags().GetInt("limit")

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
		gitArgs = append(gitArgs, "-z", "--pretty=format:"+jsonLogFormat, "--date=iso-strict")
	} else {
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
	_, _ = cmd.OutOrStdout().Write(stdout)
	if len(stdout) > 0 && !strings.HasSuffix(string(stdout), "\n") {
		fmt.Fprintln(cmd.OutOrStdout())
	}
	return nil
}

const (
	defaultLogFormat = "%C(yellow)%h%C(reset) %C(green)(%ar)%C(reset) %C(bold blue)<%an>%C(reset) %s%C(auto)%d%C(reset)"
	// JSON 모드는 %x00 구분자로 레코드, 필드. 파이프로 디코드하기 위해 레코드는 %x1e (RS).
	jsonLogFormat = "%H%x00%h%x00%an%x00%ae%x00%aI%x00%s%x00%b%x1e"
)

var shortSinceRE = regexp.MustCompile(`^(\d+)\s*([smhdwMy])$`)

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
