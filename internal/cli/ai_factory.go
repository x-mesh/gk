package cli

import (
	"strings"
	"time"

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

// aiFactoryOptionsWithModel builds the factory options and applies a
// one-shot model override (e.g. a --model flag). An empty override leaves
// the config/adapter model in place. Honoured by HTTP providers only; CLI
// providers (gemini/qwen/kiro) own their model selection and ignore it.
func aiFactoryOptionsWithModel(ai config.AIConfig, modelOverride string) provider.FactoryOptions {
	opts := aiFactoryOptionsFromAI(ai)
	if strings.TrimSpace(modelOverride) != "" {
		opts.Model = modelOverride
	}
	return opts
}

// aiFactoryOptionsFromAI is the entry point for callers that already
// hold an AIConfig (e.g. ai_commit.go which applies flag overrides
// before constructing the provider).
func aiFactoryOptionsFromAI(ai config.AIConfig) provider.FactoryOptions {
	opts := provider.FactoryOptions{
		Runner: provider.ExecRunner{},
		Name:   ai.Provider,
	}
	// timeoutFor parses a provider's config timeout string ("60s"); a zero
	// result leaves the adapter default in place.
	timeoutFor := func(s string) time.Duration { return parseDurationOrDefault(s, 0) }
	switch ai.Provider {
	case "anthropic", "claude":
		opts.Model = ai.Anthropic.Model
		opts.Endpoint = ai.Anthropic.Endpoint
		opts.Timeout = timeoutFor(ai.Anthropic.Timeout)
		opts.APIKey = ai.Anthropic.APIKey
	case "openai":
		opts.Model = ai.OpenAI.Model
		opts.Endpoint = ai.OpenAI.Endpoint
		opts.Timeout = timeoutFor(ai.OpenAI.Timeout)
		opts.APIKey = ai.OpenAI.APIKey
	case "groq":
		opts.Model = ai.Groq.Model
		opts.Endpoint = ai.Groq.Endpoint
		opts.Timeout = timeoutFor(ai.Groq.Timeout)
		opts.APIKey = ai.Groq.APIKey
	case "nvidia":
		opts.Model = ai.Nvidia.Model
		opts.Endpoint = ai.Nvidia.Endpoint
		opts.Timeout = timeoutFor(ai.Nvidia.Timeout)
		opts.APIKey = ai.Nvidia.APIKey
	default:
		// A name outside the built-in whitelist: resolve it against the
		// custom `ai.providers.<name>` map. Build it from the entry's wire
		// Format (default "openai") so `provider: kiro-api` reaches the
		// OpenAI adapter with the user's endpoint/model. When the name is
		// not registered, opts.Name is left as-is and NewProvider surfaces
		// the "unknown provider" error.
		if custom, ok := ai.CustomProvider(ai.Provider); ok {
			format := strings.TrimSpace(custom.Format)
			if format == "" {
				format = "openai"
			}
			opts.Name = format
			opts.Model = custom.Model
			opts.Endpoint = custom.Endpoint
			opts.Timeout = timeoutFor(custom.Timeout)
			opts.APIKey = custom.APIKey
		}
	}
	return opts
}
