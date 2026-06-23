package sessionaudit

import "testing"

func TestHint(t *testing.T) {
	cases := []struct {
		name        string
		command     string
		wantCovered bool
		wantKind    string
	}{
		{"status probe", "git status --short", true, "raw-context-probes"},
		{"bare add", "git add .", true, "raw-commit-sequence"},
		{"checkout branch", "git checkout -b feat/x", true, "raw-branch-switch"},
		{"switch", "git switch main", true, "raw-branch-switch"},
		{"worktree", "git worktree add ../wt feat", true, "raw-worktree"},
		{"integration", "git rebase main", true, "raw-integration"},
		{"gk short alias", "gk pull --with-base", true, "gk-short-alias"},
		// Highest severity wins across a chain: conflict (high) over status (medium).
		{"chain picks highest severity", "git diff --cc && git status", true, "raw-conflict-probes"},
		// Not covered: plumbing, file-restore checkout, already git-kit, non-git.
		{"plumbing", "git rev-parse HEAD", false, ""},
		{"checkout restore", "git checkout -- app.go", false, ""},
		{"already git-kit", "git-kit context --include=all", false, ""},
		{"non-git", "ls -la", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Hint(tc.command)
			if res.Covered != tc.wantCovered {
				t.Fatalf("Hint(%q).Covered = %v, want %v (%+v)", tc.command, res.Covered, tc.wantCovered, res)
			}
			if res.Kind != tc.wantKind {
				t.Errorf("Hint(%q).Kind = %q, want %q", tc.command, res.Kind, tc.wantKind)
			}
			if tc.wantCovered && len(res.CoveredBy) == 0 {
				t.Errorf("Hint(%q) covered but CoveredBy empty", tc.command)
			}
		})
	}
}
