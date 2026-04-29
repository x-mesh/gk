package aicommit

import (
	"github.com/x-mesh/gk/internal/ai/provider"
)

// composePromptOverhead is a fixed token allowance for the system
// prompt + group metadata (type/scope/files block) attached to every
// Compose call. Measured against a few sample prompts at 4 chars per
// token; bumped a little for safety.
const composePromptOverhead = 250

// charsPerToken is the cheap heuristic used to convert byte length to
// token count. Real tokenisers vary by model; 4 is a conservative
// average for the gpt/llama family. Off by ~30% is acceptable for a
// cost preview — we just need to flag "this group will burn 50K
// tokens" before the user fires the call.
const charsPerToken = 4

// EstimateComposeTokens approximates the input token count of a single
// Provider.Compose request for the given group/diff. Returns 0 for
// groups that the heuristic bypass will short-circuit (lockfile-only
// build groups, CI-only groups), since those never reach the LLM.
//
// Use this in `--dry-run` to surface per-group cost before any API
// call. The number is upper-bounded by the diff already having been
// truncated via TruncateDiff (DefaultComposeDiffByteCap).
func EstimateComposeTokens(g provider.Group, diff string, lang string) int {
	if _, ok := heuristicMessage(g, lang); ok {
		return 0
	}
	// File list contributes ~16 chars/file in the prompt
	// ("- internal/x/foo.go\n").
	fileBytes := 0
	for _, f := range g.Files {
		fileBytes += len(f) + 4
	}
	return composePromptOverhead + (fileBytes+len(diff))/charsPerToken
}

// EstimateClassifyTokens approximates the input token count of a
// single Provider.Classify request. Classify only sees the file list
// (no diff content), so it's very cheap — ~500 tokens for a 60-file
// repo. Surfaced in --dry-run for completeness.
func EstimateClassifyTokens(files []FileChange) int {
	const overhead = 200
	bytes := 0
	for _, f := range files {
		bytes += len(f.Path) + len(f.Status) + 8
	}
	return overhead + bytes/charsPerToken
}
