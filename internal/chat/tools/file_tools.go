package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/aicommit"
)

// FileTools exposes direct work-tree reads. Everything routes through the
// Sandbox — these two tools are the widest leak surface in gk chat (an
// arbitrary file's bytes travel to a remote provider), so there is no
// path that bypasses Resolve.
type FileTools struct {
	Sandbox *Sandbox
	// PerFileCap bounds one file read (0 → registry's ResultCap applies
	// anyway; this exists to keep single reads well under it).
	PerFileCap int
}

const defaultPerFileCap = 24 * 1024

type fileReadInput struct {
	Path string `json:"path"`
	// StartLine/EndLine optionally bound the read to a 1-based, inclusive
	// line range so the model can page a large file instead of always
	// getting the (byte-capped) top. Both optional; omit both to read from
	// the top. These names match what tool-callers reach for by reflex —
	// before they existed, a start_line guess was a hard "unknown field"
	// error that stalled investigation on big files.
	StartLine int `json:"start_line,omitempty"`
	EndLine   int `json:"end_line,omitempty"`
}

func (f *FileTools) fileRead(_ context.Context, raw json.RawMessage) (string, error) {
	var in fileReadInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	if in.Path == "" {
		return "", fmt.Errorf("file_read: path is required")
	}
	abs, rel, err := f.Sandbox.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if bin, _ := aicommit.DetectBinary(abs); bin {
		return "", fmt.Errorf("file_read: %s is binary", rel)
	}
	b, rErr := os.ReadFile(abs)
	if rErr != nil {
		return "", fmt.Errorf("file_read: %v", rErr)
	}
	text := string(b)
	if in.StartLine > 0 || in.EndLine > 0 {
		text = sliceLines(text, in.StartLine, in.EndLine)
	}
	limit := f.PerFileCap
	if limit <= 0 {
		limit = defaultPerFileCap
	}
	return capBytes(text, limit), nil
}

// sliceLines returns the 1-based, inclusive [start, end] line range of text.
// start <= 0 means "from the first line"; end <= 0 or past the end means "to
// the last line". A start past the end returns a note rather than "" so the
// model learns the file's real length instead of seeing a blank result.
func sliceLines(text string, start, end int) string {
	lines := strings.Split(text, "\n")
	n := len(lines)
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > n {
		end = n
	}
	if start > n {
		return fmt.Sprintf("(file has %d line(s); start_line %d is past the end)", n, start)
	}
	if end < start {
		end = start
	}
	return strings.Join(lines[start-1:end], "\n")
}

type fileListInput struct {
	Path string `json:"path,omitempty"`
}

func (f *FileTools) fileList(_ context.Context, raw json.RawMessage) (string, error) {
	var in fileListInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	p := in.Path
	if p == "" {
		p = "."
	}
	abs, rel, err := f.Sandbox.Resolve(p)
	if err != nil {
		return "", err
	}
	entries, rErr := os.ReadDir(abs)
	if rErr != nil {
		return "", fmt.Errorf("file_list: %v", rErr)
	}
	var lines []string
	for _, e := range entries {
		name := e.Name()
		if name == ".git" {
			continue
		}
		childRel := name
		if rel != "." {
			childRel = filepath.ToSlash(filepath.Join(rel, name))
		}
		// Denied entries are omitted entirely — listing their names would
		// advertise exactly the files the deny list protects.
		if g := aicommit.MatchDeny(childRel, f.Sandbox.DenyGlobs); g != "" {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return "(empty)", nil
	}
	return strings.Join(lines, "\n"), nil
}

// RegisterFileTools adds the two file tools to the registry.
func RegisterFileTools(r *Registry, f *FileTools) {
	r.Register(Tool{
		Name:        "file_read",
		Description: "Read one text file from the repository work tree (repo-relative path). Optionally pass start_line and/or end_line (1-based, inclusive) to read only that range of a large file; omit both to read from the top (output is byte-capped). Binary files and paths outside the repo are refused.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"path":{"type":"string"},
			"start_line":{"type":"integer","description":"1-based first line to read (inclusive); optional"},
			"end_line":{"type":"integer","description":"1-based last line to read (inclusive); optional"}
		},"required":["path"],"additionalProperties":false}`),
		Handler: f.fileRead,
	})
	r.Register(Tool{
		Name:        "file_list",
		Description: "List entries of one directory in the work tree (repo-relative; default repo root). Directories end with '/'.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"path":{"type":"string"}
		},"additionalProperties":false}`),
		Handler: f.fileList,
	})
}
