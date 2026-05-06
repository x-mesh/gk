package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/forget"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "forget [path...]",
		Short: "Remove paths from the entire git history (rewrites SHAs)",
		Long: `Remove one or more paths from every commit in the repository, including
historical revisions, by delegating to git filter-repo.

When called with no paths, gk forget auto-detects tracked files that are
already covered by .gitignore — turning the common workflow

  echo db/ >> .gitignore
  gk forget

into a one-line cleanup for files that were committed before the
.gitignore rule existed.

DESTRUCTIVE — every commit SHA changes. After the rewrite finishes,
collaborators must re-clone or hard-reset to the new history. A backup
is written to refs/gk/forget-backup/<unix>/<original-ref> and a text
manifest at .git/gk/forget-backup-<unix>.txt; either one is enough to
roll back.

Requires git filter-repo (https://github.com/newren/git-filter-repo).
Install: brew install git-filter-repo  (or: pip install git-filter-repo)

Examples:
  gk forget                       # auto-detect tracked-but-ignored paths
  gk forget db/ secrets.json      # explicit path list
  gk forget --dry-run db/         # preview without rewriting
  gk forget --yes db/             # skip the confirmation prompt
`,
		RunE: runForget,
	}
	cmd.Flags().BoolP("yes", "y", false, "skip the interactive confirmation prompt")
	rootCmd.AddCommand(cmd)
}

func runForget(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun := DryRun()

	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)

	// Preflight: filter-repo must be installed before we go any further.
	// Done first so a missing binary is the first error a user sees, not
	// a cryptic mid-run failure.
	if err := forget.EnsureFilterRepo(); err != nil {
		return WithHint(err, "run `gk doctor` to see the full preflight result")
	}

	// Same dirty/in-progress preflight as `gk wipe` — history rewrite
	// must not run while a rebase or merge is half-applied, and a dirty
	// tree means uncommitted work would be lost when filter-repo blasts
	// the index.
	rep, err := gitsafe.Check(ctx, runner, gitsafe.WithWorkDir(RepoFlag()))
	if err != nil {
		return err
	}
	if err := rep.Err(); err != nil {
		return err
	}

	paths, err := resolveTargets(ctx, runner, args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return WithHint(
			fmt.Errorf("no paths to forget"),
			"add paths to .gitignore and rerun, or pass paths explicitly: gk forget <path>...",
		)
	}

	commits, err := forget.CountTouchingCommits(ctx, runner, paths)
	if err != nil {
		return err
	}

	branch, _ := client.CurrentBranch(ctx)

	fmt.Fprintln(w, "gk forget — history rewrite preview")
	fmt.Fprintf(w, "  branch:  %s\n", displayBranch(branch))
	fmt.Fprintf(w, "  paths:   %d\n", len(paths))
	for _, p := range paths {
		fmt.Fprintf(w, "    - %s\n", p)
	}
	fmt.Fprintf(w, "  commits affected: %d (every commit SHA in the repo will change)\n", commits)
	fmt.Fprintln(w, "  backup:  refs/gk/forget-backup/<unix>/...  +  .git/gk/forget-backup-<unix>.txt")

	if dryRun {
		fmt.Fprintln(w, "[dry-run] not rewriting history")
		return nil
	}

	if !yes {
		if !ui.IsTerminal() {
			return WithHint(fmt.Errorf("refusing to rewrite history without confirmation"),
				"pass --yes to proceed non-interactively")
		}
		ok, cerr := ui.Confirm(
			fmt.Sprintf("Rewrite %d commits to remove %d path(s)?", commits, len(paths)),
			false,
		)
		if cerr != nil {
			return cerr
		}
		if !ok {
			return fmt.Errorf("aborted")
		}
	}

	gitDir, err := repoGitDir(ctx, runner)
	if err != nil {
		return fmt.Errorf("locate .git dir: %w", err)
	}

	backup, err := forget.CreateBackup(ctx, runner, gitDir, time.Now())
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	fmt.Fprintf(w, "backup written: %s\n", backup.Manifest)

	captured, err := forget.CaptureOrigin(ctx, runner)
	if err != nil {
		return err
	}

	if err := forget.RunFilterRepo(ctx, RepoFlag(), paths); err != nil {
		return fmt.Errorf("filter-repo: %w (backup at %s)", err, backup.Manifest)
	}

	if err := forget.RestoreOrigin(ctx, runner, captured); err != nil {
		// Non-fatal — the rewrite succeeded; surface as a warning so the
		// user can re-add origin themselves rather than rolling back.
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: could not restore origin remote: %v\n", err)
	}

	fmt.Fprintln(w, "history rewritten.")
	if captured != nil && captured.URL != "" && branch != "" {
		fmt.Fprintln(w, "next:")
		fmt.Fprintf(w, "  git push --force-with-lease %s %s\n", captured.Name, branch)
		fmt.Fprintln(w, "  # collaborators must re-clone or run:")
		fmt.Fprintf(w, "  #   git fetch %s && git reset --hard %s/%s\n", captured.Name, captured.Name, branch)
	}
	fmt.Fprintf(w, "rollback: git update-ref --stdin < %s\n", backup.Manifest)
	return nil
}

// resolveTargets returns the path list to feed filter-repo. When the user
// passes explicit paths, those win as-is. Otherwise we auto-detect
// tracked-but-ignored entries — the canonical "I added .gitignore after
// the fact" workflow.
func resolveTargets(ctx context.Context, r git.Runner, args []string) ([]string, error) {
	if len(args) > 0 {
		// Pass-through; filter-repo accepts both files and directories.
		// We don't validate existence on disk because the whole point of
		// this command is to remove paths that may not exist anymore.
		out := make([]string, 0, len(args))
		for _, a := range args {
			a = strings.TrimSpace(a)
			if a != "" {
				out = append(out, a)
			}
		}
		return out, nil
	}
	auto, err := forget.AutoDetectIgnored(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("auto-detect tracked-but-ignored paths: %w", err)
	}
	// Filter to paths that actually appear in history; an entry could be
	// staged-but-not-committed and would be a no-op for filter-repo.
	return forget.PathInHistory(ctx, r, auto)
}

func repoGitDir(ctx context.Context, r git.Runner) (string, error) {
	out, _, err := r.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		// Resolve relative to the working tree root so callers get an
		// absolute path regardless of where gk was invoked from.
		root, _, err := r.Run(ctx, "rev-parse", "--show-toplevel")
		if err == nil {
			dir = filepath.Join(strings.TrimSpace(string(root)), dir)
		}
	}
	return dir, nil
}
