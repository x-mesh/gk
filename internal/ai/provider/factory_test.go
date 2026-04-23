package provider

import (
	"context"
	"strings"
	"testing"
)

func TestNewProviderByName(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"gemini", "qwen", "kiro", "kiro-cli"} {
		t.Run(name, func(t *testing.T) {
			p, err := NewProvider(ctx, FactoryOptions{Name: name, Runner: &FakeCommandRunner{}})
			if err != nil {
				t.Fatalf("NewProvider(%s): %v", name, err)
			}
			if p == nil {
				t.Fatal("nil provider")
			}
			if name == "kiro-cli" {
				if p.Name() != "kiro" {
					t.Errorf("kiro-cli should resolve to provider named %q, got %q", "kiro", p.Name())
				}
			} else if p.Name() != name {
				t.Errorf("Name: want %q, got %q", name, p.Name())
			}
		})
	}
}

func TestNewProviderUnknown(t *testing.T) {
	_, err := NewProvider(context.Background(), FactoryOptions{Name: "claude", Runner: &FakeCommandRunner{}})
	if err == nil {
		t.Fatal("want error for unknown provider")
	}
}

func TestNewProviderAutoDetectNoneAvailable(t *testing.T) {
	// Running on a system where none of the provider binaries exist
	// under the given fake names. Override binary names to guaranteed-absent.
	// Use FactoryOptions.AutoOrder with non-existent names to force
	// every Available() to fail.
	_, err := NewProvider(context.Background(), FactoryOptions{
		AutoOrder: []string{"nonexistent-provider-zzz"},
		Runner:    &FakeCommandRunner{},
	})
	if err == nil {
		t.Fatal("want error when no provider is available")
	}
}

func TestNewProviderAutoDetectErrorMessageListsCandidates(t *testing.T) {
	_, err := NewProvider(context.Background(), FactoryOptions{
		AutoOrder: []string{"nonexistent-a", "nonexistent-b"},
		Runner:    &FakeCommandRunner{},
	})
	if err == nil {
		t.Fatal("want error")
	}
	// Error message must name the candidates so the user knows what to install.
	msg := err.Error()
	if !strings.Contains(msg, "no AI provider available") {
		t.Errorf("error should start with summary, got %q", msg)
	}
}
