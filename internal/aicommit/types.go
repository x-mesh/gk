// Package aicommit implements `gk commit` — WIP clustering into
// semantic file groups, Conventional Commit message generation via an
// external AI CLI provider, and safe multi-commit application.
package aicommit

// FileChange is one file in the working tree worth committing.
//
// Status is normalised to one of:
//
//	"added"     — new in the index (A_ or _A in porcelain v2)
//	"modified"  — content changed (M_, _M, MM, AM, etc.)
//	"deleted"   — removed (D_, _D)
//	"renamed"   — rename-detected (R_)
//	"copied"    — copy-detected (C_)
//	"untracked" — `?` entry (no index side)
//	"unmerged"  — conflicted (`u` record in porcelain v2)
//
// Staged is true when the index differs from HEAD for this path;
// Unstaged is true when the worktree differs from the index. Both
// can be true (staged + further edits). Untracked is true for `?`
// entries and always implies !Staged && !Unstaged.
//
// IsBinary is a best-effort flag derived from `.gitattributes`
// (`binary`, `-diff`) and fallback content sniffing for the first
// 8KiB of the worktree file. Binary files are forwarded to the
// provider as stats-only stubs.
type FileChange struct {
	Path     string
	Status   string
	Staged   bool
	Unstaged bool
	IsBinary bool
	// OrigPath is set for renames/copies — the source side.
	OrigPath string
	// DeniedBy is non-empty when the path matched a DenyPaths glob;
	// matched files are kept in the slice so callers can warn, but
	// must never be forwarded to the provider.
	DeniedBy string
}
