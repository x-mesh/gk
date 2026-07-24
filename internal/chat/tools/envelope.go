package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OpenAI-family models sometimes emit a tool call's arguments wrapped in the
// internal envelope their multi-tool dispatcher uses, e.g.
//
//	git_log {"recipient_name":"functions.git_log","parameters":{"range":"main..develop"}}
//
// instead of the flat {"range":"main..develop"} the schema declares. The
// arguments are correct — only the packaging is wrong — so rejecting the call
// wastes a round on a mistake the model did not really make, and it burns the
// FIRST round, before any evidence has been gathered.
//
// Unwrapping happens in Dispatch rather than in strictUnmarshal because only
// Dispatch knows which tool was called, which is what makes the mismatch check
// below possible. It also means every handler benefits without opting in —
// the same reason redaction and capping live there.
const envelopeRecipientPrefix = "functions."

// toolEnvelope is the wrapper shape described above. Both fields are optional
// so a partial envelope (parameters only) is still recognized.
type toolEnvelope struct {
	RecipientName string          `json:"recipient_name"`
	Parameters    json.RawMessage `json:"parameters"`
}

// UnwrapEnvelope returns the real arguments for a call, unwrapping the
// envelope when one is present. Anything that is not unambiguously an
// envelope is returned untouched: a false positive would silently drop real
// arguments, which is far worse than the error a genuine envelope produces.
//
// A recipient naming a DIFFERENT tool than the one invoked is rejected rather
// than unwrapped. That combination means the model addressed one tool and
// called another, so its arguments belong to the tool it named — feeding them
// to the invoked tool would either error confusingly or, worse, succeed with
// arguments meant for something else. Naming both sides lets the model fix the
// call in one step instead of guessing.
func UnwrapEnvelope(toolName string, raw json.RawMessage) (json.RawMessage, error) {
	if !looksLikeEnvelope(raw) {
		return raw, nil
	}
	var env toolEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return raw, nil
	}
	if len(env.Parameters) == 0 {
		return raw, nil
	}

	recipient := strings.TrimPrefix(strings.TrimSpace(env.RecipientName), envelopeRecipientPrefix)
	if recipient != "" && recipient != toolName {
		return nil, fmt.Errorf(
			"tool call mismatch: you invoked %q but addressed %q in recipient_name. "+
				"Call %q directly with its own arguments as the top-level object — "+
				"do not wrap them in recipient_name/parameters",
			toolName, recipient, recipient)
	}
	return env.Parameters, nil
}

// looksLikeEnvelope reports whether raw is a JSON object whose keys are drawn
// ONLY from the envelope's own vocabulary, and which carries "parameters".
// Requiring the key set to be exhausted is what keeps a real input that merely
// happens to have a "parameters" field from being unwrapped.
func looksLikeEnvelope(raw json.RawMessage) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	if _, ok := probe["parameters"]; !ok {
		return false
	}
	for k := range probe {
		if k != "parameters" && k != "recipient_name" {
			return false
		}
	}
	// The wrapped value must itself be an object; "parameters": "..." is a
	// field named parameters, not an envelope.
	var inner map[string]json.RawMessage
	return json.Unmarshal(probe["parameters"], &inner) == nil
}
