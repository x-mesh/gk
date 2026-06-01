package cli

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func TestSuggestNextAction(t *testing.T) {
	tests := []struct {
		name      string
		g         groupedEntries
		st        *git.Status
		wantNext  string
		wantWhyIn string
	}{
		{
			name:      "detached HEAD attaches to branch",
			st:        &git.Status{Branch: "(detached)"},
			wantNext:  "gk switch <branch>   ·   gk branch <new>",
			wantWhyIn: "detached HEAD",
		},
		{
			name:      "empty branch treated as detached",
			st:        &git.Status{Branch: ""},
			wantNext:  "gk switch <branch>   ·   gk branch <new>",
			wantWhyIn: "detached HEAD",
		},
		{
			name:      "conflict takes priority over detached",
			g:         groupedEntries{Unmerged: []git.StatusEntry{{}}},
			st:        &git.Status{Branch: "(detached)"},
			wantNext:  "gk continue   ·   gk abort",
			wantWhyIn: "conflict",
		},
		{
			name:      "branch without upstream still suggests upstream",
			st:        &git.Status{Branch: "feature"},
			wantNext:  "git branch --set-upstream-to=origin/<branch>",
			wantWhyIn: "no upstream configured",
		},
		{
			name:      "clean and in sync",
			st:        &git.Status{Branch: "feature", Upstream: "origin/feature"},
			wantNext:  "자유롭게 작업하세요",
			wantWhyIn: "in sync",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, why := suggestNextAction(tt.g, tt.st)
			if next != tt.wantNext {
				t.Errorf("next = %q, want %q", next, tt.wantNext)
			}
			if !strings.Contains(why, tt.wantWhyIn) {
				t.Errorf("why = %q, want substring %q", why, tt.wantWhyIn)
			}
		})
	}
}
