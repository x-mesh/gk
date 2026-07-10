package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// hangUntilTimeoutClient simulates one (or more) slow/hung HTTP attempts:
// the first hangCalls Do() calls behave like a real net/http.Client whose
// own http.Client.Timeout — independent of whatever context the caller
// passed in — fires after hangFor, UNLESS the context's own deadline is
// even sooner. Every call after that serves the canned responses in
// order. FakeHTTPClient (nvidia_test.go) can't express this: it answers
// instantly and never blocks, so it can't stand in for "one attempt was
// slow" scenarios.
type hangUntilTimeoutClient struct {
	hangCalls int
	hangFor   time.Duration
	responses []*http.Response
	calls     int
}

func (h *hangUntilTimeoutClient) Do(ctx context.Context, _ *http.Request) (*http.Response, error) {
	i := h.calls
	h.calls++
	if i < h.hangCalls {
		select {
		case <-time.After(h.hangFor):
			return nil, fmt.Errorf("simulated http.Client.Timeout after %v", h.hangFor)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	idx := i - h.hangCalls
	if idx < len(h.responses) {
		return h.responses[idx], nil
	}
	return nil, errors.New("hangUntilTimeoutClient: no more responses")
}

// TestNvidiaRetryBudget_AttemptTimeoutIndependentOfLoopBudget is the
// budget-separation regression test: a single slow/hung attempt must be
// cut off at its OWN small per-attempt timeout, not allowed to run
// anywhere near the (much larger) round budget — otherwise the retries
// that budget exists to cover never get a fair chance, and a normal
// response that would have succeeded on the second or third attempt
// never gets the time to happen. This is the exact real-world failure
// pattern t11 fixes: a proxy that occasionally 500s slowly, with
// per-attempt Timeout previously inflated to match the round budget
// (chat.go's old minTimeout hack), meant one slow attempt alone could
// consume the whole round.
func TestNvidiaRetryBudget_AttemptTimeoutIndependentOfLoopBudget(t *testing.T) {
	perAttempt := 40 * time.Millisecond
	loopBudget := 2 * time.Second // stand-in for ai.chat.round_timeout

	client := &hangUntilTimeoutClient{
		hangCalls: 1, // attempt 0 "hangs" the way a slow proxy would
		hangFor:   perAttempt,
		responses: []*http.Response{okResponse(chatResponse{
			Model: "test-model",
			Choices: []chatChoice{{
				Message:      chatMessage{Role: "assistant", Content: "final answer"},
				FinishReason: "stop",
			}},
			Usage: &chatUsage{TotalTokens: 3},
		})},
	}
	nv := &Nvidia{
		Client:      client,
		APIKey:      "test-key",
		Endpoint:    "https://test.example.com/v1/chat/completions",
		Model:       "test-model",
		Timeout:     perAttempt, // per-attempt cap, independent of RetryBudget
		RetryBudget: loopBudget, // the round's own, much larger, final ceiling
		MaxRetry:    3,
		EnvLookup:   func(string) string { return "" },
		SleepFn:     func(context.Context, time.Duration) bool { return true }, // skip real backoff sleeps
	}

	start := time.Now()
	res, err := nv.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "user", Text: "hi"}},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ChatWithTools: %v (the first attempt's hang should not have doomed the whole call)", err)
	}
	if res.Text != "final answer" {
		t.Errorf("Text = %q, want %q", res.Text, "final answer")
	}
	if client.calls != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (one hung attempt + the retry that answers)", client.calls)
	}
	// The hung first attempt must have been treated as bounded by its OWN
	// small per-attempt timeout, leaving virtually the entire loop budget
	// (2s) still available for the retry — proving the two budgets are
	// independent instead of the attempt silently eating the whole thing.
	if elapsed > loopBudget/2 {
		t.Errorf("elapsed = %v, want well under the loop budget %v (a hung attempt should cost ~%v, not consume the round)", elapsed, loopBudget, perAttempt)
	}
}

// TestNvidiaRetryBudget_DefaultsToTimeout confirms RetryBudget's
// zero-value fallback: every existing caller that never sets it (do/ask/
// commit/etc., and every pre-existing test — see
// TestProperty11_RetryTotalTimeWithinTimeout) keeps EXACTLY the single-
// deadline behavior that predates this field.
func TestNvidiaRetryBudget_DefaultsToTimeout(t *testing.T) {
	nv := &Nvidia{Timeout: 45 * time.Second}
	if got := nv.retryBudget(); got != 45*time.Second {
		t.Errorf("retryBudget() = %v, want Timeout (45s) when RetryBudget is unset", got)
	}
	nv.RetryBudget = 120 * time.Second
	if got := nv.retryBudget(); got != 120*time.Second {
		t.Errorf("retryBudget() = %v, want the explicit RetryBudget (120s)", got)
	}
}
