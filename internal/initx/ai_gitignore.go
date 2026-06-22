package initx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AIGitignoreProvider는 AI를 통해 gitignore 패턴을 제안하는 인터페이스이다.
// provider.Summarizer와 유사한 패턴으로, 시스템 프롬프트 + 유저 프롬프트를 받아
// free-form text를 반환한다.
type AIGitignoreProvider interface {
	SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error)
}

// SuggestGitignorePatterns는 AI provider를 사용하여 프로젝트에 맞는
// 추가 gitignore 패턴을 제안한다.
// provider가 nil이면 빈 목록을 반환한다.
func SuggestGitignorePatterns(ctx context.Context, provider AIGitignoreProvider, dir string, result *AnalysisResult) []string {
	if provider == nil {
		return nil
	}

	info := buildProjectInfo(dir, result)
	patterns, err := provider.SuggestGitignore(ctx, info)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gk init: ai gitignore suggestion failed: %v\n", err)
		return nil
	}
	return patterns
}

// buildProjectInfo는 AI에게 보낼 프로젝트 정보 문자열을 구성한다.
func buildProjectInfo(dir string, result *AnalysisResult) string {
	var b strings.Builder

	// 감지된 언어
	if len(result.Languages) > 0 {
		b.WriteString("Detected languages: ")
		for i, l := range result.Languages {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(l.Name)
		}
		b.WriteByte('\n')
	}

	// 빌드 시스템
	if len(result.BuildSystems) > 0 {
		b.WriteString("Build systems: ")
		for i, bs := range result.BuildSystems {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(bs.Name)
		}
		b.WriteByte('\n')
	}

	// 프로젝트 루트의 파일/디렉토리 목록 (1 depth)
	b.WriteString("\nTop-level files and directories:\n")
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".git") && name != ".gitignore" {
				continue // .git 디렉토리는 제외
			}
			if !shareableProjectInfoName(name) {
				continue
			}
			if e.IsDir() {
				b.WriteString("  " + name + "/\n")
			} else {
				b.WriteString("  " + name + "\n")
			}
		}
	}

	// 주요 config 파일 내용 힌트
	for _, f := range []string{"package.json", "go.mod", "pyproject.toml", "Cargo.toml"} {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		content := string(data)
		// 너무 길면 잘라냄
		if len(content) > 500 {
			content = content[:500] + "\n... (truncated)"
		}
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", f, content)
	}

	return b.String()
}

// FormatAISuggestedSection은 AI가 제안한 패턴을 gitignore 섹션 문자열로 포맷한다.
func FormatAISuggestedSection(patterns []string) string {
	patterns = CleanAISuggestedPatterns(patterns)
	if len(patterns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# AI-suggested\n")
	for _, p := range patterns {
		b.WriteString(p)
		b.WriteByte('\n')
	}
	return b.String()
}

// CleanAISuggestedPatterns keeps only conservative, single-line gitignore
// patterns. AI output is advisory and must not be able to unignore files or add
// catch-all rules such as "*".
func CleanAISuggestedPatterns(patterns []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		p = strings.TrimLeft(p, "-*•> \t")
		p = strings.TrimSpace(p)
		if !safeAIGitignorePattern(p) || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func safeAIGitignorePattern(p string) bool {
	if p == "" || strings.HasPrefix(p, "#") || strings.HasPrefix(p, "```") {
		return false
	}
	if strings.HasPrefix(p, "!") {
		return false
	}
	if strings.ContainsAny(p, "\r\n\t ") {
		return false
	}
	if p == "*" || p == "/*" || p == "/" || p == "." || p == ".." {
		return false
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return false
	}
	return true
}

func shareableProjectInfoName(name string) bool {
	for _, pat := range SecurityPatterns {
		if gitignoreNameMatch(pat, name) {
			return false
		}
	}
	return true
}

func gitignoreNameMatch(pattern, name string) bool {
	p := strings.TrimSuffix(pattern, "/")
	if p == "" {
		return false
	}
	if ok, _ := filepath.Match(p, name); ok {
		return true
	}
	return p == name
}
