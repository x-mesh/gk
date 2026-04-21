package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/reflog"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "undo",
		Short: "Pick a recent reflog entry to restore HEAD to",
		Long: "Reads git reflog, lets you pick a past state, and runs `git reset --mixed`\n" +
			"to that point after recording a backup ref. Working tree is always preserved.",
		RunE: runUndo,
	}
	cmd.Flags().Bool("list", false, "print reflog entries only (don't prompt or reset)")
	cmd.Flags().Int("limit", 20, "max reflog entries to show")
	cmd.Flags().Bool("yes", false, "skip confirmation prompt")
	cmd.Flags().String("to", "", "undo directly to a ref (e.g. HEAD@{3}) without picker")
	rootCmd.AddCommand(cmd)
}

// undoDeps groups injectable dependencies for testability.
type undoDeps struct {
	Runner git.Runner
	Client *git.Client
	Picker ui.Picker
	Now    func() time.Time
}

func defaultUndoDeps(repo string) *undoDeps {
	r := &git.ExecRunner{Dir: repo}
	return &undoDeps{
		Runner: r,
		Client: git.NewClient(r),
		Picker: ui.NewPicker(),
		Now:    time.Now,
	}
}

func runUndo(cmd *cobra.Command, args []string) error {
	return runUndoWith(cmd, defaultUndoDeps(RepoFlag()))
}

func runUndoWith(cmd *cobra.Command, d *undoDeps) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	listOnly, _ := cmd.Flags().GetBool("list")
	limit, _ := cmd.Flags().GetInt("limit")
	yes, _ := cmd.Flags().GetBool("yes")
	to, _ := cmd.Flags().GetString("to")

	if limit <= 0 {
		limit = 20
	}

	entries, err := reflog.Read(ctx, d.Runner, "HEAD", limit)
	if err != nil {
		return fmt.Errorf("read reflog: %w", err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no reflog entries available")
		return nil
	}

	if listOnly {
		printUndoList(cmd.OutOrStdout(), entries)
		return nil
	}

	var target reflog.Entry
	if to != "" {
		// user-specified ref; resolve to sha for consistency
		sha, err := resolveRef(ctx, d.Runner, to)
		if err != nil {
			return fmt.Errorf("resolve %q: %w", to, err)
		}
		target = reflog.Entry{NewSHA: sha, Ref: to, Summary: "(--to " + to + ")"}
	} else {
		if !ui.IsTerminal() && !yes {
			return errors.New("no TTY and --to not set; use --list or --to <ref>")
		}
		items := entriesToPickerItems(entries)
		picked, perr := d.Picker.Pick(ctx, "undo to", items)
		if errors.Is(perr, ui.ErrPickerAborted) {
			fmt.Fprintln(cmd.OutOrStdout(), "aborted")
			return nil
		}
		if perr != nil {
			return perr
		}
		i, err := strconv.Atoi(picked.Key)
		if err != nil || i < 0 || i >= len(entries) {
			return fmt.Errorf("invalid picker key %q", picked.Key)
		}
		target = entries[i]
	}

	// Safety preflight
	if err := preflight(ctx, d); err != nil {
		return err
	}

	if !yes && ui.IsTerminal() {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"will reset HEAD to %s (%s). continue? [y/N] ", shortSHA(target.NewSHA), target.Summary)
		var ans string
		_, _ = fmt.Fscanln(cmd.InOrStdin(), &ans)
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans != "y" && ans != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "aborted")
			return nil
		}
	}

	// Create a backup ref so the user can restore
	branch, _ := d.Client.CurrentBranch(ctx) // may be detached; OK, fallback below
	backupRef := backupRefName(branch, d.Now())
	if err := updateRef(ctx, d.Runner, backupRef, "HEAD"); err != nil {
		return fmt.Errorf("create backup ref: %w", err)
	}

	// Perform the mixed reset — index moved, working tree preserved
	_, stderr, err := d.Runner.Run(ctx, "reset", "--mixed", target.NewSHA)
	if err != nil {
		return fmt.Errorf("git reset: %s: %w", strings.TrimSpace(string(stderr)), err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "undone to %s\n", shortSHA(target.NewSHA))
	fmt.Fprintf(cmd.OutOrStdout(), "backup saved at %s\n", backupRef)
	fmt.Fprintf(cmd.OutOrStdout(), "to revert this undo: git reset --hard %s\n", backupRef)
	return nil
}

// printUndoList writes a formatted reflog list to w.
func printUndoList(w interface{ Write([]byte) (int, error) }, entries []reflog.Entry) {
	for i, e := range entries {
		rel := "—"
		if !e.When.IsZero() {
			rel = humanSince(time.Since(e.When))
		}
		fmt.Fprintf(w, "%2d) %s  %-14s  %s  %s\n", i, shortSHA(e.NewSHA), string(e.Action), rel, e.Summary)
	}
}

// entriesToPickerItems converts reflog entries to picker items.
// Key is the slice index as a string so we can look up the entry after selection.
func entriesToPickerItems(entries []reflog.Entry) []ui.PickerItem {
	out := make([]ui.PickerItem, 0, len(entries))
	for i, e := range entries {
		rel := "—"
		if !e.When.IsZero() {
			rel = humanSince(time.Since(e.When))
		}
		out = append(out, ui.PickerItem{
			Key:     strconv.Itoa(i),
			Display: fmt.Sprintf("%s  %-14s  %s  %s", shortSHA(e.NewSHA), string(e.Action), rel, e.Summary),
			Preview: fmt.Sprintf("sha: %s\naction: %s\nref: %s\nmessage: %s", e.NewSHA, string(e.Action), e.Ref, e.Message),
		})
	}
	return out
}

// preflight checks the working tree and in-progress state before resetting.
// gitstate is checked first because rebase/merge conflicts also show as dirty.
func preflight(ctx context.Context, d *undoDeps) error {
	// Determine workDir from runner if possible
	workDir := ""
	if er, ok := d.Runner.(*git.ExecRunner); ok {
		workDir = er.Dir
	}
	state, err := gitstate.Detect(ctx, workDir)
	if err != nil {
		return err
	}
	if state.Kind != gitstate.StateNone {
		return fmt.Errorf("in-progress %s; run `gk continue` or `gk abort` first", state.Kind)
	}

	dirty, err := d.Client.IsDirty(ctx)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if dirty {
		return errors.New("working tree has uncommitted changes; commit or stash first")
	}
	return nil
}

// resolveRef resolves a ref to its full commit SHA.
func resolveRef(ctx context.Context, r git.Runner, ref string) (string, error) {
	out, stderr, err := r.Run(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// updateRef creates or updates a git ref to point at from.
func updateRef(ctx context.Context, r git.Runner, ref, from string) error {
	_, stderr, err := r.Run(ctx, "update-ref", ref, from)
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// backupRefName builds a backup ref path like refs/gk/undo-backup/<branch>/<unix>.
func backupRefName(branch string, now time.Time) string {
	safe := branch
	if safe == "" {
		safe = "detached"
	}
	safe = strings.ReplaceAll(safe, "/", "-")
	return fmt.Sprintf("refs/gk/undo-backup/%s/%d", safe, now.Unix())
}

// shortSHA returns the first 8 characters of a SHA, or the full string if shorter.
func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// humanSince converts a duration to a short human-readable string.
func humanSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
