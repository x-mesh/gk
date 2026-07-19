package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// The range needs a duration attached: 20 commits over 11 hours and 20 over
// six months are different stories, and the payload previously carried no
// dates at all, so the model could not tell them apart.
func TestCommitSpan(t *testing.T) {
	base := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	records := []commitRecord{
		{authorTime: base.Add(11 * time.Hour)}, // newest first, as git log emits
		{authorTime: base.Add(3 * time.Hour)},
		{authorTime: base},
	}
	first, last, span := commitSpan(records)
	if first != "2026-07-19T00:00:00Z" {
		t.Errorf("first = %q, want the OLDEST commit time", first)
	}
	if last != "2026-07-19T11:00:00Z" {
		t.Errorf("last = %q, want the NEWEST commit time", last)
	}
	if span != "3 commits spanning 11 hours" {
		t.Errorf("span = %q", span)
	}
}

// Order must not be trusted — a --date-order or grafted history can emit
// records out of sequence, and min/max still has to be right.
func TestCommitSpanIgnoresRecordOrder(t *testing.T) {
	base := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	shuffled := []commitRecord{
		{authorTime: base.Add(3 * time.Hour)},
		{authorTime: base.Add(11 * time.Hour)},
		{authorTime: base},
	}
	first, last, _ := commitSpan(shuffled)
	if first != "2026-07-19T00:00:00Z" || last != "2026-07-19T11:00:00Z" {
		t.Errorf("out-of-order records mis-scanned: first=%q last=%q", first, last)
	}
}

func TestCommitSpanEmpty(t *testing.T) {
	if f, l, s := commitSpan(nil); f != "" || l != "" || s != "" {
		t.Errorf("no records → no span, got %q %q %q", f, l, s)
	}
	if f, _, _ := commitSpan([]commitRecord{{}}); f != "" {
		t.Errorf("zero-time records must not fabricate a span, got %q", f)
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "under a minute"},
		{5 * time.Minute, "5 minutes"},
		{time.Hour, "1 hour"},
		{11 * time.Hour, "11 hours"},
		{72 * time.Hour, "3 days"},
		{100 * 24 * time.Hour, "3 months"},
	}
	for _, tc := range cases {
		if got := humanizeDuration(tc.d); got != tc.want {
			t.Errorf("humanizeDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// A release rewrites CHANGELOG, every README, and the docs tree at once, so
// docs churn outranks code churn and buries the actual story unless labelled.
func TestHotspotKind(t *testing.T) {
	docs := []string{
		"CHANGELOG.md", "README.ko.md", "docs/commands.md",
		"documentation/guide.rst", "NOTES.txt", "doc/x.adoc",
	}
	for _, p := range docs {
		if got := hotspotKind(p); got != "docs" {
			t.Errorf("hotspotKind(%q) = %q, want docs", p, got)
		}
	}
	code := []string{
		"internal/cli/log.go", "cmd/gk/main.go", "Makefile",
		// "markdown" is not an extension match, and a package named
		// "docsite" is not the docs/ directory.
		"internal/markdown/render.go", "internal/docsite/serve.go",
	}
	for _, p := range code {
		if got := hotspotKind(p); got != "code" {
			t.Errorf("hotspotKind(%q) = %q, want code", p, got)
		}
	}
}

// collectHotspots reduces to a set for the viz layer; the counts it discards
// are exactly what the summarizer needs, so both must come from one source.
func TestCollectHotspotCountsFeedsTheSet(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	// churn.txt gets 6 touches (above the 5 threshold), quiet.txt gets 2.
	for i := 0; i < 6; i++ {
		repo.WriteFile("churn.txt", fmt.Sprintf("rev %d", i))
		if i < 2 {
			repo.WriteFile("quiet.txt", fmt.Sprintf("rev %d", i))
		}
		repo.Commit(fmt.Sprintf("edit %d", i))
	}

	ctx := context.Background()
	runner := &git.ExecRunner{Dir: repo.Dir}
	ranked := collectHotspotCounts(ctx, runner)
	set := collectHotspots(ctx, runner)
	if len(ranked) != len(set) {
		t.Fatalf("set (%d) and ranked list (%d) disagree", len(set), len(ranked))
	}
	for _, h := range ranked {
		if !set[h.Path] {
			t.Errorf("%q ranked but missing from the set", h.Path)
		}
		if h.Touches < 5 {
			t.Errorf("%q kept with only %d touches (threshold is 5)", h.Path, h.Touches)
		}
	}
	// Highest churn first — magnitude is the whole point of keeping counts.
	for i := 1; i < len(ranked); i++ {
		if ranked[i-1].Touches < ranked[i].Touches {
			t.Errorf("not sorted by touches desc: %+v", ranked)
			break
		}
	}
}

// The prompt must ask for the interpretive contract and must point at
// merge_state rather than letting the model re-derive the direction from
// merged_count/unmerged_count.
func TestLogAssistPromptContract(t *testing.T) {
	p := logAssistSystemPrompt(false)
	for _, want := range []string{
		"STORY:", "SHAPE:", "WATCH:",
		"merge_state",
		"touches",
		`kind="docs"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("log assist prompt missing %q:\n%s", want, p)
		}
	}
}
