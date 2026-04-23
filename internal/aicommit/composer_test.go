package aicommit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestComposeAllFirstAttemptClean(t *testing.T) {
	p := provider.NewFake()
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: "add classifier", Model: "fake-v1"},
	}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs: %+v", msgs)
	}
	if msgs[0].Attempts != 1 {
		t.Errorf("Attempts: want 1, got %d", msgs[0].Attempts)
	}
	if msgs[0].Subject != "add classifier" {
		t.Errorf("Subject: %q", msgs[0].Subject)
	}
}

func TestComposeAllRetriesOnLintFail(t *testing.T) {
	p := provider.NewFake()
	// Attempt 1: subject way too long → lint fails.
	longSubj := strings.Repeat("x", 200)
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: longSubj},
		{Subject: "short and clean"},
	}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if msgs[0].Attempts != 2 {
		t.Errorf("Attempts: want 2 (retry), got %d", msgs[0].Attempts)
	}
	if msgs[0].Subject != "short and clean" {
		t.Errorf("Subject: %q", msgs[0].Subject)
	}
}

func TestComposeAllFailsAfterMaxAttempts(t *testing.T) {
	p := provider.NewFake()
	// All three attempts return lint-violating subjects.
	badSubj := strings.Repeat("y", 200)
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: badSubj},
		{Subject: badSubj},
		{Subject: badSubj},
	}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	_, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		MaxAttempts:      3,
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err == nil {
		t.Fatal("want error after max retries")
	}
	if !strings.Contains(err.Error(), "commitlint failed after 3 attempts") {
		t.Errorf("err: %v", err)
	}
}

func TestComposeAllFeedsRetryContext(t *testing.T) {
	p := provider.NewFake()
	var capturedAttempts [][]provider.AttemptFeedback
	p.OnCompose = func(in provider.ComposeInput) {
		capturedAttempts = append(capturedAttempts, in.PreviousAttempts)
	}
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: strings.Repeat("z", 200)}, // triggers retry
		{Subject: "clean subject"},
	}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	_, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if len(capturedAttempts) != 2 {
		t.Fatalf("compose invocations: %d", len(capturedAttempts))
	}
	if len(capturedAttempts[0]) != 0 {
		t.Errorf("first call should have no history, got %+v", capturedAttempts[0])
	}
	if len(capturedAttempts[1]) != 1 {
		t.Fatalf("second call should have 1 history entry, got %d", len(capturedAttempts[1]))
	}
	if !strings.Contains(strings.Join(capturedAttempts[1][0].Issues, " "), "subject-max-length") {
		t.Errorf("issues not threaded into retry: %+v", capturedAttempts[1][0].Issues)
	}
}

func TestComposeAllProviderErrorBubbles(t *testing.T) {
	p := provider.NewFake()
	p.ComposeErrs = []error{errors.New("provider down")}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	_, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "provider down") {
		t.Errorf("err: %v", err)
	}
}
