package aicommit

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/commitlint"
)

// ClassifyOptions shapes Classify behaviour.
//
// HeuristicOnly forces the path-based heuristic and skips the LLM
// entirely (useful for --no-llm / tests). Threshold configures when the
// hybrid strategy falls back to the LLM. AllowedTypes constrains both
// the heuristic and the LLM to valid Conventional Commit types.
type ClassifyOptions struct {
	HeuristicOnly bool
	// HybridFileLimit: when len(files) <= this AND all files share a
	// single top-level directory, the heuristic alone decides. Default 5.
	HybridFileLimit int
	AllowedTypes    []string
	AllowedScopes   []string
	Lang            string
	// ScopeRequired mirrors commit.scope_required. Heuristic groups carry
	// no scope, so when a scope is mandatory the definite-kind fast path is
	// disabled: a scopeless heuristic message would fail commitlint, so the
	// LLM Classify path runs instead (it can infer a scope).
	ScopeRequired bool
}

// Classify groups the WIP file list into proposed commits.
//
// Hybrid strategy:
//  1. Always compute the heuristic classification.
//  2. If HeuristicOnly OR the file set is small+homogeneous, return it.
//  3. Otherwise call provider.Classify and apply the **path-rule
//     override** so test/docs/ci files always keep their heuristic
//     type even if the LLM picked something else — this is the pitfall
//     research's P2.1 mitigation.
//
// The returned ClassifyResult carries the provider's Model and TokensUsed so
// the caller can report classify cost; the heuristic short-circuits report
// Model "heuristic" with zero tokens (no provider call was made).
func Classify(
	ctx context.Context,
	p provider.Provider,
	files []FileChange,
	opts ClassifyOptions,
) (provider.ClassifyResult, error) {
	// Drop denied files up front — never sent to the LLM.
	safe := filterSafe(files)
	if len(safe) == 0 {
		return provider.ClassifyResult{}, nil
	}

	rawHeuristic := heuristicGroups(safe)
	heuristic := constrainTypes(rawHeuristic, opts.AllowedTypes)
	if opts.HeuristicOnly || isSmallHomogeneous(safe, opts.HybridFileLimit) {
		return provider.ClassifyResult{Groups: heuristic, Model: heuristicModel}, nil
	}
	// Definite-type single-group fast path: when the heuristic already
	// resolves the whole change to exactly ONE group of a DEFINITE kind
	// (test / docs / ci / build), the Conventional Commit type is certain
	// and the LLM Classify round-trip can add nothing — skip it. This
	// covers cases isSmallHomogeneous misses (e.g. >5 doc files, or docs
	// spanning several top-dirs like README.md + docs/x.md). The catch-all
	// "chore" type is deliberately excluded: a single chore group may be a
	// mixed source change the LLM should split into feat/fix/refactor.
	// Skipped when ScopeRequired: heuristic groups have no scope, so the
	// scopeless message would fail commitlint — defer to the LLM, which can
	// infer one.
	// Definiteness is a property of the PATH (the raw heuristic kind), while
	// the returned label may have been folded by constrainTypes — so the
	// check reads rawHeuristic and the return ships the folded groups. A
	// lockfile-only change in a repo whose commit.types lacks "build" still
	// skips the LLM; it just lands as the repo's catch-all type.
	if len(rawHeuristic) == 1 && isDefiniteKind(rawHeuristic[0].Type) && !opts.ScopeRequired {
		return provider.ClassifyResult{Groups: heuristic, Model: heuristicModel}, nil
	}

	// LLM path — chunked: one call's response must reference every file, so
	// beyond ~classifyChunkSize files even a scaled response cap is at risk
	// (the space-mesh incident: 2,475 files → mid-JSON truncation). Chunks
	// are disjoint, so classifying them independently is safe; the
	// type/scope bucketing in overrideWithPathRules merges same-typed
	// groups across chunks, and its coverage guard sweeps any file a chunk
	// response dropped.
	var groups []provider.Group
	var model string
	var tokens int
	for start := 0; start < len(safe); start += classifyChunkSize {
		end := min(start+classifyChunkSize, len(safe))
		chunk := safe[start:end]
		in := provider.ClassifyInput{
			Files:         toProviderFiles(chunk),
			Lang:          opts.Lang,
			AllowedTypes:  opts.AllowedTypes,
			AllowedScopes: opts.AllowedScopes,
		}
		res, err := p.Classify(ctx, in)
		if err != nil {
			return provider.ClassifyResult{}, err
		}
		groups = append(groups, res.Groups...)
		model = res.Model
		tokens += res.TokensUsed
	}
	return provider.ClassifyResult{
		Groups:     constrainTypes(overrideWithPathRules(groups, safe), opts.AllowedTypes),
		Model:      model,
		TokensUsed: tokens,
	}, nil
}

// constrainTypes folds group types the repo's commit.types rejects into
// "chore". The path heuristics (heuristicType, and overrideWithPathRules
// stamping their verdict over the LLM's) know nothing about AllowedTypes, so
// in a repo that deliberately narrows commit.types they would emit a type the
// composer is guaranteed to reject — gk failing on a label gk itself
// invented. Chore is the Conventional Commits catch-all for maintenance
// work, which is exactly what the definite kinds (test/docs/ci/build) are.
// When chore itself is not allowed there is no honest fallback: the groups
// pass through unchanged and the composer's fail-fast names the config fix.
// Folding can leave two groups with the same (type, scope); those merge so
// the user reviews one chore commit, not several.
func constrainTypes(groups []provider.Group, allowed []string) []provider.Group {
	if len(allowed) == 0 || len(groups) == 0 {
		return groups
	}
	needsFold := false
	for _, g := range groups {
		if !commitlint.TypeAllowed(g.Type, allowed) {
			needsFold = true
			break
		}
	}
	if !needsFold || !commitlint.TypeAllowed("chore", allowed) {
		return groups
	}
	type key struct{ typ, scope string }
	index := map[key]int{}
	out := make([]provider.Group, 0, len(groups))
	for _, g := range groups {
		if !commitlint.TypeAllowed(g.Type, allowed) {
			note := fmt.Sprintf("folded from %q (not in commit.types)", g.Type)
			if g.Rationale != "" {
				g.Rationale += "; " + note
			} else {
				g.Rationale = note
			}
			g.Type = "chore"
		}
		k := key{typ: strings.ToLower(g.Type), scope: g.Scope}
		if i, seen := index[k]; seen {
			out[i].Files = append(out[i].Files, g.Files...)
			continue
		}
		index[k] = len(out)
		out = append(out, g)
	}
	return out
}

// classifyChunkSize bounds how many files one provider Classify call sees.
// It keeps the index-referencing response far below every provider's output
// ceiling and the per-call diff payload inside ai.commit.max_tokens without
// collapsing everything to a bare cluster summary.
const classifyChunkSize = 150

// filterSafe drops entries where DeniedBy is set.
func filterSafe(in []FileChange) []FileChange {
	out := in[:0:0]
	for _, f := range in {
		if f.DeniedBy != "" {
			continue
		}
		out = append(out, f)
	}
	return out
}

// isSmallHomogeneous returns true when len(files) <= limit AND all
// files share the same top-level directory. In that case the hybrid
// strategy skips the LLM.
func isSmallHomogeneous(files []FileChange, limit int) bool {
	if limit <= 0 {
		limit = 5
	}
	if len(files) > limit {
		return false
	}
	topDir := ""
	for _, f := range files {
		td := topLevelDir(f.Path)
		if topDir == "" {
			topDir = td
		} else if td != topDir {
			return false
		}
	}
	return true
}

// topLevelDir returns the first path segment, or "." for bare filenames.
func topLevelDir(p string) string {
	p = filepath.ToSlash(p)
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

// FileKind classifies a path into the coarse change category used by
// surfaces like `gk diff --digest` (test / docs / ci / build); empty when
// the path is regular source. Exported wrapper over the commit
// classifier's path heuristics so the vocabulary stays in one place.
func FileKind(path string) string {
	if isDefiniteKind(heuristicType(path)) {
		return heuristicType(path)
	}
	return ""
}

// isDefiniteKind reports whether a heuristic type is a path-certain
// Conventional Commit kind (test / docs / ci / build) — i.e. one the LLM
// cannot improve on. The catch-all "chore" and the empty (no-heuristic)
// type are NOT definite. Used by both FileKind and the Classify fast path.
func isDefiniteKind(t string) bool {
	switch t {
	case "test", "docs", "ci", "build":
		return true
	default:
		return false
	}
}

// heuristicType picks a Conventional Commit type from a path alone.
//
// The rules are intentionally narrow: they exist to override obviously
// misclassified LLM output (test file called "feat", docs called
// "chore"), not to replace a proper classifier. Order matters —
// earlier matches win.
func heuristicType(path string) string {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(lower)

	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, ".test.ts"),
		strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".test.js"),
		strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".spec.tsx"),
		strings.HasSuffix(base, ".spec.js"),
		strings.HasPrefix(lower, "test/"),
		strings.HasPrefix(lower, "tests/"),
		strings.HasPrefix(lower, "__tests__/"),
		strings.Contains(lower, "/test/"),
		strings.Contains(lower, "/tests/"),
		strings.Contains(lower, "/__tests__/"):
		return "test"
	case strings.HasSuffix(base, ".md"),
		strings.HasSuffix(base, ".rst"),
		strings.HasSuffix(base, ".adoc"),
		strings.HasPrefix(lower, "docs/"),
		strings.Contains(lower, "/docs/"):
		return "docs"
	case strings.HasPrefix(lower, ".github/"),
		strings.HasPrefix(lower, ".gitlab/"),
		strings.HasPrefix(lower, ".circleci/"),
		strings.HasSuffix(base, ".yml") && strings.Contains(lower, "workflow"),
		base == ".goreleaser.yaml",
		base == ".golangci.yml":
		return "ci"
	case strings.HasPrefix(base, "makefile"),
		base == "dockerfile",
		strings.HasSuffix(base, ".dockerfile"),
		base == "go.sum",
		base == "package-lock.json",
		base == "yarn.lock",
		base == "pnpm-lock.yaml",
		base == "bun.lockb":
		return "build"
	}
	return "" // no heuristic — leave it to the LLM or generic "chore"
}

// heuristicGroups produces groups by grouping files on their heuristic
// type. Files with no heuristic hit fall into "chore".
func heuristicGroups(files []FileChange) []provider.Group {
	buckets := map[string][]string{}
	order := []string{}
	for _, f := range files {
		t := heuristicType(f.Path)
		if t == "" {
			t = "chore"
		}
		if _, seen := buckets[t]; !seen {
			order = append(order, t)
		}
		buckets[t] = append(buckets[t], f.Path)
	}
	sort.Strings(order)
	groups := make([]provider.Group, 0, len(order))
	for _, t := range order {
		groups = append(groups, provider.Group{
			Type:      t,
			Files:     buckets[t],
			Rationale: "heuristic path-based",
		})
	}
	return groups
}

// overrideWithPathRules corrects obvious LLM type mistakes without
// splitting auxiliary files away from the feature they document/test.
func overrideWithPathRules(groups []provider.Group, files []FileChange) []provider.Group {
	if len(groups) == 0 {
		return groups
	}
	fileByPath := map[string]FileChange{}
	for _, f := range files {
		fileByPath[f.Path] = f
	}

	type key struct {
		typ   string
		scope string
	}
	bucket := map[key][]string{}
	var order []key

	addTo := func(t, scope, path string) {
		k := key{typ: t, scope: scope}
		if _, seen := bucket[k]; !seen {
			order = append(order, k)
		}
		bucket[k] = append(bucket[k], path)
	}

	rationale := map[key]string{}
	for _, g := range groups {
		primary := groupHasPrimaryFile(g)
		for _, p := range g.Files {
			// Ignore paths the LLM invented or normalized away — only
			// real gathered files may be committed (otherwise a later
			// `git commit -- <path>` fails on a phantom path).
			if _, known := fileByPath[p]; !known {
				continue
			}
			ht := heuristicType(p)
			if ht != "" && ht != g.Type && !isAuxiliaryForPrimaryGroup(ht, primary) {
				addTo(ht, "", p)
				rationale[key{typ: ht}] = "path-rule override"
				continue
			}
			addTo(g.Type, g.Scope, p)
			if _, ok := rationale[key{typ: g.Type, scope: g.Scope}]; !ok {
				rationale[key{typ: g.Type, scope: g.Scope}] = g.Rationale
			}
		}
	}

	// Coverage guard: the LLM sometimes omits files from every group.
	// Without this they are silently dropped and never committed, forcing
	// the user to re-run `gk commit`. Sweep any uncovered gathered file
	// into its heuristic type (or "chore") so one run commits everything.
	covered := make(map[string]bool)
	for _, ps := range bucket {
		for _, p := range ps {
			covered[p] = true
		}
	}
	for _, f := range files {
		if covered[f.Path] {
			continue
		}
		t := heuristicType(f.Path)
		if t == "" {
			t = "chore"
		}
		addTo(t, "", f.Path)
		if _, ok := rationale[key{typ: t}]; !ok {
			rationale[key{typ: t}] = "swept in (uncovered by classifier)"
		}
	}

	out := make([]provider.Group, 0, len(order))
	for _, k := range order {
		out = append(out, provider.Group{
			Type:      k.typ,
			Scope:     k.scope,
			Files:     bucket[k],
			Rationale: rationale[k],
		})
	}
	return out
}

func groupHasPrimaryFile(g provider.Group) bool {
	for _, p := range g.Files {
		if heuristicType(p) == "" {
			return true
		}
	}
	return false
}

func isAuxiliaryForPrimaryGroup(heuristic string, hasPrimary bool) bool {
	if !hasPrimary {
		return false
	}
	return heuristic == "docs" || heuristic == "test"
}

// toProviderFiles converts []FileChange → []provider.FileChange.
// DiffHint is left empty here — the composer is responsible for
// attaching per-file diffs when it invokes Provider.Compose.
func toProviderFiles(files []FileChange) []provider.FileChange {
	out := make([]provider.FileChange, len(files))
	for i, f := range files {
		out[i] = provider.FileChange{
			Path:     f.Path,
			Status:   f.Status,
			Added:    f.Added,
			Deleted:  f.Deleted,
			IsBinary: f.IsBinary,
			OrigPath: f.OrigPath,
		}
	}
	return out
}
