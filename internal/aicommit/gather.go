package aicommit

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// Scope selects which WIP entries GatherWIP returns.
type Scope int

const (
	// ScopeAll returns staged + unstaged + untracked (default).
	ScopeAll Scope = iota
	// ScopeStagedOnly drops unstaged-only edits and untracked files.
	ScopeStagedOnly
	// ScopeUnstagedOnly drops staged-only entries.
	ScopeUnstagedOnly
)

// GatherOptions controls GatherWIP.
type GatherOptions struct {
	Scope     Scope
	DenyPaths []string
}

// GatherWIP runs `git status --porcelain=v2 -z --untracked-files=all`
// and returns the normalised list of FileChange entries.
//
// DeniedBy is populated for paths matching any DenyPaths glob; those
// entries are still returned so callers can warn, but must never be
// forwarded to a provider.
func GatherWIP(ctx context.Context, runner git.Runner, opts GatherOptions) ([]FileChange, error) {
	stdout, _, err := runner.Run(ctx,
		"status", "--porcelain=v2", "--untracked-files=all", "-z",
	)
	if err != nil {
		return nil, fmt.Errorf("aicommit: git status: %w", err)
	}

	entries, err := parsePorcelainV2(stdout)
	if err != nil {
		return nil, err
	}

	out := make([]FileChange, 0, len(entries))
	for _, e := range entries {
		if !e.matchesScope(opts.Scope) {
			continue
		}
		if denyGlob := matchDeny(e.Path, opts.DenyPaths); denyGlob != "" {
			e.DeniedBy = denyGlob
		}
		// Binary detection — sniff the on-disk file so downstream stages
		// (summariseForSecretScan, concatFileDiffs, prompt builders) can
		// skip blobs like __pycache__/*.pyc, *.so, images. Without this
		// the IsBinary flag is always false and binary content leaks into
		// LLM payloads, blowing up token budgets and producing garbage in
		// --show-prompt output.
		if e.DeniedBy == "" {
			if isBin, _ := DetectBinary(e.Path); isBin {
				e.IsBinary = true
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// matchesScope filters entries according to Scope rules.
func (e FileChange) matchesScope(s Scope) bool {
	switch s {
	case ScopeStagedOnly:
		return e.Staged
	case ScopeUnstagedOnly:
		return e.Unstaged || e.Status == "untracked"
	default:
		return true
	}
}

// matchDeny returns the first glob in patterns that matches path
// (by basename first, then full path). Empty result means no match.
func matchDeny(path string, patterns []string) string {
	base := filepath.Base(path)
	for _, g := range patterns {
		if g == "" {
			continue
		}
		if ok, _ := filepath.Match(g, base); ok {
			return g
		}
		if ok, _ := filepath.Match(g, path); ok {
			return g
		}
	}
	return ""
}

// parsePorcelainV2 parses `git status --porcelain=v2 -z` output.
//
// Record types handled:
//
//	"1"  changed entry (ordinary, plus M/D against HEAD and index)
//	"2"  renamed/copied entry (followed by a NUL-separated original path)
//	"u"  unmerged entry
//	"?"  untracked
//	"!"  ignored (dropped)
//
// Reference: https://git-scm.com/docs/git-status#_porcelain_format_version_2
func parsePorcelainV2(data []byte) ([]FileChange, error) {
	var out []FileChange
	// With --porcelain=v2 -z, records are NUL-terminated. Renamed/copied
	// records carry a second NUL-terminated path immediately after, so
	// we can't just SplitN on NUL and parse each chunk independently.
	i := 0
	for i < len(data) {
		end := bytes.IndexByte(data[i:], 0)
		if end < 0 {
			break
		}
		line := string(data[i : i+end])
		i += end + 1
		if line == "" {
			continue
		}
		switch line[0] {
		case '1':
			e, err := parseV2Ordinary(line)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		case '2':
			// The original path is the next NUL-terminated token.
			end2 := bytes.IndexByte(data[i:], 0)
			if end2 < 0 {
				return nil, fmt.Errorf("aicommit: porcelain v2: rename record missing orig path")
			}
			orig := string(data[i : i+end2])
			i += end2 + 1
			e, err := parseV2Rename(line, orig)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		case 'u':
			e, err := parseV2Unmerged(line)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		case '?':
			// "? path"
			if len(line) < 3 {
				return nil, fmt.Errorf("aicommit: porcelain v2: bad untracked record %q", line)
			}
			out = append(out, FileChange{
				Path:   line[2:],
				Status: "untracked",
			})
		case '!':
			// ignored — drop.
		default:
			return nil, fmt.Errorf("aicommit: porcelain v2: unknown record type %q", line[:1])
		}
	}
	return out, nil
}

// parseV2Ordinary parses a "1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>" line.
func parseV2Ordinary(line string) (FileChange, error) {
	parts := strings.SplitN(line, " ", 9)
	if len(parts) < 9 {
		return FileChange{}, fmt.Errorf("aicommit: porcelain v2: short ordinary record %q", line)
	}
	xy := parts[1]
	path := parts[8]
	return classifyXY(path, xy, "", false), nil
}

// parseV2Rename parses a "2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>" line.
func parseV2Rename(line, orig string) (FileChange, error) {
	parts := strings.SplitN(line, " ", 10)
	if len(parts) < 10 {
		return FileChange{}, fmt.Errorf("aicommit: porcelain v2: short rename record %q", line)
	}
	xy := parts[1]
	path := parts[9]
	fc := classifyXY(path, xy, orig, true)
	return fc, nil
}

// parseV2Unmerged parses a "u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>" line.
func parseV2Unmerged(line string) (FileChange, error) {
	parts := strings.SplitN(line, " ", 11)
	if len(parts) < 11 {
		return FileChange{}, fmt.Errorf("aicommit: porcelain v2: short unmerged record %q", line)
	}
	return FileChange{
		Path:     parts[10],
		Status:   "unmerged",
		Staged:   false,
		Unstaged: true,
	}, nil
}

// classifyXY maps the two-letter porcelain code to FileChange fields.
//
//	X = index-vs-HEAD  (staged side)
//	Y = worktree-vs-index (unstaged side)
//
// '.' means unchanged; A/M/D/R/C/T/U each denote the same letter's
// meaning as in plain porcelain.
func classifyXY(path, xy, orig string, renameRecord bool) FileChange {
	if len(xy) < 2 {
		return FileChange{Path: path, Status: "modified"}
	}
	x, y := rune(xy[0]), rune(xy[1])
	fc := FileChange{
		Path:     path,
		OrigPath: orig,
		Staged:   x != '.',
		Unstaged: y != '.',
	}
	// Pick the more informative side for Status.
	switch {
	case renameRecord:
		fc.Status = "renamed"
		if x == 'C' || y == 'C' {
			fc.Status = "copied"
		}
	case x == 'A' || y == 'A':
		fc.Status = "added"
	case x == 'D' || y == 'D':
		fc.Status = "deleted"
	default:
		fc.Status = "modified"
	}
	return fc
}
