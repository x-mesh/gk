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
	Timeout    time.Duration
	Dbg        func(string, ...any)
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
	userPrompt := buildDoUserPrompt(input, repoCtx, p.Lang)

	// 3. Call the AI provider via Summarizer.
	result, err := p.Summarizer.Summarize(ctx, provider.SummarizeInput{
		Kind: "do",
		Diff: userPrompt,
		Lang: p.Lang,
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
