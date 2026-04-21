package reflog

import "strings"

// Action represents a coarse-grained classification of a reflog entry.
type Action string

const (
	ActionReset      Action = "reset"
	ActionCommit     Action = "commit" // includes commit (initial), commit (amend)
	ActionMerge      Action = "merge"
	ActionRebase     Action = "rebase"   // finish, start, pick, continue, skip, abort
	ActionCheckout   Action = "checkout" // moving from X to Y
	ActionPull       Action = "pull"
	ActionPush       Action = "push"
	ActionBranch     Action = "branch" // Branch: created, renamed
	ActionCherryPick Action = "cherry-pick"
	ActionStash      Action = "stash"
	ActionUnknown    Action = "unknown"
)

// classifyAction inspects the reflog message and returns a coarse-grained Action.
func classifyAction(msg string) Action {
	m := strings.ToLower(strings.TrimSpace(msg))
	switch {
	case strings.HasPrefix(m, "reset:"):
		return ActionReset
	case strings.HasPrefix(m, "commit:") || strings.HasPrefix(m, "commit (") || strings.HasPrefix(m, "commit "):
		return ActionCommit
	case strings.HasPrefix(m, "merge ") || strings.HasPrefix(m, "merge:"):
		return ActionMerge
	case strings.HasPrefix(m, "rebase") || strings.Contains(m, "rebase (") || strings.Contains(m, "rebase -i"):
		return ActionRebase
	case strings.HasPrefix(m, "checkout:"):
		return ActionCheckout
	case strings.HasPrefix(m, "pull:") || strings.HasPrefix(m, "pull ") || strings.Contains(m, "pull --"):
		return ActionPull
	case strings.HasPrefix(m, "push:") || strings.HasPrefix(m, "push ") || m == "push" || strings.Contains(m, "push --"):
		return ActionPush
	case strings.HasPrefix(m, "branch:"):
		return ActionBranch
	case strings.HasPrefix(m, "cherry-pick"):
		return ActionCherryPick
	case strings.HasPrefix(m, "stash"):
		return ActionStash
	}
	return ActionUnknown
}
