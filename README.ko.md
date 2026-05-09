<p align="center">
  <img src="assets/gk-logo.jpeg" alt="gk" width="520">
</p>

<p align="center">
  <a href="README.md">English</a> · <strong>한국어</strong>
</p>

# gk — git 도우미

매일 쓰는 pull/log/status/branch 작업을 위한 작은 Go git 도우미입니다. 두 가지를 우선합니다. 위험한 작업은 되돌릴 수 있게(reflog 기반 undo, 타임머신 복원, 정책-as-code), 진단은 손에 잡히게(`doctor`, `precheck`, `sync`).

[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![Release](https://img.shields.io/github/v/release/x-mesh/gk)](https://github.com/x-mesh/gk/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## 왜 gk인가?

- **기본값이 안전한 push.** `gk push`는 보낼 커밋 diff에서 AWS, GitHub, Slack, OpenAI 키와 PEM 본문을 스캔합니다. 보호된 브랜치를 force-push하려면 브랜치 이름을 직접 타이핑해야 합니다.
- **HEAD 타임머신.** `gk timemachine list`는 복구할 수 있는 모든 상태(reflog + gk 백업 refs)를 보여줍니다. `gk timemachine restore <sha|ref>`은 리셋 전에 먼저 백업 ref를 남기고, 필요하면 autostash도 합니다. rebase/merge 진행 중이면 실행을 거부합니다.
- **Reflog 기반 undo.** `gk undo`는 reflog에서 과거 HEAD를 골라(fzf 또는 번호 픽커) 리셋하고 `refs/gk/undo-backup/<branch>/<unix>`에 백업 ref를 남깁니다. undo를 되돌리고 싶다면 같은 명령을 한 번 더 실행하면 됩니다.
- **정책-as-code.** `gk guard check`는 저장소 정책 규칙(시크릿 스캔, 커밋 크기, 필수 트레일러 등)을 병렬로 평가합니다. `gk guard init`은 주석 처리된 스텁을 `.gk.yaml`에 깔아주므로 필요한 항목만 풀어 쓰면 됩니다. `gk hooks install --pre-commit`으로 pre-commit에 연결합니다.
- **드라이런 병합.** `gk precheck <target>`은 `git merge-tree`를 돌려 작업 트리를 건드리지 않고 충돌 경로를 보고합니다. CI는 충돌 시 exit 3을 받습니다.
- **로컬 우선 rebase.** `gk sync`는 네트워크를 건드리지 않고 로컬 `<base>` 위로 현재 브랜치를 rebase합니다. 네트워크까지 같이 돌리고 싶으면 `gk sync --fetch` 한 번이면 끝납니다. 로컬 `<base>`가 `<remote>/<base>`보다 뒤처져 있으면 stale-base 힌트가 뜹니다.
- **분기된 pull 안전망.** 히스토리가 분기된 상태에서 `gk pull`은 로컬 SHA를 조용히 덮어쓰지 않고 멈춰서 묻습니다. 선택지는 `--rebase`, `--merge`, `--fetch-only`이고, `pull.strategy` 설정(또는 직접 플래그)으로 게이트를 우회할 수 있습니다. 히스토리를 다시 쓰는 모든 작업은 먼저 `refs/gk/backup/<branch>/<ts>` ref를 남깁니다.
- **Conventional Commits 인식 훅.** `gk hooks install`은 `commit-msg` → `gk lint-commit`, `pre-push` → `gk preflight`, `pre-commit` → `gk guard check`를 연결합니다. 관리되는 훅에는 마커가 있어 재설치는 idempotent하고, 외부 훅은 `--force` 없이는 덮어쓰지 않습니다.
- **한눈에 보는 상태.** `gk doctor`는 git 버전, pager, `$EDITOR`, 설정 유효성, 훅 상태, gitleaks 설치 여부, gk 백업 ref 누적에 대해 PASS/WARN/FAIL을 보고합니다. WARN/FAIL 줄마다 복사해 붙여넣을 수 있는 수정 명령이 같이 출력되며, `--verbose`를 붙이면 옵셔널 AI 통합 항목도 함께 진단합니다.
- **설치 방식과 무관한 self-update.** `gk update`는 현재 실행 중인 바이너리 경로를 보고 Homebrew, `install.sh`, `go install` 중 어떤 경로로 깔렸는지 판단해 알맞게 분기합니다. brew면 `brew upgrade x-mesh/tap/gk`로 위임, manual이면 릴리즈에서 `gk_<os>_<arch>.tar.gz`를 받아 `checksums.txt`로 sha256을 검증한 뒤 같은 디렉토리에서 atomic rename(`/usr/local/bin`처럼 권한이 부족하면 sudo로 escalate, 이전 바이너리는 `.bak`으로 백업), go-install이면 `go install …@latest` 명령을 출력해 줍니다. `gk update --check`는 다운로드 없이 비교만 하고 새 버전이 있으면 exit 1을 반환해 cron/CI에서 게이트로 쓸 수 있습니다.
- **history에서 경로 제거.** 실수로 커밋한 DB 덤프나 시크릿 파일을 history까지 지우고 싶을 때, `.gitignore`에 경로만 추가하고 `gk forget` 한 번이면 됩니다. tracked-but-ignored 파일을 자동 감지해 `git filter-repo`(deprecated `filter-branch`의 후속)에 넘기고, rewrite 전에 backup ref와 평문 manifest를 남겨 `git update-ref --stdin`로 되돌릴 수 있게 합니다. 명시적 경로(`gk forget db/ secrets.json`)도 받고, `--dry-run`은 영향받는 커밋 수만 보여줍니다.
- **입문자를 위한 Easy Mode.** `--easy`(또는 `output.easy: true` / `GK_EASY=1`)는 git 용어를 한국어로 옮기되 원어를 괄호로 같이 보여주고(`commit` → `변경사항 저장 (commit)`), 상태 섹션에 이모지를 붙이고, 끝줄에 상황에 맞는 다음 단계 힌트를 끼워 넣습니다. 워킹 트리가 깨끗해도 ↑/↓ 카운터에 따라 `📤 서버에 올릴 커밋 N개 → gk push` 같은 안내가 떠서 무엇을 해야 할지 보입니다. `gk guide`는 Easy Mode와 별개로 동작하는 단계별 git 안내입니다.
- **할 일을 알려주는 에러.** 대부분의 에러는 두 번째 줄에 `hint:`로 다음에 칠 명령을 같이 출력합니다.

## 설치

### Homebrew tap (권장)

```bash
brew install x-mesh/tap/gk
# 나중에 업그레이드:
brew upgrade x-mesh/tap/gk
```

### Linux / 수동 다운로드

한 줄로 설치. OS/arch를 자동 감지하고 sha256을 검증합니다:

```bash
curl -fsSL https://raw.githubusercontent.com/x-mesh/gk/main/install.sh | sh
```

버전 고정이나 설치 경로 변경은 환경변수로:

```bash
curl -fsSL https://raw.githubusercontent.com/x-mesh/gk/main/install.sh \
  | GK_VERSION=v0.29.0 GK_INSTALL_DIR=$HOME/.local/bin sh
```

[GitHub Releases](https://github.com/x-mesh/gk/releases/latest)에서 직접 받을 수도 있습니다. 파일명에 버전이 없어서 URL이 릴리스마다 바뀌지 않습니다:

```bash
# linux amd64 (linux_arm64 / darwin_amd64 / darwin_arm64으로 교체 가능)
curl -fsSL https://github.com/x-mesh/gk/releases/latest/download/gk_linux_amd64.tar.gz \
  | sudo tar -xz -C /usr/local/bin gk
```

### go install

```bash
go install github.com/x-mesh/gk/cmd/gk@latest
```

**git ≥ 2.38** 이 필요합니다(`merge-tree --write-tree`용; `gk precheck`가 충돌 경로를 이름으로 열거하려면 ≥ 2.40 권장). 설치 후 `gk doctor`로 확인하세요.

### oh-my-zsh 사용자: alias 충돌

oh-my-zsh의 `git` 플러그인이 `gk`를 `gitk` 런처로 정의하여 `gk` 바이너리를 가립니다. oh-my-zsh 로드 후 `~/.zshrc`에 충돌 alias를 제거하세요:

```zsh
unalias gk gke 2>/dev/null
```

## 빠른 시작

```bash
# 일상 작업
gk clone JINWOO-J/playground # git@github.com:JINWOO-J/playground.git로 확장
gk pull                      # fetch + rebase, upstream 자동 감지
gk pull --strategy ff-only   # fast-forward only; 히스토리 분기 시 에러
gk merge main                # main 브랜치를 현재 브랜치로 병합 (precheck 포함)
gk sync                      # fetch + fast-forward only (rebase 없음)
gk status                    # 간결한 작업 트리 요약
gk next                      # 현재 상태를 쉬운 말로 설명하고 다음 행동 제안
gk log                       # 짧고 컬러풀한 커밋 로그

# 안전
gk precheck main     # main으로 드라이런 병합; 충돌 시 exit 3
gk push              # 시크릿 스캔 + 보호 브랜치 규칙 적용
gk undo              # reflog에서 과거 HEAD 선택하여 복원

# 타임머신
gk timemachine list          # 복구 가능한 모든 HEAD 상태 (reflog + 백업)
gk timemachine restore <sha> # 안전 리셋 — 먼저 백업 ref 기록

# 정책
gk guard init        # .gk.yaml에 주석 처리된 정책 스텁 생성
gk guard check       # 모든 정책 규칙 평가; exit 0/1/2

# 온보딩
gk doctor            # 환경 상태 보고 + 수정 명령
gk hooks install --all       # commit-msg + pre-push + pre-commit 훅 연결

# 규약
gk lint-commit --staged    # Conventional Commits 기준으로 커밋 메시지 검증
gk branch-check            # 브랜치 이름 규칙 적용
gk preflight               # 설정된 검사 순서 실행
gk ship dry-run            # squash/version/changelog/tag/push 플랜 미리보기
```

## 명령어

### 일상

| 명령어 | 별칭 | 설명 |
|---|---|---|
| `gk clone <owner/repo \| alias:owner/repo \| url>` | | 단축 URL 확장 clone. `owner/repo`는 기본 `git@github.com:owner/repo.git` (ssh, 설정 가능). `--ssh`/`--https` override. `clone.hosts`로 alias(`gl:`, `work:`). `clone.root`, `clone.post_actions: [hooks-install, doctor]` 옵션 지원. |
| `gk pull` | | fetch + upstream 통합. `--strategy rebase\|merge\|ff-only\|auto`; `@{u}` 우선 해석; HEAD가 이미 ancestor이면 `--ff-only`로 자동 전환 |
| `gk merge <target>` | | 대상 브랜치를 현재 브랜치로 병합 (precheck + AI plan 포함). `--plan-only`, `--no-ai`, `--ff-only`, `--no-ff`, `--no-commit`, `--squash`, `--autostash` 지원 |
| `gk sync` | | fetch + fast-forward only; `--all`로 모든 추적 브랜치 |
| `gk status` | `gk st` | 간결한 작업 트리 상태. `-f`/`--fetch`로 ↑N ↓N을 원격에서 갱신. `--ai`는 현재 상태와 다음 행동을 쉬운 말로 설명. Opt-in `--vis gauge,bar,progress,types,staleness,tree,conflict,churn,risk` 오버레이 |
| `gk log` | `gk slog` | 짧은 컬러 커밋 로그. `--pulse`, `--calendar`, `--tags-rule`, `--impact`, `--cc`, `--safety`, `--hotspots`, `--trailers`, `--lanes` 시각화 |

### 브랜치

| 명령어 | 별칭 | 설명 |
|---|---|---|
| `gk branch list` | | `--stale <N>` / `--merged` / `--unmerged` / `--gone` 필터로 브랜치 목록 |
| `gk branch clean` | | 보호 목록을 존중하며 병합된 브랜치 삭제; `--gone`으로 upstream이 삭제된 브랜치 대상 |
| `gk branch pick` | | 인터랙티브 브랜치 선택기 (비TTY 폴백 포함) |
| `gk branch-check` | | 설정된 패턴에 대해 현재 브랜치 이름 검증 |
| `gk switch [name]` | `gk sw` | 브랜치 전환; `-m`/`--main`으로 main 이동, `-d`/`--develop`으로 develop |

### Worktree

| 명령어 | 별칭 | 설명 |
|---|---|---|
| `gk worktree` (no sub) | `gk wt` | 인터랙티브 TUI — 목록/추가/삭제/진입. `cd` 선택 시 대상 디렉토리에서 `$SHELL` 서브셸을 열고(`exit`로 복귀), `--print-path`면 alias 패턴용으로 경로만 stdout 출력 |
| `gk worktree add <name>` | | 상대 경로는 `<worktree.base>/<worktree.project>/<name>`(기본 `~/.gk/worktree/<repo>/<name>`)에 배치, 절대 경로는 그대로 사용. 고아 브랜치 충돌 시 인라인 재사용/삭제/취소 선택 |
| `gk worktree list` | | `git worktree list --porcelain` 파싱 결과를 테이블 또는 `--json`으로 |
| `gk worktree remove <path>` | | 삭제; dirty/locked는 force 재확인, stale admin 엔트리는 자동 prune |

### 안전

| 명령어 | 설명 |
|---|---|
| `gk push` | 가드된 push: 시크릿 스캔 + 보호 브랜치 적용; `--force`는 `--force-with-lease`로 라우팅 |
| `gk ship` | 릴리즈 ship 게이트: status/dry-run/squash 모드, SemVer 추론, 버전/CHANGELOG 릴리즈 커밋, 가드된 브랜치/태그 push. 태그 push가 릴리즈 워크플로 트리거 |
| `gk precheck <target>` | `git merge-tree`로 드라이런 충돌 스캔; 충돌 시 exit 3; CI용 `--json` |
| `gk preflight` | 설정된 검사 순서 실행 (`commit-lint`, `branch-check`, `no-conflict`, 또는 쉘 명령) |
| `gk lint-commit` | Conventional Commits 기준으로 커밋 메시지 검증; `--staged`, `--file PATH`, `<rev-range>` |

### 정책

| 명령어 | 설명 |
|---|---|
| `gk guard check` | 모든 정책 규칙을 병렬로 평가; human 또는 `--json` 출력; exit 0 clean / 1 warn / 2 error |
| `gk guard init` | 완전히 주석 처리된 `policies:` 블록으로 `.gk.yaml` 스캐폴드; `--force` 덮어쓰기, `--out` 경로 지정 |

### 복구

| 명령어 | 별칭 | 설명 |
|---|---|---|
| `gk timemachine list` | | reflog + gk 백업 refs의 통합 타임라인; `--kinds`, `--json` (NDJSON) |
| `gk timemachine restore <sha\|ref>` | | 안전 리셋: 먼저 백업 ref 기록 후 리셋; `--mode soft\|mixed\|hard\|auto`, `--dry-run`, `--autostash` |
| `gk timemachine list-backups` | | gk 관리 백업 refs만; `--kind undo\|wipe\|timemachine`, `--json` |
| `gk timemachine show <sha\|ref>` | | 타임라인 항목의 커밋 헤더 + diff stat (또는 `--patch`) |
| `gk undo` | | Reflog 기반 HEAD 복원; `refs/gk/undo-backup/...`에 백업 ref 남김 |
| `gk reset` | | upstream으로 hard-reset; `--to-remote`는 `<remote>/<current>` 사용 |
| `gk forget [path...]` | | 전체 git history에서 경로 제거 (`git filter-repo` 위임). 인자 없으면 `.gitignore`로 가린 tracked 파일을 자동 감지. rewrite 전에 backup ref `refs/gk/forget-backup/<branch>/<unix>` (`gk timemachine list`에 함께 표시) + `.git/gk/` 아래 평문 manifest 작성. `--analyze`는 rewrite 없이 path별 통계, target이 없으면 repo 전체 audit으로 fallback합니다 (`--depth N` 기본 1, `--top N` 기본 20, HEAD에 없는 history-only bucket 표시). `--sort size\|churn\|name`으로 정렬 변경 (churn은 rewrite-heavy 경로 부각). `--bar=auto\|filled\|block\|none`으로 bar 스타일 (TTY에선 htop 스타일 in-bar label). `--json`으로 CI/대시보드용 안정 출력. `-i`/`--interactive`는 audit 결과에서 multi-select picker → 선택된 경로로 바로 rewrite. `--keep <glob>`은 제외 경로 (`filepath.Match`, 반복 가능). target 안의 dirty 파일은 통과시키고 외부 dirty는 `--force-dirty` 없이는 abort. `--dry-run`/`--yes` 지원. `git-filter-repo` 설치 필요 |
| `gk wipe` | | `reset --hard` + `clean -fd`; `refs/gk/wipe-backup/...`에 pre-wipe HEAD 백업 |
| `gk restore --lost` | | 댕글링 커밋/blob을 cherry-pick 힌트와 함께 표시 |
| `gk edit-conflict` | `gk ec` | 첫 번째 `<<<<<<<` 마커에서 에디터 열기 |
| `gk continue` | | 중단된 rebase/merge/cherry-pick 계속 |
| `gk abort` | | 중단된 rebase/merge/cherry-pick 취소 |
| `gk wip` / `gk unwip` | | 컨텍스트 전환용 임시 WIP 커밋 |

### AI

| 명령어 | 설명 |
|---|---|
| `gk commit` | WIP(staged+unstaged+untracked)를 AI CLI로 의미 있는 커밋 그룹으로 분할하고 Conventional Commit 메시지로 적용. `-f/--force` 리뷰 스킵, `--dry-run` 미리보기, `--abort` 마지막 백업 ref로 HEAD 복원. 아래 **AI commit** 섹션 참조 |
| `gk next` | 현재 저장소 상태를 쉬운 언어로 설명하고 다음에 실행할 안전한 명령을 제안. AI provider가 없으면 로컬 규칙 기반 플랜으로 폴백 |
| `gk pr` | 브랜치 커밋으로부터 구조화된 PR 설명(Summary, Changes, Risk Assessment, Test Plan) 생성. `--output clipboard`으로 클립보드 복사; `--dry-run`으로 프롬프트 미리보기 |
| `gk review` | staged 변경(`git diff --cached`) 또는 커밋 범위(`--range ref1..ref2`)에 대한 AI 코드 리뷰. `--format json`으로 구조화된 출력 |
| `gk changelog` | 커밋 범위에서 Conventional Commit 타입별로 그룹화된 changelog 생성. `--from`/`--to` ref 지정; 기본값은 최신 태그..HEAD |

### 온보딩 / 설정

| 명령어 | 설명 |
|---|---|
| `gk doctor` | 환경 상태 보고 (git/pager/editor/config/hooks/gitleaks/backup-refs); `--verbose`로 AI 프로바이더 행 추가; `--json` for CI |
| `gk update [--check] [--force] [--to <vX.Y.Z>]` | self-update. `os.Executable()` 결과로 brew / `install.sh` / `go install`을 구분해 분기합니다. brew → `brew upgrade x-mesh/tap/gk` 위임; manual → 릴리즈에서 `gk_<os>_<arch>.tar.gz` 받아 `checksums.txt`로 검증 후 atomic rename(필요 시 sudo escalate, 이전 바이너리는 `.bak` 백업); go-install → `go install …@latest` 명령 출력. `--check`는 다운로드 없이 비교만 (새 버전 있으면 exit 1), `--to`는 manual 설치에서 특정 태그 핀 |
| `gk init [--only <target>] [--kiro] [--force]` | 프로젝트 분석 후 `.gitignore`, `.gk.yaml`, AI 컨텍스트 파일(`CLAUDE.md`, `AGENTS.md`)을 한 번에 스캐폴드. `--only gitignore\|config\|ai`로 범위 제한; `--kiro`는 `.kiro/steering/`도 함께 작성. 인터랙티브 huh 폼이 작성 전 플랜을 미리 보여줌 |
| `gk config init [--force] [--out <path>]` | `$XDG_CONFIG_HOME/gk/config.yaml`에 주석 달린 YAML 템플릿 스캐폴드 (첫 `gk` 실행 시 자동 생성; `GK_NO_AUTO_CONFIG=1`로 비활성화). `gk init config` 별칭은 호환용으로 유지 |
| `gk hooks install [--commit-msg\|--pre-push\|--pre-commit\|--all] [--force]` | `.git/hooks/` 아래 gk 관리 훅 심 설치 (`--pre-commit`은 `gk guard check` 연결) |
| `gk hooks uninstall [...]` | gk 관리 훅 제거 (외부 훅은 삭제 거부) |
| `gk config show` | 완전히 해석된 설정을 YAML로 출력 |
| `gk config get <key>` | 점 경로로 단일 설정 값 출력 |

전체 플래그 참조는 [docs/commands.md](docs/commands.md), 릴리즈별 상세 내용은 [CHANGELOG.md](CHANGELOG.md)를 참조하세요.

## AI commit

`gk commit`은 현재 작업 트리(staged + unstaged + untracked)를 살펴본 뒤 AI에게 의미 단위로 묶어 달라고 요청하고, 그룹마다 Conventional Commit 하나씩 만들어 적용합니다.

### Provider 설치

`anthropic`, `openai`, `nvidia`, `groq`은 HTTP로 직접 API를 호출합니다. 환경변수 키는 그대로 provider에 전달되고 gk가 따로 보관하지 않습니다. `gemini`, `qwen`, `kiro-cli`는 이미 설치된 CLI를 subprocess로 띄우고 인증은 그쪽에 맡깁니다.

| Provider | 설치 | 인증 |
|---|---|---|
| `anthropic` (Anthropic Claude) — **기본값** | 바이너리 불필요 | `export ANTHROPIC_API_KEY=...` |
| `openai` (OpenAI) | 바이너리 불필요 | `export OPENAI_API_KEY=...` |
| `nvidia` (NVIDIA) | 바이너리 불필요 | `export NVIDIA_API_KEY=...` |
| `groq` (Groq) | 바이너리 불필요 | `export GROQ_API_KEY=...` |
| `gemini` (Google) | `npm i -g @google/gemini-cli` 또는 `brew install gemini-cli` | `export GEMINI_API_KEY=...` 또는 `gemini` 최초 실행 시 OAuth |
| `qwen` (Alibaba) | `npm i -g @qwen-code/qwen-code` | `qwen auth qwen-oauth` 또는 `export DASHSCOPE_API_KEY=...` |
| `kiro-cli` (AWS Kiro headless, `kiro` IDE 런처와는 다름) | [kiro.dev/docs/cli/installation](https://kiro.dev/docs/cli/installation) | `export KIRO_API_KEY=...` (Kiro Pro) 또는 IDE OAuth |

자동 감지 순서(`ai.provider`가 비어 있을 때): `anthropic → openai → nvidia → groq → gemini → qwen → kiro-cli`. `--provider`를 명시하지 않으면 fallback chain이 이 순서대로 시도하고, 실패하면 다음 provider로 넘어갑니다.

`gk doctor --verbose` 출력에서 각 provider 설치/인증 상태를 확인할 수 있습니다.

### 플래그

```
gk commit [flags]

      --abort                      마지막 ai-commit 백업 ref로 HEAD 복원 후 종료
      --allow-secret-kind strings  지정한 종류의 secret 검출을 무시 (반복 가능)
      --ci                         CI 모드: --force 또는 --dry-run 필수, 프롬프트 금지
      --dry-run                    플랜만 출력하고 커밋하지 않음
  -f, --force                      대화형 리뷰 없이 바로 커밋
      --include-unstaged           unstaged + untracked 포함 (기본값)
      --lang string                ai.lang 오버라이드 (en|ko|...)
      --provider string            ai.provider 오버라이드 (anthropic|openai|nvidia|groq|gemini|qwen|kiro)
      --staged-only                스테이지된 변경만 대상
  -y, --yes                        모든 프롬프트 자동 수락 (비-TTY에선 --force 별칭)
```

### 설정

```yaml
# .gk.yaml (또는 ~/.config/gk/config.yaml)
ai:
  enabled: true              # 전역 off-switch. GK_AI_DISABLE=1 로도 비활성화 가능
  provider: ""               # "" = 자동 감지 (anthropic → openai → nvidia → groq → gemini → qwen → kiro-cli)
  lang: "en"                 # 메시지 언어 (BCP-47)
  anthropic:                 # Anthropic Claude — HTTP 직접 호출 (Messages API), 바이너리 불필요
    # model: "claude-sonnet-4-5-20250929"  # 기본값
    # endpoint: "https://api.anthropic.com/v1/messages"
    # timeout: "60s"
  openai:                    # OpenAI — HTTP 직접 호출 (Chat Completions), 바이너리 불필요
    # model: "gpt-4o-mini"  # 기본값
    # endpoint: "https://api.openai.com/v1/chat/completions"
    # timeout: "60s"
  nvidia:                    # NVIDIA provider — HTTP 직접 호출, 바이너리 불필요
    # model: "meta/llama-3.1-8b-instruct"  # 기본값
    # endpoint: "https://integrate.api.nvidia.com/v1/chat/completions"
    # timeout: "60s"
  groq:                      # Groq provider — HTTP 직접 호출 (OpenAI 호환), 바이너리 불필요
    # model: "llama-3.3-70b-versatile"  # 기본값
    # endpoint: "https://api.groq.com/openai/v1/chat/completions"
    # timeout: "60s"
  commit:
    mode: "interactive"      # interactive | force | dry-run (CLI 플래그가 오버라이드)
    max_groups: 10
    max_tokens: 24000
    timeout: "30s"
    allow_remote: true       # false 이면 remote Locality provider 전체 차단
    trailer: false           # true 이면 각 커밋에 "AI-Assisted-By: <provider>@<version>" trailer
    audit: false             # true 이면 .git/gk-ai-commit/audit.jsonl 에 JSONL 기록
    deny_paths:              # 프로세스를 떠나기 전 무조건 스킵되는 glob 목록
      - ".env"
      - ".env.*"
      - "*.pem"
      - "id_rsa*"
      - "credentials.json"
      - "*.pfx"
      - "*.kdbx"
      - "*.keystore"
      - "service-account*.json"
      - "terraform.tfstate"
      - "terraform.tfstate.*"
  assist:
    mode: "off"              # off | suggest | auto
    status: true             # true이면 gk status --ai / gk next 활성
    include_diff: false      # 현재 status assistant는 patch 본문을 보내지 않음
```

### 안전 장치

- **Secret gate.** `internal/secrets.Scan`과 `gitleaks`(설치돼 있으면)가 payload를 함께 훑습니다. 하나라도 걸리면 `--force` 여부와 상관없이 abort합니다. 특정 종류만 이번 실행에서 무시하려면 `--allow-secret-kind <kind>`.
- **Privacy gate.** remote provider(`Locality=remote`)로 나가는 payload는 자동으로 정리됩니다. secret, `deny_paths` 매치, 민감 패턴이 `[SECRET_1]`, `[PATH_1]` 같은 토큰으로 치환되고, 한 payload에서 10개를 넘게 감지하면 abort합니다. `--show-prompt`로 redact된 payload를 확인할 수 있고, `ai.commit.audit`이 켜져 있으면 `.gk/ai-audit.jsonl`에 기록이 남습니다.
- **Deny paths.** `.env`, 비공개 키, tfstate 같은 매칭 파일은 payload가 프로세스를 떠나기 전에 빠집니다.
- **git-state 차단.** rebase/merge/cherry-pick이 진행 중이면 `gk commit`은 아예 실행하지 않습니다. `MERGE_MSG`를 덮어쓰지 않기 위함입니다.
- **Backup ref.** 매 실행마다 커밋 전에 `refs/gk/ai-commit-backup/<branch>/<unix>`를 기록합니다. 실패하면 `gk commit --abort`로 거기로 HEAD를 되돌립니다.
- **Conventional lint 루프.** 생성된 메시지를 `commitlint.Lint`로 검증하고, 실패하면 직전 lint 이슈를 프롬프트에 끼워 넣어 최대 2회까지 provider에 다시 묻습니다.
- **Path-rule override.** `_test.go`, `docs/*.md`, CI yaml, lock 파일은 provider가 다른 type을 골라도 각각 `test` / `docs` / `ci` / `build`로 재지정합니다.

### 간단 예시

```bash
# 계획만 보기
gk commit --dry-run

# 바로 커밋 (TUI 없이)
gk commit --force --provider gemini

# 중간 실패 복구
gk commit --abort
```

## pr / review / changelog

이 명령들은 provider의 **Summarizer** 기능을 사용합니다. 모든 내장 provider(`anthropic`, `openai`, `nvidia`, `groq`, `gemini`, `qwen`, `kiro-cli`)가 Summarizer를 구현합니다.

### `gk pr`

현재 브랜치의 커밋으로부터 base 브랜치 대비 구조화된 PR 설명을 생성합니다.

```bash
gk pr                          # stdout으로 출력
gk pr --output clipboard       # 클립보드에 복사
gk pr --dry-run                # 프롬프트 미리보기
gk pr --provider nvidia --lang ko
```

플래그: `--output` (stdout|clipboard), `--dry-run`, `--provider`, `--lang`

### `gk review`

staged 변경 또는 커밋 범위에 대한 AI 코드 리뷰.

```bash
gk review                      # staged diff 리뷰
gk review --range main..HEAD   # 커밋 범위 리뷰
gk review --format json        # 구조화된 JSON 출력
```

플래그: `--range`, `--format` (text|json), `--dry-run`, `--provider`

### `gk changelog`

커밋 범위에서 Conventional Commit 타입별로 그룹화된 changelog를 생성합니다.

```bash
gk changelog                   # 최신 태그..HEAD, markdown
gk changelog --from v1.0.0 --to v1.1.0
gk changelog --format json
```

플래그: `--from`, `--to`, `--format` (markdown|json), `--dry-run`, `--provider`

## 전역 플래그

| 플래그 | 설명 |
|---|---|
| `-d, --debug` | 진단 로그(서브프로세스 호출, 재시도 사유, 타이밍)를 stderr로 출력. `GK_DEBUG=1` 환경변수도 인식. 각 줄은 시작 이후 경과 시간 prefix가 붙어 어느 단계에서 시간이 걸리는지 한눈에 파악 |
| `--dry-run` | 실행 없이 동작 출력 |
| `--easy` | 이번 호출만 Easy Mode 활성화 (한국어 용어 번역 + 이모지 + 힌트). `GK_EASY=1`과 동등 |
| `--no-easy` | 설정/환경변수가 켜져 있어도 이번 호출만 Easy Mode 비활성화 |
| `--json` | 지원되는 경우 JSON 출력 |
| `--no-color` | 컬러 출력 비활성화 |
| `--repo <path>` | git 저장소 경로 (기본값: 현재 디렉토리) |
| `--verbose` | 상세 출력 |

### Easy Mode 환경 변수

| 변수 | 기본값 | 설명 |
|---|---|---|
| `GK_EASY` | unset | `1` / `true`로 Easy Mode 전역 활성화, `0` / `false`로 강제 비활성화 |
| `GK_LANG` | `ko` | 메시지 카탈로그 언어 (BCP-47 short code; `en`/`ko` 제공) |
| `GK_EMOJI` | `true` | 상태 섹션에 이모지 붙이기 (`📦` / `✏️` / `💡` 등) |
| `GK_HINTS` | `verbose` | 힌트 상세도: `verbose` / `minimal` / `off` |

## 설정

gk는 우선순위 순서로 여러 소스에서 설정을 읽습니다 (높은 것이 우선):

1. CLI 플래그
2. `GK_*` 환경 변수
3. `git config gk.*` 항목
4. 저장소 루트의 `.gk.yaml`
5. `~/.config/gk/config.yaml` (XDG)
6. 내장 기본값

모든 필드는 [docs/config.md](docs/config.md)를 참조하세요. 샘플 설정은 [examples/config.yaml](examples/config.yaml)에 있습니다.

## 종료 코드

| 코드 | 의미 |
|:-:|---|
| 0 | 성공 |
| 1 | warn (`gk guard check`: 경고 수준 위반) / 일반 에러 |
| 2 | 잘못된 입력 / `gk guard check`: 에러 수준 위반 |
| 3 | 충돌 (merge/rebase/precheck) |
| 4 | 분기됨 (fast-forward 불가) |
| 5 | 네트워크 에러 |

스크립트는 릴리즈 간 안정성을 보장받을 수 있습니다.

## 개발

```bash
git clone https://github.com/x-mesh/gk.git
cd gk

make build          # bin/gk에 출력
make test           # go test ./... -race -cover
make lint           # golangci-lint run
make fmt            # gofmt + go mod tidy
```

Go 1.25+ 및 git 2.38+가 필요합니다.

## 라이선스

[MIT](LICENSE)
