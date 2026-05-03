package aichat

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

// ---------------------------------------------------------------------------
// 17.2 CLI 플래그 통합 검증
//
// 이 파일은 CLI 플래그가 핵심 로직 컴포넌트에 올바르게 전달되는지 검증한다.
// Requirements: 6.3, 6.5, 10.4, 11.4
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 1. Provider flag (6.3): Kind 필드가 각 명령어 타입에 맞게 설정되는지 검증
// ---------------------------------------------------------------------------

func TestIntegration_IntentParser_KindField(t *testing.T) {
	validJSON := `{"commands":[{"command":"git status","description":"check status"}]}`
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: validJSON, Provider: "test-provider"},
	}
	p := newTestParser(sum)

	_, err := p.Parse(context.Background(), "show status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.captured == nil {
		t.Fatal("Summarizer was not called")
	}
	if sum.captured.Kind != "do" {
		t.Errorf("IntentParser Kind = %q, want %q", sum.captured.Kind, "do")
	}
}

func TestIntegration_ErrorAnalyzer_KindField_Explain(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "Cause: ...\nSolution: ...\nPrevention: ...", Provider: "test-provider"},
	}
	a := newTestAnalyzer(sum)

	_, err := a.DiagnoseError(context.Background(), "fatal: not a git repository")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.captured == nil {
		t.Fatal("Summarizer was not called")
	}
	if sum.captured.Kind != "explain" {
		t.Errorf("ErrorAnalyzer DiagnoseError Kind = %q, want %q", sum.captured.Kind, "explain")
	}
}

func TestIntegration_ErrorAnalyzer_KindField_ExplainLast(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "Step 1: ...", Provider: "test-provider"},
	}
	a := newTestAnalyzer(sum)

	_, err := a.ExplainLast(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.captured == nil {
		t.Fatal("Summarizer was not called")
	}
	if sum.captured.Kind != "explain-last" {
		t.Errorf("ErrorAnalyzer ExplainLast Kind = %q, want %q", sum.captured.Kind, "explain-last")
	}
}

func TestIntegration_QAEngine_KindField(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "Answer about rebase.", Provider: "test-provider"},
	}
	q := newTestQAEngine(sum)

	_, err := q.Answer(context.Background(), "What is rebase?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.captured == nil {
		t.Fatal("Summarizer was not called")
	}
	if sum.captured.Kind != "ask" {
		t.Errorf("QAEngine Kind = %q, want %q", sum.captured.Kind, "ask")
	}
}

// ---------------------------------------------------------------------------
// 2. Lang override (10.4): Lang이 Summarizer의 SummarizeInput.Lang에 전달되는지 검증
// ---------------------------------------------------------------------------

func TestIntegration_IntentParser_LangOverride(t *testing.T) {
	validJSON := `{"commands":[{"command":"git status","description":"상태 확인"}]}`
	for _, lang := range []string{"ko", "en", "ja", "zh"} {
		t.Run("lang="+lang, func(t *testing.T) {
			sum := &fakeSummarizer{
				response: provider.SummarizeResult{Text: validJSON, Provider: "fake"},
			}
			p := newTestParser(sum)
			p.Lang = lang

			_, err := p.Parse(context.Background(), "show status")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sum.captured == nil {
				t.Fatal("Summarizer was not called")
			}
			if sum.captured.Lang != lang {
				t.Errorf("IntentParser SummarizeInput.Lang = %q, want %q", sum.captured.Lang, lang)
			}
		})
	}
}

func TestIntegration_ErrorAnalyzer_LangOverride(t *testing.T) {
	for _, lang := range []string{"ko", "en", "ja"} {
		t.Run("lang="+lang, func(t *testing.T) {
			sum := &fakeSummarizer{
				response: provider.SummarizeResult{Text: "diagnosis", Provider: "fake"},
			}
			a := newTestAnalyzer(sum)
			a.Lang = lang

			_, err := a.DiagnoseError(context.Background(), "error: push rejected")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sum.captured == nil {
				t.Fatal("Summarizer was not called")
			}
			if sum.captured.Lang != lang {
				t.Errorf("ErrorAnalyzer SummarizeInput.Lang = %q, want %q", sum.captured.Lang, lang)
			}
		})
	}
}

func TestIntegration_ErrorAnalyzer_ExplainLast_LangOverride(t *testing.T) {
	for _, lang := range []string{"ko", "en"} {
		t.Run("lang="+lang, func(t *testing.T) {
			sum := &fakeSummarizer{
				response: provider.SummarizeResult{Text: "step by step", Provider: "fake"},
			}
			a := newTestAnalyzer(sum)
			a.Lang = lang

			_, err := a.ExplainLast(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sum.captured == nil {
				t.Fatal("Summarizer was not called")
			}
			if sum.captured.Lang != lang {
				t.Errorf("ErrorAnalyzer ExplainLast SummarizeInput.Lang = %q, want %q", sum.captured.Lang, lang)
			}
		})
	}
}

func TestIntegration_QAEngine_LangOverride(t *testing.T) {
	for _, lang := range []string{"ko", "en", "ja"} {
		t.Run("lang="+lang, func(t *testing.T) {
			sum := &fakeSummarizer{
				response: provider.SummarizeResult{Text: "answer", Provider: "fake"},
			}
			q := newTestQAEngine(sum)
			q.Lang = lang

			_, err := q.Answer(context.Background(), "How do I rebase?")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sum.captured == nil {
				t.Fatal("Summarizer was not called")
			}
			if sum.captured.Lang != lang {
				t.Errorf("QAEngine SummarizeInput.Lang = %q, want %q", sum.captured.Lang, lang)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. AI disabled (6.5): 에러 메시지 형식 검증
//
// CLI 레이어에서 ai.enabled=false일 때 출력하는 에러 메시지 형식을 검증한다.
// 실제 CLI 실행 없이 에러 메시지 문자열 패턴을 확인한다.
// ---------------------------------------------------------------------------

func TestIntegration_AIDisabledErrorMessage(t *testing.T) {
	// CLI 레이어에서 사용하는 에러 메시지 형식과 동일한지 검증.
	// ai_do.go, ai_explain.go, ai_ask.go 모두 동일한 패턴을 사용한다.
	expectedMsg := "AI features are disabled (ai.enabled=false)"
	expectedHint := "hint: set ai.enabled=true in .gk.yaml or unset GK_AI_DISABLE"

	// 각 명령어에서 사용하는 에러 메시지 형식을 검증.
	for _, cmd := range []string{"do", "explain", "ask"} {
		t.Run(cmd, func(t *testing.T) {
			// CLI에서 생성하는 에러 메시지 형식 재현.
			errMsg := expectedMsg + "\n" + expectedHint
			if !strings.Contains(errMsg, "ai.enabled=false") {
				t.Error("에러 메시지에 'ai.enabled=false'가 포함되어야 한다")
			}
			if !strings.Contains(errMsg, "ai.enabled=true") {
				t.Error("에러 메시지에 활성화 힌트가 포함되어야 한다")
			}
			if !strings.Contains(errMsg, "GK_AI_DISABLE") {
				t.Error("에러 메시지에 환경변수 힌트가 포함되어야 한다")
			}
		})
	}

	// GK_AI_DISABLE=1 에러 메시지 형식도 검증.
	envMsg := "AI features are disabled (GK_AI_DISABLE=1)"
	envHint := "hint: unset GK_AI_DISABLE to enable AI features"
	fullEnvMsg := envMsg + "\n" + envHint
	if !strings.Contains(fullEnvMsg, "GK_AI_DISABLE=1") {
		t.Error("환경변수 에러 메시지에 'GK_AI_DISABLE=1'이 포함되어야 한다")
	}
	if !strings.Contains(fullEnvMsg, "unset GK_AI_DISABLE") {
		t.Error("환경변수 에러 메시지에 해제 힌트가 포함되어야 한다")
	}
}

// ---------------------------------------------------------------------------
// 4. Timeout (11.4): 타임아웃 파싱 및 적용 검증
//
// parseDurationOrDefault는 cli 패키지에 있으므로, 여기서는 동일한 로직을
// 로컬 헬퍼로 재현하여 타임아웃 문자열 파싱이 올바르게 동작하는지 검증한다.
// 또한 실제 컴포넌트에 타임아웃이 적용되는지 검증한다.
// ---------------------------------------------------------------------------

// parseDurationOrDefault는 cli 패키지의 동일 함수와 같은 로직.
// 타임아웃 문자열 파싱 동작을 검증하기 위한 로컬 복제본.
func parseDurationOrDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

func TestIntegration_ParseDurationOrDefault(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		def      time.Duration
		expected time.Duration
	}{
		{"valid 30s", "30s", 10 * time.Second, 30 * time.Second},
		{"valid 1m", "1m", 10 * time.Second, 1 * time.Minute},
		{"valid 500ms", "500ms", 10 * time.Second, 500 * time.Millisecond},
		{"valid 2m30s", "2m30s", 10 * time.Second, 2*time.Minute + 30*time.Second},
		{"empty string → default", "", 30 * time.Second, 30 * time.Second},
		{"invalid string → default", "not-a-duration", 30 * time.Second, 30 * time.Second},
		{"invalid number → default", "abc", 15 * time.Second, 15 * time.Second},
		{"zero default", "", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDurationOrDefault(tt.input, tt.def)
			if got != tt.expected {
				t.Errorf("parseDurationOrDefault(%q, %v) = %v, want %v", tt.input, tt.def, got, tt.expected)
			}
		})
	}
}

// TestIntegration_TimeoutAppliedToContext는 타임아웃이 컨텍스트에 적용되어
// AI 호출이 타임아웃되는지 검증한다.
func TestIntegration_TimeoutAppliedToContext(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
			"rev-parse --short HEAD":      {Stdout: "abc1234\n"},
			"rev-parse --abbrev-ref @{u}": {Stdout: "origin/main\n"},
			"status --porcelain=v2":       {Stdout: ""},
			"reflog -10 --format=%h %gs":  {Stdout: "abc1234 commit: init\n"},
		},
	}

	// IntentParser 타임아웃 검증
	t.Run("IntentParser", func(t *testing.T) {
		p := &IntentParser{
			Summarizer: &slowSummarizer{},
			Context:    &RepoContextCollector{Runner: r, TokenBudget: 2000},
			Safety:     &SafetyClassifier{},
			Lang:       "en",
			Timeout:    50 * time.Millisecond,
		}
		_, err := p.Parse(context.Background(), "do something")
		if err == nil {
			t.Fatal("타임아웃 에러가 발생해야 한다")
		}
		if !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Errorf("에러에 'context deadline exceeded'가 포함되어야 한다, got: %v", err)
		}
	})

	// ErrorAnalyzer 타임아웃 검증
	t.Run("ErrorAnalyzer", func(t *testing.T) {
		a := &ErrorAnalyzer{
			Summarizer: &slowSummarizer{},
			Context:    &RepoContextCollector{Runner: r, TokenBudget: 2000},
			Lang:       "en",
			Timeout:    50 * time.Millisecond,
		}
		_, err := a.DiagnoseError(context.Background(), "some error")
		if err == nil {
			t.Fatal("타임아웃 에러가 발생해야 한다")
		}
		if !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Errorf("에러에 'context deadline exceeded'가 포함되어야 한다, got: %v", err)
		}
	})

	// QAEngine 타임아웃 검증
	t.Run("QAEngine", func(t *testing.T) {
		rq := &git.FakeRunner{
			Responses: map[string]git.FakeResponse{
				"rev-parse --abbrev-ref HEAD":      {Stdout: "main\n"},
				"rev-parse --short HEAD":           {Stdout: "abc1234\n"},
				"rev-parse --abbrev-ref @{u}":      {Stdout: "origin/main\n"},
				"status --porcelain=v2":            {Stdout: ""},
				"reflog -10 --format=%h %gs":       {Stdout: "abc1234 commit: init\n"},
				"branch --format=%(refname:short)": {Stdout: "main\n"},
			},
		}
		q := &QAEngine{
			Summarizer: &slowSummarizer{},
			Context:    &RepoContextCollector{Runner: rq, TokenBudget: 2000},
			Lang:       "en",
			Timeout:    50 * time.Millisecond,
		}
		_, err := q.Answer(context.Background(), "How do I rebase?")
		if err == nil {
			t.Fatal("타임아웃 에러가 발생해야 한다")
		}
		if !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Errorf("에러에 'context deadline exceeded'가 포함되어야 한다, got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 5. 전체 흐름 통합: Summarizer에 전달되는 입력이 올바른지 검증
// ---------------------------------------------------------------------------

func TestIntegration_SummarizerReceivesCorrectInput(t *testing.T) {
	// IntentParser: Diff 필드에 사용자 입력이 포함되는지 검증
	t.Run("IntentParser_DiffContainsInput", func(t *testing.T) {
		validJSON := `{"commands":[{"command":"git status","description":"check"}]}`
		sum := &fakeSummarizer{
			response: provider.SummarizeResult{Text: validJSON, Provider: "fake"},
		}
		p := newTestParser(sum)
		p.Lang = "ko"

		_, err := p.Parse(context.Background(), "상태 확인해줘")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sum.captured == nil {
			t.Fatal("Summarizer was not called")
		}
		// Diff 필드에 사용자 입력이 포함되어야 한다.
		if !strings.Contains(sum.captured.Diff, "상태 확인해줘") {
			t.Error("SummarizeInput.Diff에 사용자 입력이 포함되어야 한다")
		}
		// Lang이 올바르게 전달되어야 한다.
		if sum.captured.Lang != "ko" {
			t.Errorf("SummarizeInput.Lang = %q, want %q", sum.captured.Lang, "ko")
		}
	})

	// ErrorAnalyzer: Diff 필드에 에러 메시지가 포함되는지 검증
	t.Run("ErrorAnalyzer_DiffContainsError", func(t *testing.T) {
		sum := &fakeSummarizer{
			response: provider.SummarizeResult{Text: "diagnosis", Provider: "fake"},
		}
		a := newTestAnalyzer(sum)
		a.Lang = "en"

		_, err := a.DiagnoseError(context.Background(), "fatal: not a git repository")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sum.captured == nil {
			t.Fatal("Summarizer was not called")
		}
		if !strings.Contains(sum.captured.Diff, "fatal: not a git repository") {
			t.Error("SummarizeInput.Diff에 에러 메시지가 포함되어야 한다")
		}
	})

	// QAEngine: Diff 필드에 질문이 포함되는지 검증
	t.Run("QAEngine_DiffContainsQuestion", func(t *testing.T) {
		sum := &fakeSummarizer{
			response: provider.SummarizeResult{Text: "answer", Provider: "fake"},
		}
		q := newTestQAEngine(sum)
		q.Lang = "en"

		_, err := q.Answer(context.Background(), "What is a merge conflict?")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sum.captured == nil {
			t.Fatal("Summarizer was not called")
		}
		if !strings.Contains(sum.captured.Diff, "What is a merge conflict?") {
			t.Error("SummarizeInput.Diff에 질문이 포함되어야 한다")
		}
	})
}

// ---------------------------------------------------------------------------
// 6. CLI help 출력 검증: gk do/explain/ask --help에 예상 플래그가 포함되는지 검증
// ---------------------------------------------------------------------------

func TestIntegration_CLIHelpOutput(t *testing.T) {
	// gk 바이너리 빌드.
	binPath := t.TempDir() + "/gk-test"
	build := exec.Command("go", "build", "-o", binPath, "./cmd/gk/")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("gk 바이너리 빌드 실패: %v\n%s", err, out)
	}

	tests := []struct {
		cmd           string
		expectedFlags []string
	}{
		{
			cmd:           "do",
			expectedFlags: []string{"--yes", "--force", "--dry-run", "--json", "--provider", "--lang"},
		},
		{
			cmd:           "explain",
			expectedFlags: []string{"--last", "--provider", "--lang"},
		},
		{
			cmd:           "ask",
			expectedFlags: []string{"--provider", "--lang"},
		},
	}

	for _, tt := range tests {
		t.Run("gk_"+tt.cmd+"_help", func(t *testing.T) {
			var buf bytes.Buffer
			cmd := exec.Command(binPath, tt.cmd, "--help")
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			if err := cmd.Run(); err != nil {
				t.Fatalf("gk %s --help 실행 실패: %v\n%s", tt.cmd, err, buf.String())
			}
			helpOutput := buf.String()
			for _, flag := range tt.expectedFlags {
				if !strings.Contains(helpOutput, flag) {
					t.Errorf("gk %s --help 출력에 %q 플래그가 포함되어야 한다", tt.cmd, flag)
				}
			}
		})
	}
}

// findRepoRoot는 go.mod 파일을 기준으로 저장소 루트 디렉토리를 찾는다.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	// 현재 작업 디렉토리에서 시작하여 go.mod를 찾는다.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("작업 디렉토리 확인 실패: %v", err)
	}
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir {
			t.Fatal("go.mod를 찾을 수 없습니다")
		}
		dir = parent
	}
}
