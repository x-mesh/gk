package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestFakeImplementsProvider(t *testing.T) {
	var _ provider.Provider = provider.NewFake()
}

func TestFakeAvailable(t *testing.T) {
	f := provider.NewFake()
	if err := f.Available(context.Background()); err != nil {
		t.Fatalf("Available default: want nil, got %v", err)
	}
	f.AvailableErr = provider.ErrNotInstalled
	if err := f.Available(context.Background()); !errors.Is(err, provider.ErrNotInstalled) {
		t.Errorf("Available after stub: want ErrNotInstalled, got %v", err)
	}
	if len(f.Calls) != 2 || f.Calls[0] != "Available" {
		t.Errorf("Calls: %v", f.Calls)
	}
}

func TestFakeClassifyAndComposeCycleThroughResponses(t *testing.T) {
	f := provider.NewFake()
	f.ClassifyResponses = []provider.ClassifyResult{
		{Groups: []provider.Group{{Type: "feat", Files: []string{"a.go"}}}},
		{Groups: []provider.Group{{Type: "test", Files: []string{"a_test.go"}}}},
	}
	f.ComposeResponses = []provider.ComposeResult{
		{Subject: "first"},
		{Subject: "second"},
	}

	ctx := context.Background()
	first, err := f.Classify(ctx, provider.ClassifyInput{})
	if err != nil {
		t.Fatalf("Classify 1: %v", err)
	}
	if got := first.Groups[0].Type; got != "feat" {
		t.Errorf("first.Groups[0].Type: want feat, got %q", got)
	}

	second, err := f.Classify(ctx, provider.ClassifyInput{})
	if err != nil {
		t.Fatalf("Classify 2: %v", err)
	}
	if got := second.Groups[0].Type; got != "test" {
		t.Errorf("second.Groups[0].Type: want test, got %q", got)
	}

	// Exhausted → zero value, no panic.
	third, err := f.Classify(ctx, provider.ClassifyInput{})
	if err != nil {
		t.Fatalf("Classify 3: %v", err)
	}
	if len(third.Groups) != 0 {
		t.Errorf("third after exhaustion: want empty, got %+v", third)
	}

	c1, err := f.Compose(ctx, provider.ComposeInput{})
	if err != nil {
		t.Fatalf("Compose 1: %v", err)
	}
	if c1.Subject != "first" {
		t.Errorf("Compose first: got %q", c1.Subject)
	}

	if len(f.Calls) != 4 {
		t.Errorf("Calls count: want 4 (3 Classify + 1 Compose), got %d (%v)", len(f.Calls), f.Calls)
	}
}

func TestFakeHooksFire(t *testing.T) {
	f := provider.NewFake()
	var sawClassify, sawCompose bool
	f.OnClassify = func(provider.ClassifyInput) { sawClassify = true }
	f.OnCompose = func(provider.ComposeInput) { sawCompose = true }
	_, _ = f.Classify(context.Background(), provider.ClassifyInput{})
	_, _ = f.Compose(context.Background(), provider.ComposeInput{})
	if !sawClassify || !sawCompose {
		t.Errorf("hooks not invoked: classify=%v compose=%v", sawClassify, sawCompose)
	}
}
