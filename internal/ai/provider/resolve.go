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

type conflictResolverAvailability interface {
	ConflictResolverAvailable(ctx context.Context) error
}

// ConflictResolverAvailable reports whether p can resolve conflicts now. A
// fallback chain may contain providers that implement different optional
// capabilities, so callers should use this helper instead of only checking
// Provider.Available.
func ConflictResolverAvailable(ctx context.Context, p Provider) error {
	if p == nil {
		return fmt.Errorf("no provider configured")
	}
	if checker, ok := p.(conflictResolverAvailability); ok {
		return checker.ConflictResolverAvailable(ctx)
	}
	if _, ok := p.(ConflictResolver); !ok {
		return fmt.Errorf("provider %q does not support conflict resolution", p.Name())
	}
	return p.Available(ctx)
}

// ConflictHunkInput은 AI에 전달할 하나의 충돌 영역 정보이다.
type ConflictHunkInput struct {
	Index         int      `json:"index"`
	Ours          []string `json:"ours"`
	Theirs        []string `json:"theirs"`
	Base          []string `json:"base,omitempty"`
	ContextBefore []string `json:"context_before,omitempty"`
	ContextAfter  []string `json:"context_after,omitempty"`
	// Delete/modify 충돌: 한쪽 stage가 통째로 없다 — 그쪽은 파일 자체를
	// 삭제했다. 이 플래그가 켜진 쪽을 고르는 것은 "파일을 지운다"는 뜻이다.
	OursDeleted   bool `json:"ours_deleted,omitempty"`
	TheirsDeleted bool `json:"theirs_deleted,omitempty"`
}

// ConflictResolutionInput은 ConflictResolver.ResolveConflicts의 입력이다.
type ConflictResolutionInput struct {
	FilePath      string              `json:"file_path"`
	Hunks         []ConflictHunkInput `json:"hunks"`
	OperationType string              `json:"operation_type"` // "merge", "rebase", "cherry-pick"
	Lang          string              `json:"lang"`
}

// ConflictResolutionOutput은 하나의 충돌 영역에 대한 AI 해결 제안이다.
type ConflictResolutionOutput struct {
	Index     int      `json:"index"`
	Strategy  string   `json:"strategy"`  // "ours", "theirs", "merged"
	Resolved  []string `json:"resolved"`  // 해결된 코드 라인
	Rationale string   `json:"rationale"` // 선택 근거 (최대 120자)
	// Confidence는 이 해결에 대한 모델의 확신도(0.0~1.0). resolve.min_confidence
	// 게이트가 이 값 미만의 hunk를 적용하지 않고 제안으로만 남긴다. 0은
	// "미보고"로 취급된다(구형 응답 하위호환).
	Confidence float64 `json:"confidence"`
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
- For each input conflict hunk, provide exactly 1 selected resolution.
- The output index must match the input hunk index; do not omit or duplicate indexes.
- Strategy choices:
  "ours" — keep the local changes
  "theirs" — accept the incoming changes
  "merged" — combine both changes into a coherent result
- For "merged", produce code that preserves the intent of both sides.
- If both sides are semantically incompatible and cannot be merged,
  choose "ours" or "theirs" and explain why in the rationale.
- Provide a one-line rationale (max 120 chars) for each resolution.
- Provide a "confidence" between 0.0 and 1.0 for each resolution: how sure
  you are the resolution preserves both sides' intent. Be honest — a merged
  resolution of semantically entangled edits deserves a LOW confidence.
- The "resolved" field must contain the exact lines of code (no markers).
- Preserve indentation and formatting of the original code.
- A hunk may carry "ours_deleted" or "theirs_deleted": that side deleted
  the whole file (delete/modify conflict). Choosing the deleted side
  means deleting the file — set "resolved" to []. Weigh whether the
  deletion or the surviving modification expresses the newer intent for
  the given operation_type; explain the choice in the rationale.`

// buildConflictResolutionUserPrompt는 ConflictResolutionInput을 user prompt 문자열로 변환한다.
func buildConflictResolutionUserPrompt(in ConflictResolutionInput) string {
	data, _ := json.Marshal(in)

	var sb strings.Builder
	sb.WriteString("Analyze the following git merge conflicts and suggest resolutions.\n\n")
	sb.WriteString("Input:\n")
	sb.Write(data)
	sb.WriteString("\n\nRespond with JSON matching this schema:\n")
	sb.WriteString(`{"resolutions":[{"index":<int>,"strategy":"<ours|theirs|merged>","resolved":["<line>",...],"rationale":"<max 120 chars>","confidence":<0.0-1.0>}]}`)
	sb.WriteString("\nReturn one resolution per input hunk, with the same index and no duplicates.\n")
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
			return ConflictResolutionResult{}, fmt.Errorf("%w: invalid conflict strategy %q", ErrProviderResponse, r.Strategy)
		}
		if r.Confidence < 0 {
			r.Confidence = 0
		} else if r.Confidence > 1 {
			r.Confidence = 1
		}
		if len([]rune(r.Rationale)) > 120 {
			r.Rationale = string([]rune(r.Rationale)[:120])
		}
	}

	return result, nil
}
