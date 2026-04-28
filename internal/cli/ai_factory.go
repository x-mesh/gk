package cli

import (
	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
)

// aiFactoryOptions builds the FactoryOptions used by every AI-powered
// command. It pulls model/endpoint overrides out of the user's config
// so a single yaml change reaches every call site.
//
// Resolution order for model/endpoint overrides:
//  1. cfg.AI.<provider>.Model / .Endpoint                (yaml)
//  2. provider's adapter default                         (compiled in)
//
// CLI providers (gemini, qwen, kiro) ignore the override — those
// binaries own their own model selection.
func aiFactoryOptions(cfg *config.Config) provider.FactoryOptions {
	if cfg == nil {
		return provider.FactoryOptions{Runner: provider.ExecRunner{}}
	}
	return aiFactoryOptionsFromAI(cfg.AI)
}

// aiFactoryOptionsFromAI is the entry point for callers that already
// hold an AIConfig (e.g. ai_commit.go which applies flag overrides
// before constructing the provider).
func aiFactoryOptionsFromAI(ai config.AIConfig) provider.FactoryOptions {
	opts := provider.FactoryOptions{
		Runner: provider.ExecRunner{},
		Name:   ai.Provider,
	}
	switch ai.Provider {
	case "anthropic", "claude":
		opts.Model = ai.Anthropic.Model
		opts.Endpoint = ai.Anthropic.Endpoint
	case "openai":
		opts.Model = ai.OpenAI.Model
		opts.Endpoint = ai.OpenAI.Endpoint
	case "groq":
		opts.Model = ai.Groq.Model
		opts.Endpoint = ai.Groq.Endpoint
	case "nvidia":
		opts.Model = ai.Nvidia.Model
		opts.Endpoint = ai.Nvidia.Endpoint
	}
	return opts
}
