package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/diff"
	"github.com/x-mesh/gk/internal/git"
)

// GitTools binds the whitelisted read-only git surface (log/show/diff/
// blame/grep — the whole list, there is no way to reach other
// subcommands) to a Runner. Every model-supplied string is validated at
// execution time: refs may not look like flags, paths go through the
// Sandbox, and pathspecs always follow a `--` separator, so a hostile
// tool input can never smuggle `--exec-path`-style options (the same
// blocked-args rule aichat's executor enforces).
type GitTools struct {
	Runner    git.Runner
	Sandbox   *Sandbox
	DenyGlobs []string
}

const gitLogMaxCommits = 200

// validateRef rejects revision arguments that could parse as flags or
// inject extra arguments. Git itself validates ref syntax afterwards.
func validateRef(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid revision %q: may not start with '-'", ref)
	}
	if strings.ContainsAny(ref, " \t\n\x00") {
		return fmt.Errorf("invalid revision %q: whitespace not allowed", ref)
	}
	return nil
}

// resolvePaths sandbox-validates each model-supplied path and returns the
// repo-relative forms for use after a `--` separator.
func (g *GitTools) resolvePaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		_, rel, err := g.Sandbox.Resolve(p)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, nil
}

func (g *GitTools) run(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, err := g.Runner.Run(ctx, args...)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return string(stdout), nil
}

// ── git_log ───────────────────────────────────────────────────────────

type gitLogInput struct {
	Range string   `json:"range,omitempty"`
	Limit int      `json:"limit,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

func (g *GitTools) gitLog(ctx context.Context, raw json.RawMessage) (string, error) {
	var in gitLogInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	if err := validateRef(in.Range); err != nil {
		return "", err
	}
	limit := in.Limit
	if limit <= 0 || limit > gitLogMaxCommits {
		limit = 30
	}
	// Metadata only — no -p. Patch content is git_diff/git_show territory
	// where deny filtering applies; keeping log patch-free removes a whole
	// leak path.
	args := []string{"log", "--no-color", "--date=iso-strict",
		"--format=%h %ad %an %s", "-n", strconv.Itoa(limit)}
	if in.Range != "" {
		args = append(args, in.Range)
	}
	if len(in.Paths) > 0 {
		rels, err := g.resolvePaths(in.Paths)
		if err != nil {
			return "", err
		}
		args = append(args, "--")
		args = append(args, rels...)
	}
	return g.run(ctx, args...)
}

// ── git_show ──────────────────────────────────────────────────────────

type gitShowInput struct {
	Ref  string `json:"ref"`
	Path string `json:"path,omitempty"`
}

func (g *GitTools) gitShow(ctx context.Context, raw json.RawMessage) (string, error) {
	var in gitShowInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	if in.Ref == "" {
		return "", fmt.Errorf("git_show: ref is required")
	}
	if err := validateRef(in.Ref); err != nil {
		return "", err
	}
	if in.Path != "" {
		// Single-file content: deny_paths enforced structurally by the
		// sandbox BEFORE git runs — historic content of a denied file is
		// exactly what the deny list exists to protect.
		_, rel, err := g.Sandbox.Resolve(in.Path)
		if err != nil {
			return "", err
		}
		return g.run(ctx, "show", "--no-color", in.Ref+":"+rel)
	}
	// Whole commit: message + patch. The patch may touch denied files —
	// drop those blocks before anything else sees the output.
	out, err := g.run(ctx, "show", "--no-color", in.Ref)
	if err != nil {
		return "", err
	}
	filtered, dropped := aicommit.FilterDiffByDeny(out, g.DenyGlobs)
	return appendDropNote(filtered, dropped), nil
}

// ── git_diff ──────────────────────────────────────────────────────────

type gitDiffInput struct {
	Range string   `json:"range,omitempty"`
	Paths []string `json:"paths,omitempty"`
	// Raw switches from the default digest (per-file stats + changed
	// symbols) to the full patch. Digest-first keeps results inside the
	// 32KB cap for typical ranges.
	Raw bool `json:"raw,omitempty"`
}

func (g *GitTools) gitDiff(ctx context.Context, raw json.RawMessage) (string, error) {
	var in gitDiffInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	if err := validateRef(in.Range); err != nil {
		return "", err
	}
	args := []string{"diff", "--no-color"}
	if in.Range != "" {
		args = append(args, in.Range)
	}
	if len(in.Paths) > 0 {
		rels, err := g.resolvePaths(in.Paths)
		if err != nil {
			return "", err
		}
		args = append(args, "--")
		args = append(args, rels...)
	}
	out, err := g.run(ctx, args...)
	if err != nil {
		return "", err
	}
	filtered, dropped := aicommit.FilterDiffByDeny(out, g.DenyGlobs)
	if in.Raw {
		return appendDropNote(filtered, dropped), nil
	}
	res, pErr := diff.ParseUnifiedDiff(strings.NewReader(filtered))
	if pErr != nil {
		// Unparseable diff → fall back to the filtered raw patch rather
		// than failing the tool call.
		return appendDropNote(filtered, dropped), nil
	}
	dg := diff.BuildDigest(res)
	b, mErr := json.MarshalIndent(dg, "", "  ")
	if mErr != nil {
		return "", fmt.Errorf("git_diff: encode digest: %w", mErr)
	}
	return appendDropNote(string(b), dropped), nil
}

// ── git_blame ─────────────────────────────────────────────────────────

type gitBlameInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

func (g *GitTools) gitBlame(ctx context.Context, raw json.RawMessage) (string, error) {
	var in gitBlameInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	if in.Path == "" {
		return "", fmt.Errorf("git_blame: path is required")
	}
	_, rel, err := g.Sandbox.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	args := []string{"blame", "--date=short"}
	if in.StartLine > 0 {
		end := in.EndLine
		if end < in.StartLine {
			end = in.StartLine
		}
		args = append(args, "-L", fmt.Sprintf("%d,%d", in.StartLine, end))
	}
	args = append(args, "--", rel)
	return g.run(ctx, args...)
}

// ── git_grep ──────────────────────────────────────────────────────────

type gitGrepInput struct {
	Pattern string   `json:"pattern"`
	Paths   []string `json:"paths,omitempty"`
}

func (g *GitTools) gitGrep(ctx context.Context, raw json.RawMessage) (string, error) {
	var in gitGrepInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return "", fmt.Errorf("git_grep: pattern is required")
	}
	// -e isolates the pattern from option parsing, so a leading '-' in a
	// model-supplied pattern is data, not a flag.
	args := []string{"grep", "-n", "--no-color", "-I", "-e", in.Pattern, "--"}
	if len(in.Paths) > 0 {
		rels, err := g.resolvePaths(in.Paths)
		if err != nil {
			return "", err
		}
		args = append(args, rels...)
	} else {
		args = append(args, ".")
	}
	// Structural exclusion: denied files never enter the match set, so
	// their content cannot appear as match lines. Both anchored and
	// any-depth spellings cover basename globs like ".env".
	for _, glob := range g.DenyGlobs {
		if glob == "" {
			continue
		}
		args = append(args,
			":(exclude,glob)"+glob,
			":(exclude,glob)**/"+glob,
		)
	}
	out, err := g.run(ctx, args...)
	if err != nil {
		// Exit 1 with empty output means "no matches" — normalize.
		if strings.Contains(err.Error(), "git grep:") && strings.TrimSpace(out) == "" {
			return "(no matches)", nil
		}
		return "", err
	}
	return out, nil
}

// appendDropNote tells the model that blocks were withheld — silence
// would read as "those files didn't change".
func appendDropNote(out string, dropped []string) string {
	if len(dropped) == 0 {
		return out
	}
	return out + fmt.Sprintf("\n[%d file(s) withheld by deny_paths: %s]",
		len(dropped), strings.Join(dropped, ", "))
}

// strictUnmarshal rejects unknown fields so a hallucinated argument fails
// loudly instead of being silently ignored.
func strictUnmarshal(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid tool input: %v", err)
	}
	return nil
}

// RegisterGitTools adds the five git tools to the registry.
func RegisterGitTools(r *Registry, g *GitTools) {
	r.Register(Tool{
		Name:        "git_log",
		Description: "List commits (hash, date, author, subject). Optional revision range (e.g. 'v1.0..HEAD'), limit (default 30, max 200), and path filters. No patch content — use git_diff or git_show for content.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"range":{"type":"string","description":"revision range like main..HEAD or a single ref"},
			"limit":{"type":"integer","description":"max commits, default 30"},
			"paths":{"type":"array","items":{"type":"string"},"description":"restrict to these repo-relative paths"}
		},"additionalProperties":false}`),
		Handler: g.gitLog,
	})
	r.Register(Tool{
		Name:        "git_show",
		Description: "Show one commit (message + patch) by ref, or one file's content at a ref when path is given.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"ref":{"type":"string","description":"commit ref (sha, tag, HEAD~2, ...)"},
			"path":{"type":"string","description":"optional repo-relative file to show at that ref"}
		},"required":["ref"],"additionalProperties":false}`),
		Handler: g.gitShow,
	})
	r.Register(Tool{
		Name:        "git_diff",
		Description: "Diff the working tree or a revision range. Default returns a structured digest (per-file stats + changed symbols); set raw=true for the full patch.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"range":{"type":"string","description":"revision range like main..HEAD; empty = working tree vs HEAD"},
			"paths":{"type":"array","items":{"type":"string"}},
			"raw":{"type":"boolean","description":"full patch instead of digest"}
		},"additionalProperties":false}`),
		Handler: g.gitDiff,
	})
	r.Register(Tool{
		Name:        "git_blame",
		Description: "Show line-by-line authorship for a file, optionally limited to a line range.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"path":{"type":"string"},
			"start_line":{"type":"integer"},
			"end_line":{"type":"integer"}
		},"required":["path"],"additionalProperties":false}`),
		Handler: g.gitBlame,
	})
	r.Register(Tool{
		Name:        "git_grep",
		Description: "Search tracked file contents with a regex pattern. Returns file:line matches.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"pattern":{"type":"string"},
			"paths":{"type":"array","items":{"type":"string"}}
		},"required":["pattern"],"additionalProperties":false}`),
		Handler: g.gitGrep,
	})
}
