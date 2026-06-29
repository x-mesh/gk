# `gk fleet` multi-repo 확장 — 설계 (종합)

> 출처: xm:panel cross (cursor + kiro 수렴안) + 메인테이너 tie-break.
> claude/codex는 240s timeout으로 미참여(2/4 패널).

## 한 줄 요약
cwd가 git repo 내부면 기존 single-repo 동작을 100% 보존하고, `--scan`/`--repos` 또는 config `fleet.repos`/`fleet.scan`이 있으면 multi-repo로 진입한다. JSON은 기존 flat 배열에 `repo`/`repo_root` 필드만 append-only로 더한다. TUI는 repo 그룹 헤더 + 접기/펼치기 트리로 렌더하고, drill-down은 1차로 기존 `gk st --watch`를 프로세스로 띄워 재사용한다.

## 4/4 패널 보강 (claude·codex 재시도, --timeout 600)
claude·codex가 cursor·kiro의 합의를 **전원 재확인**(JSON flat·모드 진입·fsnotify 배제·그룹 트리·semaphore·ExecProcess drill-down 모두 4/4). claude는 코드를 읽고 종합안의 **실질 갭 4가지**를 잡음 — 아래는 확정 반영분:

1. **repo dedup은 `EvalSymlinks`가 아니라 `git rev-parse --git-common-dir`** [claude]. linked worktree는 `.git`이 *파일*로 메인 repo를 가리키므로, 경로 정규화만으론 같은 repo가 여러 entry로 중복 집계된다. common-dir 키가 symlink+worktree를 한 번에 흡수.
2. **도달불가/timeout repo를 flat 배열에서 사라지지 않게** `status:"error"` + `error` 필드 합성 entry 1개 [claude]. 안 그러면 느린 repo가 조용히 빠져 "정상"처럼 보임.
3. **`gatherFleet`의 worktree goroutine은 현재 상한 없는 `WaitGroup`** [claude, 코드 사실]. "기존 gatherFleet 그대로 재사용"만으론 repo N × worktree M 프로세스 폭발을 못 막는다 → repo-레벨과 worktree-레벨이 **공유하는 단일 전역 semaphore**에 enrich goroutine까지 태워야 함.
4. **status 계열 git 호출에 `GIT_OPTIONAL_LOCKS=0`** [codex]. 멀티 에이전트 환경에서 에이전트의 `git add`/index 쓰기와 fleet의 read-only probe가 `index.lock` 경합하는 걸 줄인다 — 이 기능의 사용 맥락(에이전트가 동시 편집 중)에 정확히 들어맞음.

추가 채택: **모드 진입 보수화** — config(`fleet.repos`/`scan`) 존재만으로 자동 multi 전환하지 않고, repo 내부에서는 `--all` 옵트인 또는 repo 밖 실행으로만 multi(놀람 최소, claude). **scan은 시작 1회만, `R` 키로 rescan**(매 poll마다 tree walk 안 함, codex).

**roll-up 우선순위 — 구현 결정(정정)**: 패널 다수는 `conflict>paused`였으나, 기존 `fleetStatus`(단일 worktree status)가 이미 `paused>conflict`(paused는 resume/abort 없이는 다른 작업 불가)다. repo roll-up이 worktree status와 어긋나면 혼란스러우므로 **일관성을 위해 codex 안 `paused > conflict > dirty > diverged > ahead > behind > clean` 채택**(`fleetStatusRank`, error는 최상위).

## 두 모델이 합의한 결정 (높은 신뢰)
1. **JSON = flat 배열 유지 + append-only 필드**. 중첩 `{repo, worktrees:[]}`는 기존 `--json`/`jq '.[]'`/agent 폴링을 깨므로 거부.
2. **repo 발견 = explicit > scan > config**. `--repos`(복수) / `--scan <dir>` / config `fleet.repos`·`fleet.scan`.
3. **모드 진입**: 플래그·config 없고 cwd가 repo 내부 → single(기존 보존). `--scan`/`--repos`/`--multi` 또는 (config 있음 + cwd가 repo 밖) → multi.
4. **fsnotify는 multi-repo overview에 쓰지 않음** — repo N × 디렉토리 M = inotify/FSEvents watch 폭발. 2s git 폴링으로 충분. fsnotify는 drill-down 단일 repo에서만.
5. **TUI = repo 그룹 헤더 + 접기/펼치기**, repo roll-up status(worst-wins), 커서 안정성은 `(repo_root, path)` 튜플, narrow 터미널 자동 접기.
6. **per-repo timeout 격리** (3~10s) — 느린 repo는 stale 표시 후 다음 틱 재시도. fleet은 fetch 안 함(local-only).
7. **버린 대안 일치**: 중첩 JSON / 별도 커맨드(`gk monitor`) / 전체 fsnotify / daemon — 모두 거부.

## 갈린 점 → tie-break (메인테이너 판단)
| 쟁점 | cursor | kiro | 결정 |
|---|---|---|---|
| 필드 네이밍 | `repoRoot`(camel) | `repo_root`(snake) | **snake_case** — 기존 스키마(`active_ago_s`,`parent_behind`)와 일치 |
| repo 내 worktree | 병렬(4) | 순차(git lock 우려) | **병렬 유지** — read-only `status --porcelain`은 index.lock 경합 없음. 기존 `gatherFleet`(worktree goroutine) 그대로 재사용 |
| 전체 동시성 | repo당 4 | `min(NumCPU,8)` 풀 | **전역 semaphore `min(NumCPU,8)`** — repo×worktree 폭발 방지 |
| roll-up 우선순위 | dirty>conflict>… | conflict>paused>dirty>… | **kiro** — conflict/paused가 가장 시급 |
| 정렬 | repo_root 알파벳 | active_ago_s | **repo_root 알파벳** — 커서 점프 최소화(활동순은 옵션 `--group-by`) |
| drill-down | `tea.ExecProcess`로 `gk st --watch` 띄움 | TUI 내부 split-pane(fsnotify) | **1차 ExecProcess**(완성품 재사용, 최소표면) → split-pane은 2차 개선 |

## 플래그 / config
```go
// 기존 유지: --interval, --repo(단일, legacy), --json
fleetCmd.Flags().StringSlice("repos", nil, "explicit repo paths")
fleetCmd.Flags().String("scan", "", "directory to scan for git repos")
fleetCmd.Flags().Bool("multi", false, "force multi-repo mode from inside a repo")
fleetCmd.Flags().Int("depth", 2, "scan recursion depth")
fleetCmd.Flags().String("group-by", "repo", "repo|status|activity")
```
```yaml
# .gk.yaml (전역 또는 로컬)
fleet:
  repos: [~/work/project/agentic/gk, ~/work/project/agentic/aic-rust]
  scan: ~/work/project/agentic   # --scan 미지정 시 기본 스캔 루트
  depth: 2
  exclude: ["*/node_modules/*", "*/.archive/*"]
  interval: 2
```
우선순위: `--repos` > `--scan` > `fleet.repos` > `fleet.scan` > (cwd가 repo면 single).

## JSON 계약 (append-only)
```jsonc
// GK_AGENT=1 → {state:"ok", ok:true, result:[ ... ]}
[
  {
    "repo": "gk",                                  // NEW basename
    "repo_root": "/Users/j/work/project/agentic/gk", // NEW 절대경로
    "path": "/Users/j/.gk/worktree/gk/feat-x",     // 기존: worktree 경로
    "branch": "feat-x", "current": false,
    "ahead": 3, "behind": 0,
    "dirty": {"staged":1,"unstaged":0,"untracked":0,"conflicts":0},
    "status": "ahead", "active_ago_s": 120,
    "operation": "", "resume": "",
    "parent": "main", "parent_behind": 2, "land_ready": false
  }
]
```
single-repo 모드도 `repo`/`repo_root` 항상 포함(전 entry 동일값) → 기존 소비자는 무시. flat 구조 불변.

## TUI mockup
```
┌─ gk fleet · 4 repos · 12 worktrees ──────────────── 12:04:20 ─┐
│ ▼ aic-rust (3)                            ⚠ conflict · 15m    │
│   ★ develop        conflict   paused rebase 2/5  [gk continue]│
│     feature/ollama clean                  2h                  │
│     experiment     dirty      staged:1    15m                 │
│ ▼ gk (5)                                  ↑2 · 2m             │
│   ★ main           ahead 2    clean       2m                  │
│     fix-bug        dirty      unstaged:3  just now            │
│     improve-ux     ahead 52   clean       11d                 │
│ ▶ web-frontend (2)                        clean · 5h         │
│ ▶ docs-site (2)                           ↑1 · 1d            │
├───────────────────────────────────────────────────────────────┤
│ j/k:move  space:fold  enter:detail  w:watch  r:refresh  q:quit│
└───────────────────────────────────────────────────────────────┘
```
정렬: repo_root 알파벳 → 그룹 내 current 먼저 → branch. roll-up: `conflict>paused>dirty>diverged>ahead>behind>clean`.

## 성능
- repo 간 병렬(전역 semaphore `min(NumCPU,8)`), repo 내 worktree는 기존 `gatherFleet` 병렬 재사용.
- per-repo context 3s timeout → 초과 시 직전 스냅샷 유지 + `⚠ stale`.
- repo>10이면 폴링 2s→4s 자동 백오프. local-only(fetch 없음).
- 실측 근거(kiro): 5 repo × 3 wt = 15 entry, `status --porcelain`+`rev-list --count` 각 ~20ms/SSD → ~300ms, 2s의 15%.

## 구현 단계 (gatherFleet 재사용)
1. `internal/cli/fleet_discover.go` — `discoverRepos(scan, repos, depth, exclude)`: explicit 우선, scan 재귀(.git 탐지), `EvalSymlinks` 중복 제거, submodule/non-git 스킵, exclude glob.
2. `gatherFleetMulti(ctx, roots)` — 전역 semaphore로 repo 병렬, 각 repo는 기존 `gatherFleet` 호출 후 `repo`/`repo_root` 주입, per-repo timeout, flat concat + 정렬.
3. `fleetEntryJSON`에 `Repo`/`RepoRoot string` 추가(omitempty 없이). `--json`/envelope 경로 공유.
4. TUI 그룹 렌더 — `repoGroup{name,root,entries,collapsed,rollup}`, `space` 토글, 커서키 `(repo_root,path)`, narrow 자동 접기.
5. drill-down — `w`/`enter`에서 `tea.ExecProcess(gk st --watch --repo <root|path>)` 후 복귀(2차: split-pane).
6. config `FleetConfig` + yaml.Node 주석 보존, 모드 진입 분기, 테스트(discover 심링크/중첩/exclude, multi gather timeout, JSON 후방호환), README/docs 갱신.
```
