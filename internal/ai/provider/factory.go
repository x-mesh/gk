package provider

import (
	"context"
	"errors"
	"fmt"
)

// FactoryOptions selects and configures a Provider.
//
// Name overrides config when non-empty ("gemini" | "qwen" | "kiro" —
// the names returned by Provider.Name()). When Name is empty the
// factory falls back to AutoOrder, returning the first provider whose
// Available() succeeds. Runner lets tests inject FakeCommandRunner
// across every adapter.
type FactoryOptions struct {
	Name      string
	AutoOrder []string // default: ["gemini", "qwen", "kiro"]
	Runner    CommandRunner
	// Model optionally overrides the adapter's default model id.
	// Honoured by HTTP-based adapters (anthropic, openai, groq, nvidia);
	// CLI adapters (gemini, qwen, kiro) ignore it because the CLI binary
	// owns its own model selection.
	Model string
	// Endpoint optionally overrides the adapter's default endpoint URL.
	// Same applicability as Model.
	Endpoint string
}

// NewProvider returns a ready-to-use Provider. When Name is set, the
// returned provider is the one named (even if Available() fails — the
// caller can surface the specific error). When Name is empty, the
// factory probes providers in AutoOrder and returns the first one that
// is Available. When none are available, it returns a wrapped
// ErrNotInstalled with hints for each candidate.
func NewProvider(ctx context.Context, opts FactoryOptions) (Provider, error) {
	if opts.Runner == nil {
		opts.Runner = ExecRunner{}
	}
	if opts.Name != "" {
		return buildWithOpts(opts.Name, opts)
	}
	order := opts.AutoOrder
	if len(order) == 0 {
		order = []string{"anthropic", "openai", "nvidia", "groq", "gemini", "qwen", "kiro"}
	}
	var probeErrs []error
	for _, n := range order {
		p, err := Build(n, opts.Runner)
		if err != nil {
			probeErrs = append(probeErrs, err)
			continue
		}
		if err := p.Available(ctx); err != nil {
			probeErrs = append(probeErrs, fmt.Errorf("%s: %w", n, err))
			continue
		}
		return p, nil
	}
	return nil, fmt.Errorf("no AI provider available (tried %v): %w",
		order, errors.Join(probeErrs...))
}

// Build constructs the concrete adapter by name. Exported so callers
// (e.g. FallbackChain builder) can construct individual providers.
func Build(name string, runner CommandRunner) (Provider, error) {
	return buildWithOpts(name, FactoryOptions{Runner: runner})
}

// buildWithOpts is the inner constructor that honours model/endpoint
// overrides for HTTP-based adapters. Build keeps the simpler signature
// for callers that only need the default config.
func buildWithOpts(name string, opts FactoryOptions) (Provider, error) {
	runner := opts.Runner
	switch name {
	case "anthropic", "claude":
		a := NewAnthropic()
		if opts.Model != "" {
			a.Model = opts.Model
		}
		if opts.Endpoint != "" {
			a.Endpoint = opts.Endpoint
		}
		return a, nil
	case "openai":
		o := NewOpenAI()
		if opts.Model != "" {
			o.Model = opts.Model
		}
		if opts.Endpoint != "" {
			o.Endpoint = opts.Endpoint
		}
		o.nv = o.toNvidia() // re-wire after override
		return o, nil
	case "nvidia":
		n := NewNvidia()
		if opts.Model != "" {
			n.Model = opts.Model
		}
		if opts.Endpoint != "" {
			n.Endpoint = opts.Endpoint
		}
		return n, nil
	case "groq":
		g := NewGroq()
		if opts.Model != "" {
			g.Model = opts.Model
		}
		if opts.Endpoint != "" {
			g.Endpoint = opts.Endpoint
		}
		g.nv = g.toNvidia()
		return g, nil
	case "gemini":
		g := NewGemini()
		g.Runner = runner
		return g, nil
	case "qwen":
		q := NewQwen()
		q.Runner = runner
		return q, nil
	case "kiro", "kiro-cli":
		k := NewKiro()
		k.Runner = runner
		return k, nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want anthropic|openai|nvidia|groq|gemini|qwen|kiro)", name)
	}
}
