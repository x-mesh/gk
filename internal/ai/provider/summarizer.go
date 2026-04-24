package provider

import "context"

// Summarizer is an optional capability that providers may implement.
// Callers detect support via Go type assertion: p.(Summarizer).
// This keeps the core Provider interface stable — gemini/qwen/kiro
// adapters are unaffected.
type Summarizer interface {
	Summarize(ctx context.Context, in SummarizeInput) (SummarizeResult, error)
}

// SummarizeInput describes what to summarize and how.
type SummarizeInput struct {
	Kind      string   // "pr" | "review" | "changelog"
	Diff      string   // unified diff content
	Commits   []string // commit messages in the range
	Lang      string   // BCP-47 output language (e.g. "en", "ko")
	MaxTokens int      // advisory cap; 0 = no cap
}

// SummarizeResult is the output of Summarize.
type SummarizeResult struct {
	Text       string // free-form text (not JSON)
	Model      string
	TokensUsed int
}
