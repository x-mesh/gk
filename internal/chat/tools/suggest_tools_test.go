package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func suggestRegistry(t *testing.T, lookup func(context.Context, string) (string, error)) *Registry {
	t.Helper()
	r := NewRegistry(nil, 0)
	RegisterSuggestTools(r, lookup)
	return r
}

func dispatchSuggest(t *testing.T, r *Registry, input string) provider.ToolResult {
	t.Helper()
	return r.Dispatch(context.Background(), provider.ToolCall{
		ID:    "call-1",
		Name:  "gk_suggest",
		Input: json.RawMessage(input),
	})
}

func TestSuggestToolSchemaIsValidJSON(t *testing.T) {
	r := suggestRegistry(t, func(context.Context, string) (string, error) { return "{}", nil })
	specs := r.Specs()
	if len(specs) != 1 || specs[0].Name != "gk_suggest" {
		t.Fatalf("expected one gk_suggest spec, got %+v", specs)
	}
	var schema map[string]any
	if err := json.Unmarshal(specs[0].InputSchema, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Error("schema must forbid additional properties")
	}
	// The description is the only thing steering the model away from
	// recalling gk's CLI from memory — the whole reason this tool exists.
	if !strings.Contains(specs[0].Description, "read-only") {
		t.Error("description must state that gk chat cannot run the command")
	}
}

func TestSuggestToolPassesIntentThrough(t *testing.T) {
	var got string
	r := suggestRegistry(t, func(_ context.Context, intent string) (string, error) {
		got = intent
		return `{"matches":[]}`, nil
	})
	res := dispatchSuggest(t, r, `{"intent":"  clean up merged branches  "}`)
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if got != "clean up merged branches" {
		t.Errorf("intent = %q, want trimmed %q", got, "clean up merged branches")
	}
}

func TestSuggestToolRejectsEmptyIntent(t *testing.T) {
	called := false
	r := suggestRegistry(t, func(context.Context, string) (string, error) {
		called = true
		return "{}", nil
	})
	for _, in := range []string{`{"intent":""}`, `{"intent":"   "}`, `{}`} {
		res := dispatchSuggest(t, r, in)
		if !res.IsError {
			t.Errorf("input %s: expected an error result, got %q", in, res.Content)
		}
	}
	if called {
		t.Error("lookup must not run for an empty intent")
	}
}

// A pasted diff or a runaway sentence is not an intent; truncating keeps the
// keyword match from degenerating into noise.
func TestSuggestToolCapsIntentLength(t *testing.T) {
	var got string
	r := suggestRegistry(t, func(_ context.Context, intent string) (string, error) {
		got = intent
		return `{"matches":[]}`, nil
	})
	long := strings.Repeat("a", maxSuggestQuery*3)
	if res := dispatchSuggest(t, r, `{"intent":"`+long+`"}`); res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if len(got) != maxSuggestQuery {
		t.Errorf("intent length = %d, want cap %d", len(got), maxSuggestQuery)
	}
}

// Suggestions flow to a remote provider and the on-disk session file like any
// other tool result, so they must pass through the registry's redactor rather
// than around it.
func TestSuggestToolResultIsRedacted(t *testing.T) {
	r := NewRegistry(func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[redacted]") }, 0)
	RegisterSuggestTools(r, func(context.Context, string) (string, error) {
		return `{"matches":[{"command":"gk config set token sk-secret"}]}`, nil
	})
	res := dispatchSuggest(t, r, `{"intent":"set a token"}`)
	if strings.Contains(res.Content, "sk-secret") {
		t.Errorf("result was not redacted: %s", res.Content)
	}
}
