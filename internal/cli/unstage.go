package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	unstageCmd := &cobra.Command{
		Use:   "unstage [path...]",
		Short: "스테이징(저장 대기)만 해제 — 파일 내용은 그대로",
		Long: `index에 올린(staged) 파일을 저장 대기 목록에서 내리되, 워킹트리의
파일 내용은 전혀 건드리지 않는다. 경로를 주지 않으면 스테이징된 전체를 내린다.

` + "`git reset [-q] HEAD -- <paths>`" + `에 해당하는 안전한 형태만 수행한다 —
커밋을 움직이는 reset(--soft/--hard)은 이 명령의 범위가 아니다. 그쪽은
` + "`gk undo`" + `(reflog 기반 복원)를 쓴다.`,
		Args: cobra.ArbitraryArgs,
		RunE: runUnstage,
	}
	rootCmd.AddCommand(unstageCmd)
}

type unstageResultJSON struct {
	Schema   int      `json:"schema"`
	Result   string   `json:"result"`
	Unstaged int      `json:"unstaged"`
	Files    []string `json:"files,omitempty"`
}

func runUnstage(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}

	// What is actually staged (optionally narrowed to the given paths) —
	// reported back so the caller sees exactly what moved, and a no-op run
	// says so instead of pretending to work.
	diffArgs := []string{"diff", "--cached", "--name-only"}
	if len(args) > 0 {
		diffArgs = append(diffArgs, "--")
		diffArgs = append(diffArgs, args...)
	}
	out, stderr, err := runner.Run(ctx, diffArgs...)
	if err != nil {
		return fmt.Errorf("unstage: list staged: %s", firstNonEmptyLine(string(stderr), err.Error()))
	}
	files := splitNonEmptyLines(string(out))
	if len(files) == 0 {
		if JSONOut() {
			return emitAgentResult(cmd.OutOrStdout(), unstageResultJSON{Schema: 1, Result: "ok", Unstaged: 0})
		}
		if len(args) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "unstage: nothing staged matching the given path(s)")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "unstage: nothing staged")
		}
		return nil
	}

	// A repo with no commits has no HEAD to reset against — unstaging there
	// is `git rm --cached` (drop from the index, keep the working file).
	// -f: without it git refuses when the staged blob differs from the
	// worktree (staged, then edited again) — --cached still never touches
	// the file. `:/` addresses the whole repo regardless of the cwd, so the
	// no-path form matches the full staged set the listing above reported.
	if _, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		rmArgs := []string{"rm", "--cached", "-q", "-r", "-f", "--"}
		if len(args) > 0 {
			rmArgs = append(rmArgs, args...)
		} else {
			rmArgs = append(rmArgs, ":/")
		}
		if _, stderr, err := runner.Run(ctx, rmArgs...); err != nil {
			return fmt.Errorf("unstage: %s", firstNonEmptyLine(string(stderr), err.Error()))
		}
	} else {
		resetArgs := []string{"reset", "-q", "HEAD"}
		if len(args) > 0 {
			resetArgs = append(resetArgs, "--")
			resetArgs = append(resetArgs, args...)
		}
		if _, stderr, err := runner.Run(ctx, resetArgs...); err != nil {
			return fmt.Errorf("unstage: %s", firstNonEmptyLine(string(stderr), err.Error()))
		}
	}

	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), unstageResultJSON{
			Schema: 1, Result: "ok", Unstaged: len(files), Files: files,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "unstaged %d file(s) — contents untouched:\n", len(files))
	for _, f := range files {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", f)
	}
	return nil
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func firstNonEmptyLine(s, fallback string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return fallback
}
