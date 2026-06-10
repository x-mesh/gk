package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// gk rebase --plan is the declarative replacement for `git rebase -i`'s
// editor session — the one git surface agents structurally cannot use. The
// LLM (or human) writes a JSON plan saying what should happen to each
// commit; gk validates it against the real history and drives git's own
// rebase machinery with a pre-built todo. Judgment stays with the caller,
// execution stays deterministic.

// rebasePlanEntry is one commit's fate in the plan.
type rebasePlanEntry struct {
	// Action: pick | squash | fixup | reword | drop
	Action string `json:"action"`
	// Commit accepts any unambiguous prefix of the SHA (≥7 chars
	// recommended); validation resolves it against the rebase range.
	Commit string `json:"commit"`
	// Message replaces the commit message for reword (required there,
	// rejected elsewhere).
	Message string `json:"message,omitempty"`
	// Subject is informational (filled by --plan-template, ignored on input).
	Subject string `json:"subject,omitempty"`
	// Pushed is informational (filled by --plan-template, ignored on input).
	Pushed bool `json:"pushed,omitempty"`
}

type rebasePlan struct {
	Entries []rebasePlanEntry
}

var rebasePlanActions = map[string]bool{
	"pick": true, "squash": true, "fixup": true, "reword": true, "drop": true,
}

// parseRebasePlan reads the JSON plan: either a bare array of entries or
// an object {"commits": [...]} — both shapes round-trip from --plan-template.
func parseRebasePlan(r io.Reader) (rebasePlan, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return rebasePlan{}, fmt.Errorf("rebase: read plan: %w", err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return rebasePlan{}, fmt.Errorf("rebase: plan is empty")
	}
	var entries []rebasePlanEntry
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return rebasePlan{}, fmt.Errorf("rebase: parse plan: %w", err)
		}
	} else {
		var doc struct {
			Commits []rebasePlanEntry `json:"commits"`
		}
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return rebasePlan{}, fmt.Errorf("rebase: parse plan: %w", err)
		}
		entries = doc.Commits
	}
	if len(entries) == 0 {
		return rebasePlan{}, fmt.Errorf("rebase: plan has no entries")
	}
	return rebasePlan{Entries: entries}, nil
}

// rebaseRangeCommit is one commit of the real onto..HEAD range, oldest first.
type rebaseRangeCommit struct {
	SHA     string
	Subject string
	Parents int
}

// loadRebaseRange lists onto..HEAD oldest-first with parent counts (to
// reject merge commits) — the ground truth the plan must match.
func loadRebaseRange(ctx context.Context, runner git.Runner, onto string) ([]rebaseRangeCommit, error) {
	out, stderr, err := runner.Run(ctx, "log", "--reverse", "--format=%H%x00%P%x00%s", onto+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("rebase: list %s..HEAD: %s: %w", onto, strings.TrimSpace(string(stderr)), err)
	}
	var commits []rebaseRangeCommit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		parents := 0
		if strings.TrimSpace(parts[1]) != "" {
			parents = len(strings.Fields(parts[1]))
		}
		commits = append(commits, rebaseRangeCommit{SHA: parts[0], Subject: parts[2], Parents: parents})
	}
	return commits, nil
}

// validateRebasePlan checks the plan against the real range and returns the
// entries with full SHAs resolved, in plan order. Every rule exists to stop
// a silent history mangling:
//
//   - every range commit must be addressed exactly once (a forgotten commit
//     is an error, not an implicit pick — dropping must be explicit)
//   - unknown/ambiguous/out-of-range SHAs are errors
//   - merge commits cannot be replayed by this engine (v1)
//   - squash/fixup cannot lead the todo (nothing to meld into)
//   - reword requires a message; other actions must not carry one
//   - rewriting commits that exist on a remote requires --allow-pushed
type rebasePlanValidated struct {
	Entries []rebasePlanEntry // Commit holds the full SHA
}

func validateRebasePlan(plan rebasePlan, rng []rebaseRangeCommit, pushed map[string]bool, pushedKnown, allowPushed bool) (rebasePlanValidated, error) {
	if len(rng) == 0 {
		return rebasePlanValidated{}, fmt.Errorf("rebase: nothing to rebase (empty range)")
	}
	for _, c := range rng {
		if c.Parents > 1 {
			return rebasePlanValidated{}, WithHint(
				fmt.Errorf("rebase: range contains merge commit %s (%s)", shortSHA(c.SHA), c.Subject),
				"merge commits cannot be replayed by --plan; pick a range below the merge or flatten it first",
			)
		}
	}

	addressed := make(map[string]int, len(rng))
	for _, c := range rng {
		addressed[c.SHA] = 0
	}
	resolve := func(prefix string) (string, error) {
		if prefix == "" {
			return "", fmt.Errorf("rebase: plan entry with empty commit")
		}
		match := ""
		for _, c := range rng {
			if strings.HasPrefix(c.SHA, prefix) {
				if match != "" {
					return "", fmt.Errorf("rebase: commit prefix %q is ambiguous in the range", prefix)
				}
				match = c.SHA
			}
		}
		if match == "" {
			return "", fmt.Errorf("rebase: commit %q is not in the rebase range", prefix)
		}
		return match, nil
	}

	out := rebasePlanValidated{Entries: make([]rebasePlanEntry, 0, len(plan.Entries))}
	for i, e := range plan.Entries {
		action := strings.ToLower(strings.TrimSpace(e.Action))
		if !rebasePlanActions[action] {
			return rebasePlanValidated{}, fmt.Errorf("rebase: entry %d: unknown action %q (pick|squash|fixup|reword|drop)", i+1, e.Action)
		}
		sha, err := resolve(strings.TrimSpace(e.Commit))
		if err != nil {
			return rebasePlanValidated{}, err
		}
		addressed[sha]++
		if addressed[sha] > 1 {
			return rebasePlanValidated{}, fmt.Errorf("rebase: commit %s appears more than once in the plan", shortSHA(sha))
		}
		if i == 0 && (action == "squash" || action == "fixup") {
			return rebasePlanValidated{}, fmt.Errorf("rebase: first entry cannot be %s — there is no previous commit to meld into", action)
		}
		if action == "reword" && strings.TrimSpace(e.Message) == "" {
			return rebasePlanValidated{}, fmt.Errorf("rebase: reword of %s needs a message", shortSHA(sha))
		}
		if action != "reword" && strings.TrimSpace(e.Message) != "" {
			return rebasePlanValidated{}, fmt.Errorf("rebase: entry %s: message is only valid with reword", shortSHA(sha))
		}
		out.Entries = append(out.Entries, rebasePlanEntry{Action: action, Commit: sha, Message: e.Message})
	}

	var missing []string
	for _, c := range rng {
		if addressed[c.SHA] == 0 {
			missing = append(missing, shortSHA(c.SHA))
		}
	}
	if len(missing) > 0 {
		return rebasePlanValidated{}, WithHint(
			fmt.Errorf("rebase: plan does not address commit(s): %s", strings.Join(missing, ", ")),
			"every commit in the range must appear in the plan — add them as pick, or drop them explicitly",
		)
	}

	// Pushed guard. A plain pick only keeps a commit's SHA while everything
	// BEFORE it is also untouched — the first deviation (non-pick action or
	// order change) rewrites every commit from that point on. So the guard
	// applies to any pushed commit at or after the first deviation.
	if pushedKnown && !allowPushed {
		firstChange := len(out.Entries)
		for i, e := range out.Entries {
			if e.Action != "pick" || e.Commit != rng[i].SHA {
				firstChange = i
				break
			}
		}
		for i := firstChange; i < len(out.Entries); i++ {
			if pushed[out.Entries[i].Commit] {
				return rebasePlanValidated{}, WithRemedy(
					fmt.Errorf("rebase: %s is already on a remote and this plan rewrites it — every collaborator would have to recover", shortSHA(out.Entries[i].Commit)),
					"rerun with --allow-pushed if you own this branch, then publish with `gk push --force`",
					errRemedy{Command: "gk rebase --allow-pushed", Safety: "destructive"},
				)
			}
		}
	}
	return out, nil
}

// buildRebaseTodo renders the validated plan as a git-rebase-todo. reword
// becomes pick + `exec git commit --amend -F <file>` — fully non-interactive
// and quoting-safe: messages live in files under msgDir, never on the shell
// line. Returns the todo content and the message files it created.
func buildRebaseTodo(plan rebasePlanValidated, msgDir string) (string, []string, error) {
	var b strings.Builder
	var files []string
	for i, e := range plan.Entries {
		switch e.Action {
		case "pick", "squash", "fixup", "drop":
			fmt.Fprintf(&b, "%s %s\n", e.Action, e.Commit)
		case "reword":
			path := filepath.Join(msgDir, fmt.Sprintf("reword-%02d-%s.txt", i, shortSHA(e.Commit)))
			if err := os.WriteFile(path, []byte(strings.TrimSpace(e.Message)+"\n"), 0o600); err != nil {
				return "", nil, fmt.Errorf("rebase: write reword message: %w", err)
			}
			files = append(files, path)
			fmt.Fprintf(&b, "pick %s\n", e.Commit)
			fmt.Fprintf(&b, "exec git commit --amend -F %s\n", shellQuote(path))
		}
	}
	return b.String(), files, nil
}

// shellQuote wraps a path for the todo's exec line (run via sh). Single
// quotes with the standard '"'"' escape keep arbitrary paths safe.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
