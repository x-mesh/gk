package aicommit

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
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
func Classify(
	ctx context.Context,
	p provider.Provider,
	files []FileChange,
	opts ClassifyOptions,
) ([]provider.Group, error) {
	// Drop denied files up front — never sent to the LLM.
	safe := filterSafe(files)
	if len(safe) == 0 {
		return nil, nil
	}

	heuristic := heuristicGroups(safe)
	if opts.HeuristicOnly || isSmallHomogeneous(safe, opts.HybridFileLimit) {
		return heuristic, nil
	}

	// LLM path.
	in := provider.ClassifyInput{
		Files:         toProviderFiles(safe),
		Lang:          opts.Lang,
		AllowedTypes:  opts.AllowedTypes,
		AllowedScopes: opts.AllowedScopes,
	}
	res, err := p.Classify(ctx, in)
	if err != nil {
		return nil, err
	}
	return overrideWithPathRules(res.Groups, safe), nil
}

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

// overrideWithPathRules mutates groups so any file whose heuristic
// type differs from the group's LLM-picked type is moved into its own
// heuristic group. This protects against the common hallucination
// where an LLM classifies `_test.go` as `feat`.
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
		for _, p := range g.Files {
			ht := heuristicType(p)
			if ht != "" && ht != g.Type {
				// override: move this file into the heuristic-typed group.
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

// toProviderFiles converts []FileChange → []provider.FileChange.
// DiffHint is left empty here — the composer is responsible for
// attaching per-file diffs when it invokes Provider.Compose.
func toProviderFiles(files []FileChange) []provider.FileChange {
	out := make([]provider.FileChange, len(files))
	for i, f := range files {
		out[i] = provider.FileChange{
			Path:     f.Path,
			Status:   f.Status,
			IsBinary: f.IsBinary,
		}
	}
	return out
}
