# Design: `--ai` Flag Expansion

상태: Draft · 2026-05-14 · 대상: 우선순위 5개 (brainstorm `brainstorm-2026-05-14-gk-ai-flag-candidates.json`)

## 1. 배경 (WHY)

`gk status --ai`와 `gk doctor --ai`가 "설명 레이어" 패턴을 정립했다. 이 패턴을
다른 커맨드로 확장하려는데, `status_ai.go`를 보면 provider 해석 / privacy
gate / spinner / local fallback / 렌더링이 **커맨드마다 복붙될 구조**다.

따라서 이 설계의 핵심은 개별 `--ai` 기능이 아니라, 그것들이 공유할
**공통 헬퍼(`aiassist`)를 먼저 추출**하는 것이다. 5개 우선순위 항목은 모두
이 헬퍼 위에 얇게 얹힌다.

### 우선순위 (brainstorm 결과)

| # | 항목 | 테마 |
|---|------|------|
| 1 | `gk diff --ai` | diff 자연어 요약 |
| 2 | `gk precheck --ai` / `gk preflight --ai` | 진단 actionable 변환 |
| 3 | `gk undo/timemachine restore/wipe/forget --ai` | 파괴적 커맨드 영향 브리핑 |
| 4 | `gk guard check --ai` | 정책 위반 코칭 |
| 5 | 전역 `--ai` 에러 핸들러 | 횡단 인프라 |

## 2. 공통 헬퍼: `internal/cli/aiassist.go` (또는 `internal/aiassist` 패키지)

`status_ai.go`에서 커맨드 비종속 로직을 추출한다. 이미 존재하는 재사용 가능 조각:

- `provider.Summarizer` — `Summarize(ctx, SummarizeInput{Kind, Diff, Lang, MaxTokens})`
- `applyPrivacyGate(prov, payload, cfg.AI)` — 원격 provider 페이로드 redaction
- `resolveStatusAssistProvider` / `buildFallbackChain` — provider 해석 + fallback chain
- `showPromptIfRequested(cmd, redacted)` — `--show-prompt` 처리
- `ui.StartBubbleSpinner(label)` — 진행 표시
- `aichat.ErrorAnalyzer` — 에러 진단기 (전역 핸들러 #5에서 재사용)

### 2.1 제안 API

```go
// AssistRequest는 한 번의 --ai 호출에 필요한 모든 것을 담는다.
type AssistRequest struct {
    Kind          string          // "diff", "precheck", "destructive", "guard", ...
    Cmd           *cobra.Command  // 플래그(--provider/--lang/--show-prompt) 읽기용
    Cfg           *config.Config
    Facts         any             // 커맨드별 facts struct (JSON 직렬화 가능)
    PromptBuilder func(facts any, lang string) string
    LocalFallback func(w io.Writer, facts any, lang string) // provider 없을 때
    MaxTokens     int
}

// RenderAssist는 status_ai.go의 renderStatusAssist를 일반화한 것:
// provider 해석 → privacy gate → Summarize → 실패 시 LocalFallback.
func RenderAssist(ctx context.Context, req AssistRequest, out, errOut io.Writer) error
```

각 커맨드는 (a) facts struct, (b) `PromptBuilder`, (c) `LocalFallback` 세 개만
구현하면 된다. provider/privacy/spinner/lang resolution은 헬퍼가 전담.

### 2.2 공통 플래그 등록 헬퍼

```go
// AddAssistFlags는 --ai/--provider/--lang을 커맨드에 일괄 등록한다.
// status.go가 이미 쓰는 3개 플래그를 표준화.
func AddAssistFlags(cmd *cobra.Command)
```

> 주의: `status_ai.go:67` 주석대로 `--ai` boolean 플래그를 Viper에 bind하면
> 안 된다 (config schema의 `ai:` 객체와 충돌). `AddAssistFlags`는 bind하지
> 않는다.

### 2.3 공통 가드 (횡단)

brainstorm #9에서 나온 횡단 가드를 헬퍼에 내장:

- **CI/non-TTY 강등** — `CI=true` 또는 비-TTY 시 `--ai`를 no-op 또는 JSON
  annotation으로 강등 (토큰 낭비·파이프 오염 방지)
- **`GK_AI_DISABLE=1` / `ai.enabled=false`** — 기존 체크 재사용
- (후속) **AI 응답 캐시** — `HEAD`+payload 해시 기반. 이번 범위 밖, 헬퍼에
  hook point만 남긴다

## 3. 커맨드별 설계

### 3.1 `gk diff --ai` (#1 — 먼저 구현, 가장 단순)

- **통합 지점**: `runDiff` (`diff.go:54`). diff 본문을 출력한 뒤, `--ai`가
  켜져 있으면 `RenderAssist` 호출.
- **Facts**: diff stat (파일 수, +/- 라인), 변경 파일 목록(경로+상태, 상한
  적용), `--staged` 여부, ref 범위. **diff 본문 자체**는 privacy gate를 거쳐
  payload로. `status_ai.go`의 `statusAssistMaxPaths=12` 같은 상한 패턴 적용.
- **Prompt**: "이 diff의 의도, 핵심 변경점, 리뷰 시 주목할 리스크 포인트를
  요약하라. 코드를 생성하지 말 것."
- **LocalFallback**: diff stat 요약 + "변경 규모가 큼/작음" 정도의 휴리스틱.
- **출력**: 기존 diff 출력 아래 `AI summary` 섹션 (status_ai의 `AI status`
  패턴).
- **제약**: `--json`과 `--ai` 동시 사용 거부 (status_ai와 동일 규칙,
  `status.go:1398` 참고).

### 3.2 `gk precheck --ai` / `gk preflight --ai` (#2)

- **통합 지점**: `runPrecheckCore` (`precheck.go:55`), `runPreflight`
  (`preflight.go:43`).
- **Facts**:
  - precheck: 충돌 파일 목록, merge-base, 대상 브랜치, exit code 의미.
  - preflight: 각 step 이름 + PASS/FAIL + 실패 step의 stderr (privacy gate
    경유).
- **Prompt**: "실패한 항목마다 (1) 왜 실패했는지 (2) 어떻게 고치는지
  (3) 실행할 `gk`/`git` 명령을 제시하라. recommended_commands에 있는 명령만
  사용." → status_ai의 `recommended_commands` 화이트리스트 패턴 재사용.
- **LocalFallback**: 실패 step별 정적 매핑 (commit-lint→`gk lint-commit`,
  no-conflict→`gk precheck` 등).
- **제약**: precheck `--json` + `--ai` 동시 사용 거부.

### 3.3 파괴적 커맨드 `--ai` 영향 브리핑 (#3)

대상: `undo` (`undo.go:65`), `timemachine restore` (`timemachine.go:408`),
`wipe` (`wipe.go`), `forget` (`forget.go:76`).

- **통합 지점**: 각 커맨드의 **확인 프롬프트 직전**. `--ai`가 켜져 있으면
  영향 브리핑을 먼저 출력하고, 그 다음 기존 확인 프롬프트(`--yes`로 skip
  가능)로 진행. `--ai`는 **실행을 대체하지 않는다** — 설명 레이어일 뿐.
- **Facts** (커맨드별):
  - `undo`: 대상 reflog 엔트리, 잃게 될 커밋/파일, push 여부, `--hard`면
    working-tree 파괴 경고.
  - `timemachine restore`: 대상 SHA, 현재 HEAD와의 diff stat, backup ref
    경로, reset mode.
  - `wipe`: 삭제될 tracked 변경 / untracked / ignored 파일 수, backup ref.
  - `forget`: 대상 경로, 고유 blob 수/바이트, history rewrite 영향, backup
    ref.
- **Prompt**: "이 작업이 무엇을 영구적으로 바꾸는지, 무엇이 backup ref로
  복구 가능한지 분류해 설명하라. 절대 실행을 권하거나 막지 말고 사실만."
- **LocalFallback**: 각 커맨드가 이미 계산하는 backup-ref/dry-run 정보를
  그대로 자연어 템플릿에 채움 (대부분 이미 dry-run 경로 존재).
- **공통**: 4개 모두 `destructive` Kind 하나로 처리. facts struct만 다름.

### 3.4 `gk guard check --ai` (#4)

- **통합 지점**: `runGuardCheck` (`guard.go:81`). 위반 리스트 출력 후, `--ai`
  시 코칭 섹션 추가.
- **Facts**: 위반 rule 이름, 위반 대상(브랜치명/파일/커밋 — untrusted 데이터
  표시), 정책 설정값.
- **Prompt**: "각 정책 위반에 대해 (1) 이 정책이 왜 존재하는지 (2) 위반이
  만드는 실제 위험 (3) 우회가 아닌 올바른 워크플로우를 설명하라."
- **LocalFallback**: rule별 정적 설명 문구.
- **제약**: `--json` + `--ai` 거부. exit code(0/1/2)는 `--ai`와 무관하게 유지.

### 3.5 전역 `--ai` 에러 핸들러 (#5 — 마지막, 가장 큰 설계 비용)

- **목표**: 모든 커맨드가 에러로 종료될 때 `--ai`가 켜져 있으면 stderr 원문 +
  직전 git 컨텍스트로 AI 진단/복구 제안을 출력. 커맨드별 구현 없이 한 곳에서.
- **통합 지점**: `rootCmd`의 `RunE` wrapper 또는 cobra의
  `SilenceErrors`+중앙 에러 처리 지점 (main.go / root.go). 현재 `gk explain
  --last`가 쓰는 `aichat.ErrorAnalyzer`를 그대로 재사용.
- **설계 쟁점**:
  - 전역 `--ai` persistent flag로 둘지, 아니면 `GK_AI_ON_ERROR` 환경변수 +
    `ai.assist.on_error` config로 둘지. → **config + 전역 persistent flag**
    조합 권장 (#9의 `--ai-level off/hint/full`로 확장 가능하게).
  - 3.1~3.4의 커맨드-로컬 `--ai`(설명을 *주 출력*으로)와 전역 `--ai`(에러 시
    *부가 진단*)는 의미가 다르다. 같은 플래그 이름을 공유하되, 커맨드-로컬
    `--ai`가 등록된 커맨드에서는 로컬 동작이 우선하고, 그 외에는 전역
    에러 핸들러로 fallback.
- **이 항목은 3.1~3.4 완료 후 별도 설계 리뷰**를 거친다 (아래 5절).

## 4. 구현 순서

1. **공통 헬퍼 `aiassist`** 추출 — `status_ai.go` 리팩터링 (기존 동작 보존,
   회귀 테스트 `status_ai_test.go` 통과 유지).
2. **`gk diff --ai`** — 헬퍼의 첫 소비자. 패턴 검증.
3. **`gk precheck/preflight --ai`** — 화이트리스트 액션 패턴 검증.
4. **파괴적 커맨드 `--ai`** — `destructive` Kind, 4개 커맨드.
5. **`gk guard check --ai`**.
6. **전역 `--ai` 에러 핸들러** — 별도 설계 리뷰 후.

각 단계는 독립 커밋. 단계마다 `go build ./...` + `go test ./internal/cli/...`.

## 5. 미해결 질문 (구현 전 결정 필요)

1. **`aiassist`를 `internal/cli` 내 파일로 둘지, `internal/aiassist` 패키지로
   분리할지.** 패키지 분리 시 `provider`/`config`/`ui` 의존성 방향 확인 필요.
2. **전역 `--ai`와 커맨드-로컬 `--ai`의 플래그 충돌.** persistent flag로
   전역 등록 시, 로컬 `--ai`를 가진 커맨드(`status`)와의 정의 충돌 — cobra가
   거부할 수 있음. 네이밍 분리(`--ai` vs `--ai-on-error`) 또는 persistent
   flag 단일화 중 택1.
3. **`--explain`(정적·오프라인) vs `--ai`(동적·LLM) 역할 분리** (#9) — 이번
   범위엔 없지만, `LocalFallback`이 사실상 `--explain`의 씨앗. 네이밍을 미리
   맞출지.
4. **CI 강등의 기본 동작** — no-op으로 조용히 끌지, "CI에서 --ai 무시됨"
   1줄 stderr를 낼지.

## 6. 참고

- 패턴 원본: `internal/cli/status_ai.go`, `internal/cli/doctor_ai.go`
- 에러 분석기: `internal/cli/ai_explain.go`, `internal/aichat`
- brainstorm 결과: `.xm/op/brainstorm-2026-05-14-gk-ai-flag-candidates.json`
- 기존 `--ai` 문서: `docs/commands.md` (status), `docs/config.md` §`ai.assist`
