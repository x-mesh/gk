package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// largeStageThresholdBytes flags any staged blob bigger than this.
// 5 MiB — large enough that legitimate binaries (icons, fonts) usually
// pass, but small enough that a stray dataset or build artifact gets
// caught before it reaches origin.
const largeStageThresholdBytes = 5 * 1024 * 1024

// checkStagedSize warns when the staged tree contains an unusually large
// blob — a common foot-gun (committing a build artifact, screenshot, or
// dataset). Reports up to three offenders so the table stays readable.
func checkStagedSize(ctx context.Context, runner git.Runner) doctorCheck {
	stdout, _, err := runner.Run(ctx, "diff", "--cached", "--numstat")
	if err != nil {
		return doctorCheck{Name: "repo: staged size", Status: statusPass, Detail: "—"}
	}
	type offender struct {
		Path  string
		Bytes int64
	}
	var big []offender
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		// numstat: "<added>\t<deleted>\t<path>" — for binaries both
		// columns are "-", so we re-query the on-disk size for those.
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		path := fields[2]
		size, err := stagedBlobSize(ctx, runner, path)
		if err != nil || size <= 0 {
			continue
		}
		if size >= largeStageThresholdBytes {
			big = append(big, offender{Path: path, Bytes: size})
		}
	}
	if len(big) == 0 {
		return doctorCheck{Name: "repo: staged size", Status: statusPass, Detail: "all staged blobs under 5 MiB"}
	}
	preview := make([]string, 0, len(big))
	for i, o := range big {
		if i == 3 {
			preview = append(preview, fmt.Sprintf("…+%d more", len(big)-3))
			break
		}
		preview = append(preview, fmt.Sprintf("%s (%s)", o.Path, humanSize(o.Bytes)))
	}
	return doctorCheck{
		Name:   "repo: staged size",
		Status: statusWarn,
		Detail: fmt.Sprintf("%d large blob(s) staged: %s", len(big), strings.Join(preview, ", ")),
		Fix:    "double-check before committing — `git restore --staged <path>` to unstage",
	}
}

func stagedBlobSize(ctx context.Context, runner git.Runner, path string) (int64, error) {
	out, _, err := runner.Run(ctx, "cat-file", "-s", ":"+path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// checkGitignore flags repos that don't have a .gitignore at all.
// Most language ecosystems pollute the working tree with build/cache
// directories; without .gitignore those end up tracked or constantly
// noise the status output.
func checkGitignore(repoDir string) doctorCheck {
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".gitignore")); err == nil {
		return doctorCheck{Name: "repo: .gitignore", Status: statusPass, Detail: "present"}
	}
	return doctorCheck{
		Name:   "repo: .gitignore",
		Status: statusWarn,
		Detail: "no .gitignore in repo root",
		Fix:    "`gk init` or hand-write one — language presets at https://github.com/github/gitignore",
	}
}

// untrackedNoiseThreshold is the number of untracked entries above
// which we suspect a missing .gitignore rule. Tuned to "uncomfortable
// to scroll past" rather than "definitely wrong".
const untrackedNoiseThreshold = 30

// checkUntrackedNoise flags repos with a *lot* of untracked files —
// usually means a missing ignore rule (build dir, virtualenv, .DS_Store
// soup). When the entries match known toolchain output (the same table
// `gk commit` uses to shield its AI classify), the finding names the
// dirs and points at the scaffolder — the space-mesh incident (2,475
// SwiftPM files under app/.build) should be one doctor run away from its
// fix, not a commit-time surprise. Soft warning; never FAIL.
func checkUntrackedNoise(ctx context.Context, runner git.Runner) doctorCheck {
	out, _, err := runner.Run(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return doctorCheck{Name: "repo: untracked count", Status: statusPass, Detail: "—"}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	count, noise := 0, 0
	noiseBy := map[string]int{}
	for _, l := range lines {
		if l == "" {
			continue
		}
		count++
		if label := noiseLabel(l); label != "" {
			noise++
			noiseBy[label]++
		}
	}
	if count < untrackedNoiseThreshold {
		return doctorCheck{Name: "repo: untracked count", Status: statusPass, Detail: fmt.Sprintf("%d untracked", count)}
	}
	if noise > 0 {
		return doctorCheck{
			Name:   "repo: untracked count",
			Status: statusWarn,
			Detail: fmt.Sprintf("%d untracked entries, %d look like toolchain output (%s)", count, noise, formatNoiseBreakdown(noiseBy)),
			Fix:    "`gk init --only gitignore` scaffolds the detected languages' build-output patterns",
		}
	}
	return doctorCheck{
		Name:   "repo: untracked count",
		Status: statusWarn,
		Detail: fmt.Sprintf("%d untracked entries — likely missing .gitignore rules", count),
		Fix:    "review with `git status` and add patterns to .gitignore",
	}
}

// noiseLabel names the noise component of a path (the matching directory,
// junk filename, or extension glob), or "" for a clean path. Same tables
// as isNoisePath — one vocabulary for commit shielding and doctor.
func noiseLabel(p string) string {
	p = filepath.ToSlash(p)
	for _, c := range strings.Split(p, "/") {
		if noiseDirs[c] {
			return c + "/"
		}
	}
	base := filepath.Base(p)
	if noiseNames[base] {
		return base
	}
	if ext := strings.ToLower(filepath.Ext(base)); noiseExts[ext] {
		return "*" + ext
	}
	return ""
}

// formatNoiseBreakdown renders the top three noise labels as
// ".build/ x2465, .DS_Store x5" — most frequent first, ties by name —
// with a "+N more" tail when further labels were cut.
func formatNoiseBreakdown(by map[string]int) string {
	labels := make([]string, 0, len(by))
	for l := range by {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool {
		if by[labels[i]] != by[labels[j]] {
			return by[labels[i]] > by[labels[j]]
		}
		return labels[i] < labels[j]
	})
	more := 0
	if len(labels) > 3 {
		more = len(labels) - 3
		labels = labels[:3]
	}
	parts := make([]string, len(labels))
	for i, l := range labels {
		parts[i] = fmt.Sprintf("%s x%d", l, by[l])
	}
	if more > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", more))
	}
	return strings.Join(parts, ", ")
}

// stashBacklogThreshold flags the user when stashes pile up — easy to
// forget about a stash, lose track of which one held what.
const stashBacklogThreshold = 5

// checkStashBacklog reports how many stashes are currently held. PASS
// for empty / small list; WARN past the threshold so users notice.
func checkStashBacklog(ctx context.Context, runner git.Runner) doctorCheck {
	out, _, err := runner.Run(ctx, "stash", "list")
	if err != nil {
		return doctorCheck{Name: "repo: stash backlog", Status: statusPass, Detail: "—"}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	count := 0
	for _, l := range lines {
		if l != "" {
			count++
		}
	}
	switch {
	case count == 0:
		return doctorCheck{Name: "repo: stash backlog", Status: statusPass, Detail: "no stashes"}
	case count < stashBacklogThreshold:
		return doctorCheck{Name: "repo: stash backlog", Status: statusPass, Detail: fmt.Sprintf("%d stash(es)", count)}
	default:
		return doctorCheck{
			Name:   "repo: stash backlog",
			Status: statusWarn,
			Detail: fmt.Sprintf("%d stashes — old ones get forgotten", count),
			Fix:    "browse with `gk stash` and apply / drop entries you don't need",
		}
	}
}

// branchTrackingMaxBranches caps how many divergent untracked branches the
// detail line lists before collapsing into "+N more"; keeps the doctor
// table from blowing out on repos with many local branches.
const branchTrackingMaxBranches = 3

// checkBranchTracking flags local branches whose @{upstream} is unset but
// whose same-named remote ref (e.g., origin/main) exists and differs.
// Pure read of cached refs — no network. Branches without a same-named
// remote ref (fork/personal) are intentionally ignored to avoid false
// positives on forks where divergence is the expected steady state.
func checkBranchTracking(ctx context.Context, runner git.Runner, remote string) doctorCheck {
	if remote == "" {
		remote = "origin"
	}
	offenders := scanUntrackedDivergent(ctx, runner, remote)
	if len(offenders) == 0 {
		return doctorCheck{Name: "repo: branch tracking", Status: statusPass, Detail: "all tracked or in sync with same-named remote"}
	}

	previews := make([]string, 0, len(offenders))
	for i, o := range offenders {
		if i == branchTrackingMaxBranches {
			previews = append(previews, fmt.Sprintf("+%d more", len(offenders)-branchTrackingMaxBranches))
			break
		}
		previews = append(previews, fmt.Sprintf("%s (↑%d ↓%d → %s)", o.Branch, o.Ahead, o.Behind, o.Implicit))
	}
	first := offenders[0]
	return doctorCheck{
		Name:   "repo: branch tracking",
		Status: statusWarn,
		Detail: fmt.Sprintf("%d untracked branch(es) diverge from %s: %s", len(offenders), remote, strings.Join(previews, ", ")),
		Fix:    fmt.Sprintf("git branch --set-upstream-to=%s %s", first.Implicit, first.Branch),
	}
}

// humanSize renders a byte count with at most one decimal place using
// binary prefixes (KiB/MiB/GiB) — same convention as `du -h`.
func humanSize(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for v := n / u; v >= u; v /= u {
		div *= u
		exp++
	}
	suffix := []string{"KiB", "MiB", "GiB", "TiB"}[exp]
	val := float64(n) / float64(div)
	if val >= 10 {
		return fmt.Sprintf("%.0f%s", val, suffix)
	}
	return fmt.Sprintf("%.1f%s", val, suffix)
}
