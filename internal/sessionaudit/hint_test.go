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
		{"stash push", "git stash push -m wip", true, "raw-stash"},
		{"stash bare", "git stash", true, "raw-stash"},
		{"integration", "git rebase main", true, "raw-integration"},
		{"apply", "git apply --cached fix.patch", true, "raw-apply"},
		{"restore staged (unstage)", "git restore --staged a.go", true, "raw-unstage"},
		{"gk short alias", "gk pull --with-base", true, "gk-short-alias"},
		// Highest severity wins across a chain: conflict (high) over status (medium).
		{"chain picks highest severity", "git diff --cc && git status", true, "raw-conflict-probes"},
		// Conflict wins over the context-probe shape it also matches.
		{"unmerged-files probe", "git diff --name-only --diff-filter=U", true, "raw-conflict-probes"},
		// Not covered: plumbing, file-restore checkout, stash subcommands git-kit
		// stash lacks, already git-kit, non-git.
		{"plumbing", "git rev-parse HEAD", false, ""},
		{"checkout restore", "git checkout -- app.go", false, ""},
		{"stash show (no gk verb)", "git stash show -p", false, ""},
		{"stash clear (no gk verb)", "git stash clear", false, ""},
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

// Silence is the right answer for an ordinary gap, but the wrong one for a gap
// that destroys work: the hook reads as approval when it says nothing.
func TestHint_DestructiveDiscardCautionsWithoutClaimingCoverage(t *testing.T) {
	for _, cmd := range []string{
		"git checkout -- src/a.rs",
		"git checkout 9db1c86 -- .goreleaser.yaml",
		"git checkout --ours -- daemon/layout.rs",
		"git restore src/a.rs",
		"git restore --source=HEAD~1 src/a.rs",
	} {
		res := Hint(cmd)
		if res.Caution == "" {
			t.Errorf("Hint(%q).Caution is empty — an irreversible discard must warn", cmd)
		}
		// The whole point: warn WITHOUT inventing a replacement.
		if res.Covered {
			t.Errorf("Hint(%q).Covered = true — gk has no verb for this", cmd)
		}
	}
}

// The caution must not leak onto commands that keep the work.
func TestHint_NoCautionOnNonDestructiveForms(t *testing.T) {
	for _, cmd := range []string{
		"git checkout main", // moves HEAD
		"git checkout -b feature origin/main",
		"git restore --staged src/a.rs", // index only — gk unstage covers it
		"git status",
		"git diff -- src/a.rs",
	} {
		if res := Hint(cmd); res.Caution != "" {
			t.Errorf("Hint(%q) raised a discard caution it should not: %q", cmd, res.CautionMatched)
		}
	}
}

// A command can be covered AND destructive; the replacement must survive the
// caution, since consider() rewrites the result wholesale on a better match.
func TestHint_CautionSurvivesAlongsideCoverage(t *testing.T) {
	res := Hint("git checkout -- a.rs && git status")
	if !res.Covered {
		t.Fatalf("the git status segment is covered, got %+v", res)
	}
	if res.Caution == "" {
		t.Error("the discard segment must still raise its caution")
	}
}
