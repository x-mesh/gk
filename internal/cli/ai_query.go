package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// Every `--ai` surface asks the same question of the machinery — may I call
// out, what exactly am I sending, have I sent it before, who answered — and
// differs only in the prompt and the payload. Before this file each surface
// hand-rolled that pipeline, and they had already drifted apart:
//
//   - status and log ran cache→privacy; pr, review and changelog ran
//     privacy→cache, so the two families keyed their caches on different
//     text (pre- vs post-redaction) for the same kind of question.
//   - status carried a private copy of the cache (statusAssistCacheKey and
//     friends) that wrote to the very directory the shared helpers use.
//   - log folded Easy Mode into its cache key, with a comment explaining
//     that otherwise a `--easy` toggle serves an answer in the wrong
//     register. status varies the same way on Easy Mode and never got the
//     fix, because there was no shared path for it to arrive through.
//
// So the duplication was not merely repetitive; it made every fix local to
// whichever surface noticed the bug. runAIQuery is the one path, and a
// surface now supplies only what is genuinely its own.

// aiQuery is one AI question. Everything that legitimately varies between
// surfaces lives here; nothing else should.
type aiQuery struct {
	// Kind names the surface ("status", "log", "pr", …). It namespaces the
	// on-disk cache and is passed through to the provider.
	Kind string

	// SystemPrompt carries the instructions. It goes in the Summarize system
	// slot, never in the payload, so the model reads it as instruction rather
	// than as untrusted data.
	SystemPrompt string

	// Payload is the data block. It is redacted before it leaves the machine
	// and the REDACTED form is what keys the cache.
	Payload string

	Lang      string
	MaxTokens int

	// Timeout bounds the single provider call; zero means no bound.
	// TimeoutHint is appended to a deadline error to name the knob that
	// governs this surface (surfaces read different config keys).
	Timeout     time.Duration
	TimeoutHint string

	// SpinnerLabel is shown while the call is in flight. The provider and
	// model are appended by runAIQuery, so callers pass only the verb
	// ("status --ai - explaining", "pr — drafting summary").
	SpinnerLabel string

	// CacheExtra are additional cache-key components for state that changes
	// the ANSWER without changing the payload — Easy Mode being the reason
	// this exists.
	CacheExtra []string

	// CacheEnabled reflects config; SkipCacheRead reflects --no-cache, which
	// suppresses the read only. A user asking for a fresh answer wants this
	// call to bypass the cache, not to stop caching from here on.
	CacheEnabled  bool
	SkipCacheRead bool

	// ErrOut receives privacy findings and hints. Defaults to the command's
	// stderr.
	ErrOut io.Writer

	// Input builds the SummarizeInput from the redacted payload. Leave nil
	// for the common case (payload travels as Diff); changelog overrides it
	// because it sends a commit list instead.
	Input func(redacted string) provider.SummarizeInput
}

// aiAnswer is what came back, plus enough provenance to credit it honestly.
type aiAnswer struct {
	Text     string
	Provider string
	Model    string
	Cached   bool
}

// Attribution renders the credit footer for this answer. A cache hit names
// no model on purpose: the key folds in the provider but not the model, so
// stored text may predate a model change and naming the current one would
// credit a model that never wrote it.
func (a *aiAnswer) Attribution() string {
	if a == nil {
		return ""
	}
	return aiAttribution(a.Provider, a.Model, a.Cached)
}

// Pipeline failures, wrapped so callers can degrade the way their surface
// requires — status falls back to deterministic local guidance, log prints
// and continues, pr/review/changelog return the error to the user.
var (
	errAIUnsupported    = errors.New("does not support Summarize")
	errAIPrivacyBlocked = errors.New("privacy gate blocked the provider payload")
	errAIEmptyAnswer    = errors.New("empty response from provider")
)

// runAIQuery executes the shared pipeline: capability and remote-policy
// gates, redaction, cache, bounded call, cache write.
//
// Order is deliberate and is the same for every surface. The privacy gate
// runs BEFORE the cache so the key is derived from the exact bytes that
// would be sent: a change to the redaction rules then misses the cache
// instead of replaying an answer written from differently-redacted input.
func runAIQuery(
	ctx context.Context,
	cmd *cobra.Command,
	runner git.Runner,
	prov provider.Provider,
	ai config.AIConfig,
	q aiQuery,
) (*aiAnswer, error) {
	if prov == nil {
		return nil, errors.New("no AI provider resolved")
	}
	errOut := q.ErrOut
	if errOut == nil && cmd != nil {
		errOut = cmd.ErrOrStderr()
	}
	if errOut == nil {
		errOut = io.Discard
	}

	sum, ok := prov.(provider.Summarizer)
	if !ok {
		return nil, fmt.Errorf("provider %q %w", prov.Name(), errAIUnsupported)
	}
	if err := ensureRemoteAllowed(prov, ai); err != nil {
		return nil, err
	}

	redacted, findings, err := applyPrivacyGate(cmd, prov, q.Payload, ai)
	if err != nil {
		renderPrivacyFindings(errOut, findings)
		return nil, fmt.Errorf("%w: %v", errAIPrivacyBlocked, err)
	}
	if cmd != nil {
		showPromptIfRequested(cmd, redacted)
	}

	keyParts := append([]string{q.Kind, redacted, q.Lang, prov.Name()}, q.CacheExtra...)
	key := aiCacheKey(keyParts...)

	if q.CacheEnabled && q.SkipCacheRead {
		Dbg("%s --ai: --no-cache — skipping cache read (key=%s), querying provider=%s", q.Kind, key, prov.Name())
	}
	if q.CacheEnabled && !q.SkipCacheRead {
		if cached, hit := readAICache(ctx, runner, q.Kind, key); hit {
			Dbg("%s --ai: cache hit (key=%s, provider=%s) — no AI call; re-run with --no-cache, or clear with: rm $(git rev-parse --git-path gk-ai-cache)/%s/%s",
				q.Kind, key, prov.Name(), q.Kind, key)
			return &aiAnswer{Text: cached, Provider: prov.Name(), Cached: true}, nil
		}
		Dbg("%s --ai: cache miss (key=%s) — querying provider=%s", q.Kind, key, prov.Name())
	}

	callCtx := ctx
	if q.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}

	in := provider.SummarizeInput{
		Kind:         q.Kind,
		SystemPrompt: q.SystemPrompt,
		Diff:         redacted,
		Lang:         q.Lang,
		MaxTokens:    q.MaxTokens,
	}
	if q.Input != nil {
		in = q.Input(redacted)
	}

	Dbg("%s --ai: querying provider=%s model=%s", q.Kind, prov.Name(), providerModel(prov))
	stop := ui.StartBubbleSpinner(fmt.Sprintf("%s via %s", q.SpinnerLabel, providerLabel(prov)))
	result, err := sum.Summarize(callCtx, in)
	stop()
	if err != nil {
		if q.TimeoutHint != "" && isDeadlineError(err) {
			fmt.Fprintf(errOut, "  hint: %s\n", q.TimeoutHint)
		}
		return nil, fmt.Errorf("summarize: %w", err)
	}

	text := strings.TrimSpace(result.Text)
	if text == "" {
		return nil, errAIEmptyAnswer
	}
	if q.CacheEnabled {
		writeAICache(ctx, runner, q.Kind, key, text)
	}
	// Credit whoever actually answered. A FallbackChain stamps the provider
	// that succeeded onto the result, which differs from the chain head after
	// a failover — reporting the head would name a provider that never ran.
	name := prov.Name()
	if result.Provider != "" {
		name = result.Provider
	}
	return &aiAnswer{Text: text, Provider: name, Model: result.Model}, nil
}

// isDeadlineError also matches by string: provider adapters that shell out
// to a CLI surface the timeout as text rather than a wrapped
// context.DeadlineExceeded.
func isDeadlineError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "deadline exceeded")
}

// addAINoCacheFlag registers the uniform cache-bypass flag. Every `--ai`
// surface caches, so every one of them needs a way to ask again; before this
// only `gk log` had one, and the alternative was deleting cache files by hand.
func addAINoCacheFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("no-cache", false, "with --ai, ignore any cached answer and query the provider again")
}

// aiNoCacheRequested reports --no-cache, tolerating commands that do not
// register it.
func aiNoCacheRequested(cmd *cobra.Command) bool {
	if cmd == nil || cmd.Flags().Lookup("no-cache") == nil {
		return false
	}
	v, _ := cmd.Flags().GetBool("no-cache")
	return v
}

// easyCacheTag is the Easy Mode component of a cache key. Easy Mode changes
// the register the answer is written in without changing the payload, so a
// key that ignores it serves developer prose to a non-developer (and back).
func easyCacheTag() string {
	if EasyEngine().IsEnabled() {
		return "easy=1"
	}
	return "easy=0"
}
