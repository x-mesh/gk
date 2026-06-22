package initx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileAction은 파일에 대해 수행할 동작을 나타낸다.
type FileAction int

const (
	ActionCreate    FileAction = iota // 새 파일 생성
	ActionMerge                       // 기존 파일에 병합
	ActionOverwrite                   // 기존 파일 덮어쓰기
	ActionSkip                        // 건너뛰기
)

// FilePlan은 생성/수정할 파일 하나의 계획이다.
type FilePlan struct {
	Path    string
	Content string
	Action  FileAction
}

// FileResult is the observable outcome for one FilePlan.
type FileResult struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Status string `json:"status"` // created | updated | skipped | dry-run
}

// InitPlan은 실제로 생성/수정할 파일 목록과 내용을 담는다.
type InitPlan struct {
	Gitignore *FilePlan // nil이면 건너뜀
	Config    *FilePlan // nil이면 건너뜀
	AIFiles   []FilePlan
}

// collectPlans는 InitPlan에서 nil이 아닌 모든 FilePlan을 flat list로 수집한다.
func collectPlans(plan *InitPlan) []FilePlan {
	var plans []FilePlan
	if plan == nil {
		return plans
	}
	if plan.Gitignore != nil {
		plans = append(plans, *plan.Gitignore)
	}
	if plan.Config != nil {
		plans = append(plans, *plan.Config)
	}
	plans = append(plans, plan.AIFiles...)
	return plans
}

// ExecutePlan은 InitPlan에 따라 파일을 생성/수정한다.
// dryRun이 true이면 stdout에 미리보기만 출력한다.
func ExecutePlan(plan *InitPlan, w io.Writer, dryRun bool) error {
	_, err := ExecutePlanDetailed(plan, w, dryRun)
	return err
}

// ExecutePlanDetailed is ExecutePlan plus a structured result for JSON/agent
// callers. Writes are atomic per file; if a later file fails, earlier writes are
// rolled back to their pre-run content.
func ExecutePlanDetailed(plan *InitPlan, w io.Writer, dryRun bool) ([]FileResult, error) {
	var results []FileResult
	var active []FilePlan
	for _, fp := range collectPlans(plan) {
		if fp.Action == ActionSkip {
			fmt.Fprintf(w, "skipped: %s\n", fp.Path)
			results = append(results, FileResult{Path: fp.Path, Action: FileActionLabel(fp.Action), Status: "skipped"})
			continue
		}

		if dryRun {
			fmt.Fprintf(w, "--- %s ---\n", fp.Path)
			fmt.Fprint(w, fp.Content)
			fmt.Fprintln(w)
			results = append(results, FileResult{Path: fp.Path, Action: FileActionLabel(fp.Action), Status: "dry-run"})
			continue
		}
		active = append(active, fp)
	}
	if dryRun || len(active) == 0 {
		return results, nil
	}

	backups, err := snapshotFiles(active)
	if err != nil {
		return results, err
	}

	var written []FilePlan
	for _, fp := range active {
		mode := os.FileMode(0o644)
		if b := backups[fp.Path]; b.existed {
			mode = b.mode.Perm()
		}

		if err := writeFileAtomic(fp.Path, []byte(fp.Content), mode); err != nil {
			rollbackFiles(backups, written)
			return results, fmt.Errorf("gk init: write %s: %w", fp.Path, err)
		}
		written = append(written, fp)
	}

	for _, fp := range active {
		label := "created"
		status := "created"
		if fp.Action == ActionMerge || fp.Action == ActionOverwrite {
			label = "updated"
			status = "updated"
		}
		fmt.Fprintf(w, "%s: %s\n", label, fp.Path)
		results = append(results, FileResult{Path: fp.Path, Action: FileActionLabel(fp.Action), Status: status})
	}
	return results, nil
}

// FileActionLabel returns the stable external label for a FileAction.
func FileActionLabel(a FileAction) string {
	switch a {
	case ActionCreate:
		return "create"
	case ActionMerge:
		return "merge"
	case ActionOverwrite:
		return "overwrite"
	case ActionSkip:
		return "skip"
	default:
		return "unknown"
	}
}

type fileBackup struct {
	existed bool
	data    []byte
	mode    os.FileMode
}

func snapshotFiles(plans []FilePlan) (map[string]fileBackup, error) {
	backups := make(map[string]fileBackup, len(plans))
	for _, fp := range plans {
		if _, seen := backups[fp.Path]; seen {
			continue
		}
		info, err := os.Stat(fp.Path)
		if err != nil {
			if os.IsNotExist(err) {
				backups[fp.Path] = fileBackup{}
				continue
			}
			return nil, fmt.Errorf("gk init: stat %s: %w", fp.Path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("gk init: write %s: path is a directory", fp.Path)
		}
		data, err := os.ReadFile(fp.Path)
		if err != nil {
			return nil, fmt.Errorf("gk init: read %s: %w", fp.Path, err)
		}
		backups[fp.Path] = fileBackup{existed: true, data: data, mode: info.Mode()}
	}
	return backups, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func rollbackFiles(backups map[string]fileBackup, written []FilePlan) {
	for i := len(written) - 1; i >= 0; i-- {
		fp := written[i]
		b := backups[fp.Path]
		if !b.existed {
			_ = os.Remove(fp.Path)
			continue
		}
		_ = writeFileAtomic(fp.Path, b.data, b.mode.Perm())
	}
}
