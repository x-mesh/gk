package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/testutil"
)

// remoteCallerFakeCaller is a minimal provider.ToolCaller double —
// remoteGuardedCaller.ChatWithTools delegates to it only after the remote-
// policy re-check passes, so tests assert on callCount to prove a blocked
// call never reaches it.
type remoteCallerFakeCaller struct {
	callCount int
	res       provider.ChatResult
	err       error
}

func (f *remoteCallerFakeCaller) ChatWithTools(context.Context, provider.ChatInput) (provider.ChatResult, error) {
	f.callCount++
	return f.res, f.err
}

// remoteCallerSummarizerProvider is a fakeChatProvider (chat_agent_test.go)
// that ALSO implements provider.Summarizer — remoteGuardedCaller.Summarize
// type-asserts r.prov (the session's own Provider), NOT r.inner, so a
// double covering that branch must satisfy both interfaces on the SAME
// value.
type remoteCallerSummarizerProvider struct {
	fakeChatProvider
	sumCalls int
	sumRes   provider.SummarizeResult
	sumErr   error
}

func (p *remoteCallerSummarizerProvider) Summarize(context.Context, provider.SummarizeInput) (provider.SummarizeResult, error) {
	p.sumCalls++
	return p.sumRes, p.sumErr
}

// remoteCallerFlags builds a *pflag.FlagSet with --repo pointing at a fresh
// git repo whose .gk.yaml (when gkYAML != "") config.Load will pick up via
// its --repo-flag discovery path (internal/config/load.go), independent of
// the test binary's own cwd. Returns the flag set only — callers that don't
// need the repo dir afterward can ignore the second return.
func remoteCallerFlags(t *testing.T, gkYAML string) *pflag.FlagSet {
	t.Helper()
	repo := testutil.NewRepo(t)
	if gkYAML != "" {
		repo.WriteFile(".gk.yaml", gkYAML)
	}
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("repo", repo.Dir, "")
	return fs
}

// remoteBlockedYAML sets ai.commit.allow_remote=false — the default is
// true (config.Defaults), so ensureRemoteAllowed only blocks a
// LocalityRemote provider (fakeChatProvider always is) when this is
// explicitly turned off.
const remoteBlockedYAML = "ai:\n  commit:\n    allow_remote: false\n"

// brokenYAML gives config.Load a value it cannot decode (a non-numeric
// string into an int field) so it returns a non-nil error — the "config
// unreadable" branch remoteGuardedCaller must fail closed on. Confirmed
// empirically: viper/mapstructure tolerates a scalar written where a
// nested struct is expected (silently drops it), but a genuinely
// unparseable scalar for a typed field (int) surfaces as a real decode
// error, exactly like a hand-edited .gk.yaml typo would in production.
const brokenYAML = "ai:\n  chat:\n    max_tool_rounds: \"not-a-number\"\n"

// TestRemoteGuardedCaller_ChatWithTools_ConfigUnreadable confirms the
// fail-closed contract: when the per-round config re-read itself errors,
// ChatWithTools must return an error WITHOUT ever reaching the inner
// caller — a broken config file must stop remote calls, not silently keep
// whatever policy was last known (the GlobalConfigHealthy lesson this
// method's docstring cites).
func TestRemoteGuardedCaller_ChatWithTools_ConfigUnreadable(t *testing.T) {
	fs := remoteCallerFlags(t, brokenYAML)
	inner := &remoteCallerFakeCaller{}
	caller := remoteGuardedCaller{inner: inner, prov: fakeChatProvider{name: "test-provider"}, flags: fs}

	_, err := caller.ChatWithTools(context.Background(), provider.ChatInput{})
	if err == nil {
		t.Fatal("want an error when config.Load fails")
	}
	if !strings.Contains(err.Error(), "config unreadable") {
		t.Errorf("err = %v, want it to mention config unreadable", err)
	}
	if inner.callCount != 0 {
		t.Errorf("inner.ChatWithTools called %d times, want 0 (fail closed before delegating)", inner.callCount)
	}
}

// TestRemoteGuardedCaller_ChatWithTools_RemoteBlocked confirms a per-round
// re-check that finds ai.commit.allow_remote=false blocks the call — the
// mid-session policy-flip scenario this wrapper exists for (the user
// disables remote calls between turns of a long REPL session).
func TestRemoteGuardedCaller_ChatWithTools_RemoteBlocked(t *testing.T) {
	fs := remoteCallerFlags(t, remoteBlockedYAML)
	inner := &remoteCallerFakeCaller{}
	caller := remoteGuardedCaller{inner: inner, prov: fakeChatProvider{name: "test-provider"}, flags: fs}

	_, err := caller.ChatWithTools(context.Background(), provider.ChatInput{})
	if err == nil {
		t.Fatal("want an error when ai.commit.allow_remote=false blocks a remote provider")
	}
	if !strings.Contains(err.Error(), "allow_remote") {
		t.Errorf("err = %v, want it to mention allow_remote", err)
	}
	if inner.callCount != 0 {
		t.Errorf("inner.ChatWithTools called %d times, want 0 (blocked before delegating)", inner.callCount)
	}
}

// TestRemoteGuardedCaller_ChatWithTools_Success confirms the pass-through
// case: a readable config with the default allow_remote=true delegates to
// the inner caller exactly once and returns its result unchanged.
func TestRemoteGuardedCaller_ChatWithTools_Success(t *testing.T) {
	fs := remoteCallerFlags(t, "")
	want := provider.ChatResult{Text: "hi", StopReason: "end_turn"}
	inner := &remoteCallerFakeCaller{res: want}
	caller := remoteGuardedCaller{inner: inner, prov: fakeChatProvider{name: "test-provider"}, flags: fs}

	got, err := caller.ChatWithTools(context.Background(), provider.ChatInput{})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if got.Text != want.Text {
		t.Errorf("got.Text = %q, want %q", got.Text, want.Text)
	}
	if inner.callCount != 1 {
		t.Errorf("inner.ChatWithTools called %d times, want 1", inner.callCount)
	}
}

// TestRemoteGuardedCaller_Summarize_ConfigUnreadable mirrors the
// ChatWithTools case for /compact's path: a config.Load failure must fail
// closed without ever reaching the Summarizer type assertion.
func TestRemoteGuardedCaller_Summarize_ConfigUnreadable(t *testing.T) {
	fs := remoteCallerFlags(t, brokenYAML)
	prov := &remoteCallerSummarizerProvider{fakeChatProvider: fakeChatProvider{name: "test-provider"}}
	caller := remoteGuardedCaller{inner: &remoteCallerFakeCaller{}, prov: prov, flags: fs}

	_, err := caller.Summarize(context.Background(), provider.SummarizeInput{})
	if err == nil {
		t.Fatal("want an error when config.Load fails")
	}
	if !strings.Contains(err.Error(), "config unreadable") {
		t.Errorf("err = %v, want it to mention config unreadable", err)
	}
	if prov.sumCalls != 0 {
		t.Errorf("Summarize called %d times, want 0 (fail closed before delegating)", prov.sumCalls)
	}
}

// TestRemoteGuardedCaller_Summarize_RemoteBlocked mirrors the
// ChatWithTools remote-blocked case for /compact.
func TestRemoteGuardedCaller_Summarize_RemoteBlocked(t *testing.T) {
	fs := remoteCallerFlags(t, remoteBlockedYAML)
	prov := &remoteCallerSummarizerProvider{fakeChatProvider: fakeChatProvider{name: "test-provider"}}
	caller := remoteGuardedCaller{inner: &remoteCallerFakeCaller{}, prov: prov, flags: fs}

	_, err := caller.Summarize(context.Background(), provider.SummarizeInput{})
	if err == nil {
		t.Fatal("want an error when ai.commit.allow_remote=false blocks a remote provider")
	}
	if !strings.Contains(err.Error(), "allow_remote") {
		t.Errorf("err = %v, want it to mention allow_remote", err)
	}
	if prov.sumCalls != 0 {
		t.Errorf("Summarize called %d times, want 0 (blocked before delegating)", prov.sumCalls)
	}
}

// TestRemoteGuardedCaller_Summarize_UnsupportedProvider confirms a session
// provider that does NOT implement provider.Summarizer degrades to a named
// error instead of a panic on the failed type assertion — mirrors
// handleChatCompact's own unsupported-provider test (chat_compact_test.go)
// one layer down, at the wrapper that performs the assertion.
func TestRemoteGuardedCaller_Summarize_UnsupportedProvider(t *testing.T) {
	fs := remoteCallerFlags(t, "")
	caller := remoteGuardedCaller{inner: &remoteCallerFakeCaller{}, prov: fakeChatProvider{name: "test-provider"}, flags: fs}

	_, err := caller.Summarize(context.Background(), provider.SummarizeInput{})
	if err == nil {
		t.Fatal("want an error when the provider does not implement Summarizer")
	}
	if !strings.Contains(err.Error(), "does not support Summarize") {
		t.Errorf("err = %v, want the unsupported-Summarize message", err)
	}
}

// TestRemoteGuardedCaller_Summarize_Success confirms the pass-through case
// delegates to r.prov.(provider.Summarizer) — NOT r.inner — exactly once
// and returns its result unchanged.
func TestRemoteGuardedCaller_Summarize_Success(t *testing.T) {
	fs := remoteCallerFlags(t, "")
	want := provider.SummarizeResult{Text: "a dense digest", Model: "m", TokensUsed: 12}
	prov := &remoteCallerSummarizerProvider{fakeChatProvider: fakeChatProvider{name: "test-provider"}, sumRes: want}
	innerCaller := &remoteCallerFakeCaller{}
	caller := remoteGuardedCaller{inner: innerCaller, prov: prov, flags: fs}

	got, err := caller.Summarize(context.Background(), provider.SummarizeInput{})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got.Text != want.Text {
		t.Errorf("got.Text = %q, want %q", got.Text, want.Text)
	}
	if prov.sumCalls != 1 {
		t.Errorf("Summarize called %d times, want 1", prov.sumCalls)
	}
	if innerCaller.callCount != 0 {
		t.Errorf("inner.ChatWithTools called %d times, want 0 — Summarize must go through r.prov, not r.inner", innerCaller.callCount)
	}
}
