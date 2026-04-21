package gitstate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// StateKind represents which in-progress operation git is currently performing.
type StateKind int

const (
	StateNone        StateKind = iota
	StateRebaseMerge           // interactive rebase / merge backend (.git/rebase-merge)
	StateRebaseApply           // am / patch-based rebase (.git/rebase-apply)
	StateMerge                 // merge conflict (.git/MERGE_HEAD)
	StateCherryPick            // cherry-pick in progress (.git/CHERRY_PICK_HEAD)
	StateRevert                // revert in progress (.git/REVERT_HEAD)
	StateBisect                // bisect in progress (.git/BISECT_LOG)
)

// String returns a human-readable name for the StateKind.
func (k StateKind) String() string {
	switch k {
	case StateNone:
		return "none"
	case StateRebaseMerge:
		return "rebase-merge"
	case StateRebaseApply:
		return "rebase-apply"
	case StateMerge:
		return "merge"
	case StateCherryPick:
		return "cherry-pick"
	case StateRevert:
		return "revert"
	case StateBisect:
		return "bisect"
	default:
		return "unknown"
	}
}

// State carries details about the in-progress operation.
type State struct {
	Kind      StateKind
	GitDir    string // 일반 git dir (.git)
	CommonDir string // common dir (worktree 대응)

	// Rebase-specific fields (only populated for StateRebaseMerge/RebaseApply)
	HeadName string // original branch (e.g. "refs/heads/feat/x")
	Onto     string // target ref
	OrigHead string // HEAD sha before rebase started
	Current  int    // current step (1-based)
	Total    int    // total step count
}

// Detect inspects the current working tree's git directory and returns State.
// If workDir == "", uses current working directory.
// Returns StateNone when no operation is in progress.
func Detect(ctx context.Context, workDir string) (*State, error) {
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getwd: %w", err)
		}
	}

	commonDir, gitDir, err := resolveGitDirs(ctx, workDir)
	if err != nil {
		return nil, err
	}

	state, err := DetectFromGitDir(commonDir)
	if err != nil {
		return nil, err
	}
	state.GitDir = gitDir
	state.CommonDir = commonDir
	return state, nil
}

// DetectFromGitDir performs the same inspection given a known git dir.
// Useful for tests — skips `git rev-parse` call.
func DetectFromGitDir(commonDir string) (*State, error) {
	base := &State{
		CommonDir: commonDir,
		GitDir:    commonDir,
	}

	// Priority 1: rebase-merge
	rebaseMergeDir := filepath.Join(commonDir, "rebase-merge")
	if dirExists(rebaseMergeDir) {
		s := &State{
			Kind:      StateRebaseMerge,
			GitDir:    base.GitDir,
			CommonDir: commonDir,
		}
		s.HeadName = readTrimmed(filepath.Join(rebaseMergeDir, "head-name"))
		s.Onto = readTrimmed(filepath.Join(rebaseMergeDir, "onto"))
		s.OrigHead = readTrimmed(filepath.Join(rebaseMergeDir, "orig-head"))
		s.Current = readInt(filepath.Join(rebaseMergeDir, "msgnum"))
		s.Total = readInt(filepath.Join(rebaseMergeDir, "end"))
		return s, nil
	}

	// Priority 2: rebase-apply
	rebaseApplyDir := filepath.Join(commonDir, "rebase-apply")
	if dirExists(rebaseApplyDir) {
		s := &State{
			Kind:      StateRebaseApply,
			GitDir:    base.GitDir,
			CommonDir: commonDir,
		}
		s.HeadName = readTrimmed(filepath.Join(rebaseApplyDir, "head-name"))
		s.Onto = readTrimmed(filepath.Join(rebaseApplyDir, "onto"))
		s.OrigHead = readTrimmed(filepath.Join(rebaseApplyDir, "orig-head"))
		if s.OrigHead == "" {
			s.OrigHead = readTrimmed(filepath.Join(rebaseApplyDir, "abort-safety"))
		}
		s.Current = readInt(filepath.Join(rebaseApplyDir, "next"))
		s.Total = readInt(filepath.Join(rebaseApplyDir, "last"))
		return s, nil
	}

	// Priority 3: merge
	if fileExists(filepath.Join(commonDir, "MERGE_HEAD")) {
		return &State{Kind: StateMerge, GitDir: base.GitDir, CommonDir: commonDir}, nil
	}

	// Priority 4: cherry-pick
	if fileExists(filepath.Join(commonDir, "CHERRY_PICK_HEAD")) {
		return &State{Kind: StateCherryPick, GitDir: base.GitDir, CommonDir: commonDir}, nil
	}

	// Priority 5: revert
	if fileExists(filepath.Join(commonDir, "REVERT_HEAD")) {
		return &State{Kind: StateRevert, GitDir: base.GitDir, CommonDir: commonDir}, nil
	}

	// Priority 6: bisect
	if fileExists(filepath.Join(commonDir, "BISECT_LOG")) {
		return &State{Kind: StateBisect, GitDir: base.GitDir, CommonDir: commonDir}, nil
	}

	return &State{Kind: StateNone, GitDir: base.GitDir, CommonDir: commonDir}, nil
}

// resolveGitDirs runs `git rev-parse --git-common-dir` (with --git-dir fallback)
// to locate the git and common directories, resolving them to absolute paths.
func resolveGitDirs(ctx context.Context, workDir string) (commonDir, gitDir string, err error) {
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, execErr := cmd.Output()
		if execErr != nil {
			return "", execErr
		}
		return strings.TrimSpace(string(out)), nil
	}

	// Try --git-common-dir first (git 2.5+)
	common, err := run("rev-parse", "--git-common-dir")
	if err != nil {
		// Fallback: use --git-dir as both git dir and common dir
		gd, gdErr := run("rev-parse", "--git-dir")
		if gdErr != nil {
			return "", "", fmt.Errorf("not a git repo: %w", gdErr)
		}
		abs, absErr := toAbs(workDir, gd)
		if absErr != nil {
			return "", "", absErr
		}
		return abs, abs, nil
	}

	// Also get --git-dir for the GitDir field
	gd, gdErr := run("rev-parse", "--git-dir")
	if gdErr != nil {
		// Rare: common-dir succeeded but git-dir failed; use common as gitDir too
		abs, absErr := toAbs(workDir, common)
		if absErr != nil {
			return "", "", absErr
		}
		return abs, abs, nil
	}

	absCommon, err := toAbs(workDir, common)
	if err != nil {
		return "", "", err
	}
	absGitDir, err := toAbs(workDir, gd)
	if err != nil {
		return "", "", err
	}
	return absCommon, absGitDir, nil
}

// toAbs converts a path that may be relative (to base) into an absolute path.
func toAbs(base, p string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	return filepath.Abs(filepath.Join(base, p))
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// readTrimmed reads a file and returns its content trimmed of whitespace.
// Returns "" on any error.
func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readInt reads a file, parses it as a decimal integer, and returns 0 on failure.
func readInt(path string) int {
	s := readTrimmed(path)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
