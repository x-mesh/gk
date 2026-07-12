package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

type contextConflictJSON struct {
	Schema    int                       `json:"schema"`
	Operation string                    `json:"operation"`
	Count     int                       `json:"count"`
	Files     []contextConflictFileJSON `json:"files"`
}

type contextConflictFileJSON struct {
	Path           string `json:"path"`
	XY             string `json:"xy"`
	Kind           string `json:"kind"`
	Hunks          int    `json:"hunks"`
	WorktreeExists bool   `json:"worktree_exists"`
	Markers        bool   `json:"markers"`
	// Symbols name the nearest enclosing definition above each conflict
	// marker — "which function is fighting", the piece a path alone doesn't
	// tell an agent. Derived locally from the worktree file (weave-style
	// entity context, no external tooling); empty when no marker sits below
	// a recognizable definition.
	Symbols []string                   `json:"symbols,omitempty"`
	Stages  []contextConflictStageJSON `json:"stages,omitempty"`
}

type contextConflictStageJSON struct {
	Stage   int    `json:"stage"`
	Role    string `json:"role"`
	Mode    string `json:"mode"`
	Blob    string `json:"blob"`
	Present bool   `json:"present"`
}

func collectContextConflict(ctx context.Context, runner *git.ExecRunner, c contextJSON) (*contextConflictJSON, error) {
	out, stderr, err := runner.Run(ctx, "status", "--porcelain=v2", "-z")
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	files := parseContextConflictStatus(out)
	if files == nil {
		files = []contextConflictFileJSON{}
	}
	for i := range files {
		files[i].Kind = conflictKindName(files[i].XY)
		files[i].Hunks = conflictHunkCount(runner.Dir, files[i].Path)
		files[i].Markers = files[i].Hunks > 0
		files[i].WorktreeExists = contextConflictWorktreeExists(runner.Dir, files[i].Path)
		if files[i].Markers {
			files[i].Symbols = conflictHunkSymbols(runner.Dir, files[i].Path)
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return &contextConflictJSON{
		Schema:    1,
		Operation: contextConflictOperation(c),
		Count:     len(files),
		Files:     files,
	}, nil
}

func contextConflictOperation(c contextJSON) string {
	if c.InProgress != nil && c.InProgress.Kind != "" {
		return c.InProgress.Kind
	}
	if c.Dirty.Conflicts > 0 {
		return "stash-apply-conflict"
	}
	return "none"
}

func parseContextConflictStatus(raw []byte) []contextConflictFileJSON {
	var files []contextConflictFileJSON
	for _, entry := range splitNULStrings(raw) {
		if !strings.HasPrefix(entry, "u ") {
			continue
		}
		parts := strings.SplitN(entry, " ", 11)
		if len(parts) != 11 {
			continue
		}
		file := contextConflictFileJSON{
			XY:   parts[1],
			Path: parts[10],
			Stages: []contextConflictStageJSON{
				contextConflictStage(1, "base", parts[3], parts[7]),
				contextConflictStage(2, "ours", parts[4], parts[8]),
				contextConflictStage(3, "theirs", parts[5], parts[9]),
			},
		}
		files = append(files, file)
	}
	return files
}

func contextConflictStage(stage int, role, mode, blob string) contextConflictStageJSON {
	return contextConflictStageJSON{
		Stage:   stage,
		Role:    role,
		Mode:    mode,
		Blob:    blob,
		Present: mode != "000000" && !isZeroObjectID(blob),
	}
}

func splitNULStrings(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := strings.Split(string(raw), "\x00")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isZeroObjectID(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return true
}

// conflictHunkSymbols reads the conflicted worktree file and, for each
// `<<<<<<<` marker, walks upward to the nearest definition line (per-extension
// patterns — the same table the live feeds use) to name the entity the
// conflict sits in. "Nearest definition above" is a heuristic, not a scope
// parse: a conflict in the gap between two functions names the one above it,
// which is still the right neighborhood for a human or agent to jump to.
// Capped and deduped like every symbol list; best-effort (unreadable or
// oversized files yield nil).
func conflictHunkSymbols(repoDir, path string) []string {
	p := path
	if !filepath.IsAbs(p) {
		if repoDir == "" {
			repoDir, _ = os.Getwd()
		}
		p = filepath.Join(repoDir, path)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Size() > untrackedProfileMaxBytes {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return conflictSymbolsFromContent(path, string(data))
}

// conflictSymbolsFromContent is the content-only core of conflictHunkSymbols:
// given a file body carrying `<<<<<<<` markers, it names the nearest enclosing
// definition above each marker (per-extension patterns keyed off path). Pure so
// it serves both the worktree reader above and precheck's forecast, which feeds
// it a merged-tree blob rather than a worktree file. Capped and deduped like
// every symbol list.
func conflictSymbolsFromContent(path, content string) []string {
	pats := definitionPatternsFor(path)
	lines := strings.Split(content, "\n")
	var syms []string
	for i, line := range lines {
		if !strings.HasPrefix(line, "<<<<<<<") {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if name := matchDefinition(pats, lines[j]); name != "" {
				syms = appendSymbol(syms, name)
				break
			}
		}
		if len(syms) >= changeProfileSymbolCap {
			break
		}
	}
	return syms
}

func contextConflictWorktreeExists(repoDir, path string) bool {
	p := path
	if !filepath.IsAbs(p) {
		if repoDir == "" {
			repoDir, _ = os.Getwd()
		}
		p = filepath.Join(repoDir, path)
	}
	_, err := os.Stat(p)
	return err == nil
}
