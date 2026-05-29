package aichat

import (
	"context"
	"fmt"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// IntentParser converts natural-language input into an ExecutionPlan
// by calling an AI provider through the Summarizer interface.
type IntentParser struct {
	Summarizer provider.Summarizer
	Context    *RepoContextCollector
	Safety     *SafetyClassifier
	Lang       string
	Easy       bool // Easy Mode: plain, non-developer language in the explanation
	Timeout    time.Duration
	MaxTokens  int // advisory response cap; 0 = provider default
	Dbg        func(string, ...any)
	// Redact, when set, sanitizes the fully-assembled prompt (user input +
	// repo context) before it leaves the process. ai_do wires this to the
	// privacy gate so that context — branch names, paths, reflog — is
	// redacted for remote providers, not just the user input. Returning an
	// error aborts the parse (e.g. too many secrets) rather than uploading.
	Redact func(string) (string, error)
}

// dbg is a helper that calls p.Dbg if non-nil.
func (p *IntentParser) dbg(format string, args ...any) {
	if p.Dbg != nil {
		p.Dbg(format, args...)
	}
}

// Parse analyses the natural-language input and returns an ExecutionPlan.
//
// Flow:
//  1. Collect repository context via RepoContextCollector.
//  2. Build the user prompt with buildDoUserPrompt.
//  3. Call Summarizer.Summarize with Kind "do".
//  4. Parse the JSON response into an ExecutionPlan.
//  5. Classify each command's risk via SafetyClassifier.
func (p *IntentParser) Parse(ctx context.Context, input string) (*ExecutionPlan, error) {
	// Apply timeout if configured.
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}

	// 1. Collect repository context.
	var repoCtx *RepoContext
	if p.Context != nil {
		repoCtx = p.Context.Collect(ctx)
	}

	// 2. Build the user prompt.
	userPrompt := buildDoUserPrompt(input, repoCtx, p.Lang, p.Easy)

	// 2b. Redact the assembled prompt (input + repo context) before it
	// leaves the process. The caller redacts the raw input separately, but
	// the repo context is added here, so without this pass branch names,
	// paths, and reflog entries would reach a remote provider un-redacted.
	if p.Redact != nil {
		redacted, err := p.Redact(userPrompt)
		if err != nil {
			return nil, err
		}
		userPrompt = redacted
	}

	// 3. Call the AI provider via Summarizer.
	result, err := p.Summarizer.Summarize(ctx, provider.SummarizeInput{
		Kind:      "do",
		Diff:      userPrompt,
		Lang:      p.Lang,
		MaxTokens: p.MaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("do: AI provider error: %w", err)
	}

	// 4. Parse the JSON response into an ExecutionPlan.
	plan, err := ParseExecutionPlan(result.Text)
	if err != nil {
		p.dbg("do: raw AI response:\n%s", result.Text)
		return nil, fmt.Errorf("do: failed to parse AI response: %w", err)
	}

	// 5. Classify each command's risk.
	if p.Safety != nil {
		p.Safety.ClassifyPlan(plan)
	}

	return plan, nil
}
