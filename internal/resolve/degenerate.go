package resolve

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// Degenerate conflicts는 marker 파서가 다룰 수 없는 unmerged 경로다:
// working tree에 파일이 없거나(delete/modify), 내용에 파싱 가능한 충돌
// marker가 없는 경우(binary, add/add without markers 등). worktree 텍스트
// 대신 index stage(:1 base, :2 ours, :3 theirs)에서 직접 해결한다 —
// ours/theirs는 기계적으로, ai는 양쪽 전체 내용과 삭제 여부를 provider에
// 보내 keep/delete/merge를 결정한다.

// stagePresence는 unmerged 경로에 어떤 index stage가 존재하는지 기록한다.
type stagePresence struct {
	Base, Ours, Theirs bool
}

// unmergedStages는 `git ls-files -u -z` 한 번으로 모든 unmerged 경로의
// stage 존재 여부를 수집한다.
func (r *Resolver) unmergedStages(ctx context.Context) (map[string]stagePresence, error) {
	out, _, err := r.Runner.Run(ctx, "ls-files", "-u", "-z")
	if err != nil {
		return nil, fmt.Errorf("gk resolve: ls-files -u: %w", err)
	}
	m := make(map[string]stagePresence)
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		// "<mode> <sha> <stage>\t<path>"
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(rec[:tab])
		if len(meta) != 3 {
			continue
		}
		p := rec[tab+1:]
		sp := m[p]
		switch meta[2] {
		case "1":
			sp.Base = true
		case "2":
			sp.Ours = true
		case "3":
			sp.Theirs = true
		}
		m[p] = sp
	}
	return m, nil
}

// stageContent는 index stage의 파일 내용을 읽는다. stage가 없으면 nil.
func (r *Resolver) stageContent(ctx context.Context, stage int, path string) []byte {
	out, _, err := r.Runner.Run(ctx, "show", fmt.Sprintf(":%d:%s", stage, path))
	if err != nil {
		return nil
	}
	return out
}

// resolveDegenerate는 퇴화 충돌 경로 하나를 strategy에 따라 해결한다.
// (false, nil)은 "이 경로는 미해결로 남긴다"이며 에러가 아니다 — AI 모드의
// binary 파일 등 사람/기계적 전략이 필요한 경우다.
func (r *Resolver) resolveDegenerate(
	ctx context.Context,
	path string,
	sp stagePresence,
	opts ResolveOptions,
	opType string,
) (bool, error) {
	switch opts.Strategy {
	case StrategyOurs:
		return true, r.takeSide(ctx, path, sp.Ours, "--ours", opts.DryRun)
	case StrategyTheirs:
		return true, r.takeSide(ctx, path, sp.Theirs, "--theirs", opts.DryRun)
	case "ai":
		return r.resolveDegenerateAI(ctx, path, sp, opts, opType)
	}
	return false, nil
}

// takeSide는 기계적 side 선택을 적용한다: 그 side의 stage가 있으면 복원,
// 없으면 그 side가 파일을 지웠다는 뜻이므로 index/worktree에서 제거한다.
func (r *Resolver) takeSide(ctx context.Context, path string, sideExists bool, flag string, dryRun bool) error {
	if dryRun {
		action := "restore the " + strings.TrimPrefix(flag, "--") + " side"
		if !sideExists {
			action = "delete (the " + strings.TrimPrefix(flag, "--") + " side removed it)"
		}
		if r.Stdout != nil {
			fmt.Fprintf(r.Stdout, "%s: would %s\n", path, action)
		}
		return nil
	}
	if !sideExists {
		if r.deferStage {
			// 워크트리에서만 지우고 index 삭제는 게이트 통과 뒤로 미룬다 —
			// stage가 남아 있어야 실패 시 checkout -m으로 복원할 수 있다.
			if err := r.removeFile(r.absPath(path)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("gk resolve: remove %s: %w", path, err)
			}
			r.pendingDelete = append(r.pendingDelete, path)
			return nil
		}
		if _, stderr, err := r.Runner.Run(ctx, "rm", "-q", "-f", "--", path); err != nil {
			return fmt.Errorf("gk resolve: git rm %s: %s: %w", path, strings.TrimSpace(string(stderr)), err)
		}
		return nil
	}
	if _, stderr, err := r.Runner.Run(ctx, "checkout", flag, "--", path); err != nil {
		return fmt.Errorf("gk resolve: git checkout %s %s: %s: %w", flag, path, strings.TrimSpace(string(stderr)), err)
	}
	if r.deferStage {
		r.pendingStage = append(r.pendingStage, path)
		return nil
	}
	return GitAdd(ctx, r.Runner, path)
}

// resolveDegenerateAI는 양쪽 stage 내용(과 삭제 여부)을 provider에 보내
// keep/delete/merge를 결정하게 한다.
func (r *Resolver) resolveDegenerateAI(
	ctx context.Context,
	path string,
	sp stagePresence,
	opts ResolveOptions,
	opType string,
) (bool, error) {
	resolver, ok := r.Provider.(provider.ConflictResolver)
	if !ok {
		return false, nil
	}

	ours := r.stageContent(ctx, 2, path)
	theirs := r.stageContent(ctx, 3, path)
	base := r.stageContent(ctx, 1, path)
	if isBinary(ours) || isBinary(theirs) || isBinary(base) {
		if r.Stderr != nil {
			fmt.Fprintf(r.Stderr, "warning: gk resolve: %s is binary — resolve with --strategy ours|theirs\n", path)
		}
		return false, nil
	}

	in := provider.ConflictResolutionInput{
		FilePath: path,
		Hunks: []provider.ConflictHunkInput{{
			Index:         0,
			Ours:          contentLines(ours),
			Theirs:        contentLines(theirs),
			Base:          contentLines(base),
			OursDeleted:   !sp.Ours,
			TheirsDeleted: !sp.Theirs,
		}},
		OperationType: opType,
		Lang:          opts.Lang,
	}
	res, err := resolver.ResolveConflicts(ctx, in)
	aiResolutions, validationErr := hunkResolutionsFromAI(res.Resolutions, 1)
	if err != nil || validationErr != nil {
		if r.Stderr != nil {
			if err == nil {
				err = validationErr
			}
			fmt.Fprintf(r.Stderr, "warning: gk resolve: AI analysis failed for %s: %v\n", path, err)
		}
		return false, nil
	}

	out := aiResolutions[0]
	// 삭제는 "선택된 쪽이 파일을 지운 쪽"일 때만이다. ours/theirs 선택에서
	// Resolved가 비는 것은 정상(그쪽 내용을 그대로 쓴다는 뜻)이므로 빈
	// Resolved 자체를 삭제 신호로 읽으면 안 된다. merged인데 내용이 비면
	// 한쪽이 삭제된 상황에서만 삭제로 해석한다(프롬프트 규약).
	deleteFile := (out.Strategy == StrategyOurs && !sp.Ours) ||
		(out.Strategy == StrategyTheirs && !sp.Theirs) ||
		(out.Strategy == StrategyMerged && len(out.ResolvedLines) == 0 && (!sp.Ours || !sp.Theirs))
	if r.Stdout != nil {
		decision := string(out.Strategy)
		if deleteFile {
			decision += " (delete)"
		}
		fmt.Fprintf(r.Stdout, "%s: %s — %s\n", path, decision, out.Rationale)
	}
	if opts.DryRun {
		return true, nil
	}

	if deleteFile {
		if r.deferStage {
			if err := r.removeFile(r.absPath(path)); err != nil && !os.IsNotExist(err) {
				return false, fmt.Errorf("gk resolve: remove %s: %w", path, err)
			}
			r.pendingDelete = append(r.pendingDelete, path)
			return true, nil
		}
		if _, stderr, rmErr := r.Runner.Run(ctx, "rm", "-q", "-f", "--", path); rmErr != nil {
			return false, fmt.Errorf("gk resolve: git rm %s: %s: %w", path, strings.TrimSpace(string(stderr)), rmErr)
		}
		return true, nil
	}

	// Side-picks use the stage content VERBATIM — same guard as the marker
	// path: the model's claim of "ours"/"theirs" is trusted, its payload not.
	lines := out.ResolvedLines
	switch out.Strategy {
	case StrategyOurs:
		lines = contentLines(ours)
	case StrategyTheirs:
		lines = contentLines(theirs)
	}
	content := []byte(strings.Join(lines, "\n") + "\n")
	if !opts.NoBackup {
		if orig, rerr := r.readFile(r.absPath(path)); rerr == nil {
			if berr := BackupOriginal(r.WriteFile, r.absPath(path), orig); berr != nil && r.Stderr != nil {
				fmt.Fprintf(r.Stderr, "warning: gk resolve: backup %s.orig: %v\n", path, berr)
			}
		}
	}
	if err := WriteResolved(r.WriteFile, r.absPath(path), content); err != nil {
		return false, fmt.Errorf("gk resolve: write %s: %w", path, err)
	}
	if r.deferStage {
		r.pendingStage = append(r.pendingStage, path)
		return true, nil
	}
	if err := GitAdd(ctx, r.Runner, path); err != nil {
		return false, fmt.Errorf("gk resolve: git add %s: %w", path, err)
	}
	return true, nil
}

// isBinary는 NUL byte 존재로 binary 여부를 판정한다 (git과 같은 휴리스틱).
func isBinary(b []byte) bool {
	return bytes.IndexByte(b, 0) >= 0
}

// contentLines는 파일 내용을 라인 슬라이스로 바꾼다. 빈 내용은 nil.
func contentLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}
