package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// noiseDirs lists directory names that are almost always build output,
// dependencies, or caches — committed by mistake when .gitignore is
// missing. Kept deliberately conservative: ambiguous names like build/,
// dist/, target/, vendor/ are NOT included because they can be real source
// directories in some projects, and the guard excludes files from the AI
// classify scope.
var noiseDirs = map[string]bool{
	"node_modules":  true,
	"__pycache__":   true,
	".venv":         true,
	"venv":          true,
	".pytest_cache": true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	".tox":          true,
}

// noiseNames / noiseExts cover individual junk files regardless of location.
var noiseNames = map[string]bool{".DS_Store": true, "Thumbs.db": true}
var noiseExts = map[string]bool{".pyc": true, ".pyo": true, ".sqlite": true, ".sqlite3": true, ".db": true}

// isNoisePath reports whether path is a well-known non-source artifact.
func isNoisePath(p string) bool {
	p = filepath.ToSlash(p)
	base := filepath.Base(p)
	if noiseNames[base] {
		return true
	}
	if noiseExts[strings.ToLower(filepath.Ext(base))] {
		return true
	}
	for _, c := range strings.Split(p, "/") {
		if noiseDirs[c] {
			return true
		}
	}
	return false
}

// noiseGitignorePatterns derives a small set of .gitignore patterns that
// cover the given noise files: a directory entry (e.g. "node_modules/") for
// anything under a noise dir, else a glob for the extension or the bare name.
func noiseGitignorePatterns(noise []aicommit.FileChange) []string {
	set := map[string]bool{}
	for _, f := range noise {
		p := filepath.ToSlash(f.Path)
		base := filepath.Base(p)
		matched := false
		for _, c := range strings.Split(p, "/") {
			if noiseDirs[c] {
				set[c+"/"] = true
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if noiseNames[base] {
			set[base] = true
			continue
		}
		if ext := strings.ToLower(filepath.Ext(base)); noiseExts[ext] {
			set["*"+ext] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// guardNoiseFiles drops well-known non-source artifacts from the commit
// scope so they never reach the AI classifier (which is what blew the token
// budget when .gitignore was missing). It explains what was excluded and, on
// a TTY, offers to add the patterns to .gitignore so the problem doesn't
// recur. Returns the kept files. Secret-denied files are left untouched —
// the secret gate owns those.
func guardNoiseFiles(ctx context.Context, cmd *cobra.Command, runner git.Runner, files []aicommit.FileChange) []aicommit.FileChange {
	var kept, noise []aicommit.FileChange
	for _, f := range files {
		if f.DeniedBy == "" && isNoisePath(f.Path) {
			noise = append(noise, f)
		} else {
			kept = append(kept, f)
		}
	}
	if len(noise) == 0 {
		return files
	}

	w := cmd.ErrOrStderr()
	pats := noiseGitignorePatterns(noise)
	fmt.Fprintf(w, "commit: %d file(s) look like build output / dependencies / caches / local DBs and are usually not committed — excluding them from the AI scope.\n", len(noise))
	fmt.Fprintf(w, "  matched: %s\n", strings.Join(pats, ", "))

	repoRoot, _ := gitToplevel(ctx, runner)
	if ui.IsTerminal() && repoRoot != "" {
		ok, err := ui.Confirm("Add these to .gitignore?", true)
		if err == nil && ok {
			added, gerr := appendGitignore(repoRoot, pats)
			switch {
			case gerr != nil:
				fmt.Fprintf(w, "  could not update .gitignore: %v\n", gerr)
			case len(added) > 0:
				fmt.Fprintf(w, "  added to .gitignore: %s\n", strings.Join(added, ", "))
				fmt.Fprintln(w, "  hint: already-tracked copies need `git rm --cached <path>` to stop tracking")
			}
		}
	} else if repoRoot != "" {
		fmt.Fprintln(w, "  hint: add these to .gitignore to stop tracking them")
	}
	return kept
}

// appendGitignore adds patterns not already present to repoRoot/.gitignore,
// creating it if needed. Returns the patterns actually added.
func appendGitignore(repoRoot string, patterns []string) ([]string, error) {
	path := filepath.Join(repoRoot, ".gitignore")
	data, _ := os.ReadFile(path)
	existing := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		existing[strings.TrimSpace(line)] = true
	}
	var toAdd []string
	for _, p := range patterns {
		if !existing[p] {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return nil, err
		}
	}
	if _, err := f.WriteString("\n# Added by gk: build output / dependencies / caches\n"); err != nil {
		return nil, err
	}
	for _, p := range toAdd {
		if _, err := f.WriteString(p + "\n"); err != nil {
			return nil, err
		}
	}
	return toAdd, nil
}
