package aicommit

import (
	"context"
	"errors"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func TestPreflightDisabledByConfig(t *testing.T) {
	err := Preflight(context.Background(), PreflightInput{
		AI: config.AIConfig{Enabled: false},
	})
	if !errors.Is(err, ErrAIDisabled) {
		t.Errorf("want ErrAIDisabled, got %v", err)
	}
}

func TestPreflightDisabledByEnv(t *testing.T) {
	err := Preflight(context.Background(), PreflightInput{
		AI: config.AIConfig{Enabled: true},
		EnvLookup: func(k string) string {
			if k == "GK_AI_DISABLE" {
				return "1"
			}
			return ""
		},
	})
	if !errors.Is(err, ErrAIDisabled) {
		t.Errorf("want ErrAIDisabled, got %v", err)
	}
}

func TestPreflightRemoteProviderBlockedWhenNotAllowed(t *testing.T) {
	p := provider.NewFake()
	p.LocalityVal = provider.LocalityRemote
	err := Preflight(context.Background(), PreflightInput{
		AI:          config.AIConfig{Enabled: true},
		Provider:    p,
		AllowRemote: false,
		SkipGPGKey:  true,
		EnvLookup:   func(string) string { return "" },
	})
	if !errors.Is(err, ErrRemoteNotAllowed) {
		t.Errorf("want ErrRemoteNotAllowed, got %v", err)
	}
}

func TestPreflightRemoteProviderAllowedPasses(t *testing.T) {
	p := provider.NewFake()
	p.LocalityVal = provider.LocalityRemote
	err := Preflight(context.Background(), PreflightInput{
		AI:          config.AIConfig{Enabled: true},
		Provider:    p,
		AllowRemote: true,
		SkipGPGKey:  true,
		EnvLookup:   func(string) string { return "" },
	})
	if err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestPreflightGpgSignWithoutKeyBlocks(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --bool commit.gpgsign": {Stdout: "true\n"},
			"config --get user.signingkey": {
				ExitCode: 1,
				Stderr:   "",
			},
		},
	}
	p := provider.NewFake()
	err := Preflight(context.Background(), PreflightInput{
		Runner:      fake,
		AI:          config.AIConfig{Enabled: true},
		Provider:    p,
		AllowRemote: true,
		EnvLookup:   func(string) string { return "" },
	})
	if !errors.Is(err, ErrGPGKeyMissing) {
		t.Errorf("want ErrGPGKeyMissing, got %v", err)
	}
}

func TestPreflightGpgSignWithKeyPasses(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --bool commit.gpgsign": {Stdout: "true\n"},
			"config --get user.signingkey": {Stdout: "ABC1234\n"},
		},
	}
	p := provider.NewFake()
	err := Preflight(context.Background(), PreflightInput{
		Runner:      fake,
		AI:          config.AIConfig{Enabled: true},
		Provider:    p,
		AllowRemote: true,
		EnvLookup:   func(string) string { return "" },
	})
	if err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestPreflightPropagatesProviderAvailableError(t *testing.T) {
	p := provider.NewFake()
	p.AvailableErr = provider.ErrNotInstalled
	err := Preflight(context.Background(), PreflightInput{
		AI:          config.AIConfig{Enabled: true},
		Provider:    p,
		AllowRemote: true,
		SkipGPGKey:  true,
		EnvLookup:   func(string) string { return "" },
	})
	if !errors.Is(err, provider.ErrNotInstalled) {
		t.Errorf("want ErrNotInstalled, got %v", err)
	}
}
