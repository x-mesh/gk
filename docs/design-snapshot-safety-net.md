# Design: `gk snapshot` — 비파괴 안전망

상태: Phase 1 구현됨 (develop, 미릴리스) · 2026-06-09 · 대상: 자동 작업 보존

## 1. 배경 (WHY)

"WIP 커밋을 자동으로 만들어주는 hook"이 있었다는 인식이 있었으나, git hook /
Claude Code hook 양쪽 모두 그런 것은 존재한 적이 없다. WIP은 항상 수동
`gk wip`(= oh-my-zsh `gwip`) 또는 git alias였다.

자동 안전망을 만들려면 핵심 제약이 하나 있다: **`gk wip`처럼 HEAD에 커밋하면
안 된다.** 주기적으로 HEAD에 WIP 커밋이 쌓이면 브랜치 히스토리가 오염되고
rebase/push 사고가 난다. 자동 안전망은 reflog 같은 **shadow 스냅샷** —
작업트리를 건드리지 않고 별도 ref에만 시계열로 기록하는 것 — 이어야 한다.

`gk wip`과 `gk snapshot`의 역할은 명확히 분리된다:

| | `gk wip` | `gk snapshot` |
|---|---|---|
| 저장 위치 | HEAD (브랜치 히스토리) | `refs/wip/<branch>` (shadow ref) |
| 작업트리/인덱스 | `git add -A` + 커밋 (변경) | **미변경** |
| 의도 | 의도적 체크포인트 (컨텍스트 전환) | 자동 안전망 (작업 보존) |
| push 영향 | 브랜치에 딸려감 | 안 딸려감 |
| 되돌리기 | `gk unwip` (HEAD~1 reset) | `gk snapshot restore` |

## 2. shadow-ref 모델

```
refs/wip/<branch>            # 최신 스냅샷 (ref가 가리키는 커밋)
refs/wip/<branch>@{0}        # 최신
refs/wip/<branch>@{1}        # 그 이전 … (reflog가 시계열 이력)
```

`refs/heads/*` 밖에 살기 때문에:
- `git branch`에 안 보이고, 기본 refspec으로 **push되지 않으며**, rebase와
  무관하다.
- reflog 덕분에 `git gc`가 최근 스냅샷을 회수하지 않는다.

### 2.1 캡처 (untracked 포함, 인덱스 무손상)

`git stash create`는 tracked 변경만 담는다 — 새로 만든 파일(untracked)이
빠지는데, 그게 가장 잃기 쉽고 보호 가치가 높다. 그래서 임시 인덱스를 쓴다:

```
tmp=$(mktemp .git/gk-snapshot-index-XXXX)   # 경로만 확보 후 삭제
GIT_INDEX_FILE=$tmp git add -A              # 빈 인덱스 기준 → 워킹트리 전체
tree=$(GIT_INDEX_FILE=$tmp git write-tree)
commit=$(git commit-tree $tree -p HEAD -m "gk snapshot: <note>")
git update-ref --create-reflog -m "<note>" refs/wip/<branch> $commit
```

- 빈 임시 인덱스에 `add -A` → 현재 워킹트리 그대로(modified·deleted·untracked),
  **`.gitignore` 존중**(venv/db 등 노이즈 제외). 실제 인덱스/작업트리는 무손상.
- Go에서는 `git.ExecRunner.ExtraEnv`로 `GIT_INDEX_FILE`을 주입한다.
- 임의 ref는 reflog가 자동 생성되지 않으므로 `update-ref --create-reflog` 필수.

### 2.2 복원

```
gk snapshot restore [n]   # n 생략 시 0 (최신)
```

- **target `@{n}`을 백업 전에 SHA로 고정한다.** dirty일 때 아래 auto-backup이
  reflog에 새 항목을 push하면 모든 `@{i}`가 한 칸씩 밀리므로, 먼저 rev-parse로
  커밋을 pin하지 않으면 엉뚱한 스냅샷을 복원하게 된다. (구현 중 실제로 잡은 버그)
- dirty면 현재 상태를 새 스냅샷으로 먼저 백업 → 아무것도 잃지 않는다.
- `git checkout <sha> -- :/`로 적용. **지금 있고 스냅샷에 없는 파일은 보존**
  (완전 일치가 아니라 "잃은 것 되찾기"가 목적).
- detached HEAD는 앵커할 ref가 없어 거부한다.

## 3. 명령 인터페이스

```
gk snapshot [-m <note>] [-q]   # 저장 (-q: 출력 억제, hook용)
gk snapshot list   (= gk snapshots)
gk snapshot restore [n] [-m <note>]
```

`restore`/`runRestore`가 이미 `restore.go`·`timemachine.go`에 점유되어 있어,
top-level `restore`와 충돌하지 않도록 `snapshot` 부모 + 서브커맨드 구조를 썼다.

구현: `internal/cli/snapshot.go` · 테스트: `internal/cli/snapshot_test.go` (5개)
· Easy-mode 도움말: `help_easy_data.go` 4개 항목.

## 4. Phase 2 — 트리거 자동화 (미적용)

Phase 1은 명령만 제공한다. 진짜 "자동" 안전망이 되려면 트리거가 필요하다.
**아래는 설계 메모일 뿐, 아직 적용하지 않았다.**

### 4.1 Claude Code Stop hook (권장, 가장 단순)

AI 턴이 끝날 때마다 스냅샷. `~/.claude/settings.json`:

```json
{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": "gk snapshot -q", "timeout": 10 } ] }
    ]
  }
}
```

- 장점: 설치 한 줄, AI 작업 단위 스냅샷. 비용 거의 0.
- 한계: Claude Code 세션 안에서만 — 에디터 직접 편집은 안 잡힌다.
- 주의: 이 프로젝트의 기존 Stop hook(`mem-mesh-stop-decide.sh`)과 **공존**하도록
  배열에 항목을 추가하는 형태여야 한다(덮어쓰기 금지).

### 4.2 시간 기반 watch 데몬

`gk autowip start` 같은 백그라운드 프로세스가 N분/N변경마다 `gk snapshot -q`.

- 장점: 에디터·도구 무관 진짜 자동.
- 비용: LaunchAgent(macOS)/systemd 생명주기 관리.

### 4.3 보존 정책 / 부가 기능

- `refs/wip/<branch>` reflog를 7일 후 자동 expire(`git reflog expire`)해 누적 방지.
- `gk restore --diff`로 스냅샷 ↔ 현재 비교.
- `git hook`은 부적합 — 커밋 시점에만 트리거되어 "자동 스냅샷"과 모순.
