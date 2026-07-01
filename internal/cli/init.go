package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
		Long: `gk init analyzes the project and scaffolds .gitignore and .gk.yaml.
Pass --kiro to also write .kiro/steering documents. CLAUDE.md and
AGENTS.md are managed separately by gk agents install.

	Use --only to generate a specific subset, --dry-run to preview, and
	--force to overwrite existing files.
	`,
		RunE: runInit,
	}
	initCmd.Flags().String("only", "", "generate only the specified target (gitignore, config, ai)")
	initCmd.Flags().Bool("force", false, "overwrite existing files instead of merging")
	initCmd.Flags().Bool("kiro", false, "also scaffold .kiro/steering/ documents")
	initCmd.Flags().Bool("ai-gitignore", false, "ask the configured AI provider for extra .gitignore patterns after confirmation")

	// deprecated alias: gk init ai
	initCmd.AddCommand(&cobra.Command{
		Use:    "ai",
		Short:  "Scaffold Kiro steering files (deprecated: use gk init --kiro)",
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
	aiGitignore, _ := cmd.Flags().GetBool("ai-gitignore")
	dryRun, _ := cmd.Root().PersistentFlags().GetBool("dry-run")
	jsonOut := JSONOut()
	humanOut := cmd.OutOrStdout()
	if jsonOut {
		humanOut = io.Discard
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

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

	dir, gitRunner, gitInitState, err := prepareInitGit(ctx, dir, dryRun, humanOut)
	if err != nil {
		return err
	}

	// 프로젝트 분석
	result, err := initx.AnalyzeProject(dir, gitRunner)
	if err != nil {
		return fmt.Errorf("gk init: analyze: %w", err)
	}

	// InitPlan 생성
	plan, err := buildInitPlan(dir, result, only, force, kiro)
	if err != nil {
		return err
	}

	confirmed := true

	// TTY 환경이고 dry-run이 아니면 TUI 표시
	if promptAllowed() && !dryRun {
		var ok bool
		plan, ok, err = RunInitTUI(result, plan)
		if err != nil {
			return fmt.Errorf("gk init: tui: %w", err)
		}
		confirmed = ok
	}

	// AI gitignore augmentation is opt-in and runs only after confirmation.
	// It may send project metadata to the configured provider, so dry-run and
	// the preview form stay local and deterministic.
	if confirmed && aiGitignore && !dryRun && (only == "" || only == "gitignore") {
		aiPatterns := suggestAIGitignore(dir, result)
		applyAIGitignoreSuggestions(plan, aiPatterns)
	}

	// 실행
	files, err := initx.ExecutePlanDetailed(plan, humanOut, dryRun)
	if err != nil {
		return err
	}

	// 컴파일 산출물 경고 — .gitignore 추가만으로는 이미 tracked된 파일을
	// 제거하지 못하므로 사용자에게 git rm 가이드를 보여준다.
	gitignoreApplied := plan.Gitignore != nil && plan.Gitignore.Action != initx.ActionSkip && !dryRun
	if gitignoreApplied {
		warnExistingGarbage(humanOut, result.Garbage)
	}
	if jsonOut {
		out := initResultJSON{
			Schema:  1,
			Result:  initResultStatus(dryRun),
			DryRun:  dryRun,
			Dir:     dir,
			GitInit: gitInitState,
			Files:   files,
		}
		if gitignoreApplied && len(result.Garbage) > 0 {
			out.Garbage = result.Garbage
		}
		return emitAgentResult(cmd.OutOrStdout(), out)
	}
	return nil
}

type initResultJSON struct {
	Schema  int                      `json:"schema"`
	Result  string                   `json:"result"`
	DryRun  bool                     `json:"dry_run"`
	Dir     string                   `json:"dir"`
	GitInit string                   `json:"git_init"`
	Files   []initx.FileResult       `json:"files"`
	Garbage []initx.GarbageDetection `json:"garbage,omitempty"`
}

func initResultStatus(dryRun bool) string {
	if dryRun {
		return "dry-run"
	}
	return "initialized"
}

func prepareInitGit(ctx context.Context, dir string, dryRun bool, w io.Writer) (string, initx.GitRunner, string, error) {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	probe := &git.ExecRunner{Dir: dir}
	if top, ok := initGitToplevel(ctx, probe); ok {
		return top, &git.ExecRunner{Dir: top}, "existing", nil
	}
	hasGit, err := hasGitMetadata(dir)
	if err != nil {
		return dir, nil, "", fmt.Errorf("gk init: stat .git: %w", err)
	}
	if hasGit {
		return dir, probe, "existing", nil
	}
	if dryRun {
		fmt.Fprintln(w, "(dry-run) would initialize git repository")
		return dir, nil, "planned", nil
	}
	if _, _, err := probe.Run(ctx, "init"); err != nil {
		return dir, nil, "", fmt.Errorf("gk init: git init: %w", err)
	}
	fmt.Fprintln(w, successLine("initialized", "git repository"))
	return dir, probe, "done", nil
}

func initGitToplevel(ctx context.Context, r initx.GitRunner) (string, bool) {
	stdout, _, err := r.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	top := strings.TrimSpace(string(stdout))
	return top, top != ""
}

func hasGitMetadata(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func applyAIGitignoreSuggestions(plan *initx.InitPlan, patterns []string) {
	if plan == nil || plan.Gitignore == nil {
		return
	}
	section := initx.FormatAISuggestedSection(patterns)
	if section == "" {
		return
	}
	merged, added := initx.MergeGitignore(plan.Gitignore.Content, section)
	if len(added) == 0 {
		return
	}
	plan.Gitignore.Content = merged
	if plan.Gitignore.Action == initx.ActionSkip {
		plan.Gitignore.Action = initx.ActionMerge
	}
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
func buildInitPlan(dir string, result *initx.AnalysisResult, only string, force, kiro bool) (*initx.InitPlan, error) {
	plan := &initx.InitPlan{}

	if only == "" || only == "gitignore" {
		fp, err := buildGitignorePlan(dir, result, force)
		if err != nil {
			return nil, err
		}
		plan.Gitignore = fp
	}
	if only == "" || only == "config" {
		fp, err := buildConfigPlan(dir, result, force)
		if err != nil {
			return nil, err
		}
		plan.Config = fp
	}
	if only == "" || only == "ai" {
		files, err := buildAIFilesPlan(dir, result, force, kiro)
		if err != nil {
			return nil, err
		}
		plan.AIFiles = files
	}

	return plan, nil
}

func buildGitignorePlan(dir string, result *initx.AnalysisResult, force bool) (*initx.FilePlan, error) {
	path := filepath.Join(dir, ".gitignore")
	generated := initx.GenerateGitignore(result)

	existing, err := os.ReadFile(path)
	if err != nil && os.IsNotExist(err) {
		// 파일 없음 → 새로 생성
		return &initx.FilePlan{Path: path, Content: generated, Action: initx.ActionCreate}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gk init: read %s: %w", path, err)
	}

	if force {
		return &initx.FilePlan{Path: path, Content: generated, Action: initx.ActionOverwrite}, nil
	}

	// 병합
	merged, added := initx.MergeGitignore(string(existing), generated)
	if len(added) == 0 {
		return &initx.FilePlan{Path: path, Content: string(existing), Action: initx.ActionSkip}, nil
	}
	return &initx.FilePlan{Path: path, Content: merged, Action: initx.ActionMerge}, nil
}

func buildConfigPlan(dir string, result *initx.AnalysisResult, force bool) (*initx.FilePlan, error) {
	path := filepath.Join(dir, ".gk.yaml")
	generated := initx.GenerateConfig(result)

	existing, err := os.ReadFile(path)
	if err != nil && os.IsNotExist(err) {
		return &initx.FilePlan{Path: path, Content: generated, Action: initx.ActionCreate}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gk init: read %s: %w", path, err)
	}

	if force {
		return &initx.FilePlan{Path: path, Content: generated, Action: initx.ActionOverwrite}, nil
	}

	merged, added, mergeErr := initx.MergeConfig(existing, []byte(generated))
	if mergeErr != nil {
		// 파싱 실패 시 skip
		return &initx.FilePlan{Path: path, Content: string(existing), Action: initx.ActionSkip}, nil
	}
	if len(added) == 0 {
		return &initx.FilePlan{Path: path, Content: string(existing), Action: initx.ActionSkip}, nil
	}
	return &initx.FilePlan{Path: path, Content: string(merged), Action: initx.ActionMerge}, nil
}

func buildAIFilesPlan(dir string, result *initx.AnalysisResult, force, kiro bool) ([]initx.FilePlan, error) {
	aiFiles := initx.GenerateAIContext(result, initx.AIContextOptions{IncludeKiro: kiro})

	var plans []initx.FilePlan
	for _, af := range aiFiles {
		path := filepath.Join(dir, af.Path)
		_, err := os.Stat(path)
		switch {
		case err != nil && os.IsNotExist(err): // 파일 없음
			plans = append(plans, initx.FilePlan{Path: path, Content: af.Content, Action: initx.ActionCreate})
		case err != nil:
			return nil, fmt.Errorf("gk init: stat %s: %w", path, err)
		case force:
			plans = append(plans, initx.FilePlan{Path: path, Content: af.Content, Action: initx.ActionOverwrite})
		default:
			plans = append(plans, initx.FilePlan{Path: path, Content: af.Content, Action: initx.ActionSkip})
		}
	}
	return plans, nil
}

// --- Deprecated alias handlers ---

func runInitAIDeprecated(cmd *cobra.Command, args []string) error {
	fmt.Fprintln(cmd.ErrOrStderr(), `"gk init ai" is deprecated, use "gk init --kiro"`)

	// 부모 initCmd의 RunE를 --only ai --kiro로 실행
	parent := cmd.Parent()
	if err := parent.Flags().Set("only", "ai"); err != nil {
		return err
	}
	if err := parent.Flags().Set("kiro", "true"); err != nil {
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
