package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/x-mesh/gk/internal/initx"
	"github.com/x-mesh/gk/internal/ui"
)

// RunInitTUI runs the interactive init flow: the remote account/name
// step (when `remote` asks for it), then the analysis summary and a
// single confirm covering files and remote alike. On cancel, every plan
// entry — the remote step included — is set to Skip; cancellation is a
// harmless no-op, never an error.
func RunInitTUI(result *initx.AnalysisResult, plan *initx.InitPlan, remote *remoteTUIInput) (*initx.InitPlan, *remotePlan, bool, error) {
	ctx := context.Background()

	// Remote step first, so its outcome lands in the same summary the
	// final confirm approves. A pre-resolved plan (--remote flag or an
	// existing origin) is echoed as-is; only the undecided case prompts.
	var rPlan *remotePlan
	if remote != nil {
		rPlan = remote.Plan
		if rPlan == nil {
			var err error
			rPlan, err = promptRemoteTUI(ctx, remote)
			if err != nil {
				return nil, nil, false, err
			}
		}
	}

	summary := buildSummary(result, plan, rPlan)

	// Note-style header lives outside the bubbletea program so the user
	// can scroll through the analysis even after the confirm exits.
	fmt.Fprintln(os.Stderr, "gk init — project analysis")
	fmt.Fprintln(os.Stderr, summary)

	confirm, err := ui.ConfirmTUI(ctx, "proceed with initialization?", "", false)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return skipAll(plan), skipRemote(rPlan), false, nil
		}
		return nil, nil, false, err
	}
	if !confirm {
		return skipAll(plan), skipRemote(rPlan), false, nil
	}
	return plan, rPlan, true, nil
}

// buildSummary는 분석 결과와 plan을 기반으로 TUI에 표시할 요약 문자열을 생성한다.
func buildSummary(result *initx.AnalysisResult, plan *initx.InitPlan, rPlan *remotePlan) string {
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

	// Remote — the one non-file entry the confirm also covers.
	if rPlan != nil {
		switch {
		case rPlan.ExistingURL != "":
			fmt.Fprintf(&b, "\nRemote:\n  • %s → %s (existing)\n", rPlan.RemoteName, rPlan.ExistingURL)
		case rPlan.Action != initx.ActionSkip:
			fmt.Fprintf(&b, "\nRemote:\n  • %s → %s (add)\n", rPlan.RemoteName, rPlan.URL)
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

// skipRemote는 remote plan을 ActionSkip으로 변경한 복사본을 반환한다 —
// skipAll의 remote 대응. confirm 거부가 remote add로 새어나가지 않도록
// executeRemotePlan의 confirmed 가드와 이중으로 막는다.
func skipRemote(rp *remotePlan) *remotePlan {
	if rp == nil {
		return nil
	}
	cp := *rp
	cp.Action = initx.ActionSkip
	return &cp
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
