package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/chat"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// chatContextJSON is the token-budget-sensitive projection of contextJSON
// (gk context's full document) that gk chat injects as REPO_CONTEXT in its
// system prompt and returns from the git_context tool. Both call sites
// share this one shape and one collection path (collectChatContext) so a
// mid-session git_context re-query returns exactly what the prompt already
// described, just fresh — this closes the gap where REPO_CONTEXT was a
// session-start-only snapshot that went stale over a long REPL.
//
// Deliberately excluded: Diff/Log/Precheck/Conflict/Remotes/Release
// (gk context's --include sections) and NextActions/Bisect/the full
// Worktrees list — those are either heavy (a diff digest, log entries,
// full worktree metadata) or low orientation value for a standing prompt
// block; an agent that needs them already has git_log/git_diff/git_status
// tools. WorktreeCount survives as a single int: "are there other
// worktrees at all" without per-worktree detail.
type chatContextJSON struct {
	Branch        string           `json:"branch"`
	Detached      bool             `json:"detached,omitempty"`
	Upstream      string           `json:"upstream,omitempty"`
	Ahead         int              `json:"ahead"`
	Behind        int              `json:"behind"`
	Dirty         contextDirtyJSON `json:"dirty"`
	InProgress    *contextOpJSON   `json:"in_progress,omitempty"`
	Base          *contextBaseJSON `json:"base,omitempty"`
	LatestTag     string           `json:"latest_tag,omitempty"`
	WorktreeCount int              `json:"worktree_count,omitempty"`
}

// collectChatContext runs collectContext — the same collector `gk context`
// uses — and projects a SUCCESSFUL result down to chatContextJSON. ok
// reports whether collection actually succeeded: false means the caller
// must treat repo orientation as UNKNOWN, never assert the returned
// (zero-value) chatContextJSON as fact — see chatContextJSONString for how
// that distinction reaches the model. This is not the same case as a
// legitimately empty repo (a bare or unborn-HEAD repository, which
// collectContext itself already degrades to sane zero/empty field values
// with a nil error — see TestCollectChatContext_UnbornHEAD/
// _NotAGitDirectory): those keep ok==true, because "no branch yet" IS the
// accurate orientation there, not a gap in it. ok==false is reserved for
// collectContext actually failing, which chat's system prompt and
// git_context tool must still never let ABORT a session or a turn over —
// they degrade to "no data", not a fabricated "clean, up to date" one
// (previously this function collapsed both cases to the same zero-value
// return with no way to tell them apart, so a real collection failure was
// asserted to the model as if it were the true, checked repo state).
func collectChatContext(ctx context.Context, runner *git.ExecRunner, cfg *config.Config, denyGlobs []string) (chatContextJSON, bool) {
	proj, ok := projectChatContext(collectContext(ctx, runner, cfg))
	// collectContext's dirty counts come from countContextDirty, which has
	// no deny_paths notion — correct for `gk context` (the user sees their
	// own tree) but a leak here: a change confined to a denied path would
	// still bump staged/unstaged/untracked/conflicts, and since REPO_CONTEXT
	// exposes only the counts (no filenames), `staged:1` next to a git_status
	// tool that withholds the path is both a contradiction and an existence
	// oracle for exactly what the deny list hides. Recompute deny-aware.
	if ok && len(denyGlobs) > 0 {
		// A recompute FAILURE must not silently overwrite the collector's
		// real dirty counts with zeros — that would make REPO_CONTEXT and the
		// git_context tool assert a clean tree when `git status` merely
		// hiccuped, the same "fabricated fact" trap collectChatContext already
		// guards for collection itself. Degrade the whole context instead
		// (ok=false), so the caller drops REPO_CONTEXT rather than lying.
		dirty, dok := countChatDirty(ctx, runner, denyGlobs)
		if !dok {
			return chatContextJSON{}, false
		}
		proj.Dirty = dirty
	}
	return proj, ok
}

// countChatDirty is countContextDirty's deny-aware twin: it tallies the
// same staged/unstaged/untracked/conflict buckets but drops any entry
// whose path (or a rename's origin path) matches deny_paths. It reads
// `--porcelain -z` rather than the default: NUL termination means paths
// with spaces or special characters arrive raw instead of C-quoted, so
// deny matching sees the real path, never a `"…"`-wrapped one it would
// fail to match (the same quoting bypass the tools layer guards against).
//
// The bool reports whether the `git status` call succeeded. false means
// the counts are UNKNOWN, not zero — the caller must not treat a failed
// recompute as "clean" (see collectChatContext).
func countChatDirty(ctx context.Context, runner git.Runner, denyGlobs []string) (contextDirtyJSON, bool) {
	var d contextDirtyJSON
	raw, _, err := runner.Run(ctx, "status", "--porcelain", "-z")
	if err != nil {
		return d, false
	}
	toks := strings.Split(string(raw), "\x00")
	for i := 0; i < len(toks); i++ {
		rec := toks[i]
		if len(rec) < 3 {
			continue
		}
		x, y := rec[0], rec[1]
		path := rec[3:]
		denied := aicommit.MatchDeny(path, denyGlobs) != ""
		// A rename/copy record is followed by its origin path in the NEXT
		// NUL field; consume it and let either endpoint trip the deny check.
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			if i+1 < len(toks) {
				if aicommit.MatchDeny(toks[i+1], denyGlobs) != "" {
					denied = true
				}
				i++
			}
		}
		if denied {
			continue
		}
		switch {
		case x == '?' && y == '?':
			d.Untracked++
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			d.Conflicts++
		default:
			if x != ' ' {
				d.Staged++
			}
			if y != ' ' {
				d.Unstaged++
			}
		}
	}
	return d, true
}

// projectChatContext converts collectContext's raw (contextJSON, error)
// pair into gk chat's projection. Split out from collectChatContext so
// tests can drive the "collection failed" branch directly with a
// synthetic error — collectContext itself never actually returns a
// non-nil error today (every internal failure it hits already degrades
// to a zero/empty field instead of surfacing outward — see its own
// docstring), so this is the only way to exercise that branch without
// waiting on a real collectContext failure mode to exist.
func projectChatContext(full contextJSON, err error) (chatContextJSON, bool) {
	if err != nil {
		return chatContextJSON{}, false
	}
	return chatContextJSON{
		Branch:        full.Branch,
		Detached:      full.Detached,
		Upstream:      full.Upstream,
		Ahead:         full.Ahead,
		Behind:        full.Behind,
		Dirty:         full.Dirty,
		InProgress:    full.InProgress,
		Base:          full.Base,
		LatestTag:     full.LatestTag,
		WorktreeCount: len(full.Worktrees),
	}, true
}

// chatContextJSONString marshals collectChatContext's result to compact
// JSON text — the exact string gk chat wraps as REPO_CONTEXT at session
// start and the git_context tool returns on an on-demand re-query. A
// collection failure (ok==false) now returns a real error instead of
// silently encoding the zero value as if it were the actual repo state:
// chat.go's session-start call site already degrades REPO_CONTEXT to ""
// on any error from this function (dropping the whole fenced section from
// the system prompt — the same "nothing to show" contract chatRepoMapString
// already uses for REPO_MAP), and Registry.Dispatch already turns a
// returned error into an IsError tool result for the git_context tool-call
// path. Both call sites needed no changes for this — they were already
// built to handle this function failing; only the swallow inside
// collectChatContext itself was hiding it.
func chatContextJSONString(ctx context.Context, runner *git.ExecRunner, cfg *config.Config, denyGlobs []string) (string, error) {
	return encodeChatContext(collectChatContext(ctx, runner, cfg, denyGlobs))
}

// encodeChatContext renders proj to compact JSON text, or — when ok is
// false, meaning collection failed — a real error instead of silently
// encoding the zero value as if it were the actual repo state. Split out
// from chatContextJSONString so tests can drive both branches directly
// without a live git repository.
func encodeChatContext(proj chatContextJSON, ok bool) (string, error) {
	if !ok {
		return "", fmt.Errorf("chat context: collection failed")
	}
	b, err := json.Marshal(proj)
	if err != nil {
		return "", fmt.Errorf("chat context: encode: %w", err)
	}
	return string(b), nil
}

// buildChatSystemPrompt assembles gk chat's system prompt: repoCtxRaw (the
// REPO_CONTEXT snapshot from chatContextJSONString) and repoMapRaw (the
// opt-in REPO_MAP tree from chatRepoMapString, "" when ai.chat.auto_context
// is off/unset) are each run through redact — the SAME non-negotiable
// stage every tool result passes through in Registry.Dispatch — before
// chat.SystemPrompt fences them as untrusted data. Without this,
// repo-derived text (branch/tag names, file paths, etc.) injected into the
// system prompt would bypass the redaction every other piece of repository
// data goes through just because it arrived via a collector instead of a
// tool call. redact==nil passes text through unchanged, mirroring
// Registry.redact's own nil-safe behavior (acceptable only in tests, same
// as there).
func buildChatSystemPrompt(repoCtxRaw, repoMapRaw string, redact func(string) string, lang string, easy bool) string {
	repoCtx := repoCtxRaw
	repoMap := repoMapRaw
	if redact != nil {
		repoCtx = redact(repoCtxRaw)
		repoMap = redact(repoMapRaw)
	}
	return chat.SystemPrompt(repoCtx, repoMap, lang, easy)
}
