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

// InitPlan은 실제로 생성/수정할 파일 목록과 내용을 담는다.
type InitPlan struct {
	Gitignore *FilePlan  // nil이면 건너뜀
	Config    *FilePlan  // nil이면 건너뜀
	AIFiles   []FilePlan
}

// collectPlans는 InitPlan에서 nil이 아닌 모든 FilePlan을 flat list로 수집한다.
func collectPlans(plan *InitPlan) []FilePlan {
	var plans []FilePlan
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
	for _, fp := range collectPlans(plan) {
		if fp.Action == ActionSkip {
			fmt.Fprintf(w, "skipped: %s\n", fp.Path)
			continue
		}

		if dryRun {
			fmt.Fprintf(w, "--- %s ---\n", fp.Path)
			fmt.Fprint(w, fp.Content)
			fmt.Fprintln(w)
			continue
		}

		// 실제 파일 쓰기
		if err := os.MkdirAll(filepath.Dir(fp.Path), 0o755); err != nil {
			return fmt.Errorf("gk init: write %s: %w", fp.Path, err)
		}
		if err := os.WriteFile(fp.Path, []byte(fp.Content), 0o644); err != nil {
			return fmt.Errorf("gk init: write %s: %w", fp.Path, err)
		}

		label := "created"
		if fp.Action == ActionMerge || fp.Action == ActionOverwrite {
			label = "updated"
		}
		fmt.Fprintf(w, "%s: %s\n", label, fp.Path)
	}
	return nil
}
