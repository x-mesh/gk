package cli

import (
	"fmt"
	"strings"

	ghapi "github.com/x-mesh/gk/internal/github"
)

// followTrigger is one parsed `--on <kind>:<verb>[=<value>]` condition.
//
//	pr:merged | pr:opened | pr:closed | pr:label=<name> | pr:review=<state>
//	issue:opened | issue:closed | issue:label=<name> | issue:comment
type followTrigger struct {
	raw   string // original spelling, for messages
	kind  string // "pr" | "issue"
	verb  string // merged|opened|closed|label|review|comment
	value string // label name, or review state
}

// parseFollowTriggers parses and validates each --on value. An empty input
// returns the default trigger (pr:merged) — the canonical "deploy on merge".
func parseFollowTriggers(raw []string) ([]followTrigger, error) {
	if len(raw) == 0 {
		return []followTrigger{{raw: "pr:merged", kind: "pr", verb: "merged"}}, nil
	}
	out := make([]followTrigger, 0, len(raw))
	for _, r := range raw {
		t, err := parseOneFollowTrigger(strings.TrimSpace(r))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func parseOneFollowTrigger(s string) (followTrigger, error) {
	kind, rest, ok := strings.Cut(s, ":")
	if !ok || kind == "" || rest == "" {
		return followTrigger{}, fmt.Errorf("follow: invalid --on %q (want <pr|issue>:<event>, e.g. pr:merged, issue:label=bug)", s)
	}
	verb, value, hasValue := strings.Cut(rest, "=")
	t := followTrigger{raw: s, kind: kind, verb: verb, value: value}

	switch kind {
	case "pr":
		switch verb {
		case "merged", "opened", "closed":
			// no value
		case "label":
			if !hasValue || value == "" {
				return followTrigger{}, fmt.Errorf("follow: pr:label needs a name, e.g. pr:label=deploy")
			}
		case "review":
			if !hasValue || value == "" {
				t.value = "approved" // default review state
			}
		default:
			return followTrigger{}, fmt.Errorf("follow: unknown pr event %q (valid: merged, opened, closed, label=<name>, review=<state>)", verb)
		}
	case "issue":
		switch verb {
		case "opened", "closed", "comment":
			// no value
		case "label":
			if !hasValue || value == "" {
				return followTrigger{}, fmt.Errorf("follow: issue:label needs a name, e.g. issue:label=bug")
			}
		default:
			return followTrigger{}, fmt.Errorf("follow: unknown issue event %q (valid: opened, closed, label=<name>, comment)", verb)
		}
	default:
		return followTrigger{}, fmt.Errorf("follow: unknown --on kind %q (valid: pr, issue)", kind)
	}
	return t, nil
}

// matches reports whether ev fires this trigger. For pr:merged, a non-empty
// branch restricts to merges into THAT base branch (the compose case:
// `gk follow main --on pr:merged`).
func (t followTrigger) matches(ev ghapi.RepoEvent, branch string) bool {
	switch t.kind {
	case "pr":
		switch t.verb {
		case "merged":
			if ev.Type != "PullRequestEvent" || ev.Action != "closed" || !ev.PRMerged {
				return false
			}
			return branch == "" || ev.PRBase == branch
		case "opened":
			return ev.Type == "PullRequestEvent" && ev.Action == "opened"
		case "closed":
			return ev.Type == "PullRequestEvent" && ev.Action == "closed"
		case "label":
			return ev.Type == "PullRequestEvent" && ev.Action == "labeled" && ev.Label == t.value
		case "review":
			return ev.Type == "PullRequestReviewEvent" && ev.Action == "submitted" && ev.ReviewState == t.value
		}
	case "issue":
		switch t.verb {
		case "opened":
			return ev.Type == "IssuesEvent" && ev.Action == "opened"
		case "closed":
			return ev.Type == "IssuesEvent" && ev.Action == "closed"
		case "label":
			return ev.Type == "IssuesEvent" && ev.Action == "labeled" && ev.Label == t.value
		case "comment":
			return ev.Type == "IssueCommentEvent" && ev.Action == "created"
		}
	}
	return false
}

// refApproximable reports whether this trigger has a git-native fallback when
// no GitHub token is available. Only pr:merged does — a merge lands on the base
// branch, so `gk follow <branch>` (ref engine) can approximate it (losing the
// PR/label granularity). Everything else never touches git, so it cannot be
// approximated and must refuse without a token.
func (t followTrigger) refApproximable() bool {
	return t.kind == "pr" && t.verb == "merged"
}

// followTriggersNeedAPI reports whether ANY trigger requires the GitHub API
// (i.e. cannot fall back to the ref engine). Used by engine resolution.
func followTriggersNeedAPI(ts []followTrigger) bool {
	for _, t := range ts {
		if !t.refApproximable() {
			return true
		}
	}
	return false
}

// firstMatch returns the first trigger in ts that ev fires, or nil.
func firstMatchingTrigger(ts []followTrigger, ev ghapi.RepoEvent, branch string) *followTrigger {
	for i := range ts {
		if ts[i].matches(ev, branch) {
			return &ts[i]
		}
	}
	return nil
}
