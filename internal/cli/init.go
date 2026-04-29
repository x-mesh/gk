package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/initx"
	"github.com/x-mesh/gk/internal/ui"
)

// validOnlyValues는 --only 플래그에 허용되는 값 목록이다.
var validOnlyValues = map[string]bool{
	"gitignore": true,
	"config":    true,
	"ai":        true,
}

func init() {
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize gk scaffolding in a repository",
		Long: `gk init analyzes the project and scaffolds .gitignore, .gk.yaml, and
AI context files (CLAUDE.md, AGENTS.md) in one step.

Use --only to generate a specific subset, --dry-run to preview, and
--force to overwrite existing files.
`,
		RunE: runInit,
	}
	initCmd.Flags().String("only", "", "generate only the specified target (gitignore, config, ai)")
	initCmd.Flags().Bool("force", false, "overwrite existing files instead of merging")
	initCmd.Flags().Bool("kiro", false, "also scaffold .kiro/steering/ documents")

	// deprecated alias: gk init ai
	initCmd.AddCommand(&cobra.Command{
		Use:    "ai",
		Short:  "Scaffold AI context files (deprecated: use gk init --only ai)",
		Hidden: true,
		RunE:   runInitAIDeprecated,
	})

	// deprecated alias: gk init config
	deprecatedConfigCmd := &cobra.Command{
		Use:    "config",
		Short:  "Scaffold global config (deprecated: use gk config init)",
		Hidden: true,
		RunE:   runInitConfigDeprecated,
	}
	deprecatedConfigCmd.Flags().Bool("force", false, "overwrite an existing file")
	deprecatedConfigCmd.Flags().String("out", "", "write to this path instead of the global default")
	initCmd.AddCommand(deprecatedConfigCmd)

	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	only, _ := cmd.Flags().GetString("only")
	force, _ := cmd.Flags().GetBool("force")
	kiro, _ := cmd.Flags().GetBool("kiro")
	dryRun, _ := cmd.Root().PersistentFlags().GetBool("dry-run")

	dir := RepoFlag()
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("gk init: cannot determine working directory: %w", err)
		}
	}

	// --only 유효성 검사
	if only != "" && !validOnlyValues[only] {
		return fmt.Errorf("gk init: invalid --only value %q (valid: gitignore, config, ai)", only)
	}

	// .git 미존재 시 git init 자동 실행
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		gitRunner := &git.ExecRunner{Dir: dir}
		if _, _, err := gitRunner.Run(context.Background(), "init"); err != nil {
			return fmt.Errorf("gk init: git init: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "initialized git repository")
	}

	// 프로젝트 분석
	gitRunner := &git.ExecRunner{Dir: dir}
	result, err := initx.AnalyzeProject(dir, gitRunner)
	if err != nil {
		return fmt.Errorf("gk init: analyze: %w", err)
	}

	// InitPlan 생성
	plan := buildInitPlan(dir, result, only, force, kiro)

	// AI provider가 있으면 gitignore 패턴 추가 제안. dry-run에선
	// 건너뛴다 — 외부 HTTP 호출 때문에 plan preview가 수십 초 늘어
	// 지는 일이 없도록 (특히 `make check`의 -race 테스트에서 시간
	// 폭증의 주범이었음). 실제 init에서만 AI 의견을 반영.
	if !dryRun && (only == "" || only == "gitignore") {
		aiPatterns := suggestAIGitignore(dir, result)
		if len(aiPatterns) > 0 && plan.Gitignore != nil {
			aiSection := initx.FormatAISuggestedSection(aiPatterns)
			plan.Gitignore.Content += "\n" + aiSection
			// AI 제안이 추가되면 skip → merge로 승격
			if plan.Gitignore.Action == initx.ActionSkip {
				plan.Gitignore.Action = initx.ActionMerge
			}
		}
	}

	// TTY 환경이고 dry-run이 아니면 TUI 표시
	if ui.IsTerminal() && !dryRun {
		plan, err = RunInitTUI(result, plan)
		if err != nil {
			return fmt.Errorf("gk init: tui: %w", err)
		}
	}

	// 실행
	if err := initx.ExecutePlan(plan, cmd.OutOrStdout(), dryRun); err != nil {
		return err
	}

	// 컴파일 산출물 경고 — .gitignore 추가만으로는 이미 tracked된 파일을
	// 제거하지 못하므로 사용자에게 git rm 가이드를 보여준다.
	if !dryRun {
		warnExistingGarbage(cmd.OutOrStdout(), result.Garbage)
	}
	return nil
}

func warnExistingGarbage(w io.Writer, garbage []initx.GarbageDetection) {
	if len(garbage) == 0 {
		return
	}
	total := 0
	for _, g := range garbage {
		total += g.Count
	}
	fmt.Fprintf(w, "\ngk init: %d compiled artifact(s) already in working tree (now in .gitignore):\n", total)
	for _, g := range garbage {
		fmt.Fprintf(w, "  %s — %d file(s)", g.Pattern, g.Count)
		if len(g.Sample) > 0 {
			fmt.Fprintf(w, " (e.g. %s", g.Sample[0])
			if g.Count > 1 {
				fmt.Fprint(w, ", ...")
			}
			fmt.Fprint(w, ")")
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "\nTo untrack files already committed:")
	fmt.Fprintln(w, "  git rm -rf --cached <pattern>")
	fmt.Fprintln(w, "  git commit -m \"chore: untrack compiled artifacts\"")
}

// buildInitPlan은 분석 결과와 플래그를 기반으로 InitPlan을 구성한다.
func buildInitPlan(dir string, result *initx.AnalysisResult, only string, force, kiro bool) *initx.InitPlan {
	plan := &initx.InitPlan{}

	if only == "" || only == "gitignore" {
		plan.Gitignore = buildGitignorePlan(dir, result, force)
	}
	if only == "" || only == "config" {
		plan.Config = buildConfigPlan(dir, result, force)
	}
	if only == "" || only == "ai" {
		plan.AIFiles = buildAIFilesPlan(dir, result, force, kiro)
	}

	return plan
}

func buildGitignorePlan(dir string, result *initx.AnalysisResult, force bool) *initx.FilePlan {
	path := filepath.Join(dir, ".gitignore")
	generated := initx.GenerateGitignore(result)

	existing, err := os.ReadFile(path)
	if err != nil {
		// 파일 없음 → 새로 생성
		return &initx.FilePlan{Path: path, Content: generated, Action: initx.ActionCreate}
	}

	if force {
		return &initx.FilePlan{Path: path, Content: generated, Action: initx.ActionOverwrite}
	}

	// 병합
	merged, added := initx.MergeGitignore(string(existing), generated)
	if len(added) == 0 {
		return &initx.FilePlan{Path: path, Content: string(existing), Action: initx.ActionSkip}
	}
	return &initx.FilePlan{Path: path, Content: merged, Action: initx.ActionMerge}
}

func buildConfigPlan(dir string, result *initx.AnalysisResult, force bool) *initx.FilePlan {
	path := filepath.Join(dir, ".gk.yaml")
	generated := initx.GenerateConfig(result)

	existing, err := os.ReadFile(path)
	if err != nil {
		return &initx.FilePlan{Path: path, Content: generated, Action: initx.ActionCreate}
	}

	if force {
		return &initx.FilePlan{Path: path, Content: generated, Action: initx.ActionOverwrite}
	}

	merged, added, mergeErr := initx.MergeConfig(existing, []byte(generated))
	if mergeErr != nil {
		// 파싱 실패 시 skip
		return &initx.FilePlan{Path: path, Content: string(existing), Action: initx.ActionSkip}
	}
	if len(added) == 0 {
		return &initx.FilePlan{Path: path, Content: string(existing), Action: initx.ActionSkip}
	}
	return &initx.FilePlan{Path: path, Content: string(merged), Action: initx.ActionMerge}
}

func buildAIFilesPlan(dir string, result *initx.AnalysisResult, force, kiro bool) []initx.FilePlan {
	aiFiles := initx.GenerateAIContext(result, initx.AIContextOptions{IncludeKiro: kiro})

	var plans []initx.FilePlan
	for _, af := range aiFiles {
		path := filepath.Join(dir, af.Path)
		_, err := os.Stat(path)
		switch {
		case err != nil: // 파일 없음
			plans = append(plans, initx.FilePlan{Path: path, Content: af.Content, Action: initx.ActionCreate})
		case force:
			plans = append(plans, initx.FilePlan{Path: path, Content: af.Content, Action: initx.ActionOverwrite})
		default:
			plans = append(plans, initx.FilePlan{Path: path, Content: af.Content, Action: initx.ActionSkip})
		}
	}
	return plans
}

// --- Deprecated alias handlers ---

func runInitAIDeprecated(cmd *cobra.Command, args []string) error {
	fmt.Fprintln(cmd.ErrOrStderr(), `"gk init ai" is deprecated, use "gk init --only ai"`)

	// 부모 initCmd의 RunE를 --only ai로 실행
	parent := cmd.Parent()
	if err := parent.Flags().Set("only", "ai"); err != nil {
		return err
	}
	return runInit(parent, nil)
}

func runInitConfigDeprecated(cmd *cobra.Command, args []string) error {
	fmt.Fprintln(cmd.ErrOrStderr(), `"gk init config" is deprecated, use "gk config init"`)
	return runConfigInit(cmd, args)
}

// --- Legacy helpers (kept for backward compat, used by deprecated alias) ---

// detectProjectType inspects dir for well-known manifest files and returns a
// short language/runtime identifier. Superseded by initx.AnalyzeProject.
func detectProjectType(dir string) string {
	manifests := []struct {
		file string
		kind string
	}{
		{"go.mod", "go"},
		{"package.json", "node"},
		{"pyproject.toml", "python"},
		{"Cargo.toml", "rust"},
		{"pom.xml", "java"},
	}
	for _, m := range manifests {
		if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
			return m.kind
		}
	}
	return "unknown"
}

// suggestAIGitignore는 AI provider를 사용하여 프로젝트에 맞는 추가 gitignore 패턴을 제안한다.
// provider가 없거나 실패하면 빈 목록을 반환한다 (graceful degradation).
//
// AI suggestion is informational — bounded by a hard ctx timeout so a
// slow/unreachable provider doesn't block `gk init` indefinitely.
// Skipped entirely under `go test` so the test suite never reaches the
// network (was the dominant cost in `make check` — 456 s with race).
func suggestAIGitignore(dir string, result *initx.AnalysisResult) []string {
	if testing.Testing() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// config에서 ai.provider + 모델/엔드포인트 override를 읽어서 사용
	cfg, _ := config.Load(nil)
	p, err := provider.NewProvider(ctx, aiFactoryOptions(cfg))
	if err != nil {
		Dbg("ai gitignore: no provider available: %v", err)
		return nil
	}

	gs, ok := p.(provider.GitignoreSuggester)
	if !ok {
		Dbg("ai gitignore: provider %q does not support GitignoreSuggester", p.Name())
		return nil
	}

	Dbg("ai gitignore: using provider %q", p.Name())
	stop := ui.StartBubbleSpinner(fmt.Sprintf("ai gitignore — asking %s for project-specific patterns", p.Name()))
	patterns := initx.SuggestGitignorePatterns(ctx, gs, dir, result)
	stop()
	Dbg("ai gitignore: got %d patterns", len(patterns))
	return patterns
}
