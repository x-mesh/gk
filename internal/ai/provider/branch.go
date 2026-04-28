package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// BranchAnalyzer는 AI를 통해 브랜치를 분석하는 optional capability이다.
// GitignoreSuggester, Summarizer와 동일한 패턴으로, 타입 assertion으로 감지한다.
//
//	if analyzer, ok := p.(BranchAnalyzer); ok { ... }
type BranchAnalyzer interface {
	AnalyzeBranches(ctx context.Context, in BranchAnalysisInput) (BranchAnalysisResult, error)
}

// BranchInfo는 분석 대상 브랜치 하나의 정보이다.
type BranchInfo struct {
	Name           string    `json:"name"`
	LastCommitMsg  string    `json:"last_commit_msg"`
	DiffStat       string    `json:"diff_stat"`
	LastCommitDate time.Time `json:"last_commit_date"`
	Status         string    `json:"status"` // "merged", "gone", "stale", "squash-merged", "ambiguous"
}

// BranchAnalysisInput은 BranchAnalyzer.AnalyzeBranches의 입력이다.
type BranchAnalysisInput struct {
	Branches   []BranchInfo `json:"branches"`
	BaseBranch string       `json:"base_branch"`
	Lang       string       `json:"lang"`
}

// BranchAnalysis는 브랜치 하나의 AI 분석 결과이다.
type BranchAnalysis struct {
	Name       string `json:"name"`
	Category   string `json:"category"` // "completed", "experiment", "in_progress", "preserve"
	Summary    string `json:"summary"`  // 최대 80자
	SafeDelete bool   `json:"safe_delete"`
}

// BranchAnalysisResult는 BranchAnalyzer.AnalyzeBranches의 출력이다.
type BranchAnalysisResult struct {
	Analyses   []BranchAnalysis `json:"analyses"`
	Model      string           `json:"model"`
	TokensUsed int              `json:"tokens_used"`
}

const branchAnalysisSystemPrompt = `You are a branch cleanup advisor embedded in the "gk" CLI.
Your task is to analyze git branches and classify them for cleanup.

Rules:
- Output ONLY valid JSON matching the schema in the user message; no prose,
  no Markdown fences, no explanations.
- Classify each branch into exactly one category:
  "completed" — PR merged or work finished, safe to delete
  "experiment" — exploratory changes, low preservation value
  "in_progress" — active development, do NOT delete
  "preserve" — important unmerged changes, do NOT delete
- Provide a one-line summary (max 80 chars) describing the branch's changes.
- Set safe_delete=true for "completed" and "experiment", false otherwise.
- Consider branch name patterns, commit messages, and diff stats.
- When status is "ambiguous", analyze the diff to determine if it was squash-merged.`

// buildBranchAnalysisUserPrompt는 BranchAnalysisInput을 user prompt 문자열로 변환한다.
func buildBranchAnalysisUserPrompt(in BranchAnalysisInput) string {
	data, _ := json.Marshal(in)

	var sb strings.Builder
	sb.WriteString("Analyze the following branches and classify each one.\n\n")
	sb.WriteString("Input:\n")
	sb.Write(data)
	sb.WriteString("\n\nRespond with JSON matching this schema:\n")
	sb.WriteString(`{"analyses":[{"name":"<branch>","category":"<completed|experiment|in_progress|preserve>","summary":"<max 80 chars>","safe_delete":<bool>}]}`)
	sb.WriteString("\n")
	return sb.String()
}

// parseBranchAnalysisResponse는 AI 응답 raw bytes를 BranchAnalysisResult로 파싱한다.
func parseBranchAnalysisResponse(raw []byte) (BranchAnalysisResult, error) {
	trimmed := stripFences(strings.TrimSpace(string(raw)))

	var result BranchAnalysisResult
	if err := tryJSONDecode(trimmed, &result); err != nil {
		return BranchAnalysisResult{}, fmt.Errorf("%w: %v", ErrProviderResponse, err)
	}

	validCategories := map[string]bool{
		"completed":   true,
		"experiment":  true,
		"in_progress": true,
		"preserve":    true,
	}

	for i := range result.Analyses {
		a := &result.Analyses[i]
		if !validCategories[a.Category] {
			a.Category = "preserve" // 안전한 기본값
		}
		if len([]rune(a.Summary)) > 80 {
			a.Summary = string([]rune(a.Summary)[:80])
		}
	}

	return result, nil
}
