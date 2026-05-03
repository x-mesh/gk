package aichat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// ErrorAnalyzer diagnoses git error messages and explains the most recent
// git/gk command. It uses the Summarizer interface to communicate with
// AI providers and RepoContextCollector for repository state.
type ErrorAnalyzer struct {
	Summarizer provider.Summarizer
	Context    *RepoContextCollector
	Lang       string
	Timeout    time.Duration
	Dbg        func(string, ...any)
}

// emptyErrorGuidance returns a guidance message when the error message is
// empty or unrecognizable. It lists common git error types and suggests
// using `gk explain --last`.
func (a *ErrorAnalyzer) emptyErrorGuidance() string {
	lang := a.Lang
	if lang == "" {
		lang = "en"
	}

	if strings.HasPrefix(lang, "ko") {
		return `에러 메시지가 비어있습니다. 일반적인 git 에러 유형:

• merge conflict — 병합 충돌
• detached HEAD — HEAD가 브랜치에 연결되지 않음
• rejected push — 원격 저장소가 push를 거부
• untracked files — 추적되지 않는 파일 충돌
• permission denied — 권한 부족
• diverged branches — 브랜치가 분기됨

팁: 마지막으로 실행한 명령어를 설명하려면 gk explain --last 를 사용하세요.`
	}

	return `No error message provided. Common git error types:

• merge conflict — conflicting changes in the same file
• detached HEAD — HEAD is not attached to any branch
• rejected push — remote rejected the push
• untracked files — untracked file conflicts
• permission denied — insufficient permissions
• diverged branches — local and remote have diverged

Tip: To explain your last command, use gk explain --last`
}

// noReflogNotice returns a notice when the last command cannot be identified
// from the reflog. It provides a reflog-based explanation with an accuracy
// limitation disclaimer.
func (a *ErrorAnalyzer) noReflogNotice() string {
	lang := a.Lang
	if lang == "" {
		lang = "en"
	}

	if strings.HasPrefix(lang, "ko") {
		return `최근 reflog 항목을 찾을 수 없습니다.

reflog가 비어있어 마지막 명령어를 정확히 식별할 수 없습니다.
이 저장소가 새로 생성되었거나 reflog가 만료되었을 수 있습니다.

참고: reflog 기반 설명은 정확도가 제한적일 수 있습니다.`
	}

	return `No recent reflog entries found.

The reflog is empty, so the last command cannot be accurately identified.
This repository may be newly created or the reflog may have expired.

Note: Reflog-based explanations may have limited accuracy.`
}

// DiagnoseError analyses an error message and returns a structured
// explanation with Cause, Solution, and Prevention sections.
//
// If errorMsg is empty, it returns a guidance message listing common
// git error types and suggesting `gk explain --last`.
func (a *ErrorAnalyzer) DiagnoseError(ctx context.Context, errorMsg string) (string, error) {
	// Empty error message → return guidance.
	if strings.TrimSpace(errorMsg) == "" {
		return a.emptyErrorGuidance(), nil
	}

	// Apply timeout if configured.
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}

	// 1. Collect repository context.
	var repoCtx *RepoContext
	if a.Context != nil {
		repoCtx = a.Context.Collect(ctx)
	}

	// 2. Build the user prompt.
	userPrompt := buildExplainUserPrompt(errorMsg, repoCtx, a.Lang)

	// 3. Call the AI provider via Summarizer.
	result, err := a.Summarizer.Summarize(ctx, provider.SummarizeInput{
		Kind: "explain",
		Diff: userPrompt,
		Lang: a.Lang,
	})
	if err != nil {
		return "", fmt.Errorf("explain: AI provider error: %w", err)
	}

	return result.Text, nil
}

// ExplainLast identifies the most recent git/gk command from the reflog
// and returns a step-by-step explanation of what it did internally.
//
// If the reflog is empty (last command unidentifiable), it returns a
// notice about accuracy limitations.
func (a *ErrorAnalyzer) ExplainLast(ctx context.Context) (string, error) {
	// Apply timeout if configured.
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}

	// 1. Collect repository context (includes reflog).
	var repoCtx *RepoContext
	if a.Context != nil {
		repoCtx = a.Context.Collect(ctx)
	}

	// If no reflog entries, return accuracy limitation notice.
	if repoCtx == nil || len(repoCtx.RecentReflog) == 0 {
		return a.noReflogNotice(), nil
	}

	// 2. Build the user prompt for --last mode.
	userPrompt := buildExplainLastUserPrompt(repoCtx, a.Lang)

	// 3. Call the AI provider via Summarizer.
	result, err := a.Summarizer.Summarize(ctx, provider.SummarizeInput{
		Kind: "explain-last",
		Diff: userPrompt,
		Lang: a.Lang,
	})
	if err != nil {
		return "", fmt.Errorf("explain-last: AI provider error: %w", err)
	}

	return result.Text, nil
}
