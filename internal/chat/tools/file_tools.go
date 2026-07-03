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
	limit := f.PerFileCap
	if limit <= 0 {
		limit = defaultPerFileCap
	}
	return capBytes(string(b), limit), nil
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
		Description: "Read one text file from the repository work tree (repo-relative path). Binary files and paths outside the repo are refused.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"path":{"type":"string"}
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
