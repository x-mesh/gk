package aicommit

import (
	"context"
	"fmt"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/commitlint"
)

// Message is one fully-formed commit message draft along with the
// Group it targets. Subject/Body come from Provider.Compose after
// commitlint validation; Footers may be empty.
type Message struct {
	Group   provider.Group
	Subject string
	Body    string
	Footers []provider.Footer
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
}

// ComposeAll runs Provider.Compose per group with commitlint validation
// after each attempt. Groups are processed sequentially — one bad
// group does NOT abort later ones; the caller inspects the returned
// slice for len != len(groups) or error-shaped Messages.
//
// On unrecoverable failure for a single group the loop returns (nil,
// err). That lets the caller distinguish "partial success" (all N
// messages returned) from "outright failure". Partial-success scenarios
// for mixed retries are not supported yet — if the user wants that,
// they can drop the offending group and re-run.
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

	out := make([]Message, 0, len(groups))
	for _, g := range groups {
		// Lockfile-only / CI-only groups skip the LLM. Saves the 50K+
		// tokens a single lockfile diff would burn, which is what was
		// hitting Groq's 100K daily TPD ceiling.
		if msg, ok := heuristicMessage(g, opts.Lang); ok {
			out = append(out, msg)
			continue
		}
		msg, err := composeOne(ctx, p, g, diffs[groupKey(g)], rules, opts, max)
		if err != nil {
			return nil, fmt.Errorf("compose group %q: %w", g.Type, err)
		}
		out = append(out, msg)
	}
	return out, nil
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

	return Message{}, fmt.Errorf("commitlint failed after %d attempts (last subject=%q)",
		maxAttempts, last.Subject)
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
