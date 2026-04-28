// Package stash wraps `git stash` so the CLI can present a friendly
// list/apply/drop UX without scattering shell-out logic across cli/.
package stash

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// Entry is one row from `git stash list`.
type Entry struct {
	Ref     string    // "stash@{0}"
	Index   int       // 0-based index parsed from Ref
	Created time.Time // committer date of the stash commit
	Message string    // free-form, usually "WIP on <branch>: <subject>"
}

// List returns every stash currently held by the repo (newest first —
// git's natural order).
func List(ctx context.Context, runner git.Runner) ([]Entry, error) {
	// Use git's %x00 placeholder so the format *string* stays NUL-free
	// (macOS' exec() rejects argv entries containing NUL); git emits a
	// real NUL byte into the *output* at render time.
	stdout, stderr, err := runner.Run(ctx,
		"stash", "list",
		"--format=%gd%x00%ct%x00%s",
	)
	if err != nil {
		return nil, fmt.Errorf("stash list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	var out []Entry
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) < 3 {
			continue
		}
		idx := parseStashIndex(parts[0])
		var created time.Time
		if n, err := strconv.ParseInt(parts[1], 10, 64); err == nil && n > 0 {
			created = time.Unix(n, 0)
		}
		out = append(out, Entry{
			Ref:     parts[0],
			Index:   idx,
			Created: created,
			Message: parts[2],
		})
	}
	return out, nil
}

// Push records the current working tree as a new stash. message is
// optional; includeUntracked covers files not yet tracked by git.
func Push(ctx context.Context, runner git.Runner, message string, includeUntracked bool) error {
	args := []string{"stash", "push"}
	if includeUntracked {
		args = append(args, "--include-untracked")
	}
	if strings.TrimSpace(message) != "" {
		args = append(args, "-m", message)
	}
	_, stderr, err := runner.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("stash push: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// Apply restores the stash to the working tree without removing it.
func Apply(ctx context.Context, runner git.Runner, ref string) error {
	_, stderr, err := runner.Run(ctx, "stash", "apply", ref)
	if err != nil {
		return fmt.Errorf("stash apply %s: %s: %w", ref, strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// Pop applies the stash and drops it on success — the standard
// "tuck this aside, then put it back" cycle.
func Pop(ctx context.Context, runner git.Runner, ref string) error {
	_, stderr, err := runner.Run(ctx, "stash", "pop", ref)
	if err != nil {
		return fmt.Errorf("stash pop %s: %s: %w", ref, strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// Drop discards a stash entry. There's no undo — git keeps it in the
// reflog briefly but `gk stash` exposes no recovery, so callers should
// confirm before invoking.
func Drop(ctx context.Context, runner git.Runner, ref string) error {
	_, stderr, err := runner.Run(ctx, "stash", "drop", ref)
	if err != nil {
		return fmt.Errorf("stash drop %s: %s: %w", ref, strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// Show returns the diff that the stash would apply, ready for a
// scrollable preview.
func Show(ctx context.Context, runner git.Runner, ref string) (string, error) {
	stdout, stderr, err := runner.Run(ctx, "stash", "show", "-p", ref)
	if err != nil {
		return "", fmt.Errorf("stash show %s: %s: %w", ref, strings.TrimSpace(string(stderr)), err)
	}
	return string(stdout), nil
}

// parseStashIndex pulls the integer from "stash@{N}". Returns -1 on
// any malformed input — callers fall back to the raw Ref for display.
func parseStashIndex(ref string) int {
	open := strings.Index(ref, "{")
	close := strings.Index(ref, "}")
	if open < 0 || close < 0 || close <= open+1 {
		return -1
	}
	n, err := strconv.Atoi(ref[open+1 : close])
	if err != nil {
		return -1
	}
	return n
}
