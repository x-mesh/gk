package cli

import "github.com/spf13/cobra"

// helpGroups defines the root-help sections in display order. The taxonomy is
// README's "Commands" chapter carried into --help (Daily/Branches/Worktree/…),
// with three deliberate deviations: commit and push sit in Daily (they are THE
// daily verbs, whatever machinery powers them), the one-command Policies and
// Continuous chapters fold into their neighbours, and the agent-native verbs
// get their own section — they are gk's identity and README under-documents
// them. Korean titles ship here too: swapEasyShorts swaps them in for the
// duration of an Easy-Mode render, same as it does for command Shorts.
var helpGroups = []struct {
	id, title, koTitle string
}{
	{"daily", "Daily workflow:", "일상 작업:"},
	{"branches", "Branches:", "브랜치:"},
	{"worktree", "Worktrees & watch:", "워크트리·감시:"},
	{"safety", "Checks & release:", "검사·릴리스:"},
	{"recovery", "Recovery & safety nets:", "복구·안전망:"},
	{"ai", "AI assistants:", "AI 도우미:"},
	{"github", "GitHub:", "GitHub:"},
	{"agent", "Agent & audit:", "에이전트·감사:"},
	{"setup", "Setup & maintenance:", "설정·관리:"},
}

// helpGroupKoTitle maps a group id to its Korean title for Easy-Mode help.
var helpGroupKoTitle = func() map[string]string {
	m := make(map[string]string, len(helpGroups))
	for _, g := range helpGroups {
		m[g.id] = g.koTitle
	}
	return m
}()

// commandGroup assigns every visible root subcommand (canonical name) to a
// help group. Central on purpose — commands register themselves from ~70
// init() files, and a per-file GroupID line is the kind of thing a new verb
// forgets; here a test walks the real command tree and fails when a visible
// command is missing, so the classification cannot silently rot. help and
// completion stay ungrouped and render under cobra's additional-commands
// section.
var commandGroup = map[string]string{
	// Daily workflow — the verbs of an ordinary working session.
	"clone": "daily", "pull": "daily", "push": "daily", "commit": "daily",
	"land": "daily", "promote": "daily", "status": "daily", "log": "daily",
	"diff": "daily", "find": "daily", "local": "daily", "merge": "daily",
	"resolve": "daily", "rebase": "daily", "sync": "daily", "stash": "daily",
	"chat": "daily", "refresh": "daily",

	// Branches.
	"branch": "branches", "branch-check": "branches", "switch": "branches",

	// Worktrees & watch (follow is the remote-watching sibling).
	"worktree": "worktree", "watch": "worktree", "prompt-info": "worktree",
	"follow": "worktree",

	// Checks & release.
	"ship": "safety", "precheck": "safety", "preflight": "safety",
	"lint-commit": "safety", "guard": "safety",

	// Recovery & safety nets — everything that walks state back or nets it.
	"timemachine": "recovery", "undo": "recovery", "unstage": "recovery",
	"apply": "recovery", "reset": "recovery", "ignore": "recovery",
	"forget": "recovery", "wipe": "recovery", "restore": "recovery",
	"edit-conflict": "recovery", "continue": "recovery", "abort": "recovery",
	"bisect": "recovery", "wip": "recovery", "unwip": "recovery",
	"snapshot": "recovery", "discard": "recovery",

	// AI assistants.
	"next": "ai", "review": "ai", "changelog": "ai", "ask": "ai",
	"explain": "ai", "do": "ai",

	// GitHub queries.
	"pr": "github", "issue": "github", "inbox": "github",

	// Agent & audit — the agent-native surface.
	"agents": "agent", "context": "agent", "batch": "agent",
	"hint": "agent", "session": "agent",

	// Setup & maintenance.
	"init": "setup", "config": "setup", "doctor": "setup", "guide": "setup",
	"drivers": "setup", "hooks": "setup", "update": "setup",
}

// installHelpGroups registers the help groups on root and assigns each child
// its GroupID from commandGroup. Runs once from Execute(), after every
// subcommand's init() has attached itself; the guard keeps repeated calls
// (tests) from duplicating group sections.
func installHelpGroups(root *cobra.Command) {
	if len(root.Groups()) > 0 {
		return
	}
	for _, g := range helpGroups {
		root.AddGroup(&cobra.Group{ID: g.id, Title: g.title})
	}
	for _, c := range root.Commands() {
		if id, ok := commandGroup[c.Name()]; ok {
			c.GroupID = id
		}
	}
}
