package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/chat"
	"github.com/x-mesh/gk/internal/config"
)

// chatSequencedCaller returns a scripted reply/err pair per call index —
// unlike chatScriptedCaller (chat_agent_test.go), which only ever has one
// canned reply, this drives the engine through MULTIPLE rounds (e.g.
// round 0 succeeds with a tool call, round 1 then fails) so the
// round-0-only fallback restriction (runFirstChatTurn / chat.
// ErrFirstRoundFailed) can be exercised precisely.
type chatSequencedCaller struct {
	replies []provider.ChatResult
	errs    []error
	calls   int
}

func (c *chatSequencedCaller) ChatWithTools(context.Context, provider.ChatInput) (provider.ChatResult, error) {
	i := c.calls
	c.calls++
	if i < len(c.errs) && c.errs[i] != nil {
		return provider.ChatResult{}, c.errs[i]
	}
	if i < len(c.replies) {
		return c.replies[i], nil
	}
	return provider.ChatResult{Text: "done", StopReason: "end_turn"}, nil
}

// TestRunFirstChatTurn_FallsBackOnFirstRoundFailure confirms the core
// session-start fallback contract: when the primary candidate's very
// first provider round fails (chat.ErrFirstRoundFailed — no assistant/
// tool message exists yet), runFirstChatTurn restarts the session with
// the next candidate instead of surfacing the primary's error.
func TestRunFirstChatTurn_FallsBackOnFirstRoundFailure(t *testing.T) {
	cmd, _, _ := chatTestCmd(t)
	engine := &chat.Engine{Registry: chatTestRegistry()}
	candidates := []chatCandidate{
		{prov: fakeChatProvider{name: "primary"}, caller: &chatScriptedCaller{err: errors.New("connection refused")}},
		{prov: fakeChatProvider{name: "secondary"}, caller: &chatScriptedCaller{reply: provider.ChatResult{Text: "hi from secondary", StopReason: "end_turn"}}},
	}

	res, prov, _, err := runFirstChatTurn(cmd, engine, candidates, "en", "question")
	if err != nil {
		t.Fatalf("runFirstChatTurn: %v", err)
	}
	if prov.Name() != "secondary" {
		t.Errorf("winning provider = %q, want %q", prov.Name(), "secondary")
	}
	if res.Text != "hi from secondary" {
		t.Errorf("res.Text = %q, want %q", res.Text, "hi from secondary")
	}
	// The primary's failed attempt must have been rolled back (RunTurn's
	// own guarantee) — only the winning candidate's turn (user +
	// assistant) should remain.
	if h := engine.History(); len(h) != 2 || h[0].Role != "user" || h[1].Text != "hi from secondary" {
		t.Errorf("history = %+v, want exactly the winning candidate's turn", h)
	}
}

// TestRunFirstChatTurn_NoFallbackPastRoundZero confirms the OTHER half
// of the contract: once the primary candidate's round 0 succeeds (here,
// with a tool call), a LATER round's failure is never retried against a
// different provider — the session already committed to a vendor-
// specific tool_use ID, and silently switching providers mid-turn would
// corrupt it. The error must propagate as-is (not wrapped in
// chat.ErrFirstRoundFailed) and the secondary candidate must never be
// invoked.
func TestRunFirstChatTurn_NoFallbackPastRoundZero(t *testing.T) {
	cmd, _, _ := chatTestCmd(t)
	engine := &chat.Engine{Registry: chatTestRegistry(), MaxToolRounds: 5}
	primary := &chatSequencedCaller{
		replies: []provider.ChatResult{
			{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)}}, StopReason: "tool_use"},
		},
		errs: []error{nil, errors.New("network blip on round 2")},
	}
	secondaryCalled := false
	secondary := scriptedFn(func() (provider.ChatResult, error) {
		secondaryCalled = true
		return provider.ChatResult{Text: "should never be reached", StopReason: "end_turn"}, nil
	})
	candidates := []chatCandidate{
		{prov: fakeChatProvider{name: "primary"}, caller: primary},
		{prov: fakeChatProvider{name: "secondary"}, caller: secondary},
	}

	_, prov, _, err := runFirstChatTurn(cmd, engine, candidates, "en", "question")
	if err == nil {
		t.Fatal("want an error — round 1 failed")
	}
	if errors.Is(err, chat.ErrFirstRoundFailed) {
		t.Errorf("err wraps chat.ErrFirstRoundFailed, want a bare later-round error: %v", err)
	}
	if prov.Name() != "primary" {
		t.Errorf("prov = %q, want primary (no fallback once round 0 succeeded)", prov.Name())
	}
	if secondaryCalled {
		t.Error("secondary candidate must never be invoked once primary got past round 0")
	}
}

// scriptedFn adapts a plain function to provider.ToolCaller — a minimal
// spy for tests that only need to observe whether a candidate was ever
// invoked.
type scriptedFn func() (provider.ChatResult, error)

func (f scriptedFn) ChatWithTools(context.Context, provider.ChatInput) (provider.ChatResult, error) {
	return f()
}

// TestRunFirstChatTurn_AllCandidatesFailListsReasons confirms the
// terminal case: every candidate fails at round 0, so the chain is
// exhausted after exactly one pass — no infinite restart — and the
// returned error names each candidate alongside its own failure reason.
func TestRunFirstChatTurn_AllCandidatesFailListsReasons(t *testing.T) {
	cmd, _, _ := chatTestCmd(t)
	engine := &chat.Engine{Registry: chatTestRegistry()}
	candidates := []chatCandidate{
		{prov: fakeChatProvider{name: "primary"}, caller: &chatScriptedCaller{err: errors.New("boom-primary")}},
		{prov: fakeChatProvider{name: "secondary"}, caller: &chatScriptedCaller{err: errors.New("boom-secondary")}},
	}

	_, _, _, err := runFirstChatTurn(cmd, engine, candidates, "en", "question")
	if err == nil {
		t.Fatal("want an error — every candidate failed")
	}
	if !errors.Is(err, errChatFallbackExhausted) {
		t.Errorf("err = %v, want it to wrap errChatFallbackExhausted", err)
	}
	if !strings.Contains(err.Error(), "primary") || !strings.Contains(err.Error(), "boom-primary") {
		t.Errorf("err = %v, want primary's failure reason listed", err)
	}
	if !strings.Contains(err.Error(), "secondary") || !strings.Contains(err.Error(), "boom-secondary") {
		t.Errorf("err = %v, want secondary's failure reason listed", err)
	}
	if len(engine.History()) != 0 {
		t.Errorf("history = %+v, want empty — every attempt must roll back", engine.History())
	}
}

// TestRunChatREPL_FirstTurnPendingSurvivesAllCandidatesFailing is the F4
// regression test: when the REPL's very first turn fails on EVERY
// candidate (a still-virgin session, per runFirstChatTurn's contract),
// firstTurnPending must stay true so the NEXT question also gets the
// full fallback chain — not just whichever single candidate happened to
// be tried last.
//
// primary fails on its first call and succeeds on its second; secondary
// always fails. Turn 1: primary's 1st call fails, secondary's 1st call
// fails too → the whole chain is exhausted (matches
// errChatFallbackExhausted). Turn 2, with the fix, calls
// runFirstChatTurn again from candidates[0]: primary's 2nd call now
// succeeds, so turn 2 gets a real answer. Before the fix,
// firstTurnPending had already dropped to false after turn 1 regardless
// of outcome, so turn 2 took the "not pending" branch and called
// engine.Caller directly — left, by runFirstChatTurn's loop, pointing at
// secondary (the last candidate tried) — which was ALSO the second call
// against secondary, and secondary always fails: turn 2 would fail too,
// so the answer text below would never appear in stdout pre-fix.
func TestRunChatREPL_FirstTurnPendingSurvivesAllCandidatesFailing(t *testing.T) {
	primary := &chatSequencedCaller{
		errs:    []error{errors.New("boom-primary-1"), nil},
		replies: []provider.ChatResult{{}, {Text: "answer from primary retry", StopReason: "end_turn"}},
	}
	secondary := &chatSequencedCaller{
		errs: []error{errors.New("boom-secondary-1"), errors.New("boom-secondary-2")},
	}
	candidates := []chatCandidate{
		{prov: fakeChatProvider{name: "primary"}, caller: primary},
		{prov: fakeChatProvider{name: "secondary"}, caller: secondary},
	}
	engine := &chat.Engine{Registry: chatTestRegistry()}
	sess := &chat.Session{ID: "sess-f4"}

	cmd, out, errOut := chatTestCmd(t)
	cmd.SetIn(strings.NewReader("first question\nsecond question\n"))

	if err := runChatREPL(cmd, engine, candidates, sess, "en", nil); err != nil {
		t.Fatalf("runChatREPL: %v", err)
	}

	if !strings.Contains(out.String(), "answer from primary retry") {
		t.Errorf("turn 2 must succeed via the retried fallback chain (firstTurnPending must survive turn 1's total failure); stdout:\n%s\nstderr:\n%s", out.String(), errOut.String())
	}
}

// TestResolveChatProviderChain_TimeoutFloorsToRoundTimeout is the F5
// regression test at the chat.go layer: the per-attempt HTTP timeout
// (Timeout) must ALSO be raised to at least roundTimeout, not just
// RetryBudget. Leaving Timeout at the provider's short do/ask-sized
// default (ai.nvidia.timeout, unset here → the adapter's own 60s default)
// truncates any chat round whose model legitimately takes longer than
// that to finish — http.Client.Timeout bounds response BODY reads too,
// so a single full SSE stream over the short default got cut off
// mid-stream regardless of how much RetryBudget headroom remained. This
// replaces TestResolveChatProviderChain_RetryBudgetIndependentOfTimeout,
// which pinned the OPPOSITE (buggy) expectation — that Timeout must NOT
// match roundTimeout; that was itself F5's bug. RetryBudget is still a
// separate field (provider.Nvidia.RetryBudget) — this test only pins
// that both end up set to roundTimeout here, not that they must differ.
func TestResolveChatProviderChain_TimeoutFloorsToRoundTimeout(t *testing.T) {
	ai := config.AIConfig{Provider: "nvidia"}
	ai.Nvidia.APIKey = "test-key"
	roundTimeout := 120 * time.Second

	candidates, err := resolveChatProviderChain(context.Background(), ai, "", roundTimeout)
	if err != nil {
		t.Fatalf("resolveChatProviderChain: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 (explicit ai.Provider pins a single candidate)", len(candidates))
	}
	nv, ok := candidates[0].prov.(*provider.Nvidia)
	if !ok {
		t.Fatalf("prov = %T, want *provider.Nvidia", candidates[0].prov)
	}
	if nv.Timeout != roundTimeout {
		t.Errorf("Timeout = %v, want it floored up to roundTimeout %v (ai.nvidia.timeout unset, so the adapter's own short default would otherwise apply)", nv.Timeout, roundTimeout)
	}
	if nv.RetryBudget != roundTimeout {
		t.Errorf("RetryBudget = %v, want %v", nv.RetryBudget, roundTimeout)
	}
}

// TestResolveChatProviderChain_TimeoutKeepsExplicitLargerConfig confirms
// the F5 floor never LOWERS an intentionally larger ai.<provider>.timeout
// — only an unset/short value gets raised to roundTimeout.
func TestResolveChatProviderChain_TimeoutKeepsExplicitLargerConfig(t *testing.T) {
	ai := config.AIConfig{Provider: "nvidia"}
	ai.Nvidia.APIKey = "test-key"
	ai.Nvidia.Timeout = "300s"
	roundTimeout := 120 * time.Second

	candidates, err := resolveChatProviderChain(context.Background(), ai, "", roundTimeout)
	if err != nil {
		t.Fatalf("resolveChatProviderChain: %v", err)
	}
	nv, ok := candidates[0].prov.(*provider.Nvidia)
	if !ok {
		t.Fatalf("prov = %T, want *provider.Nvidia", candidates[0].prov)
	}
	if nv.Timeout != 300*time.Second {
		t.Errorf("Timeout = %v, want the explicit larger config value (300s) preserved, not clamped down to roundTimeout", nv.Timeout)
	}
}

// TestResolveChatProviderChain_ErrorBranches covers the three ways
// resolveChatProviderChain can fail — none of them exercised before this
// task: an explicit ai.Provider that builds fine but doesn't implement
// ToolCaller (a CLI-type provider — gemini/qwen/kiro, per gk chat's own
// docs), an explicit ai.Provider that fails to build at all (unknown
// name), and auto-detect (ai.Provider=="") exhausting every ToolCaller-
// capable candidate because none is Available().
func TestResolveChatProviderChain_ErrorBranches(t *testing.T) {
	cases := []struct {
		name          string
		setup         func(t *testing.T) config.AIConfig
		wantErrSubstr string
	}{
		{
			name: "explicit provider without tool-calling support",
			setup: func(t *testing.T) config.AIConfig {
				return config.AIConfig{Provider: "gemini"}
			},
			wantErrSubstr: "does not support tool calling",
		},
		{
			name: "explicit provider fails to build",
			setup: func(t *testing.T) config.AIConfig {
				return config.AIConfig{Provider: "totally-bogus-provider"}
			},
			wantErrSubstr: "provider:",
		},
		{
			name: "auto-detect exhausts every candidate",
			setup: func(t *testing.T) config.AIConfig {
				// Every ToolCaller-capable adapter (anthropic/openai/nvidia/
				// groq) falls back to its own env var when APIKey is unset
				// (see e.g. Anthropic.apiKey) — clearing all four makes
				// Available() fail for each regardless of what the dev
				// machine's real shell environment happens to export.
				for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "NVIDIA_API_KEY", "GROQ_API_KEY"} {
					t.Setenv(key, "")
				}
				return config.AIConfig{}
			},
			wantErrSubstr: "no tool-calling provider available",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ai := c.setup(t)
			_, err := resolveChatProviderChain(context.Background(), ai, "", 30*time.Second)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), c.wantErrSubstr) {
				t.Errorf("err = %v, want it to contain %q", err, c.wantErrSubstr)
			}
		})
	}
}

// TestResolveChatProviderChain_AutoDetectFindsAvailableCandidate confirms
// the auto-detect path's success shape: with only one ToolCaller-capable
// provider Available() (anthropic, via an explicit APIKey — no real
// credential needed since Available() is a local key-presence check, not a
// network probe), resolveChatProviderChain returns exactly that one
// candidate in aiAutoOrder's position, not every adapter it built.
func TestResolveChatProviderChain_AutoDetectFindsAvailableCandidate(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "NVIDIA_API_KEY", "GROQ_API_KEY"} {
		t.Setenv(key, "")
	}
	ai := config.AIConfig{}
	ai.Anthropic.APIKey = "test-key"

	candidates, err := resolveChatProviderChain(context.Background(), ai, "", 30*time.Second)
	if err != nil {
		t.Fatalf("resolveChatProviderChain: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v, want exactly 1 (only anthropic is Available())", candidates)
	}
	if candidates[0].prov.Name() != "anthropic" {
		t.Errorf("candidates[0].prov.Name() = %q, want %q", candidates[0].prov.Name(), "anthropic")
	}
	if candidates[0].caller == nil {
		t.Error("candidates[0].caller is nil, want the resolved ToolCaller adapter")
	}
}

// TestRunFirstChatTurn_SingleCandidateTimeoutNotMaskedAsNoProvider pins
// the 3rd-panel finding: with a SINGLE candidate, a first-round timeout
// must classify as round_timeout, not be wrapped in errChatFallbackExhaust-
// ed (which agent mode reports as state:blocked/no_provider with a
// `gk doctor` remedy — the wrong diagnosis for a slow model). There is no
// other provider to "fall back" to, so the real cause must survive.
func TestRunFirstChatTurn_SingleCandidateTimeoutNotMaskedAsNoProvider(t *testing.T) {
	cmd, _, _ := chatTestCmd(t)
	engine := &chat.Engine{Registry: chatTestRegistry()}
	candidates := []chatCandidate{
		{prov: fakeChatProvider{name: "only"}, caller: &chatScriptedCaller{err: context.DeadlineExceeded}},
	}

	_, _, _, err := runFirstChatTurn(cmd, engine, candidates, "en", "q")
	if err == nil {
		t.Fatal("want an error — the only candidate timed out")
	}
	if errors.Is(err, errChatFallbackExhausted) {
		t.Errorf("single-candidate first-round timeout was masked as no_provider: %v", err)
	}
	if got := classifyChatTurnError(err); got != chatErrRoundTimeout {
		t.Errorf("classifyChatTurnError = %v, want chatErrRoundTimeout (a deadline must not become no_provider)", got)
	}
}
