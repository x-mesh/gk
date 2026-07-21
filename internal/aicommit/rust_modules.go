package aicommit

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// rustModuleDeclRE recognizes an added Rust module declaration. Restricting
// this to added lines makes the pairing evidence-based: an unrelated change
// to lib.rs or mod.rs cannot pull a new sibling source file into its group.
var rustModuleDeclRE = regexp.MustCompile(`^\+\s*(?:pub(?:\([^)]*\))?\s+)?mod\s+([A-Za-z_][A-Za-z0-9_]*)\s*;`)

// PairRustModuleGroups keeps an added Rust module source file in the same
// commit group as the added declaration that requires it. Without this, a
// generated multi-commit history can contain an intermediate `pub mod x;`
// commit that cannot compile because x.rs lands later.
//
// Only explicit added declarations and added/untracked candidate files move.
// Existing files and unrelated sibling edits remain under the classifier's
// decision. The diff must describe the same change set as files.
func PairRustModuleGroups(groups []provider.Group, files []FileChange, unifiedDiff string) []provider.Group {
	if len(groups) < 2 || len(files) == 0 || unifiedDiff == "" {
		return groups
	}

	newFile := make(map[string]bool, len(files))
	for _, f := range files {
		if f.Status == "added" || f.Status == "untracked" {
			newFile[filepath.ToSlash(f.Path)] = true
		}
	}
	if len(newFile) == 0 {
		return groups
	}

	declarations := rustModuleDeclarations(unifiedDiff)
	if len(declarations) == 0 {
		return groups
	}

	loc := map[string]int{}
	for i, group := range groups {
		for _, path := range group.Files {
			loc[filepath.ToSlash(path)] = i
		}
	}

	out := make([]provider.Group, len(groups))
	for i, group := range groups {
		out[i] = group
		out[i].Files = append([]string(nil), group.Files...)
	}
	for modulePath, declarationPath := range declarations {
		if !newFile[modulePath] {
			continue
		}
		from, sourceOK := loc[modulePath]
		to, declarationOK := loc[declarationPath]
		if !sourceOK || !declarationOK || from == to {
			continue
		}
		out[from].Files = removeGroupFile(out[from].Files, modulePath)
		out[to].Files = append(out[to].Files, modulePath)
	}

	result := out[:0]
	for _, group := range out {
		if len(group.Files) > 0 {
			result = append(result, group)
		}
	}
	return result
}

// rustModuleDeclarations maps candidate module paths to the changed source
// file that declares them. It supports both `foo.rs` and `foo/mod.rs`.
func rustModuleDeclarations(unifiedDiff string) map[string]string {
	result := map[string]string{}
	var path string
	for _, line := range strings.Split(unifiedDiff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			path = strings.TrimPrefix(line, "+++ b/")
			continue
		}
		if path == "" || !strings.HasSuffix(path, ".rs") {
			continue
		}
		match := rustModuleDeclRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		name := match[1]
		result[filepath.ToSlash(filepath.Join(dir, name+".rs"))] = path
		result[filepath.ToSlash(filepath.Join(dir, name, "mod.rs"))] = path
	}
	return result
}

func removeGroupFile(files []string, target string) []string {
	out := files[:0]
	for _, file := range files {
		if filepath.ToSlash(file) != target {
			out = append(out, file)
		}
	}
	return out
}
