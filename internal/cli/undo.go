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
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/reflog"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "undo",
		Short: "Pick a recent reflog entry to restore HEAD to",
		Long: `Reads git reflog, lets you pick a past state, and resets to that point after
recording a backup ref.

Reset modes:
  default  --mixed  HEAD moves, index updated, working tree preserved
                    (your file edits become unstaged changes — no data loss)
  --hard           HEAD moves, index *and* working tree updated to that point
                    (file edits at that point are restored, current edits gone)
  --soft           only HEAD moves — index and working tree untouched
                    (uncommit: the undone commits' changes stay staged; use it
                    before a squash or to rewrite a commit message)

Use --hard when you really want to "go back to that exact moment"; the default
errs on the safe side. In a non-interactive run (--json / GK_AGENT / no TTY),
--soft without --to defaults to HEAD~1 — the "uncommit the last commit" move.`,
		RunE: runUndo,
	}
	cmd.Flags().Bool("list", false, "print reflog entries only (don't prompt or reset)")
	cmd.Flags().Int("limit", 20, "max reflog entries to show")
	cmd.Flags().Bool("yes", false, "skip confirmation prompt")
	cmd.Flags().String("to", "", "undo directly to a ref (e.g. HEAD@{3}) without picker")
	cmd.Flags().Bool("hard", false, "discard working-tree changes and restore the picked state exactly (DANGEROUS)")
	cmd.Flags().Bool("soft", false, "move HEAD only — keep index and working tree untouched (uncommit: changes stay staged)")
	rootCmd.AddCommand(cmd)
}

// undoSoftDefaultTarget is where a bare non-interactive `gk undo --soft`
// resets to: the parent of HEAD, i.e. "uncommit the last commit".
const undoSoftDefaultTarget = "HEAD~1"

// undoResultJSON backs `GK_AGENT=1 gk undo` (and --json). It mirrors the
// human output — undone-to SHA plus the recovery backup ref — with the reset
// mode made explicit so agents can tell soft/mixed/hard apart.
type undoResultJSON struct {
	Schema    int    `json:"schema"`
	Result    string `json:"result"`
	From      string `json:"from"`
	To        string `json:"to"`
	BackupRef string `json:"backup_ref"`
	Mode      string `json:"mode"`
}

// undoListJSON backs `gk undo --list` under --json/GK_AGENT (and, with zero
// entries, the empty-reflog case) so agent mode never emits bare prose on a
// success exit.
type undoListJSON struct {
	Schema  int             `json:"schema"`
	Entries []undoEntryJSON `json:"entries"`
}

type undoEntryJSON struct {
	SHA     string `json:"sha"`
	Action  string `json:"action"`
	Ref     string `json:"ref"`
	Summary string `json:"summary"`
	When    string `json:"when,omitempty"` // RFC3339; empty when unknown
}

func undoListPayload(entries []reflog.Entry) undoListJSON {
	out := undoListJSON{Schema: 1, Entries: make([]undoEntryJSON, 0, len(entries))}
	for _, e := range entries {
		je := undoEntryJSON{SHA: e.NewSHA, Action: string(e.Action), Ref: e.Ref, Summary: e.Summary}
		if !e.When.IsZero() {
			je.When = e.When.UTC().Format(time.RFC3339)
		}
		out.Entries = append(out.Entries, je)
	}
	return out
}

// undoDeps groups injectable dependencies for testability.
type undoDeps struct {
	Runner  git.Runner
	Client  *git.Client
	Picker  ui.Picker
	Now     func() time.Time
	WorkDir string // repo root; passed to gitsafe.Check for filesystem-based state detection
}

func defaultUndoDeps(repo string) *undoDeps {
	r := &git.ExecRunner{Dir: repo}
	return &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		Picker:  ui.NewPicker(),
		Now:     time.Now,
		WorkDir: repo,
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
	hard, _ := cmd.Flags().GetBool("hard")
	soft, _ := cmd.Flags().GetBool("soft")

	if soft && hard {
		return errors.New("--soft and --hard are mutually exclusive")
	}
	// Bare --soft in a non-interactive run defaults to HEAD~1 — the dominant
	// "uncommit the last commit" move. Interactive runs keep the picker so
	// --soft stays a mode, not a separate flow.
	softDefaulted := false
	if soft && to == "" && !promptAllowed() {
		to = undoSoftDefaultTarget
		softDefaulted = true
	}

	if limit <= 0 {
		limit = 20
	}

	entries, err := reflog.Read(ctx, d.Runner, "HEAD", limit)
	if err != nil {
		return fmt.Errorf("read reflog: %w", err)
	}

	if listOnly {
		if JSONOut() {
			return emitAgentResult(cmd.OutOrStdout(), undoListPayload(entries))
		}
		if len(entries) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no reflog entries available")
			return nil
		}
		printUndoList(cmd.OutOrStdout(), entries)
		return nil
	}
	if len(entries) == 0 {
		// In JSON/agent mode bare prose with exit 0 would break the envelope
		// contract — surface the empty reflog as a blocked precondition.
		if JSONOut() {
			return WithBlocked(errors.New("undo: no reflog entries available"),
				"no-reflog-entries",
				"the reflog is empty (disabled or pruned) — there is no recorded state to undo to")
		}
		fmt.Fprintln(cmd.OutOrStdout(), "no reflog entries available")
		return nil
	}

	var target reflog.Entry
	if to != "" {
		// user-specified ref; resolve to sha for consistency
		sha, rerr := gitsafe.ResolveRef(ctx, d.Runner, to)
		if rerr != nil {
			err := fmt.Errorf("resolve %q: %w", to, rerr)
			if softDefaulted {
				// The implicit HEAD~1 target fails only when HEAD is the
				// root commit — say so instead of a bare resolve error.
				return WithHint(err,
					"HEAD has no parent commit to uncommit to — a root-only branch has nothing for --soft to undo")
			}
			return err
		}
		target = reflog.Entry{NewSHA: sha, Ref: to, Summary: "(--to " + to + ")"}
	} else {
		if !promptAllowed() && !yes {
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

	// Safety preflight. Dirty trees aren't a hard stop — let the user
	// resolve the situation in-line: stash + auto-pop (the safe path),
	// continue without stashing (advanced), or cancel.
	rep, err := gitsafe.Check(ctx, d.Runner, gitsafe.WithWorkDir(d.WorkDir))
	if err != nil {
		return err
	}
	if soft {
		// A soft reset never touches the index or working tree — a dirty
		// tree is the point of uncommitting (staged changes must survive),
		// and stashing here would destroy exactly what the user wants kept.
		// Only an in-progress rebase/merge/cherry-pick still blocks.
		rep = rep.AllowDirty()
	}
	stashed := false
	if dirtyErr := rep.Err(); dirtyErr != nil {
		if !promptAllowed() || yes {
			return WithHint(dirtyErr,
				"stash or commit first, then re-run gk undo")
		}
		statusOut, _, _ := d.Runner.Run(ctx, "status", "--short")
		body := strings.TrimRight(string(statusOut), "\n")
		if body == "" {
			body = "(git status --short returned no output, but the safety check flagged the tree as dirty)"
		}
		body += "\n\n" + dirtyErr.Error()

		options := []ui.ScrollSelectOption{
			{Key: "s", Value: "stash", Display: "stash & continue — restore with `git stash pop` after undo", IsDefault: true},
			{Key: "c", Value: "cancel", Display: "cancel undo"},
		}
		choice, perr := ui.ScrollSelectTUI(ctx, "working tree is dirty", body, options)
		if perr != nil {
			if errors.Is(perr, ui.ErrPickerAborted) {
				fmt.Fprintln(cmd.OutOrStdout(), "aborted")
				return nil
			}
			return perr
		}
		switch choice {
		case "stash":
			if _, errOut, sErr := d.Runner.Run(ctx, "stash", "push", "--include-untracked", "-m", "gk-undo-autostash"); sErr != nil {
				return WithHint(
					fmt.Errorf("stash before undo: %s: %w", strings.TrimSpace(string(errOut)), sErr),
					"git failed to write the index. common causes:\n"+
						"  - another git process is running (rebase/merge/commit in progress)\n"+
						"  - a stale lock file: `ls .git/index.lock` and remove it if no git is running\n"+
						"  - filesystem permissions / read-only mount\n"+
						"  - in-progress operation: try `gk abort` first\n"+
						"resolve the underlying issue, then re-run `gk undo`")
			}
			stashed = true
			defer func() {
				if _, errOut, pErr := d.Runner.Run(ctx, "stash", "pop"); pErr != nil {
					reason := strings.TrimSpace(string(errOut))
					// Indent multi-line git output so it stands apart
					// from the recovery instructions.
					indented := "    " + strings.ReplaceAll(reason, "\n", "\n    ")
					fmt.Fprintf(cmd.ErrOrStderr(),
						"\nwarning: stash pop failed — your changes are still safe at stash@{0}.\n"+
							"git said:\n%s\n\n"+
							"recover when you're ready:\n"+
							"  1. inspect the stash:   git stash show -p stash@{0}\n"+
							"  2. resolve the blocker (e.g. delete or rename conflicting untracked files)\n"+
							"  3. re-apply:           git stash pop\n",
						indented)
				}
			}()
		default: // "cancel" or empty
			fmt.Fprintln(cmd.OutOrStdout(), "aborted")
			return nil
		}
	}
	_ = stashed

	mode := "mixed"
	modeNote := "(working tree preserved — current edits become unstaged)"
	switch {
	case hard:
		mode = "hard"
		modeNote = "(working tree DISCARDED — current edits gone, files restored to that state)"
	case soft:
		mode = "soft"
		modeNote = "(index and working tree untouched — the undone commits' changes stay staged)"
	}
	if !yes && promptAllowed() {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"will reset --%s HEAD to %s (%s).\n  %s\ncontinue? [y/N] ",
			mode, shortSHA(target.NewSHA), target.Summary, modeNote)
		var ans string
		_, _ = fmt.Fscanln(cmd.InOrStdin(), &ans)
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans != "y" && ans != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "aborted")
			return nil
		}
	}

	// Backup current HEAD + reset via gitsafe.Restorer (SHARED-04).
	strategy := gitsafe.StrategyMixed
	switch {
	case hard:
		strategy = gitsafe.StrategyHard
	case soft:
		strategy = gitsafe.StrategySoft
	}
	branch, _ := d.Client.CurrentBranch(ctx) // may be detached; OK, SanitizeBranchSegment handles it
	restorer := gitsafe.NewRestorer(d.Runner, d.Now, "undo")
	res, err := restorer.Restore(ctx, branch,
		gitsafe.Target{SHA: target.NewSHA, Label: target.Ref, Summary: target.Summary},
		strategy)
	if err != nil {
		return err
	}

	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), undoResultJSON{
			Schema: 1, Result: "ok",
			From: res.From, To: res.To, BackupRef: res.BackupRef, Mode: mode,
		})
	}
	fmt.Fprintln(cmd.OutOrStdout(), successLinef("undone", "to %s", shortSHA(res.To)))
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", cellFaint("backup saved at"), res.BackupRef)
	fmt.Fprintln(cmd.OutOrStdout(), stylizeHintLine(fmt.Sprintf("hint: git reset --hard %s", res.BackupRef)))
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
