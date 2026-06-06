package aichat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
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

	// 1b. Deterministic fast path. Well-understood intents (ignore/untrack a
	// file, erase from history, undo last commit) resolve to a canonical plan
	// without an LLM round-trip — instant, reproducible, and free of
	// hallucinated steps. No clear match falls through to the AI path below.
	if plan := p.recognize(ctx, input, repoCtx); plan != nil {
		if p.Safety != nil {
			p.Safety.ClassifyPlan(plan)
		}
		return plan, nil
	}

	// 2. Build the user prompt. Append tracked-state facts for any paths named
	// in the request so the model knows whether `git rm --cached` is needed
	// and prefers `gk ignore`.
	userPrompt := buildDoUserPrompt(input, repoCtx, p.Lang, p.Easy)
	if hints := p.pathTrackingHints(ctx, input); hints != "" {
		userPrompt += "\n\n" + hints
	}

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

// runner returns the git.Runner backing the context collector, or nil.
func (p *IntentParser) runner() git.Runner {
	if p.Context == nil {
		return nil
	}
	return p.Context.Runner
}

// recognize runs the deterministic intent recognizers, returning a canonical
// plan when one matches (and logging which). Returns nil to defer to the LLM.
func (p *IntentParser) recognize(ctx context.Context, input string, rc *RepoContext) *ExecutionPlan {
	plan, name := recognizeIntent(ctx, input, rc, p.runner(), p.Lang)
	if plan != nil {
		p.dbg("do: deterministic match — %s (LLM skipped)", name)
	}
	return plan
}

// pathTrackingHints reports the git tracked-state of paths named in the input
// so the AI plan can choose `git rm --cached` / `gk ignore` correctly. Empty
// when no paths are mentioned or no runner is available.
func (p *IntentParser) pathTrackingHints(ctx context.Context, input string) string {
	runner := p.runner()
	if runner == nil {
		return ""
	}
	paths := mentionedPaths(input)
	if len(paths) == 0 {
		return ""
	}
	var lines []string
	for _, path := range paths {
		state := "untracked"
		if _, _, err := runner.Run(ctx, "ls-files", "--error-unmatch", "--", path); err == nil {
			state = "tracked"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", path, state))
		if len(lines) >= 5 {
			break
		}
	}
	return "Tracked-state of mentioned paths (to stop tracking a file use " +
		"`gk ignore <path>`; to erase it from history use `gk forget <path>`):\n" +
		strings.Join(lines, "\n")
}
