package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// rebaseTodoEnv carries the pre-built todo path into the hidden editor
// command: gk runs `git rebase -i` with GIT_SEQUENCE_EDITOR pointing back at
// itself, and the child invocation just copies this file over git's todo.
// All judgment (validation, transformation) happened in the parent before
// git ever started.
const rebaseTodoEnv = "GK_REBASE_TODO_FILE"

// rebaseResultJSON is the agent contract for `gk rebase --plan`.
// Fields are append-only.
type rebaseResultJSON struct {
	Schema    int    `json:"schema"`
	Result    string `json:"result"` // completed | conflict | dry-run
	Onto      string `json:"onto"`
	Pre       string `json:"pre,omitempty"`
	Post      string `json:"post,omitempty"`
	BackupRef string `json:"backup_ref,omitempty"`
	Todo      string `json:"todo,omitempty"` // dry-run: the todo that would run
	// Conflict carries the paused-state contract (files + resume/abort).
	Conflict *pullConflictJSON `json:"conflict,omitempty"`
}

// rebaseTemplateJSON is what --plan-template emits: the real range, oldest
// first, every entry pre-filled as pick — the caller edits actions/messages
// and feeds it back via --plan.
type rebaseTemplateJSON struct {
	Schema  int               `json:"schema"`
	Onto    string            `json:"onto"`
	Commits []rebasePlanEntry `json:"commits"`
}

func init() {
	cmd := &cobra.Command{
		Use:   "rebase",
		Short: "Declarative history editing — rebase -i without the editor",
		Long: `Edits the commits between the base and HEAD according to a JSON plan:
pick / squash / fixup / reword / drop, in the order the plan lists them.
This is git rebase -i with the editor session replaced by a machine
contract, so AI agents (and scripts) can clean up history safely.

Workflow:
  1. gk rebase --plan-template            # current range as a JSON draft
  2. edit actions / messages / order      # the judgment step
  3. gk rebase --plan - < plan.json       # validate + execute

Validation refuses silent damage: every commit in the range must be
addressed exactly once (dropping is explicit), merge commits are rejected,
and rewriting already-pushed commits requires --allow-pushed. A snapshot
backup ref is written before anything moves; on conflict the standard
contract applies (gk resolve --ai, gk continue / gk abort).`,
		Args: rebasePlanArgs,
		RunE: runRebasePlan,
	}
	cmd.Flags().String("plan", "", "JSON plan: a file path, or '-' for stdin")
	cmd.Flags().Bool("plan-template", false, "emit the current range as a plan draft (JSON) and exit")
	cmd.Flags().String("onto", "", "base of the rebase range (default: tracking upstream, else remote base branch)")
	cmd.Flags().Bool("allow-pushed", false, "permit rewriting commits that already exist on a remote (requires force-push afterwards)")
	rootCmd.AddCommand(cmd)

	// Hidden editor entry point: invoked by git as GIT_SEQUENCE_EDITOR with
	// the todo path as the argument. Copies the pre-built todo over it.
	editor := &cobra.Command{
		Use:    "rebase-todo-editor <todo-path>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rewriteRebaseTodo(args[0], os.Getenv(rebaseTodoEnv))
		},
	}
	rootCmd.AddCommand(editor)
}

// rewriteRebaseTodo replaces git's generated todo with the validated one.
func rewriteRebaseTodo(todoPath, preparedPath string) error {
	if preparedPath == "" {
		return fmt.Errorf("rebase-todo-editor: %s is not set — this command is internal to `gk rebase --plan`", rebaseTodoEnv)
	}
	content, err := os.ReadFile(preparedPath)
	if err != nil {
		return fmt.Errorf("rebase-todo-editor: read prepared todo: %w", err)
	}
	if err := os.WriteFile(todoPath, content, 0o644); err != nil {
		return fmt.Errorf("rebase-todo-editor: write todo: %w", err)
	}
	return nil
}

func runRebasePlan(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}
	// Refuse to stack a plan on top of a paused operation.
	if st, derr := gitstate.Detect(ctx, repo); derr == nil && st.Kind != gitstate.StateNone {
		return WithRemedy(
			fmt.Errorf("rebase: a %s is already in progress", inProgressOp(st)),
			"finish or abort it first",
			errRemedy{Command: "gk continue", Safety: "safe"},
			errRemedy{Command: "gk abort", Safety: "destructive"},
		)
	}

	planArg, _ := cmd.Flags().GetString("plan")
	template, _ := cmd.Flags().GetBool("plan-template")
	ontoFlag, _ := cmd.Flags().GetString("onto")
	allowPushed, _ := cmd.Flags().GetBool("allow-pushed")

	if template {
		onto, err := resolveRebaseOnto(ctx, runner, cmd, ontoFlag)
		if err != nil {
			return err
		}
		rng, err := loadRebaseRange(ctx, runner, onto)
		if err != nil {
			return err
		}
		pushed, pushedKnown := collectPushedShas(ctx, runner)
		tpl := rebaseTemplateJSON{Schema: 1, Onto: onto, Commits: make([]rebasePlanEntry, 0, len(rng))}
		for _, c := range rng {
			tpl.Commits = append(tpl.Commits, rebasePlanEntry{
				Action: "pick", Commit: c.SHA, Subject: c.Subject,
				Pushed: pushedKnown && pushed[c.SHA],
			})
		}
		return emitAgentResult(cmd.OutOrStdout(), tpl)
	}

	if planArg == "" {
		return WithHint(
			fmt.Errorf("rebase: no plan given"),
			"start from a draft: gk rebase --plan-template, then feed it back with --plan - (stdin) or --plan <file>",
		)
	}
	var planReader io.Reader
	if planArg == "-" {
		planReader = cmd.InOrStdin()
	} else {
		f, oerr := os.Open(planArg)
		if oerr != nil {
			return fmt.Errorf("rebase: open plan: %w", oerr)
		}
		defer f.Close()
		planReader = f
	}
	plan, err := parseRebasePlan(planReader)
	if err != nil {
		return err
	}
	onto, err := resolveRebasePlanOnto(ctx, runner, cmd, ontoFlag, plan.Onto)
	if err != nil {
		return err
	}
	rng, err := loadRebaseRange(ctx, runner, onto)
	if err != nil {
		return err
	}
	pushed, pushedKnown := collectPushedShas(ctx, runner)
	validated, err := validateRebasePlan(plan, rng, pushed, pushedKnown, allowPushed)
	if err != nil {
		return err
	}

	msgDir, err := os.MkdirTemp("", "gk-rebase-*")
	if err != nil {
		return fmt.Errorf("rebase: temp dir: %w", err)
	}
	todo, _, err := buildRebaseTodo(validated, msgDir)
	if err != nil {
		return err
	}

	preHEAD := headRev(ctx, runner)
	if DryRun() {
		fmt.Fprintln(cmd.ErrOrStderr(), landHeader("─── Rebase plan (dry-run) ────────────────────"))
		for _, ln := range strings.Split(strings.TrimRight(todo, "\n"), "\n") {
			fmt.Fprintln(cmd.ErrOrStderr(), "  "+stylizeHintCommand(ln))
		}
		if JSONOut() {
			return emitAgentResult(cmd.OutOrStdout(), rebaseResultJSON{Schema: 1, Result: "dry-run", Onto: onto, Pre: preHEAD, Todo: todo})
		}
		_ = os.RemoveAll(msgDir)
		return nil
	}

	// Safety net before anything moves: a backup ref of the current tip.
	backupRef := ""
	client := git.NewClient(runner)
	if branch, berr := client.CurrentBranch(ctx); berr == nil && branch != "" && preHEAD != "" {
		if ref, cerr := client.CreateBackup(ctx, branch, preHEAD); cerr == nil {
			backupRef = ref
		}
	}

	todoFile := msgDir + "/todo"
	if err := os.WriteFile(todoFile, []byte(todo), 0o600); err != nil {
		return fmt.Errorf("rebase: write todo: %w", err)
	}
	gkPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("rebase: locate gk binary: %w", err)
	}

	rebaseErr := runRebaseWithEditor(ctx, repo, gkPath, todoFile, onto)
	postHEAD := headRev(ctx, runner)

	if rebaseErr != nil {
		// Paused on conflict? Same contract as pull/merge; anything else is
		// a hard failure — git already rolled back or we point at the backup.
		if st, derr := gitstate.Detect(ctx, repo); derr == nil && st.Kind != gitstate.StateNone {
			fmt.Fprintln(cmd.ErrOrStderr(), "conflict — resolve, then `gk continue` (or `gk abort`)")
			if JSONOut() {
				emitPullConflictJSON(cmd, "", false)
			}
			return &ConflictError{Code: 3}
		}
		hint := "history is untouched"
		if backupRef != "" {
			hint = "restore point: " + backupRef
		}
		return WithHint(fmt.Errorf("rebase: %w", rebaseErr), hint)
	}

	_ = os.RemoveAll(msgDir)
	fmt.Fprintf(cmd.ErrOrStderr(), "%s rebase complete: %s → %s",
		cellGreenBold("✓"), shortSHA(preHEAD), shortSHA(postHEAD))
	if backupRef != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "  %s", cellFaint("(backup: "+backupRef+")"))
	}
	fmt.Fprintln(cmd.ErrOrStderr())
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), rebaseResultJSON{
			Schema: 1, Result: "completed", Onto: onto,
			Pre: preHEAD, Post: postHEAD, BackupRef: backupRef,
		})
	}
	return nil
}

// rebasePlanArgs keeps a common range-shaped invocation from degrading into
// Cobra's generic "unknown command" error. A plan always replays onto one
// base, so --onto is the unambiguous spelling.
func rebasePlanArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return WithHint(
		fmt.Errorf("rebase: unexpected range argument %q", args[0]),
		"use `gk rebase --plan-template --onto <base>`; plans always replay <base>..HEAD",
	)
}

// resolveRebaseOnto mirrors pull's upstream resolution so "the range" means
// the same thing everywhere: explicit --onto, else @{u}, else remote base.
func resolveRebaseOnto(ctx context.Context, runner *git.ExecRunner, cmd *cobra.Command, flag string) (string, error) {
	if flag != "" {
		if err := guardRef(flag); err != nil {
			return "", fmt.Errorf("rebase: invalid --onto: %w", err)
		}
		return flag, nil
	}
	if upstream, _, _, ok := tryTrackingUpstream(ctx, runner); ok {
		return upstream, nil
	}
	cfg, _ := config.Load(cmd.Flags())
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	base := cfg.BaseBranch
	if base == "" {
		detected, err := git.NewClient(runner).DefaultBranch(ctx, remote)
		if err != nil {
			return "", WithHint(
				fmt.Errorf("rebase: no upstream and no base branch — cannot infer the range"),
				"pass it explicitly: gk rebase --onto <ref> ...",
			)
		}
		base = detected
	}
	if candidate := remote + "/" + base; git.RefExists(ctx, runner, candidate) {
		return candidate, nil
	}
	return base, nil
}

// resolveRebasePlanOnto gives a round-tripped plan's onto field the same
// authority as --onto when no CLI base was supplied. A contradictory explicit
// flag is rejected instead of silently widening or changing the rewrite range.
func resolveRebasePlanOnto(ctx context.Context, runner *git.ExecRunner, cmd *cobra.Command, flag, planOnto string) (string, error) {
	planOnto = strings.TrimSpace(planOnto)
	if planOnto != "" {
		if err := guardRef(planOnto); err != nil {
			return "", fmt.Errorf("rebase: invalid plan onto: %w", err)
		}
		if flag != "" && flag != planOnto {
			return "", WithHint(
				fmt.Errorf("rebase: --onto %q disagrees with plan onto %q", flag, planOnto),
				"use one base consistently, or remove the plan's onto field to resolve it from the current branch",
			)
		}
		if flag == "" {
			return planOnto, nil
		}
	}
	return resolveRebaseOnto(ctx, runner, cmd, flag)
}

// newGitRebaseCmd builds the `git rebase -i <onto>` invocation with stdio
// inherited — exec steps (reword amends) stream their output directly.
func newGitRebaseCmd(ctx context.Context, repo, onto string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", "rebase", "-i", onto)
	if repo != "" {
		c.Dir = repo
	}
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c
}

// runRebaseWithEditor spawns `git rebase -i <onto>` with gk itself as the
// sequence editor. Stdio is inherited so exec-step output streams naturally.
func runRebaseWithEditor(ctx context.Context, repo, gkPath, todoFile, onto string) error {
	c := newGitRebaseCmd(ctx, repo, onto)
	// GIT_EDITOR=true keeps squash non-interactive: git's default combined
	// message is accepted as-is. A declarative plan must never open an editor.
	c.Env = append(os.Environ(),
		"GIT_SEQUENCE_EDITOR="+shellQuote(gkPath)+" rebase-todo-editor",
		rebaseTodoEnv+"="+todoFile,
		"GIT_EDITOR=true",
	)
	if err := c.Run(); err != nil {
		var ee interface{ ExitCode() int }
		if errors.As(err, &ee) {
			return fmt.Errorf("git rebase exited %d", ee.ExitCode())
		}
		return err
	}
	return nil
}
