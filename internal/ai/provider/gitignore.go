package provider

import (
	"context"
	"strings"
)

// GitignoreSuggester는 AI를 통해 프로젝트에 맞는 gitignore 패턴을 제안하는
// optional capability이다. Summarizer와 동일한 패턴으로, 타입 assertion으로 감지한다.
type GitignoreSuggester interface {
	SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error)
}

const gitignoreSystemPrompt = `You are a senior developer embedded in the "gk" CLI.
Your task is to suggest .gitignore patterns for a project based on its structure.

Rules:
- Output ONLY gitignore patterns, one per line.
- No comments, no explanations, no markdown fences.
- Only suggest patterns that are NOT already standard for the detected languages.
- Focus on: build artifacts, cache directories, local config, generated files,
  dependency locks that shouldn't be committed, OS-specific files.
- Do NOT suggest patterns for: .env, .idea/, .vscode/, node_modules/, __pycache__/,
  target/, bin/ — these are already handled by the standard rules.
- Be conservative: only suggest patterns you are confident about.
- If nothing extra is needed, output nothing.`

const gitignoreUserPromptPrefix = `Analyze this project and suggest additional .gitignore patterns
that are specific to this project but NOT covered by standard language/IDE/security rules.

`

// parseGitignoreLines는 AI 응답에서 gitignore 패턴을 추출한다.
// 모든 provider adapter에서 공통으로 사용한다. CLI 어댑터(gemini/qwen/
// kiro)는 stdout을 색칠해서 보내기도 하므로 ANSI escape와 흔한 마크다운
// 장식("- ", "* ", "> ")은 라인 단위로 정리한 뒤 받아들인다.
func parseGitignoreLines(content string) []string {
	content = stripANSI(content)
	var patterns []string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(stripANSI(raw))
		// Trim common markdown bullet / quote prefixes the model
		// sometimes adds (e.g. "- node_modules/", "> node_modules/").
		line = strings.TrimLeft(line, "-*•> \t")
		line = strings.TrimSpace(line)
		// Drop fenced code blocks, comments, and any leftover ANSI
		// remnants that survived the strip pass.
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") {
			continue
		}
		// A pattern with whitespace inside is almost always prose
		// ("AI-suggested entries:") rather than a real glob.
		if strings.ContainsAny(line, " \t") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}
