package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// maxSuggestQuery bounds the intent string the model may send. A suggestion
// lookup is a keyword match over gk's own command tree, not a search engine:
// anything longer is a pasted diff or a hallucinated essay, and matching it
// would only produce noise.
const maxSuggestQuery = 200

// suggestInput is the model-supplied intent, e.g. "delete branches whose
// remote is gone" or "release a new version".
type suggestInput struct {
	Intent string `json:"intent"`
}

// RegisterSuggestTools adds gk_suggest: a lookup of gk's OWN command tree by
// intent keywords. It exists to make the follow-up suggestion at the end of a
// chat answer a lookup rather than an invention.
//
// The distinction matters more here than anywhere else in the registry. Every
// other tool reads the repository, so the model's claims are grounded by
// construction. A "what should I run next?" suggestion is inherently forward-
// looking — nothing in the repository can confirm it — so left to prior
// knowledge the model will confidently name subcommands and flags that do not
// exist in THIS build of gk. Routing it through the real cobra tree means the
// worst failure mode degrades from "a plausible command that errors out" to
// "no suggestion".
//
// lookup is injected by the CLI layer for the same import-cycle reason
// RegisterContextTools takes a collector: the cobra command tree lives in
// internal/cli, the layer ABOVE this package that wires up the registry.
// Injecting it also keeps the catalogue honest — it is built by walking the
// live root command, so a command added, renamed, or removed anywhere in gk
// changes what this tool can suggest without anyone updating a table here.
func RegisterSuggestTools(r *Registry, lookup func(ctx context.Context, intent string) (string, error)) {
	r.Register(Tool{
		Name: "gk_suggest",
		Description: "Map an intent to gk commands, returning each match's full command path, one-line " +
			"summary, notable flags, and usage example. THIS IS NOT AN EXPLORATION TOOL: it reads gk's " +
			"command list, never the repository, so it cannot find code, explain behaviour, or answer " +
			"any question about THIS project's source — use git_grep/file_read/git_log for that, " +
			"including when the question is about gk's own implementation. Call this only once you know " +
			"what you want to tell the user to RUN: the command you are about to name in your answer, " +
			"or a follow-up action you are about to suggest after it. Every result comes from this " +
			"build's real command tree, so prefer its exact spelling over your own recollection of gk's " +
			"CLI; if it returns no match, gk has no such command — say so or stay silent instead of " +
			"inventing one. This does NOT run anything: gk chat is read-only.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"intent":{"type":"string","description":"What the user would want to do next, in a few words (e.g. \"clean up merged branches\", \"undo the last commit\", \"cut a release\"). Keywords work better than sentences."}` +
			`},"required":["intent"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in suggestInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "", err
			}
			intent := strings.TrimSpace(in.Intent)
			if intent == "" {
				return "", errors.New("gk_suggest: intent is required")
			}
			if len(intent) > maxSuggestQuery {
				intent = intent[:maxSuggestQuery]
			}
			return lookup(ctx, intent)
		},
	})
}
