package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	limit := f.PerFileCap
	if limit <= 0 {
		limit = defaultPerFileCap
	}
	fh, oErr := os.Open(abs)
	if oErr != nil {
		return "", fmt.Errorf("file_read: %v", oErr)
	}
	defer fh.Close()
	if in.StartLine > 0 || in.EndLine > 0 {
		return readLineRange(fh, in.StartLine, in.EndLine, limit)
	}
	b, rErr := io.ReadAll(io.LimitReader(fh, int64(limit)+1))
	if rErr != nil {
		return "", fmt.Errorf("file_read: %v", rErr)
	}
	return capBytes(string(b), limit), nil
}

// readLineRange streams the 1-based, inclusive [start, end] line range.
// start <= 0 means "from the first line"; end <= 0 or past the end means "to
// the last line". A start past the end returns a note rather than "" so the
// model learns the file's real length instead of seeing a blank result. The
// reader never retains unselected lines or more than limit+1 selected bytes.
func readLineRange(r io.Reader, start, end, limit int) (string, error) {
	if start <= 0 {
		start = 1
	}
	if end > 0 && end < start {
		end = start
	}
	if limit <= 0 {
		limit = defaultPerFileCap
	}

	br := bufio.NewReader(r)
	var out strings.Builder
	line, lineCount := 1, 0
	selectedAny, selectedLineStarted := false, false
	writeBounded := func(fragment []byte) bool {
		if len(fragment) == 0 {
			return false
		}
		remaining := limit + 1 - out.Len()
		if len(fragment) > remaining {
			fragment = fragment[:remaining]
		}
		out.Write(fragment)
		return out.Len() > limit
	}
	for {
		fragment, err := br.ReadSlice('\n')
		if len(fragment) > 0 {
			lineCount = line
			if line >= start && (end <= 0 || line <= end) {
				if !selectedLineStarted {
					if selectedAny && writeBounded([]byte{'\n'}) {
						return capBytes(out.String(), limit), nil
					}
					selectedAny = true
					selectedLineStarted = true
				}
				if err == nil {
					fragment = fragment[:len(fragment)-1] // newline is a separator, not line content
				}
				if writeBounded(fragment) {
					return capBytes(out.String(), limit), nil
				}
			}
		}

		switch err {
		case nil:
			if end > 0 && line >= end {
				return out.String(), nil
			}
			line++
			selectedLineStarted = false
		case bufio.ErrBufferFull:
			continue // another fragment of the same line
		case io.EOF:
			if start > lineCount {
				return fmt.Sprintf("(file has %d line(s); start_line %d is past the end)", lineCount, start), nil
			}
			return out.String(), nil
		default:
			return "", fmt.Errorf("file_read: %v", err)
		}
	}
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
