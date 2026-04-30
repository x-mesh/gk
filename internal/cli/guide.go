package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	guideCmd := &cobra.Command{
		Use:   "guide [워크플로]",
		Short: "인터랙티브 git 워크플로 가이드",
		Long:  "일반적인 git 워크플로를 단계별로 안내합니다. 워크플로 이름을 인자로 전달하면 메뉴를 건너뛰고 바로 시작합니다.",
		RunE:  runGuide,
		Args:  cobra.MaximumNArgs(1),
	}
	rootCmd.AddCommand(guideCmd)
}

// runGuide implements the `gk guide [워크플로]` command.
// With an argument it jumps directly to that workflow; without one it
// shows an interactive picker (TTY) or a plain-text list (non-TTY).
func runGuide(cmd *cobra.Command, args []string) error {
	// Direct workflow selection via argument.
	if len(args) == 1 {
		wf, err := findWorkflow(args[0])
		if err != nil {
			return err
		}
		printWorkflowSteps(cmd, wf)
		return nil
	}

	// Non-TTY: print workflow list as plain text and exit.
	if !ui.IsTerminal() {
		printWorkflowListText(cmd)
		return nil
	}

	// TTY: interactive TablePicker.
	wf, err := pickWorkflow(cmd.Context())
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStderr()) // blank line between picker and steps
	printWorkflowSteps(cmd, wf)
	return nil
}

// findWorkflow looks up a workflow by Name. Returns a user-friendly
// error listing available workflows when the name is not found.
func findWorkflow(name string) (Workflow, error) {
	for _, wf := range defaultWorkflows {
		if wf.Name == name {
			return wf, nil
		}
	}
	names := make([]string, len(defaultWorkflows))
	for i, wf := range defaultWorkflows {
		names[i] = wf.Name
	}
	return Workflow{}, fmt.Errorf(
		"워크플로 %q을(를) 찾을 수 없습니다.\n사용 가능한 워크플로: %s",
		name, strings.Join(names, ", "),
	)
}

// pickWorkflow shows a TablePicker with the default workflows and
// returns the one the user selected.
func pickWorkflow(ctx context.Context) (Workflow, error) {
	items := make([]ui.PickerItem, len(defaultWorkflows))
	for i, wf := range defaultWorkflows {
		items[i] = ui.PickerItem{
			Key:     wf.Name,
			Display: wf.DisplayName,
			Cells:   []string{wf.DisplayName, wf.Description},
		}
	}

	picker := &ui.TablePicker{
		Headers: []string{"워크플로", "설명"},
	}
	chosen, err := picker.Pick(ctx, "워크플로를 선택하세요", items)
	if err != nil {
		return Workflow{}, err
	}

	for _, wf := range defaultWorkflows {
		if wf.Name == chosen.Key {
			return wf, nil
		}
	}
	// Should not happen — the picker only shows known workflows.
	return Workflow{}, fmt.Errorf("선택한 워크플로를 찾을 수 없습니다")
}

// printWorkflowSteps renders a step-by-step guide for the given
// workflow. Always uses easy language regardless of Easy Mode setting.
func printWorkflowSteps(cmd *cobra.Command, wf Workflow) {
	w := cmd.OutOrStdout()
	bold := color.New(color.Bold)
	faint := color.New(color.Faint)

	bold.Fprintf(w, "📋 %s\n", wf.DisplayName)
	faint.Fprintf(w, "   %s\n\n", wf.Description)

	for i, step := range wf.Steps {
		bold.Fprintf(w, "  %d단계: %s\n", i+1, step.Title)
		fmt.Fprintf(w, "     %s\n", step.Description)
		if step.Command != "" {
			fmt.Fprintf(w, "     ▸ 실행: ")
			color.New(color.FgCyan).Fprintf(w, "%s", step.Command)
			fmt.Fprintln(w)
		}
		if i < len(wf.Steps)-1 {
			fmt.Fprintln(w)
		}
	}
}

// printWorkflowListText outputs the workflow list as plain text for
// non-TTY environments.
func printWorkflowListText(cmd *cobra.Command) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "사용 가능한 워크플로:")
	fmt.Fprintln(w)
	for _, wf := range defaultWorkflows {
		fmt.Fprintf(w, "  %-20s %s\n", wf.Name, wf.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "사용법: gk guide <워크플로명>")
}
