package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ConflictResolver는 AI를 통해 충돌을 분석하고 해결 대안을 제안하는
// optional capability이다. BranchAnalyzer, Summarizer와 동일한 패턴으로,
// 타입 assertion으로 감지한다.
//
//	if resolver, ok := p.(ConflictResolver); ok { ... }
type ConflictResolver interface {
	ResolveConflicts(ctx context.Context, in ConflictResolutionInput) (ConflictResolutionResult, error)
}

// ConflictHunkInput은 AI에 전달할 하나의 충돌 영역 정보이다.
type ConflictHunkInput struct {
	Index         int      `json:"index"`
	Ours          []string `json:"ours"`
	Theirs        []string `json:"theirs"`
	Base          []string `json:"base,omitempty"`
	ContextBefore []string `json:"context_before,omitempty"`
	ContextAfter  []string `json:"context_after,omitempty"`
}

// ConflictResolutionInput은 ConflictResolver.ResolveConflicts의 입력이다.
type ConflictResolutionInput struct {
	FilePath      string             `json:"file_path"`
	Hunks         []ConflictHunkInput `json:"hunks"`
	OperationType string             `json:"operation_type"` // "merge", "rebase", "cherry-pick"
	Lang          string             `json:"lang"`
}

// ConflictResolutionOutput은 하나의 충돌 영역에 대한 AI 해결 제안이다.
type ConflictResolutionOutput struct {
	Index     int      `json:"index"`
	Strategy  string   `json:"strategy"`  // "ours", "theirs", "merged"
	Resolved  []string `json:"resolved"`  // 해결된 코드 라인
	Rationale string   `json:"rationale"` // 선택 근거 (최대 120자)
}

// ConflictResolutionResult는 ConflictResolver.ResolveConflicts의 출력이다.
type ConflictResolutionResult struct {
	Resolutions []ConflictResolutionOutput `json:"resolutions"`
	Model       string                     `json:"model"`
	TokensUsed  int                        `json:"tokens_used"`
}

const conflictResolutionSystemPrompt = `You are a git conflict resolution advisor embedded in the "gk" CLI.
Your task is to analyze git merge conflicts and suggest resolutions.

Rules:
- Output ONLY valid JSON matching the schema in the user message; no prose,
  no Markdown fences, no explanations.
- For each conflict hunk, provide exactly 3 resolutions:
  "ours" — keep the local changes
  "theirs" — accept the remote changes
  "merged" — combine both changes into a coherent result
- For "merged" resolution, produce code that preserves the intent of both sides.
- If both sides are semantically incompatible and cannot be merged,
  set the merged rationale to explain why and recommend "ours" or "theirs".
- Provide a one-line rationale (max 120 chars) for each resolution.
- The "resolved" field must contain the exact lines of code (no markers).
- Preserve indentation and formatting of the original code.`

// buildConflictResolutionUserPrompt는 ConflictResolutionInput을 user prompt 문자열로 변환한다.
func buildConflictResolutionUserPrompt(in ConflictResolutionInput) string {
	data, _ := json.Marshal(in)

	var sb strings.Builder
	sb.WriteString("Analyze the following git merge conflicts and suggest resolutions.\n\n")
	sb.WriteString("Input:\n")
	sb.Write(data)
	sb.WriteString("\n\nRespond with JSON matching this schema:\n")
	sb.WriteString(`{"resolutions":[{"index":<int>,"strategy":"<ours|theirs|merged>","resolved":["<line>",...],"rationale":"<max 120 chars>"}]}`)
	sb.WriteString("\n")
	return sb.String()
}

// parseConflictResolutionResponse는 AI 응답 raw bytes를 ConflictResolutionResult로 파싱한다.
func parseConflictResolutionResponse(raw []byte) (ConflictResolutionResult, error) {
	trimmed := stripFences(strings.TrimSpace(string(raw)))

	var result ConflictResolutionResult
	if err := tryJSONDecode(trimmed, &result); err != nil {
		return ConflictResolutionResult{}, fmt.Errorf("%w: %v", ErrProviderResponse, err)
	}

	validStrategies := map[string]bool{
		"ours":   true,
		"theirs": true,
		"merged": true,
	}

	for i := range result.Resolutions {
		r := &result.Resolutions[i]
		if !validStrategies[r.Strategy] {
			r.Strategy = "ours" // 안전한 기본값
		}
		if len([]rune(r.Rationale)) > 120 {
			r.Rationale = string([]rune(r.Rationale)[:120])
		}
	}

	return result, nil
}
