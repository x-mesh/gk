package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// worktreeBranchPick is the outcome of pickWorktreeBranch.
type worktreeBranchPick struct {
	// Name is the short branch name to check out ("feat/x"). For a
	// remote-only row this is the local branch that will be created.
	Name string
	// TrackRef is the remote-tracking ref ("origin/feat/x") when the row
	// was remote-only, empty when the branch already exists locally.
	// Non-empty means the caller must create the local branch.
	TrackRef string
}

// worktreeBranchRow is one selectable branch in the add picker, flattened
// from the local/remote sources so the list can be sorted as a whole.
type worktreeBranchRow struct {
	Name       string
	TrackRef   string // "" for local rows
	Source     string // upstream / "(local)" / "remote: origin"
	Hash       string
	LastCommit time.Time
	// OccupiedBy names the worktree already holding this branch, empty
	// when free. Occupied rows stay listed (so the user learns where the
	// branch went) but selecting one is refused.
	OccupiedBy string
}

const (
	keyWorktreeBranchPrefix = "wtbranch:"
	// keyWorktreeBranchNone marks the empty-list placeholder row.
	// TablePicker refuses to open with zero items, so an unselectable
	// row stands in and carries the "widen the scope" advice.
	keyWorktreeBranchNone = "wtbranch:__none__"
)

// worktreeBranchColumnPriority ranks the add picker's columns for narrow
// terminals. AGE is second only to BRANCH because recency is what makes
// the list answer "which one was I working on?" — the whole reason this
// picker exists. WORKTREE outranks UPSTREAM here (unlike `gk sw`, see
// switchColumnPriority) because occupancy decides whether a row can be
// chosen at all, while upstream is merely context.
func worktreeBranchColumnPriority() map[string]int {
	return map[string]int{
		"BRANCH":   100,
		"AGE":      80,
		"WORKTREE": 60,
		"UPSTREAM": 40,
		"HASH":     10,
	}
}

// errNoSelectableBranch reports that every branch is either occupied or
// absent, so the picker has nothing to offer.
var errNoSelectableBranch = errors.New("no selectable branch")

// loadWorktreeBranchRows enumerates candidate branches for a new
// worktree, newest commit first. Remote-only rows are returned separately
// so the caller can keep them out of the default view while still making
// them reachable by filter (TablePicker.FilterItems) or the remotes
// toggle.
//
// Occupancy covers *every* worktree including the current one: git
// refuses a second checkout of a branch already in use, and the branch we
// are standing on is no exception.
func loadWorktreeBranchRows(ctx context.Context, runner git.Runner) (local, remote []worktreeBranchRow, err error) {
	locals, err := listLocalBranches(ctx, runner)
	if err != nil {
		return nil, nil, err
	}
	remotes, rerr := listRemoteOnlyBranches(ctx, runner, locals)
	if rerr != nil {
		remotes = nil
	}
	wt := loadSwitchWorktrees(ctx, runner)

	occupied := make(map[string]string, len(wt.byBranch)+1)
	for name, e := range wt.byBranch {
		occupied[name] = filepath.Base(e.Path)
	}
	// loadSwitchWorktrees excludes the invoking worktree from byBranch —
	// it is "here", not "elsewhere". For adding a worktree that
	// distinction is irrelevant: its branch is just as unavailable.
	if b := wt.current.Branch; b != "" {
		occupied[b] = filepath.Base(wt.current.Path)
	}

	for _, b := range locals {
		local = append(local, worktreeBranchRow{
			Name:       b.Name,
			Source:     worktreeBranchSource(b),
			Hash:       b.Hash,
			LastCommit: b.LastCommit,
			OccupiedBy: occupied[b.Name],
		})
	}
	for _, r := range remotes {
		remote = append(remote, worktreeBranchRow{
			Name:       r.Name,
			TrackRef:   r.TrackRef,
			Source:     "remote: " + r.Remote,
			Hash:       r.Hash,
			LastCommit: r.LastCommit,
		})
	}
	sortWorktreeBranchRows(local)
	sortWorktreeBranchRows(remote)
	return local, remote, nil
}

// sortWorktreeBranchRows orders rows newest commit first. This is the one
// place the add picker deliberately diverges from `gk sw`, which lists
// locals in refname order: `sw` answers "go to the branch I have in mind"
// (alphabetical scans well), while this picker answers "what was I working
// on recently" (only recency surfaces that). Ties fall back to name so the
// order stays stable across runs.
func sortWorktreeBranchRows(rows []worktreeBranchRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].LastCommit.Equal(rows[j].LastCommit) {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].LastCommit.After(rows[j].LastCommit)
	})
}

// worktreeBranchSource renders the UPSTREAM cell for a local branch,
// mirroring the vocabulary of the `gk sw` picker so the two lists read
// the same way.
func worktreeBranchSource(b branchInfo) string {
	switch {
	case b.Gone:
		return "(gone)"
	case b.Upstream != "" && b.UpstreamInferred:
		return "~ " + b.Upstream
	case b.Upstream != "":
		return "↑ " + b.Upstream
	case b.ForkBranch != "" && b.ForkPoint != "":
		return fmt.Sprintf("from %s@%s", b.ForkBranch, b.ForkPoint)
	default:
		return "(local)"
	}
}

// buildWorktreeBranchItems renders rows as picker items. Occupied rows
// carry a distinct marker and their holding worktree so the list explains
// itself before the user hits the selection guard.
func buildWorktreeBranchItems(rows []worktreeBranchRow, wtCol bool) []ui.PickerItem {
	items := make([]ui.PickerItem, 0, len(rows))
	for _, r := range rows {
		marker := cellGreen("●")
		if r.TrackRef != "" {
			marker = cellCyan("○")
		}
		if r.OccupiedBy != "" {
			marker = cellFaint("⊗")
		}
		age := shortAge(r.LastCommit)
		cells := []string{marker + " " + r.Name, r.Source}
		if wtCol {
			cells = append(cells, cellYellow(r.OccupiedBy))
		}
		cells = append(cells, r.Hash, age)
		items = append(items, ui.PickerItem{
			Key:     keyWorktreeBranchPrefix + r.Name,
			Cells:   cells,
			Display: fmt.Sprintf("%-38s  %-30s  %-8s  %s", r.Name, r.Source, r.Hash, age),
		})
	}
	return items
}

// worktreeBranchNoneItem builds the unselectable stand-in row shown when
// the current scope yields nothing — every local branch occupied, say.
// It names the way out (widen to remotes, or go back and create a branch)
// instead of leaving the user staring at an empty table.
func worktreeBranchNoneItem(cols, remoteN int, showRemotes bool) ui.PickerItem {
	advice := "no free branch — esc, then pick [x] new branch"
	if !showRemotes && remoteN > 0 {
		advice = fmt.Sprintf("no free local branch — ctrl+r to include %d remote, or esc for [x] new branch", remoteN)
	}
	cells := make([]string, cols)
	cells[0] = cellFaint(advice)
	return ui.PickerItem{Key: keyWorktreeBranchNone, Cells: cells, Display: advice}
}

// buildWorktreeBranchSubtitle states the current scope and — when remotes
// are hidden — asks whether to widen it. The question lives here rather
// than in a dedicated form step so the flow stays one screen, and because
// typing a filter already pulls matching remote rows in (FilterItems):
// the toggle is for browsing them all, not a precondition for finding one.
func buildWorktreeBranchSubtitle(localN, remoteN int, showRemotes bool) string {
	if showRemotes {
		return fmt.Sprintf("check out which branch?  ·  %d local + %d remote  ·  ctrl+r for local only",
			localN, remoteN)
	}
	if remoteN == 0 {
		return fmt.Sprintf("check out which branch?  ·  %d local", localN)
	}
	return fmt.Sprintf("check out which branch?  ·  %d local  ·  ctrl+r to include %d remote",
		localN, remoteN)
}

// worktreeBranchExtras wires the remotes toggle. The ctrl alias keeps the
// action reachable while the filter prompt is focused, where bare letters
// are swallowed as filter text.
func worktreeBranchExtras() []ui.TablePickerExtraKey {
	return []ui.TablePickerExtraKey{
		{Key: "r", FilterKey: "ctrl+r", Help: "r remotes", Exit: true},
	}
}

// pickWorktreeBranch runs the branch picker for `worktree add` on an
// existing branch, returning the chosen branch. Selecting a branch held
// by another worktree is refused in place (git would reject it anyway)
// and the picker re-opens with the reason printed, so the user never
// leaves the flow to learn why.
//
// Returns ui.ErrPickerAborted when the user backs out, which the caller
// treats as "go back one step", not "cancel everything".
func pickWorktreeBranch(ctx context.Context, runner git.Runner, w io.Writer) (worktreeBranchPick, error) {
	showRemotes := false
	filter := ""
	for {
		local, remote, err := loadWorktreeBranchRows(ctx, runner)
		if err != nil {
			return worktreeBranchPick{}, err
		}
		if len(local) == 0 && len(remote) == 0 {
			return worktreeBranchPick{}, errNoSelectableBranch
		}

		byName := make(map[string]worktreeBranchRow, len(local)+len(remote))
		for _, r := range append(append([]worktreeBranchRow{}, local...), remote...) {
			byName[r.Name] = r
		}
		// The WORKTREE column only earns its width when something is
		// actually occupied, matching how `gk sw` gates the same column.
		wtCol := false
		for _, r := range local {
			if r.OccupiedBy != "" {
				wtCol = true
				break
			}
		}

		visible := local
		var hidden []ui.PickerItem
		if showRemotes {
			visible = append(append([]worktreeBranchRow{}, local...), remote...)
			sortWorktreeBranchRows(visible)
		} else if len(remote) > 0 {
			// Hidden from the default list but matched once the user types:
			// searching by name finds a remote branch without first knowing
			// to widen the scope.
			hidden = buildWorktreeBranchItems(remote, wtCol)
		}

		headers := []string{"BRANCH", "UPSTREAM", "HASH", "AGE"}
		if wtCol {
			headers = []string{"BRANCH", "UPSTREAM", "WORKTREE", "HASH", "AGE"}
		}

		items := buildWorktreeBranchItems(visible, wtCol)
		if len(items) == 0 {
			items = []ui.PickerItem{
				worktreeBranchNoneItem(len(headers), len(remote), showRemotes),
			}
		}
		picker := &ui.TablePicker{
			Headers:        headers,
			Extras:         worktreeBranchExtras(),
			Subtitle:       buildWorktreeBranchSubtitle(len(local), len(remote), showRemotes),
			FilterItems:    hidden,
			InitialFilter:  filter,
			ColumnPriority: worktreeBranchColumnPriority(),
		}
		choice, err := picker.Pick(ctx, "worktree branch", items)
		if err != nil {
			return worktreeBranchPick{}, err
		}
		filter = choice.FilterValue

		if choice.ExtraAction == "r" {
			showRemotes = !showRemotes
			continue
		}
		if choice.Key == keyWorktreeBranchNone || choice.Key == "" {
			continue
		}

		name := strings.TrimPrefix(choice.Key, keyWorktreeBranchPrefix)
		row, ok := byName[name]
		if !ok {
			continue
		}
		if row.OccupiedBy != "" {
			fmt.Fprintf(w, "✗ %s is checked out in worktree %q\n", name, row.OccupiedBy)
			fmt.Fprintln(w, "  hint: a branch can only live in one worktree — pick another, or remove that worktree first")
			continue
		}
		return worktreeBranchPick{Name: name, TrackRef: row.TrackRef}, nil
	}
}

// collectExistingBranchInputs drives the existing-branch half of the add
// flow: pick a branch, then name the worktree. Backing out of the name
// prompt returns to the picker rather than dropping the whole flow, so a
// mis-picked branch costs one keystroke.
func collectExistingBranchInputs(ctx context.Context, runner *git.ExecRunner, cfg *config.Config, w io.Writer) (worktreeAddInputs, error) {
	validate := func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return errors.New("name is required")
		}
		resolved, err := resolveWorktreePath(ctx, runner, cfg, s)
		if err != nil {
			return err
		}
		// Same check the caller runs before `git worktree add`, hoisted
		// here so a colliding name is caught while it is still editable.
		exists, err := nonEmptyDirExists(resolved)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%s already exists and is not empty", resolved)
		}
		return nil
	}

	for {
		pick, err := pickWorktreeBranch(ctx, runner, w)
		if err != nil {
			switch {
			case errors.Is(err, ui.ErrPickerAborted):
				return worktreeAddInputs{}, errAddCancelled
			case errors.Is(err, errNoSelectableBranch):
				fmt.Fprintln(w, "✗ no branch available to check out")
				fmt.Fprintln(w, "  hint: create one instead — gk wt add <name> -b <branch>")
				return worktreeAddInputs{}, errAddCancelled
			default:
				return worktreeAddInputs{}, err
			}
		}

		name, err := runWorktreeNameTUI(ctx, pick.Name, worktreeBranchDetail(ctx, runner, pick),
			suggestWorktreeName(pick.Name), validate)
		if err != nil {
			if errors.Is(err, errAddCancelled) {
				// Back to the picker, not out of the flow.
				continue
			}
			return worktreeAddInputs{}, err
		}
		return worktreeAddInputs{
			Name:         name,
			CreateBranch: false,
			BranchName:   pick.Name,
			TrackRef:     pick.TrackRef,
		}, nil
	}
}

// worktreeBranchDetail summarises the chosen branch for the name prompt's
// context line ("7h · ↑ origin/feat/x"), so the user can confirm they
// picked the right one without going back.
func worktreeBranchDetail(ctx context.Context, runner git.Runner, pick worktreeBranchPick) string {
	local, remote, err := loadWorktreeBranchRows(ctx, runner)
	if err != nil {
		return ""
	}
	for _, rows := range [][]worktreeBranchRow{local, remote} {
		for _, r := range rows {
			if r.Name != pick.Name {
				continue
			}
			age := shortAge(r.LastCommit)
			if r.Source == "" {
				return age
			}
			return age + " · " + r.Source
		}
	}
	return ""
}

// suggestWorktreeName derives a worktree name from a branch name:
// "feat/relay-agent-notify" → "relay-agent-notify". The last segment is
// what distinguishes branches in practice, and a slash in the name would
// otherwise nest a directory level under the managed base.
func suggestWorktreeName(branch string) string {
	base := filepath.Base(strings.TrimSpace(branch))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}
