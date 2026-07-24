package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// defaultResultCap bounds one tool result's bytes sent back to the model.
// 32KB keeps a single hostile/huge file from consuming the whole context
// window; the engine additionally enforces a per-turn cumulative cap.
const defaultResultCap = 32 * 1024

// Tool is one model-callable operation: schema for the vendor API,
// handler for execution. Handlers receive pre-validated JSON input and
// return raw text — redaction and capping happen in Dispatch, uniformly,
// so no individual handler can forget them.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Handler     func(ctx context.Context, input json.RawMessage) (string, error)
}

// Registry owns the tool set and the two non-negotiable result stages:
// redact (secrets never leave the process un-scrubbed — results go to a
// REMOTE provider and are persisted to the session file) and cap.
type Registry struct {
	tools map[string]Tool
	order []string
	// Redact scrubs a tool result before it reaches the model or disk.
	// Injected by the CLI layer (aicommit.Redact + secret patterns); nil
	// means no redaction and is only acceptable in tests.
	Redact func(string) string
	// ResultCap bounds one result's bytes (0 → defaultResultCap).
	ResultCap int
}

// NewRegistry returns an empty registry with the given redactor.
func NewRegistry(redact func(string) string, resultCap int) *Registry {
	if resultCap <= 0 {
		resultCap = defaultResultCap
	}
	return &Registry{
		tools:     make(map[string]Tool),
		Redact:    redact,
		ResultCap: resultCap,
	}
}

// Register adds a tool, keeping registration order for Specs.
func (r *Registry) Register(t Tool) {
	if _, dup := r.tools[t.Name]; !dup {
		r.order = append(r.order, t.Name)
	}
	r.tools[t.Name] = t
}

// Specs renders the registry as vendor-neutral tool definitions.
func (r *Registry) Specs() []provider.ToolSpec {
	out := make([]provider.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		out = append(out, provider.ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	return out
}

// Dispatch executes one model-requested call. Every failure mode returns
// an IsError result instead of an error — the model should see "that
// didn't work" and adapt, not kill the conversation. Execution-time
// validation lives in the handlers (executor.go's defense-in-depth rule:
// never trust that a request which LOOKS safe was validated elsewhere).
func (r *Registry) Dispatch(ctx context.Context, call provider.ToolCall) (res provider.ToolResult) {
	res.ToolCallID = call.ID
	defer func() {
		if p := recover(); p != nil {
			// The panic payload can quote whatever the handler was holding
			// — redact it like any other model-visible text.
			res.Content = r.redact(fmt.Sprintf("tool %s panicked: %v", call.Name, p))
			res.IsError = true
		}
	}()

	t, ok := r.tools[call.Name]
	if !ok {
		res.Content = fmt.Sprintf("unknown tool %q", call.Name)
		res.IsError = true
		return res
	}
	if err := ctx.Err(); err != nil {
		res.Content = err.Error()
		res.IsError = true
		return res
	}

	input, err := UnwrapEnvelope(call.Name, call.Input)
	if err != nil {
		res.Content = r.redact(err.Error())
		res.IsError = true
		return res
	}

	out, err := t.Handler(ctx, input)
	if err != nil {
		// Error text is model-visible; redact it too — a sandbox denial
		// echoes the requested path, and git errors can quote content.
		res.Content = r.redact(err.Error())
		res.IsError = true
		return res
	}
	res.Content = capBytes(r.redact(out), r.ResultCap)
	return res
}

func (r *Registry) redact(s string) string {
	if r.Redact == nil {
		return s
	}
	return r.Redact(s)
}

// capBytes truncates s to max bytes on a rune boundary, appending an
// explicit marker so the model knows data is missing instead of silently
// reasoning over a cut-off view.
func capBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n...[truncated %d bytes]", len(s)-cut)
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
