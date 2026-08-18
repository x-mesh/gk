package sessionaudit

import (
	"strings"
	"testing"
)

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

// The hook speaks with gk's authority, so a confident wrong answer costs more
// than silence. gk context returns a fixed newest-first slice of the current
// branch with no formatting control — it cannot produce a date-filtered,
// custom-format, or other-ref log — and it prints no per-commit stat. Pointing
// an agent at it for those was not a reporting error: the PreToolUse hook said
// it out loud, mid-session. This pins what the hook may and may not claim.
func TestHint_LogQueriesNameTheVerbThatAnswersThem(t *testing.T) {
	cases := []struct {
		name        string
		command     string
		wantKind    string
		wantCovered bool
	}{
		// gk log takes revisions positionally and has -n / --since / --format.
		{"beyond the slice", "git log --oneline -20", "raw-log-query", true},
		{"another ref", "git log --oneline origin/main", "raw-log-query", true},
		{"date and format", `git log --since=2026-07-25 --pretty=format:%h`, "raw-log-query", true},

		// Still orientation — one gk context call answers these beside git status.
		{"within the slice", "git log --oneline -3", "raw-context-probes", true},
		{"bare log", "git log", "raw-context-probes", true},

		// gk log has no --stat / --until, and gk wraps no object inspection.
		// Naming a verb that would reject the flag is worse than saying nothing.
		{"log stat", "git log --stat", "", false},
		{"log until", "git log --until=2026-01-01", "", false},
		{"show stat", "git show --stat HEAD", "", false},

		// The hunt still wins: gk find collapses several guesses into one call,
		// which a 1:1 gk log swap cannot match.
		{"path scoped", "git log -20 -- internal/", "raw-history-search", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Hint(tc.command)
			if res.Kind != tc.wantKind {
				t.Errorf("Hint(%q).Kind = %q, want %q", tc.command, res.Kind, tc.wantKind)
			}
			if res.Covered != tc.wantCovered {
				t.Errorf("Hint(%q).Covered = %v, want %v", tc.command, res.Covered, tc.wantCovered)
			}
			// Suggestion is the sentence the PreToolUse hook actually prints.
			// Kind and CoveredBy follow from the spec table, so asserting only
			// those would leave the user-visible text untested — and the text
			// is what sent agents to the wrong command in the first place.
			if tc.wantCovered {
				// Every covered row asserts the verb its kind promises, so a
				// classifier that routes to the wrong covered kind fails here
				// too — not only the raw-log-query rows.
				wantVerb := map[string]string{
					"raw-log-query":      "git-kit log",
					"raw-context-probes": "git-kit context",
					"raw-history-search": "git-kit find",
				}[tc.wantKind]
				if wantVerb == "" {
					t.Fatalf("test bug: no expected verb for kind %q", tc.wantKind)
				}
				if !strings.Contains(res.Suggestion, wantVerb) {
					t.Errorf("Hint(%q).Suggestion = %q, want it to name %s", tc.command, res.Suggestion, wantVerb)
				}
				// The text may still MENTION context — it explains why context
				// is the wrong tool here. What it must never do is tell the
				// agent to run it.
				if tc.wantKind != "raw-context-probes" && strings.Contains(res.Suggestion, "Use git-kit context") {
					t.Errorf("Hint(%q).Suggestion still recommends context: %q", tc.command, res.Suggestion)
				}
			} else if res.Suggestion != "" {
				t.Errorf("Hint(%q) should stay silent, got Suggestion %q", tc.command, res.Suggestion)
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

// The uncommit answers with gk undo --soft — a 1:1 swap kept out of the
// collapse groups, but the hint (and the PreToolUse hook built on it) must
// name the verb that records a backup ref first. covered_by[0] matters: the
// hook tells the agent to run exactly that entry.
func TestHint_UncommitNamesUndoSoft(t *testing.T) {
	res := Hint("git reset --soft HEAD~1")
	if !res.Covered || res.Kind != "raw-uncommit" {
		t.Fatalf("Hint(reset --soft) = %+v, want covered raw-uncommit", res)
	}
	if len(res.CoveredBy) == 0 || res.CoveredBy[0] != "git-kit undo --soft" {
		t.Errorf("CoveredBy = %v, want git-kit undo --soft first", res.CoveredBy)
	}
}
