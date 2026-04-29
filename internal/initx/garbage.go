package initx

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// GarbageDetection records compiled-artifact files that already exist
// in the working tree at gk init time. The CLI surfaces these so the
// user can choose to `git rm -rf --cached` them — adding a .gitignore
// pattern alone won't unstage already-tracked files.
type GarbageDetection struct {
	Pattern string   // the matching glob from CompiledArtifactPatterns
	Sample  []string // up to garbageSampleLimit example paths
	Count   int      // total matches (>= len(Sample))
}

// garbageSampleLimit caps Sample length so a node_modules-deep tree
// can't produce a 10K-line warning.
const garbageSampleLimit = 5

// garbageWalkFileLimit caps total files visited per scan. Stops the
// walk on large monorepos so gk init never hangs on AnalyzeProject.
// 50K is enough to find leftover .pyc in any reasonable repo while
// finishing in well under a second on SSD.
const garbageWalkFileLimit = 50_000

// garbageSkipDirs are top-level directories the scanner refuses to
// descend into. They're either huge (vendor/), expected to contain
// build output we don't care about (target/), or meaningless to a
// gitignore audit (.git/).
var garbageSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"build":        true,
	"dist":         true,
	".venv":        true,
	"venv":         true,
}

// DetectExistingGarbage walks dir looking for files matching any
// CompiledArtifactPatterns entry. Empty result means the working tree
// is clean.
//
// The walk is bounded (depth-first via filepath.WalkDir, with skip
// directories + a hard file-count limit) so it stays cheap even on
// monorepos. Errors during walking degrade gracefully — partial
// results are returned rather than aborting init entirely.
func DetectExistingGarbage(dir string) []GarbageDetection {
	if dir == "" {
		return nil
	}

	// Map: pattern → matches. We track the actual files per pattern
	// so the CLI can show a sample.
	hits := map[string]*GarbageDetection{}
	visited := 0

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable directory shouldn't kill the whole walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == dir {
			return nil
		}
		if d.IsDir() {
			if garbageSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}

		visited++
		if visited > garbageWalkFileLimit {
			return filepath.SkipAll
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		for _, pat := range CompiledArtifactPatterns {
			if !matchArtifactPattern(pat, rel) {
				continue
			}
			h, ok := hits[pat]
			if !ok {
				h = &GarbageDetection{Pattern: pat}
				hits[pat] = h
			}
			h.Count++
			if len(h.Sample) < garbageSampleLimit {
				h.Sample = append(h.Sample, rel)
			}
			break
		}
		return nil
	})

	if len(hits) == 0 {
		return nil
	}

	out := make([]GarbageDetection, 0, len(hits))
	for _, h := range hits {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out
}

// matchArtifactPattern matches a CompiledArtifactPatterns entry against
// a forward-slash relative path. Handles three pattern shapes used in
// that list:
//
//   - "*.pyc" — basename glob, match anywhere
//   - "__pycache__/" — directory marker, match when any component equals
//     the trimmed name
//   - "coverage/" — same; trailing slash means directory component
//
// Bare basenames fall through to filepath.Match against the basename.
func matchArtifactPattern(pattern, relPath string) bool {
	// Directory marker: "foo/" matches when "foo" appears as a path
	// component anywhere.
	if strings.HasSuffix(pattern, "/") {
		dir := strings.TrimSuffix(pattern, "/")
		for _, c := range strings.Split(relPath, "/") {
			if c == dir {
				return true
			}
		}
		return false
	}
	// Otherwise: basename glob.
	base := filepath.Base(relPath)
	if ok, _ := filepath.Match(pattern, base); ok {
		return true
	}
	return false
}
