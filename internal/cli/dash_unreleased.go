package cli

import (
	"context"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// dashUnreleasedGroup is the pull requests merged into one branch that no
// release holds yet. Since is the tag, or the release branch, the range starts
// after. NotFetched lists the PRs GitHub merged into the branch whose commit
// the local repository does not have: the range cannot hold them until a
// fetch, so the list without them is not the whole answer.
type dashUnreleasedGroup struct {
	Base       string
	Since      string
	PRs        []releasePR
	NotFetched []int
}

// collectDashUnreleased lists, for the release branch and for each base branch
// of the merged PRs, the pull requests merged into <remote>/<base> after its
// latest tag. A branch that is not the release branch also leaves out what the
// release branch holds. The branches come from the last fetch and the pull
// requests from commit subjects (see listReleasePRs), so the membership needs
// no GitHub data and does not depend on the checked-out branch.
//
// A pull request that several branches hold shows under its base on GitHub
// when that branch holds it, and under the first branch otherwise, the release
// branch first. A branch with no tag and no release branch to compare with has
// no starting point and is left out. A failed read is a note.
func collectDashUnreleased(ctx context.Context, runner git.Runner, remote, releaseBase string, merged []ghapi.DashPR) ([]dashUnreleasedGroup, []string) {
	var notes []string
	if releaseBase == "" {
		detected, err := git.NewClient(runner).DefaultBranch(ctx, remote)
		if err != nil {
			notes = append(notes, "unreleased: release branch unknown (set base_branch): "+err.Error())
		} else {
			releaseBase = detected
		}
	}
	releaseRef := "refs/remotes/" + remote + "/" + releaseBase
	hasRelease := releaseBase != "" && refExists(ctx, runner, releaseRef)

	var groups []dashUnreleasedGroup
	for _, base := range unreleasedBases(releaseBase, merged) {
		tip := "refs/remotes/" + remote + "/" + base
		if !refExists(ctx, runner, tip) {
			continue
		}
		revs := []string{tip}
		since := ""
		if out, _, err := runner.Run(ctx, "describe", "--tags", "--abbrev=0", tip); err == nil {
			if tag := strings.TrimSpace(string(out)); tag != "" {
				since = tag
				revs = append(revs, "^refs/tags/"+tag)
			}
		}
		if base != releaseBase && hasRelease {
			revs = append(revs, "^"+releaseRef)
			if since == "" {
				since = remote + "/" + releaseBase
			}
		}
		if since == "" {
			continue
		}
		prs, err := listReleasePRs(ctx, runner, revs...)
		if err != nil {
			notes = append(notes, "unreleased "+base+": "+err.Error())
			continue
		}
		groups = append(groups, dashUnreleasedGroup{Base: base, Since: since, PRs: prs})
	}

	// A branch cut from another holds that branch's pull requests too.
	holds := map[string]map[int]bool{}
	owner := map[int]string{}
	for _, g := range groups {
		holds[g.Base] = map[int]bool{}
		for _, p := range g.PRs {
			holds[g.Base][p.Number] = true
			if _, ok := owner[p.Number]; !ok {
				owner[p.Number] = g.Base
			}
		}
	}
	for _, m := range merged {
		if holds[m.BaseRef][m.Number] {
			owner[m.Number] = m.BaseRef
		}
	}
	missing := missingMergeCommits(ctx, runner, merged)
	for i := range groups {
		g := &groups[i]
		kept := g.PRs[:0:0]
		for _, p := range g.PRs {
			if owner[p.Number] == g.Base {
				kept = append(kept, p)
			}
		}
		g.PRs = kept
		for _, m := range merged {
			if m.BaseRef == g.Base && missing[m.Number] {
				g.NotFetched = append(g.NotFetched, m.Number)
			}
		}
		sort.Sort(sort.Reverse(sort.IntSlice(g.NotFetched)))
	}
	return groups, notes
}

// missingMergeCommits returns the numbers of the merged PRs whose merge commit
// the local repository does not have. When git cannot tell, it returns none:
// a PR is reported as not fetched only when that is known.
func missingMergeCommits(ctx context.Context, runner git.Runner, merged []ghapi.DashPR) map[int]bool {
	args := []string{"rev-list", "--no-walk", "--ignore-missing"}
	for _, m := range merged {
		if isHexOID(m.MergeOID) {
			args = append(args, m.MergeOID)
		}
	}
	if len(args) == 3 {
		return nil
	}
	out, _, err := runner.Run(ctx, args...)
	if err != nil {
		return nil
	}
	present := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		present[line] = true
	}
	missing := map[int]bool{}
	for _, m := range merged {
		if isHexOID(m.MergeOID) && !present[m.MergeOID] {
			missing[m.Number] = true
		}
	}
	return missing
}

// isHexOID reports whether s is a full object name. The value comes from
// GitHub and goes on a git command line, so anything else is not passed.
func isHexOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// unreleasedBases orders the branches to scan: the release branch, then the
// base branches of the merged PRs, the one with the most PRs first.
func unreleasedBases(releaseBase string, merged []ghapi.DashPR) []string {
	count := map[string]int{}
	for _, p := range merged {
		if p.BaseRef != "" && p.BaseRef != releaseBase {
			count[p.BaseRef]++
		}
	}
	others := make([]string, 0, len(count))
	for b := range count {
		others = append(others, b)
	}
	sort.Slice(others, func(i, j int) bool {
		if count[others[i]] != count[others[j]] {
			return count[others[i]] > count[others[j]]
		}
		return others[i] < others[j]
	})
	if releaseBase == "" {
		return others
	}
	return append([]string{releaseBase}, others...)
}
