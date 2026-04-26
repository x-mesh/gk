package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/x-mesh/gk/internal/initx"
)

// RunInitTUI는 huh form을 표시하여 사용자가 분석 결과를 확인·수정할 수 있게 한다.
// 사용자가 확인하면 plan을 그대로 반환하고, 취소하면 모든 항목을 Skip으로 변경한다.
func RunInitTUI(result *initx.AnalysisResult, plan *initx.InitPlan) (*initx.InitPlan, error) {
	summary := buildSummary(result, plan)

	var confirm bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("gk init — project analysis").
				Description(summary),
			huh.NewConfirm().
				Title("proceed with initialization?").
				Value(&confirm),
		),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}

	if !confirm {
		return skipAll(plan), nil
	}
	return plan, nil
}

// buildSummary는 분석 결과와 plan을 기반으로 TUI에 표시할 요약 문자열을 생성한다.
func buildSummary(result *initx.AnalysisResult, plan *initx.InitPlan) string {
	var b strings.Builder

	// 감지된 언어
	if len(result.Languages) > 0 {
		names := make([]string, len(result.Languages))
		for i, l := range result.Languages {
			names[i] = l.Name
		}
		fmt.Fprintf(&b, "Languages: %s\n", strings.Join(names, ", "))
	} else {
		b.WriteString("Languages: (none detected)\n")
	}

	// 빌드 시스템
	if len(result.BuildSystems) > 0 {
		names := make([]string, len(result.BuildSystems))
		for i, bs := range result.BuildSystems {
			names[i] = bs.Name
		}
		fmt.Fprintf(&b, "Build systems: %s\n", strings.Join(names, ", "))
	}

	// base branch
	if result.BaseBranch != "" {
		fmt.Fprintf(&b, "Base branch: %s\n", result.BaseBranch)
	}

	b.WriteString("\nFiles to generate:\n")

	// .gitignore
	if plan.Gitignore != nil && plan.Gitignore.Action != initx.ActionSkip {
		fmt.Fprintf(&b, "  • .gitignore (%s)\n", actionLabel(plan.Gitignore.Action))
	}

	// .gk.yaml
	if plan.Config != nil && plan.Config.Action != initx.ActionSkip {
		fmt.Fprintf(&b, "  • .gk.yaml (%s)\n", actionLabel(plan.Config.Action))
	}

	// AI files
	for _, af := range plan.AIFiles {
		if af.Action != initx.ActionSkip {
			fmt.Fprintf(&b, "  • %s (%s)\n", af.Path, actionLabel(af.Action))
		}
	}

	return b.String()
}

func actionLabel(a initx.FileAction) string {
	switch a {
	case initx.ActionCreate:
		return "create"
	case initx.ActionMerge:
		return "merge"
	case initx.ActionOverwrite:
		return "overwrite"
	default:
		return "skip"
	}
}

// skipAll은 plan의 모든 항목을 ActionSkip으로 변경한 복사본을 반환한다.
func skipAll(plan *initx.InitPlan) *initx.InitPlan {
	cp := &initx.InitPlan{}
	if plan.Gitignore != nil {
		g := *plan.Gitignore
		g.Action = initx.ActionSkip
		cp.Gitignore = &g
	}
	if plan.Config != nil {
		c := *plan.Config
		c.Action = initx.ActionSkip
		cp.Config = &c
	}
	for _, af := range plan.AIFiles {
		af.Action = initx.ActionSkip
		cp.AIFiles = append(cp.AIFiles, af)
	}
	return cp
}
