package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:   "ignore <path>...",
		Short: "파일을 .gitignore에 추가하고, 추적 중이면 추적 해제",
		Long: `지정한 경로를 .gitignore에 기록한다. 이미 git이 추적 중인 경로는
git rm --cached 로 추적만 해제하고 작업 트리의 파일은 그대로 남긴다.

AI를 쓰지 않는 순수 결정론 동작 — "이 파일을 git에 포함하고 싶지 않다"의 정답 도구.
디렉터리는 끝에 / 를 붙인 패턴으로 기록한다.

예시:
  gk ignore .env
  gk ignore mesh-explorer-web/frontend/.omc/state/idle-notif-cooldown.json
  gk ignore dist/ build/ --commit

이미 커밋된 히스토리에서까지 완전히 지우려면 gk forget <path> 를 쓴다 (filter-repo).`,
		Args: cobra.MinimumNArgs(1),
		RunE: runIgnore,
	}
	cmd.Flags().Bool("commit", false, ".gitignore 변경과 추적 해제를 바로 하나의 커밋으로 기록")
	cmd.Flags().Bool("dry-run", false, "변경 없이 수행할 작업만 출력")
	rootCmd.AddCommand(cmd)
}

// ignoreTarget is one resolved path for `gk ignore`.
type ignoreTarget struct {
	rel     string // repo-relative slash path
	pattern string // the .gitignore line (trailing slash for dirs)
	tracked bool   // currently tracked in the index
}

func runIgnore(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	doCommit, _ := cmd.Flags().GetBool("commit")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	out := cmd.OutOrStdout()

	repoRoot, err := gitToplevel(ctx, runner)
	if err != nil || repoRoot == "" {
		return fmt.Errorf("ignore: git 저장소가 아닙니다")
	}

	targets, err := resolveIgnoreTargets(ctx, runner, repoRoot, args)
	if err != nil {
		return fmt.Errorf("ignore: %w", err)
	}

	// Dry-run: describe the deterministic plan, change nothing.
	if dryRun {
		fmt.Fprintln(out, "ignore (dry-run):")
		for _, t := range targets {
			fmt.Fprintf(out, "  + .gitignore: %s\n", t.pattern)
			if t.tracked {
				fmt.Fprintf(out, "  - git rm --cached %s   (추적 해제, 파일은 유지)\n", t.rel)
			}
		}
		if doCommit {
			fmt.Fprintf(out, "  → commit: %q\n", ignoreCommitMessage(targets))
		}
		return nil
	}

	return applyIgnore(ctx, runner, repoRoot, out, targets, doCommit)
}

// applyIgnore performs the deterministic mutation: append .gitignore rules,
// untrack any tracked targets (keeping the working file), stage the change,
// and optionally commit. Split out from runIgnore so it can be exercised
// directly against a real repo in tests.
func applyIgnore(ctx context.Context, runner git.Runner, repoRoot string, out io.Writer, targets []ignoreTarget, doCommit bool) error {
	// 1. Record patterns in .gitignore.
	pats := make([]string, 0, len(targets))
	for _, t := range targets {
		pats = append(pats, t.pattern)
	}
	added, gerr := appendGitignoreWithHeader(repoRoot, "# Added by gk ignore", pats)
	if gerr != nil {
		return fmt.Errorf("ignore: .gitignore 갱신: %w", gerr)
	}
	if len(added) > 0 {
		fmt.Fprintf(out, ".gitignore에 추가: %s\n", strings.Join(added, ", "))
	} else {
		fmt.Fprintln(out, ".gitignore: 규칙이 이미 모두 존재")
	}

	// 2. Untrack tracked targets (keep the working-tree file).
	var untracked []string
	for _, t := range targets {
		if !t.tracked {
			continue
		}
		if _, _, rmErr := runner.Run(ctx, "rm", "--cached", "-r", "--", t.rel); rmErr != nil {
			return fmt.Errorf("ignore: git rm --cached %s: %w", t.rel, rmErr)
		}
		untracked = append(untracked, t.rel)
	}
	if len(untracked) > 0 {
		fmt.Fprintf(out, "추적 해제(파일 유지): %s\n", strings.Join(untracked, ", "))
	}

	// 3. Stage the .gitignore change so it is ready to commit.
	if len(added) > 0 {
		_, _, _ = runner.Run(ctx, "add", "--", ".gitignore")
	}

	// 4. Optional one-shot commit.
	if doCommit {
		msg := ignoreCommitMessage(targets)
		if _, _, cerr := runner.Run(ctx, "commit", "-m", msg); cerr != nil {
			return fmt.Errorf("ignore: commit: %w", cerr)
		}
		fmt.Fprintf(out, "커밋 완료: %s\n", msg)
		return nil
	}

	if len(added) > 0 || len(untracked) > 0 {
		fmt.Fprintln(out, stylizeHintLine("hint: 커밋하려면 `gk commit`, 또는 다음부터 `gk ignore ... --commit`"))
	}
	return nil
}

// resolveIgnoreTargets normalizes each user path to a repo-relative path,
// derives its .gitignore pattern (trailing slash for directories), and
// records whether it is currently tracked.
func resolveIgnoreTargets(ctx context.Context, runner git.Runner, repoRoot string, args []string) ([]ignoreTarget, error) {
	var targets []ignoreTarget
	for _, raw := range args {
		rel, perr := repoRelPath(repoRoot, raw)
		if perr != nil {
			return nil, perr
		}
		pattern := rel
		if isDirPath(filepath.Join(repoRoot, rel)) {
			pattern = strings.TrimSuffix(rel, "/") + "/"
		}
		targets = append(targets, ignoreTarget{
			rel:     rel,
			pattern: pattern,
			tracked: isTrackedPath(ctx, runner, rel),
		})
	}
	return targets, nil
}

// repoRelPath converts a user-supplied path (relative to the current
// directory, or absolute) into a clean repo-relative slash path. Paths that
// escape the repository are rejected.
//
// Both the repo root and the working directory are canonicalized via
// EvalSymlinks before the relative math so a symlinked prefix (macOS /var →
// /private/var, where gitToplevel reports the canonical form but os.Getwd may
// not) doesn't make every path look "outside" the repo. The target file
// itself is never resolved — it may be already deleted ("I rm'd it, now hide
// it"), and only its location matters.
func repoRelPath(repoRoot, p string) (string, error) {
	root := canonicalDir(repoRoot)

	var abs string
	if filepath.IsAbs(p) {
		abs = canonicalLeaf(filepath.Clean(p))
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		abs = filepath.Join(canonicalDir(cwd), p)
	}

	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("경로 해석 실패 %q: %w", p, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("경로 %q 가 저장소 밖에 있습니다", p)
	}
	return rel, nil
}

// canonicalDir resolves symlinks in an existing directory path, returning it
// unchanged if resolution fails.
func canonicalDir(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return dir
}

// canonicalLeaf canonicalizes a path's existing directory prefix while
// preserving the (possibly non-existent) final element.
func canonicalLeaf(abs string) string {
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return filepath.Join(canonicalDir(filepath.Dir(abs)), filepath.Base(abs))
}

// isDirPath reports whether p exists and is a directory. A deleted path
// (the common "I already rm'd it, now hide it" case) reports false, so it is
// treated as a file pattern.
func isDirPath(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// isTrackedPath reports whether rel is in the git index.
func isTrackedPath(ctx context.Context, runner git.Runner, rel string) bool {
	_, _, err := runner.Run(ctx, "ls-files", "--error-unmatch", "--", rel)
	return err == nil
}

// ignoreCommitMessage builds a conventional-commit subject for --commit.
func ignoreCommitMessage(targets []ignoreTarget) string {
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.rel)
	}
	return "chore: ignore " + strings.Join(names, ", ")
}
