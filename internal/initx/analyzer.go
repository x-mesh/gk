package initx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/x-mesh/gk/internal/commitlint"
)

// Language는 프로젝트에서 감지된 프로그래밍 언어를 나타낸다.
type Language struct {
	Name       string // "go", "node", "python", "rust", "java", "ruby", "php"
	MarkerFile string // 감지 근거 파일 (예: "go.mod")
}

// BuildSystem은 프로젝트에서 감지된 빌드 시스템을 나타낸다.
type BuildSystem struct {
	Name      string   // "make", "docker", "docker-compose", "taskfile", "just"
	Artifacts []string // 관련 산출물 패턴 (예: "bin/", "dist/")
}

// CommitAnalysis는 커밋 히스토리 분석 결과를 담는다.
type CommitAnalysis struct {
	ConventionalRatio float64  // conventional commit 사용 비율 (0.0~1.0)
	Types             []string // 빈도 내림차순 commit type 목록
	Lang              string   // 커밋 메시지 주요 언어 (BCP-47)
	SampleSize        int      // 분석한 커밋 수
}

// PreflightStep은 preflight 단계 하나를 나타낸다.
type PreflightStep struct {
	Name    string // step 이름 (예: "lint", "test")
	Command string // 실행 명령어 (예: "make lint")
}

// AnalysisResult는 프로젝트 분석의 전체 결과를 담는다.
type AnalysisResult struct {
	Languages    []Language
	BuildSystems []BuildSystem
	BaseBranch   string
	Protected    []string
	BranchPats   []string
	CommitInfo   CommitAnalysis
	Preflight    []PreflightStep
	AIProviders  []string
	// Garbage는 working tree에서 발견된 컴파일 산출물(.pyc, *.class 등)이다.
	// nil이면 깨끗하다는 뜻; 비어있지 않으면 CLI가 사용자에게 git rm -rf
	// --cached 가이드를 출력한다. .gitignore 패턴 추가만으로는 이미
	// tracked된 파일을 제거하지 못하기 때문이다.
	Garbage []GarbageDetection
}

// GitRunner는 git 명령어를 실행하는 인터페이스이다.
// internal/git.Runner와 동일한 시그니처를 사용하여 호환성을 유지한다.
type GitRunner interface {
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// markerToLang은 marker file → 언어 이름 매핑이다.
var markerToLang = map[string]string{
	"go.mod":           "go",
	"package.json":     "node",
	"requirements.txt": "python",
	"pyproject.toml":   "python",
	"setup.py":         "python",
	"Cargo.toml":       "rust",
	"pom.xml":          "java",
	"build.gradle":     "java",
	"Gemfile":          "ruby",
	"composer.json":    "php",
}

// MarkerFiles는 검색 대상 marker file 목록이다 (테스트에서도 참조).
var MarkerFiles = []string{
	"go.mod", "package.json",
	"requirements.txt", "pyproject.toml", "setup.py",
	"Cargo.toml", "pom.xml", "build.gradle",
	"Gemfile", "composer.json",
}

// buildSystemFiles는 빌드 시스템 파일 → 이름 매핑이다.
var buildSystemFiles = map[string]string{
	"Makefile":            "make",
	"Dockerfile":          "docker",
	"docker-compose.yml":  "docker-compose",
	"docker-compose.yaml": "docker-compose",
	"Taskfile.yml":        "taskfile",
	"justfile":            "just",
}

// BuildSystemFileList는 검색 대상 빌드 시스템 파일 목록이다 (테스트에서도 참조).
var BuildSystemFileList = []string{
	"Makefile", "Dockerfile",
	"docker-compose.yml", "docker-compose.yaml",
	"Taskfile.yml", "justfile",
}

// AnalyzeProject는 프로젝트 루트를 스캔하여 AnalysisResult를 반환한다.
// gitRunner가 nil이면 git 관련 분석(커밋, branch)을 건너뛴다.
func AnalyzeProject(dir string, gitRunner GitRunner) (*AnalysisResult, error) {
	result := &AnalysisResult{}

	// 1. 언어 감지
	result.Languages = detectLanguages(dir)
	if len(result.Languages) == 0 {
		fmt.Fprintln(os.Stderr, "gk init: no language marker files found")
	}

	// 2. 빌드 시스템 감지
	result.BuildSystems = detectBuildSystems(dir)

	// 3. Preflight step 감지
	result.Preflight = detectPreflightSteps(dir)

	// 4. AI provider 감지
	result.AIProviders = detectAIProviders()

	// 5. 컴파일 산출물 감지 — 이미 working tree에 흘러들어와 있는
	//    .pyc / *.class 등을 찾아 사용자에게 알린다.
	result.Garbage = DetectExistingGarbage(dir)

	// 6. Git 관련 분석 (gitRunner가 nil이면 건너뜀)
	if gitRunner != nil {
		ctx := context.Background()
		result.CommitInfo = analyzeCommits(ctx, gitRunner)
		result.BaseBranch, result.Protected, result.BranchPats = analyzeBranches(ctx, gitRunner)
	}

	return result, nil
}

// detectLanguages는 marker file 기반으로 언어를 감지한다.
// 동일 언어가 여러 marker file에 매핑되더라도 한 번만 반환한다.
func detectLanguages(dir string) []Language {
	var langs []Language
	seen := make(map[string]bool)

	for _, marker := range MarkerFiles {
		path := filepath.Join(dir, marker)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		lang := markerToLang[marker]
		if seen[lang] {
			continue
		}
		seen[lang] = true
		langs = append(langs, Language{Name: lang, MarkerFile: marker})
	}
	return langs
}

// detectBuildSystems는 빌드 시스템 파일을 검색한다.
// docker-compose.yml과 .yaml이 모두 존재해도 한 번만 반환한다.
func detectBuildSystems(dir string) []BuildSystem {
	var systems []BuildSystem
	seen := make(map[string]bool)

	for _, file := range BuildSystemFileList {
		path := filepath.Join(dir, file)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		name := buildSystemFiles[file]
		if seen[name] {
			continue
		}
		seen[name] = true
		systems = append(systems, BuildSystem{Name: name})
	}
	return systems
}

// detectPreflightSteps는 프로젝트에서 preflight step을 감지한다.
// 기본 steps (commit-lint, branch-check, no-conflict)는 항상 포함된다.
func detectPreflightSteps(dir string) []PreflightStep {
	// 기본 steps
	steps := []PreflightStep{
		{Name: "commit-lint", Command: "commit-lint"},
		{Name: "branch-check", Command: "branch-check"},
		{Name: "no-conflict", Command: "no-conflict"},
	}

	// package.json scripts 파싱
	steps = append(steps, detectNodeScripts(dir)...)

	// Makefile 타겟 파싱
	steps = append(steps, detectMakeTargets(dir)...)

	// .golangci.yml / .golangci.yaml 존재 확인
	if fileExists(filepath.Join(dir, ".golangci.yml")) || fileExists(filepath.Join(dir, ".golangci.yaml")) {
		steps = append(steps, PreflightStep{Name: "lint", Command: "golangci-lint run"})
	}

	// go.mod 존재 시 go test 추가
	if fileExists(filepath.Join(dir, "go.mod")) {
		steps = append(steps, PreflightStep{Name: "test", Command: "go test ./..."})
	}

	return steps
}

// detectNodeScripts는 package.json에서 lint/test 스크립트를 감지한다.
func detectNodeScripts(dir string) []PreflightStep {
	path := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var pkg struct {
		Scripts map[string]interface{} `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}

	var steps []PreflightStep
	if _, ok := pkg.Scripts["lint"]; ok {
		steps = append(steps, PreflightStep{Name: "lint", Command: "npm run lint"})
	}
	if _, ok := pkg.Scripts["test"]; ok {
		steps = append(steps, PreflightStep{Name: "test", Command: "npm run test"})
	}
	return steps
}

// detectMakeTargets는 Makefile에서 lint/test 타겟을 감지한다.
// 줄 시작이 "lint:" 또는 "test:"인지 확인한다.
func detectMakeTargets(dir string) []PreflightStep {
	path := filepath.Join(dir, "Makefile")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var steps []PreflightStep
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "lint:") {
			steps = append(steps, PreflightStep{Name: "lint", Command: "make lint"})
		}
		if strings.HasPrefix(trimmed, "test:") {
			steps = append(steps, PreflightStep{Name: "test", Command: "make test"})
		}
	}
	return steps
}

// detectAIProviders는 사용 가능한 AI provider를 감지한다.
// NVIDIA_API_KEY → gemini CLI → qwen CLI → kiro-cli CLI 순서로 확인한다.
func detectAIProviders() []string {
	var providers []string

	if os.Getenv("NVIDIA_API_KEY") != "" {
		providers = append(providers, "nvidia")
	}
	if os.Getenv("GROQ_API_KEY") != "" {
		providers = append(providers, "groq")
	}
	for _, bin := range []string{"gemini", "qwen", "kiro-cli"} {
		if _, err := exec.LookPath(bin); err == nil {
			providers = append(providers, bin)
		}
	}
	return providers
}

// fileExists는 파일 존재 여부를 확인하는 헬퍼이다.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DefaultCommitTypes는 conventional commit 비율이 낮거나 커밋이 없을 때 사용하는 기본 type 목록이다.
var DefaultCommitTypes = []string{"feat", "fix", "chore", "docs", "style", "refactor", "perf", "test", "build", "ci", "revert"}

// DefaultBranchPattern은 branch 패턴을 추출할 수 없을 때 사용하는 기본 패턴이다.
var DefaultBranchPattern = "^(feat|fix|chore|docs|refactor|test|perf|build|ci|revert)/[a-z0-9._-]+"

// analyzeCommits는 git log를 통해 커밋 히스토리를 분석한다.
// git 실패 시 경고를 stderr에 출력하고 기본값을 반환한다 (graceful degradation).
func analyzeCommits(ctx context.Context, gitRunner GitRunner) CommitAnalysis {
	result := CommitAnalysis{
		Types: append([]string(nil), DefaultCommitTypes...),
		Lang:  detectLangFromLocale(),
	}

	stdout, _, err := gitRunner.Run(ctx, "log", "--format=%s", "-200")
	if err != nil {
		// 커밋이 없는 새 repo에서는 git log가 실패함 — 정상 동작이므로 조용히 기본값 사용
		return result
	}

	lines := splitNonEmpty(string(stdout))
	if len(lines) == 0 {
		// 커밋 없음 — locale 기반 언어 사용
		return result
	}

	result.SampleSize = len(lines)

	// 각 메시지를 파싱하여 conventional commit 비율과 type 빈도를 계산
	var validCount int
	typeCounts := make(map[string]int)

	for _, line := range lines {
		msg := commitlint.Parse(line)
		if msg.HeaderValid {
			validCount++
			typeCounts[msg.Type]++
		}
	}

	result.ConventionalRatio = float64(validCount) / float64(len(lines))

	if result.ConventionalRatio >= 0.5 {
		result.Types = sortTypesByFrequency(typeCounts)
	}
	// 비율 < 0.5이면 이미 기본 type 목록이 설정되어 있음

	// 언어 감지: 커밋 메시지에서 CJK 문자 비율로 판단
	result.Lang = detectLangFromMessages(lines)

	return result
}

// sortTypesByFrequency는 type을 빈도 내림차순으로 정렬하여 반환한다.
func sortTypesByFrequency(counts map[string]int) []string {
	type kv struct {
		Key   string
		Count int
	}
	var pairs []kv
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count != pairs[j].Count {
			return pairs[i].Count > pairs[j].Count
		}
		return pairs[i].Key < pairs[j].Key // 동일 빈도 시 알파벳순
	})
	types := make([]string, len(pairs))
	for i, p := range pairs {
		types[i] = p.Key
	}
	return types
}

// detectLangFromMessages는 커밋 메시지에서 주요 언어를 감지한다.
func detectLangFromMessages(messages []string) string {
	var cjkCount, totalChars int
	for _, msg := range messages {
		for _, r := range msg {
			if unicode.IsLetter(r) {
				totalChars++
				if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hiragana, r) {
					cjkCount++
				}
			}
		}
	}
	if totalChars == 0 {
		return detectLangFromLocale()
	}
	ratio := float64(cjkCount) / float64(totalChars)
	if ratio > 0.1 {
		// CJK 문자가 10% 이상이면 세부 언어 판별
		var koCount, jaCount int
		for _, msg := range messages {
			for _, r := range msg {
				if unicode.Is(unicode.Hangul, r) {
					koCount++
				}
				if unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hiragana, r) {
					jaCount++
				}
			}
		}
		if koCount > jaCount {
			return "ko"
		}
		if jaCount > koCount {
			return "ja"
		}
		return "zh"
	}
	return "en"
}

// detectLangFromLocale는 시스템 locale에서 언어 코드를 추출한다.
func detectLangFromLocale() string {
	for _, env := range []string{"LC_ALL", "LANG", "LC_MESSAGES"} {
		val := os.Getenv(env)
		if val == "" || val == "C" || val == "POSIX" {
			continue
		}
		// "ko_KR.UTF-8" → "ko", "en_US.UTF-8" → "en"
		lang := strings.SplitN(val, "_", 2)[0]
		lang = strings.SplitN(lang, ".", 2)[0]
		if len(lang) >= 2 {
			return strings.ToLower(lang[:2])
		}
	}
	return "en"
}

// analyzeBranches는 git branch -a를 통해 branch 패턴, base branch, protected branch를 분석한다.
func analyzeBranches(ctx context.Context, gitRunner GitRunner) (baseBranch string, protected []string, patterns []string) {
	baseBranch = "main" // 기본값

	stdout, _, err := gitRunner.Run(ctx, "branch", "-a")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gk init: git branch failed: %v, using defaults\n", err)
		return baseBranch, nil, []string{DefaultBranchPattern}
	}

	branches := parseBranchOutput(string(stdout))
	if len(branches) == 0 {
		return baseBranch, nil, []string{DefaultBranchPattern}
	}

	// Base branch 감지: origin/HEAD → develop → main → master
	baseBranch = detectBaseBranch(branches)

	// Protected branch 감지
	protected = detectProtectedBranches(branches)

	// Branch 패턴 추출
	patterns = ExtractBranchPatterns(branches)

	return baseBranch, protected, patterns
}

// parseBranchOutput은 `git branch -a` 출력을 파싱하여 branch 이름 목록을 반환한다.
func parseBranchOutput(output string) []string {
	var branches []string
	seen := make(map[string]bool)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 현재 branch 표시 제거
		line = strings.TrimPrefix(line, "* ")

		// "remotes/origin/HEAD -> origin/main" 같은 참조 처리
		if strings.Contains(line, " -> ") {
			parts := strings.SplitN(line, " -> ", 2)
			// HEAD가 가리키는 대상도 추가
			ref := strings.TrimPrefix(parts[1], "remotes/")
			ref = strings.TrimPrefix(ref, "origin/")
			if !seen[ref] {
				seen[ref] = true
				branches = append(branches, ref)
			}
			// "HEAD" 자체는 건너뜀
			continue
		}

		// remotes/origin/ prefix 제거
		name := strings.TrimPrefix(line, "remotes/origin/")
		if !seen[name] {
			seen[name] = true
			branches = append(branches, name)
		}
	}
	return branches
}

// detectBaseBranch는 branch 목록에서 base branch를 감지한다.
// origin/HEAD → develop → main → master 순서.
func detectBaseBranch(branches []string) string {
	branchSet := make(map[string]bool)
	for _, b := range branches {
		branchSet[b] = true
	}

	// origin/HEAD가 가리키는 branch는 parseBranchOutput에서 이미 처리됨
	// 우선순위: develop → main → master
	for _, candidate := range []string{"develop", "main", "master"} {
		if branchSet[candidate] {
			return candidate
		}
	}
	return "main"
}

// detectProtectedBranches는 main, master, develop 중 실제 존재하는 branch를 반환한다.
func detectProtectedBranches(branches []string) []string {
	branchSet := make(map[string]bool)
	for _, b := range branches {
		branchSet[b] = true
	}

	var protected []string
	for _, candidate := range []string{"main", "master", "develop"} {
		if branchSet[candidate] {
			protected = append(protected, candidate)
		}
	}
	return protected
}

// ExtractBranchPatterns는 branch 이름에서 공통 prefix 패턴을 추출하여 정규식 목록을 반환한다.
// 테스트에서 직접 호출할 수 있도록 exported.
func ExtractBranchPatterns(branches []string) []string {
	prefixCounts := make(map[string]int)

	for _, b := range branches {
		idx := strings.Index(b, "/")
		if idx > 0 && idx < len(b)-1 {
			prefix := b[:idx]
			prefixCounts[prefix]++
		}
	}

	if len(prefixCounts) == 0 {
		return []string{DefaultBranchPattern}
	}

	// prefix를 빈도 내림차순으로 정렬
	type kv struct {
		Prefix string
		Count  int
	}
	var pairs []kv
	for k, v := range prefixCounts {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count != pairs[j].Count {
			return pairs[i].Count > pairs[j].Count
		}
		return pairs[i].Prefix < pairs[j].Prefix
	})

	// 추출된 prefix로 정규식 생성
	prefixes := make([]string, len(pairs))
	for i, p := range pairs {
		prefixes[i] = regexp.QuoteMeta(p.Prefix)
	}

	pattern := "^(" + strings.Join(prefixes, "|") + ")/[a-z0-9._-]+"
	return []string{pattern}
}

// splitNonEmpty는 문자열을 줄 단위로 분리하고 빈 줄을 제거한다.
func splitNonEmpty(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
