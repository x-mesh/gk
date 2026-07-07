# Phase 2 Improvements — 예약된 Phase 2를 완성한다

상태: 구현됨 · 2026-07-07 · 브랜치: `claude/gl-phase2-improvements-tc7b69`

## 1. 배경 (WHY)

코드베이스 감사 결과, "Phase 1은 출시되고 Phase 2는 예약만 된" 지점이 두 곳
남아 있었다. 둘 다 설계 문서/코드 주석에 Phase 2의 알고리즘까지 적혀 있었지만
구현은 stub이거나 설계 메모였다:

| 영역 | Phase 1 (구현됨) | Phase 2 (이 프로젝트 이전) |
|------|------------------|---------------------------|
| snapshot 안전망 ([design-snapshot-safety-net.md](design-snapshot-safety-net.md)) | `gk snapshot` 저장/목록/복원 명령 | §4 트리거 자동화·보존 정책·diff — **설계 메모만** |
| branchparent 추론 (`internal/branchparent/resolver.go`) | 명시적 `gk-parent` config 읽기 + Resolver API | `inferParent` — **항상 ""를 반환하는 stub** |

이 프로젝트는 **현재 상태**(수동 명령·명시적 설정만)를 **미래 상태**(자동
트리거·무설정 추론)로 끌어올리되, 두 영역 모두 "안전망/표시용 기능이 쓰기
동작을 오염시키지 않는다"는 경계를 명시적으로 지킨다.

## 2. 현재 상태 진단 (구현 전)

### 2.1 snapshot: 명령은 있지만 "자동"이 아니다

- `gk snapshot`은 손으로 실행해야만 안전망이 된다. 설계 문서 §4.1이 권장한
  Claude Code Stop hook은 사용자가 `~/.claude/settings.json`을 직접
  편집해야 했고, 기존 hook과의 공존(append-only) 규칙도 수동 책임이었다.
- 보존 정책이 없어 hook을 걸면 스냅샷 reflog가 무한히 쌓인다(§4.3 미적용).
- 스냅샷과 현재 상태를 비교할 수단이 없다 — 복원 전에 무엇이 돌아올지 알 수
  없다.

### 2.2 branchparent: API는 최종형인데 추론이 비어 있다

- Resolver는 status/switch/worktree/land가 공유하는 단일 진입점으로 설계됐고,
  주석에 Phase 2 알고리즘(가장 오래된 reflog 항목 = 분기점 → `for-each-ref
  --contains` → 단일 후보만 채택)까지 명시돼 있었다.
- stub 상태라 `gk branch set-parent`를 안 쓴 사용자는 stacked 브랜치에서
  status의 "from <base>" 비교가 항상 트렁크 기준으로 나왔다.
- 단, Resolver는 `land`/`promote`의 **머지 대상 결정**에도 쓰이고 있었다 —
  추론을 그대로 켜면 휴리스틱이 머지 방향을 정하게 되는 위험이 있었다.

## 3. 구현된 미래 상태

### 3.1 snapshot Phase 2 — 자동 안전망 완성

| 항목 | 구현 |
|------|------|
| 트리거 (§4.1) | `gk snapshot hook install\|status\|uninstall` — Claude Code Stop hook(`gk snapshot -q`)을 설정 JSON에 append-only·멱등으로 설치. 기본 `~/.claude/settings.json`, `--project`는 repo의 `.claude/settings.json`, `--settings <path>` 임의 경로. 파싱 불가 파일은 **덮어쓰지 않고 거부**. 제거는 gk 소유 항목만. |
| 보존 (§4.3) | `gk snapshot prune [--keep-days N] [--all]` + `snapshot.retention_days` config(기본 0 = off). retention이 켜져 있으면 저장 때마다 조용히 best-effort expire — 실패해도 저장은 성공한다(스냅샷 자체가 안전망이므로). 전부 만료된 ref는 삭제해 `@{n}`으로 접근 불가능한 유령 ref를 남기지 않는다. |
| 비교 (§4.3) | `gk snapshot diff [n] [--stat]` — 복원이 적용할 방향(스냅샷 → 작업트리) 그대로 표시. |

### 3.2 branchparent Phase 2 — 추론 + 쓰기 경로 차단

- `inferParent` 구현: 문서화된 알고리즘 그대로. 후보가 정확히 하나일 때만
  채택(`SourceInferred`), 0개/여러 개면 추론 포기 — main과 develop이 둘 다
  분기점을 포함하는 공유 트렁크 repo에서는 자연히 꺼진다. reflog가 없거나
  만료된 환경(bare/CI)은 에러 없이 "추론 없음"으로 강등.
- **`Resolver.ExplicitOnly()`** 신설: `land`/`promote`의 hop 대상 결정은 이
  모드로 고정 — 머지 목적지는 명시적 `gk-parent` 또는 트렁크 폴백에서만
  나온다. 추론은 표시 경로(status의 base 비교, worktree agent envelope)에만
  적용된다.

## 4. 설계 결정 기록

1. **retention 기본값 0 (off).** 설계 문서는 "7일 자동 expire"를 제안했지만,
   Phase 1 사용자가 이미 쌓아둔 스냅샷이 업그레이드만으로 사라지는 것은
   안전망의 계약 위반이다. 자동 정리는 opt-in(`retention_days > 0`),
   `prune`의 수동 기본값만 7일.
2. **hook 설치기는 JSON을 절대 재작성-복구하지 않는다.** 깨진
   settings.json은 고치라는 에러와 함께 거부 — 파싱 실패 상태에서의 재작성은
   사용자 설정 파괴와 구분할 수 없다. 숫자는 `json.Number`로 보존해
   재직렬화가 `timeout: 30`을 float 표기로 바꾸지 않는다.
3. **추론은 단일 후보만, 쓰기 경로는 차단.** 추론이 틀릴 수 있는 두 상황
   (다중 후보, 죽은 ref)은 모두 "추론 없음"으로 강등되고, 머지 대상에는
   아예 관여하지 못한다. 잘못된 추론의 최악 결과는 status 표시가 트렁크
   대신 다른 브랜치 기준으로 나오는 것 — 복구 불가능한 동작은 없다.
4. **`restore --diff` 대신 `snapshot diff`.** top-level `restore`
   네임스페이스 충돌(§3의 기존 결정)과 일관되게 snapshot 서브커맨드로 배치.

## 5. 남은 백로그 (Phase 3 후보)

- **watch 데몬 (§4.2)** — 에디터 무관 시간 기반 자동 스냅샷.
  LaunchAgent/systemd 생명주기 관리 비용이 커서 수요 신호가 생길 때까지 보류.
- **status의 inferred 출처 표시 강화** — 현재 `-v`/explain 계층에서 source가
  드러나지만, 헤드라인에 "(inferred)" 라벨을 붙일지는 사용 피드백 후 결정.
- **`gk snapshot prune --dry-run`** — 만료 대상 미리보기.
- **inference 캐시** — 브랜치당 reflog 2회 호출은 status 예산 안이지만,
  대형 repo에서 `for-each-ref --contains`가 느리면 결과를 git config에
  기록(promote-to-explicit)하는 최적화를 검토.

## 6. 검증

- `internal/branchparent`: 추론 단일 후보/다중 후보(모호)/reflog 부재/
  ExplicitOnly(reflog 접근 금지 검증)/죽은 ref 폴백 — 유닛 테스트.
- `internal/cli`: prune(만료·빈 ref 삭제·no-op), diff(변경·무변경·--stat·
  범위 초과), hook(생성·멱등·기존 설정 보존·부분 제거·손상 JSON 거부) —
  통합 테스트. 전체 `go build ./...` / `go vet` / 패키지 테스트 통과.
