package cli

import (
	"bytes"
	"encoding/json"
	"io"
)

// Agent mode (GK_AGENT=1) wraps every machine-readable output in one uniform
// envelope so agent tooling branches on a single dispatch key — `state` —
// instead of learning each command's shape:
//
//	{ "schema": 1, "state": "ok",     "ok": true,  "result": { ...payload... } }
//	{ "schema": 1, "state": "paused", "ok": false, "result": { ...resume contract... } }
//	{ "schema": 1, "state": "error",  "ok": false, "error": { "code", "message", "hint", "remedies" } }
//
// `state` is one of ok | paused | blocked | error:
//   - ok      — the command succeeded.
//   - paused  — the operation is suspended awaiting input (a rebase/merge/
//     cherry-pick conflict, a `gk continue` that stopped again); the result
//     carries the resume/abort contract.
//   - blocked — a precondition is unmet (e.g. a diverged base that cannot
//     fast-forward); the result or error carries the remedy.
//   - error   — the command failed.
//
// `ok` is retained as a derived alias (ok == state=="ok") so existing
// consumers that branch on `ok` keep working without a re-parse. The non-zero
// exit code still signals paused/blocked/error to shell callers; `state` lets
// an agent tell them apart without inspecting the exit code or the prose.
//
// Without GK_AGENT the payload is emitted bare, byte-identical to the
// pre-envelope output — existing --json consumers see no change.
//
// The envelope schema is append-only; breaking changes bump `schema`.

type agentEnvelope struct {
	Schema int         `json:"schema"`
	State  string      `json:"state"`
	OK     bool        `json:"ok"`
	Result any         `json:"result,omitempty"`
	Error  *agentError `json:"error,omitempty"`
}

// Envelope state values. `state` is the primary dispatch key; see the package
// doc above for the semantics of each.
const (
	envStateOK      = "ok"
	envStatePaused  = "paused"
	envStateBlocked = "blocked"
	envStateError   = "error"
)

// agentStater lets a result payload declare a non-"ok" envelope state.
// Payloads that don't implement it (the common case) default to "ok"; an
// implementer may also return "" to mean "ok", so the state can be derived
// from the payload's own fields without special-casing the default.
type agentStater interface {
	agentState() string
}

// agentStateValid reports whether s is one of the four known envelope states.
// It is the single source of truth for the enum and guards emitAgentResult
// against a payload returning an out-of-range (or empty) state — either falls
// back to "ok".
func agentStateValid(s string) bool {
	switch s {
	case envStateOK, envStatePaused, envStateBlocked, envStateError:
		return true
	}
	return false
}

type agentError struct {
	Code     string      `json:"code"`
	Message  string      `json:"message"`
	Hint     string      `json:"hint,omitempty"`
	Remedies []errRemedy `json:"remedies,omitempty"`
}

// agentJSONIndent is the pretty-print indent every JSON emitter uses.
const agentJSONIndent = "  "

// agentCompactThresholdBytes is the compact-encoded size above which
// size-aware emitters drop pretty-print indentation: on payloads this large
// the indent is pure token overhead (~20% on the session-audit corpus) and
// nobody eyeballs them anyway. Opt-in per command via
// emitAgentResultCompactOver — the emitAgentResult default stays indented.
const agentCompactThresholdBytes = 16 << 10

// agentWrap wraps payload in the agent envelope when agent mode is on and
// returns it bare otherwise — the one place the envelope state is derived.
func agentWrap(payload any) any {
	if !AgentOut() {
		return payload
	}
	state := envStateOK
	if s, ok := payload.(agentStater); ok {
		if st := s.agentState(); agentStateValid(st) {
			state = st
		}
	}
	return agentEnvelope{Schema: 1, State: state, OK: state == envStateOK, Result: payload}
}

// emitAgentResult writes payload as indented JSON to w — wrapped in the
// agent envelope when agent mode is on, bare otherwise. Every JSON-emitting
// command routes through this so the envelope appears everywhere at once.
func emitAgentResult(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", agentJSONIndent)
	return enc.Encode(agentWrap(payload))
}

// emitAgentResultCompactOver behaves like emitAgentResult until the
// compact-encoded output exceeds threshold bytes, then emits it compact
// (no indentation). Small payloads stay pretty for eyeball-friendliness.
func emitAgentResultCompactOver(w io.Writer, payload any, threshold int) error {
	wrapped := agentWrap(payload)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(wrapped); err != nil {
		return err
	}
	if buf.Len() > threshold {
		_, err := w.Write(buf.Bytes())
		return err
	}
	enc = json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", agentJSONIndent)
	return enc.Encode(wrapped)
}

// FormatErrorJSON renders err as the failure envelope. main.go uses it in
// agent mode in place of FormatError; the exit code is unchanged — the
// envelope is diagnosis, not control flow.
func FormatErrorJSON(err error) string {
	if err == nil {
		return ""
	}
	// Layer gk guidance onto known raw-git failures (e.g. corrupt
	// commit-graph) before extracting hint/remedies/code, so the agent
	// envelope carries the same fix the human renderer shows. No-op when
	// the error already has a hint or matches no known pattern.
	err = decorateRawGitError(err)
	// Remedies and hints are command suggestions — rebrand them to the
	// invoked name so the caller can execute them verbatim. The message
	// is left alone: it may quote repository content.
	remedies := RemediesFrom(err)
	for i := range remedies {
		remedies[i].Command = selfRewrite(remedies[i].Command)
	}
	// Most failures are state:"error"; a blocked precondition (WithBlocked,
	// e.g. a diverged base) overrides to state:"blocked" so an agent runs the
	// remedy instead of treating it as a hard failure. ok stays false either
	// way — only state=="ok" derives ok:true.
	state := envStateError
	if s := stateFrom(err); agentStateValid(s) {
		state = s
	}
	env := agentEnvelope{Schema: 1, State: state, OK: false, Error: &agentError{
		Code:     errorCodeFromError(err),
		Message:  err.Error(),
		Hint:     selfRewrite(HintFrom(err)),
		Remedies: remedies,
	}}
	b, merr := json.MarshalIndent(env, "", agentJSONIndent)
	if merr != nil {
		return FormatError(err)
	}
	return string(b)
}
