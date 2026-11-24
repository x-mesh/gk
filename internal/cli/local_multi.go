package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/ui"
)

// localAutoDepth is how deep an implicit scan looks. Deeper than watch's 1:
// a handoff check that silently skips a repo one level down is worse than a
// slower one, and ~/work/<group>/<repo> is a common layout.
const localAutoDepth = 2

// localRepoTimeout bounds one repository's check. A var so a test can shrink it.
var localRepoTimeout = 10 * time.Second

// localCollectFn is the per-repository collector; a test swaps it to inject a
// collector that outlives its deadline.
var localCollectFn = collectLocal

// localRepoTarget is one repository a multi-repo run checks.
type localRepoTarget struct {
	Label string
	Root  string
}

// localRepoSet is what resolveLocalRepos decided: the repositories to check,
// and how they were found, for the notice line and the JSON result.
type localRepoSet struct {
	Targets []localRepoTarget
	Root    string // the scan root when exactly one was scanned
	Depth   int
	Auto    bool // found by scanning the current directory without a flag
}

type localMultiRepo struct {
	Repo   string       `json:"repo"`
	Path   string       `json:"path"`
	Error  string       `json:"error,omitempty"`
	Report *localReport `json:"report,omitempty"`
}

type localMultiCounts struct {
	Repos      int `json:"repos"`
	NeedAction int `json:"need_action"`
	Clean      int `json:"clean"`
	Unknown    int `json:"unknown"`
}

type localMultiResult struct {
	Scope  string           `json:"scope"`
	Root   string           `json:"root,omitempty"`
	Depth  int              `json:"depth"`
	Auto   bool             `json:"auto"`
	Repos  []localMultiRepo `json:"repos"`
	Clean  bool             `json:"clean"`
	Counts localMultiCounts `json:"counts"`
}

// resolveLocalRepos decides whether `gk local` checks one repository or many.
// It returns nil when the run stays single-repo: no --scan/--repos and the
// current directory is inside a repository.
func resolveLocalRepos(ctx context.Context, cmd *cobra.Command) (*localRepoSet, error) {
	reposFlag, _ := cmd.Flags().GetStringSlice("repos")
	scanFlag, _ := cmd.Flags().GetStringSlice("scan")

	var fc config.FleetConfig
	if cfg, err := config.Load(nil); err == nil && cfg != nil {
		fc = cfg.Fleet
	}
	depth := localAutoDepth
	if fc.Depth > 0 {
		depth = fc.Depth
	}
	if cmd.Flags().Changed("depth") {
		if d, _ := cmd.Flags().GetInt("depth"); d > 0 {
			depth = d
		}
	}

	set := &localRepoSet{Depth: depth}
	scan := scanFlag
	if len(reposFlag) == 0 && len(scanFlag) == 0 {
		if currentRepoRoot(ctx) != "" {
			return nil, nil
		}
		cwd := RepoFlag()
		if cwd == "" {
			var err error
			if cwd, err = os.Getwd(); err != nil {
				return nil, fmt.Errorf("gk local: cannot determine the working directory: %w", err)
			}
		}
		// An agent standing in the wrong directory should hear so, not get a
		// report about every repository below it.
		if AgentOut() {
			return nil, WithRemedy(
				fmt.Errorf("gk local: not a git repository: %s", cwd),
				"to check every repository below this directory, pass --scan",
				errRemedy{Command: selfCmd("local --scan ."), Safety: "safe"})
		}
		scan = []string{cwd}
		set.Auto = true
	}

	for _, p := range reposFlag {
		if _, _, ok := repoRootAndCommonDir(ctx, expandHome(p)); !ok {
			return nil, fmt.Errorf("gk local: not a git repository: %s", p)
		}
	}
	ids, err := discoverRepos(ctx, reposFlag, scan, fc.Exclude, depth)
	if err != nil {
		return nil, fmt.Errorf("gk local: %w", err)
	}
	if len(scan) == 1 {
		set.Root = expandHome(scan[0])
	}
	if len(ids) == 0 {
		where := strings.Join(scan, ", ")
		if where == "" {
			where = strings.Join(reposFlag, ", ")
		}
		return nil, fmt.Errorf("gk local: no git repositories found under %s (depth %d); pass --scan <dir> or a larger --depth", where, depth)
	}

	base := ""
	if set.Root != "" {
		base = canonDir(set.Root)
	}
	for _, id := range ids {
		set.Targets = append(set.Targets, localRepoTarget{Label: localRepoLabel(base, id.Root), Root: id.Root})
	}
	return set, nil
}

// localRepoLabel names a repository by its path below the scan root, so two
// repos that share a directory name stay apart; outside the root it falls
// back to the ~-shortened absolute path.
func localRepoLabel(base, root string) string {
	if base != "" {
		if rel, err := filepath.Rel(base, canonDir(root)); err == nil && rel != "." &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return tildePath(root)
}

// canonDir resolves symlinks so a scan root spelled through one (/tmp on
// macOS) compares against repo roots git reports resolved (/private/tmp).
func canonDir(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// collectLocalMulti checks every target with the repo-scope collector,
// bounded by the shared fleet concurrency. A repository whose check fails or
// runs past its deadline becomes an error entry and counts as not clean: a
// silent gap would read as "nothing stranded".
func collectLocalMulti(ctx context.Context, set *localRepoSet, limit int, done *atomic.Int64) (localMultiResult, error) {
	res := localMultiResult{Scope: "multi", Root: set.Root, Depth: set.Depth, Auto: set.Auto}
	res.Repos = make([]localMultiRepo, len(set.Targets))
	sem := newFleetLimiter(fleetConcurrency())
	var wg sync.WaitGroup
	for i, t := range set.Targets {
		wg.Add(1)
		go func(i int, t localRepoTarget) {
			defer wg.Done()
			defer done.Add(1)
			entry := localMultiRepo{Repo: t.Label, Path: t.Root}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				entry.Error = ctx.Err().Error()
				res.Repos[i] = entry
				return
			}
			defer func() { <-sem }()
			// The deadline starts once a slot is held, so waiting in the
			// queue does not eat into this repository's budget.
			rctx, cancel := context.WithTimeout(ctx, localRepoTimeout)
			defer cancel()
			rep, err := localCollectFn(rctx, t.Root, true, limit)
			// The collectors swallow per-branch errors into zero counts, so
			// a deadline that fires midway can still return a "clean" report.
			// Only rctx itself can tell.
			if err == nil && rctx.Err() != nil {
				err = fmt.Errorf("timed out after %s", localRepoTimeout)
			}
			if err != nil {
				entry.Error = err.Error()
			} else {
				entry.Report = &rep
			}
			res.Repos[i] = entry
		}(i, t)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		// An interrupted scan has no verdict; a partial one would read as clean.
		return localMultiResult{}, fmt.Errorf("gk local: scan interrupted: %w", err)
	}

	res.Counts.Repos = len(res.Repos)
	for _, r := range res.Repos {
		switch {
		case r.Report == nil:
			res.Counts.Unknown++
		case r.Report.Clean:
			res.Counts.Clean++
		default:
			res.Counts.NeedAction++
		}
	}
	res.Clean = res.Counts.NeedAction == 0 && res.Counts.Unknown == 0
	return res, nil
}

func runLocalMulti(cmd *cobra.Command, set *localRepoSet) error {
	ctx := cmd.Context()
	limit, _ := cmd.Flags().GetInt("limit")

	var done atomic.Int64
	total := len(set.Targets)
	stop := ui.StartBubbleSpinnerLive(func() string {
		return fmt.Sprintf("scanning repos %d/%d", done.Load(), total)
	})
	res, err := collectLocalMulti(ctx, set, limit, &done)
	stop()
	if err != nil {
		return err
	}

	if JSONOut() {
		if err := emitAgentResult(cmd.OutOrStdout(), res); err != nil {
			return err
		}
		return localExitCode(cmd, res.Clean)
	}
	renderLocalMulti(cmd.OutOrStdout(), res)
	return localExitCode(cmd, res.Clean)
}

// renderLocalMulti prints only the repositories that need action, each as its
// label over the same rows `gk local --all` prints, then one summary line.
func renderLocalMulti(w io.Writer, res localMultiResult) {
	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()
	if res.Auto {
		fmt.Fprintln(w, faint(fmt.Sprintf("outside a repo — scanned %s (depth %d)", tildePath(res.Root), res.Depth)))
	}
	for _, r := range res.Repos {
		if r.Report != nil && r.Report.Clean {
			continue
		}
		if r.Report == nil {
			fmt.Fprintf(w, "%s\n  %s\n", bold(r.Repo), color.RedString("state unknown: %s", r.Error))
			continue
		}
		fmt.Fprintf(w, "%s  %s\n", bold(r.Repo),
			faint(fmt.Sprintf("— %d branch(es) scanned", len(r.Report.Branches))))
		renderLocalAllRows(w, *r.Report)
	}

	c := res.Counts
	if res.Clean {
		fmt.Fprintln(w, faint(fmt.Sprintf("✓ nothing stranded in %d repos", c.Repos)))
		return
	}
	summary := fmt.Sprintf("%d repos scanned · %d need action · %d clean", c.Repos, c.NeedAction, c.Clean)
	if c.Unknown > 0 {
		summary += fmt.Sprintf(" · %d unknown", c.Unknown)
	}
	fmt.Fprintln(w, bold(summary))
	fmt.Fprintln(w, faint(strings.TrimSpace(localPushHint)))
}
