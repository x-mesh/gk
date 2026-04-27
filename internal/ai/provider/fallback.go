package provider

import (
	"context"
	"errors"
	"fmt"
)

// FallbackChain wraps multiple providers and delegates to the first one
// that succeeds. On failure it logs via Dbg and moves to the next
// provider in order. This extends the Factory's static auto-detect into
// a runtime failover mechanism.
//
// Configuration errors (ErrUnauthenticated, ErrNotInstalled) are NOT
// retried — they indicate a setup problem, not a transient failure.
// Only runtime errors (network, timeout, malformed response) trigger
// fallback to the next provider.
type FallbackChain struct {
	Providers []Provider
	Dbg       func(string, ...any) // debug logger; nil-safe
}

func (fc *FallbackChain) dbg(format string, args ...any) {
	if fc.Dbg != nil {
		fc.Dbg(format, args...)
	}
}

// Name returns the name of the first provider, or "fallback" when the
// chain is empty.
func (fc *FallbackChain) Name() string {
	if len(fc.Providers) > 0 {
		return fc.Providers[0].Name()
	}
	return "fallback"
}

// Locality returns the locality of the first provider, or
// LocalityRemote when the chain is empty.
func (fc *FallbackChain) Locality() Locality {
	if len(fc.Providers) > 0 {
		return fc.Providers[0].Locality()
	}
	return LocalityRemote
}

// Available returns nil if any provider in the chain is available.
func (fc *FallbackChain) Available(ctx context.Context) error {
	var lastErr error
	for _, p := range fc.Providers {
		if err := p.Available(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("fallback: no providers configured")
}

// Classify tries each provider in order and returns the first success.
// Configuration errors (auth, install) stop the chain immediately.
func (fc *FallbackChain) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error) {
	var lastErr error
	for _, p := range fc.Providers {
		res, err := p.Classify(ctx, in)
		if err == nil {
			return res, nil
		}
		fc.dbg("fallback: %s failed: %v", p.Name(), err)
		if isConfigError(err) {
			// Skip this provider silently — it's not set up.
			lastErr = err
			continue
		}
		lastErr = err
	}
	return ClassifyResult{}, lastErr
}

// Compose tries each provider in order and returns the first success.
// Configuration errors (auth, install) stop the chain immediately.
func (fc *FallbackChain) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	var lastErr error
	for _, p := range fc.Providers {
		res, err := p.Compose(ctx, in)
		if err == nil {
			return res, nil
		}
		fc.dbg("fallback: %s failed: %v", p.Name(), err)
		if isConfigError(err) {
			lastErr = err
			continue
		}
		lastErr = err
	}
	return ComposeResult{}, lastErr
}

// Summarize tries each provider that implements Summarizer in order.
// If no provider implements Summarizer, a descriptive error is returned.
// Configuration errors (auth, install) stop the chain immediately.
func (fc *FallbackChain) Summarize(ctx context.Context, in SummarizeInput) (SummarizeResult, error) {
	var lastErr error
	tried := 0
	for _, p := range fc.Providers {
		s, ok := p.(Summarizer)
		if !ok {
			continue
		}
		tried++
		res, err := s.Summarize(ctx, in)
		if err == nil {
			if res.Provider == "" {
				res.Provider = p.Name()
			}
			return res, nil
		}
		fc.dbg("fallback: %s failed: %v", p.Name(), err)
		if isConfigError(err) {
			lastErr = err
			continue
		}
		lastErr = err
	}
	if tried == 0 {
		return SummarizeResult{}, fmt.Errorf("fallback: no provider implements Summarizer")
	}
	return SummarizeResult{}, lastErr
}

// isConfigError returns true for errors that indicate a setup problem
// (missing binary, missing auth) rather than a transient runtime failure.
// These providers are silently skipped in the fallback chain.
func isConfigError(err error) bool {
	return errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrNotInstalled)
}

// Compile-time interface checks.
var (
	_ Provider   = (*FallbackChain)(nil)
	_ Summarizer = (*FallbackChain)(nil)
)
