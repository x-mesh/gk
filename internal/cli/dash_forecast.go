package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// Forecast bounds. A pair costs one merge-tree of about 14 ms, and N PRs make
// N(N-1)/2 pairs, so the PRs of one base and the whole run are capped.
const (
	dashForecastLimit  = 20
	dashForecastBudget = 10 * time.Second
)

// dashForecastEnv fixes the identity and date of the temporary commits, so
// they need no user config and the same inputs give the same OID.
var dashForecastEnv = []string{
	"GIT_AUTHOR_NAME=gk dash", "GIT_AUTHOR_EMAIL=gk-dash@localhost", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
	"GIT_COMMITTER_NAME=gk dash", "GIT_COMMITTER_EMAIL=gk-dash@localhost", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
}

type dashForecastPR struct {
	Number    int
	HeadOID   string
	Base      string
	UpdatedAt time.Time
}

type dashBaseConflict struct {
	Number int
	Files  []string
}

type dashPairConflict struct {
	A, B  int
	Files []string
}

// dashForecastUnknown is a PR (B is 0) or a pair the forecast could not
// compute. It is never reported as clean.
type dashForecastUnknown struct {
	A, B   int
	Reason string
}

type dashForecastGroup struct {
	Base          string
	Total         int
	Considered    int
	BaseConflicts []dashBaseConflict
	Pairs         []dashPairConflict
	Unknown       []dashForecastUnknown
	Uncomputed    int    // pairs left when the time budget ran out
	Note          string // why pairs were skipped for the whole group
}

// dashForecastMemo keeps pair results for the life of the process, so a
// refresh of gk dash does not merge the same commits again.
var dashForecastMemo = struct {
	sync.Mutex
	m map[string]dashPairResult
}{m: map[string]dashPairResult{}}

type dashPairResult struct {
	files []string
	err   error
}

// forecastPRConflicts merges each PR with its base and then each pair of PRs
// of the same base, with git merge-tree. It reads <remote>/<base> as the base
// and writes only objects: no ref, index, or worktree changes.
func forecastPRConflicts(ctx context.Context, r *git.ExecRunner, remote string, prs []dashForecastPR, limit int, budget time.Duration) []dashForecastGroup {
	byBase := map[string][]dashForecastPR{}
	var bases []string
	for _, p := range prs {
		if _, ok := byBase[p.Base]; !ok {
			bases = append(bases, p.Base)
		}
		byBase[p.Base] = append(byBase[p.Base], p)
	}
	sort.Strings(bases)
	deadline := time.Now().Add(budget)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	commitRunner := &git.ExecRunner{Dir: r.Dir, ExtraEnv: append(append([]string(nil), r.ExtraEnv...), dashForecastEnv...)}

	var groups []dashForecastGroup
	for _, base := range bases {
		list := byBase[base]
		sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
		g := dashForecastGroup{Base: base, Total: len(list)}
		if len(list) > limit {
			list = list[:limit]
		}
		g.Considered = len(list)
		groups = append(groups, forecastGroup(ctx, r, commitRunner, remote, g, list))
	}
	return groups
}

func forecastGroup(ctx context.Context, r, commitRunner *git.ExecRunner, remote string, g dashForecastGroup, list []dashForecastPR) dashForecastGroup {
	baseOID, ok := dashRevParse(ctx, r, "refs/remotes/"+remote+"/"+g.Base+"^{commit}")
	if ctx.Err() != nil {
		// A git call cut off by the deadline fails like a missing ref.
		for _, p := range list {
			g.Unknown = append(g.Unknown, dashForecastUnknown{A: p.Number, Reason: "time limit"})
		}
		return g
	}
	if !ok {
		g.Note = fmt.Sprintf("%s/%s not found locally: run gk dash --fetch", remote, g.Base)
		for _, p := range list {
			g.Unknown = append(g.Unknown, dashForecastUnknown{A: p.Number, Reason: "base not found"})
		}
		return g
	}

	type cleanPR struct {
		pr   dashForecastPR
		tree string
	}
	var clean []cleanPR
	for _, p := range list {
		if ctx.Err() != nil {
			g.Unknown = append(g.Unknown, dashForecastUnknown{A: p.Number, Reason: "time limit"})
			continue
		}
		if _, ok := dashRevParse(ctx, r, p.HeadOID+"^{commit}"); !ok || p.HeadOID == "" {
			g.Unknown = append(g.Unknown, dashForecastUnknown{A: p.Number, Reason: "head not found: run gk dash --fetch"})
			continue
		}
		mb, ok := dashMergeBase(ctx, r, baseOID, p.HeadOID)
		if !ok {
			g.Unknown = append(g.Unknown, dashForecastUnknown{A: p.Number, Reason: "no merge base with " + g.Base})
			continue
		}
		scan, err := scanMergeConflictsTree(ctx, r, mb, baseOID, p.HeadOID)
		switch {
		case err != nil:
			g.Unknown = append(g.Unknown, dashForecastUnknown{A: p.Number, Reason: err.Error()})
		case len(scan.conflicts) > 0:
			g.BaseConflicts = append(g.BaseConflicts, dashBaseConflict{Number: p.Number, Files: scan.conflicts})
		default:
			clean = append(clean, cleanPR{pr: p, tree: scan.treeOID})
		}
	}
	for _, c := range clean {
		if c.tree == "" {
			// git before 2.40 writes no result tree, so there is nothing to
			// merge the second PR into. The PR-to-base forecast stays.
			g.Note = "git 2.40 or later is needed to forecast conflicts between PRs"
			return g
		}
	}

	merged := map[int]string{} // PR number → temporary commit of base + PR
	for i := 0; i < len(clean); i++ {
		for j := i + 1; j < len(clean); j++ {
			if ctx.Err() != nil {
				g.Uncomputed++
				continue
			}
			a, b := clean[i], clean[j]
			res := forecastPair(ctx, r, commitRunner, merged, baseOID, a.pr, a.tree, b.pr)
			switch {
			case res.err != nil && ctx.Err() != nil:
				g.Uncomputed++
			case res.err != nil:
				g.Unknown = append(g.Unknown, dashForecastUnknown{A: a.pr.Number, B: b.pr.Number, Reason: res.err.Error()})
			case len(res.files) > 0:
				g.Pairs = append(g.Pairs, dashPairConflict{A: a.pr.Number, B: b.pr.Number, Files: res.files})
			}
		}
	}
	return g
}

// forecastPair merges b into a temporary commit of base + a. The merge base
// is that of base and b, so the changes of a count as one side.
func forecastPair(ctx context.Context, r, commitRunner *git.ExecRunner, merged map[int]string, baseOID string, a dashForecastPR, aTree string, b dashForecastPR) dashPairResult {
	key := baseOID + " " + a.HeadOID + " " + b.HeadOID
	dashForecastMemo.Lock()
	if res, ok := dashForecastMemo.m[key]; ok {
		dashForecastMemo.Unlock()
		return res
	}
	dashForecastMemo.Unlock()

	tmp, ok := merged[a.Number]
	if !ok {
		out, stderr, err := commitRunner.Run(ctx, "commit-tree", aTree, "-p", baseOID, "-p", a.HeadOID, "-m", "gk dash forecast")
		if err != nil {
			return dashPairResult{err: dashGitErr("commit-tree", stderr, err)}
		}
		tmp = strings.TrimSpace(string(out))
		merged[a.Number] = tmp
	}
	mb, ok := dashMergeBase(ctx, r, baseOID, b.HeadOID)
	if !ok {
		return dashPairResult{err: fmt.Errorf("no merge base of #%d", b.Number)}
	}
	scan, err := scanMergeConflictsTree(ctx, r, mb, tmp, b.HeadOID)
	res := dashPairResult{files: scan.conflicts, err: err}
	if err == nil {
		dashForecastMemo.Lock()
		dashForecastMemo.m[key] = res
		dashForecastMemo.Unlock()
	}
	return res
}

func dashRevParse(ctx context.Context, r git.Runner, rev string) (string, bool) {
	out, _, err := r.Run(ctx, "rev-parse", "--verify", "--quiet", rev)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func dashMergeBase(ctx context.Context, r git.Runner, a, b string) (string, bool) {
	out, _, err := r.Run(ctx, "merge-base", a, b)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func dashGitErr(cmd string, stderr []byte, err error) error {
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return fmt.Errorf("%s: %s", cmd, msg)
	}
	return fmt.Errorf("%s: %w", cmd, err)
}
