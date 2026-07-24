package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestUnwrapToolEnvelope(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		raw     string
		want    string
		wantErr string
	}{
		{
			// The exact shape observed from a live openai run.
			name: "full envelope for the invoked tool",
			tool: "git_log",
			raw:  `{"recipient_name":"functions.git_log","parameters":{"range":"main..develop","limit":30}}`,
			want: `{"range":"main..develop","limit":30}`,
		},
		{
			name: "envelope without the functions prefix",
			tool: "git_log",
			raw:  `{"recipient_name":"git_log","parameters":{"limit":5}}`,
			want: `{"limit":5}`,
		},
		{
			name: "parameters only, no recipient",
			tool: "git_status",
			raw:  `{"parameters":{"limit":20}}`,
			want: `{"limit":20}`,
		},
		{
			name: "empty parameters object still unwraps",
			tool: "git_context",
			raw:  `{"recipient_name":"functions.git_context","parameters":{}}`,
			want: `{}`,
		},
		{
			// Also observed live: the model invoked git_diff while addressing
			// git_status, so the arguments belong to neither call as issued.
			name:    "recipient names a different tool",
			tool:    "git_diff",
			raw:     `{"recipient_name":"functions.git_status","parameters":{"limit":20}}`,
			wantErr: "tool call mismatch",
		},
		{
			name: "ordinary input is untouched",
			tool: "git_log",
			raw:  `{"range":"main..develop","limit":30}`,
			want: `{"range":"main..develop","limit":30}`,
		},
		{
			// The guard that matters most: a real input carrying its own
			// "parameters" field must never be unwrapped out from under the
			// handler, because that silently discards every other field.
			name: "real input containing a parameters field",
			tool: "some_tool",
			raw:  `{"parameters":{"a":1},"path":"x.go"}`,
			want: `{"parameters":{"a":1},"path":"x.go"}`,
		},
		{
			name: "parameters holding a non-object",
			tool: "some_tool",
			raw:  `{"parameters":"main..develop"}`,
			want: `{"parameters":"main..develop"}`,
		},
		{
			name: "empty input",
			tool: "git_context",
			raw:  ``,
			want: ``,
		},
		{
			name: "malformed json is left for the handler to report",
			tool: "git_log",
			raw:  `{"parameters":`,
			want: `{"parameters":`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnwrapEnvelope(tc.tool, json.RawMessage(tc.raw))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got input %s", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// The mismatch message has one job: let the model fix the call in a single
// step. That requires naming both the tool it invoked and the one it addressed.
func TestUnwrapToolEnvelopeMismatchNamesBothTools(t *testing.T) {
	_, err := UnwrapEnvelope("git_diff", json.RawMessage(`{"recipient_name":"functions.git_status","parameters":{}}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"git_diff", "git_status", "recipient_name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must mention %q: %s", want, err)
		}
	}
}

// End-to-end through Dispatch: an enveloped call must reach the handler with
// flat arguments, which is the whole point — the handler stays unaware.
func TestDispatchUnwrapsEnvelopeBeforeHandler(t *testing.T) {
	var seen string
	r := NewRegistry(nil, 0)
	r.Register(Tool{
		Name:   "git_log",
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, input json.RawMessage) (string, error) {
			seen = string(input)
			return "ok", nil
		},
	})

	res := r.Dispatch(context.Background(), provider.ToolCall{
		ID:    "c1",
		Name:  "git_log",
		Input: json.RawMessage(`{"recipient_name":"functions.git_log","parameters":{"limit":7}}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if seen != `{"limit":7}` {
		t.Errorf("handler saw %s, want flat arguments", seen)
	}
}

func TestDispatchReportsEnvelopeMismatchAsToolError(t *testing.T) {
	called := false
	r := NewRegistry(nil, 0)
	r.Register(Tool{
		Name:   "git_diff",
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, json.RawMessage) (string, error) {
			called = true
			return "ok", nil
		},
	})

	res := r.Dispatch(context.Background(), provider.ToolCall{
		ID:    "c1",
		Name:  "git_diff",
		Input: json.RawMessage(`{"recipient_name":"functions.git_status","parameters":{"limit":20}}`),
	})
	if !res.IsError {
		t.Fatalf("expected an error result, got %q", res.Content)
	}
	if called {
		t.Error("handler must not run for a mismatched envelope")
	}
	// A conversation-killing error would defeat the self-correction path.
	if res.ToolCallID != "c1" {
		t.Errorf("result must carry the call id, got %q", res.ToolCallID)
	}
}
