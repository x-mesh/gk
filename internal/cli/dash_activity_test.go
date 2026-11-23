package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestDashDayRanges(t *testing.T) {
	kst := time.FixedZone("KST", 9*3600)
	// 00:30 local on the 23rd is still the 22nd in UTC: the days are local.
	days := dashDayRanges(time.Date(2026, 11, 23, 0, 30, 0, 0, kst), 3)
	if len(days) != 3 {
		t.Fatalf("days = %v", days)
	}
	want := []string{"2026-11-21T00:00:00+09:00 2026-11-21T23:59:59+09:00", "2026-11-22T00:00:00+09:00 2026-11-22T23:59:59+09:00", "2026-11-23T00:00:00+09:00 2026-11-23T23:59:59+09:00"}
	for i, d := range days {
		if got := d.Start.Format(time.RFC3339) + " " + d.End.Format(time.RFC3339); got != want[i] {
			t.Errorf("day %d = %s\nwant    %s", i, got, want[i])
		}
	}
	// The window crosses a month.
	if first := dashDayRanges(time.Date(2026, 12, 2, 9, 0, 0, 0, kst), 7)[0].Start; first.Format("2006-01-02") != "2026-11-26" {
		t.Errorf("first day = %s, want 2026-11-26", first.Format("2006-01-02"))
	}
}

func TestDashSpark(t *testing.T) {
	for in, want := range map[string]string{
		"0 0 0":            "···",
		"1 0 8":            "▁·█",
		"1 2 3 4 5 6 7 8":  "▁▂▃▄▅▆▇█",
		"5":                "█",
		"300 0 1 150":      "█·▁▄",
		"":                 "",
		"2 2 2":            "███",
		"98 254 255 36 6":  "▄██▂▁",
		"1000000 1 500000": "█▁▄",
	} {
		var days []int
		for _, f := range strings.Fields(in) {
			var n int
			fmt.Sscan(f, &n)
			days = append(days, n)
		}
		if got := dashSpark(days); got != want {
			t.Errorf("dashSpark(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestLocalCommitSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	kst := time.FixedZone("KST", 9*3600)
	days := dashDayRanges(time.Date(2026, 11, 23, 15, 0, 0, 0, kst), 3)
	r := testutil.NewRepo(t)
	at := func(day, hour int) string { return time.Date(2026, 11, day, hour, 0, 0, 0, kst).Format(time.RFC3339) }
	// git log --since reads the committer date.
	commit := func(when, msg string) {
		t.Setenv("GIT_COMMITTER_DATE", when)
		r.RunGit("commit", "--allow-empty", "-m", msg, "--date", when)
	}
	commit(at(20, 23), "before the window")
	commit(at(21, 0), "first instant of day 0")
	commit(days[0].End.Format(time.RFC3339), "last second of day 0")
	r.RunGit("update-ref", "refs/remotes/origin/main", "main")
	r.CreateBranch("develop")
	commit(at(23, 9), "on develop, day 2")
	commit(at(23, 10), "on develop, day 2 again")
	r.RunGit("update-ref", "refs/remotes/origin/develop", "develop")
	// A local branch that is not pushed does not count.
	r.CreateBranch("local-only")
	commit(at(22, 9), "local only, day 1")

	runner := &git.ExecRunner{Dir: r.Dir}
	s, err := localCommitSeries(context.Background(), runner, "origin", []string{"main", "develop", "gone"}, days)
	if err != nil || s == nil {
		t.Fatalf("localCommitSeries: %v, %v", s, err)
	}
	// main's two commits are on develop too and count one time.
	if fmt.Sprint(s.Days) != "[2 0 2]" || s.Total != 4 {
		t.Errorf("days %v total %d, want [2 0 2] and 4", s.Days, s.Total)
	}
	if s, err := localCommitSeries(context.Background(), runner, "origin", []string{"main"}, days); err != nil || fmt.Sprint(s.Days) != "[2 0 0]" {
		t.Errorf("main alone = %v, %v", s, err)
	}
	if s, err := localCommitSeries(context.Background(), runner, "origin", []string{"gone"}, days); s != nil || err != nil {
		t.Errorf("no remote branch = %v, %v; want no series", s, err)
	}
}

func TestRenderDashActivity(t *testing.T) {
	setNoColor(t, true, func() {})
	snap := dashTableSnapshot("repo:x-mesh/gk", "x-mesh/gk")
	if out := renderSnapshot(t, snap, 200); strings.Contains(out, "Activity") {
		t.Errorf("a snapshot without the block prints it:\n%s", out)
	}
	snap.Activity = &dashActivityJSON{}
	if out := renderSnapshot(t, snap, 200); strings.Contains(out, "Activity") {
		t.Errorf("a block with no count prints a line:\n%s", out)
	}

	snap.Activity = &dashActivityJSON{
		Opened:  newDashSeries([]int{47, 80, 98, 11, 3, 24, 35}),
		Merged:  newDashSeries([]int{46, 79, 97, 13, 0, 24, 30}),
		Commits: &dashSeriesJSON{Total: 1775, Days: []int{93, 254, 255, 36, 6, 79, 1052}, Partial: true},
	}
	out := renderSnapshot(t, snap, 200)
	want := "\nActivity  7d · PRs opened 298 ▄▇█▁▁▂▃ · merged 289 ▄▇█▂·▂▃ · commits 1,775+ ▁▂▂▁▁▁█\n\nPRs  "
	if !strings.Contains(out, want) {
		t.Errorf("output has no %q:\n%s", want, out)
	}
	if strings.Index(out, "Activity") > strings.Index(out, "PRs  ") {
		t.Errorf("Activity is not above the PRs:\n%s", out)
	}

	// Before the first read, and with only the commits read.
	snap.Activity = &dashActivityJSON{Opened: &dashSeriesJSON{Pending: true}, Merged: &dashSeriesJSON{Pending: true}, Commits: newDashSeries([]int{1, 0, 2})}
	if out = renderSnapshot(t, snap, 200); !strings.Contains(out, "Activity  7d · PRs opened … · merged … · commits 3 ▄·█\n") {
		t.Errorf("a pending block:\n%s", out)
	}
	// An org before its commit read: the label PRs stays on the first count.
	snap.Activity = &dashActivityJSON{Merged: newDashSeries([]int{2, 2}), Commits: &dashSeriesJSON{Pending: true}}
	if out = renderSnapshot(t, snap, 200); !strings.Contains(out, "Activity  7d · PRs merged 4 ██ · commits …\n") {
		t.Errorf("a block without the opened count:\n%s", out)
	}

	// A failed first read, and a failed refresh that keeps the days.
	snap.Activity = &dashActivityJSON{
		Opened:  &dashSeriesJSON{Error: "github activity query timed out"},
		Merged:  &dashSeriesJSON{Error: "github activity query timed out"},
		Commits: &dashSeriesJSON{Total: 3, Days: []int{1, 0, 2}, Error: "evil\x1b[2Jboom"},
	}
	out = renderSnapshot(t, snap, 200)
	for _, want := range []string{"Activity  7d · PRs opened ? · merged ? · commits 3 ▄·█\n", "  could not read: github activity query timed out\n  could not read: evil?[2Jboom\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "github activity query timed out") != 1 {
		t.Errorf("one failure of two counts shows two times:\n%s", out)
	}
	if strings.ContainsAny(out, "\x1b\x07") || snap.Activity.Commits.Error != "evil\x1b[2Jboom" {
		t.Errorf("control characters: output %q, snapshot error %q", out, snap.Activity.Commits.Error)
	}
}

func TestBuildDashSnapshotActivity(t *testing.T) {
	kst := time.FixedZone("KST", 9*3600)
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, kst)
	commits := newDashSeries([]int{1, 2})
	snap := buildDashSnapshot(dashInputs{Scope: "repo:o/r", Now: now, Res: &ghapi.DashResult{HasRepo: true}, Local: &dashLocal{}, Commits: commits})
	a := snap.Activity
	if a == nil || a.Commits != commits || a.Opened != nil || a.Merged != nil {
		t.Fatalf("activity = %+v", a)
	}
	if got := a.Since.Format(time.RFC3339); got != "2026-11-17T00:00:00+09:00" {
		t.Errorf("since = %s, want the start of the day six days before", got)
	}
}

func TestRenderDashUnreleasedCommitsWithoutPRs(t *testing.T) {
	setNoColor(t, true, func() {})
	snap := dashTableSnapshot("repo:x-mesh/gk", "x-mesh/gk")
	snap.Unreleased = []dashUnreleasedJSON{{Base: "main", Remote: "origin", Since: "v1.0.0", Commits: 2, Items: []dashUnreleasedRow{}}}
	if out := renderSnapshot(t, snap, 200); !strings.Contains(out, "Unreleased  main · 2 commits · no merged PR since v1.0.0\n") {
		t.Errorf("commits pushed without a PR:\n%s", out)
	}
}
