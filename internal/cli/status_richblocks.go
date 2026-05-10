package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// renderDivergenceDiagram returns a small ASCII branch-divergence
// diagram suitable for the rich-mode `divergence` section. The shape
// shows how many commits each side has diverged from the merge base
// — emitted only when there is actual divergence to draw, since
// `↑0 ↓0` is just two empty rays. The merge-base SHA is fetched once;
// errors collapse the function to an empty result rather than emitting
// a half-rendered section.
//
//	   o─o─o  ↑3 you
//	  /
//	──●  merge-base 86d3aac
//	  \
//	   o─o    ↓2 origin
//
// `ahead` is the number of local-only commits and `behind` is the
// number of upstream-only commits. Either side may be zero (renders an
// empty ray with a 0 label) but at least one must be non-zero, else
// the caller short-circuits.
func renderDivergenceDiagram(ctx context.Context, runner *git.ExecRunner, upstream string, ahead, behind int) []string {
	if upstream == "" || (ahead == 0 && behind == 0) {
		return nil
	}
	out, _, err := runner.Run(ctx, "merge-base", "HEAD", upstream)
	mergeBase := ""
	if err == nil {
		mergeBase = shortSHA(strings.TrimSpace(string(out)))
	}

	row := func(n int) string {
		if n <= 0 {
			return "(none)"
		}
		// Cap the visual at 6 nodes; beyond that the count carries
		// the load. "o─o─o─o─o─o…" stays single-cell per glyph.
		const maxNodes = 6
		visible := n
		if visible > maxNodes {
			visible = maxNodes
		}
		nodes := make([]string, visible)
		for i := range nodes {
			nodes[i] = "o"
		}
		s := strings.Join(nodes, "─")
		if n > maxNodes {
			s += "…"
		}
		return s
	}

	mbLine := "──●"
	if mergeBase != "" {
		mbLine += "  merge-base " + mergeBase
	}

	return []string{
		fmt.Sprintf("   %s   ↑%d you", row(ahead), ahead),
		"  /",
		mbLine,
		"  \\",
		fmt.Sprintf("   %s   ↓%d %s", row(behind), behind, displayRemoteSide(upstream)),
	}
}

// displayRemoteSide returns the trailing remote-side label for the
// divergence diagram. Most users see `origin` here; preserving the
// upstream's leading remote name keeps the diagram accurate when the
// branch tracks a non-default remote (`upstream/main`, etc.).
func displayRemoteSide(upstream string) string {
	if i := strings.IndexByte(upstream, '/'); i > 0 {
		return upstream[:i]
	}
	return "remote"
}

// renderActivityHeatmap returns the 7-day commit-sparkline block for
// the rich-mode `activity 7d` section. The heatmap counts commits per
// local day (Mon..Sun, today on the right edge) and scales them to an
// 8-cell glyph ramp. The total commit count is returned separately so
// the caller can hoist it into the section's summary slot rather than
// repeating the magnitude inside the body.
//
//	▁ ▂ ▅ █ ▃ ▁ ▂        (body[0], sparkline)
//	M T W T F S S        (body[1], day labels)
//	23                   (total, hoisted to summary)
//
// On `git log` failure the block is omitted entirely (ok=false); the
// rich layout is informational and a missing sparkline is preferable
// to a noisy half-rendered one.
func renderActivityHeatmap(ctx context.Context, runner *git.ExecRunner) (lines []string, total int, ok bool) {
	out, _, err := runner.Run(ctx, "log",
		"--since=7.days.ago",
		"--no-merges",
		"--pretty=format:%cd",
		"--date=unix",
	)
	if err != nil {
		return nil, 0, false
	}
	now := time.Now()
	// `today` index 6, `today-6` index 0 — the most recent day sits
	// on the right edge so the eye lands on "now" first.
	counts := make([]int, 7)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		secs, perr := strconv.ParseInt(line, 10, 64)
		if perr != nil {
			continue
		}
		t := time.Unix(secs, 0)
		days := int(now.Sub(t).Hours() / 24)
		if days < 0 || days > 6 {
			continue
		}
		idx := 6 - days
		counts[idx]++
		total++
	}
	if total == 0 {
		return []string{
			"▁ ▁ ▁ ▁ ▁ ▁ ▁",
			heatmapDayLabels(now),
		}, 0, true
	}
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	ramp := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	cells := make([]string, 7)
	for i, c := range counts {
		var idx int
		switch {
		case c == 0:
			idx = 0
		case max <= 1:
			idx = len(ramp) - 1
		default:
			idx = int(float64(c) / float64(max) * float64(len(ramp)-1))
			if idx < 1 {
				idx = 1
			}
			if idx >= len(ramp) {
				idx = len(ramp) - 1
			}
		}
		cells[i] = string(ramp[idx])
	}
	return []string{
		strings.Join(cells, " "),
		heatmapDayLabels(now),
	}, total, true
}

// heatmapDayLabels returns the 7-character day-of-week strip aligned
// to the same columns as the sparkline cells. Today's column is
// rightmost; the strip uses single-letter labels so it fits inside
// any reasonable TTY width without wrapping.
func heatmapDayLabels(now time.Time) string {
	labels := make([]string, 7)
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, -(6 - i))
		switch d.Weekday() {
		case time.Sunday:
			labels[i] = "S"
		case time.Monday:
			labels[i] = "M"
		case time.Tuesday:
			labels[i] = "T"
		case time.Wednesday:
			labels[i] = "W"
		case time.Thursday:
			labels[i] = "T"
		case time.Friday:
			labels[i] = "F"
		case time.Saturday:
			labels[i] = "S"
		}
	}
	return strings.Join(labels, " ")
}
