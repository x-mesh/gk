# gk worktree panel gate plan

## 한 줄 요약
`gk`는 feature worktree를 만들고, 부모 브랜치(`develop` 등)로 합치는 과정을 직렬화하며, merge 전후에 외부 quality gate(`xm panel` 포함)를 실행할 수 있는 deterministic git 레이어를 제공한다. `xm`의 PRD/DAG/agent orchestration은 `gk` 위에서 호출하고, `gk`는 모델 판단이나 task scheduling을 직접 소유하지 않는다.

## 목표
- `main -> develop -> many feature branches` 구조에서 여러 터미널/에이전트가 feature worktree를 병렬로 작업하게 한다.
- 완료된 feature는 `develop`으로 merge하기 전에 patch 단위 review gate를 통과하게 한다.
- `develop`으로 합친 직후 integration patch를 다시 검토할 수 있게 한다.
- merge 대상 브랜치 업데이트는 lock으로 직렬화해서 병렬 feature 작업이 서로 간섭하지 않게 한다.
- gate가 검토하는 patch가 실제로 merge되는 patch와 정확히 일치하게 한다(lock 하에 SHA를 고정).
- agent mode JSON 계약(`{state, ok, result, error}`)을 유지한다.

## 비목표
- `xm panel`의 verdict semantics를 `gk`에 내장하지 않는다.
- `x-build`의 PRD, DAG, task dependency, agent fan-out을 `gk`가 재구현하지 않는다.
- remote PR 생성, release, push 정책은 이 기능의 기본 scope가 아니다.

## 사용자 흐름
```bash
# feature worktree 준비
GK_AGENT=1 git-kit worktree acquire feat/search-index --from develop

# 작업 후 현재 feature worktree에서 완료 처리
GK_AGENT=1 git-kit worktree finish \
  --to develop \
  --gate "xm panel {patch} --json" \
  --gate-phase before \
  --cleanup
```

integration patch까지 확인하는 경우:

```bash
GK_AGENT=1 git-kit worktree finish \
  --to develop \
  --gate "xm panel {patch} --json" \
  --gate-phase both \
  --cleanup
```

## CLI 제안
기존 `git-kit worktree finish`를 확장한다.

```bash
git-kit worktree finish \
  [--to <branch|parent|base>] \
  [--gate <command-template>] \
  [--gate-arg <token>]... \
  [--gate-phase before|after|both] \
  [--gate-timeout <duration>] \
  [--gate-keep-patch] \
  [--cleanup] \
  [--delete-branch] \
  [--push]
```

`--gate`는 흔한 경우를 위한 축약이고, 완전한 argv 제어가 필요하면 반복형 `--gate-arg`를 쓴다(아래 gate 실행 모델 참고).

편의 shorthand는 2단계 이후에 추가한다.

```bash
git-kit worktree finish --to develop --panel-review --gate-phase before
```

`--panel-review`는 아래 template의 alias로만 동작한다.

```bash
xm panel {patch} --json
```

이렇게 하면 `gk`는 `xm`에 hard dependency를 갖지 않고, 다른 reviewer나 자체 script도 gate로 쓸 수 있다.

## gate template 변수
```text
{patch}              임시 patch 파일 경로
{source}             source branch 이름
{target}             target branch 이름
{base_sha}           merge-base(source, target) SHA
{head_sha}           source HEAD SHA
{target_before_sha}  merge 직전에 lock 하에 고정된 target SHA
{target_after_sha}   merge 직후 target SHA (after phase에서만 채워짐)
{phase}              before 또는 after
```

`{base_sha}`는 언제나 `merge-base(source, target)` 하나만 의미한다. merge 직전/직후의 target tip은 각각 `{target_before_sha}`/`{target_after_sha}`로 분리했고, 이는 상태 파일 필드와 1:1로 대응한다.

예:

```bash
GK_AGENT=1 git-kit worktree finish \
  --to develop \
  --gate "xm panel {patch} --json --timeout 600" \
  --gate-phase before
```

## gate 실행 모델
`--gate` template은 shell을 거치지 않는다. gk는 template을 공백 기준으로 argv token list로 분해하고(이 tokenizer는 신규 코드다), 각 `{token}`을 정확히 하나의 argv 원소로 치환한 뒤 `exec.CommandContext(ctx, argv[0], argv[1:]...)`로 실행한다. exec 자체는 `gk worktree run -- <command> [args...]`가 이미 쓰는 no-shell 계약(`internal/cli/worktree_run.go`, "run directly, not through a shell")을 그대로 따른다 — 다만 `worktree run`은 OS가 미리 쪼갠 argv를 그대로 받으므로 단일 문자열을 tokenize하지 않는다는 점이 다르다(즉 shell을 안 거치는 exec 부분만 공유하고, 문자열 tokenizer는 gate 전용 신규 코드다). `cmd.Dir`은 feature worktree, `cmd.Env`는 `os.Environ()`이다.

- 치환된 값에 공백이나 shell metacharacter가 있어도 재분해하지 않는다(한 argv 원소 유지). 따라서 word-splitting/shell injection 경로가 없다 — 이것이 테스트 계획의 "argv 전달" 항목을 뒷받침하는 실제 실행 모델이다.
- pipe/redirect/`&&` 같은 shell operator가 필요하면 사용자가 명시적으로 `sh -c`를 argv로 넣는다: `--gate-arg sh --gate-arg -c --gate-arg "xm panel {patch} | tee log"`. 기본 경로는 절대 `sh -c`로 문자열을 보간하지 않는다. `sh -c` 보간 모델은 worktree.init `run:` step 전용(`internal/cli/worktree_init.go`)이고, gate string에 그대로 쓰면 quoting/injection 위험이 생긴다.
- `--gate "..."`는 gk 자체 tokenizer로만 분해되며(shell metacharacter 해석 없음), 세밀한 인용이 필요하면 반복형 `--gate-arg <token>`이 canonical 형태다.

## before gate 동작
1. 현재 branch, target branch, parent metadata를 `finishChildArgs`로 해석한다. target은 `effectiveTo`(parent/base/explicit → 구체 브랜치)로 확정된다.
2. worktree 상태를 검사하고 기존 `finish`/`promote` 규칙대로 commit 또는 block한다.
3. **target lock을 먼저 잡는다.** lock key는 repo common-dir + `effectiveTo`이고, `runWorktreeFinish`(`internal/cli/worktree_agent.go`)에서 `finishChildArgs`가 `effectiveTo`를 돌려준 직후·integration self-exec(`landRunChild`) 이전에 획득한다. (아래 lock 정책 참고 — 이 lock은 신규 코드다.)
4. **lock 하에서 target-before SHA를 고정한다.** `refs/heads/<target>`은 target이 어느 worktree에 checkout됐든(또는 bare든) 언제나 target tip을 가리키므로, 케이스 분기 없이 항상 `gitsafe.ResolveRef`(`git rev-parse --verify refs/heads/<target>^{commit}`, `runMergeIntoBare`/`BranchFF`가 쓰는 oldSHA와 동일)로 읽는다. feature worktree의 `headRev`(`git rev-parse HEAD`)는 **source tip**을 가리키므로 여기서는 절대 쓰지 않는다(그걸 쓰면 baseline이 오염됨). 이 값이 상태 파일 `target_before_sha`이며 이후 모든 patch의 기준선이다.
5. 고정된 SHA를 기준으로 source patch를 생성한다.

```bash
git diff --binary <target_before_sha>...HEAD > <patch>
```

6. gate 실행 모델대로 `--gate` command를 argv로 실행한다.
7. gate가 non-zero이거나 timeout이면 merge하지 않고 lock을 해제한 뒤 `state:"blocked"`로 종료한다. target은 변경되지 않았으므로 언제나 `blocked`이며 `paused`가 아니다.
8. gate가 통과하면 **lock을 유지한 채** 기존 `runMergeInto` 경로로 진행한다. 병렬 finish의 직렬화를 실제로 보장하는 것은 이 lock이다 — target 단위 lock이 잡혀 있어 다른 finish는 임계 구역(SHA 고정 → patch → gate → merge)에 진입하지 못하므로, gate가 승인한 patch와 실제 merge되는 patch가 어긋날 수 없다. bare 경로에서는 `BranchFF`의 CAS(`update-ref -m <reason> refs/heads/<target> newSHA oldSHA`)가 backstop으로 한 번 더 방어한다 — 단 `BranchFF`는 `oldSHA`를 넘겨받는 게 아니라 ref에서 다시 resolve하므로, 그 `oldSHA`가 `target_before_sha`와 일치하는 것은 lock을 잡고 있는 덕분이지 CAS가 `target_before_sha`를 인지해서가 아니다. receiver worktree 경로(`runMergeInto`가 worktree 안에서 실제 `git merge`)에는 CAS가 아예 없고, git native index.lock과 gk lock이 직렬화를 담당한다. 즉 두 경로 모두에서 진짜 serializer는 lock이고, CAS는 bare 경로 한정 backstop이다.

## after gate 동작
1. before phase에서 이미 target lock과 `target_before_sha`가 확보되어 있다(after 단독 실행이면 이 단계에서 동일하게 lock 획득 후 고정).
2. 기존 merge/promote를 `runMergeInto` 경로로 수행한다. receiver worktree routing과 `BranchFF` CAS/backup을 그대로 재사용한다. merge 후 target tip을 다시 읽어 `target_after_sha`로 기록한다 — before와 동일하게 `ResolveRef("refs/heads/<target>^{commit}")`를 쓰면 bare/receiver worktree 모두에서 target tip을 정확히 얻는다(어느 worktree의 HEAD를 읽느냐로 갈리지 않음).
3. lock 하에 고정된 두 SHA로 integration patch를 생성한다.

```bash
git diff --binary <target_before_sha>..<target_after_sha> > <patch>
```

4. `--gate` command를 `phase=after`로 실행한다.
5. after gate 실패 시 자동 revert하지 않는다. worktree cleanup/delete도 하지 않고, `state:"paused"`로 종료한다. lock은 마지막에 해제한다.

여기서 `paused`의 의미를 envelope 계약과 맞춰 못박는다. gk agent envelope에서 `paused`는 "작업이 mid-flight라 resume/abort 결정이 필요"를 뜻하고 `error.remedies`에 resume/abort 쌍을 담는다(`internal/cli/agents.go`). after-gate 실패는 git 레벨에서 mid-flight인 conflict가 아니라 **merge는 이미 성공했고 finish 워크플로가 사람의 accept/revert 결정을 기다리며 멈춘 상태**다. 따라서 계약을 어기지 않도록 remedies에 두 갈래를 모두 담는다:

- **resume-forward(accept)**: gate 결과를 받아들이고 마무리를 진행한다 — `GK_AGENT=1 git-kit worktree finish --to <target> --resume-accept`(cleanup/delete를 이 시점부터 실행), safety `safe`.
- **abort(revert)**: 아래 "복구" 명령으로 target을 `target_before_sha`로 되돌린다, safety `destructive`.

이렇게 하면 `state=="paused"`로 분기한 에이전트가 언제나 실행 가능한 resume/abort 명령을 손에 쥔다. `result.merged:true`는 유지되어 "merge는 됐고 결정만 남았다"는 사실이 드러난다. (`--resume-accept`는 rollout 3단계에서 추가하는 신규 플래그다.)

after gate 실패는 이미 target branch가 변경된 상태라서 자동 되돌림이 더 위험하다. 기본값은 "멈추고 증거를 남김"이다. 복구는 `BranchFF`가 남긴 backup ref(`refs/gk/<kind>-backup/<target>/<unix>`)와 고정된 `target_before_sha`를 이용한다.

- target이 어디에도 checkout되지 않은 base 브랜치면 CAS rewind: `git update-ref refs/heads/<target> <target_before_sha> <target_after_sha>` (`pull_base.go`/`BranchFF`가 쓰는 패턴, safety `destructive`).
- target이 현재/다른 worktree에 checkout된 브랜치면 `git reset --hard <target_before_sha>` (`backup.go`/`undo.go`가 canonical recovery로 문서화, safety `destructive`).
- gk verb를 선호하면 `gk undo --to <target_before_sha> --hard`가 있으나 HEAD/current branch만 되돌리고 dirty tree를 거부하므로 base 브랜치 복구에는 위 raw git CAS를 쓴다.

gk에는 임의 브랜치를 되돌리는 `revert` verb가 없다(존재하는 것은 `undo`뿐). base 브랜치를 한 verb로 되돌리는 `gk undo --branch <base>` 확장은 rollout 의존성으로 남긴다.

## lock 정책
- feature worktree 생성과 feature 내부 작업은 병렬이다.
- target branch merge는 target 단위 lock으로 직렬화한다. lock key는 repo common-dir + target branch 조합이다.
- **이 lock은 신규 코드다.** `internal/cli/worktree_lock.go`의 `worktreeLockInfo`는 git native per-worktree lock을 **읽기만** 하고(경로 기준, 브랜치 기준 아님), acquire/release primitive가 없다. 따라서 (common-dir + branch) lock을 직접 만든다.
- 구현: common-dir은 기존 헬퍼 `gitCommonDir(ctx, runner)`(`internal/cli/preflight_dirty.go`, `git rev-parse --git-common-dir`, 절대경로 반환)로 해석하고, lock 파일을 `<git-common-dir>/gk/locks/<sanitized-branch>.lock`에 `O_CREATE|O_EXCL`로 만든다(`worktree_init.go`의 atomic file-copy 패턴). 파일에 pid를 기록해 `worktree_lock.go`의 `lockPidRe`/`pidAlive`로 stale holder를 감지한다. release는 `os.Remove`(defer, `cleanupFinishedWorktree` 이후). git native `git worktree lock/unlock`은 경로 기준이라 대안일 뿐 기본은 파일 lock을 쓴다. lock 파일과 상태 파일이 같은 common-dir 아래 있으므로 linked worktree 간에 lock/audit state가 desync되지 않는다.
- target branch가 다른 worktree에서 dirty이면 block한다. `--autostash`가 명시된 경우에만 기존 gk stash 정책을 따른다.
- **target이 다른 worktree에 clean 상태로 checkout돼 있어도** naive한 외부 ref update를 하지 않는다. `runMergeInto`는 `findWorktreeForBranch(target)`으로 receiver worktree를 찾고, 있으면 그 worktree 안에서 실제 `git merge`를 돌려 index/HEAD/ref를 함께 전진시킨다(runner를 `entry.Path`에 rooted). 없으면 bare 경로로 가고 ref는 `BranchFF`가 옮긴다. `BranchFF`의 `branchCheckoutPath` guard는 어떤 worktree가 checkout한 ref를 bare update-ref로 건드리면 `FFBlocked`로 거부한다. 이 receiver-worktree-aware 계약은 이미 존재하므로 gate는 별도 lock/panel을 추가하지 않고 이 경로를 그대로 재사용한다.

## 상태 파일
gate run은 재시도와 감사가 가능해야 한다. 상태 파일은 linked worktree와 별도 gk 호출 간에 공유되어야 하므로, 워킹트리의 `.git`(linked worktree에서는 per-worktree `.git/worktrees/<name>`으로 격리됨)이 아니라 모든 worktree가 공유하는 common-dir 아래에 둔다.

```text
<git-common-dir>/gk/worktree-gate/<run-id>.json
```

경로는 `filepath.Join(gitCommonDir(ctx, runner), "gk", "worktree-gate", "<run-id>.json")`로 계산하며, 이는 manual bisect가 세션 상태를 `<git-common-dir>/gk/bisect.json`에 저장하는 기존 precedent(`internal/cli/bisect.go`)를 그대로 따른다. 상위 디렉터리는 `os.MkdirAll(dir, 0o755)`로 만들고 파일은 `0o644`로 쓴다. `gitCommonDir`가 `""`를 반환하면(비저장소 등) 상태 파일 없이 degrade한다.

최소 필드(before phase 기록 예시 — merge 전이므로 `target_after_sha`는 `null`):

```json
{
  "run_id": "20260701-120102-feat-search-index",
  "source": "feat/search-index",
  "target": "develop",
  "phase": "before",
  "base_sha": "<merge-base sha>",
  "head_sha": "<source HEAD sha>",
  "target_before_sha": "<locked target tip sha>",
  "target_after_sha": null,
  "patch": "/tmp/gk-gate-...patch",
  "gate_command": "xm panel {patch} --json",
  "gate_exit_code": 0,
  "started_at": "2026-07-01T12:01:02Z",
  "finished_at": "2026-07-01T12:04:31Z"
}
```

`base_sha`는 `merge-base(source, target)`이고, merge 직전/직후 target tip은 각각 `target_before_sha`/`target_after_sha`다(gate template 변수와 동일). `phase:"before"` 기록에서는 아직 merge 전이라 `target_after_sha`가 `null`이며, `phase:"after"` 기록에서만 실제 SHA로 채워진다(gate template 변수의 "after phase에서만 채워짐" 규칙과 1:1로 대응).

## agent mode 결과
성공:

```json
{
  "state": "ok",
  "ok": true,
  "result": {
    "source": "feat/search-index",
    "target": "develop",
    "merged": true,
    "gate": {"before": "passed", "after": "skipped"},
    "cleanup": "done"
  }
}
```

before gate 실패(target 무변경 → 항상 `blocked`):

```json
{
  "state": "blocked",
  "ok": false,
  "error": {
    "message": "gate failed before merge",
    "remedies": [
      {
        "command": "xm panel /tmp/gk-gate-before.patch --json",
        "safety": "safe"
      }
    ]
  }
}
```

after gate 실패(merge는 성공, finish가 accept/revert 결정 대기 → `paused`, 증거 보존):

```json
{
  "state": "paused",
  "ok": false,
  "result": {
    "merged": true,
    "cleanup": "skipped",
    "target": "develop",
    "gate": {"before": "passed", "after": "failed"}
  },
  "error": {
    "message": "gate failed after merge; finish paused for accept/revert decision",
    "remedies": [
      {
        "command": "GK_AGENT=1 git-kit worktree finish --to develop --resume-accept",
        "safety": "safe"
      },
      {
        "command": "git update-ref refs/heads/develop <target_before_sha> <target_after_sha>",
        "safety": "destructive"
      }
    ]
  }
}
```

remedies는 resume/abort 쌍이다: 첫 번째(`--resume-accept`)가 gate 결과를 받아들이고 cleanup을 진행하는 resume-forward, 두 번째가 target을 되돌리는 abort. `safety`는 gk agent envelope의 실제 값(`safe` | `destructive`)을 쓴다. abort의 기본형은 CAS `update-ref`(bare rewind)이며, develop이 어떤 worktree에 checkout된 경우에는 `git reset --hard <target_before_sha>`(safety `destructive`)를 대신 넣는다.

## 주요 edge case
- `xm`이 PATH에 없음: gate command not found로 block, merge 전이면 target 변경 없음.
- patch가 너무 큼: `gk`는 patch 생성까지 담당하고, 크기 정책은 `--gate-max-bytes` 추가 전까지 gate command가 판단한다.
- merge conflict: 기존 `git-kit merge/promote` pause contract를 그대로 반환한다.
- 이미 통합된 target: `runMergeInto`의 `isAncestor(source, target)` no-op short-circuit을 그대로 타서 clean-but-already-merged receiver가 dirty guard에 걸리지 않는다.
- target이 upstream보다 뒤처짐: 기존 `finish`/`promote` precheck 정책을 재사용한다.
- lock 획득 중 stale holder: lock 파일 pid가 `pidAlive`로 죽어 있으면 회수하고, 살아 있으면 block한다.
- branch parent가 없음: `--to`가 있으면 사용하고, 없으면 기존 parent/base resolution error를 반환한다.

## 테스트 계획
- before gate 실패 시 target branch SHA가 변하지 않고 `state:"blocked"`로 끝난다.
- before gate 성공 시 기존 `worktree finish` merge 경로(`runMergeInto`)와 같은 결과를 만든다.
- `target_before_sha`는 lock 획득 후에 고정되고, before-gate source patch와 after-gate integration patch가 모두 그 고정 SHA를 기준으로 계산된다(gate가 실제 merge될 patch를 본다).
- after gate는 `target_before_sha..target_after_sha` patch를 생성한다.
- 동시에 두 feature를 finish하면 target lock으로 직렬화된다: 두 번째 finish는 lock을 얻을 때까지 임계 구역(SHA 고정 → patch → gate → merge)에 진입하지 못하므로 다른 feature 커밋이 integration patch/복구 범위에 interleave되지 않는다. bare 경로에서는 `BranchFF` CAS가 `oldSHA` 불일치 시 loudly 실패하는 backstop을 추가로 제공하지만(직렬화의 1차 보증은 lock), receiver worktree 경로에는 CAS가 없으므로 lock이 유일한 serializer임을 검증한다.
- after gate 실패 시 cleanup/delete-branch가 실행되지 않고, remedy가 실재하는 명령(`git update-ref` CAS rewind 또는 `git reset --hard`)이다 — `git-kit revert` 같은 없는 verb를 참조하지 않는다.
- 상태 파일이 `<git-common-dir>/gk/worktree-gate/`에 생성되어 linked worktree와 별도 gk 호출 간에 공유된다(per-worktree `.git`에 갇히지 않는다).
- `phase:"before"` 상태 기록에서 `target_after_sha`가 `null`이고, `phase:"after"` 기록에서만 SHA로 채워진다.
- dirty target worktree는 block하고, `--autostash` 명시 시 기존 stash 규칙을 따른다. 다른 worktree에 clean checkout된 target은 `runMergeInto`가 그 worktree 안에서 merge를 돌린다(별도 ref update 아님).
- `{patch}`, `{source}`, `{target}`, `{phase}` 등 template 치환이 공백/shell metacharacter를 포함해도 shell injection 없이 각각 단일 argv로 전달된다(`sh -c`를 거치지 않음).
- agent mode JSON은 `state` 기준으로 판별 가능하다(before 실패=`blocked`, after 실패=`paused`).

## rollout
1. `--gate` + `--gate-phase before` + argv 실행 모델(no shell)만 구현한다.
2. common-dir 기반 target lock(신규 코드)과 `<git-common-dir>/gk/worktree-gate/` gate run state 파일을 추가한다.
3. `--gate-phase after|both`와 cleanup hold 정책, integration patch, `--resume-accept`(paused 상태의 resume-forward), CAS/`reset --hard` 복구 remedy를 추가한다. gk에 base-branch revert verb가 없으므로 복구는 raw git CAS(또는 current-branch 한정 `gk undo`)로 제공하고, 전용 verb(`gk undo --branch <base>`)는 후속 rollout 의존성으로 남긴다.
4. `--panel-review` shorthand와 config default를 추가한다.
5. `gk fleet`/`gk worktree list`에서 gate paused 상태를 노출한다.

## xm과의 경계
`gk`는 아래 primitive만 제공한다.

- worktree acquire/reuse
- branch parent/base metadata
- source patch 생성
- target merge 직렬화
- gate command 실행
- integration patch 생성
- cleanup/delete policy

`xm`은 아래를 담당한다.

- PRD/task DAG 생성
- 어떤 task를 병렬 실행할지 결정
- agent prompt/context 생성
- `xm panel` verdict 해석
- task status와 artifact 저장

## 검증 반영 이력
4-model 패널(claude/codex/agy/cursor) 검증 결과를 반영한 항목:

- **T1** (4/4, agy=critical): after-gate가 target lock을 잡기 전에 before-SHA를 캡처해 병렬 finish의 커밋이 interleave되던 문제 → lock을 먼저 잡고 그 하에서 `target_before_sha`를 고정하도록 before/after 동작을 재작성. 직렬화를 실제로 보장하는 것은 lock 자체이며(lock이 임계 구역 전체를 덮음), `BranchFF` CAS는 bare 경로 한정 backstop이다. CAS의 `oldSHA`가 `target_before_sha`와 일치하는 것도 lock을 잡고 있기 때문이고, receiver worktree 경로에는 CAS가 없다.
- **T2** (claude/codex/agy, code-confirmed): 존재하지 않는 `git-kit revert --range` remedy 삭제 → 실재하는 `git update-ref refs/heads/<target> <before> <after>` CAS rewind(또는 checkout된 브랜치면 `git reset --hard <before>`)로 교체. base-branch 전용 verb는 rollout 의존성으로 명시.
- **T3** (cursor/claude/codex, code-confirmed): 상태 파일 경로 `.git/gk/worktree-gate/`는 linked worktree에서 per-worktree gitdir로 격리됨 → `<git-common-dir>/gk/worktree-gate/`(bisect precedent, `gitCommonDir` 헬퍼)로 변경하고 이유를 명시. lock 파일도 같은 common-dir(`<git-common-dir>/gk/locks/`) 아래 두어 desync를 막음.
- **T4** (codex/cursor): before-gate patch를 lock/SHA 고정 없이 생성해 승인한 patch와 실제 merge가 달라지던 문제 → source/integration patch 모두 lock 하에 고정된 `target_before_sha`(및 `target_after_sha`) 기준으로 계산.
- **T5** (claude, cursor-contested): gate 실행 모델 미정의 → "gate 실행 모델" 절 추가. template을 argv로 tokenize(신규 tokenizer 코드), 각 `{token}`을 단일 argv 원소로 치환, `sh -c` 없이 실행(`gk worktree run`의 no-shell exec 부분만 재사용, 단일 문자열 tokenize는 gate 전용 신규 코드임을 명시). 테스트 계획의 "no shell injection" 항목을 이 모델로 뒷받침.
- **T6** (claude, low): `{base_sha}`의 이중 정의 제거 → `merge-base(source, target)` 하나로 고정하고 `{target_before_sha}`/`{target_after_sha}`를 상태 파일 필드와 1:1 분리. before phase 기록에서 `target_after_sha`는 `null`.
- **T7** (claude, low): before-gate 실패 state가 `blocked`/`paused` 혼재 → before 실패는 target 무변경이므로 언제나 `blocked`, `paused`는 after-gate 실패 전용으로 통일(JSON 예제 포함).
- **Contested-1** (agy 제기, cursor 수긍, claude 반박): lock이 dirty target만 막고 clean-but-checked-out target은 desync될 수 있다는 우려 → 새 lock/panel 대신 이미 존재하는 `runMergeInto`의 receiver-worktree-aware 계약(`findWorktreeForBranch` routing + `BranchFF.branchCheckoutPath` guard)을 재사용한다고 lock 정책에 명시.

### 재검증(re-verify)이 잡은 회귀 2건 — 개정 과정에서 새로 생겼다 수정
- **R1** (T1/T4 재작성이 유발): before-gate step 4가 "다른 worktree에 checkout된 target은 `headRev`(`git rev-parse HEAD`)로 읽는다"고 썼으나, `headRev`는 finish를 실행하는 **feature worktree의 HEAD = source tip**을 가리켜 baseline을 오염시킴 → checkout 위치와 무관하게 `refs/heads/<target>`이 항상 target tip이므로 케이스 분기 없이 `ResolveRef("refs/heads/<target>^{commit}")`만 쓰도록 before(step 4)·after(step 2) 모두 통일.
- **R2** (T7 해결이 유발): after-gate 실패에 `state:"paused"`를 배정했으나 envelope 계약의 `paused`(git op mid-flight → resume/abort 쌍)와 충돌 — merge는 이미 끝났고 remedy는 파괴적 rewind 하나뿐이라 `paused`로 분기한 에이전트가 resume할 op이 없었음 → `paused`를 "merge 성공 후 finish가 accept/revert 결정을 대기하며 멈춘 상태"로 재정의하고, remedies에 resume-forward(`--resume-accept`, safe)와 abort(rewind, destructive) 쌍을 넣어 계약 형태를 충족.