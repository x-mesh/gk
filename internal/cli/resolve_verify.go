package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/resolve"
)

// The verification gate is what makes auto-resolution a reversible attempt
// instead of a gamble: resolved contents are written but NOT staged
// (ResolveOptions.DeferStage), the gate runs, and only a pass stages and
// continues. A failure restores the exact conflicted state with
// `git checkout -m` — possible precisely because the unmerged index stages
// were never cleared.

// resolveVerifyError reports which check failed; the CLI maps it to a paused
// report with the rolled-back state.
type resolveVerifyError struct {
	Check  string // failing command, or "conflict-marker scan"
	Detail string
}

func (e *resolveVerifyError) Error() string {
	return fmt.Sprintf("verify %q failed: %s", e.Check, e.Detail)
}

// applyResolveGate runs the verification gate over one Run result and, on
// pass, stages every deferred path. On failure it restores only the files gk
// WROTE (checkout -m from the intact index stages); user-accepted markerless
// content is never touched, and delete/modify resolutions that were staged
// before the gate are reported as non-restorable rather than reverted.
func applyResolveGate(ctx context.Context, runner git.Runner, repoRoot string, verifyCmds []string, result *resolve.ResolveResult, stderr io.Writer) error {
	if result == nil || len(result.Resolved) == 0 {
		return nil
	}
	if err := runResolveVerifyGate(ctx, repoRoot, verifyCmds, result, stderr); err != nil {
		// Restore what gk wrote AND what it deleted — both keep their index
		// stages until the gate passes, so checkout -m rebuilds the exact
		// conflicted state (deleted worktree files reappear).
		rollbackPendingResolutions(ctx, runner, append(append([]string{}, result.PendingStage...), result.PendingDelete...), stderr)
		return err
	}
	if err := stagePendingResolutions(ctx, runner, append(append([]string{}, result.PendingStage...), result.PendingAccept...)); err != nil {
		return err
	}
	for _, p := range result.PendingDelete {
		// The worktree file is already gone; record the deletion in the index
		// (this also clears the unmerged stages).
		if _, errOut, err := runner.Run(ctx, "rm", "-q", "--cached", "--ignore-unmatch", "--", p); err != nil {
			return fmt.Errorf("gk resolve: git rm --cached %s: %s: %w", p, strings.TrimSpace(string(errOut)), err)
		}
	}
	return nil
}

// runResolveVerifyGate runs the always-on conflict-marker scan over the
// resolved files, then each configured resolve.verify command from the repo
// root. First failure wins. A read error fails the gate — the only benign
// absence is a path deleted by a degenerate resolution, i.e. one gk did not
// write.
func runResolveVerifyGate(ctx context.Context, repoRoot string, verifyCmds []string, result *resolve.ResolveResult, stderr io.Writer) error {
	written := make(map[string]bool, len(result.PendingStage))
	for _, p := range result.PendingStage {
		written[p] = true
	}
	for _, p := range result.Resolved {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(repoRoot, p)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) && !written[p] {
				continue // deleted by a degenerate resolution — nothing to scan
			}
			return &resolveVerifyError{Check: "conflict-marker scan", Detail: p + ": " + err.Error()}
		}
		if hasConflictMarkers(data) {
			return &resolveVerifyError{Check: "conflict-marker scan", Detail: p + " still contains conflict markers"}
		}
	}
	for _, cmdStr := range verifyCmds {
		if strings.TrimSpace(cmdStr) == "" {
			continue
		}
		if stderr != nil {
			fmt.Fprintf(stderr, "resolve: verify: %s\n", cmdStr)
		}
		c := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		c.Dir = repoRoot
		out, err := c.CombinedOutput()
		if err != nil {
			return &resolveVerifyError{Check: cmdStr, Detail: tailLines(string(out), 10)}
		}
	}
	return nil
}

// hasConflictMarkers detects leftover conflict markers: both an ours opener
// and a theirs closer at line starts. Requiring the pair avoids flagging
// files that legitimately contain a lone "=======" (markdown underlines).
func hasConflictMarkers(data []byte) bool {
	s := string(data)
	return containsLinePrefix(s, "<<<<<<< ") && containsLinePrefix(s, ">>>>>>> ")
}

func containsLinePrefix(s, prefix string) bool {
	if strings.HasPrefix(s, prefix) {
		return true
	}
	return strings.Contains(s, "\n"+prefix)
}

// stagePendingResolutions stages the deferred paths after the gate passed.
func stagePendingResolutions(ctx context.Context, runner git.Runner, paths []string) error {
	for _, p := range paths {
		if err := resolve.GitAdd(ctx, runner, p); err != nil {
			return fmt.Errorf("gk resolve: git add %s: %w", p, err)
		}
	}
	return nil
}

// rollbackPendingResolutions restores each written-but-unstaged path to its
// conflicted state from the still-intact index stages. Errors are collected
// as warnings — a partially restored tree is still inspectable, and the
// operation remains paused either way.
func rollbackPendingResolutions(ctx context.Context, runner git.Runner, paths []string, stderr io.Writer) {
	for _, p := range paths {
		if _, errOut, err := runner.Run(ctx, "checkout", "-m", "--", p); err != nil && stderr != nil {
			fmt.Fprintf(stderr, "warning: gk resolve: restore conflict %s: %s\n", p, strings.TrimSpace(string(errOut)))
		}
	}
}

// ensureRerere turns on git rerere for the repo (idempotent) and applies any
// recorded resolutions to the current conflicts. Files rerere fully resolves
// lose their markers and flow through the existing markerless-accept path.
func ensureRerere(ctx context.Context, runner git.Runner, stderr io.Writer) {
	if _, _, err := runner.Run(ctx, "config", "--local", "rerere.enabled", "true"); err != nil {
		return // read-only repo or bare — rerere is a bonus, never a blocker
	}
	if _, errOut, err := runner.Run(ctx, "rerere"); err != nil && stderr != nil {
		fmt.Fprintf(stderr, "warning: gk resolve: rerere: %s\n", strings.TrimSpace(string(errOut)))
	}
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
