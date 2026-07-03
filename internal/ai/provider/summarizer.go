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
	// Kind selects the prompt template. Known values:
	//   "pr" | "review" | "changelog" | "merge-plan" | "status" | "log" |
	//   "do" | "ask" | "explain" | "explain-last"
	// Unknown kinds fall back to a generic summary.
	Kind      string
	Diff      string   // unified diff content (or the assembled prompt payload)
	Commits   []string // commit messages in the range
	Lang      string   // BCP-47 output language (e.g. "en", "ko")
	MaxTokens int      // advisory cap; 0 = no cap
	// SystemPrompt overrides the default summarize system prompt. Empty
	// keeps summarizeSystemPrompt. Used by callers (e.g. `gk status --ai`)
	// that supply their own role/instructions instead of the generic
	// "senior engineer summarize" framing — keeping the instructions out of
	// the user payload so they are not treated as untrusted <DIFF> data.
	SystemPrompt string
}

// SummarizeResult is the output of Summarize.
type SummarizeResult struct {
	Text       string // free-form text (not JSON)
	Model      string
	TokensUsed int
	Provider   string
}
