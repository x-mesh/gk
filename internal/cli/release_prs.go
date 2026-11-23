package cli

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// releasePRListCap is how many pull requests a release listing carries. The
// count beside the list is never capped.
const releasePRListCap = 20

// releasePR is one pull request found in the history of a range.
type releasePR struct {
	Number int
	Title  string
	SHA    string
	Date   string // committer date of the merge or squash commit, RFC 3339
}

// The two subjects GitHub writes when it merges a pull request: a merge
// commit, and a squash or rebase commit with the number as a suffix.
var (
	mergePRSubject  = regexp.MustCompile(`^Merge pull request #(\d+) from (\S+)`)
	squashPRSubject = regexp.MustCompile(`^(.*\S)\s*\(#(\d+)\)$`)
)

// parseReleasePR reads the pull request a commit merged from its subject. A
// merge commit keeps the PR title on the first line of its body, and falls
// back to the head branch when the body is empty.
func parseReleasePR(subject, body string) (releasePR, bool) {
	subject = strings.TrimSpace(subject)
	if m := mergePRSubject.FindStringSubmatch(subject); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return releasePR{}, false
		}
		title := m[2]
		for _, line := range strings.Split(body, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				title = line
				break
			}
		}
		return releasePR{Number: n, Title: title}, true
	}
	if m := squashPRSubject.FindStringSubmatch(subject); m != nil {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return releasePR{}, false
		}
		return releasePR{Number: n, Title: m[1]}, true
	}
	return releasePR{}, false
}

// listReleasePRs returns the pull requests merged in the commits that revs
// select, newest first, one entry for each number. It reads the first-parent
// history only: a commit inside a merged branch that ends in "(#12)" names an
// issue or another PR, not a merge into this branch. It reads commit subjects,
// so it needs no network and misses a merge whose subject was rewritten.
func listReleasePRs(ctx context.Context, runner git.Runner, revs ...string) ([]releasePR, error) {
	args := append([]string{"log", "--first-parent", "--pretty=format:%h%x1f%cI%x1f%s%x1f%b%x1e"}, revs...)
	stdout, stderr, err := runner.Run(ctx, args...)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	var prs []releasePR
	seen := map[int]bool{}
	for _, rec := range strings.Split(string(stdout), "\x1e") {
		parts := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x1f", 4)
		if len(parts) != 4 {
			continue
		}
		pr, ok := parseReleasePR(parts[2], parts[3])
		if !ok || seen[pr.Number] {
			continue
		}
		seen[pr.Number] = true
		pr.SHA, pr.Date = parts[0], parts[1]
		prs = append(prs, pr)
	}
	return prs, nil
}
