package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:     "switch [branch]",
		Aliases: []string{"sw"},
		Short:   "Switch branches; interactive picker when no name is given",
		Args:    cobra.MaximumNArgs(1),
		RunE:    runSwitch,
	}
	cmd.Flags().BoolP("create", "c", false, "create a new branch with the given name before switching")
	cmd.Flags().BoolP("force", "f", false, "discard local changes (git switch --discard-changes)")
	cmd.Flags().Bool("detach", false, "detach HEAD at the ref instead of switching to a branch")
	cmd.Flags().BoolP("main", "m", false, "switch to the detected main/master branch")
	cmd.Flags().BoolP("develop", "d", false, "switch to the develop/dev branch")
	rootCmd.AddCommand(cmd)
}

func runSwitch(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	create, _ := cmd.Flags().GetBool("create")
	force, _ := cmd.Flags().GetBool("force")
	detach, _ := cmd.Flags().GetBool("detach")
	toMain, _ := cmd.Flags().GetBool("main")
	toDevelop, _ := cmd.Flags().GetBool("develop")

	if toMain && toDevelop {
		return fmt.Errorf("--main and --develop are mutually exclusive")
	}
	if (toMain || toDevelop) && (len(args) > 0 || create) {
		return fmt.Errorf("--main/--develop take no branch name and cannot combine with --create")
	}

	if toMain {
		cfg, _ := config.Load(cmd.Flags())
		name, err := resolveMainBranch(ctx, runner, client, cfg.Remote)
		if err != nil {
			return err
		}
		return doSwitch(ctx, runner, w, name, false, force, detach)
	}
	if toDevelop {
		name, err := resolveDevelopBranch(ctx, runner)
		if err != nil {
			return err
		}
		return doSwitch(ctx, runner, w, name, false, force, detach)
	}

	if len(args) == 1 {
		return doSwitch(ctx, runner, w, args[0], create, force, detach)
	}

	if create {
		return fmt.Errorf("--create requires a branch name")
	}

	pick, err := pickBranchForSwitch(ctx, runner, client)
	if err != nil {
		return err
	}
	return doSwitch(ctx, runner, w, pick, false, force, detach)
}

// resolveMainBranch picks the repo's canonical main branch.
// Order: DefaultBranch() result → local "main" → local "master".
func resolveMainBranch(ctx context.Context, r git.Runner, client *git.Client, remote string) (string, error) {
	if name, err := client.DefaultBranch(ctx, remote); err == nil {
		if localBranchExists(ctx, r, name) {
			return name, nil
		}
	}
	for _, cand := range []string{"main", "master"} {
		if localBranchExists(ctx, r, cand) {
			return cand, nil
		}
	}
	return "", WithHint(errors.New("no main/master branch found"),
		"check with: git branch")
}

// resolveDevelopBranch picks the repo's canonical develop branch.
// Tries "develop" then "dev".
func resolveDevelopBranch(ctx context.Context, r git.Runner) (string, error) {
	for _, cand := range []string{"develop", "dev"} {
		if localBranchExists(ctx, r, cand) {
			return cand, nil
		}
	}
	return "", WithHint(errors.New("no develop/dev branch found"),
		"check with: git branch")
}

// localBranchExists reports whether refs/heads/<name> exists.
func localBranchExists(ctx context.Context, r git.Runner, name string) bool {
	_, _, err := r.Run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

func pickBranchForSwitch(ctx context.Context, runner git.Runner, client *git.Client) (string, error) {
	branches, err := listLocalBranches(ctx, runner)
	if err != nil {
		return "", err
	}
	cur, _ := client.CurrentBranch(ctx)

	// Recent first — most useful when a user has many branches.
	sort.Slice(branches, func(i, j int) bool {
		return branches[i].LastCommit.After(branches[j].LastCommit)
	})

	items := make([]ui.PickerItem, 0, len(branches))
	for _, b := range branches {
		if b.Name == cur {
			continue
		}
		ups := b.Upstream
		if ups == "" {
			ups = "-"
		}
		items = append(items, ui.PickerItem{
			Key:     b.Name,
			Display: fmt.Sprintf("%-40s  %-30s  %s", b.Name, ups, b.LastCommit.Format("2006-01-02")),
		})
	}
	if len(items) == 0 {
		return "", errors.New("no other branches to switch to")
	}

	choice, err := ui.NewPicker().Pick(ctx, "switch", items)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return "", WithHint(errors.New("aborted"), "pass a branch name directly: gk switch <name>")
		}
		return "", err
	}
	return choice.Key, nil
}

func doSwitch(ctx context.Context, r git.Runner, w io.Writer, branch string, create, force, detach bool) error {
	args := []string{"switch"}
	if create {
		args = append(args, "-c")
	}
	if force {
		args = append(args, "--discard-changes")
	}
	if detach {
		args = append(args, "--detach")
	}
	args = append(args, branch)

	_, stderr, err := r.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("git switch failed: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintf(w, "switched to %s\n", branch)
	return nil
}
