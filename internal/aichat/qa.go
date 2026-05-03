package aichat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// QAEngine answers git/gk questions using repository context.
// It uses the Summarizer interface to communicate with AI providers
// and RepoContextCollector for repository state.
type QAEngine struct {
	Summarizer provider.Summarizer
	Context    *RepoContextCollector
	Lang       string
	Timeout    time.Duration
	Dbg        func(string, ...any)
}

// dbg is a helper that calls q.Dbg if non-nil.
func (q *QAEngine) dbg(format string, args ...any) {
	if q.Dbg != nil {
		q.Dbg(format, args...)
	}
}

// isNonGitQuestion returns true when the question is clearly unrelated
// to git or gk. The check is intentionally conservative — when in doubt
// the question is forwarded to the AI provider.
func isNonGitQuestion(question string) bool {
	lower := strings.ToLower(question)

	// Keywords that strongly indicate a non-git question.
	nonGitKeywords := []string{
		"weather", "recipe", "cook", "capital of",
		"president", "movie", "song", "lyrics",
		"stock price", "bitcoin", "crypto",
		"translate", "dictionary",
		"날씨", "요리", "레시피", "수도는", "대통령",
		"영화", "노래", "가사", "주가", "번역",
	}

	for _, kw := range nonGitKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// nonGitGuidance returns a guidance message when the question is not
// about git or gk.
func (q *QAEngine) nonGitGuidance() string {
	lang := q.Lang
	if lang == "" {
		lang = "en"
	}

	if strings.HasPrefix(lang, "ko") {
		return `이 질문은 git/gk와 관련이 없는 것 같습니다.

gk ask는 git 및 gk CLI 관련 질문만 지원합니다. 예시:

• "현재 브랜치에서 main으로 어떻게 머지하나요?"
• "마지막 커밋을 취소하려면?"
• "rebase와 merge의 차이점은?"
• "gk sync는 어떻게 동작하나요?"

팁: gk do "원하는 작업" 으로 자연어 명령어 실행도 가능합니다.`
	}

	return `This question doesn't seem to be related to git/gk.

gk ask only supports git and gk CLI related questions. Examples:

• "How do I merge my current branch into main?"
• "How do I undo my last commit?"
• "What's the difference between rebase and merge?"
• "How does gk sync work?"

Tip: You can also use gk do "your task" to run natural language commands.`
}

// emptyQuestionGuidance returns a guidance message when the question is
// empty or whitespace-only.
func (q *QAEngine) emptyQuestionGuidance() string {
	lang := q.Lang
	if lang == "" {
		lang = "en"
	}

	if strings.HasPrefix(lang, "ko") {
		return `질문이 비어있습니다. git/gk 관련 질문을 입력해 주세요. 예시:

• "현재 브랜치에서 main으로 어떻게 머지하나요?"
• "마지막 커밋을 취소하려면?"
• "rebase와 merge의 차이점은?"

팁: gk ask "질문" 형식으로 사용하세요.`
	}

	return `No question provided. Please ask a git/gk related question. Examples:

• "How do I merge my current branch into main?"
• "How do I undo my last commit?"
• "What's the difference between rebase and merge?"

Tip: Use gk ask "your question" to ask a question.`
}

// Answer analyses the question and returns a context-aware answer using
// the repository state.
//
// If the question is empty, it returns a guidance message.
// If the question is not about git/gk, it returns a non-git guidance message.
func (q *QAEngine) Answer(ctx context.Context, question string) (string, error) {
	// Empty question → return guidance.
	if strings.TrimSpace(question) == "" {
		return q.emptyQuestionGuidance(), nil
	}

	// Non-git question → return guidance.
	if isNonGitQuestion(question) {
		return q.nonGitGuidance(), nil
	}

	// Apply timeout if configured.
	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}

	// 1. Collect repository context (with branch list for Q&A).
	var repoCtx *RepoContext
	if q.Context != nil {
		repoCtx = q.Context.CollectForQuestion(ctx, question)
	}

	// 2. Build the user prompt.
	userPrompt := buildAskUserPrompt(question, repoCtx, q.Lang)

	// 3. Call the AI provider via Summarizer.
	result, err := q.Summarizer.Summarize(ctx, provider.SummarizeInput{
		Kind: "ask",
		Diff: userPrompt,
		Lang: q.Lang,
	})
	if err != nil {
		return "", fmt.Errorf("ask: AI provider error: %w", err)
	}

	return result.Text, nil
}
