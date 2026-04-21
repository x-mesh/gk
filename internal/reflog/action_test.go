package reflog

import "testing"

func TestClassifyAction(t *testing.T) {
	tests := []struct {
		msg  string
		want Action
	}{
		// Reset
		{"reset: moving to HEAD~1", ActionReset},
		{"reset: moving to abc1234", ActionReset},

		// Commit
		{"commit: fix typo", ActionCommit},
		{"commit (amend): fix typo", ActionCommit},
		{"commit (initial): initial commit", ActionCommit},

		// Merge
		{"merge feat/x: Merge made by recursive", ActionMerge},
		{"merge: Merge branch 'main'", ActionMerge},

		// Rebase
		{"rebase (start): checkout main", ActionRebase},
		{"rebase finished: returning to refs/heads/x", ActionRebase},
		{"rebase (pick): fix: handle nil pointer", ActionRebase},
		{"rebase -i (squash): squash commit", ActionRebase},

		// Checkout
		{"checkout: moving from main to feat/y", ActionCheckout},
		{"checkout: moving from feat/y to main", ActionCheckout},

		// Pull
		{"pull: Fast-forward", ActionPull},
		{"pull --rebase: Fast-forward", ActionPull},

		// Push
		{"push", ActionPush},
		{"push --force", ActionPush},

		// Branch
		{"branch: Created from HEAD", ActionBranch},
		{"branch: Reset to abc1234", ActionBranch},

		// Cherry-pick
		{"cherry-pick: fix: handle nil pointer", ActionCherryPick},
		{"cherry-pick: Merge commit 'abc1234'", ActionCherryPick},

		// Stash
		{"stash: WIP on main: abc1234 fix typo", ActionStash},
		{"stash drop stash@{0}: dropped", ActionStash},

		// Unknown
		{"random text", ActionUnknown},
		{"", ActionUnknown},
		{"some unrecognized action: details", ActionUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.msg, func(t *testing.T) {
			got := classifyAction(tc.msg)
			if got != tc.want {
				t.Errorf("classifyAction(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}
