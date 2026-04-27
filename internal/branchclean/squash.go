package branchclean

import (
	"context"
	"fmt"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// SquashDetector는 git cherry를 사용하여 squash merge를 감지한다.
type SquashDetector struct {
	Runner git.Runner
}

// DetectSquashMerged는 각 브랜치에 대해 git cherry를 실행하여
// squash-merged 또는 ambiguous 상태를 판별한다.
// git cherry 실패 시 해당 브랜치를 건너뛰고 경고를 반환한다.
func (d *SquashDetector) DetectSquashMerged(
	ctx context.Context,
	branches []string,
	base string,
	protected map[string]bool,
) (squashMerged []string, ambiguous []string, warnings []string) {
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

		switch {
		case allApplied:
			squashMerged = append(squashMerged, branch)
		case mixed:
			ambiguous = append(ambiguous, branch)
		}
	}
	return squashMerged, ambiguous, warnings
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
