package aicommit

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/commitlint"
)

// DefaultComposeConcurrency caps how many groups Compose runs in
// parallel. Compose is the dominant latency in `gk commit`: each group
// is an independent LLM round-trip (plus up to MaxAttempts-1 commitlint
// retries), and running them serially made wall-clock scale linearly
// with the group count. The groups share no state, so they fan out.
//
// 4 is a deliberate ceiling, not a target: it keeps a typical 2-4 group
// commit fully concurrent while bounding (a) burst pressure on remote
// providers' rate limits (Groq's free tier in particular) and (b) the
// number of CLI-provider subprocesses (gemini/qwen/kiro) spawned at
// once. ai.commit.concurrency overrides it; this is the fallback.
//
// Exported so the CLI can label the effective dispatch
// ("parallel ×N") without re-deriving the fallback.
const DefaultComposeConcurrency = 4

// Message is one fully-formed commit message draft along with the
// Group it targets. Subject/Body come from Provider.Compose after
// commitlint validation; Footers may be empty.
type Message struct {
	Group   provider.Group
	Subject string
	Body    string
	Footers []provider.Footer
	// Breaking, when true, appends the Conventional Commits "!" marker to
	// the header (e.g. "feat(x)!: ..."). Set by plan-driven commits whose
	// message carried a "!" or a "BREAKING CHANGE" footer; the AI path
	// leaves it false (the model encodes breaking changes in the footer
	// instead).
	Breaking bool
	// AllowEmpty permits a commit with no files via `git commit
	// --allow-empty`. Only honoured when Group.Files is empty; ApplyMessages
	// otherwise stages and commits the listed pathspecs as usual.
	AllowEmpty bool
	// Attempts is the number of Provider.Compose calls it took to
	// produce a commitlint-clean message. 1 is the happy path;
	// values >1 indicate the retry loop fired.
	Attempts int
	// Model records the provider's concrete model id (when available).
	Model string
}

// ComposeOptions tunes the retry loop.
//
// MaxAttempts is the total number of Compose calls per group
// (1 initial + up to MaxAttempts-1 retries). 3 is a good default; 0
// falls back to 3. AllowedTypes and MaxSubjectLength mirror the repo's
// commitlint rules and are forwarded to the provider.
type ComposeOptions struct {
	MaxAttempts      int
	AllowedTypes     []string
	ScopeRequired    bool
	MaxSubjectLength int
	Lang             string
	// Concurrency caps how many groups Compose runs in parallel. <= 0
	// falls back to defaultComposeConcurrency. Wired from
	// ai.commit.concurrency so operators can tighten it for strict
	// provider rate limits or loosen it on a paid tier.
	Concurrency int
	// WarmCache, when true and there is more than one LLM group, runs the
	// first group synchronously before fanning out the rest. The first
	// call populates the provider's prompt cache (Anthropic's ephemeral
	// system-prompt cache) so the parallel siblings read the cached prefix
	// instead of each paying a cache-miss. Pure latency plays leave this
	// false; the CLI sets it only for cache-capable providers.
	WarmCache bool
}

// ComposeAll runs Provider.Compose per group with commitlint validation
// after each attempt. Groups are independent, so they are composed
// CONCURRENTLY (bounded by defaultComposeConcurrency) and the results
// re-assembled in input order — out[i] always corresponds to groups[i].
//
// On unrecoverable failure for a single group the call returns (nil,
// err): the first failing group's error is surfaced and the shared
// context is cancelled so in-flight siblings stop early. That lets the
// caller distinguish "partial success" (all N messages returned) from
// "outright failure". Partial-success scenarios for mixed retries are
// not supported yet — if the user wants that, they can drop the
// offending group and re-run.
func ComposeAll(
	ctx context.Context,
	p provider.Provider,
	groups []provider.Group,
	diffs map[string]string,
	opts ComposeOptions,
) ([]Message, error) {
	max := opts.MaxAttempts
	if max <= 0 {
		max = 3
	}
	rules := commitlint.Rules{
		AllowedTypes:     opts.AllowedTypes,
		ScopeRequired:    opts.ScopeRequired,
		MaxSubjectLength: opts.MaxSubjectLength,
	}

	// A group whose TYPE the lint rules reject can never compose clean: the
	// type is pinned to the group (not the model), so every retry would fail
	// the same type-enum check while burning an LLM call per attempt. The
	// classifier is told the allowed types, so this only fires on classifier
	// drift or a config mismatch — fail fast and name the fix instead.
	if len(rules.AllowedTypes) > 0 {
		for _, grp := range groups {
			if !commitlint.TypeAllowed(grp.Type, rules.AllowedTypes) {
				return nil, fmt.Errorf(
					"group type %q is not an allowed commit type (%s) — add it to commit.types in .gk.yaml, or commit with an explicit plan (gk commit --plan -)",
					grp.Type, strings.Join(rules.AllowedTypes, ", "))
			}
		}
	}

	// Index-aligned output: each goroutine writes its own slot, so no
	// lock is needed and input order is preserved for review/apply.
	out := make([]Message, len(groups))

	// Split into heuristic (inline, no network) and LLM (fan-out) work.
	// Lockfile-only / CI-only groups skip the LLM — saves the 50K+ tokens
	// a single lockfile diff would burn (the original Groq TPD trigger) —
	// so resolve them here without spending a goroutine or a slot.
	llm := make([]int, 0, len(groups))
	for i, grp := range groups {
		if msg, ok := heuristicMessage(grp, opts.Lang); ok {
			out[i] = msg
			continue
		}
		llm = append(llm, i)
	}
	if len(llm) == 0 {
		return out, nil
	}

	composeAt := func(ctx context.Context, i int) error {
		grp := groups[i]
		msg, err := composeOne(ctx, p, grp, diffs[groupKey(grp)], rules, opts, max)
		if err != nil {
			return fmt.Errorf("compose group %q: %w", grp.Type, err)
		}
		out[i] = msg
		return nil
	}

	// Single-group fast-path: one LLM call, the claude/codex shape. No
	// errgroup, no goroutine — just compose it on the calling goroutine.
	if len(llm) == 1 {
		if err := composeAt(ctx, llm[0]); err != nil {
			return nil, err
		}
		return out, nil
	}

	// Optional cache warm-up: compose the first group synchronously so a
	// prompt cache is primed before the siblings fan out (see WarmCache).
	rest := llm
	if opts.WarmCache {
		if err := composeAt(ctx, llm[0]); err != nil {
			return nil, err
		}
		rest = llm[1:]
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(composeConcurrency(len(rest), opts.Concurrency))
	for _, i := range rest {
		g.Go(func() error { return composeAt(gctx, i) })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// composeConcurrency resolves the worker limit. configured <= 0 falls
// back to defaultComposeConcurrency; the result is then clamped to
// [1, groupCount] so there are no idle workers and SetLimit(0) (which
// would block every Go call) can never happen. Callers pass groupCount
// >= 1.
func composeConcurrency(groupCount, configured int) int {
	limit := configured
	if limit <= 0 {
		limit = DefaultComposeConcurrency
	}
	if groupCount < limit {
		return groupCount
	}
	return limit
}

// composeOne is the per-group retry loop.
func composeOne(
	ctx context.Context,
	p provider.Provider,
	g provider.Group,
	diff string,
	rules commitlint.Rules,
	opts ComposeOptions,
	maxAttempts int,
) (Message, error) {
	var history []provider.AttemptFeedback
	var last provider.ComposeResult

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		in := provider.ComposeInput{
			Group:            g,
			Lang:             opts.Lang,
			AllowedTypes:     opts.AllowedTypes,
			ScopeRequired:    opts.ScopeRequired,
			MaxSubjectLength: opts.MaxSubjectLength,
			Diff:             diff,
			PreviousAttempts: history,
		}
		res, err := p.Compose(ctx, in)
		if err != nil {
			// Provider-level error — abort this group; caller decides.
			return Message{}, err
		}
		last = res

		issues := lintMessage(res, g, rules)
		if len(issues) == 0 {
			return Message{
				Group:    g,
				Subject:  res.Subject,
				Body:     res.Body,
				Footers:  res.Footers,
				Attempts: attempt,
				Model:    res.Model,
			}, nil
		}
		history = append(history, provider.AttemptFeedback{
			Subject: res.Subject,
			Body:    res.Body,
			Issues:  issuesToStrings(issues),
		})
	}

	// Name the violated rules: "commitlint failed" alone sends the user
	// hunting through config for WHICH rule — the last attempt's issues are
	// the diagnosis (e.g. a type-enum violation reveals a group type the
	// repo's commit.types doesn't allow, which no amount of re-composing can
	// fix).
	lastIssues := issuesToStrings(lintMessage(last, g, rules))
	return Message{}, fmt.Errorf("commitlint failed after %d attempts (last subject=%q; violations: %s)",
		maxAttempts, last.Subject, strings.Join(lastIssues, "; "))
}

// lintMessage assembles the full "type(scope): subject" header
// (plus body) and runs it through commitlint. Type/scope come from
// the group so the provider can't wander — callers want the header
// to match the group they're proposing, not whatever the LLM felt
// like emitting.
func lintMessage(res provider.ComposeResult, g provider.Group, rules commitlint.Rules) []commitlint.Issue {
	header := g.Type
	if g.Scope != "" {
		header += "(" + g.Scope + ")"
	}
	header += ": " + res.Subject
	raw := header
	if res.Body != "" {
		raw += "\n\n" + res.Body
	}
	msg := commitlint.Parse(raw)
	return commitlint.Lint(msg, rules)
}

// issuesToStrings makes the retry prompt message short: "code: reason".
func issuesToStrings(issues []commitlint.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Code+": "+i.Message)
	}
	return out
}

// groupKey returns a deterministic key used to look up per-group
// diffs in the diffs map supplied by the caller. Mirrors the CLI
// wiring: diffs[groupKey(g)] returns the aggregated diff for that
// group's files.
func groupKey(g provider.Group) string {
	// Simple join of files is sufficient since files never repeat
	// across groups (classifier invariant).
	key := g.Type + "|"
	for i, f := range g.Files {
		if i > 0 {
			key += ","
		}
		key += f
	}
	return key
}
