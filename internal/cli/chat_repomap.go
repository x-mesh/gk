package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// repoMapMaxDepth caps how many directory nesting levels gk chat's REPO_MAP
// expands (aider's repo-map idea, scoped down to structure only — gk chat's
// git-Q&A position doesn't call for a ctags/symbol index): directories at
// nesting depth <= repoMapMaxDepth have their own files and subdirectory
// names listed; a subdirectory that would sit past the cap is still named
// (so its existence is visible) but its own contents collapse to a single
// "..." marker instead of being expanded, so deep trees (vendor/,
// node_modules equivalents, generated code) don't blow up the prompt.
//
// repoMapMaxFiles caps the TOTAL file lines rendered across the whole tree
// (directories don't count against it — only leaves do). Past the cap,
// rendering stops and a trailing line reports how many more files exist,
// so the model knows the tree is partial rather than silently incomplete.
const (
	repoMapMaxDepth = 3
	repoMapMaxFiles = 300
)

// repoTreeDir is one directory node of the in-memory tree built from `git
// ls-files` output, before rendering. dirs is keyed by the directory's own
// (base) name; files holds the (base) names of files directly inside it.
type repoTreeDir struct {
	dirs  map[string]*repoTreeDir
	files []string
}

func newRepoTreeDir() *repoTreeDir {
	return &repoTreeDir{dirs: make(map[string]*repoTreeDir)}
}

// buildRepoTree inserts every path (forward-slash-relative, as `git
// ls-files` reports them) into a directory tree rooted at the repository
// root. Empty/blank entries are skipped so a trailing NUL-split artifact
// can't produce a phantom node.
func buildRepoTree(paths []string) *repoTreeDir {
	root := newRepoTreeDir()
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		segs := strings.Split(p, "/")
		node := root
		for i, seg := range segs {
			if seg == "" {
				continue
			}
			if i == len(segs)-1 {
				node.files = append(node.files, seg)
				continue
			}
			child, ok := node.dirs[seg]
			if !ok {
				child = newRepoTreeDir()
				node.dirs[seg] = child
			}
			node = child
		}
	}
	return root
}

// renderRepoTree walks the tree depth-first, always visiting directories
// and files in sorted-name order — the ONLY thing that makes the output
// deterministic given `git ls-files`, whose own order is index order, not
// a sorted one. Directory and file entries at each level are interleaved
// alphabetically (matching a plain `ls` listing) rather than grouped, so
// the rendering reflects one consistent ordering rule throughout.
//
// maxDepth bounds recursion: a directory's own files and named
// subdirectories are shown as long as the directory's nesting depth is
// <= maxDepth; a subdirectory that would cross past maxDepth is still
// named (so its existence is visible) but not expanded — if it has any
// content, a lone "..." line under it marks the elision instead of
// recursing into it. maxFiles bounds the total file lines printed across
// the whole walk; once reached, remaining files (at any level) are folded
// into one trailing "N more files not shown" summary line instead of
// being silently dropped.
func renderRepoTree(root *repoTreeDir, maxDepth, maxFiles int) string {
	var b strings.Builder
	shown := 0
	remaining := 0 // files that would have been shown past the cap

	type entry struct {
		name  string
		isDir bool
	}

	var walk func(node *repoTreeDir, depth int, indent string)
	walk = func(node *repoTreeDir, depth int, indent string) {
		entries := make([]entry, 0, len(node.dirs)+len(node.files))
		for name := range node.dirs {
			entries = append(entries, entry{name: name, isDir: true})
		}
		for _, name := range node.files {
			entries = append(entries, entry{name: name, isDir: false})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

		for _, e := range entries {
			if !e.isDir {
				if shown >= maxFiles {
					remaining++
					continue
				}
				fmt.Fprintf(&b, "%s%s\n", indent, e.name)
				shown++
				continue
			}
			child := node.dirs[e.name]
			fmt.Fprintf(&b, "%s%s/\n", indent, e.name)
			if depth+1 > maxDepth {
				if len(child.dirs) > 0 || len(child.files) > 0 {
					fmt.Fprintf(&b, "%s  ...\n", indent)
				}
				continue
			}
			walk(child, depth+1, indent+"  ")
		}
	}
	walk(root, 0, "")

	out := strings.TrimRight(b.String(), "\n")
	if remaining > 0 {
		if out != "" {
			out += "\n"
		}
		out += fmt.Sprintf("… %d more file(s) not shown (file cap %d reached)", remaining, maxFiles)
	}
	return out
}

// chatRepoMapString returns gk chat's REPO_MAP block content — a
// depth/file-capped directory tree built from `git ls-files` — gated by
// ai.chat.auto_context. Every exit that isn't "config on AND ls-files
// produced at least one path" degrades to "" rather than erroring: a bare
// repo, an unborn HEAD, or a plain ls-files failure must never abort a
// chat session just because repo-map orientation could not be gathered
// (same degrade contract as collectChatContext).
//
// deny_paths is applied to the raw path list before the tree is built.
// Redaction scrubs secret *values* out of text, which does nothing for a
// tree whose leak is the *filename* itself: without this filter REPO_MAP
// would enumerate exactly the tracked paths that file_list and git_status
// refuse to name (internal/chat/tools/file_tools.go, status_tools.go), so
// merely turning ai.chat.auto_context on would hand the provider the one
// listing the deny policy exists to withhold.
// denyGlobs is the effective chat deny list (defaults ∪ commit denies ∪
// global chat denies) as assembled by runChat — passed in rather than
// recomputed so REPO_MAP can never drift from what the tools enforce.
func chatRepoMapString(ctx context.Context, runner git.Runner, cfg *config.Config, denyGlobs []string) string {
	if cfg == nil || !cfg.AI.Chat.AutoContext || runner == nil {
		return ""
	}
	out, _, err := runner.Run(ctx, "ls-files", "-z")
	if err != nil {
		return ""
	}
	raw := strings.TrimRight(string(out), "\x00")
	if raw == "" {
		return ""
	}
	paths := filterDeniedPaths(strings.Split(raw, "\x00"), denyGlobs)
	if len(paths) == 0 {
		return ""
	}
	tree := buildRepoTree(paths)
	return renderRepoTree(tree, repoMapMaxDepth, repoMapMaxFiles)
}

// filterDeniedPaths drops every path matching deny_paths, using the same
// aicommit.MatchDeny semantics the chat tools apply. A denied path is
// dropped whole: its name never reaches the tree, so no parent directory
// is created for it either when it is the only entry under that parent.
func filterDeniedPaths(paths, denyGlobs []string) []string {
	if len(denyGlobs) == 0 {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if aicommit.MatchDeny(p, denyGlobs) != "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
