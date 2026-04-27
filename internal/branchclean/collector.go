package branchclean

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// Collector는 다양한 조건으로 브랜치를 수집한다.
type Collector struct {
	Runner git.Runner
	Client *git.Client
}

// CollectAll은 옵션에 따라 삭제 후보 브랜치를 수집한다.
// protected, current, base branch는 항상 제외된다.
func (c *Collector) CollectAll(ctx context.Context, opts CleanOptions) ([]BranchEntry, error) {
	protected := make(map[string]bool)
	for _, p := range opts.Protected {
		protected[p] = true
	}
	if cur, err := c.Client.CurrentBranch(ctx); err == nil {
		protected[cur] = true
	}

	remote := opts.RemoteName
	if remote == "" {
		remote = "origin"
	}

	base := opts.BaseBranch
	if base == "" {
		b, err := c.Client.DefaultBranch(ctx, remote)
		if err != nil {
			return nil, fmt.Errorf("gk branch clean: could not determine base branch: %w", err)
		}
		base = b
	}
	protected[base] = true

	var all []BranchEntry

	// merged 브랜치 수집 (기본 동작 또는 --all)
	merged, err := c.CollectMerged(ctx, base, protected)
	if err != nil {
		return nil, err
	}
	all = append(all, merged...)

	// gone 브랜치 수집 (--gone 또는 --all)
	if opts.Gone || opts.All {
		gone, err := c.CollectGone(ctx, protected)
		if err != nil {
			return nil, err
		}
		all = append(all, gone...)
	}

	// stale 브랜치 수집
	staleDays := opts.Stale
	if staleDays == 0 && opts.All {
		staleDays = opts.StaleDays
	}
	if staleDays > 0 {
		stale, err := c.CollectStale(ctx, staleDays, protected)
		if err != nil {
			return nil, err
		}
		all = append(all, stale...)
	}

	return DeduplicateEntries(all), nil
}

// CollectMerged는 base branch에 merged된 브랜치를 수집한다.
func (c *Collector) CollectMerged(ctx context.Context, base string, protected map[string]bool) ([]BranchEntry, error) {
	stdout, stderr, err := c.Runner.Run(ctx, "branch", "--merged", base, "--format=%(refname:short)")
	if err != nil {
		return nil, fmt.Errorf("gk branch clean: branch --merged: %s: %w", strings.TrimSpace(string(stderr)), err)
	}

	var entries []BranchEntry
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if protected[name] {
			continue
		}
		entries = append(entries, BranchEntry{
			Name:   name,
			Status: StatusMerged,
		})
	}
	return entries, nil
}

// CollectGone은 upstream이 삭제된 브랜치를 수집한다.
func (c *Collector) CollectGone(ctx context.Context, protected map[string]bool) ([]BranchEntry, error) {
	branches, err := listBranches(ctx, c.Runner)
	if err != nil {
		return nil, err
	}

	var entries []BranchEntry
	for _, b := range branches {
		if !b.gone || protected[b.name] {
			continue
		}
		entries = append(entries, BranchEntry{
			Name:           b.name,
			Upstream:       b.upstream,
			LastCommitDate: b.lastCommit,
			Gone:           true,
			Status:         StatusGone,
		})
	}
	return entries, nil
}

// CollectStale은 마지막 커밋이 N일 이상 경과한 브랜치를 수집한다.
func (c *Collector) CollectStale(ctx context.Context, days int, protected map[string]bool) ([]BranchEntry, error) {
	branches, err := listBranches(ctx, c.Runner)
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().AddDate(0, 0, -days)
	var entries []BranchEntry
	for _, b := range branches {
		if protected[b.name] {
			continue
		}
		if !b.lastCommit.Before(cutoff) {
			continue
		}
		entries = append(entries, BranchEntry{
			Name:           b.name,
			Upstream:       b.upstream,
			LastCommitDate: b.lastCommit,
			Gone:           b.gone,
			Status:         StatusStale,
		})
	}
	return entries, nil
}

// localBranch는 git for-each-ref 결과를 파싱한 내부 구조체이다.
type localBranch struct {
	name       string
	upstream   string
	lastCommit time.Time
	gone       bool
}

// listBranches는 git for-each-ref로 로컬 브랜치 목록을 가져온다.
func listBranches(ctx context.Context, r git.Runner) ([]localBranch, error) {
	stdout, stderr, err := r.Run(ctx,
		"for-each-ref",
		"--format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track)",
		"refs/heads",
	)
	if err != nil {
		return nil, fmt.Errorf("gk branch clean: for-each-ref: %s: %w", strings.TrimSpace(string(stderr)), err)
	}

	var out []localBranch
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) < 3 {
			continue
		}
		n, _ := strconv.ParseInt(parts[2], 10, 64)
		b := localBranch{
			name:       parts[0],
			upstream:   parts[1],
			lastCommit: time.Unix(n, 0),
		}
		if len(parts) >= 4 && strings.Contains(parts[3], "gone") {
			b.gone = true
		}
		out = append(out, b)
	}
	return out, nil
}
