package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Client wraps a Runner and provides high-level, type-safe git operations.
type Client struct {
	R Runner
}

// NewClient creates a Client backed by the given Runner.
func NewClient(r Runner) *Client {
	return &Client{R: r}
}

// Raw returns the underlying Runner for advanced callers.
func (c *Client) Raw() Runner { return c.R }

// Fetch runs `git fetch <remote>` or `git fetch <remote> <ref>` if ref != "".
// prune=true adds --prune. If remote is empty, "origin" is used.
func (c *Client) Fetch(ctx context.Context, remote, ref string, prune bool) error {
	if remote == "" {
		remote = "origin"
	}
	args := []string{"fetch"}
	if prune {
		args = append(args, "--prune")
	}
	args = append(args, remote)
	if ref != "" {
		args = append(args, ref)
	}
	_, _, err := c.R.Run(ctx, args...)
	return err
}

// RebaseResult holds the classified outcome of a rebase operation.
type RebaseResult struct {
	Success   bool
	Conflict  bool // conflict occurred
	NothingTo bool // already up-to-date / nothing to do
	Stdout    string
	Stderr    string
}

// RebaseOnto runs `git rebase <upstream>` and returns a classified RebaseResult.
// Conflict cases return a nil error — callers should inspect the Conflict flag.
func (c *Client) RebaseOnto(ctx context.Context, upstream string) (RebaseResult, error) {
	stdout, stderr, err := c.R.Run(ctx, "rebase", upstream)
	res := RebaseResult{
		Stdout: string(stdout),
		Stderr: string(stderr),
	}
	if err == nil {
		res.Success = true
		lower := strings.ToLower(res.Stdout + res.Stderr)
		if strings.Contains(lower, "up to date") ||
			strings.Contains(lower, "nothing to do") ||
			strings.Contains(lower, "already up to date") {
			res.NothingTo = true
		}
		return res, nil
	}
	// Non-zero exit — classify conflict vs fatal.
	combined := res.Stdout + res.Stderr
	if strings.Contains(combined, "CONFLICT") || strings.Contains(combined, "could not apply") {
		res.Conflict = true
		return res, nil
	}
	return res, err
}

// ErrDetachedHEAD is returned by CurrentBranch when HEAD is detached.
var ErrDetachedHEAD = fmt.Errorf("detached HEAD")

// CurrentBranch returns the current branch name via `git symbolic-ref --short HEAD`.
// Returns ("", ErrDetachedHEAD) when HEAD is detached.
func (c *Client) CurrentBranch(ctx context.Context) (string, error) {
	stdout, stderr, err := c.R.Run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		if strings.Contains(string(stderr), "fatal: ref HEAD is not a symbolic ref") ||
			strings.Contains(string(stderr), "not a symbolic ref") {
			return "", ErrDetachedHEAD
		}
		return "", err
	}
	return strings.TrimSpace(string(stdout)), nil
}

// ErrNoDefaultBranch is returned when no default branch can be determined.
var ErrNoDefaultBranch = fmt.Errorf("could not determine default branch")

// DefaultBranch detects the base branch with the following fallback chain:
//  1. git symbolic-ref --short refs/remotes/<remote>/HEAD  (e.g. "origin/main" -> "main")
//  2. git show-ref --verify --quiet refs/remotes/<remote>/develop
//  3. git show-ref --verify --quiet refs/remotes/<remote>/main
//  4. git show-ref --verify --quiet refs/remotes/<remote>/master
//
// Returns ErrNoDefaultBranch if none exist.
func (c *Client) DefaultBranch(ctx context.Context, remote string) (string, error) {
	if remote == "" {
		remote = "origin"
	}

	// Step 1: symbolic-ref
	stdout, _, err := c.R.Run(ctx, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(stdout))
		// ref is like "origin/main"
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) == 2 && parts[1] != "" {
			return parts[1], nil
		}
	}

	// Step 2-4: show-ref fallback
	for _, branch := range []string{"develop", "main", "master"} {
		_, _, err := c.R.Run(ctx, "show-ref", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch)
		if err == nil {
			return branch, nil
		}
	}

	return "", ErrNoDefaultBranch
}

// StatusKind classifies a status entry.
type StatusKind int

const (
	KindOrdinary StatusKind = iota
	KindRenamed
	KindUnmerged
	KindUntracked
	KindIgnored
)

// StatusEntry represents a single entry in `git status --porcelain=v2`.
type StatusEntry struct {
	XY   string
	Path string
	Orig string // original path for renames
	Kind StatusKind
}

// Status holds parsed output of `git status --porcelain=v2 -z --branch`.
type Status struct {
	Branch        string
	Upstream      string
	Ahead, Behind int
	Entries       []StatusEntry
}

// Status runs `git status --porcelain=v2 -z --branch` and returns parsed output.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	stdout, _, err := c.R.Run(ctx, "status", "--porcelain=v2", "-z", "--branch")
	if err != nil {
		return nil, err
	}
	return parsePorcelainV2(stdout)
}

// parsePorcelainV2 parses the NUL-delimited output of `git status --porcelain=v2 -z --branch`.
func parsePorcelainV2(data []byte) (*Status, error) {
	s := &Status{}
	// Split on NUL bytes; filter trailing empty token.
	raw := string(data)
	tokens := strings.Split(raw, "\x00")

	i := 0
	for i < len(tokens) {
		tok := tokens[i]
		if tok == "" {
			i++
			continue
		}

		if strings.HasPrefix(tok, "# ") {
			// Header line.
			parseHeader(s, tok[2:])
			i++
			continue
		}

		if len(tok) == 0 {
			i++
			continue
		}

		switch tok[0] {
		case '1': // ordinary changed entry
			// Format: 1 <xy> <sub> <mH> <mI> <mW> <hH> <hI> <path>
			entry := parseOrdinaryEntry(tok)
			s.Entries = append(s.Entries, entry)
			i++
		case '2': // renamed/copied entry
			// Format: 2 <xy> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>
			// followed by NUL + <origPath>
			entry := parseRenamedEntry(tok)
			if i+1 < len(tokens) {
				entry.Orig = tokens[i+1]
				i++ // consume orig path token
			}
			s.Entries = append(s.Entries, entry)
			i++
		case 'u': // unmerged
			entry := parseUnmergedEntry(tok)
			s.Entries = append(s.Entries, entry)
			i++
		case '?': // untracked
			s.Entries = append(s.Entries, StatusEntry{
				XY:   "??",
				Path: tok[2:],
				Kind: KindUntracked,
			})
			i++
		case '!': // ignored
			s.Entries = append(s.Entries, StatusEntry{
				XY:   "!!",
				Path: tok[2:],
				Kind: KindIgnored,
			})
			i++
		default:
			i++
		}
	}

	return s, nil
}

func parseHeader(s *Status, line string) {
	switch {
	case strings.HasPrefix(line, "branch.head "):
		s.Branch = strings.TrimPrefix(line, "branch.head ")
	case strings.HasPrefix(line, "branch.upstream "):
		s.Upstream = strings.TrimPrefix(line, "branch.upstream ")
	case strings.HasPrefix(line, "branch.ab "):
		// "+<ahead> -<behind>"
		parts := strings.Fields(strings.TrimPrefix(line, "branch.ab "))
		if len(parts) == 2 {
			if n, err := strconv.Atoi(strings.TrimPrefix(parts[0], "+")); err == nil {
				s.Ahead = n
			}
			if n, err := strconv.Atoi(strings.TrimPrefix(parts[1], "-")); err == nil {
				s.Behind = n
			}
		}
	}
}

// parseOrdinaryEntry parses a "1 ..." porcelain v2 line.
// Format: 1 <xy> <sub> <mH> <mI> <mW> <hH> <hI> <path>
func parseOrdinaryEntry(line string) StatusEntry {
	// Fields: [1, xy, sub, mH, mI, mW, hH, hI, path]
	parts := strings.SplitN(line, " ", 9)
	entry := StatusEntry{Kind: KindOrdinary}
	if len(parts) >= 2 {
		entry.XY = parts[1]
	}
	if len(parts) >= 9 {
		entry.Path = parts[8]
	}
	return entry
}

// parseRenamedEntry parses a "2 ..." porcelain v2 line.
// Format: 2 <xy> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>
func parseRenamedEntry(line string) StatusEntry {
	parts := strings.SplitN(line, " ", 10)
	entry := StatusEntry{Kind: KindRenamed}
	if len(parts) >= 2 {
		entry.XY = parts[1]
	}
	if len(parts) >= 10 {
		entry.Path = parts[9]
	}
	return entry
}

// parseUnmergedEntry parses a "u ..." porcelain v2 line.
// Format: u <xy> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
func parseUnmergedEntry(line string) StatusEntry {
	parts := strings.SplitN(line, " ", 11)
	entry := StatusEntry{Kind: KindUnmerged}
	if len(parts) >= 2 {
		entry.XY = parts[1]
	}
	if len(parts) >= 11 {
		entry.Path = parts[10]
	}
	return entry
}

// IsDirty returns true if any tracked changes exist (untracked files ignored).
// Uses `git status --porcelain=v1 -uno`.
func (c *Client) IsDirty(ctx context.Context) (bool, error) {
	stdout, _, err := c.R.Run(ctx, "status", "--porcelain=v1", "-uno")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(stdout)) != "", nil
}

// GitDir returns the `.git` directory path via `git rev-parse --git-dir`.
func (c *Client) GitDir(ctx context.Context) (string, error) {
	stdout, _, err := c.R.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(stdout)), nil
}

// GitCommonDir returns the common git directory via `git rev-parse --git-common-dir`.
// For worktrees this differs from GitDir.
func (c *Client) GitCommonDir(ctx context.Context) (string, error) {
	stdout, _, err := c.R.Run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(stdout)), nil
}

// CheckRefFormat validates a ref name via `git check-ref-format --branch <ref>`.
// Returns nil if valid; non-nil error otherwise.
func (c *Client) CheckRefFormat(ctx context.Context, ref string) error {
	_, stderr, err := c.R.Run(ctx, "check-ref-format", "--branch", ref)
	if err != nil {
		return fmt.Errorf("invalid ref %q: %s", ref, strings.TrimSpace(string(stderr)))
	}
	return nil
}
