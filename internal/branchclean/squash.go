package branchclean

import (
	"context"
	"fmt"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// SquashDetector는 git cherry와 merge-tree 콘텐츠 검사로 squash merge를
// 감지한다.
type SquashDetector struct {
	Runner git.Runner
}

// DetectSquashMerged는 각 브랜치를 두 단계로 판별한다:
//
//  1. git cherry — 커밋 단위 patch-id 동등성. 리베이스·체리픽처럼 커밋이
//     하나씩 base에 대응되는 경우를 잡는다.
//  2. merge-tree 콘텐츠 검사 — cherry가 확인하지 못한 브랜치에 대해, base로
//     머지해도 트리가 변하지 않는지를 본다. GitHub "Squash & Merge"처럼 여러
//     커밋이 하나로 합쳐지면 합쳐진 커밋의 patch-id는 원본 어느 커밋과도
//     일치하지 않아 cherry에는 전부 `+`로 보인다 — 가장 흔한 squash merge가
//     정확히 이 단계에서만 잡힌다.
//
// base^{tree}를 해석할 수 없으면 콘텐츠 검사 없이 cherry 판정만 남는다
// (해석 "실패"는 경고, FakeRunner류의 빈 성공 출력은 조용히 비활성).
// 브랜치 개별 실행 실패는 그 브랜치를 건너뛰고 경고를 반환한다.
func (d *SquashDetector) DetectSquashMerged(
	ctx context.Context,
	branches []string,
	base string,
	protected map[string]bool,
) (squashMerged []string, ambiguous []string, warnings []string) {
	baseTree := ""
	if out, _, err := d.Runner.Run(ctx, "rev-parse", base+"^{tree}"); err == nil {
		baseTree = strings.TrimSpace(string(out))
	} else {
		warnings = append(warnings,
			fmt.Sprintf("resolve %s^{tree} failed — merge-tree content check disabled: %v", base, err))
	}

	for _, branch := range branches {
		if protected[branch] {
			continue
		}

		stdout, _, err := d.Runner.Run(ctx, "cherry", base, branch)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("git cherry failed for %s: %v", branch, err))
			continue
		}

		allApplied, mixed, parseErr := ParseCherryOutput(string(stdout))
		if parseErr != nil {
			warnings = append(warnings, fmt.Sprintf("parse cherry output failed for %s: %v", branch, parseErr))
			continue
		}
		if allApplied {
			squashMerged = append(squashMerged, branch)
			continue
		}

		// cherry가 확인 못 한 나머지: 콘텐츠 검사로 승격 시도. net-zero면
		// squash-merged, 아니면 cherry의 mixed 판정(ambiguous)이 남는다.
		if baseTree != "" {
			ok, cerr := effectivelyMergedTree(ctx, d.Runner, baseTree, base, branch)
			if cerr != nil {
				warnings = append(warnings, fmt.Sprintf("merge-tree check failed for %s: %v", branch, cerr))
			} else if ok {
				squashMerged = append(squashMerged, branch)
				continue
			}
		}
		if mixed {
			ambiguous = append(ambiguous, branch)
		}
	}
	return squashMerged, ambiguous, warnings
}

// EffectivelyMerged reports whether merging branch into base would change
// nothing — base already holds every change the branch carries, even when the
// branch is NOT an ancestor (squash merge, rebase-then-merge, cherry-picks).
// The test is content-level: `git merge-tree --write-tree <base> <branch>`
// (a real three-way against their merge-base) compared with base's own tree.
// A conflicted merge is a definitive "not merged", not an error.
func EffectivelyMerged(ctx context.Context, r git.Runner, base, branch string) (bool, error) {
	out, _, err := r.Run(ctx, "rev-parse", base+"^{tree}")
	if err != nil {
		return false, fmt.Errorf("resolve %s^{tree}: %w", base, err)
	}
	return effectivelyMergedTree(ctx, r, strings.TrimSpace(string(out)), base, branch)
}

// EffectivelyMergedSet runs the content check over branches, resolving
// base^{tree} once. A branch whose check errors is simply absent from the
// result — under-claiming keeps it listed as unmerged work, the safe
// direction for every caller. Cost: one rev-parse plus one merge-tree per
// branch, deliberately WITHOUT git cherry — cherry computes per-commit
// patch-ids on both sides of the fork point, which gets expensive against a
// long-lived base, and survey commands run this by default.
func EffectivelyMergedSet(ctx context.Context, r git.Runner, base string, branches []string) map[string]bool {
	out := make(map[string]bool, len(branches))
	if len(branches) == 0 {
		return out
	}
	baseTreeRaw, _, err := r.Run(ctx, "rev-parse", base+"^{tree}")
	if err != nil {
		return out
	}
	baseTree := strings.TrimSpace(string(baseTreeRaw))
	if baseTree == "" {
		return out
	}
	for _, b := range branches {
		if ok, cerr := effectivelyMergedTree(ctx, r, baseTree, base, b); cerr == nil && ok {
			out[b] = true
		}
	}
	return out
}

func effectivelyMergedTree(ctx context.Context, r git.Runner, baseTree, base, branch string) (bool, error) {
	stdout, stderr, err := r.Run(ctx, "merge-tree", "--write-tree", base, branch)
	first := strings.TrimSpace(string(stdout))
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = strings.TrimSpace(first[:i])
	}
	if err != nil {
		// Exit 1 with an OID on the first line is a CONFLICTED merge — a
		// definitive verdict, not an execution failure.
		if isFullOID(first) {
			return false, nil
		}
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return false, fmt.Errorf("%s: %w", msg, err)
		}
		return false, err
	}
	return first != "" && first == baseTree, nil
}

// isFullOID reports whether s is a full git object id (40- or 64-hex).
func isFullOID(s string) bool {
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

// ParseCherryOutput은 git cherry 출력을 파싱하여
// 모든 커밋이 반영되었는지 판별한다.
// 반환값: allApplied (모두 `-`), mixed (`+`와 `-` 혼합), err
func ParseCherryOutput(output string) (allApplied bool, mixed bool, err error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false, false, nil
	}

	lines := strings.Split(trimmed, "\n")

	var hasPlus, hasMinus bool
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "- "):
			hasMinus = true
		case strings.HasPrefix(line, "+ "):
			hasPlus = true
		default:
			return false, false, fmt.Errorf("unexpected cherry line format: %q", line)
		}
	}

	if hasMinus && !hasPlus {
		return true, false, nil
	}
	if hasMinus && hasPlus {
		return false, true, nil
	}
	// all plus or no lines with content
	return false, false, nil
}
