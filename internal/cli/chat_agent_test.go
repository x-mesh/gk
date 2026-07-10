package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/chat"
	"github.com/x-mesh/gk/internal/chat/tools"
)

// TestChatJSONResultAgentState pins the Failure→state mapping chatJSONResult
// implements agentStater with: ErrMaxRounds is a clearable precondition
// (blocked), a round timeout is not fixed by retrying as-is (error),
// no_provider is clearable the same way max_rounds is (blocked), a canceled
// turn produced no answer so it must NOT default to "ok" (error — this is
// the case that was missing before classifyChatTurnError existed), an
// unclassified failure ("error", F1's regression: chatOneShotFailure's
// switch previously had no default case, so an unrecognized error ALSO
// left Failure=="" and fell through to "ok") likewise must not default to
// "ok", and a successful turn (Failure=="") defers to emitAgentResult's
// own "ok" default.
func TestChatJSONResultAgentState(t *testing.T) {
	cases := []struct {
		name    string
		failure string
		want    string
	}{
		{"success", "", ""},
		{"max-rounds", "max_rounds", envStateBlocked},
		{"round-timeout", "round_timeout", envStateError},
		{"no-provider", "no_provider", envStateBlocked},
		{"canceled", "canceled", envStateError},
		{"unclassified error (F1)", "error", envStateError},
		{"unknown falls back to ok", "something-unrecognized", ""},
	}
	for _, c := range cases {
		if got := (chatJSONResult{Failure: c.failure}).agentState(); got != c.want {
			t.Errorf("%s: agentState() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestClassifyChatTurnError pins the single classification both
// chatOneShotFailure and runChatREPL branch on. context.Canceled must win
// even when a raw error string ALSO happens to contain "deadline exceeded"
// text (the isDeadlineErr heuristic), a bare unrelated error must fall
// through to chatErrOther rather than matching anything by accident, and
// (F2's regression) an exhausted fallback chain must classify as
// chatErrNoProvider even when EVERY candidate's own failure reason
// happened to be a timeout — errChatFallbackExhausted's message joins
// those reasons verbatim (see runFirstChatTurn), so the joined text
// itself contains "deadline exceeded"; the exact errors.Is(errChatFallbackExhausted)
// check must win that race over isDeadlineErr's substring heuristic, the
// same way context.Canceled already wins it.
func TestClassifyChatTurnError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want chatTurnErrorKind
	}{
		{"bare context.Canceled", context.Canceled, chatErrCanceled},
		{"wrapped context.Canceled", fmt.Errorf("chat: %w", context.Canceled), chatErrCanceled},
		{"bare chat.ErrMaxRounds", chat.ErrMaxRounds, chatErrMaxRounds},
		{"wrapped chat.ErrMaxRounds", fmt.Errorf("turn: %w", chat.ErrMaxRounds), chatErrMaxRounds},
		{"bare context.DeadlineExceeded", context.DeadlineExceeded, chatErrRoundTimeout},
		{"adapter-string deadline (no wrapped sentinel)", errors.New("openai: http call: context deadline exceeded"), chatErrRoundTimeout},
		{"bare errChatFallbackExhausted", errChatFallbackExhausted, chatErrNoProvider},
		{"wrapped errChatFallbackExhausted", fmt.Errorf("turn: %w", errChatFallbackExhausted), chatErrNoProvider},
		{
			name: "errChatFallbackExhausted whose joined reasons mention a deadline (F2)",
			err:  fmt.Errorf("%w:\n  - %s\n  - %s", errChatFallbackExhausted, "anthropic: context deadline exceeded", "openai: context deadline exceeded"),
			want: chatErrNoProvider,
		},
		{"unrelated error", errors.New("connection refused"), chatErrOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyChatTurnError(c.err); got != c.want {
				t.Errorf("classifyChatTurnError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// chatScriptedCaller returns a fixed reply or error to every ChatWithTools
// call — enough to drive the engine into ErrMaxRounds (always returning a
// tool call, never a final answer) or a round-timeout error (returning
// context.DeadlineExceeded on the first round).
type chatScriptedCaller struct {
	reply provider.ChatResult
	err   error
}

func (c *chatScriptedCaller) ChatWithTools(context.Context, provider.ChatInput) (provider.ChatResult, error) {
	if c.err != nil {
		return provider.ChatResult{}, c.err
	}
	return c.reply, nil
}

// fakeChatProvider is a minimal provider.Provider — runChatOneShot only
// calls Name() (spinner label, JSON "provider" field); the rest exist to
// satisfy the interface.
type fakeChatProvider struct{ name string }

func (f fakeChatProvider) Name() string                    { return f.name }
func (f fakeChatProvider) Locality() provider.Locality     { return provider.LocalityRemote }
func (f fakeChatProvider) Available(context.Context) error { return nil }
func (f fakeChatProvider) Classify(context.Context, provider.ClassifyInput) (provider.ClassifyResult, error) {
	return provider.ClassifyResult{}, nil
}
func (f fakeChatProvider) Compose(context.Context, provider.ComposeInput) (provider.ComposeResult, error) {
	return provider.ComposeResult{}, nil
}

func chatTestRegistry() *tools.Registry {
	r := tools.NewRegistry(nil, 0)
	r.Register(tools.Tool{
		Name:        "echo",
		Description: "echo",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	return r
}

func chatTestCmd(t *testing.T) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	return cmd, out, errOut
}

// stdoutEnvelope is the shape emitAgentResult wraps chatJSONResult in.
type chatStdoutEnvelope struct {
	Schema int    `json:"schema"`
	State  string `json:"state"`
	OK     bool   `json:"ok"`
	Result struct {
		chatJSONResult
	} `json:"result"`
}

// TestChatOneShotMaxRounds_Blocked drives a one-shot turn whose model never
// stops calling tools, so the engine gives up with chat.ErrMaxRounds. Under
// GK_AGENT=1 this must produce: a stdout chatJSONResult envelope with
// state:"blocked" and failure:"max_rounds", plus a returned error that
// FormatErrorJSON renders as state:"blocked" with the max_tool_rounds /
// --model remedies — previously both channels reported "error" with no
// remedies at all (the bug this task fixes).
func TestChatOneShotMaxRounds_Blocked(t *testing.T) {
	withAgentMode(t, true)
	cmd, out, _ := chatTestCmd(t)

	looping := provider.ChatResult{
		ToolCalls:  []provider.ToolCall{{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)}},
		StopReason: "tool_use",
	}
	engine := &chat.Engine{
		Caller:        &chatScriptedCaller{reply: looping},
		Registry:      chatTestRegistry(),
		MaxToolRounds: 2,
	}
	prov := fakeChatProvider{name: "test-provider"}
	sess := &chat.Session{ID: "sess-max-rounds"}

	err := runChatOneShot(cmd, engine, prov, sess, "ko", "왜 자꾸 도구만 호출해?")
	if err == nil {
		t.Fatal("want an error when the round budget is exhausted")
	}
	if !errors.Is(err, chat.ErrMaxRounds) {
		t.Fatalf("returned error does not wrap chat.ErrMaxRounds: %v", err)
	}

	// stdout: chatJSONResult envelope, state derived from agentState().
	var env chatStdoutEnvelope
	if uerr := json.Unmarshal(out.Bytes(), &env); uerr != nil {
		t.Fatalf("stdout is not a valid envelope: %v\n%s", uerr, out.String())
	}
	if env.State != envStateBlocked || env.OK {
		t.Errorf("stdout envelope: state=%q ok=%v, want state=%q ok=false", env.State, env.OK, envStateBlocked)
	}
	if env.Result.Failure != "max_rounds" {
		t.Errorf("result.failure = %q, want %q", env.Result.Failure, "max_rounds")
	}
	if env.Result.SessionID != "sess-max-rounds" || env.Result.Provider != "test-provider" {
		t.Errorf("result missing session/provider context: %+v", env.Result)
	}

	// stderr channel (main.go's FormatErrorJSON(err)): state + remedies.
	var errEnv struct {
		State string `json:"state"`
		OK    bool   `json:"ok"`
		Error struct {
			Code     string      `json:"code"`
			Remedies []errRemedy `json:"remedies"`
		} `json:"error"`
	}
	if uerr := json.Unmarshal([]byte(FormatErrorJSON(err)), &errEnv); uerr != nil {
		t.Fatalf("FormatErrorJSON output is not valid JSON: %v", uerr)
	}
	if errEnv.State != envStateBlocked || errEnv.OK {
		t.Errorf("error envelope: state=%q ok=%v, want state=%q ok=false", errEnv.State, errEnv.OK, envStateBlocked)
	}
	if errEnv.Error.Code != "chat-max-rounds" {
		t.Errorf("error.code = %q, want %q", errEnv.Error.Code, "chat-max-rounds")
	}
	if len(errEnv.Error.Remedies) != 2 {
		t.Fatalf("error.remedies = %+v, want 2 entries (max_tool_rounds bump, --model retry)", errEnv.Error.Remedies)
	}
	if !strings.Contains(errEnv.Error.Remedies[0].Command, "max_tool_rounds") {
		t.Errorf("remedies[0] = %+v, want a max_tool_rounds bump", errEnv.Error.Remedies[0])
	}
	if !strings.Contains(errEnv.Error.Remedies[1].Command, "--model") {
		t.Errorf("remedies[1] = %+v, want a --model retry", errEnv.Error.Remedies[1])
	}
}

// TestChatOneShotRoundTimeout_Error drives a one-shot turn whose single
// provider call exceeds ai.chat.round_timeout. This must map to state:
// "error" (not recoverable by retrying the identical turn) with a remedy —
// previously the hint was only ever printed as plain stderr prose, never
// attached to the error, so agent mode got error.remedies:[] (empty).
func TestChatOneShotRoundTimeout_Error(t *testing.T) {
	withAgentMode(t, true)
	cmd, out, _ := chatTestCmd(t)

	engine := &chat.Engine{
		Caller:       &chatScriptedCaller{err: context.DeadlineExceeded},
		Registry:     chatTestRegistry(),
		RoundTimeout: 30_000_000_000, // 30s, exact value irrelevant — never actually waited
	}
	prov := fakeChatProvider{name: "test-provider"}
	sess := &chat.Session{ID: "sess-timeout"}

	err := runChatOneShot(cmd, engine, prov, sess, "ko", "이 함수 왜 바뀌었지?")
	if err == nil {
		t.Fatal("want an error on a round timeout")
	}

	var env chatStdoutEnvelope
	if uerr := json.Unmarshal(out.Bytes(), &env); uerr != nil {
		t.Fatalf("stdout is not a valid envelope: %v\n%s", uerr, out.String())
	}
	if env.State != envStateError || env.OK {
		t.Errorf("stdout envelope: state=%q ok=%v, want state=%q ok=false", env.State, env.OK, envStateError)
	}
	if env.Result.Failure != "round_timeout" {
		t.Errorf("result.failure = %q, want %q", env.Result.Failure, "round_timeout")
	}

	var errEnv struct {
		State string `json:"state"`
		Error struct {
			Remedies []errRemedy `json:"remedies"`
		} `json:"error"`
	}
	if uerr := json.Unmarshal([]byte(FormatErrorJSON(err)), &errEnv); uerr != nil {
		t.Fatalf("FormatErrorJSON output is not valid JSON: %v", uerr)
	}
	if errEnv.State != envStateError {
		t.Errorf("error envelope state = %q, want %q", errEnv.State, envStateError)
	}
	if len(errEnv.Error.Remedies) != 2 {
		t.Fatalf("error.remedies = %+v, want 2 entries (round_timeout bump, --model retry)", errEnv.Error.Remedies)
	}
	if !strings.Contains(errEnv.Error.Remedies[0].Command, "round_timeout") {
		t.Errorf("remedies[0] = %+v, want a round_timeout bump", errEnv.Error.Remedies[0])
	}
}

// TestChatOneShotCanceled_Error is the regression test for the bug
// classifyChatTurnError fixes: before it existed, chatOneShotFailure never
// checked context.Canceled at all, so a Ctrl-C'd one-shot left Failure=="" —
// chatJSONResult's OWN success marker — and agent mode reported state:"ok"
// for a turn that produced no answer. This drives a one-shot turn whose
// caller returns context.Canceled and confirms it now reports state:"error"
// with failure:"canceled", never "ok".
func TestChatOneShotCanceled_Error(t *testing.T) {
	withAgentMode(t, true)
	cmd, out, _ := chatTestCmd(t)

	engine := &chat.Engine{
		Caller:   &chatScriptedCaller{err: context.Canceled},
		Registry: chatTestRegistry(),
	}
	prov := fakeChatProvider{name: "test-provider"}
	sess := &chat.Session{ID: "sess-canceled"}

	err := runChatOneShot(cmd, engine, prov, sess, "ko", "질문")
	if err == nil {
		t.Fatal("want an error when the turn is canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("returned error does not wrap context.Canceled: %v", err)
	}

	var env chatStdoutEnvelope
	if uerr := json.Unmarshal(out.Bytes(), &env); uerr != nil {
		t.Fatalf("stdout is not a valid envelope: %v\n%s", uerr, out.String())
	}
	if env.State != envStateError || env.OK {
		t.Errorf("stdout envelope: state=%q ok=%v, want state=%q ok=false (NOT \"ok\" — the bug this test guards against)", env.State, env.OK, envStateError)
	}
	if env.Result.Failure != "canceled" {
		t.Errorf("result.failure = %q, want %q", env.Result.Failure, "canceled")
	}
	if env.Result.Answer != "" {
		t.Errorf("result.answer = %q, want empty — a canceled turn produced no answer", env.Result.Answer)
	}

	var errEnv struct {
		State string `json:"state"`
		OK    bool   `json:"ok"`
	}
	if uerr := json.Unmarshal([]byte(FormatErrorJSON(err)), &errEnv); uerr != nil {
		t.Fatalf("FormatErrorJSON output is not valid JSON: %v", uerr)
	}
	if errEnv.State != envStateError || errEnv.OK {
		t.Errorf("error envelope: state=%q ok=%v, want state=%q ok=false", errEnv.State, errEnv.OK, envStateError)
	}
}

// TestChatOneShotUnclassifiedError_Error is the F1 regression test: an
// unclassified failure (classifyChatTurnError's chatErrOther — a provider
// 500, an auth error, a malformed JSON reply, or here just a plain
// stdlib error standing in for any of those) must NOT report state:"ok".
// Before this fix, chatOneShotFailure's switch had no default case, so
// this exact scenario left Failure=="" — chatJSONResult's OWN success
// marker — and agentState()'s own "" → "ok" default reported success
// with an empty answer for a turn that outright failed.
func TestChatOneShotUnclassifiedError_Error(t *testing.T) {
	withAgentMode(t, true)
	cmd, out, _ := chatTestCmd(t)

	engine := &chat.Engine{
		Caller:   &chatScriptedCaller{err: errors.New("provider: unexpected 500 from upstream")},
		Registry: chatTestRegistry(),
	}
	prov := fakeChatProvider{name: "test-provider"}
	sess := &chat.Session{ID: "sess-unclassified"}

	err := runChatOneShot(cmd, engine, prov, sess, "ko", "질문")
	if err == nil {
		t.Fatal("want an error when the provider call fails")
	}

	var env chatStdoutEnvelope
	if uerr := json.Unmarshal(out.Bytes(), &env); uerr != nil {
		t.Fatalf("stdout is not a valid envelope: %v\n%s", uerr, out.String())
	}
	if env.State != envStateError || env.OK {
		t.Errorf("stdout envelope: state=%q ok=%v, want state=%q ok=false (NOT \"ok\" — the F1 bug this test guards against)", env.State, env.OK, envStateError)
	}
	if env.Result.Failure != "error" {
		t.Errorf("result.failure = %q, want %q", env.Result.Failure, "error")
	}
	if env.Result.Answer != "" {
		t.Errorf("result.answer = %q, want empty — a failed turn produced no answer", env.Result.Answer)
	}

	var errEnv struct {
		State string `json:"state"`
		OK    bool   `json:"ok"`
	}
	if uerr := json.Unmarshal([]byte(FormatErrorJSON(err)), &errEnv); uerr != nil {
		t.Fatalf("FormatErrorJSON output is not valid JSON: %v", uerr)
	}
	if errEnv.State != envStateError || errEnv.OK {
		t.Errorf("error envelope: state=%q ok=%v, want state=%q ok=false", errEnv.State, errEnv.OK, envStateError)
	}
}
