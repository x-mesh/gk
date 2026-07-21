// Package provider models external AI CLIs as Provider adapters.
//
// Each supported CLI (gemini, qwen, kiro-cli) implements Provider so the
// rest of gk can call Classify/Compose without knowing which binary is
// on PATH. Providers are plain Go values — no global state — and all
// subprocess I/O is mediated by CommandRunner so tests can inject a
// fake without touching the real binary.
package provider

import (
	"context"
	"errors"
)

// Locality describes where a provider sends prompts.
//
//   - LocalityLocal: the provider runs entirely on the user's machine.
//   - LocalityRemote: the provider uploads prompts to a vendor-hosted
//     LLM. Orgs that forbid external LLMs gate this via config
//     (ai.commit.allow_remote) and the AIProviderRemoteDisallowed
//     policy rule.
//
// All three shipped adapters (gemini/qwen/kiro-cli) are Remote — they
// talk to vendor APIs. The type is kept open so future fully-local
// providers (e.g., Ollama) slot in without schema changes.
type Locality string

const (
	LocalityLocal  Locality = "local"
	LocalityRemote Locality = "remote"
)

// FileChange represents one WIP file handed to Classify. Status uses the
// short set returned by `git status --porcelain=v2` normalised to one
// of: "added", "modified", "deleted", "renamed", "untracked".
type FileChange struct {
	Path   string
	Status string
	// Added and Deleted are exact text line deltas when available. They are
	// advisory classification context; zero is also valid for binary files.
	Added    int
	Deleted  int
	IsBinary bool
	// OrigPath is the source path for rename/copy operations. Empty for
	// all other status values.
	OrigPath string
	// DiffHint holds an abbreviated diff snippet (stat-only for binary,
	// numstat + short hunk for text). Full diffs are never embedded here
	// — the caller decides what to forward to the provider.
	DiffHint string
}

// ClassifyInput is the input to Provider.Classify.
//
// Files is the full WIP set; Lang is the target message language
// (BCP-47 short code). AllowedTypes constrains the provider to the
// Conventional Commit types configured by the repo (usually the same
// list `commitlint` enforces). AllowedScopes, when non-empty, limits
// scope hallucination to known top-level directories.
type ClassifyInput struct {
	Files         []FileChange
	Lang          string
	AllowedTypes  []string
	AllowedScopes []string
	// MaxTokens is an advisory cap; adapters may use it to truncate the
	// diff payload before invoking the CLI. Zero means "no cap".
	MaxTokens int
}

// classifyMaxTokens sizes the response-token cap for one Classify call.
// The response must reference every file, so it scales with file count —
// a provider's fixed default (often 4096) truncated large working trees
// mid-JSON. ai.commit.max_tokens caps the INPUT payload and never
// controlled this. The chunked classify path bounds per-call file count,
// which keeps the result well under every provider's output ceiling.
func classifyMaxTokens(nFiles int) int {
	n := 2048 + 16*nFiles
	if n < 4096 {
		n = 4096
	}
	return n
}

// Group is one proposed commit: a set of files that belong together,
// with the Conventional Commit type/scope the provider picked and a
// short rationale for review.
type Group struct {
	Type      string
	Scope     string
	Files     []string
	Rationale string
}

// ClassifyResult is what Classify returns.
type ClassifyResult struct {
	Groups []Group
	// Model records the concrete model id the provider used (e.g.
	// "gemini-3-flash-preview"). Empty when the adapter cannot tell.
	Model string
	// TokensUsed is best-effort; zero when the provider does not report
	// token counts.
	TokensUsed int
}

// ComposeInput is the input to Provider.Compose. It is called per
// group — the orchestrator runs Compose N times for N groups so a
// retry for group i doesn't re-cost the whole batch.
type ComposeInput struct {
	Group            Group
	Lang             string
	AllowedTypes     []string
	ScopeRequired    bool
	MaxSubjectLength int
	// PreviousAttempts carries commitlint issues from prior Compose
	// attempts on this group, so the adapter can inline them in the
	// retry prompt. Empty on the first attempt.
	PreviousAttempts []AttemptFeedback
	// Diff is the abbreviated diff for the group's files.
	Diff      string
	MaxTokens int
}

// AttemptFeedback carries per-retry context back into the provider.
type AttemptFeedback struct {
	Subject string
	Body    string
	Issues  []string // one line per commitlint Issue
}

// ComposeResult is a single proposed commit message. Subject is the
// one-liner after the colon; Body may be empty.
type ComposeResult struct {
	Subject    string
	Body       string
	Footers    []Footer
	Model      string
	TokensUsed int
}

// Footer mirrors commitlint.Footer for adapters that can return
// structured footers ("Signed-off-by", "Refs"). Plain text commitlint
// parsing runs separately to validate/normalise.
type Footer struct {
	Token string
	Value string
}

// Provider is what every AI CLI adapter implements.
type Provider interface {
	// Name returns the short identifier ("gemini", "qwen", "kiro-cli").
	// Used by config matching, logs, and the optional AI-Assisted-By
	// trailer.
	Name() string

	// Locality reports whether the provider is local or remote.
	Locality() Locality

	// Available verifies the provider is ready to run: the binary
	// exists on PATH, the version meets the adapter's MinVersion, and
	// authentication (env var or OAuth session) is configured.
	// Returning nil means the caller can proceed with Classify/Compose.
	// Errors wrap ErrNotInstalled, ErrUnauthenticated, or
	// ErrVersionTooOld so callers can offer specific fix-ups.
	Available(ctx context.Context) error

	// Classify asks the provider to split Files into Groups.
	Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error)

	// Compose asks the provider to write a commit message for one
	// Group. Callers run commitlint afterwards and may loop with
	// PreviousAttempts populated.
	Compose(ctx context.Context, in ComposeInput) (ComposeResult, error)
}

// ModelIdentifier is the optional interface for adapters that know which
// model they will call BEFORE the request is sent — the HTTP providers.
// ModelID reports the EFFECTIVE model id: the configured override when one
// is set, otherwise the adapter's own default. Callers use it to display
// or log the model without duplicating each adapter's default constant.
//
// CLI adapters (gemini / qwen / kiro) deliberately do NOT implement it:
// they own their model selection and only learn the id from the response.
// Callers must treat a missing implementation, and an empty return, as
// "unknown" rather than assuming a default.
type ModelIdentifier interface {
	ModelID() string
}

// Sentinel errors adapters wrap so callers can branch on failure type.
var (
	ErrNotInstalled     = errors.New("provider binary not installed")
	ErrUnauthenticated  = errors.New("provider not authenticated")
	ErrVersionTooOld    = errors.New("provider version too old")
	ErrProviderResponse = errors.New("provider returned malformed response")
	ErrProviderTimeout  = errors.New("provider timed out")
)
