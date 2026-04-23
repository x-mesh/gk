package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	wt := &cobra.Command{
		Use:     "worktree",
		Aliases: []string{"wt"},
		Short:   "Worktree management helpers (interactive TUI when run without a subcommand)",
		Long: `Worktree management helpers.

With no subcommand, gk opens an interactive TUI for listing, adding,
removing, and entering worktrees. Picking a worktree spawns a new
$SHELL in its directory — type 'exit' to return to the original shell.

If you prefer the cd-in-place pattern, pass --print-path and wrap in
a shell alias so the chosen path applies to the parent shell:

    # ~/.zshrc or ~/.bashrc
    gwt() { local p="$(gk wt --print-path)"; [ -n "$p" ] && cd "$p"; }

The TUI falls back to printing this help on non-interactive stdin/stdout.`,
		RunE: runWorktreeTUI,
	}
	wt.Flags().Bool("print-path", false, "on 'cd', print the chosen path instead of entering a subshell (for `cd $(gk wt --print-path)` wrappers)")

	list := &cobra.Command{
		Use:   "list",
		Short: "List worktrees (table or --json)",
		RunE:  runWorktreeList,
	}

	add := &cobra.Command{
		Use:   "add <name|path> [branch]",
		Short: "Create a worktree checking out [branch] (or HEAD)",
		Long: `Create a worktree.

Path resolution:
  - An absolute path is used as-is.
  - A relative name/path is resolved under the managed base directory:
      <worktree.base>/<worktree.project>/<name>
    Default base is ~/.gk/worktree; project defaults to the repo's
    toplevel basename. Override both in .gk.yaml.

Branch logic:
  - Without --new, [branch] must already exist (local or remote-tracking).
  - With --new (-b), a new branch named [branch] is created from
    --from (default HEAD).

Examples:
  gk worktree add ai-commit           # ~/.gk/worktree/<project>/ai-commit
  gk worktree add feat-x -b feat/x    # new branch, managed path
  gk worktree add /tmp/exp -b hotfix  # absolute path wins, branch still created
`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runWorktreeAdd,
	}
	add.Flags().BoolP("new", "b", false, "create a new branch named [branch] at --from")
	add.Flags().String("from", "", "base ref for the new branch (default: HEAD)")
	add.Flags().Bool("detach", false, "detach HEAD in the worktree instead of tracking a branch")

	rm := &cobra.Command{
		Use:   "remove <path>",
		Short: "Remove a worktree",
		Args:  cobra.ExactArgs(1),
		RunE:  runWorktreeRemove,
	}
	rm.Flags().BoolP("force", "f", false, "force remove even when the worktree is dirty or locked")

	prune := &cobra.Command{
		Use:   "prune",
		Short: "Prune worktree administrative records",
		RunE:  runWorktreePrune,
	}

	wt.AddCommand(list, add, rm, prune)
	rootCmd.AddCommand(wt)
}

// WorktreeEntry represents a single row in `gk worktree list --json`.
type WorktreeEntry struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Bare     bool   `json:"bare"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
}

func runWorktreeList(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	stdout, stderr, err := runner.Run(cmd.Context(), "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	entries := parseWorktreePorcelain(string(stdout))

	if JSONOut() {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	w := cmd.OutOrStdout()
	for _, e := range entries {
		label := e.Branch
		switch {
		case e.Bare:
			label = "(bare)"
		case e.Detached:
			label = "(detached HEAD)"
		case label == "":
			label = "-"
		}
		marks := ""
		if e.Locked {
			marks += " [locked]"
		}
		if e.Prunable {
			marks += " [prunable]"
		}
		short := e.Head
		if len(short) > 7 {
			short = short[:7]
		}
		fmt.Fprintf(w, "%-40s  %-8s  %s%s\n", e.Path, short, label, marks)
	}
	return nil
}

// parseWorktreePorcelain parses the output of `git worktree list --porcelain`.
// Records are separated by blank lines. Each record contains key/value lines:
//
//	worktree <path>
//	HEAD <sha>
//	branch refs/heads/<name>   (or: "detached" / "bare")
//	locked [reason...]
//	prunable [reason...]
func parseWorktreePorcelain(raw string) []WorktreeEntry {
	var out []WorktreeEntry
	var cur *WorktreeEntry
	flush := func() {
		if cur != nil && cur.Path != "" {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			flush()
			continue
		}
		if cur == nil {
			cur = &WorktreeEntry{}
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			cur.Path = val
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	flush()
	return out
}

func runWorktreeAdd(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	cfg, _ := config.Load(cmd.Flags())

	rawPath := args[0]
	branch := ""
	if len(args) == 2 {
		branch = args[1]
	}
	newBranch, _ := cmd.Flags().GetBool("new")
	from, _ := cmd.Flags().GetString("from")
	detach, _ := cmd.Flags().GetBool("detach")

	if newBranch && detach {
		return fmt.Errorf("--new and --detach are mutually exclusive")
	}
	if from != "" && !newBranch {
		return fmt.Errorf("--from requires --new")
	}

	resolvedPath, err := resolveWorktreePath(ctx, runner, cfg, rawPath)
	if err != nil {
		return err
	}
	Dbg("worktree add: raw=%q resolved=%q managed=%v", rawPath, resolvedPath, resolvedPath != rawPath)
	// Only create intermediate dirs when the path was rewritten through
	// the managed layout. An absolute path is the user's responsibility.
	if resolvedPath != rawPath {
		if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
			return fmt.Errorf("ensure worktree base: %w", err)
		}
	}

	gitArgs := []string{"worktree", "add"}
	if detach {
		gitArgs = append(gitArgs, "--detach")
	}
	if newBranch {
		if branch == "" {
			return fmt.Errorf("--new requires a branch name (e.g. gk worktree add <path> <branch> -b)")
		}
		gitArgs = append(gitArgs, "-b", branch)
	}
	gitArgs = append(gitArgs, resolvedPath)

	if newBranch {
		if from != "" {
			gitArgs = append(gitArgs, from)
		}
	} else if !detach && branch != "" {
		gitArgs = append(gitArgs, branch)
	}

	stdout, stderr, err := runner.Run(ctx, gitArgs...)
	if err != nil {
		return fmt.Errorf("worktree add: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if len(stdout) > 0 {
		_, _ = w.Write(stdout)
	}
	fmt.Fprintf(w, "added worktree at %s\n", resolvedPath)
	return nil
}

// resolveWorktreePath expands a worktree path argument into a concrete
// filesystem path using gk's managed layout:
//
//  1. absolute path → use as-is (user intent is explicit)
//  2. relative path → <base>/<project>/<input>, where:
//       base    = cfg.Worktree.Base (default "~/.gk/worktree"), ~ expanded
//       project = cfg.Worktree.Project (explicit), else basename of the
//                 git toplevel directory for the current repo
//
// When the managed base cannot be resolved (empty config, no home dir,
// no git toplevel) the raw input is returned so git falls back to
// placing the worktree relative to the caller's cwd — matching the
// pre-v0.9 behaviour of `gk worktree add`.
func resolveWorktreePath(ctx context.Context, runner git.Runner, cfg *config.Config, input string) (string, error) {
	if filepath.IsAbs(input) {
		return input, nil
	}
	if cfg == nil || cfg.Worktree.Base == "" {
		return input, nil
	}
	base := expandHome(cfg.Worktree.Base)
	if base == "" {
		return input, nil
	}

	project := cfg.Worktree.Project
	if project == "" {
		slug, derr := deriveWorktreeProjectSlug(ctx, runner)
		if derr != nil || slug == "" {
			// No toplevel? Fall back to cwd behavior rather than
			// surprising the user with a failed mkdir.
			return input, nil
		}
		project = slug
	}
	if strings.ContainsAny(project, "/\\") || strings.Contains(project, "..") {
		return "", fmt.Errorf("invalid worktree.project %q: must not contain path separators or '..'", project)
	}
	return filepath.Join(base, project, input), nil
}

// deriveWorktreeProjectSlug returns basename(toplevel) so two clones at
// /Users/me/work/gk and /Users/me/personal/gk still share the layout
// unless `worktree.project` is set explicitly.
func deriveWorktreeProjectSlug(ctx context.Context, runner git.Runner) (string, error) {
	out, _, err := runner.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("empty toplevel")
	}
	return filepath.Base(top), nil
}

func runWorktreeRemove(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	force, _ := cmd.Flags().GetBool("force")

	gitArgs := []string{"worktree", "remove"}
	if force {
		gitArgs = append(gitArgs, "--force")
	}
	gitArgs = append(gitArgs, args[0])

	if _, stderr, err := runner.Run(cmd.Context(), gitArgs...); err != nil {
		return fmt.Errorf("worktree remove: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed worktree %s\n", args[0])
	return nil
}

func runWorktreePrune(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	stdout, stderr, err := runner.Run(cmd.Context(), "worktree", "prune", "-v")
	if err != nil {
		return fmt.Errorf("worktree prune: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if len(stdout) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "nothing to prune")
		return nil
	}
	_, _ = cmd.OutOrStdout().Write(stdout)
	return nil
}

// runWorktreeTUI is the handler for bare `gk wt` / `gk worktree`. It
// drives a REPL-style loop over the worktree list and dispatches to
// add/remove/cd actions. All interactive rendering is kept on stderr so
// a caller can safely wrap the command in `$(gk wt)` and capture the
// chosen path for a cd alias.
func runWorktreeTUI(cmd *cobra.Command, args []string) error {
	if !ui.IsTerminal() {
		return cmd.Help()
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	cfg, _ := config.Load(cmd.Flags())
	stderr := cmd.ErrOrStderr()

	// Menu action sentinels. Using clearly reserved keys avoids any
	// collision with a real worktree path a user might pick.
	const (
		keyAddNew = "__gk_add_new__"
		keyQuit   = "__gk_quit__"
	)

	for {
		entries, err := listWorktreesForTUI(ctx, runner)
		if err != nil {
			return err
		}

		bold := color.New(color.Bold).SprintFunc()
		faint := color.New(color.Faint).SprintFunc()

		items := make([]ui.PickerItem, 0, len(entries)+2)
		for _, e := range entries {
			label := worktreeTUILabel(e, bold, faint)
			items = append(items, ui.PickerItem{
				Display: label,
				Key:     e.Path,
			})
		}
		items = append(items,
			ui.PickerItem{Display: faint("[+] add new worktree"), Key: keyAddNew},
			ui.PickerItem{Display: faint("[q] quit"), Key: keyQuit},
		)

		picked, err := ui.NewPicker().Pick(ctx, "worktree", items)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return nil
			}
			return err
		}

		switch picked.Key {
		case keyQuit:
			return nil
		case keyAddNew:
			if err := worktreeTUIAdd(ctx, runner, cfg); err != nil {
				fmt.Fprintf(stderr, "%s %v\n", color.RedString("error:"), err)
			}
		default:
			// Chosen worktree — open the action submenu. Look up the
			// full entry so downstream actions (remove + branch drop)
			// see branch/locked/prunable state, not just the path.
			entry := findWorktreeEntry(entries, picked.Key)
			if entry == nil {
				continue
			}
			done, err := worktreeTUIActOnEntry(ctx, runner, cmd, *entry)
			if err != nil {
				fmt.Fprintf(stderr, "%s %v\n", color.RedString("error:"), err)
			}
			if done {
				return nil
			}
		}
	}
}

// findWorktreeEntry locates the entry whose Path matches path, returning
// nil when not found (stale menu selection vs. concurrent change).
func findWorktreeEntry(entries []WorktreeEntry, path string) *WorktreeEntry {
	for i := range entries {
		if entries[i].Path == path {
			return &entries[i]
		}
	}
	return nil
}

// listWorktreesForTUI returns the current worktrees parsed from
// `git worktree list --porcelain`. Shared with the non-interactive
// `gk worktree list` command so both see identical data.
func listWorktreesForTUI(ctx context.Context, runner *git.ExecRunner) ([]WorktreeEntry, error) {
	stdout, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return parseWorktreePorcelain(string(stdout)), nil
}

// worktreeTUILabel formats one row for the picker. The branch (or
// "(detached)"/"(bare)") is bolded so it scans first; path is faint.
// Flags like [locked] / [prunable] append as red-on-default suffixes.
func worktreeTUILabel(e WorktreeEntry, bold, faint func(a ...interface{}) string) string {
	var branch string
	switch {
	case e.Bare:
		branch = "(bare)"
	case e.Detached:
		branch = "(detached)"
	case e.Branch != "":
		branch = e.Branch
	default:
		branch = "-"
	}
	tail := ""
	if e.Locked {
		tail += "  " + color.RedString("[locked]")
	}
	if e.Prunable {
		tail += "  " + color.RedString("[prunable]")
	}
	return fmt.Sprintf("%-20s  %s%s", bold(branch), faint(e.Path), tail)
}

// worktreeTUIActOnEntry is the secondary picker: what to do with the
// worktree the user just selected. Returns (done, err) — done=true
// means the outer loop should exit (e.g. user picked "cd").
func worktreeTUIActOnEntry(ctx context.Context, runner *git.ExecRunner, cmd *cobra.Command, entry WorktreeEntry) (bool, error) {
	const (
		actCD     = "cd"
		actRemove = "remove"
		actCancel = "cancel"
	)
	printPath, _ := cmd.Flags().GetBool("print-path")

	faint := color.New(color.Faint).SprintFunc()
	cdHint := faint("(enter a subshell in this worktree)")
	if printPath {
		cdHint = faint("(print path to stdout for shell alias)")
	}
	items := []ui.PickerItem{
		{Display: fmt.Sprintf("cd  %s", cdHint), Key: actCD},
		{Display: "remove", Key: actRemove},
		{Display: "cancel", Key: actCancel},
	}
	picked, err := ui.NewPicker().Pick(ctx, entry.Path, items)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return false, nil
		}
		return false, err
	}

	switch picked.Key {
	case actCD:
		if printPath {
			// Scripting path: `cd $(gk wt --print-path)`. All menu
			// rendering has been on stderr so stdout stays clean.
			fmt.Fprintln(cmd.OutOrStdout(), entry.Path)
			return true, nil
		}
		return true, enterWorktreeSubshell(cmd, entry.Path)
	case actRemove:
		return false, worktreeTUIRemove(ctx, runner, cmd, entry)
	default: // cancel
		return false, nil
	}
}

// enterWorktreeSubshell launches $SHELL inside the worktree path and
// blocks until the user exits it. stdin/stdout/stderr are inherited so
// the subshell is fully interactive. On exit the caller's shell is
// still at its original cwd — this is the standard "tool shell" pattern
// used by nix-shell, poetry shell, etc.
//
// We expose the original PWD via GK_WT_PARENT_PWD so an advanced user
// can `cd "$GK_WT_PARENT_PWD"` from within the subshell if they want
// to peek back at the outer tree without exiting.
func enterWorktreeSubshell(cmd *cobra.Command, path string) error {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	origPWD, _ := os.Getwd()

	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()
	fmt.Fprintf(cmd.ErrOrStderr(), "%s %s  %s\n",
		bold("▸ entered"), path, faint("(type `exit` to return)"))

	sub := exec.Command(sh)
	sub.Dir = path
	sub.Stdin = os.Stdin
	sub.Stdout = os.Stdout
	sub.Stderr = os.Stderr
	sub.Env = append(os.Environ(),
		"GK_WT_PARENT_PWD="+origPWD,
		"GK_WT="+path,
	)
	// A non-zero exit from the user's shell (Ctrl+D after a failing
	// last command, e.g.) is not our failure. Swallow *ExitError so gk
	// returns 0 when the subshell closes cleanly from the user's POV.
	if err := sub.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil
		}
		return fmt.Errorf("enter subshell: %w", err)
	}
	return nil
}

// worktreeTUIRemove removes a worktree, handling the common failure
// modes git surfaces as a terse one-line error:
//
//   - "is dirty" / "contains modified or untracked files" → offer --force
//   - "is locked" → offer --force (bypasses the lock)
//   - "is a main working tree" → refuse up front; git would anyway
//   - "not a working tree" / stale admin entry → auto-run `worktree prune`
//
// After a successful removal we also offer to delete the branch that
// was checked out, but only when no other worktree still owns it.
// Confirm defaults to yes because the user already picked "remove" in
// the action menu — a second No-by-default prompt feels redundant.
func worktreeTUIRemove(ctx context.Context, runner *git.ExecRunner, cmd *cobra.Command, entry WorktreeEntry) error {
	stderr := cmd.ErrOrStderr()

	if entry.Bare {
		return fmt.Errorf("cannot remove the bare/main worktree: %s", entry.Path)
	}

	ok, err := ui.Confirm(fmt.Sprintf("remove %s?", entry.Path), true)
	if err != nil || !ok {
		return nil
	}

	// First try: plain remove.
	_, rerr, err := runner.Run(ctx, "worktree", "remove", entry.Path)
	if err != nil {
		msg := strings.ToLower(strings.TrimSpace(string(rerr)) + " " + err.Error())
		switch {
		case strings.Contains(msg, "dirty") ||
			strings.Contains(msg, "contains modified") ||
			strings.Contains(msg, "contains untracked") ||
			strings.Contains(msg, "is locked"):
			// Dirty or locked — surface the exact git message, then
			// ask whether to force.
			fmt.Fprintln(stderr, strings.TrimSpace(string(rerr)))
			force, _ := ui.Confirm("force-remove anyway?", false)
			if !force {
				return nil
			}
			if _, rerr2, err := runner.Run(ctx, "worktree", "remove", "--force", entry.Path); err != nil {
				return fmt.Errorf("worktree remove --force: %s: %w", strings.TrimSpace(string(rerr2)), err)
			}
		case strings.Contains(msg, "not a working tree") ||
			strings.Contains(msg, "is not a working tree") ||
			strings.Contains(msg, "already deleted"):
			// The on-disk path is gone or never was — git's admin
			// record is stale. Prune and treat as success.
			fmt.Fprintln(stderr, "worktree entry is stale — pruning admin records")
			if _, perr, err := runner.Run(ctx, "worktree", "prune", "-v"); err != nil {
				return fmt.Errorf("worktree prune: %s: %w", strings.TrimSpace(string(perr)), err)
			}
		default:
			return fmt.Errorf("worktree remove: %s: %w", strings.TrimSpace(string(rerr)), err)
		}
	}
	fmt.Fprintf(stderr, "removed %s\n", entry.Path)

	// Offer to delete the now-orphaned branch. Guardrails: only when
	// we actually know the branch name and it is not checked out by
	// another worktree.
	if entry.Branch == "" || entry.Detached {
		return nil
	}
	if branchInUse(ctx, runner, entry.Branch) {
		return nil
	}
	drop, _ := ui.Confirm(fmt.Sprintf("also delete branch %q?", entry.Branch), false)
	if !drop {
		return nil
	}
	if _, berr, err := runner.Run(ctx, "branch", "-D", entry.Branch); err != nil {
		fmt.Fprintf(stderr, "warn: branch -D %s: %s\n", entry.Branch, strings.TrimSpace(string(berr)))
		return nil
	}
	fmt.Fprintf(stderr, "deleted branch %s\n", entry.Branch)
	return nil
}

// worktreeTUIAdd collects a worktree name + branch choice from the user
// and dispatches to git worktree add via the managed-path resolver.
// huh forms render on stderr by default, so stdout stays reserved for
// the `cd` action in the outer loop.
func worktreeTUIAdd(ctx context.Context, runner *git.ExecRunner, cfg *config.Config) error {
	var name string
	var createBranch bool = true
	var branchName string
	var fromRef string

	nameForm := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("worktree name").
			Description("relative → placed under the managed base; absolute → used as-is").
			Value(&name).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("name is required")
				}
				return nil
			}),
		huh.NewConfirm().
			Title("create a new branch?").
			Description("no → check out an existing branch").
			Value(&createBranch),
	))
	if err := nameForm.Run(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)

	branchPrompt := "branch name"
	branchDesc := "existing branch to check out"
	if createBranch {
		branchDesc = "name of the new branch"
		branchName = filepath.Base(name) // sensible default: "feat/api" → "api"
	}
	branchForm := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(branchPrompt).Description(branchDesc).Value(&branchName),
	))
	if err := branchForm.Run(); err != nil {
		return err
	}
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return fmt.Errorf("branch name is required")
	}

	if createBranch {
		fromForm := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("base ref").
				Description("blank = HEAD; e.g. origin/main").
				Value(&fromRef),
		))
		if err := fromForm.Run(); err != nil {
			return err
		}
		fromRef = strings.TrimSpace(fromRef)
	}

	resolved, err := resolveWorktreePath(ctx, runner, cfg, name)
	if err != nil {
		return err
	}
	if resolved != name {
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return fmt.Errorf("ensure worktree base: %w", err)
		}
	}

	// Pre-flight: refuse up-front when the target path is non-empty or
	// the requested new branch already exists. Without this, a
	// `git worktree add -b <br> <path>` that aborts at the path-create
	// step still creates `<br>` and leaves it orphaned — next retry
	// fails with "branch already exists" and the user sees a half-
	// completed state.
	if exists, err := nonEmptyDirExists(resolved); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("target path already exists and is non-empty: %s", resolved)
	}
	if createBranch && branchExists(ctx, runner, branchName) {
		// Conflict resolution: creating a branch that already exists
		// would fail deep inside `git worktree add -b`. Handle it here
		// so the user has real choices instead of a dead-end error.
		// A branch already in use by another worktree cannot be either
		// re-used (git refuses) or silently deleted (would strand the
		// other worktree), so we bail out cleanly in that case.
		if branchInUse(ctx, runner, branchName) {
			return fmt.Errorf("branch %q is checked out in another worktree — pick a different name or remove that worktree first", branchName)
		}
		resolution, err := promptOrphanBranchResolution(branchName, orphanBranchTip(ctx, runner, branchName))
		if err != nil {
			return err
		}
		switch resolution {
		case orphanReuse:
			// Switch modes: check out the existing branch instead of
			// recreating it. The git command below already handles
			// `worktree add <path> <branch>` when createBranch=false.
			createBranch = false
		case orphanDelete:
			if _, berr, err := runner.Run(ctx, "branch", "-D", branchName); err != nil {
				return fmt.Errorf("branch -D %s: %s: %w", branchName, strings.TrimSpace(string(berr)), err)
			}
		case orphanCancel:
			return nil
		}
	}

	gitArgs := []string{"worktree", "add"}
	if createBranch {
		gitArgs = append(gitArgs, "-b", branchName, resolved)
		if fromRef != "" {
			gitArgs = append(gitArgs, fromRef)
		}
	} else {
		gitArgs = append(gitArgs, resolved, branchName)
	}
	if _, gitErr, err := runner.Run(ctx, gitArgs...); err != nil {
		// Defensive rollback: even with the pre-flight above, a race or
		// an unexpected git failure can still leave a new branch behind.
		// If we asked git to create the branch and it was left dangling
		// (no worktree entry points at it), remove it so the user's
		// next attempt is not blocked by a phantom branch.
		if createBranch && branchExists(ctx, runner, branchName) && !branchInUse(ctx, runner, branchName) {
			_, _, _ = runner.Run(ctx, "branch", "-D", branchName)
		}
		return fmt.Errorf("worktree add: %s: %w", strings.TrimSpace(string(gitErr)), err)
	}
	// Surface the resolved path on stderr so the user sees where it
	// landed without polluting stdout.
	fmt.Fprintln(os.Stderr, "added worktree at", resolved)
	return nil
}

// orphanResolution enumerates the choices presented when the user asks
// for a new branch whose name already exists as an orphan (branch
// present, not checked out in any worktree — typically left behind by a
// previous failed `worktree add`).
type orphanResolution int

const (
	orphanCancel orphanResolution = iota
	orphanReuse
	orphanDelete
)

// promptOrphanBranchResolution asks the user what to do with an orphan
// branch that collides with their requested new name. The tip preview
// (e.g. "a12bc3f4 fix: something · 2h") helps decide whether it is safe
// to delete. On a non-TTY session this returns orphanCancel so callers
// surface a clear error rather than guessing.
func promptOrphanBranchResolution(name, tip string) (orphanResolution, error) {
	if !ui.IsTerminal() {
		return orphanCancel, fmt.Errorf("branch %q already exists (orphan) — re-run interactively to resolve, or delete with `git branch -D %s`", name, name)
	}
	var picked string
	title := fmt.Sprintf("branch %q already exists (orphan — no worktree uses it)", name)
	desc := tip
	if desc == "" {
		desc = "choose how to proceed"
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title).
			Description(desc).
			Options(
				huh.NewOption("check out the existing branch in the new worktree", "reuse"),
				huh.NewOption(fmt.Sprintf("delete %q and create a fresh branch", name), "delete"),
				huh.NewOption("cancel", "cancel"),
			).
			Value(&picked),
	))
	if err := form.Run(); err != nil {
		return orphanCancel, err
	}
	switch picked {
	case "reuse":
		return orphanReuse, nil
	case "delete":
		return orphanDelete, nil
	default:
		return orphanCancel, nil
	}
}

// orphanBranchTip returns a single-line preview of the branch's tip
// commit ("a12bc3f  fix: X  · 2h") for the orphan-branch prompt.
// Silent empty-string on failure — the prompt still works, just without
// the age cue.
func orphanBranchTip(ctx context.Context, runner git.Runner, branch string) string {
	out, _, err := runner.Run(ctx, "log", "-1",
		"--format=%h\x1f%s\x1f%ar",
		"refs/heads/"+branch,
	)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(strings.TrimRight(string(out), "\n"), "\x1f", 3)
	if len(parts) != 3 {
		return ""
	}
	return fmt.Sprintf("tip: %s  %s  · %s", parts[0], parts[1], parts[2])
}

// nonEmptyDirExists reports whether path is a directory with at least
// one entry. Returns false when the path does not exist. Used to avoid
// the confusing half-succeeded worktree add — git creates the branch
// before checking the destination, so colliding paths orphan branches.
func nonEmptyDirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return true, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return len(entries) > 0, nil
}

// branchExists reports whether refs/heads/<name> resolves. Uses
// show-ref --verify --quiet so the signal is a pure exit code without
// stderr noise that would otherwise leak into the TUI output.
func branchExists(ctx context.Context, runner git.Runner, name string) bool {
	_, _, err := runner.Run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// branchInUse reports whether any existing worktree currently has the
// named branch checked out. This guards the rollback step: if a parallel
// invocation already owns the branch, we must not delete it.
func branchInUse(ctx context.Context, runner git.Runner, name string) bool {
	out, _, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	needle := "branch refs/heads/" + name
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	return false
}
