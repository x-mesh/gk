package cli

import (
	"encoding/json"
	"io"
)

// Agent mode (GK_AGENT=1) wraps every machine-readable output in one uniform
// envelope so agent tooling branches on two fields — `ok` and `error.code` —
// instead of learning each command's shape:
//
//	{ "schema": 1, "ok": true,  "result": { ...command payload... } }
//	{ "schema": 1, "ok": false, "error": { "code", "message", "hint", "remedies" } }
//
// Without GK_AGENT the payload is emitted bare, byte-identical to the
// pre-envelope output — existing --json consumers see no change. A paused
// state with a resume contract (e.g. pull's result:"conflict") is a
// *result*, not an error: ok stays true and the non-zero exit code signals
// the pause.
//
// The envelope schema is append-only; breaking changes bump `schema`.

type agentEnvelope struct {
	Schema int         `json:"schema"`
	OK     bool        `json:"ok"`
	Result any         `json:"result,omitempty"`
	Error  *agentError `json:"error,omitempty"`
}

type agentError struct {
	Code     string      `json:"code"`
	Message  string      `json:"message"`
	Hint     string      `json:"hint,omitempty"`
	Remedies []errRemedy `json:"remedies,omitempty"`
}

// emitAgentResult writes payload as indented JSON to w — wrapped in the
// agent envelope when agent mode is on, bare otherwise. Every JSON-emitting
// command routes through this so the envelope appears everywhere at once.
func emitAgentResult(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if AgentOut() {
		return enc.Encode(agentEnvelope{Schema: 1, OK: true, Result: payload})
	}
	return enc.Encode(payload)
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
	env := agentEnvelope{Schema: 1, OK: false, Error: &agentError{
		Code:     errorCodeFromError(err),
		Message:  err.Error(),
		Hint:     selfRewrite(HintFrom(err)),
		Remedies: remedies,
	}}
	b, merr := json.MarshalIndent(env, "", "  ")
	if merr != nil {
		return FormatError(err)
	}
	return string(b)
}
