package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestParseReleasePR(t *testing.T) {
	cases := []struct {
		name, subject, body string
		number              int
		title               string
	}{
		{"merge commit takes the title from the body", "Merge pull request #87 from x-mesh/fix/squash", "fix(branch): match a squash\n\ndetails", 87, "fix(branch): match a squash"},
		{"merge commit without a body falls back to the head", "Merge pull request #9 from o/fix/c", "\n", 9, "o/fix/c"},
		{"squash commit", "perf(branch): read the base once (#86)", "body (#1)", 86, "perf(branch): read the base once"},
		{"squash of a revert", `Revert "perf: b (#8)" (#91)`, "", 91, `Revert "perf: b (#8)"`},
		{"local revert of a squash", `Revert "perf: b (#8)"`, "", 0, ""},
		{"number in the middle", "docs: see issue #12 for details", "", 0, ""},
		{"a branch merge", "Merge branch 'main' into develop", "", 0, ""},
		{"number only", "(#5)", "", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pr, ok := parseReleasePR(c.subject, c.body)
			if ok != (c.number != 0) || pr.Number != c.number || pr.Title != c.title {
				t.Errorf("parseReleasePR(%q) = %+v, %v; want #%d %q", c.subject, pr, ok, c.number, c.title)
			}
		})
	}
}

// mergePRCommit merges a one-commit branch into the checked-out branch the way
// GitHub's merge button does. The branch commit ends in "(#3)" to stand for an
// issue reference inside a merged branch.
func mergePRCommit(r *testutil.Repo, number int, branch, title string) {
	base := strings.TrimSpace(r.RunGit("rev-parse", "--abbrev-ref", "HEAD"))
	r.CreateBranch(branch)
	r.WriteFile(strings.ReplaceAll(branch, "/", "-")+".txt", title+"\n")
	r.Commit("fix: work in " + branch + " (#3)")
	r.Checkout(base)
	r.RunGit("merge", "--no-ff", "-m", fmt.Sprintf("Merge pull request #%d from o/%s\n\n%s", number, branch, title), branch)
}

func squashPRCommit(r *testutil.Repo, subject string) {
	r.RunGit("commit", "--allow-empty", "-m", subject)
}

func releasePRNumbers(prs []releasePR) string {
	nums := make([]string, len(prs))
	for i, p := range prs {
		nums[i] = strconv.Itoa(p.Number)
	}
	return strings.Join(nums, " ")
}

func TestListReleasePRs(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	r := testutil.NewRepo(t)
	squashPRCommit(r, "feat: released (#1)")
	r.RunGit("tag", "v1.0.0")
	mergePRCommit(r, 7, "fix/a", "fix: a real title")
	squashPRCommit(r, "perf: b (#8)")
	squashPRCommit(r, `Revert "perf: b (#8)"`)
	squashPRCommit(r, "docs: see issue #12 for details")
	squashPRCommit(r, "perf: b again (#8)")

	prs, err := listReleasePRs(context.Background(), &git.ExecRunner{Dir: r.Dir}, "v1.0.0..HEAD")
	if err != nil {
		t.Fatalf("listReleasePRs: %v", err)
	}
	// Newest first. #1 is in the tag, #3 is inside the merged branch, #12 is in
	// the middle of a subject, and #8 shows one time with its newest commit.
	if got := releasePRNumbers(prs); got != "8 7" {
		t.Fatalf("PRs = %s, want 8 7: %+v", got, prs)
	}
	if prs[0].Title != "perf: b again" || prs[1].Title != "fix: a real title" {
		t.Errorf("titles = %q, %q", prs[0].Title, prs[1].Title)
	}
	if prs[1].SHA == "" || prs[1].Date == "" {
		t.Errorf("entry has no commit: %+v", prs[1])
	}

	if _, err := listReleasePRs(context.Background(), &git.ExecRunner{Dir: r.Dir}, "no-such-ref..HEAD"); err == nil {
		t.Errorf("a range with an unknown ref is no error")
	}
}

// The release section counts every merged PR of the range and lists the
// newest releasePRListCap: the commit lists stop at 20, the PR count does not.
func TestIntegration_ContextReleasePRs(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	squashPRCommit(repo, "feat: seed (#1)")
	repo.RunGit("tag", "v1.0.0")
	mergePRCommit(repo, 2, "fix/a", "fix: a merged title")
	const squashed = releasePRListCap + 4
	for i := range squashed {
		squashPRCommit(repo, fmt.Sprintf("feat: change %d (#%d)", i, 100+i))
	}

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	out, err := collectContext(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	collectContextIncludes(context.Background(), runner, &cfg, map[string]bool{"release": true}, &out)
	rel := out.Release
	if rel == nil {
		t.Fatalf("release section missing, notes=%v", out.Notes)
	}
	if rel.PRCount != squashed+1 {
		t.Errorf("pr_count = %d, want %d", rel.PRCount, squashed+1)
	}
	if len(rel.PRs) != releasePRListCap {
		t.Fatalf("prs = %d entries, want %d", len(rel.PRs), releasePRListCap)
	}
	if rel.PRs[0].Number != 100+squashed-1 || rel.PRs[0].SHA == "" {
		t.Errorf("newest PR = %+v, want #%d", rel.PRs[0], 100+squashed-1)
	}

	var buf strings.Builder
	writeContextReleasePRs(&buf, rel)
	if want := fmt.Sprintf("  %d merged PR(s): #%d ", squashed+1, 100+squashed-1); !strings.HasPrefix(buf.String(), want) || !strings.HasSuffix(buf.String(), " …\n") {
		t.Errorf("text = %q, want prefix %q and a trailing ellipsis", buf.String(), want)
	}
}

// With a release branch, the PRs are those of <base>..HEAD: a PR the release
// branch holds is left out even when no tag marks it.
func TestIntegration_ContextReleasePRsVsBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	squashPRCommit(repo, "feat: seed")
	repo.RunGit("tag", "v1.0.0")
	squashPRCommit(repo, "feat: shipped without a tag (#4)")
	repo.RunGit("update-ref", "refs/remotes/origin/main", "HEAD")
	repo.CreateBranch("develop")
	squashPRCommit(repo, "fix: waits for a release (#5)")

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	cfg.BaseBranch = "main"
	out, err := collectContext(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	collectContextIncludes(context.Background(), runner, &cfg, map[string]bool{"release": true}, &out)
	rel := out.Release
	if rel == nil || rel.BaseRef != "origin/main" {
		t.Fatalf("release = %+v, notes=%v", rel, out.Notes)
	}
	if rel.PRCount != 1 || len(rel.PRs) != 1 || rel.PRs[0].Number != 5 || rel.PRs[0].Title != "fix: waits for a release" {
		t.Errorf("prs = %d %+v, want #5 only", rel.PRCount, rel.PRs)
	}
}
