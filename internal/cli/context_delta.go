package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// contextIncludeResponseKeys are the top-level JSON keys that only the
// --include collection (or its degraded notes) produces. The --delta path
// re-emits these fresh on every call and never folds them into the snapshot
// comparison: an agent that asked for a live diff/log/precheck section wants
// the current value, not "unchanged since". They are disjoint from the core
// fields, so a fresh include key never collides with a changed core field.
var contextIncludeResponseKeys = []string{"diff", "log", "precheck", "conflict", "remotes", "release", "github", "notes"}

// contextDeltaOutcome classifies the current core context against the
// last-saved snapshot for this worktree.
type contextDeltaOutcome struct {
	kind    string                     // "baseline" | "unchanged" | "changed"
	base    string                     // baseline SavedAt (RFC3339); empty for "baseline"
	changed map[string]json.RawMessage // changed core fields (only when kind=="changed")
}

// runContextWithDelta is the --delta code path. It is entered with the core
// context already collected but the include sections NOT yet fused, so the
// core snapshot it takes is pure. It snapshots core, fuses fresh includes,
// consults (and updates) the per-worktree ledger, then emits a
// full/unchanged/changed response. Every failure mode along the way — a
// marshal error, no home directory, an unreadable ledger — degrades silently
// to the plain full response; --delta is an additive optimization, never a
// reason to fail orientation.
func runContextWithDelta(cmd *cobra.Command, runner *git.ExecRunner, cfg *config.Config, includes map[string]bool, out contextJSON) error {
	ctx := cmd.Context()

	// The delta comparison is defined over CORE fields only — marshal the
	// snapshot before include sections are fused into out.
	coreBytes, merr := json.Marshal(out)

	// Fresh include sections ride along on every delta response regardless of
	// the core outcome.
	collectContextIncludes(ctx, runner, cfg, includes, &out)

	if merr != nil {
		return emitContextFull(cmd, out)
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		// No home directory → no ledger. Delta degrades to the full response.
		return emitContextFull(cmd, out)
	}
	worktree := repoToplevel(ctx, runner)
	if worktree == "" {
		worktree = RepoFlag()
	}

	outcome := computeContextDelta(home, worktree, coreBytes)

	if JSONOut() {
		return emitContextDeltaJSON(cmd, out, outcome)
	}
	return emitContextDeltaText(cmd, out, outcome)
}

// computeContextDelta loads the previous snapshot, persists the current one,
// and classifies the change. The load MUST precede the save, or every call
// would compare the current snapshot against itself and always report
// "unchanged". A missing, corrupt, or unreadable baseline — including one
// whose stored snapshot is not a JSON object — is the cold-start "baseline"
// case. The save is best-effort: a write failure (permissions, disk) is
// swallowed so the response still returns.
func computeContextDelta(home, worktree string, coreBytes []byte) contextDeltaOutcome {
	path := contextLedgerPath(home, worktree)
	baseline, ok := loadContextLedger(path)

	_ = saveContextLedger(path, contextLedgerEntry{
		Schema:   contextLedgerSchema,
		Worktree: normalizeWorktreePath(worktree),
		SavedAt:  time.Now().UTC(),
		Snapshot: json.RawMessage(coreBytes),
	})

	if !ok {
		return contextDeltaOutcome{kind: "baseline"}
	}

	prev := map[string]json.RawMessage{}
	cur := map[string]json.RawMessage{}
	if json.Unmarshal(baseline.Snapshot, &prev) != nil || json.Unmarshal(coreBytes, &cur) != nil {
		return contextDeltaOutcome{kind: "baseline"}
	}

	changed := diffTopLevelFields(prev, cur)
	base := baseline.SavedAt.Format(time.RFC3339)
	if len(changed) == 0 {
		return contextDeltaOutcome{kind: "unchanged", base: base}
	}
	return contextDeltaOutcome{kind: "changed", base: base, changed: changed}
}

// diffTopLevelFields returns the current value of every top-level field that
// differs from the baseline. A field that appeared or whose bytes changed
// carries its new raw value; a field that vanished (e.g. in_progress cleared
// once a rebase finished, which omitempty drops from the object) is surfaced
// as JSON null so the agent learns it went away rather than silently missing
// the transition. Byte comparison is exact and safe here because both maps
// come from json.Marshal of the same struct type, whose encoding is
// deterministic.
func diffTopLevelFields(prev, cur map[string]json.RawMessage) map[string]json.RawMessage {
	changed := map[string]json.RawMessage{}
	for k, cv := range cur {
		pv, ok := prev[k]
		if !ok || !bytes.Equal(pv, cv) {
			changed[k] = cv
		}
	}
	for k := range prev {
		if _, ok := cur[k]; !ok {
			changed[k] = json.RawMessage("null")
		}
	}
	return changed
}

// emitContextDeltaJSON writes the delta response in agent/JSON mode. The
// unchanged and changed responses are compact maps (only what the agent needs
// to update its picture); the baseline response is the full document plus a
// delta:"baseline" marker.
func emitContextDeltaJSON(cmd *cobra.Command, out contextJSON, outcome contextDeltaOutcome) error {
	inc, err := freshIncludeFields(out)
	if err != nil {
		// Extracting the include keys should never fail; if it somehow does,
		// fall back to the full response rather than dropping them.
		return emitContextFull(cmd, out)
	}

	switch outcome.kind {
	case "unchanged":
		return emitAgentResult(cmd.OutOrStdout(), unchangedDeltaResponse(outcome.base, inc))
	case "changed":
		return emitAgentResult(cmd.OutOrStdout(), changedDeltaResponse(outcome.base, outcome.changed, inc))
	default: // "baseline"
		out.Delta = "baseline"
		return emitAgentResult(cmd.OutOrStdout(), out)
	}
}

// emitContextDeltaText renders the delta response for humans. The compact
// forms would only obscure things on a terminal, so the render stays full;
// an "unchanged" call gets one leading line to say nothing moved since the
// last look.
func emitContextDeltaText(cmd *cobra.Command, out contextJSON, outcome contextDeltaOutcome) error {
	if outcome.kind == "unchanged" {
		fmt.Fprintf(cmd.OutOrStdout(), "delta: unchanged since %s\n", outcome.base)
	}
	renderContextText(cmd, out)
	return nil
}

// unchangedDeltaResponse is the compressed "nothing changed" payload: the
// marker fields plus whatever fresh include sections were requested.
func unchangedDeltaResponse(base string, inc map[string]json.RawMessage) map[string]json.RawMessage {
	resp := map[string]json.RawMessage{
		"delta":      rawJSONValue("unchanged"),
		"unchanged":  rawJSONValue(true),
		"delta_base": rawJSONValue(base),
	}
	mergeRawInto(resp, inc)
	return resp
}

// changedDeltaResponse carries only the core fields that moved (in their
// original names and shapes) plus fresh include sections.
func changedDeltaResponse(base string, changed, inc map[string]json.RawMessage) map[string]json.RawMessage {
	resp := map[string]json.RawMessage{
		"delta":      rawJSONValue("changed"),
		"delta_base": rawJSONValue(base),
	}
	mergeRawInto(resp, changed)
	mergeRawInto(resp, inc)
	return resp
}

// freshIncludeFields extracts the include-produced top-level keys from a
// fully-collected context (core + includes). Returning them as raw messages
// lets the compact delta responses re-attach them verbatim, byte-for-byte
// identical to what the full response would carry.
func freshIncludeFields(out contextJSON) (map[string]json.RawMessage, error) {
	full, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(full, &m); err != nil {
		return nil, err
	}
	inc := map[string]json.RawMessage{}
	for _, k := range contextIncludeResponseKeys {
		if v, ok := m[k]; ok {
			inc[k] = v
		}
	}
	return inc, nil
}

// emitContextFull is the degraded fallback: emit the full context exactly as
// a non-delta call would, with no delta marker.
func emitContextFull(cmd *cobra.Command, out contextJSON) error {
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), out)
	}
	renderContextText(cmd, out)
	return nil
}

// rawJSONValue marshals a scalar (string/bool) whose encoding never fails, so
// the error is intentionally dropped.
func rawJSONValue(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
}

// mergeRawInto copies src's entries into dst.
func mergeRawInto(dst, src map[string]json.RawMessage) {
	for k, v := range src {
		dst[k] = v
	}
}
