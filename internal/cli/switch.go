package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
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
	// Remote-only picks need `git switch --track <remote>/<branch>` so DWIM
	// creates a local tracking branch. Local picks go straight through.
	if pick.Remote {
		return doSwitchTrack(ctx, runner, w, pick.TrackRef, force, detach)
	}
	return doSwitch(ctx, runner, w, pick.Name, false, force, detach)
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

// switchPick is the result of the interactive picker. Remote=true means
// the chosen branch exists only on a remote and should be created via
// `git switch --track` using TrackRef ("<remote>/<branch>").
type switchPick struct {
	Name     string // local branch name, or short name for remote-only pick
	TrackRef string // "origin/foo" for remote-only picks; empty for local
	Remote   bool
}

// remoteBranchInfo captures the bits of a refs/remotes/* entry needed for
// the picker. Name is the short name (e.g. "feature/foo"); TrackRef is
// the full "<remote>/<branch>" string passed to `git switch --track`.
type remoteBranchInfo struct {
	Name       string
	TrackRef   string
	Remote     string
	LastCommit time.Time
}

// listRemoteOnlyBranches enumerates refs/remotes/* branches that do NOT
// have a corresponding local branch, so the picker only surfaces ones
// the user hasn't already checked out. HEAD aliases (e.g.
// refs/remotes/origin/HEAD → origin/main) are skipped since they'd be
// duplicates of the real ref they point at.
func listRemoteOnlyBranches(ctx context.Context, r git.Runner, local []branchInfo) ([]remoteBranchInfo, error) {
	stdout, stderr, err := r.Run(ctx,
		"for-each-ref",
		"--format=%(refname:short)%00%(committerdate:unix)%00%(symref)",
		"refs/remotes",
	)
	if err != nil {
		return nil, fmt.Errorf("for-each-ref refs/remotes: %s: %w",
			strings.TrimSpace(string(stderr)), err)
	}

	localSet := make(map[string]struct{}, len(local))
	for _, b := range local {
		localSet[b.Name] = struct{}{}
	}

	var out []remoteBranchInfo
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) < 2 {
			continue
		}
		// Skip HEAD aliases — they have a non-empty symref field.
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			continue
		}
		trackRef := parts[0] // "origin/feature/foo"
		slash := strings.IndexByte(trackRef, '/')
		if slash < 0 || slash == len(trackRef)-1 {
			continue
		}
		remoteName := trackRef[:slash]
		shortName := trackRef[slash+1:]
		if shortName == "HEAD" {
			continue
		}
		// Hide remote entries that duplicate a local branch — user
		// should pick the local one (or create a differently-named
		// branch explicitly via `gk sw -c <name>`).
		if _, dup := localSet[shortName]; dup {
			continue
		}
		ts, _ := strconv.ParseInt(parts[1], 10, 64)
		out = append(out, remoteBranchInfo{
			Name:       shortName,
			TrackRef:   trackRef,
			Remote:     remoteName,
			LastCommit: time.Unix(ts, 0),
		})
	}
	return out, nil
}

func pickBranchForSwitch(ctx context.Context, runner git.Runner, client *git.Client) (switchPick, error) {
	branches, err := listLocalBranches(ctx, runner)
	if err != nil {
		return switchPick{}, err
	}
	remotes, err := listRemoteOnlyBranches(ctx, runner, branches)
	if err != nil {
		// Non-fatal: fall through with local-only list so an fetch/remote
		// enumeration failure doesn't block the picker entirely.
		remotes = nil
	}
	cur, _ := client.CurrentBranch(ctx)

	// Recent first — most useful when a user has many branches.
	sort.Slice(branches, func(i, j int) bool {
		return branches[i].LastCommit.After(branches[j].LastCommit)
	})
	sort.Slice(remotes, func(i, j int) bool {
		return remotes[i].LastCommit.After(remotes[j].LastCommit)
	})

	// Key format distinguishes origin without requiring the picker UI
	// to carry auxiliary metadata:
	//   local  → "local:<name>"
	//   remote → "remote:<trackRef>"  (e.g. remote:origin/feature/foo)
	const (
		keyLocalPrefix  = "local:"
		keyRemotePrefix = "remote:"
	)

	faint := color.New(color.Faint).SprintFunc()
	items := make([]ui.PickerItem, 0, len(branches)+len(remotes))

	for _, b := range branches {
		if b.Name == cur {
			continue
		}
		ups := b.Upstream
		trail := "-"
		if ups != "" {
			trail = "→ " + ups
		}
		if b.Gone {
			trail = faint("(gone)")
		}
		items = append(items, ui.PickerItem{
			Key: keyLocalPrefix + b.Name,
			Display: fmt.Sprintf("%s  %-36s  %-32s  %s",
				color.GreenString("●"),
				b.Name, trail, shortAge(b.LastCommit),
			),
		})
	}
	for _, r := range remotes {
		items = append(items, ui.PickerItem{
			Key: keyRemotePrefix + r.TrackRef,
			Display: fmt.Sprintf("%s  %-36s  %-32s  %s",
				color.CyanString("○"),
				r.Name,
				faint("(from "+r.Remote+")"),
				shortAge(r.LastCommit),
			),
		})
	}

	if len(items) == 0 {
		return switchPick{}, errors.New("no other branches to switch to")
	}

	choice, err := ui.NewPicker().Pick(ctx, "switch", items)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return switchPick{}, WithHint(errors.New("aborted"), "pass a branch name directly: gk switch <name>")
		}
		return switchPick{}, err
	}
	switch {
	case strings.HasPrefix(choice.Key, keyRemotePrefix):
		trackRef := strings.TrimPrefix(choice.Key, keyRemotePrefix)
		short := trackRef
		if i := strings.IndexByte(trackRef, '/'); i >= 0 {
			short = trackRef[i+1:]
		}
		return switchPick{Name: short, TrackRef: trackRef, Remote: true}, nil
	case strings.HasPrefix(choice.Key, keyLocalPrefix):
		return switchPick{Name: strings.TrimPrefix(choice.Key, keyLocalPrefix)}, nil
	default:
		// Defensive: accept bare keys for backward-compat with older
		// picker contracts.
		return switchPick{Name: choice.Key}, nil
	}
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

// doSwitchTrack creates a local tracking branch from a remote ref and
// switches to it. Used when the interactive picker returns a remote-only
// entry: `gk sw` with the user selecting `○ experimental (from origin)`
// becomes `git switch --track origin/experimental`.
func doSwitchTrack(ctx context.Context, r git.Runner, w io.Writer, trackRef string, force, detach bool) error {
	args := []string{"switch", "--track"}
	if force {
		args = append(args, "--discard-changes")
	}
	if detach {
		args = append(args, "--detach")
	}
	args = append(args, trackRef)

	_, stderr, err := r.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("git switch --track %s failed: %s: %w",
			trackRef, strings.TrimSpace(string(stderr)), err)
	}
	short := trackRef
	if i := strings.IndexByte(trackRef, '/'); i >= 0 {
		short = trackRef[i+1:]
	}
	fmt.Fprintf(w, "switched to %s (tracking %s)\n", short, trackRef)
	return nil
}
