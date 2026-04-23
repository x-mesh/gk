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
		return build(opts.Name, opts.Runner)
	}
	order := opts.AutoOrder
	if len(order) == 0 {
		order = []string{"gemini", "qwen", "kiro"}
	}
	var probeErrs []error
	for _, n := range order {
		p, err := build(n, opts.Runner)
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

// build constructs the concrete adapter by name.
func build(name string, runner CommandRunner) (Provider, error) {
	switch name {
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
		return nil, fmt.Errorf("unknown provider %q (want gemini|qwen|kiro)", name)
	}
}
