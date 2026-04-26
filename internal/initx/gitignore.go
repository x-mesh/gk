package initx

import "strings"

// 언어별 ignore 패턴 매핑 테이블.
var langIgnorePatterns = map[string][]string{
	"go":     {"bin/", "*.test", "*.out", "coverage.out"},
	"node":   {"node_modules/", "dist/", ".next/", "*.tsbuildinfo"},
	"python": {"__pycache__/", "*.pyc", ".venv/", "*.egg-info/"},
	"rust":   {"target/"},
	"java":   {"*.class", "build/", ".gradle/", "target/"},
	"ruby":   {"vendor/bundle/", ".bundle/"},
	"php":    {"vendor/", ".phpunit.result.cache"},
}

// langDisplayName은 내부 언어 키 → 표시 이름 매핑이다.
var langDisplayName = map[string]string{
	"go":     "Go",
	"node":   "Node.js",
	"python": "Python",
	"rust":   "Rust",
	"java":   "Java",
	"ruby":   "Ruby",
	"php":    "PHP",
}

// SecurityPatterns는 공통 보안 관련 ignore 패턴이다.
var SecurityPatterns = []string{
	".env",
	".env.*",
	"*.pem",
	"id_rsa*",
	"credentials.json",
	"*.pfx",
	"*.kdbx",
	"*.keystore",
	"service-account*.json",
}

// IDEPatterns는 공통 IDE/에디터 ignore 패턴이다.
var IDEPatterns = []string{
	".idea/",
	".vscode/",
	".cursor/",
	".kiro/",
	".xm/",
	".omc/",
	"*.swp",
	".DS_Store",
	"Thumbs.db",
}

// GenerateGitignore는 분석 결과를 기반으로 .gitignore 내용을 생성한다.
// 카테고리별 주석 헤더와 함께 언어별 패턴, 보안 패턴, IDE 패턴을 포함한다.
func GenerateGitignore(result *AnalysisResult) string {
	var b strings.Builder

	// 언어별 패턴 (감지된 순서대로)
	for i, lang := range result.Languages {
		if i > 0 {
			b.WriteByte('\n')
		}
		display := langDisplayName[lang.Name]
		if display == "" {
			display = lang.Name
		}
		b.WriteString("# Language: ")
		b.WriteString(display)
		b.WriteByte('\n')
		for _, pat := range langIgnorePatterns[lang.Name] {
			b.WriteString(pat)
			b.WriteByte('\n')
		}
	}

	// 보안 패턴 (항상 포함)
	if len(result.Languages) > 0 {
		b.WriteByte('\n')
	}
	b.WriteString("# Security\n")
	for _, pat := range SecurityPatterns {
		b.WriteString(pat)
		b.WriteByte('\n')
	}

	// IDE/에디터 패턴 (항상 포함)
	b.WriteByte('\n')
	b.WriteString("# IDE/Editor\n")
	for _, pat := range IDEPatterns {
		b.WriteString(pat)
		b.WriteByte('\n')
	}

	return b.String()
}

// ParseGitignore는 .gitignore 파일을 파싱하여 패턴 목록을 반환한다.
// 주석과 빈 줄은 제외한다.
func ParseGitignore(content string) []string {
	var patterns []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// MergeGitignore는 기존 .gitignore에 새 규칙을 병합한다.
// 이미 존재하는 패턴은 건너뛰고, 추가된 규칙만 반환한다.
// 기존 규칙의 삭제/순서 변경은 하지 않는다.
func MergeGitignore(existing string, generated string) (merged string, added []string) {
	existingPatterns := make(map[string]bool)
	for _, p := range ParseGitignore(existing) {
		existingPatterns[p] = true
	}

	// generated를 줄 단위로 순회하며 새 패턴만 수집 (카테고리 헤더 포함)
	var newSections strings.Builder
	var currentHeader string
	headerWritten := false

	for _, line := range strings.Split(generated, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "#") {
			// 새 카테고리 헤더 — 아직 쓰지 않고 기억만
			currentHeader = trimmed
			headerWritten = false
			continue
		}
		if trimmed == "" {
			continue
		}

		// 기존에 없는 패턴만 추가
		if existingPatterns[trimmed] {
			continue
		}

		// 이 카테고리의 첫 새 패턴이면 헤더 먼저 출력
		if !headerWritten && currentHeader != "" {
			if newSections.Len() > 0 {
				newSections.WriteByte('\n')
			}
			newSections.WriteString(currentHeader)
			newSections.WriteByte('\n')
			headerWritten = true
		}
		newSections.WriteString(trimmed)
		newSections.WriteByte('\n')
		added = append(added, trimmed)
	}

	if len(added) == 0 {
		return existing, nil
	}

	// 기존 내용 끝에 새 섹션 추가
	base := existing
	if !strings.HasSuffix(base, "\n") && base != "" {
		base += "\n"
	}
	base += "\n"
	merged = base + newSections.String()
	return merged, added
}
