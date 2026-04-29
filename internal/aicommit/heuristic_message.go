package aicommit

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// heuristicModel is the Model value attached to Messages produced
// without an LLM round-trip. Surfaces in the audit log so operators can
// tell heuristic commits apart from provider-generated ones.
const heuristicModel = "heuristic"

// CountHeuristicGroups returns how many of the given groups will be
// composed without an LLM round-trip. The CLI uses it to surface
// "X of Y messages composed without LLM" in the status line so users
// understand where the token savings come from.
func CountHeuristicGroups(groups []provider.Group, lang string) int {
	n := 0
	for _, g := range groups {
		if _, ok := heuristicMessage(g, lang); ok {
			n++
		}
	}
	return n
}

// heuristicMessage returns a deterministic Message for groups that
// don't need an LLM round-trip. ok=false means the caller must fall
// back to Provider.Compose.
//
// Why: lockfile-only diffs routinely run 50K+ tokens per group and
// produce no insight the path alone wouldn't tell us. CI-config-only
// groups are similar. Skipping the round-trip eliminates the dominant
// source of TPD exhaustion (Groq llama-3.3 daily 100K limit was the
// trigger).
func heuristicMessage(g provider.Group, lang string) (Message, bool) {
	switch g.Type {
	case "build":
		if allLockfiles(g.Files) {
			return Message{
				Group:    g,
				Subject:  lockfileSubject(g.Files, lang),
				Attempts: 0,
				Model:    heuristicModel,
			}, true
		}
	case "ci":
		if allCIFiles(g.Files) {
			return Message{
				Group:    g,
				Subject:  ciSubject(g.Files, lang),
				Attempts: 0,
				Model:    heuristicModel,
			}, true
		}
	}
	return Message{}, false
}

// lockfileNames are the bare basenames matched as "lockfile" for the
// build-group bypass. Stored as a set so heuristicMessage stays O(n).
var lockfileNames = map[string]string{
	"go.sum":            "go",
	"package-lock.json": "npm",
	"yarn.lock":         "yarn",
	"pnpm-lock.yaml":    "pnpm",
	"bun.lockb":         "bun",
	"bun.lock":          "bun",
	"Cargo.lock":        "cargo",
	"Gemfile.lock":      "bundler",
	"poetry.lock":       "poetry",
	"uv.lock":           "uv",
	"Pipfile.lock":      "pipenv",
	"composer.lock":     "composer",
	"mix.lock":          "mix",
	"pubspec.lock":      "pub",
	"flake.lock":        "nix",
}

func allLockfiles(files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, p := range files {
		if _, ok := lockfileNames[filepath.Base(p)]; !ok {
			return false
		}
	}
	return true
}

// ciDirPrefixes are top-level directories the CI bypass recognises.
// A group matches when EVERY file sits under one of these prefixes.
var ciDirPrefixes = []string{
	".github/",
	".gitlab/",
	".circleci/",
	".buildkite/",
	".azure/",
	".drone/",
	".travis/",
}

func allCIFiles(files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, p := range files {
		if !hasAnyPrefix(filepath.ToSlash(p), ciDirPrefixes) {
			return false
		}
	}
	return true
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func lockfileSubject(files []string, lang string) string {
	managers := lockfileManagers(files)
	if lang == "ko" {
		if len(managers) == 1 {
			return managers[0] + " 락파일 갱신"
		}
		return "락파일 갱신"
	}
	if len(managers) == 1 {
		return "update " + managers[0] + " lockfile"
	}
	return "update lockfiles"
}

func lockfileManagers(files []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range files {
		m, ok := lockfileNames[filepath.Base(p)]
		if !ok || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func ciSubject(files []string, lang string) string {
	dirs := ciDirsTouched(files)
	if lang == "ko" {
		if len(dirs) == 1 {
			return dirs[0] + " 워크플로우 업데이트"
		}
		return "CI 설정 업데이트"
	}
	if len(dirs) == 1 {
		return "update " + dirs[0] + " workflows"
	}
	return "update CI configuration"
}

func ciDirsTouched(files []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range files {
		p = filepath.ToSlash(p)
		for _, pfx := range ciDirPrefixes {
			if !strings.HasPrefix(p, pfx) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(pfx, "."), "/")
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
			break
		}
	}
	sort.Strings(out)
	return out
}
