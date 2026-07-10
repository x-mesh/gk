package tools

import (
	"context"
	"encoding/json"
)

// RegisterContextTools adds git_context: an on-demand re-query of the same
// lightweight repository orientation snapshot gk chat injects into its
// system prompt as REPO_CONTEXT. REPO_CONTEXT is collected once at session
// start, so a long-lived REPL conversation can drift out of date (the user
// starts a rebase in another terminal, switches branches, etc.) — this tool
// lets the model clear that staleness instead of trusting a stale snapshot
// or guessing.
//
// collect is injected by the CLI layer rather than implemented here: the
// actual collection (branch/upstream/ahead-behind/dirty/in-progress-op/
// base-drift/worktree-count) is gk context's collectContext, which lives in
// internal/cli — the layer ABOVE this package that wires up the registry in
// the first place, so importing it here would create an import cycle.
// collect must return the exact same JSON shape the REPO_CONTEXT block
// carries (same source, same field-selected projection) so a re-query
// reads as "the same document, refreshed" rather than a different view.
func RegisterContextTools(r *Registry, collect func(ctx context.Context) (string, error)) {
	r.Register(Tool{
		Name: "git_context",
		Description: "Re-collect the current repository orientation snapshot: branch, detached HEAD, " +
			"upstream, ahead/behind, dirty summary (staged/unstaged/untracked/conflicts), any in-progress " +
			"rebase/merge/cherry-pick/revert (with resume/abort commands), base-branch drift, latest tag, " +
			"and how many linked worktrees exist. Same shape as the REPO_CONTEXT block in the system " +
			"prompt, freshly re-queried — call this in a long-running conversation when repo state may " +
			"have changed since the session started (e.g. \"am I mid-rebase right now?\").",
		Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			return collect(ctx)
		},
	})
}
