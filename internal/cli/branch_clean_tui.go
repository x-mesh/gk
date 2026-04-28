package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/branchclean"
	"github.com/x-mesh/gk/internal/ui"
)

// FormatCandidateLabel은 TUI에 표시할 브랜치 라벨을 생성한다.
// 형식: "[local]  branch-name  (created 5m ago, commit 5d ago)  [merged]  completed: 로그인 기능 구현"
// remote-only 브랜치는 [remote: origin] prefix가 붙는다.
func FormatCandidateLabel(c branchclean.CleanCandidate) string {
	var b strings.Builder
	if c.IsRemote {
		rn := c.RemoteName
		if rn == "" {
			rn = "origin"
		}
		fmt.Fprintf(&b, "[remote: %s] ", rn)
	} else {
		b.WriteString("[local]        ")
	}
	b.WriteString(c.Name)
	if t := formatBranchTimes(c.CreatedAt, c.LastCommitDate); t != "" {
		b.WriteString("  (")
		b.WriteString(t)
		b.WriteString(")")
	}
	b.WriteString("  [")
	b.WriteString(string(c.Status))
	b.WriteString("]")

	if c.AICategory != "" {
		fmt.Fprintf(&b, "  %s: %s", c.AICategory, c.AISummary)
	}
	return b.String()
}

// formatBranchTimes formats the (created, last commit) pair so the user
// can tell apart a freshly-branched-from-old-base case from a stale
// branch. Falls back gracefully when reflog is unavailable.
// relativeTime() already returns "Nd ago" / "today" — do not append "ago"
// here.
func formatBranchTimes(created, lastCommit time.Time) string {
	hasCreated := !created.IsZero()
	hasCommit := !lastCommit.IsZero()
	switch {
	case hasCreated && hasCommit:
		// Collapse when the two are within a minute — same wall-clock
		// event, so a single label is clearer than "created X ago, commit X ago".
		if d := created.Sub(lastCommit); d < time.Minute && d > -time.Minute {
			return relativeTime(time.Since(created))
		}
		return fmt.Sprintf("created %s, commit %s",
			relativeTime(time.Since(created)),
			relativeTime(time.Since(lastCommit)))
	case hasCreated:
		return "created " + relativeTime(time.Since(created))
	case hasCommit:
		return relativeTime(time.Since(lastCommit))
	}
	return ""
}

// relativeTime은 duration을 사람이 읽기 쉬운 상대 시간으로 변환한다.
func relativeTime(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days < 1:
		return "today"
	case days < 7:
		return fmt.Sprintf("%dd ago", days)
	case days < 30:
		return fmt.Sprintf("%dw ago", days/7)
	case days < 365:
		return fmt.Sprintf("%dm ago", days/30)
	default:
		return fmt.Sprintf("%dy ago", days/365)
	}
}

// CandidateKey returns a stable key that distinguishes a local branch
// from a remote-only branch with the same name (e.g. local "feat-x"
// vs origin's "feat-x"). Callers route deletion based on the prefix.
func CandidateKey(c branchclean.CleanCandidate) string {
	if c.IsRemote {
		rn := c.RemoteName
		if rn == "" {
			rn = "origin"
		}
		return "remote:" + rn + "/" + c.Name
	}
	return "local:" + c.Name
}

// RunCleanTUI presents a checkbox list of branch-clean candidates.
// Local-only candidates show by default; pressing 'i' toggles whether
// remote-only candidates are also listed. Returns the selected
// CandidateKeys — the caller must split them via ParseCandidateKey.
func RunCleanTUI(allCandidates []branchclean.CleanCandidate, startWithRemote bool) ([]string, error) {
	if len(allCandidates) == 0 {
		return nil, nil
	}
	includeRemote := startWithRemote

	build := func() ([]ui.MultiSelectItem, map[string]bool) {
		items := make([]ui.MultiSelectItem, 0, len(allCandidates))
		preselect := map[string]bool{}
		for _, c := range allCandidates {
			if c.IsRemote && !includeRemote {
				continue
			}
			k := CandidateKey(c)
			items = append(items, ui.MultiSelectItem{
				Key:     k,
				Display: FormatCandidateLabel(c),
			})
			if c.Selected {
				preselect[k] = true
			}
		}
		return items, preselect
	}

	items, preselect := build()
	extra := ui.MultiSelectExtraKey{
		Key:  "i",
		Help: "i toggle remote",
		OnPress: func() ([]ui.MultiSelectItem, map[string]bool, error) {
			includeRemote = !includeRemote
			its, pre := build()
			return its, pre, nil
		},
	}

	selected, err := ui.MultiSelectTUI(context.Background(), "select branches to delete", items, preselect, extra)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return nil, nil
		}
		return nil, err
	}
	return selected, nil
}

// ParseCandidateKey splits a CandidateKey produced by CandidateKey
// back into (name, isRemote, remoteName).
func ParseCandidateKey(key string) (name string, isRemote bool, remote string) {
	if rest, ok := strings.CutPrefix(key, "remote:"); ok {
		isRemote = true
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			remote = rest[:i]
			name = rest[i+1:]
			return
		}
		name = rest
		return
	}
	if rest, ok := strings.CutPrefix(key, "local:"); ok {
		name = rest
		return
	}
	name = key
	return
}
