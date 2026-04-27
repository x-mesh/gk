package cli

import (
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/x-mesh/gk/internal/branchclean"
)

// ---------------------------------------------------------------------------
// Generators
// ---------------------------------------------------------------------------

func tuiBranchStatusGen() *rapid.Generator[branchclean.BranchStatus] {
	return rapid.SampledFrom([]branchclean.BranchStatus{
		branchclean.StatusMerged, branchclean.StatusGone, branchclean.StatusStale,
		branchclean.StatusSquashMerged, branchclean.StatusAmbiguous, branchclean.StatusActive,
	})
}

func tuiAICategoryGen() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		"", "completed", "experiment", "in_progress", "preserve",
	})
}

func tuiBranchNameGen() *rapid.Generator[string] {
	return rapid.Custom[string](func(t *rapid.T) string {
		prefix := rapid.SampledFrom([]string{"feat/", "fix/", "chore/", "release/", ""}).Draw(t, "prefix")
		suffix := rapid.StringMatching(`[a-z][a-z0-9\-]{1,15}`).Draw(t, "suffix")
		return prefix + suffix
	})
}

func tuiCleanCandidateGen() *rapid.Generator[branchclean.CleanCandidate] {
	return rapid.Custom[branchclean.CleanCandidate](func(t *rapid.T) branchclean.CleanCandidate {
		cat := tuiAICategoryGen().Draw(t, "aiCategory")
		var summary string
		if cat != "" {
			summary = rapid.StringMatching(`[a-zA-Z가-힣 ]{1,40}`).Draw(t, "aiSummary")
		}
		return branchclean.CleanCandidate{
			BranchEntry: branchclean.BranchEntry{
				Name:           tuiBranchNameGen().Draw(t, "name"),
				Status:         tuiBranchStatusGen().Draw(t, "status"),
				LastCommitDate: time.Now().AddDate(0, 0, -rapid.IntRange(0, 730).Draw(t, "daysAgo")),
			},
			AICategory: cat,
			AISummary:  summary,
			Selected:   rapid.Bool().Draw(t, "selected"),
		}
	})
}

// ---------------------------------------------------------------------------
// Feature: ai-branch-clean, Property 7: FormatCandidateLabel 필수 정보 포함
// ---------------------------------------------------------------------------

// TestProperty7_FormatCandidateLabelContainsRequiredInfo verifies that
// FormatCandidateLabel always includes branch name, status, and AI info
// when present.
// **Validates: Requirements 8.2, 8.5**
func TestProperty7_FormatCandidateLabelContainsRequiredInfo(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := tuiCleanCandidateGen().Draw(rt, "candidate")
		label := FormatCandidateLabel(c)

		// (1) 브랜치 이름 포함
		if !strings.Contains(label, c.Name) {
			rt.Fatalf("label %q does not contain branch name %q", label, c.Name)
		}

		// (2) Status 정보 포함
		if !strings.Contains(label, string(c.Status)) {
			rt.Fatalf("label %q does not contain status %q", label, string(c.Status))
		}

		// (3) AICategory가 비어있지 않으면 category와 summary 포함
		if c.AICategory != "" {
			if !strings.Contains(label, c.AICategory) {
				rt.Fatalf("label %q does not contain AI category %q", label, c.AICategory)
			}
			if !strings.Contains(label, c.AISummary) {
				rt.Fatalf("label %q does not contain AI summary %q", label, c.AISummary)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

func TestFormatCandidateLabel_WithAI(t *testing.T) {
	c := branchclean.CleanCandidate{
		BranchEntry: branchclean.BranchEntry{
			Name:           "feat/login",
			Status:         branchclean.StatusSquashMerged,
			LastCommitDate: time.Now().AddDate(0, 0, -3),
		},
		AICategory: "completed",
		AISummary:  "OAuth2 로그인 구현",
	}
	label := FormatCandidateLabel(c)

	if !strings.Contains(label, "feat/login") {
		t.Errorf("expected branch name in label: %s", label)
	}
	if !strings.Contains(label, "3d ago") {
		t.Errorf("expected relative time in label: %s", label)
	}
	if !strings.Contains(label, "[squash-merged]") {
		t.Errorf("expected status in label: %s", label)
	}
	if !strings.Contains(label, "completed: OAuth2 로그인 구현") {
		t.Errorf("expected AI info in label: %s", label)
	}
}

func TestFormatCandidateLabel_WithoutAI(t *testing.T) {
	c := branchclean.CleanCandidate{
		BranchEntry: branchclean.BranchEntry{
			Name:           "fix/typo",
			Status:         branchclean.StatusMerged,
			LastCommitDate: time.Now().AddDate(0, 0, -14),
		},
	}
	label := FormatCandidateLabel(c)

	if !strings.Contains(label, "fix/typo") {
		t.Errorf("expected branch name in label: %s", label)
	}
	if !strings.Contains(label, "2w ago") {
		t.Errorf("expected relative time in label: %s", label)
	}
	if !strings.Contains(label, "[merged]") {
		t.Errorf("expected status in label: %s", label)
	}
	// AI 정보가 없으므로 category가 포함되지 않아야 함
	if strings.Contains(label, "completed") || strings.Contains(label, "experiment") {
		t.Errorf("unexpected AI info in label: %s", label)
	}
}

func TestFormatCandidateLabel_Today(t *testing.T) {
	c := branchclean.CleanCandidate{
		BranchEntry: branchclean.BranchEntry{
			Name:           "hotfix/urgent",
			Status:         branchclean.StatusActive,
			LastCommitDate: time.Now(),
		},
	}
	label := FormatCandidateLabel(c)

	if !strings.Contains(label, "(today)") {
		t.Errorf("expected 'today' in label: %s", label)
	}
}

func TestFormatCandidateLabel_OldBranch(t *testing.T) {
	c := branchclean.CleanCandidate{
		BranchEntry: branchclean.BranchEntry{
			Name:           "old/feature",
			Status:         branchclean.StatusStale,
			LastCommitDate: time.Now().AddDate(-2, 0, 0),
		},
	}
	label := FormatCandidateLabel(c)

	if !strings.Contains(label, "2y ago") {
		t.Errorf("expected '2y ago' in label: %s", label)
	}
}

func TestRelativeTime(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"today", 6 * time.Hour, "today"},
		{"1 day", 36 * time.Hour, "1d ago"},
		{"5 days", 5 * 24 * time.Hour, "5d ago"},
		{"2 weeks", 14 * 24 * time.Hour, "2w ago"},
		{"3 months", 90 * 24 * time.Hour, "3m ago"},
		{"1 year", 400 * 24 * time.Hour, "1y ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relativeTime(tt.duration)
			if got != tt.want {
				t.Errorf("relativeTime(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}
