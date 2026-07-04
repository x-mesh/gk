# Changelog

All notable changes to gk will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **`gk push`가 커밋 없는 저장소에서 무슨 일인지 분명히 말한다.** 첫 커밋 전 `gk push`는 secret scan을 건너뛴 뒤 git의 알쏭달쏭한 `src refspec main does not match any`로 실패했다(0.113.0에서 스캔 크래시는 고쳤지만 push 자체 메시지는 여전히 불친절했다). 이제 push 진입부에서 unborn HEAD를 감지해 `nothing to push: "main" has no commits yet` + `gk commit` remedy로 안내한다.

## [0.114.1] - 2026-07-09

### Fixed

- **`gk commit`이 provider의 JSON 대신 prose 응답에 재시도한다.** OpenAI 호환 게이트웨이가 `response_format=json_object`를 무시하고 `"I'm sorry, …"` 같은 산문을 뱉으면 classify가 `provider returned malformed response: invalid character 'I'`로 즉사했다 — HTTP는 200이라 기존 429/5xx 전송 재시도 경로를 타지 않았기 때문이다. 이제 classify/compose는 HTTP는 성공했지만 콘텐츠가 요청한 JSON 모양이 아닌 경우를 별도로 감지해 같은 요청을 1회 재발송한다(`maxContentRetry`). 응답이 토큰 한도로 잘린 경우(`errTruncatedJSON`)는 재시도해도 또 잘리므로 제외하고 기존의 "파일 수를 줄이거나 `gk commit --plan -`" 안내를 그대로 띄운다. openai·groq는 nvidia 어댑터 위임으로 자동 적용된다. 5모델×2렌즈 cross-vendor 리뷰가 compose 쪽 갭을 잡아냈다: compose의 plain-text fallback이 prose 첫 줄을 커밋 subject로 받아들여 재시도를 건너뛰던 문제를, json_object 모드 전용 strict 파서(`parseComposeJSON`, fallback 없음)를 분리해 막았다 — prose는 이제 compose에서도 재시도되고 소진되면 쓰레기 subject 대신 명확한 에러로 실패한다. CLI 어댑터(gemini/qwen/kiro)는 markdown을 정상 반환하므로 lenient fallback을 유지한다.

## [0.114.0] - 2026-07-09

### Added

- **snapshot 안전망이 진짜 "자동"이 됐다 — Phase 2 완성.** Phase 1은 `gk snapshot` 명령만 제공해 안전망이 수동이었다([설계 문서](docs/design-snapshot-safety-net.md) §4가 트리거·보존·비교를 Phase 2로 예약). 이번에 세 축을 모두 구현했다. ① **트리거**: `gk snapshot hook install|status|uninstall`이 Claude Code **Stop hook**(`gk snapshot -q`)을 설정 JSON에 설치해, AI가 한 턴을 마칠 때마다 작업트리가 `refs/wip/<branch>`에 체크포인트된다 — 브랜치 히스토리 오염 없이. 설치는 append-only(기존 훅·설정 키 보존)·멱등이고, 숫자는 `json.Number`로 보존해 재직렬화가 `timeout: 30`을 float로 바꾸지 않으며, 파싱 불가 파일은 **덮어쓰지 않고 거부**한다. 기본 대상은 `~/.claude/settings.json`, `--project`는 repo의 `.claude/settings.json`. ② **보존**: `gk snapshot prune [--keep-days N] [--all]` + `snapshot.retention_days` config(기본 0 = off — 업그레이드가 기존 스냅샷을 조용히 지우면 안전망의 계약 위반이라 opt-in). retention이 켜져 있으면 저장 때마다 best-effort로 조용히 expire하고 — 실패해도 저장은 성공한다, 스냅샷 자체가 안전망이므로 — 전부 만료된 ref는 삭제해 `@{n}` 접근이 안 되는 유령 ref를 남기지 않는다. ③ **비교**: `gk snapshot diff [n] [--stat]`가 복원이 적용할 방향 그대로 스냅샷 ↔ 작업트리 차이를 보여준다. ([docs/phase2-improvements.md](docs/phase2-improvements.md))
- **`gk agents hook`에 `collapse` 모드 추가 — 반복 프로브만 정확히 막는다.** 기존 훅은 warn(전부 조언)과 block(covered raw git 전부 거부) 둘뿐이라, block은 일회성 `git status` 하나도 막아 실사용에 과했다. 세션 감사 결과 회수 가능 턴의 대부분이 **오리엔테이션 재프로브**(`git status` → `git log` → `git diff`를 여러 턴에 나눠 실행 — `gk context` 한 번이면 될 것)에서 나오는데, warn 넛지는 트렌드상 이 낭비를 못 줄였다. `--mode collapse`는 그 중간을 노린다: 단발 covered 명령은 여전히 advisory(defer)로 두되, **직전 같은-그룹 raw 프로브를 이어가는 두 번째 호출(collapse 신호)만 deny**해 `gk context` 한 번으로 접게 강제한다. 결정 로직은 순수 함수 `hookDecide`로 분리해 세 모드(warn/collapse/block)를 단일 소스로 관리하고, 기존 `--warn` 설치는 그대로 warn으로 판독된다(하위호환). ([docs/commands.md](docs/commands.md#gk-agents-hook))
- **브랜치 부모를 설정 없이 추론한다 — branchparent Phase 2.** `gk branch set-parent`를 쓰지 않아도, 브랜치의 생성 지점(가장 오래된 reflog 항목)을 `for-each-ref --contains`로 조회해 그 커밋을 포함하는 로컬 브랜치가 **정확히 하나**면 그것을 부모로 쓴다(source: `inferred`) — stacked 브랜치의 `gk status`가 트렁크 대신 진짜 부모 기준으로 비교된다. 후보가 0개/여러 개(main·develop이 둘 다 포함하는 공유 트렁크 repo의 일상)면 추론을 포기하고 기존 base 폴백을 쓰므로 절대 추측하지 않고, reflog가 없는 환경(bare/CI)은 에러 없이 강등된다. **머지 대상은 예외로 못박았다**: 신설한 `Resolver.ExplicitOnly()`로 `land`/`promote`의 hop 대상 결정에는 추론이 아예 관여하지 못한다 — 머지 목적지는 명시적 `gk-parent` 또는 트렁크 폴백에서만 나온다.

## [0.113.0] - 2026-07-04

### Added

- **`gk chat` — 저장소와 대화한다.** `gk ask`가 미리 모은 컨텍스트로 한 번 답하는 것과 달리, chat은 모델이 읽기 전용 도구 7종(git log/show/diff(digest-first)/blame/grep + 파일 읽기/목록)을 **스스로 호출해 근거를 찾아가며** 답하는 에이전틱 루프다. "이 함수 언제 왜 바뀌었지?"라고 물으면 log → blame → 파일 읽기를 알아서 연쇄하고, 실제로 본 SHA와 파일:라인을 인용한다. 모든 도구 호출은 한 줄씩 화면에 표시된다. REPL(`gk chat`) · one-shot(`gk chat "질문"`) · `--continue`(턴마다 append-only JSONL로 `.git/gk-chat/`에 저장, 손상 세션은 경고 후 새로 시작) 세 진입점을 제공하며, REPL에서 Ctrl-C는 현재 턴만 취소하고 턴 실패는 세션을 죽이지 않는다. provider 계층에는 `ToolCaller` capability가 신설됐다(Anthropic tool_use blocks + OpenAI 호환 tool_calls — openai/groq는 nvidia 위임으로 자동 지원, CLI형 gemini/qwen/kiro는 미지원으로 `gk ask` 안내). 보안은 프롬프트가 아니라 코드가 강제한다: repo-root 샌드박스(symlink 해석 후 containment, .git/submodule/타 worktree 차단), 실행 시점 인자 재검증(flag injection 차단), **deny_paths가 과거 커밋 내용에도 적용**(`git show`/`diff` 출력을 파일 단위로 분할해 차단 블록 제거, `git grep`은 `:(exclude,glob)`로 구조적 배제), redact-before-persist(도구 결과가 provider와 세션 파일에 닿기 전 시크릿 제거), 상한(턴당 15라운드·누적 192KB·동일 호출 반복 거부)과 deny 목록은 global config 전용(`ai.chat.max_tool_rounds`/`tool_result_cap`/`deny_paths` — 클론한 repo의 `.gk.yaml`이 예산이나 차단 표면을 못 건드림), remote 정책은 라운드마다 재확인. GK_AGENT에서 REPL은 차단되고 one-shot은 `{answer, tool_calls[], session_id, ...}` envelope을 반환한다. REPL 입력은 x/term 라인 에디터라 ↑/↓로 이전 질문을 오가며 편집할 수 있고, 라운드 예산은 `ai.chat.round_timeout`(기본 120s — 프록시의 간헐 500 재시도까지 수용, 실측 기반)이 지배한다. 출시 전 5모델×2렌즈 cross-vendor 리뷰로 ref 경로 밀반입(`HEAD:.env`)·.git 대소문자 우회·merge commit diff --cc 우회·rename/quoted path 미검사·실패 턴의 세션 오염(Anthropic 역할 교대 위반) 등 15건을 수정했다.
- **AI privacy gate에 벤더 시크릿 패턴이 연결됐다.** 기존 gate는 일반 키워드형 정규식 5개뿐이라 `token=` 같은 키워드 없이 등장하는 GitHub PAT(`ghp_`)·Slack(`xox`)·OpenAI(`sk-`)·AWS 40자 시크릿·PEM 블록이 모든 AI 표면(ask/do/status/log/chat)을 그대로 통과했다 — `gk push`의 시크릿 스캐너가 이미 가진 고신호 패턴들을 gate에 배선해 전 표면에서 redact한다. (gk chat 설계 리서치가 발견한 전 표면 공통 갭)
- **`gk push`가 GitHub repo가 없으면 만들어 준다.** `gk init`은 origin URL만 등록하고 GitHub repo는 생성하지 않아, 첫 push가 "Repository not found"로 실패하곤 했다. 이제 push가 그 이유로 실패하면 origin URL에서 owner/repo를 파싱해 `gh repo create`로 생성한 뒤 재시도한다. GitHub repo 생성은 외부로 나가는 되돌리기 어려운 동작이라 무음 생성은 하지 않는다 — 대화형 터미널에서는 "create it as private and push? [y/N]" 확인을 받고, 비대화형(agent/CI)에서는 `--create-remote` 플래그를 명시했을 때만 생성하며 그렇지 않으면 정확한 `gh repo create` remedy를 안내한다. 기본은 private, `--public`으로 공개 repo. gh가 없거나 origin이 GitHub owner/repo로 해석되지 않으면 기존 git 에러를 그대로 보여준다.

### Fixed

- **`gk push`가 커밋 없는 새 저장소에서 깨끗하게 처리된다.** 첫 커밋 전 `gk push`는 시크릿 스캔이 `git log -p HEAD`를 돌리다 "ambiguous argument 'HEAD'"로 실패했다. unborn HEAD를 감지해 스캔을 건너뛰므로, 이제 git이 직접 "nothing to push"류 메시지를 준다.
- **Easy Mode 에러 표시가 git 명령 에코를 번역하지 않는다.** `git.ExitError`의 명령 에코(`git push --set-upstream origin main: exit code N:`)가 용어 번역을 거쳐 `git push --set-원격 기준점 (upstream) …`처럼 렌더되어, 마치 gk가 한국어를 git 인자로 넘긴 것처럼 보이던 문제를 수정했다. 명령 에코를 stderr 꼬리와 함께 보호 span으로 처리한다. (시크릿 스캔의 중복 stderr splice도 함께 제거)

## [0.112.0] - 2026-07-03

### Added

- **`gk log --ai`가 선택된 커밋 범위를 사용자 언어로 서술한다 — `gk changelog`와 달리 릴리스 노트가 아니라 읽기 동반자다.** `gk log`가 `--since`/`--limit`/pathspec/revision args로 이미 골라낸 범위를 대상으로, 커밋 제목만 모델에 던지는 대신 log.go가 `--hotspots`/`--wip`/`--breaking`/`--cc`에서 이미 계산하는 결정론적 신호(hotspot 파일, WIP 체인, breaking 표시, CC 타입 집계, base 대비 merged/unmerged)를 근거(facts) JSON으로 함께 넘겨 모델이 사실을 지어내지 않고 인용하게 한다. 기존 로그 목록은 그대로 두고 AI 요약은 그 아래에 추가되어 `--graph`/`--lanes`/`--json` 등 기존 옵션과 자연스럽게 합성된다. `--json`(--ai 없이)은 기존 배열 shape을 그대로 유지하고, `--json --ai`일 때만 `{entries, ai_summary}` 객체로 확장된다. 언어는 `output.lang`을 자동으로 따르고 `--lang`/`--provider`로 오버라이드 가능. 대형 범위는 150커밋으로 절단해 payload를 억제하되 집계 신호는 전체 범위에서 계산하며, `resolve.verify`류 원격 차단·프라이버시 게이트·콘텐츠 주소 캐시 등 기존 AI 파이프라인을 그대로 재사용한다. (5모델 cross-vendor 리뷰에서 pathspec 밖 hotspot 유출, remote-gate 순서, self-base 오신호 등 결함을 잡아 반영했다.)
- **cross-vendor 리뷰(5모델×2렌즈)가 잡은 Phase 2 결함 10건을 반영했다.** Critical: delete/modify AI 경로가 confidence 게이트를 우회해 낮은 확신의 삭제 결정이 그대로 실행되던 것(4개 벤더 합류 발견 — 이제 보류·제안 동봉). High: confidence 보류 마커가 `resolve.verify` 빌드 검사를 실패시켜 정상 해결분까지 롤백하는 상호 무효화(잔여 충돌이 있으면 명령 검증을 건너뛰고 명시), 부분 해결 파일이 롤백에서 빠지던 것, multi-pick 후속 라운드의 `remaining` 누락, 롤백 리포트의 삭제 경로 누락. 그 외: pristine 검사가 EOF 빈 줄 편집을 통과시키던 것(한 줄 개행 차이만 허용), AI 모드에서 결정론으로 풀 수 있는 hunk가 모델 확신도에 인질로 잡히던 것(hunk별 기계 overlay + 게이트 면제), merged+빈 응답이 파일을 비우던 회귀(생존 side fallback 복원), 부분 재출력이 사용자의 마커 스타일을 diff3로 바꾸던 것(분류는 enriched로, 재출력은 원본으로), global config 파손 시 안전 설정이 무음으로 꺼지던 것(경고 추가).
- **`gk resolve`의 한계 네 곳을 메꿨다 — base 재구성·부분 safe·완전한 롤백·side-take 원문 강제.** ① git 기본 conflict style은 diff3 base 블록을 남기지 않아 기계 티어의 base 기반 규칙(한쪽 무변경, union 추가성 검사)이 대부분의 repo에서 꺼져 있었다 — 이제 index stage(:1/:2/:3)에서 `git merge-file --diff3`로 base를 **메모리에서 재구성**해 모든 repo에서 작동하며, 워크트리가 pristine 재병합과 바이트 일치할 때만 사용해 손 편집을 절대 덮어쓰지 않는다. ② `--safe`가 파일 단위 all-or-nothing이던 것을 **hunk 단위 부분 해결**로: 증명 가능한 hunk만 고치고 나머지는 마커째 남긴다(파일은 unmerged 유지). ③ delete/modify 해결도 stage를 미룬다 — 워크트리 삭제는 즉시(verify가 진짜 결과를 보도록), index 삭제는 게이트 통과 뒤로 — 검증 실패 시 `checkout -m`이 삭제된 파일까지 복원해 롤백 사각지대가 사라졌다. ④ AI가 "ours/theirs"를 선택하면 그 side의 **원문을 그대로** 적용한다: side를 주장하며 변조된 텍스트를 내는 무음 손상 클래스는 confidence로 못 잡는 것이라 구조적으로 차단했다(marker 경로·degenerate 경로 모두).
- **`gk resolve`에 hunk별 confidence 게이트와 제안 동봉이 붙었다 — Phase 2.** AI가 모든 hunk 해결에 확신도(0.0~1.0)를 보고하고, global config의 `resolve.min_confidence`(기본 0 = off) 미만인 hunk는 **적용하지 않는다**: 파일은 확신 hunk만 치환되고 불확신 hunk는 마커 그대로인 부분 해결 상태로 쓰이며(stage 안 됨, `remaining`으로 보고), 보류된 답은 paused 리포트의 `proposals[]`(파일·hunk 번호·전략·확신도·rationale·해결 라인)로 동봉된다 — 에이전트가 "해결해줘"를 기다리는 대신 제안을 검토해 직접 적용하거나 재실행할 수 있다. 양의 게이트에서 확신도 미보고(구형 응답)는 미달로 취급해 옵트인한 게이트를 우회할 수 없고, multi-pick rebase의 후속 라운드 제안도 리포트에 누적된다. `min_confidence`는 `verify`/`union_files`와 같은 신뢰 경계로 global config 전용이다 — 해결 대상 repo가 기준을 낮출 수 없다.

## [0.111.0] - 2026-07-03

### Added

- **`gk resolve`에 결정론 티어(`--safe`)·검증 게이트·rerere가 붙었다 — "자동 해결을 믿어도 되는 인프라"의 1단계.** 실전 충돌의 상당수는 판단이 필요 없다: 양쪽이 동일, trailing whitespace·줄 끝(CRLF)만 다름(내부 공백·들여쓰기는 문자열 리터럴·Python·Makefile에서 의미를 가지므로 이 티어에 넣지 않는다), (diff3 마커에서) 한쪽이 base 그대로라 반대쪽이 유일한 변경, 그리고 양쪽이 **추가**일 때의 union 파일(`CHANGELOG.md`/`go.sum` — go.sum은 같은 module@version에 다른 해시가 오면 변조 신호이므로 병합을 거부한다). `--safe`는 이 티어만 해결하고 나머지는 마커째 남겨 `remaining`으로 보고하며 — 아무것도 추측하지 않는다 — 같은 분류기가 `--ai`의 pre-pass로도 돌아 AI에 가는 표면을 줄인다. batch 해결(`--strategy`/`--ai`/`--safe`)은 이제 **stage를 미룬다**: 충돌-마커 스캔(상시)과 `resolve.verify` 명령(예: `go build ./...`)이 통과해야 stage·continue가 진행되고 — multi-pick rebase의 후속 라운드에도 같은 게이트가 걸린다 — 실패하면 index의 unmerged stage가 그대로 남아 있는 덕에 gk가 **쓴** 파일만 `git checkout -m`으로 정확히 복원하고(사용자가 이미 손으로 정리한 markerless 파일은 절대 덮어쓰지 않는다) `verify_failed`와 함께 paused로 보고한다 — 잘못된 자동 해결의 비용이 0이 된다. `resolve.verify`·`resolve.union_files`는 **global config에서만** 읽는다: 해결 대상 repo의 `.gk.yaml`이 셸 명령을 심거나 자동 병합 표면을 넓힐 수 없다(`init.ai_gitignore`와 같은 신뢰 경계). `resolve.rerere`(기본 on)는 첫 실행에서 git rerere를 켜고 기록된 해결을 먼저 적용해, 장수 브랜치의 반복 충돌을 AI 비용 없이 처리한다 — 순수 side-take를 약속하는 명시적 `--strategy ours|theirs`에서는 rerere도 건너뛴다. (이 항목의 안전 주장 자체를 5개 모델 cross-vendor 리뷰로 반박시켜 Critical 2건 — 느슨한 공백 규칙, markerless 롤백 파괴 — 을 포함해 11건을 반영한 결과다.)

## [0.110.0] - 2026-07-03

### Added

- **`gk status`가 dirty 항목마다 "마지막으로 만진 시각"을 표시한다.** 상태 화면은 무엇이 바뀌었는지는 보여줘도 언제 바뀌었는지는 보여주지 않아서, dirty 파일 9개 중 방금 작업한 것과 사흘 전에 만지다 만 잔재물을 구분할 수 없었다. 이제 기본 vis(`staleness`)에서 트리·flat 목록의 변경·충돌·untracked 항목 뒤에 mtime 기반 상대 시각이 붙고(`status.go +85 -13 · 1m`, 1분 미만은 `· now` — untracked의 옛 `(Nd old)` 별도 표기는 이 어휘로 통일), agent JSON에는 `entries[].modified_at`(RFC3339)으로 실린다 — 에이전트가 추가 프로브 없이 최근 변경 순 정렬이나 잔재물 판별을 할 수 있다. 경로는 `rev-parse --show-toplevel`로 해석해 저장소 하위 디렉토리에서 실행해도 정확하며, 비용은 toplevel 조회 1회 + 표시 항목당 lstat 1회다. mtime은 "마지막 쓰기" 시각이라 checkout·포맷터도 갱신한다는 한계는 그대로다 — 절대적 저작 시각이 아니라 "아, 최근에 바뀌었구나"의 신호로 읽는 것. (5개 모델 cross-vendor 리뷰가 잡아낸 서브디렉토리 경로 결함·뷰 간 표기 불일치를 반영한 결과다.)

## [0.109.1] - 2026-07-03

### Security

- **`init.ai_gitignore`는 global config에서만 존중된다.** 이 키는 `gk init` 시 원격 AI 호출을 켜는 스위치라서, clone해 온(신뢰할 수 없는) 저장소의 repo-local `.gk.yaml`이 이를 심어두면 그 안에서 `gk init`을 실행한 사람의 프로젝트 메타데이터가 플래그 없이 프로바이더로 나갈 수 있었다 — cross-vendor 리뷰의 쟁점 finding(F6)을 하드닝으로 수용한 것. 이제 `$XDG_CONFIG_HOME/gk/config.yaml`의 값만 읽고 repo-local 레이어는 이 키에 한해 무시하며, 명시 `--ai-gitignore[=false]` 플래그는 여전히 우선한다.

### Changed

- **`gk doctor`/`gk forget`/`gk status`의 목록 미리보기가 공통 헬퍼로 통일됐다.** 상한을 넘는 항목은 셋 다 `+N more` 꼬리로 잘렸음을 표기해, 잘린 목록이 전부인 것처럼 읽히지 않는다.

## [0.109.0] - 2026-07-03

### Added

- **`gk commit`의 AI classify가 대형 작업 트리에서도 mid-JSON 절단 없이 동작한다 — 응답 인덱스 프로토콜, 파일 청크 분할, 응답 토큰 캡 배선.** 종전에는 파일 목록을 프롬프트 그대로 응답에 재나열시켜, 파일이 수천 개면 응답 JSON 자체가 프로바이더의 출력 한도를 넘어 잘렸다(`ai.commit.max_tokens`는 입력 절단 예산일 뿐이라 이 실패를 전혀 제어하지 못했는데, 에러 힌트는 그 값을 올리라고 안내하고 있었다). 이제 프롬프트는 파일을 번호로 나열하고 모델은 번호만 참조해 응답 크기가 경로 길이와 무관해지며, 150개씩 청크로 나눠 호출한 뒤(각 호출의 응답 캡을 파일 수에 비례해 배선) 같은 타입 그룹을 병합하고 커버리지 가드로 누락 파일을 쓸어 담는다 — 5개 어댑터(anthropic/openai 호환/gemini/qwen/kiro) 전부. 에러 힌트도 실제로 듣는 노브(재시도·`--plan` 경로·부분 스테이징)를 가리키도록 고쳤다.
- **`gk init`이 Swift/Dart/C++ 프로젝트를 인식하고, `.gitignore`에 반영 안 된 대형 untracked 트리를 `gk doctor`가 선제 경고한다.** marker 파일 테이블에 `Package.swift`/`pubspec.yaml`/`CMakeLists.txt`가 빠져 있던 탓에, SwiftPM `.build/` 산출물 수천 개가 언어 미탐지 상태로 `gk commit`의 AI classify 페이로드까지 흘러드는 사고가 있었다 — 이제 세 언어 모두 감지되고 각자의 빌드 산출물 패턴(`.build/`·`.swiftpm/`·`DerivedData/`, `.dart_tool/`, `CMakeFiles/`·`cmake-build-*/`)이 스캐폴딩된다. `gk commit`의 2차 방어선(noise 분류)도 `.build`/`.dart_tool`/`.swiftpm`을 인지해 init을 거치지 않은 기존 repo도 보호한다. `gk doctor`의 untracked 검사는 이제 항목이 알려진 toolchain 산출물과 매치되면 그 사실과 함께 `gk init --only gitignore` 조치를 제시한다. AI gitignore 보강(`--ai-gitignore`)은 `init.ai_gitignore: true`로 기본값을 만들 수 있고, provider가 설정된 채로 옵션이 꺼져 있으면 스캐폴드 후 한 줄 힌트가 뜬다.
- **`gk session audit`가 어답션을 프로젝트별로 분해하고, `gk unstage`가 새로 생겼다.** 글로벌 adoption rate 하나로는 "어느 repo에 계약/훅을 깔아야 하는가"를 답할 수 없었다 — 이제 `projects[]`가 raw git 많은 순으로 프로젝트별 adoption을 나열한다(Claude 세션은 워크스페이스 디렉터리로, Codex 세션은 프로젝트 마커가 없어 한 버킷으로 집계). 새 `gk unstage [path...]`는 인덱스만 내리고 워킹트리 내용은 건드리지 않는 안전한 `git reset [-q] HEAD -- <paths>` 형태를 감싸며, audit·hint도 이 형태를 `raw-unstage`(covered)로 인식한다 — 브랜치를 움직이는 reset(`--soft`/`--hard`, `HEAD~1`)은 여전히 `gk undo`의 영역이라 gap으로 남는다.

### Fixed

- **`gk session audit`의 `--since` 시간 윈도우, `--trend --json` 무음 탈락, 순차 스캔 속도.** `--since 30d`(또는 `12h` 등)로 세션 파일을 mtime 기준으로 잘라, 가이던스 수정 이전 세션이 adoption rate를 희석해 "지금 개선되고 있는가"를 가리던 문제를 없앴다. `--trend --json` 조합은 JSON 조기 반환에 막혀 trend를 무음 탈락시켰는데, 이제 기록된 히스토리를 `result.trend[]`로 싣는다. 스캔은 파일 단위로 병렬화되고 `--metric=turns` 경로의 이중 읽기(occurrence 분류와 turn 추출이 파일을 각자 읽던 것)를 1회 읽기 공유로 바꿔, 1,300여 파일 기준 wall ~7.3s → ~1.0s로 줄었다.

## [0.108.0] - 2026-07-02

### Added

- **`gk session audit`가 "전체 역사"가 아니라 "지금"을 측정한다 — `--since` 윈도우, 1-read 병렬 스캔, turn-가중 gap 신호.** `--since 30d`(또는 `12h` 등)는 세션 파일 mtime으로 코퍼스를 잘라, 가이던스 수정 **이전** 세션이 adoption rate를 희석해 "지금 개선되고 있는가"를 가리던 문제를 없앤다 — 리포트에 cutoff가 `since` 필드(human 출력은 `window:` 라인)로 박히고, 걸러낸 파일 수는 note로 남는다. `--trend --json` 조합은 종전에 JSON 조기 반환에 막혀 trend를 무음 탈락시켰는데, 이제 기록된 히스토리를 envelope의 `result.trend[]`로 싣는다. 스캔은 파일 단위로 병렬화되고 `--metric=turns` 경로의 파일 이중 읽기(occurrence 분류와 turn 추출이 각자 읽던 것)를 1회 읽기 공유로 바꿔, 1,300여 파일 기준 wall ~7.3s → ~1.0s — 병합은 수집 순서 그대로 순차라 출력은 결정적으로 동일하다. gap 신호도 로드맵답게 다듬었다: `uncovered-raw-git`의 evidence를 전역 5개 캡 대신 **서브커맨드당 1개씩** 보존하고(희귀 서브커맨드가 예시 없이 보고되던 문제 해소), 원시 형태가 단일 호출이라 gk verb로 바꿔도 turn 절감이 ~0인 서브커맨드(`init`,`clone`,`mv`,`rm`,`archive`,`clean`)는 `one_shot` 라벨로 표시해 count 랭킹이 다중-turn 워크플로 gap(apply/reset 복구 arc) 위로 올라오지 않게 했다. 저수준 plumbing(`merge-tree`,`read-tree`,`checkout-index`,`diff-tree`,`update-index`,`commit-graph`,`cherry`)과 `git kit …` help 잔여물, `remote`/`submodule`의 read-only 형태(`remote -v`/`show`/`get-url`, `submodule status`/`summary`)는 gap에서 제외하되, 변경형(`remote add`/`set-url`, `submodule update`/`add`)은 missing-verb 신호로 유지한다.
- **`clone.hosts`가 계정 프로필이 되고, `gk init`이 그 프로필로 origin을 연결한다.** alias에 `owner`를 주면 `alias:repo` 단축이 owner를 프로필에서 완성하고(`gk clone personal:playground`), `ssh_host`는 `~/.ssh/config`의 Host alias를 ssh URL에 심어 다중 계정 키 분리를 지원한다. origin이 없는 repo에서 `gk init`은 remote 연결 단계를 하나 얹는다: 등록된 계정 프로필을 **config 선언 순서 그대로** 피커에 나열해 두 키 입력으로 연결하고, 프로필이 없으면 direct 입력(owner/repo 또는 URL) 후 그 계정을 글로벌 `clone.hosts`에 저장할지 제안한다. 비대화형은 `--remote <alias|alias:repo|owner/repo|URL>`(+`--name`, `--ssh`/`--https` 프로토콜 오버라이드)로 프롬프트 없이 연결하고, JSON 결과에 `result.remote = {status, name, url, alias}`가 실린다. `gk clone`과 달리 여기서 알 수 없는 alias는 에러다 — `git remote add`는 오타를 그대로 기록해 첫 pull에서야 터지기 때문. Esc는 언제든 remote 단계만 건너뛰며, 연결 후에는 원격 저장소가 아직 없을 때를 위한 `gh repo create` 후속 명령을 안내한다.

## [0.107.0] - 2026-07-02

### Added

- **`gk fleet`가 파일 수준 가시성을 갖췄다 — 병합 change feed, LAST CHANGE 컬럼, fsnotify 즉시 반응.** 대시보드 테이블에 worktree별 최근 변경 파일(LAST CHANGE) 컬럼이 붙고, 테이블 아래에는 어떤 worktree의 어떤 파일이 바뀌었는지 시간순으로 합쳐 보여주는 change feed pane이 흐른다(`e` 토글, 시작 시점의 dirty 파일은 baseline으로 조용히 넘기고 이후 변화만 기록, 200건 ring buffer). `--feed-stats`(또는 `fleet.feed_stats`)를 켜면 이벤트에 +/− 라인 수가 붙는다(worktree당 poll마다 `git diff --numstat` 2회 추가 비용이라 옵트인). 내부적으로 worktree당 2회 돌던 porcelain 스캔(dirty 카운트용 + mtime용)을 `--no-optional-locks` 단일 스캔으로 통합해 파일 가시성을 얹고도 poll 비용은 오히려 줄었고, single-repo 경로도 multi-repo처럼 `GIT_OPTIONAL_LOCKS=0`으로 실행해 에이전트의 `index.lock`과 경합하지 않는다. filesystem watch를 세울 수 있으면 편집에 즉시 반응하고 poll은 12s heartbeat로 강등된다(worktree N개가 프로세스 FD 예산을 나눠 쓰며, 예산 초과 worktree는 heartbeat로 degrade; 이벤트 폭주는 in-flight 1회로 coalesce). TUI 조작도 확장: `w`가 single-repo에서도 해당 worktree의 `gk status --watch`로 드릴다운하고, `f`/`s`가 view 필터(all→busy→stuck)와 정렬(default→activity→status)을 순환하며, detail 패널에 해당 worktree의 최근 이벤트 3건과 land-ready 브랜치의 다음 행동(`gk worktree remove <branch>`)이 표시된다.
- **`gk fleet --events` — 오케스트레이터용 NDJSON 이벤트 스트림 + `fleet.notify` 전이 훅.** one-shot `--json` 스냅샷이 "지금 상태"라면 `--events`는 "바뀌는 순간"이다: `file-changed`(file/note±stats) / `status-changed`(from/to) / `op-start`·`op-end`(operation) / `land-ready` 이벤트를 한 줄에 하나씩 스트리밍해, 감독 에이전트가 스냅샷을 폴링·diff하는 대신 구독한다. `GK_AGENT=1`에서는 첫 줄에 `{"schema":1,"state":"streaming",...}` 헤더 프레임을 먼저 내보내 one-shot envelope 계약과 구분된다(플래그명은 기존 `gk follow` 커맨드와의 충돌을 피해 `--events`). `fleet.notify` config(`conflict`/`paused`/`land_ready` → shell 명령)를 주면 해당 전이에 `GK_FLEET_*` 환경변수를 실어 훅을 실행한다 — TUI와 스트림 양쪽에서 동작하는 옵트인.

## [0.106.0] - 2026-07-01

### Added

- **`gk worktree finish`에 quality-gate 훅(`--gate`)이 붙어 merge 전후에 외부 리뷰 명령을 실행한다.** feature worktree를 부모/base로 합칠 때 `--gate "xm panel {patch} --json"`을 주면, gk가 target 브랜치 lock을 먼저 잡고 그 lock 하에서 target tip SHA를 고정한 뒤 정확히 merge될 patch를 생성해 gate 명령에 넘긴다 — gate가 승인한 patch와 실제 merge되는 patch가 병렬 finish 사이에서도 어긋나지 않는다. `--gate-phase before|after|both`로 실행 시점을 고르고(기본 before), before gate가 실패하면 merge 없이 `state:"blocked"`(target 무변경)로, after gate가 실패하면 merge는 유지한 채 `state:"paused"`(exit 3)로 멈춰 cleanup을 보류하고 resume/abort 복구 명령을 `result.gate.recover`에 싣는다(`--resume-accept`로 재개, 또는 pinned SHA로 rewind). gate 명령은 `strings.Fields`로 argv 토큰화 후 각 `{token}`(`{patch}`,`{source}`,`{target}`,`{base_sha}`,`{head_sha}`,`{target_before_sha}`,`{target_after_sha}`,`{phase}`)을 단일 argv 원소로 치환해 shell을 거치지 않으므로 injection 경로가 없다(정밀 제어는 반복형 `--gate-arg`, `xm panel` 축약은 `--panel-review`). target lock과 감사용 run-state 파일은 linked worktree 간 공유되도록 `<git-common-dir>/gk/locks/`·`<git-common-dir>/gk/worktree-gate/`에 두고(`--gate-phase both`는 before/after 기록을 각각 남긴다), `--gate-timeout`/`--gate-keep-patch`로 실행 한도와 patch 보존을 제어한다. `--resume-accept`는 브랜치가 실제로 target에 병합됐을 때만 cleanup을 수행하고(미병합이면 `blocked`로 거부해 미병합 작업 손실 방지), after gate는 이미 published된 통합을 되돌릴 수 없으므로 `--push`와 `--gate-phase after|both`의 조합은 거부한다. gate를 주지 않으면 기존 finish 동작과 byte-identical.

## [0.105.0] - 2026-07-01

### Changed

- **`gk agents install`이 기본으로 compact 계약 블록을 설치한다.** 에이전트가 반드시 지켜야 할 최소 git-kit 라우팅 규칙만 instruction 파일에 넣어 세션 컨텍스트 비용을 줄이고, 상세 레퍼런스가 필요한 경우 `gk agents print --full` / `gk agents install --full`로 기존 긴 블록을 선택할 수 있다. 계약 버전은 v22로 올라가며, `gk agents check`는 같은 버전의 compact/full 블록을 모두 최신으로 인정한다.
- **`gk resolve` 경로를 더 보수적으로 다룬다.** agents compact 블록은 AI 충돌 해결 shortcut을 기본 행동으로 제안하지 않고, `resolve`도 명시 요청 시에만 쓰도록 제한한다. 코드 경로에서는 `--strategy ours|theirs`가 AI provider를 보지 않는 순수 기계적 전략으로 고정되고, `resolve --ai`는 hunk index/schema를 검증한 응답만 적용한다.
- **agent/json/CI 모드에서는 TUI prompt를 열지 않는다.** TTY가 있어도 `GK_AGENT=1`, `--json`, `CI=true`인 실행은 interactive로 간주하지 않는다. `gk sync`의 dirty-tree stash prompt, `gk pull --no-autostash`, `gk resolve` interactive fallback, destructive confirmation prompt들이 이제 명시 플래그(`--autostash`, `--strategy`, `--yes` 등)를 요구하는 non-interactive 경로로 빠진다. `gk doctor --fix`는 배치 수정 경로가 없어(finding별 대화형 선택 전용) 비대화형에서 조용히 성공하는 대신 명확히 실패한다 — agent envelope에 `state=error`와 힌트(`gk doctor --json`의 finding별 fix를 직접 실행)를 실어, 에이전트가 no-op을 "복구됨"으로 오독하지 않게 한다.

### Fixed

- **`gk session audit`가 raw `git clone`·`git filter-repo`를 gk-covered로 분류한다.** 종전에는 두 명령이 uncovered로 남아 turn 절감 리포트에서 누락됐는데, 이제 `git clone`은 `git-kit clone`(short-form URL 확장), `git filter-repo`는 `git-kit forget`(history에서 경로 제거)으로 각각 대체 가능함을 인식하고 권고와 함께 집계한다.

## [0.104.0] - 2026-06-30

### Added

- **`gk session audit`가 raw git "발생 횟수"를 넘어 "turn 절감"을 측정한다(`--metric=turns`).** gk의 목적은 에이전트가 여러 tool-call(=여러 turn)로 쪼개 실행한 raw git을 하나의 gk 호출로 접어 turn을 줄이는 것인데, 종전 audit은 raw git을 납작하게 세기만 해 `git status`(turn1)·`git log`(turn2)·`git diff`(turn3)처럼 **별도 turn으로 쪼개진** 경우(접으면 2 turn 절감)와 `git status && git log && git diff`처럼 **이미 한 turn**인 경우(절감 0)를 구분하지 못했다. 이제 `--metric=turns|both`로 켜면 Claude는 assistant message-id, Codex는 function_call 배치로 turn 경계를 잡아(병렬 호출은 같은 turn) 같은 그룹의 raw git이 인접한 여러 turn에 걸친 "collapsible run"을 찾아 `estimated_turns_saved`와 gk 호출별 절감 내역을 보고한다 — 실패-재시도(`is_error`), 다른 repo, 다른 객체를 보는 `git show A`/`B`(paging)는 접지 않는다. `--viz`는 collapsible run을 turn-graph(`●─●`)로 그리고, `--record`/`--trend`는 매 실행을 `~/.gk/audit-history.jsonl`에 적어 절감 추이를 sparkline으로 보여준다. 같은 분류기를 공유하는 `gk agents` PreToolUse 훅은 라이브 세션 transcript의 직전 turn을 읽어, 대기 중인 명령이 직전 raw git run을 이으면 둘을 gk 한 번으로 합쳐 turn을 아끼라고 실시간으로 안내한다. 옵트인이라 `--metric`을 주지 않으면 기존 occurrence 출력·스키마가 그대로 유지된다.

## [0.103.1] - 2026-06-29

### Fixed

- **paused 상태가 이제 종료 코드 3으로 끝나 `gk batch`·`gk land`가 멈춤을 감지한다.** 종전엔 충돌로 중단된 `gk continue`·`gk resolve`나 `gk bisect`의 수동 후보가 `state:"paused"` envelope을 내면서도 프로세스는 **exit 0**으로 끝났다 — 그래서 이들을 자식 프로세스로 실행하는 `gk batch`/`gk land`가 "멈춤"을 성공으로 오인해 다음 단계(예: `push`)로 그냥 넘어갔다(문서가 약속한 "exit 3" 계약과도 어긋났고, batch의 `exit==3` 감지 분기는 도달 불가능한 죽은 코드였다). 이제 paused 명령은 envelope 출력 모드(agent/human)와 무관한 별도 채널인 **종료 코드 3**을 내고(`cmd/gk/main.go`가 `ExitError`에서 코드를 뽑아 종료하되 이미 렌더된 결과 위에 에러를 덧쓰지 않는다), 완료(`done`)된 작업은 종전대로 exit 0이다. `gk batch`/`gk land`는 paused step을 만나면 그 step을 `paused`로 보고하고 나머지를 건너뛰며, 자신도 exit 3을 전파해 상위 batch/land가 중첩 멈춤을 똑같이 감지한다.

## [0.103.0] - 2026-06-29

### Added

- **`gk bisect` — 회귀를 처음 넣은 커밋을 이분 탐색으로 찾는다.** 표준 디버깅 도구인 `git bisect`는 상태 보존형·대화형이라 매 단계 작업 트리 HEAD를 체크아웃하며 휘젓는다 — 에이전트가 중간에 길을 잃기 가장 쉬운 git 시나리오이고 gk가 감싸지 않던 공백이었다. `gk bisect`는 탐색을 버려도 되는 detached worktree에서 돌려 **작업 트리와 HEAD를 건드리지 않는다**. 두 모드를 지원한다: 분류 명령을 `--`로 주면(`gk bisect --good v1.2 --bad HEAD -- go test ./...`) `git bisect run`을 위임해 완전 자동으로 범인 커밋을 `{culprit:{sha,subject,author,date}, good, bad, tested}` envelope으로 반환하고, 명령 없이 시작하면 후보 커밋마다 `state:"paused"`로 멈춰 `gk bisect good|bad|skip`으로 진행하며 `gk bisect reset`으로 끝낸다(rebase paused와 동형 계약, 세션은 `<git-common-dir>/gk/bisect.json`에 영속). 진행 중에는 `gk context`가 `bisect` 필드와 다음 동작을, `gk fleet`이 그 worktree를 `bisect` 상태로 표시한다.

## [0.102.0] - 2026-06-29

### Added

- **`gk fleet`가 여러 저장소를 한 화면에서 감독한다(multi-repo).** 종전엔 `git worktree list` 한 번이라 한 저장소의 worktree 경계를 못 넘었다 — 에이전트가 서로 다른 repo(`~/work/project/agentic/{gk,aic-rust,…}`)에서 동시에 작업하면 한눈에 못 봤다. 이제 `--repos`/`--scan`/`--all`(또는 `fleet.repos`/`fleet.scan`/`fleet.depth`/`fleet.exclude` config)로 multi-repo 모드에 진입하면, 발견된 모든 repo의 worktree를 모아 TUI는 repo 그룹 헤더로 묶어 보여주고(`space`로 접기/펼치기, `w`로 선택 worktree의 `gk status --watch` change-feed로 들어갔다 복귀), `--json`은 flat 배열을 유지하되 각 항목에 `repo`/`repo_root`를 달아 `jq 'group_by(.repo_root)'`로 그룹핑할 수 있다. 발견은 `git rev-parse --git-common-dir`로 dedup해 symlink나 linked worktree로 같은 repo가 중복 집계되지 않고, gather에 실패하거나 3초 내 응답 못 한 repo는 조용히 사라지는 대신 `status:"error"` 항목으로 노출된다. fleet은 네트워크를 쓰지 않고(fetch 없음) 모든 probe를 `GIT_OPTIONAL_LOCKS=0`로 돌려 그 repo에서 편집 중인 에이전트의 `git add`와 `index.lock` 경합을 피한다. 저장소 안에서 인자 없이 실행한 `gk fleet`은 `fleet.*` config가 있어도 기존대로 single-repo로 남으며(config는 저장소 밖에서 실행할 때만 multi-repo를 자동 활성화), 저장소 안에서 강제하려면 `--all`을 쓴다.

## [0.101.1] - 2026-06-29

### Added

- **`gk session audit`와 `gk agents` PreToolUse 훅이 raw `git stash`를 인식해 `git-kit stash`로 안내한다.** 종전엔 stash 분류기가 없어 `git stash`가 `uncovered-raw-git` 갭으로 잘못 집계됐고 block 훅도 그냥 통과시켰다. 이제 `gitSegmentFinding`이 stash를 `raw-stash`(covered)로 분류하며 — 분류기·audit·훅이 공유하는 단일 소스(`hint.go`)라 한 번의 추가로 셋이 함께 개선된다 — `gitKitStashCovers`가 인자를 보고 git-kit stash가 실제 등록한 서브명령(`push`/`list`/`pop`/`apply`/`drop`, 인자 없는 bare 포함)만 covered로 잡고, 대응 verb가 없는 `show`/`clear`/`branch`/`create`/`store`는 갭으로 남긴다(`git checkout -- <file>` 제외와 동형). `git reset`·`git restore`는 gk의 같은 이름 verb가 "리모트로 리셋"·"dangling 작업 복원"이라 의미가 달라 의도적으로 매핑하지 않는다.

## [0.101.0] - 2026-06-29

### Changed

- **`gk push`·`gk ship`의 pre-push 시크릿 스캔이 보고하는 줄 번호를 현재 HEAD 파일 기준으로 맞춘다.** 종전엔 `remote/branch..HEAD`를 `git log -p`로 커밋별로 훑어, 이른 커밋에서 추가된 토큰이 *그 커밋 시점의* 줄 번호로 보고됐다 — 이후 커밋이 그 위에 줄을 끼워 넣으면 최종 파일에서의 실제 위치와 어긋났다. 이제 스캔 기준점(`resolveScanCmp`: 브랜치 upstream → base의 remote ref)을 정하고 그 base와의 net 3-dot diff(`base...HEAD`)로 hunk를 현재 HEAD 파일에 앵커링해, `src/foo.rs:218`처럼 편집기에서 보이는 줄과 일치하는 위치를 보고한다.

### Fixed

- **pre-push 시크릿 스캔이 push 범위 안에서 한 커밋에 추가됐다가 다음 커밋에서 지워진 시크릿도 잡는다.** net 3-dot diff(`base...HEAD`)는 추가와 삭제가 상쇄돼 사라진 시크릿을 보지 못하지만, `git push`는 두 커밋을 모두 발행해 시크릿이 히스토리에 남는다 — 게이트가 정확히 막아야 할 "시크릿을 커밋한 뒤 다음 커밋에서 지우고 push" 경우가 조용히 통과됐다. 이제 `scanCommitsToPush`가 발행될 커밋 범위(`base..HEAD`)를 커밋별로 한 번 더 훑어 history-only 시크릿을 합치고(겹치면 net diff의 HEAD 기준 정확한 줄이 이긴다), `resolveScanCmp`는 리모트에 없는 로컬 base로 폴백하지 않는다 — 폴백하면 그 base 커밋들이 발행되면서도 스캔에서 빠졌기에, fresh/empty 리모트로의 첫 push는 전체 히스토리를 스캔한다.

## [0.100.0] - 2026-06-25

### Added

- **Agent용 worktree lifecycle 명령을 추가한다: `gk wt acquire`, `gk wt finish`, `gk wt cleanup`.** `acquire`는 branch worktree를 만들거나 재사용하고 `worktree.init`을 기본 실행해 JSON `path`를 돌려준다. `finish`는 현재 worktree에서 `promote`(기본) 또는 `land --to`(`--push`)로 부모/base에 통합한 뒤 `--cleanup`/`--delete-branch`를 수행한다. `cleanup`은 기본 dry-run으로 완료된 worktree 후보를 찾고, `-y`에서만 current/dirty/live-lock/protected/unmerged 항목을 제외한 안전 후보를 제거한다.

### Changed

- **`gk follow`가 branch 인자를 생략하면 현재 branch를 따른다.** `gk follow -- make test`처럼 hook만 넘기는 사용이 자연스럽게 current branch mirror로 동작하고, detached HEAD에서는 명시 branch를 요구한다. 컨테이너 기본 실행도 `main` 고정 대신 mounted repo의 current branch를 따르도록 맞췄다.
- **agents 계약 v20에 worktree lifecycle 흐름을 반영한다.** `gk agents install`이 생성하는 CLAUDE.md/AGENTS.md 지시에 `worktree acquire`로 `result.path`를 얻어 cwd로 쓰고, `worktree finish --to parent --cleanup`으로 부모에 통합하며, `worktree cleanup --merged --stale 7d`로 완료 worktree를 일괄 정리하는 agent 표준 경로를 추가한다.
- **`gk wt run`의 `worktree.init` 적용을 `--init` 옵트인으로 바꾼다 — 기본 실행에서는 더 이상 init하지 않는다(종전엔 worktree 생성 시 자동 실행).** 실행(`run`)과 setup(`acquire`)을 분리한다: 이제 `--init`을 줘야 worktree.init이 돌고, 그때는 새로 만든 worktree뿐 아니라 **재사용하는 worktree에도** bootstrap을 다시 적용한다. JSON 결과에 `init` 상태(`done`/`skipped`)를 포함한다.

## [0.99.0] - 2026-06-24

### Added

- **`gk promote`·`gk land`에 `--autostash`를 추가한다 — 부모(받는) worktree가 dirty여도 통합을 진행한다.** worktree에서 작업한 브랜치를 부모로 통합할 때, 부모 브랜치가 다른 worktree에 체크아웃돼 있고 거기 저장 안 한 변경이 있으면 종전엔 `working tree has tracked changes`로 막혔다(promote/land엔 우회 플래그가 없었다). 이제 `gk promote --autostash` / `gk land --autostash`는 내부 `gk merge --into` 단계로 `--autostash`를 전달해, 받는 worktree의 변경을 머지 전에 stash했다가 머지 후 pop으로 되돌린다. 받는 worktree는 남이 작업 중인 상태일 수 있어 **기본이 아닌 명시적 옵트인**이다. 더불어 dirty-receiver 거부 메시지가 이제 `rerun with --autostash …`로 한-플래그 해결책을 안내한다(종전엔 `cd <path>`만).

- **`promote.autostash` / `land.autostash` config(및 `GK_PROMOTE_AUTOSTASH` / `GK_LAND_AUTOSTASH` env)를 추가한다 — 위 `--autostash`를 기본으로 켠다.** worktree 흐름에서 부모 체크아웃이 으레 dirty인 사용자를 위해, 매번 플래그를 붙이는 대신 `gk config set promote.autostash true`(또는 `land.autostash`)로 기본화한다. 해상도는 명시 플래그 > config > 기본(false): config로 켜둬도 `--autostash=false`로 이번 한 번만 끌 수 있다. `gk land --promote` 플래그와 새 `promote:` config 섹션의 이름 충돌은 `reservedConfigSections`에 `promote`를 더해 막았다(플래그는 그대로, config 섹션도 보존).

### Changed

- **`gk pull`이 저장 안 한 변경을 만나면 기본으로 자동 보관(autostash)하고, 인터랙티브 멈춤을 없앤다.** 종전엔 dirty 트리에서 `[s] stash & continue / [c] cancel` 프롬프트를 띄워 멈췄고(화면 없는 CI에선 아예 거부) — 정작 충돌 여부는 stash→통합→복원의 *복원* 단계에서야 갈리는데 그 *전에* 묻는 마찰이었다. 이제 추적 중인 변경을 합치기 전에 stash에 넣고 합친 뒤 되돌리며, 충돌만 없으면 묻지 않고 `stashed N / restored N` 한 줄로 흘려보낸다 — CI(비-TTY) 실행도 더 이상 멈추지 않는다. 복원 시 내 변경과 가져온 커밋이 같은 부분을 건드릴 때만 멈춘다(0이 아닌 종료, 보관함은 그대로 보존되고 명확한 해결 힌트를 단다). 끄려면 `--no-autostash`(또는 `pull.autostash: false` / `GK_PULL_AUTOSTASH=0`)로 예전 게이트를 되살린다(TTY 프롬프트, 비-TTY 거부). `--autostash`는 설정으로 꺼둬도 한 번은 강제로 켠다. `gk sync`의 같은 프롬프트는 이번 변경 범위 밖이다.

- **`gk sw`·`gk wt`(및 모든 TablePicker)가 터미널이 좁아지면 우선순위가 낮은 컬럼을 통째로 떨어뜨려 중요한 컬럼을 지킨다.** 종전엔 컬럼이 화면 폭을 넘으면 bubbles/table이 *오른쪽 끝부터 글자 단위로* 잘라내, 폭은 짧지만 가치 높은 AGE가 가장 먼저 사라지고 UPSTREAM은 글자가 깨진 채 남았다. 이제 `TablePicker.ColumnPriority`(헤더 제목→keep-weight 맵)를 받아, 폭이 모자라면 화면 점유(렌더 할당 폭) 기준으로 가장 낮은 우선순위 컬럼부터 한 컬럼씩 깔끔하게 드롭하고(동률이면 오른쪽부터), 최소 한 컬럼은 남긴다. 드롭된 수는 부제 옆 `+N cols · widen` 노트로 알린다. 제목으로 키를 잡으므로 `gk wt`의 `g` 전역 토글처럼 컬럼 레이아웃이 바뀌어도 우선순위가 어긋나지 않는다. `gk sw`는 BRANCH(항상 유지) > AGE > UPSTREAM > WORKTREE > HASH 순, `ColumnPriority`가 nil인 기존 picker는 동작 그대로다. (참고: 셀에 색이 있으면 bubbles/table이 ANSI 포함 길이만큼 화면을 채우므로, 매우 좁은 폭에선 색칠된 BRANCH 셀이 화면을 다 먹어 AGE까지 떨어질 수 있다 — 이 경우 노트가 안내한다.)

- **`gk wt`(인터랙티브 worktree picker)에 HASH·AGE 컬럼을 추가한다.** 종전엔 BRANCH·SOURCE·PATH·FLAGS(전역 모드는 PROJECT·BRANCH·PATH·FLAGS)만 보여 worktree가 가리키는 커밋과 마지막 작업 시점을 한눈에 알 수 없었다. 이제 로컬 모드는 `BRANCH·SOURCE·HASH·AGE·PATH·FLAGS`로, HASH는 각 worktree의 실제 HEAD(`WorktreeEntry.Head`, detached/bare 포함)에서, AGE는 브랜치 tip 커밋 시각에서 채운다. 위 반응형 드롭과 묶여 BRANCH > AGE > PATH > SOURCE > FLAGS > HASH 우선순위로, 좁아지면 HASH가 먼저 비켜나고 AGE는 끝까지 남는다. 전역(`g`) 모드는 다른 프로젝트의 커밋 시각을 이 repo에서 풀 수 없어 AGE 컬럼을 빼고(빈 컬럼 대신 생략) HASH만 보여준다.

### Fixed

- **이미 머지된 브랜치를 `gk merge --into`(및 그 위의 `gk promote`·`gk land --to`)할 때 받는 worktree가 dirty면 거부하던 버그를 고친다.** source가 이미 receiver의 조상(=통합 완료)이면 머지는 받는 worktree를 전혀 건드리지 않는 no-op인데, 종전 worktree 경로(`runMergeInto`→`runMergeCore`)는 그 판정 *전에* 받는 worktree의 dirty 체크를 먼저 해 `working tree has tracked changes`로 거부했다 — 부모 브랜치 체크아웃에 무관한 변경이 쌓여 있을 뿐인데 promote가 실패하는 혼란. 이제 받는 worktree로 라우팅하기 전에 `isAncestor(source, into)`를 먼저 검사해 `Already up to date — <into> already contains <source>`로 깔끔히 끝낸다(bare 경로는 `base==sourceSHA`로 이미 처리하던 것의 worktree 짝). 실제로 새 커밋이 있는 머지는 종전대로 dirty receiver를 막는다.

## [0.98.0] - 2026-06-24

### Added

- **`gk agents hook`을 추가한다 — Claude Code PreToolUse 강제 훅을 명령으로 설치·원복한다.** `gk agents install`이 까는 지시 블록(CLAUDE.md/AGENTS.md)이 "조언"이라면, 이 훅은 명령 실행 시점의 강제다. `gk agents hook install`이 `settings.json`의 `hooks.PreToolUse`에 Bash 매처 엔트리를 더해, 매 Bash 호출 직전 `gk agents hook run`(이 바이너리의 핸들러)이 명령을 `gk session audit`과 같은 매핑으로 분류한다. 두 모드 — warn(기본, 명령은 그대로 실행하되 에이전트에 노트를 주입)과 block(`--mode block`, covered raw git을 deny해 git-kit으로 재시도시킴). settings 편집은 수술적이다(tidwall sjson/gjson): gk 엔트리만 더하거나 빼고 나머지 훅·설정은 보존하며, `.bak`을 먼저 쓰고 파일 권한을 유지하고 `--dry-run`으로 미리 본다. `--global`은 repo 대신 `~/.claude/settings.json`을 대상으로 한다. 핸들러는 fail-open이다 — 비-Bash·미커버·빈 명령·깨진 stdin이면 아무것도 출력하지 않고 정상 흐름에 맡긴다.

- **`gk hint`를 추가한다 — raw git 한 줄을 그에 대응하는 git-kit 동사로 매핑한다.** `gk session audit`이 쓰는 분류기를 단일 명령에 적용해, 셸 명령(인자 또는 stdin)을 받아 `{covered, kind, covered_by, suggestion, matched}`를 emit한다(`--json`/사람용 한 줄). 체인이면 최고 심각도 패턴을 고르고, `--exit-code`는 git-kit 대체가 있으면 1로 나가 훅 스크립트가 분기할 수 있게 한다. 읽기 전용 plumbing(`rev-parse`/`config` 등)·이미 git-kit인 명령은 통과시킨다. `gk agents hook`의 백엔드이자 audit 매핑의 단일 출처다.

- **`gk session audit`가 미커버 raw-git을 `uncovered-raw-git` gap finding으로 서피싱하고, `checkout`/`switch`·`worktree`를 covered로 승격한다.** 종전 audit은 git-kit이 *대체하는* raw git만 잔소리했고, git-kit이 대응 동사가 없는 raw git은 리포트에 안 보였다(죽어 있던 `gap` 인프라). 이제 어떤 covered 분류기에도 안 걸리고 plumbing(`rev-parse`/`config`/`cat-file`/diff 변형 등)이 아닌 raw git을 모아 subcommand 분해(`stash x4, apply x3, …`)와 함께 emit한다 — 잔소리 도구에서 "무엇을 만들지" 알려주는 로드맵으로. 더불어 raw `git checkout <branch>`/`git switch`는 `git-kit switch`, `git worktree …`는 `git-kit worktree`로 매핑하는 `raw-branch-switch`·`raw-worktree` 분류기를 더해(`checkout -- <path>` 파일 복원 형태는 제외) 종전 gap에 잘못 새던 항목을 covered로 옮긴다. adoption 줄에 미커버 수(`… ; K had none`)를 분리해 채택률 지표가 plumbing을 누수로 오인하지 않게 한다.

- **agents 계약 v19 — 블록 상단에 raw-git→git-kit DON'T→DO 규칙 표를 단다.** `gk agents install`이 CLAUDE.md/AGENTS.md에 까는 계약을, 긴 산문 레퍼런스에서 스캔 가능한 규칙집으로 바꾼다. audit이 잡는 누수 패턴(status/log/diff probe→context, add+commit→commit, checkout/switch→switch, worktree, pull/merge/rebase→pull/sync/…, tag+push→ship, full diff→diff, 체인→batch, short `gk`→`git-kit`)을 표 한 장으로 최상단에 올리고, 읽기 전용 plumbing은 raw로 둔다는 예외를 명시한다. 기존 상세 산문은 `### Detail`로 강등해 보존한다. 계약 버전이 18→19로 올라 `gk agents check`가 모든 repo에서 drift를 감지한다.

- **`gk fleet`이 worktree별 활동성·중단된 작업·parent/land 준비도를 보여주고, 커서 행 상세 패널과 라이브 벽시계를 단다.** 병렬 에이전트 감독에서 "누가 멈췄나·언제 작업했나·정리해도 되나"를 채우기 위해, 엔트리마다 `active_ago_s`(HEAD 커밋 시각, dirty면 변경 파일 mtime으로 끌어올림 — 커밋 안 한 채 편집 중인 에이전트도 "now"), `operation`+`resume`(중단된 rebase/merge/cherry-pick + `gk continue`), `parent`/`parent_behind`/`land_ready`를 더한다(JSON 계약 append-only). `paused`를 새 status로 두어 conflict보다 우선 표시하고(브랜치 옆 `⏸`), TUI는 글랜스 테이블에 커서 행 상세 패널을 붙이고(`enter` 토글) 헤더 우측에 `gk status --watch`와 같은 라이브 벽시계(1초 render-only tick)를 그려 폴 사이에도 살아 있음을 보여준다.

### Changed

- **`gk commit`의 AI classify 기본 timeout을 30s→120s로 올리고, classify 진행을 경과/한도 카운트다운 스피너로 보여준다.** 파일이 많을 때 30s로는 classify 호출이 자주 `context deadline exceeded`로 끊겼다(이 timeout은 재시도까지 포함한 전체 예산이다) — `ai.commit.timeout` 기본값을 120s로 올린다(채팅 timeout은 30s 유지). 더불어 classify 스피너에 `42s / 120s` 경과/예산 카운트다운을 달아(80%↑ 노랑, 95%↑ 빨강) 타임아웃 임박을 눈으로 보게 하고, 완료 라인에 provider model과 토큰 수를 붙인다(`… in 18.3s · <model> · 1.2k tok`). 스피너는 stderr 전용·비-TTY/agent no-op이라 머신 출력엔 영향이 없다.

## [0.97.0] - 2026-06-23

### Added

- **`gk ship`가 `pyproject.toml`·`Cargo.toml`·`pubspec.yaml`/`Chart.yaml`·Python `__version__` 모듈의 버전을 네이티브로 bump한다.** 종전엔 `VERSION`·`package.json`·`marketplace.json` 세 포맷만 교체하고 나머지는 조용히 건너뛰었다. 이제 TOML은 테이블 스코프로 다뤄 `[project]`/`[tool.poetry]`(pyproject)·`[package]`(Cargo) 아래의 `version`만 고치고 의존성 핀은 절대 건드리지 않으며, YAML은 top-level `version:`을, Python은 `__version__ = "…"` 대입을 교체한다. `ship.version_files` 미설정 시 자동 감지 목록에도 `pyproject.toml`·`Cargo.toml`·`pubspec.yaml`이 추가된다.

- **`ship.version_files`가 bare 경로 문자열과 `{path, pattern, key}` 매핑을 한 목록에서 섞어 받는다.** 네이티브 핸들러가 없는 포맷까지 커버하기 위해, `pattern`은 `{version}` 자리표시자 하나를 담은 리터럴 템플릿으로 임의 텍스트 파일의 해당 위치만 교체하고(`__version__ = "{version}"` 등), `key`는 YAML의 점 경로(`tool.poetry.version`)를 주석을 보존한 채 고친다. viper 언마샬에 string→struct 디코드 훅을 더해 두 형식이 같은 리스트에 공존한다(기존 duration·slice 훅은 compose로 보존).

- **`gk init`이 감지한 버전 매니페스트로 `ship.version_files`를 미리 채운다.** 프로젝트 루트의 `pyproject.toml`·`Cargo.toml`·`package.json`·`pubspec.yaml`·`VERSION`을 찾아 `.gk.yaml`에 기록하고, 버전이 태그에만 사는 Go 같은 프로젝트는 `ship:` 섹션 없이 둔다. `gk config init` 템플릿에도 주석 처리된 `ship:` 섹션 전체(version_files 두 형식·watch·verify·auto_confirm·wait)를 문서화한다.

### Changed

- **`ship.version_files`에 적힌 파일을 더 이상 조용히 건너뛰지 않는다.** 네이티브 핸들러도 없고 `pattern`/`key`도 없는 항목은 사일런트 no-op 대신 하드 에러로 막는다 — 버전이 그대로인 파일을 안고 릴리스가 태그되는 사고를 차단한다.

## [0.96.0] - 2026-06-23

### Added

- **`gk session audit`가 shell-chain마다 대체 `git-kit batch --plan -` 계획을 합성한다 — `shell-chain` finding의 마지막 PARTIAL 갭을 닫는다.** 종전 audit은 `git … && git … && git …` 체인을 감지해 "batch를 쓰라"고 권하면서도 정작 어떤 batch 계획으로 바꿀지는 보여주지 못해 status가 `partial`이었다. 이제 각 체인을 segment로 쪼개 raw-git segment를 finding이 쓰는 동일 분류기(`isRawContextProbe`/`isRawConflictProbe`/`isRawFullDiff` 등)로 git-kit verb에 매핑하고(연속 중복 step은 하나로 접고, `echo`/`grep`/`cd` 등 git-kit이 실행할 수 없는 segment는 `omitted`로 분리), 그 결과를 evidence에 붙은 복사-실행 가능한 `{"steps":[{"args":[...]}]}` 한 줄로 emit한다 — 사람 출력엔 `batch plan: git-kit batch --plan - <<< '…'` 줄로, `--json`/agent 봉투엔 `findings[].evidence[].plan`으로. status는 `covered`로 올라가고 gap 문구는 사라진다.

- **`gk session audit`가 git-kit 채택률(adoption) 지표를 보고한다 — 지침 회귀를 측정 가능하게 만든다.** finding 목록은 "무엇이 새는가"는 보여줬지만 "얼마나 새는가"는 한눈에 잡히지 않았다. 이제 `usage` 줄 아래 `adoption: git-kit N of M git calls (P%); K raw calls had a git-kit path`를 출력한다 — `Rate`는 `GitKit / (RawGit+GitKit+GKShort)`, `CoveredRawHits`는 이미 git-kit 대체 경로가 있는 raw-git 패턴 적중 수(covered `raw-*` finding count 합, 순수 습관 누수). audit을 주기적으로 다시 돌려 `Rate`가 오르고 `CoveredRawHits`가 줄어드는지로 지침 변경의 효과를 추적한다. `--json`/agent 봉투엔 `adoption` 객체로 노출된다.

- **`gk fleet` — 여러 worktree를 한눈에 보는 라이브 관제 대시보드를 추가한다.** 병렬 작업(특히 worktree마다 AI 에이전트가 도는 상황)을 사람이 감독할 때 "누가 dirty·충돌·behind인지"를 worktree별 status 조회 없이 보여준다. `git worktree list`를 주기 폴링해 branch·ahead/behind·dirty/conflict·current를 색상 테이블로 렌더하고(`q` 종료·`j`/`k` 이동·`r` 새로고침, 기본 2초 간격, `--interval`로 조정), 파생 `status`(clean/dirty/conflict/ahead/behind/diverged)로 한 줄 롤업한다. `--json`(또는 GK_AGENT)에선 같은 데이터를 1회 스냅샷 봉투로 emit해 에이전트·스크립트가 직접 폴링할 수 있다 — 머신 계약이 먼저고 TUI는 그 consumer다. 기존 `gk worktree list` 보강 로직(porcelain 파싱 + ahead/behind + per-path dirty 프로브)을 그대로 재사용한다.

- **`gk commit -i` / `--interactive` — working-tree 파일을 손으로 커밋 단위로 묶는 TUI를 추가한다.** AI 분류(`gk commit`)에 맡기지 않고 사람이 직접 그룹을 정하고 싶을 때, plan JSON을 손으로 쓰는(`gk commit --plan`) 대신 인터랙티브하게 묶는다. 각 라운드에서 남은 파일을 고르고(라이브 선택 프리뷰) Conventional Commit 메시지를 입력하면(commitlint 통과까지 재프롬프트), 빈 선택을 확인해 종료한다. 결과는 `--plan`이 적용하는 것과 동일한 commit plan이라 검증·시크릿 게이트·백업 ref 뒤 적용을 그대로 공유한다(AI 호출 없음). 고르지 않은 파일은 트리에 남는다. non-TTY에선 `gk commit --plan` 사용을 안내하며 거부한다.

## [0.95.1] - 2026-06-23

### Fixed

- **`gk push`의 stale ahead 카운트 교정이 사람 요약뿐 아니라 `--json`/agent 봉투에도 적용된다.** v0.95.0은 push가 보낼 게 없을 때 git의 "Everything up-to-date"를 stale ahead 카운트보다 신뢰하도록 고쳤지만, 그 교정이 사람용 요약 경로에만 들어가고 `--json` 봉투(`pushResult.Ahead`)에는 빠져 있었다 — remote-tracking ref가 stale한 상황(이미 원격에 있는 커밋)에서 사람에겐 "up-to-date", 에이전트에겐 "N개 푸시됨"으로 서로 모순됐다. 봉투는 에이전트가 신뢰하는 권위 경로인데도 정작 고친 버그가 거기 그대로 남아 있던 셈이다. 이제 두 경로가 공통 `reportedPushCount` 헬퍼를 거쳐 git의 no-op 보고를 단일 권위로 삼아 항상 일치한다. 더불어 upstream 없는 새 브랜치의 첫 push는 ahead가 0으로 하드코딩돼 실제로 커밋을 올리는데도 거짓 "이미 up-to-date"로 표시됐는데, 이제 `rev-list --count <branch> --not --remotes=<remote>`로 아직 원격에 없는 커밋 수를 세어 실제 푸시 수를 보고한다.

- **`gk commit`의 ai-commit backup ref가 `gk timemachine list`·doctor에서 실제로 보인다.** v0.95.0이 매 `gk commit`마다 `refs/gk/ai-commit-backup/<branch>/<unix>` 스냅샷을 남기고 문서가 "`gk timemachine list`로 찾으라" 안내했지만, lister(`gitsafe.ListBackups`)의 종류 allow-list에 그 패밀리가 빠져 있어 git `for-each-ref` 단계에서 걸러져 복구 앵커가 전혀 보이지 않았다. 이제 allow-list에 `refs/gk/ai-commit-backup/`를 추가해 `gk timemachine list`·doctor·`gk timemachine show` 컨텍스트 헤더가 모두 노출하며 `--kind ai-commit`로 필터할 수 있다(함께 누락돼 있던 `forget` 종류 표기도 `--kind` 도움말·docs에 보완).

- **backup ref 문서의 부정확한 설명을 바로잡는다.** `gk timemachine show <ref>`를 `git diff <ref>..HEAD`와 등가로 적었으나, 실제로는 rollback 대상 커밋(커밋 전 HEAD) 자체의 부모 대비 diff를 보여줄 뿐 run이 만든 새 커밋이 아니다 — 잘못된 등가 표기를 제거했다. retention 문구의 "30일 내 최근 10개 유지 … 절대 누적되지 않음"도 실제 union 정책(30일이 지났고 **그리고** 최신 10개 밖일 때만 prune, 최근 스냅샷은 항상 보존 — 커밋 폭주 시 만료 전까지 커밋당 한 개씩 남을 수 있음)에 맞게 정정했다.

### Changed

- **`land.promote`의 `GK_LAND_PROMOTE` 해소에서 잉여 `BindEnv`를 제거한다(동작·우선순위 불변).** v0.95.0이 추가한 `SetDefault`만으로 viper의 `AutomaticEnv` + `.`→`_` replacer가 `GK_LAND_PROMOTE`를 이미 해소하므로(`GK_SHIP_WAIT`와 동일 경로), 중복 `BindEnv`와 사실과 반대로 적혀 있던 "parity with ship.*" 주석을 제거했다 — 환경변수 해소와 flag·config·env 우선순위는 그대로다.

## [0.95.0] - 2026-06-22

### Added

- **`land.promote`를 `GK_LAND_PROMOTE` 환경 변수로도 설정한다 — `ship.*`·`output.*`와 env parity를 맞춘다.** `land.promote`는 `.gk.yaml`·git config로만 켤 수 있어, config 파일 없이 도는 CI·스크립트에서는 promote 기본값을 줄 방법이 없었다(viper `AutomaticEnv`가 등록되지 않은 nested 키를 집지 않아 `ship.auto_confirm` 등과 비대칭이었다). 이제 `land.promote`에 `SetDefault` + `BindEnv("GK_LAND_PROMOTE")`를 달아 `GK_LAND_PROMOTE=parent`(또는 브랜치명)로 promote 타깃을 환경에서 지정할 수 있다. 값 의미는 config와 동일하다 — `parent`는 gk-parent 한 단계(없으면 base), 브랜치명은 거기까지 부모 체인 walk이며, base는 그 브랜치의 실제 이름으로 적는다(`base`라는 단어는 `--to` 플래그 전용).

- **agents 계약 v18 — `GK_AGENT=1`을 매 호출 prefix로 안내하고, 3턴 Quick start와 `--to` 3모드를 명시한다.** 기존 계약은 "Set `export GK_AGENT=1` once"라고 적었는데, 에이전트의 툴 호출은 셸 환경이 호출 간 유지되지 않아 한 번 export해도 다음 호출에서 사라져 envelope 대신 산문이 나오고 "parse prose 금지" 원칙이 조용히 깨졌다. 이제 "매 에이전트 툴 호출을 `GK_AGENT=1 git-kit …`로 prefix하라(사람은 인터랙티브 셸에서 한 번 `export`로 충분)"로 바꿔 함정을 없앤다. 더불어 도입부에 "대부분의 세션은 3턴 — `git-kit context`(오리엔트) → 작업 → `git-kit land`(commit+pull+push), 릴리스는 `ship -y`"라는 Quick start를 넣고, `--to parent|base|<branch>`의 세 모드(parent=gk-parent 한 단계+base fallback, base=곧장 base, `<branch>`=부모 체인 hop-walk)와 `land.promote`/`GK_LAND_PROMOTE` 기본값 연결을 명확히 했다.

### Fixed

- **`gk commit`의 ai-commit backup ref가 무한 누적되지 않도록 retention을 건다(브랜치별 최근 10개·30일).** 매 `gk commit`은 커밋 *전* HEAD를 가리키는 `refs/gk/ai-commit-backup/<branch>/<unix>` 스냅샷을 남기는데, 이 패밀리만 정리 경로가 없어(`git.PruneBackups`는 `refs/gk/backup/*`만, `BranchFF`의 prune은 자기 종류만 본다) clone 수명 동안 ref가 끝없이 쌓였다(실측 한 저장소에 ~73개). 이제 `EnsureBackupRef`가 ref를 만든 뒤 BranchFF와 같은 정책으로 정리한다 — 브랜치별 최근 10개와 30일 이내는 보존하고 나머지를 best-effort로 prune한다(방금 만든 ref는 최신이라 항상 남는다). 더불어 backup ref가 단일 스냅샷임을 문서화해 커밋 결과는 `git diff <ref>..HEAD`로 보도록 안내한다.

- **`gk push`가 동기화된 상태에서 git 영어 줄과 gk 현지화 요약을 이중으로 내지 않고, stale한 ahead 카운트보다 git의 no-op 보고를 신뢰한다.** push가 보낼 게 없을 때 git은(guardEnv의 `LC_ALL=C`로 항상 영어) "Everything up-to-date"를 내는데, gk가 그 위에 현지화 요약을 또 얹어 같은 사실이 두 언어로 중복됐다 — 이제 요약이 그 사실을 전할 때는 git 줄을 억제해 한 번만 보인다. 또한 ahead 카운트는 로컬 remote-tracking ref 기준이라 stale하면(이미 원격에 있는 커밋) 실제론 no-op인데 "N개 푸시됨"이라는 거짓 요약이 날 수 있었다 — 이제 git이 "up-to-date"를 보고하면 그것을 권위로 삼아 요약도 up-to-date로 맞춘다.

## [0.94.0] - 2026-06-22

### Added

- **internal:** add conflict context section and stash-apply detection

### Changed

- **internal:** enhance diff and init features with agent support

## [0.93.0] - 2026-06-21

### Added

- **`gk follow <branch>` — 원격 브랜치를 추종하며 변경될 때마다 로컬을 미러하고 훅을 한 번 돌리는 포그라운드 워처(인프라 0의 "git-sync + watchexec").** ArgoCD/CI 없이 개발 박스·에이전트 샌드박스·단일 컨테이너를 원격에 자동 추종시키고 싶을 때, `git ls-remote`로 원격 SHA를 싸게 폴링하다 SHA가 움직이면 fetch 후 `git reset --hard`로 로컬 체크아웃을 원격 tip에 맞추고(GitOps 미러) 훅 명령을 한 번 실행한다. 파괴적 reset은 git-kit의 안전 wedge로 감싼다 — reset **전에** 항상 backup ref(`refs/gk/follow-backup/<branch>/<unix>`)를 남겨 `git reset --hard <backup-ref>`로 복구할 수 있고, 작업 트리에 미커밋 변경이 있으면(사람의 실수일 가능성) reset을 거부한다(`--discard-dirty`로 명시 우회). 훅은 루프 안에서 동기로 돌아 실행이 겹치지 않으며, 0이 아닌 종료 코드는 폴링 간격을 지수 backoff(interval‥10×)시켜 깨진 커밋이 thrash하지 않게 한다. 데몬 매니저는 내장하지 않는다 — SIGINT/SIGTERM에 깨끗이 멈추는(exit 0) 포그라운드 프로세스로, systemd·docker `--restart`·k8s가 supervise한다(루트 `Dockerfile` 동봉). `GK_AGENT`에선 사이클마다 결과 envelope를 낸다. 플래그: `--remote`(기본 origin)·`--interval`(기본 30s, 초 또는 duration)·`--run "<sh -c>"`(또는 후행 `-- <cmd>...`가 우선)·`--once`·`--discard-dirty`. 이름은 의도적으로 `gk watch`가 아니다 — `gk status --watch`가 정반대(로컬 파일 변경 피드) 의미로 그 이름을 이미 쓴다.

## [0.92.0] - 2026-06-20

### Changed

- **`gk ship`·`gk merge --into`(=`gk land --to`/`gk promote`)가 브랜치 ref를 옮길 때 공유 안전 primitive(`gitsafe.BranchFF`)를 쓴다 — CAS + 이동 전 backup + worktree 가드.** ship의 base fast-forward는 `branch -f`로 CAS 없이 ref를 옮겨, 병렬 worktree가 동시에 base를 건드리면 조용히 덮어쓸 위험이 있었다. `merge --into`(land --to/promote가 호출)는 머지 커밋 전에 backup ref를 안 만들어, 잘못된 머지를 되돌릴 gk 경로가 없었다(`gk reset --hard` 수동 의존). 이제 두 경로 모두 `BranchFF`로 통일한다: 이동 전 항상 `refs/gk/<kind>-backup/<branch>/<unix>`를 남기고(`gk timemachine`으로 복구 가능), oldSHA compare-and-swap로만 옮기며(동시 변경 시 조용한 덮어쓰기 대신 실패), 다른 worktree에 체크아웃된 브랜치는 거부한다. ship의 차단은 `state:"blocked"`(code `base-ff-blocked`)로 보고해 diverged-base 신호와 일관된다. `BranchFF`는 자기 종류(`<kind>-backup`)의 오래된 backup을 자동 정리(최근 10개/30일 보존)해 매 마무리마다 ref가 무한 누적되는 것을 막는다.

- **`gk land --to <branch>`가 임의 브랜치까지 부모 체인을 단계별로 통합한다 — `--to`가 deprecated `--promote`를 완전히 대체.** 지금까지 `--to`는 `parent`·`base`만 받아, 다단계 체인 워크는 deprecated `--promote=<branch>`로만 가능했다(후속 플래그가 별칭보다 표현력이 낮은 역전). 이제 `--to <branch>`는 `--promote=<branch>`와 동일하게 부모 스택을 hop by hop 올린다(체인 밖 타깃은 거부, `gk promote <branch>`와 같은 머신). 더불어 실패·중단 시 재실행 안내(resume)가 더 이상 deprecated `--promote`를 출력하지 않고 `--to` 철자로 안내한다 — bare `--promote`→`--to parent`, `--promote=<branch>`→`--to <branch>`.

- **`gk commit`이 LLM에 보내는 diff 페이로드를 줄여 토큰·비용을 낮춘다 — 큰 파일은 심볼 digest로 접고, compose 컨텍스트를 `-U1`로 줄이며, CLI provider의 diff 이중 전송을 없앤다.** 커밋 메시지 생성은 그룹별 diff를 LLM에 보내는데, 대형 파일·과한 컨텍스트·provider 이중 전송이 입력 토큰을 불필요하게 키웠다. 네 가지를 적용한다: (1) 파일 12KB(`DefaultComposePerFileDiffCap`)/그룹 32KB를 넘는 파일은 raw hunk 대신 한 줄 **digest**(바뀐 함수 심볼·±줄·hunk 수)로 접는다 — 실측 40.8KB 2-파일 커밋에서 ~3,900→~130 입력 토큰(96.7%↓)이고 수정 파일의 심볼은 보존된다. 순수-add 신규 파일은 hunk에 함수 컨텍스트가 없어 added 라인의 top-level 선언명을 추출해 심볼을 복구한다(무-오탐 우선). (2) compose diff를 git 기본 `-U3` 대신 **`-U1`**로 떠 정상 커밋의 diff 본문을 실측 ~15% 줄인다(가감 줄 수는 불변이고, 미리보기 stat 패스는 심볼 컬럼 보존을 위해 `-U3`를 유지). (3) `kiro`·`gemini`·`qwen`이 diff를 프롬프트와 stdin 양쪽으로 **이중 전송**하던 것을 stdin을 비워 끊는다 — diff는 이미 프롬프트에 인라인돼 있어 CLI provider 입력이 ~50% 줄고 내용은 동일하다(무손실). (4) 변경 전체가 단일 확정-종류 그룹(test/docs/ci/build)이면 classify LLM 왕복을 건너뛴다 — 혼합·`chore` 변경은 그대로 LLM이 분리하고, `scope_required`에선 비활성이다.

- **`gk pull`이 base 브랜치의 ahead-only와 진짜 diverged를 구분해 알맞은 remedy를 안내한다.** 기존엔 로컬 base가 원격보다 앞서기만 한 경우(미푸시 커밋만)와 양쪽에 커밋이 갈라진 경우를 같은 신호로 묶어, ahead-only인데도 pull을 권하는 어긋난 안내가 나올 수 있었다. 이제 `countRevs`/`countAheadBehind`로 둘을 가려 ahead-only면 push를, 진짜 diverged면 pull을 제안한다(i18n 메시지·테스트도 구분에 맞춰 갱신).

## [0.91.0] - 2026-06-16

### Added

- **`gk land --to parent|base` — 마무리를 한 단계 통합까지 한 번에 끝내는 단일 wrap-up 동사.** 기존 `gk land`는 commit → `pull --with-base` → push로 끝났고, 푸시 후 상위 브랜치로 머지하려면 의미가 다른 `--promote` 플래그를 따로 써야 했다. 이제 `--to parent`는 현재 브랜치를 부모(`branch.<name>.gk-parent`, 없으면 설정된 base)로 **한 단계** forward-merge하고, `--to base`는 곧장 base로 한 번에 머지한다 — 같은 FF-only promote 머신(`gk merge --into <target>` + `gk push --from <target>`)을 재사용한다. 스택의 중간 브랜치까지 차례로 올리는 다단계 워크는 그대로 `gk promote <branch>`다. `--no-push`는 실행을 로컬에 묶는다: push와 통합 push를 모두 건너뛰고 commit + pull + 로컬 머지만 수행한다(나중에 통합 브랜치에서 publish하는 흐름의 접힌 형태). bare `gk land`는 불변이다 — `--to`를 주지 않으면 base·parent를 건드리지 않는다(위험한 기본값 없음). `--promote`는 `--to`의 deprecated 별칭으로 한 릴리스 동안 유지되며(사용 시 soft stderr 힌트), 기존 흐름과 `land.promote` 설정은 그대로 동작한다. agents 계약 v17.

### Changed

- **에이전트 envelope가 이진 `{ok}`에서 명시적 상태 enum `state: ok|paused|blocked|error`로 바뀐다.** 기존 envelope은 성공/실패만 구분해, 충돌로 *멈춘* 상태("이어서 해줘")와 진짜 실패를 종료 코드 없이는 구분할 수 없었다. 이제 `state`가 1차 분기 키다: pull/merge/rebase 충돌, 부분 resolve, 다시 멈춘 continue 같은 멈춤 결과는 `agentStater` 인터페이스를 통해 `state:"paused"`로 보고되어, 에이전트가 exit code를 들여다보지 않고도 "resume me"와 "done"을 구분한다. `ok`는 파생 별칭(`ok == state=="ok"`)으로 남아 기존 `ok`-분기 소비자는 깨지지 않는다. agents 계약 v15. (`blocked`는 아래 base-FF 통합을 위해 예약됐다 — roadmap RFC #2.)

- **diverged base는 이제 `state:"blocked"` + 안정 코드 `base-diverged`로 보고되고, remedy는 `gk sync`다 — ship/pull의 신호를 통일.** non-base 브랜치(예: develop)에서 `gk ship`은 base(main)를 fast-forward한 뒤 태그하는데, 히스토리가 갈라지면 멈춰야 한다. 이전엔 이게 평범한 `error`였고 계약 텍스트는 잘못된 remedy(`pull --with-base`)를 안내했다 — `pull --with-base`는 로컬 base를 *원격에서* 갱신하는 반대 방향이라 no-op 재시도 루프를 만들었다. 이제 "아무것도 바꾸지 않았지만 먼저 해소해야 하는 선행조건"을 위한 `blocked` 상태를 `WithBlocked(err, code, hint, remedies...)`로 production에 들였고(hintError에 state+code 필드, `FormatErrorJSON`이 `state:"blocked"`·`ok:false`를 존중, localized 메시지도 안정 코드로 매핑), ship의 diverged-base 하드스톱을 여기에 연결했다. remedy는 `gk sync`(브랜치를 로컬 base 위로 rebase — ship의 게이트가 로컬 `isAncestor(base, branch)`라 이게 실제로 게이트를 해소한다)로 못 박았다. `pull --with-base`의 base FF는 거기선 선택적이라 best-effort soft-skip을 유지한다(보고 동등성이지 동작 동등성이 아님 — council 합의). agents 계약 v16.

- **`gk agents check/install`가 `GK_AGENT=1`에서 Codex가 바로 분기할 수 있는 단일 JSON envelope를 낸다.** `check`는 파일별 `scope`·`state`·`reason`·버전과 `drift`/`absent` 집계를 결과에 담고, 명시 타깃(`--global`/`--file`)이 빠져 있으면 별도 에러 JSON을 또 쓰지 않고 `state:"blocked"` + `needs_install` + `install_commands`로 보고한다. `install`도 타깃별 `action`(`created`/`updated`/`unchanged`)을 결과로 낸다. 사람용 출력은 그대로 유지된다.

### Fixed

- **`gk pull --with-base`가 narrow/single-branch fetch 설정에서도 첫 실행에 base remote-tracking ref를 갱신한다.** 기존 base fetch는 `git fetch origin main` 형태라 repo의 `remote.origin.fetch`가 `develop`만 매핑하는 경우 `FETCH_HEAD`만 갱신되고 `origin/main`은 stale하게 남을 수 있었다. 이제 `+refs/heads/<base>:refs/remotes/<remote>/<base>` refspec으로 명시 갱신해 첫 `gk pull --with-base`에서 바로 local base를 fast-forward한다.

## [0.90.0] - 2026-06-16

### Added

- **`gk agents`가 global 스코프를 다룬다 — `--global`로 `~/.claude/CLAUDE.md`·`~/.codex/AGENTS.md`에 설치하고, `check`는 local·global 상태를 한 번에 보고한다.** 기존 `gk agents install`/`check`는 repo 루트의 `CLAUDE.md`/`AGENTS.md`만 봤다. 이제 `gk agents install --global`은 에이전트별 전역 지시 파일에 계약 블록을 심는다(부모 디렉토리는 없으면 자동으로 만든다). 그러면 모든 프로젝트가 gk 사용 규약을 상속한다. 경로는 `$CLAUDE_CONFIG_DIR`(기본 `~/.claude`)와 `$CODEX_HOME`(기본 `~/.codex`)을 따른다. `gk agents check`는 인자 없이 **local(저장소 안일 때)과 global 양쪽**을 스코프별로 묶어 각 파일의 설치 상태와 버전을 출력한다. 설치돼 있지만 구버전인 drift(예: 전역에 남은 v11 블록)는 non-zero exit과 `gk agents install --global` 힌트로 잡고, 아직 설치 안 된 스코프는 정보성으로만 표시해 기본 뷰를 실패시키지 않는다(`--global`로 명시해서 타게팅하면 미설치도 실패한다). repo 밖에서 `gk agents install`은 `--global`이나 `--file`을 안내하는 에러로 끝난다.

### Changed

- **`gk status --watch` 헤더가 HEAD 커밋의 나이를 함께 보여준다.** 라이브 피드 위 컴팩트 헤더 둘째 줄은 마지막 커밋의 짧은 해시와 제목만 보여줘서, 그게 언제 커밋된 건지는 알 수 없었다. 이제 해시와 제목 사이에 나이 칩(`12m`·`1h` 같은 `formatAge` 표기)을 끼운다. 1분이 안 된 커밋은 `now`로 적어, watch 중에 커밋이 들어오면 바로 `now`로 떴다가 다음 refresh마다 `1m`, `2m`로 올라간다. 값은 이미 `headCommitInfo`가 계산하고도 버리던 것이라 추가 git 호출은 없다.

## [0.89.0] - 2026-06-16

### Added

- **`gk log`가 base 통합 경계를 한 줄 divider로 그린다 — 어디까지가 아직 base에 안 올라간 작업인지 한눈에.** `develop`처럼 base(`main`) 위에서 일하면 로컬 브랜치가 base보다 앞서는데, `gk log`만 봐서는 어느 커밋까지가 미머지인지 알 수 없었다(`gk pull`이 "Already up to date"라고 해도 브랜치 간 해시는 다르니 "동기화된 게 맞나" 하는 혼란이 반복됐다). 이제 base에 도달하는 첫 커밋 바로 위에 `──┤ ↑ N unmerged → main ├──` 경계선을 그린다. `--safety`의 push boundary와 같은 모양이고, 색은 `○` 마커와 맞춘 cyan이다. 커밋마다 `○`를 찍는 `--vis merged` 마커와 달리 divider는 한 줄이라 노이즈 없이 **기본 노출**된다. 현재 브랜치가 base와 다를 때만 그려지고, base 위에서는 모든 커밋이 이미 머지된 상태라 자동 생략되므로 추가 `rev-list` 비용이 없다. push boundary와 같은 행에 겹치면 `──┤ ↑ N unpushed · unmerged → main ├──` 한 줄로 합치는데, unpushed가 더 급하니 색은 yellow다. flat과 `--graph` 양쪽 렌더 경로가 같은 배치 로직(`boundaryLines`)을 공유한다.

### Changed

- **`gk commit`이 커밋 메시지 작성을 병렬화한다.** 변경이 여러 커밋 그룹으로 나뉠 때 각 그룹의 메시지를 AI로 동시에 생성해 `gk commit -f`의 대기 시간을 줄였다.

- **`gk commit`의 계획 미리보기가 커밋별 파일 stats를 함께 보여준다.** 큐레이션 멀티커밋 계획(`--plan-template`/preview)을 검토할 때 각 커밋에 묶인 파일 수와 변경 규모를 미리 확인할 수 있고, 출력 들여쓰기·간격도 다듬었다.

## [0.88.1] - 2026-06-15

### Added

- **`gk doctor`가 손상된 commit-graph 캐시를 감지·복구한다 + 재발 방지 하드닝.** git의 commit-graph 캐시(`.git/objects/info/commit-graph(s)`)가 객체 저장소와 desync되면 `gk sync`/`gk pull`이 rebase 도중 `fatal: invalid commit position. commit-graph is likely corrupt`로 멈춘다(인터럽트된 쓰기, 또는 한 repo에 여러 git 프로세스가 동시 접근 — 병렬 에이전트/워크트리 환경에서 흔함). 이제 `gk doctor`에 `repo: commit-graph` 검사가 추가되어 `git commit-graph verify`로 손상을 FAIL로 표시하고(human·`--json` 양쪽, fix 힌트 포함), `gk doctor --fix`는 두 가지를 제안한다 — **repair**(캐시 삭제 후 `git commit-graph write --reachable`로 재생성)와 **repair + harden**(삭제 + `gc.writeCommitGraph`/`fetch.writeCommitGraph`/`core.commitGraph`를 `--local false`로 꺼 재발 차단). 캐시는 객체 저장소에서 재생성되는 순수 성능 최적화라 어느 쪽도 커밋을 잃지 않는다.

- **그 손상 에러가 어느 명령에서 터지든 `gk doctor --fix`로 안내한다.** `invalid commit position` / `commit-graph is likely corrupt` 문구를 에러 체인 단일 지점에서 감지해(not-a-repo 처리와 같은 방식, git 원문 메시지는 보존) hint와 machine-executable remedy(`gk doctor --fix`, safety `safe`)를 입힌다. agent envelope에는 안정 코드 `commit-graph-corrupt`와 함께 노출되어(errcode 어휘는 append-only), 사람·에이전트 양쪽이 동일한 해결책을 받는다.

### Fixed

- **`gk commit`이 gitignored 디렉토리 안의 tracked 파일을 처리하도록 staging을 두 단계로 분리.** 기존엔 `git add -A` 한 번으로 처리해, ignore된 디렉토리 안에 이미 추적 중인 파일이 있으면 add가 실패했다. 이제 tracked 파일은 `git add -u`(ignore 규칙 무시), 신규 파일은 `git add -A`(ignore 규칙 준수)로 나눠 stage한다 — ignore 디렉토리 안의 추적 파일도 정상 커밋된다.

- **`gk ship` preflight가 `GK_AGENT`를 자식 스텝에 흘리지 않는다 — `export GK_AGENT=1` 환경에서 gk 자기 릴리스가 깨지던 문제.** `GK_AGENT=1`은 gk *자신*의 출력을 JSON envelope으로 바꾸는 스위치다. preflight 스텝(`go test`/`golangci-lint` 등)은 자식 프로세스인데 이 env를 그대로 상속해, gk가 자기 자신을 dogfooding할 때(`gk ship`의 preflight가 gk의 `go test ./...`를 띄울 때) 테스트 안의 gk가 envelope으로 출력 → bare 출력을 가정한 단언 수십 건이 이중 래핑으로 깨졌다(다른 프로젝트는 테스트가 `GK_AGENT`를 안 읽어 무관). 이제 preflight는 자식 스텝 env에서 `GK_AGENT`를 제거하고(스텝 결과는 stdout이 아니라 exit code로만 읽으므로 계약 불변), gk 자체 테스트 스위트도 ambient `GK_AGENT`에 무관하게 결정적이 되도록 baseline을 고정했다. 계약서의 "`export GK_AGENT=1` once"와 "ship works under GK_AGENT" 약속이 gk 자기 릴리스에서도 참이 된다.

## [0.88.0] - 2026-06-15

### Added

- **`gk ship --preflight` — 릴리스 없이 preflight만 돌려 미리 검증.** 에이전트/사람이 `ship -y`로 커밋·태그·푸시에 들어가기 *전에* 설정된 preflight(lint/test/goreleaser 등)를 싸게 돌려본다. 릴리스 plan을 만들지 않으므로 **dirty 트리에서도 동작**하고 절대 태그·푸시하지 않는다. 파이프라인의 preflight와 달리 **fail-fast가 아니라 모든 스텝을 돌려** 한 번에 모든 문제를 보여주고, 실패 시 비정상 종료한다(`gk ship --preflight && gk ship -y`로 게이트 가능). `--json`/`GK_AGENT`은 `{result, steps:[{name,command,ok}], failed_step}`(exit 0 — `result` 필드로 분기). "gofmt 위반이 릴리스 도중 preflight를 깨뜨려 수정 커밋을 강요하던" 반복 마찰의 정면 해결책 — agents 규약 v14의 Release 항목이 이걸 `-y` 전에 쓰라고 안내한다.

- **`gofmt` preflight builtin — ship이 포맷 위반을 golangci-lint보다 먼저 잡는다.** preflight 스텝 `command: gofmt`(commit-lint/branch-check/no-conflict와 같은 내장)는 repo의 tracked·비생성 `.go`가 gofmt-clean인지 검사해 위반 시 파일명과 함께 실패한다 — 즉시 끝나므로 느린 lint 전에 fail-fast. Go 모듈이 아니거나 gofmt가 없으면 조용히 통과. gk 자신의 `.gk.yaml` preflight 첫 스텝으로 추가해 dogfood한다. v0.85.0의 commit-time gofmt advisory와 짝을 이뤄 "포맷 위반이 릴리스 도중 preflight를 깨뜨리던" 마찰을 양쪽에서 막는다.

### Changed

- **agents 규약 블록 v14 — Release 항목을 에이전트 실전형으로 재작성하고 `--preflight` 안내 추가.** 얇던 한 줄(`ship --dry-run` / `ship -y`)을, 에이전트가 실제로 막히던 지점을 못 박은 안내로 교체했다: `git-kit ship --dry-run --json`으로 plan(추론 버전·CHANGELOG 초안·preflight/watch/verify 단계·`merge_to_base`)을 먼저 읽고, `git-kit ship -y`는 **GK_AGENT 아래에서도 동작**(진행은 stderr, stdout엔 결과 envelope `{tag,branch,base,merged_to_base,pushed,shipped_on}` — `env -u GK_AGENT` 우회 불필요)하며, preflight가 릴리스를 게이트하니 `gofmt`/테스트를 먼저 통과시키고(`git-kit commit`의 gofmt 경고), non-base 브랜치는 base를 FF 후 태그(분기 시 `git-kit pull --with-base`), `--wait=false`/`ship.auto_confirm` config, 미출하 확인은 `git-kit context --include=release`. 추가로 v14는 "`-y` 전에 `git-kit ship --preflight`로 검증"을 명시해 위 `gk ship --preflight` 기능을 안내한다. `gk agents install`로 CLAUDE.md/AGENTS.md가 v14로 갱신된다. release 스킬 Phase 4에도 `ship -y`가 GK_AGENT 세션에서 그대로 동작함을 명시.

- **`gk status --watch`가 라이브 체인지 피드로 재편 — "풀 status 2초 재렌더" 대신 "지금 뭘 편집 중인가".** 기존 `--watch`는 전체 status 블록을 2초마다 통째로 다시 그렸는데, 세션 중 거의 안 변하는 블록(DIVERGENCE·ACTIVITY·NEXT)까지 매번 재렌더하는 비용이 컸고 정작 라이브로 의미 있는 "변하는 파일"엔 시간축이 없었다. 이제 `--watch`는 **변화의 타임라인**이다: 작업 트리를 스냅샷해 직전과 비교하고 **new / re-touched / cleared** 이벤트를 쌓는다(글리프 `+`new `~`mod `−`del `→`rename `⚔`conflict `✓`cleared + `+N −M` stat + 1/100초 정밀 시각 `14:25:18.11`로 실시간 감). 화면은 split — 상단에 압축 status 헤더(repo · branch ⇄ upstream ↑↓ + HEAD 짧은해시·제목 + 변경 파일수·총±)가 고정되고 하단 `─── live changes ───` 아래 피드가 남은 높이를 채우며 스크롤한다(헤더 값은 매 렌더가 아니라 refresh 때만 가져와 렌더당 git 호출 0). 구분선 우측에는 **매초 똑딱이는 라이브 시계 `● 14:25:18`** — 파일 변화가 없어도 UI가 살아있음을 보여준다(render-only 1초 틱, git 호출 0). **`[s]`로 풀 status 대시보드**(기존 watch의 트리/DIVERGENCE/ACTIVITY)를 그 자리에 띄웠다 끌 수 있다. 트리거는 **fsnotify가 1차**(파일 변경 순간 ~200ms debounce, idle 비용 ≈ 0)이고 12s heartbeat 폴이 안전망 — fsnotify 미가용(미지원 플랫폼·디렉토리 수가 descriptor 예산 초과·설정 실패)은 `--watch-interval` **폴링으로 자동 폴백**한다. 재귀 감시는 `.gitignore` 디렉토리(node_modules 등)·`.git`을 제외하고 런타임에 새로 생긴 디렉토리도 따라붙는다. 스냅샷은 `git status --porcelain -z`/`diff --numstat -z`를 모두 `--no-optional-locks`로 호출해 agent의 `git add`와 .git/index.lock 경합을 피한다(폴링 폴백 시 틱 사이 중간 편집은 net만 보이는 게 한계). 비-TTY(파이프/캡처)는 `tail -f`식 append-only 스트림(상위 도구가 소비 가능). 한동안 `--watch-files`로 시험하던 이 동작을 `--watch`로 합치고 별도 플래그는 제거했다.

### Fixed

- **`gk status --watch` 체인지 피드 정확성 4건 (Codex 리뷰).** (1) `git status` 실패를 빈 스냅샷으로 삼켜 깨진 repo/`--repo` 비-repo에서 "working tree clean"을 무한 표시하던 문제 → 진입 시 worktree root를 한 번 해석해 실패면 명확히 에러(`not a git repository`)로 중단. (2) 이미 dirty인 파일을 ±수 변화 없이 재저장(동수 줄 교체 등)하면 `re-touched`가 드롭되던 문제 → `fileSig`에 mtime을 포함해 모든 재저장을 잡는다. (3) watch 중 새로 생긴 ignored 디렉토리(`npm install`의 node_modules 등)가 startup 스냅샷에 없어 감시에 붙어 descriptor 예산을 소진하던 문제 → 새 디렉토리 Add 전 `git check-ignore`로 재확인(미감시 디렉토리의 자식은 이벤트가 안 와 신규 top 디렉토리당 1회로 한정). (4) staged-add(`A `)가 `+` 대신 노란 `~`로 표시되던 문제 → `added`도 `+` 글리프로.

- **ship의 `--json`+라이브-실행 거부 에러에 안정 코드 부여.** `gk ship -y --json`(또는 명시 `--json`)이 거부될 때 agent envelope의 `error.code`가 `unknown`이었다 — agents 계약은 "code로 분기"를 약속하는데 분기할 수 없었다. 이제 `json-needs-dry-run` 안정 코드를 반환한다(errcode 어휘는 append-only).

- **`gk status --watch`를 빠져나오면 이후 셸 글씨가 밀리던(터미널 깨짐) 문제.** lipgloss 기본 렌더러는 첫 스타일 렌더 때 터미널에 OSC 11(배경색)+DSR 질의를 lazily 보내는데, 이게 bubbletea 세션 *중*에 일어나면 bubbletea의 stdin 리더와 응답을 두고 경합해 미소비 응답 바이트가 종료 후 셸 입력으로 새어 프롬프트가 어긋났다. 이제 bubbletea가 stdin을 잡기 **전에** `lipgloss.ColorProfile()`/`HasDarkBackground()`로 감지를 강제해(응답을 cooked 모드에서 깨끗이 소비) 세션 중엔 캐시만 쓰고 질의하지 않는다 — PTY 검증: OSC 11 질의 1회, alt-screen 진입 전, 세션 중 재질의 없음.

- **`GK_AGENT=1 gk ship -y`가 "requires --dry-run"으로 거부되던 문제.** agent 모드는 `--json`을 *암시*하는데, ship의 `--json`은 릴리스 *계획* 출력 계약이라 `--dry-run`과만 짝지어야 해서, 에이전트가 자연스럽게 친 `GK_AGENT=1 gk ship -y`가 에러로 막혔다(release 스킬도 `env -u GK_AGENT`로 우회 중이었다). 이제 **암시된** `--json`(명령줄에 `--json`을 타이핑하지 않음, `Changed("json")==false`)으로 라이브 실행에 들어오면 사람용 진행 출력을 stderr로 흘리고 **끝에 결과 envelope를 stdout**으로 내보낸다(`{tag,branch,base,merged_to_base,pushed,shipped_on}`) — stdout은 깨끗한 JSON 한 덩어리로 유지된다. **명시적** `--json`(`gk ship -y --json`)은 계획 문서를 요청한 것이므로 종전대로 거부한다(스트리밍 실행은 단일 계획 JSON을 만들 수 없음).

## [0.87.0] - 2026-06-15

### Added

- **`gk worktree run <branch> -- <command>` — 격리된 병렬 작업의 단발 CLI.** `<branch>`용 worktree를 만들거나(이미 그 브랜치를 체크아웃한 worktree가 있으면 재사용) 그 안에서 명령을 실행하고(작업 디렉토리 = worktree), 명령의 exit code를 그대로 전파한다 — 새 브랜치는 HEAD에서 잘라 managed-base 레이아웃에 두고 gk-parent를 기록하며 `worktree.init`(link/copy/run)을 적용한다. `--cleanup`은 명령이 성공(exit 0)하면 worktree를 회수하고, 이 호출이 브랜치까지 만든 경우 브랜치도 삭제한다 — 실패한 명령은 검사할 수 있게 worktree를 그대로 남긴다. `--` 뒤는 셸을 거치지 않고 직접 실행하므로 연산자가 필요하면 `sh -c '...'`로 감싼다(`gk worktree run feat/api --cleanup -- sh -c 'npm ci && npm test'`). `--from <ref>`(새 브랜치 베이스, 기본 HEAD), `--init`/`--no-init`(부트스트랩 강제/생략). 결과는 `{path,branch,created,command,exit_code,removed}`로 보고한다(`--json`/`GK_AGENT`). Workflow의 worktree-격리 패턴을 명령 한 줄로 쓰는 형태다.

- **per-worktree 상태 — "어느 worktree에 미완 작업이 있나"를 한 호출로.** `gk worktree list --json`과 `gk context`가 worktree별 branch·ahead/behind·parent·lock·dirty(staged/unstaged/untracked) 신호를 함께 싣는다 — 경로마다 따로 `git status`를 돌리지 않고 한 번에 답한다.

- **`gk batch`의 per-step worktree 타게팅.** plan의 각 step에 `"worktree"` 필드(브랜치 이름 또는 절대 경로)를 주면 그 step을 해당 worktree에서 실행한다 — 한 트랜잭션이 여러 worktree를 가로지른다. 브랜치 이름은 그 위에 체크아웃된 worktree로, 경로는 등록된 worktree일 때만 해석되며, 해석 실패는 그 step의 `on_failure` 정책을 따르는 실패로 처리된다.

### Changed

- **agents 규약 블록 v12 — `worktree run` 격리 태스크 안내 추가.** `gk agents` 계약 블록에 **Isolated worktree task** 항목을 신설했다: `git-kit worktree run <branch> -- <command>`(브랜치용 worktree 생성/재사용 → 그 안에서 명령 실행 → 명령의 exit code로 종료, `--cleanup`은 성공 시 worktree 회수+생성한 브랜치 삭제, `--from`/`--init`/`--no-init`)와, "어느 worktree에 미완 작업이 있나"를 경로별 프로브 없이 한 호출로 답하는 `git-kit worktree list --json`(worktree별 ahead/behind·parent·lock·dirty). `gk agents install`로 CLAUDE.md/AGENTS.md 블록이 v12로 갱신된다.

- **`gk st` rich BRANCH 섹션이 HEAD 커밋을 두 줄로 — 전체 해시 + 커밋 제목.** 기존엔 정체성 줄 끝에 `· last commit 7h 2f6e7520`처럼 짧은 SHA만 붙어 무슨 커밋인지 알 수 없었다. 이제 1줄은 브랜치 정체성 + `· 7h ago`(상대 시각)로 끝나고, 2줄에 **전체 40자 해시**(복붙 가능, git-log 관례대로 노란색)와 **커밋 제목(subject)**이 내려온다. 제목은 터미널 폭(3칸 indent + 40자 해시 + 2칸 gap 차감)에 맞춰 `runewidth`로 잘려 줄바꿈을 막고, non-TTY(파이프/캡처)에선 전체를 그대로 출력한다. `--vis staleness` 레이어에 속하므로 staleness를 끄면 함께 사라진다.

### Fixed

- **`gk worktree add --dry-run`이 실제로 worktree를 만들던 버그.** `--dry-run`이 무시돼 계획만 보려 해도 worktree가 생성됐다. 이제 `--dry-run`은 아무것도 만들지 않고 계획만 내며, agent envelope/`--json` 결과 `{path,branch,parent,created,init}`로 보고한다(기존엔 사람용 성공 줄뿐).

- **`gk switch` 피커에서 `d`/`D`로 워크트리 브랜치를 지우면 "delete failed"로 끝나던 버그.** 다른 워크트리에 체크아웃된 브랜치(피커의 WORKTREE 컬럼에 표시되는 행)에 커서를 두고 `d`를 누르면 `git branch -d`가 실행됐는데, git은 워크트리가 점유한 브랜치 삭제를 거부한다(`cannot delete branch 'x' used by worktree at ...`) — 사용자는 워크트리를 치우려는 의도였지만 브랜치 삭제가 시도되며 혼란스러운 실패만 봤다. 이제 `d`/`D`가 점유 브랜치를 감지하면 `gk wt`의 워크트리 제거 흐름(`worktreeTUIRemove`)으로 리다이렉트한다 — lock(살아있는 holder는 강제 확인, stale은 unlock+remove)·dirty(강제 제거 확인)·stale 레코드(prune)를 모두 처리하고, 제거 후 해방된 브랜치를 지울지도 물어본 뒤 피커를 다시 그린다. 재사용을 위해 `worktreeTUIRemove`/`worktreeLockInfo`/`forceRemoveWorktree`/`maybeDeleteOrphanBranch`의 시그니처를 `*git.ExecRunner`/`*cobra.Command`에서 `git.Runner`/`io.Writer` 인터페이스로 일반화했다(동작 불변).

## [0.86.0] - 2026-06-14

### Added

- **`gk resolve`가 continue까지 끝낸다 — 충돌 한 번에 작업 완주.** 지금까지 resolve는 충돌을 풀고 "run 'gk continue'"를 출력하며 멈췄다(에이전트 기준 호출 1번 손해, 사람 기준 명령 하나 더). 이제 전체 해결에 성공하면 `git <op> --continue`를 직접 실행하고, batch 모드(`--strategy`/`--ai`)에서는 이후 pick이 또 충돌해도 같은 전략으로 재해결하며 rebase가 끝날 때까지 루프한다 — `gk pull --ai`가 서브프로세스 재실행으로 하던 루프의 in-process 판이며, pull --ai도 이 경로를 타도록 정리했다. 해결 결과가 빈 pick이 되면(`--strategy ours`로 상류와 동일해진 경우 등) 빈 커밋 실패 대신 `--skip`으로 자동 건너뛴다 — stderr 파싱 없이 `diff --cached --quiet` 구조 판정. interactive(TUI) 모드도 세션 후 continue까지 가되, 다음 pick의 새 충돌은 루프하지 않고 사람에게 돌려준다. `--no-continue`로 기존 2단계 흐름 유지, `--dry-run`은 여전히 아무것도 건드리지 않는다. batch 모드에 `--json`/`GK_AGENT` 결과 `{resolved, total, rounds, skipped_empty, done, state, resume}`도 신설(기존엔 prose뿐). 부수: 충돌 파일이 있어도 in-progress op이 없으면(stash apply 등) "run 'gk continue'" 힌트를 더 이상 출력하지 않는다 — continue할 대상이 없는데 안내하던 오류. agents 규약 블록 v11.

- **`gk resolve`가 delete/modify·markerless 충돌도 해결한다 — index stage 기반 degenerate 경로.** 지금까지 worktree에 파일이 없거나(한쪽이 삭제) 충돌 marker가 없는 unmerged 파일은 "git rm 또는 git checkout --ours를 직접 치라"는 힌트만 내고 건너뛰었다. 이제 `:1/:2/:3` stage에서 직접 해결한다: `--strategy ours|theirs`는 선택한 쪽을 복원하거나 그쪽이 지운 파일이면 삭제(기계적), `--ai`는 양쪽 전체 내용과 삭제 플래그(`ours_deleted`/`theirs_deleted`)를 provider에 보내 keep/delete/merge를 근거와 함께 결정한다. binary 충돌은 AI 제외(ours/theirs만). 함께 잡은 잠복 버그: modify/delete에서 git이 살아남은 쪽을 worktree에 남기면 marker가 없어서 "hunk 0개 = 해결됨"으로 집계했는데 index는 unmerged인 채라 continue가 막혔다 — 이제 양쪽 stage가 있는 markerless 파일은 수동 해결로 받아들여 stage하고, 비대칭이면 degenerate 경로로 보낸다.

- **`gk resolve`가 worktree root에 앵커링된다.** 충돌 파일 IO가 프로세스 cwd 기준 상대 경로여서 `--repo <path>`로 repo 밖에서 실행하거나 repo 하위 디렉토리에서 실행하면 파일을 못 찾았다. 이제 `rev-parse --show-toplevel`로 root를 잡아 모든 git 호출과 파일 읽기/쓰기/백업을 root 기준으로 수행한다.

### Fixed

- **`gk continue`가 보이지 않는 에디터를 기다리며 무한 대기하던 버그.** `git rebase|merge|cherry-pick --continue`는 커밋 메시지 확인을 위해 에디터를 띄우는데, ExecRunner가 stdout/stderr를 파이프로 캡처한 채 실행하므로 vim이 화면 없이 멈춰 있었다(셸에 `GIT_EDITOR`가 없으면 항상 재현 — `GIT_EDITOR=true`인 에이전트 환경에서만 우연히 통과). 이제 guardEnv에 `GIT_EDITOR=true`를 넣어 모든 git 자식 프로세스가 준비된 메시지를 그대로 쓴다(`GIT_TERMINAL_PROMPT=0`과 같은 비대화 원칙). 함께: `continue`/`abort`/`resolve`가 `ExtraEnv: os.Environ()`으로 guardEnv를 도로 덮어쓰던 경로 제거(사용자 셸의 `GIT_EDITOR=vim`이 가드를 이겼다), 성공 시 침묵하던 `continue`에 `✓ <op> complete` 출력과 `--json`/`GK_AGENT` `{action, done}` 결과 추가(침묵은 행과 구분 불가).

- **`gk continue`의 사전 NOTE가 정반대 안내를 하던 문제.** 기존 "working tree still has changes (they will be included)"는 충돌 해결로 staged된 파일 때문에 매번 출력되는 노이즈였고, 내용도 틀렸다 — `--continue`는 index만 커밋하므로 unstaged/untracked 파일(예: `gk resolve`의 `*.orig` 백업)은 포함되지 **않는다**. 이제 staged 변경(정상 상태)에는 침묵하고, unstaged/untracked 잔여물이 있을 때만 "working tree에 남고 커밋에 포함되지 않는다"고 정확히 알린다.

## [0.85.0] - 2026-06-11

### Added

- **`gk commit --plan` / `--plan-template` — 큐레이션 멀티커밋을 JSON 계약으로.** 그루핑을 AI가 아니라 호출자(주로 에이전트)가 정할 때, `--plan-template`이 dirty 파일 목록을 초안 JSON으로 내고, `{"commits":[{"message","files":[...]}]}`를 `--plan -`에 주면 N개 커밋이 한 번의 결정론적 호출로 만들어진다 — `rebase --plan` 철학의 커밋 생성판. 중복/미존재 파일·형식 불량 메시지는 커밋 전에 전부 거부되고(아무 일도 안 일어남), plan이 다루지 않은 파일은 dirty로 남는다. secret scan·backup ref·`--abort`는 AI 플로우와 동일 계약이고, 결과는 `{result, commits:[{...,sha}], failed_at?, backup_ref}`로 보고된다(중간 실패는 `partial`). hunk 분할(한 파일을 두 커밋에)은 미지원. 부수 개선: ApplyMessages가 staged 삭제/리네임 상태를 그룹마다 재계산해 "첫 커밋이 지운 파일이 다음 그룹을 깨뜨리는" 잠재 버그가 사라졌고, breaking(`!`) 마커와 `allow_empty`(빈 커밋)도 plan에서 보존된다.

- **`gk commit`의 gofmt advisory 게이트.** go.mod가 있는 repo에서 커밋 대상 `.go` 파일에 gofmt 위반이 있으면 커밋 전에 █ NOTE로 파일 목록과 `gofmt -w` 명령을 알려준다 — 위반이 ship preflight(golangci-lint)까지 살아남아 릴리스 도중 수정 커밋을 강요하던 5턴 손해의 예방선. 차단이 아니라 advisory이며(`--no-verify`로 게이트 자체 skip), gofmt 바이너리가 없거나 Go repo가 아니면 조용히 비활성, 생성 코드(`*.pb.go`, `*_gen.go`, `zz_generated*`)는 제외한다.

- **`gk context --include=release` — "뭐가 아직 안 나갔지?"를 context 한 호출로.** 최신 tag 이후(`tag..HEAD`)의 커밋 수와 요약(최대 20개, 총수는 `commit_count`)을 context 문서에 융합한다. 릴리스 오리엔테이션이 `describe`+`rev-list`+`log` 프로브 체인 대신 `--include=all` 한 번이 된다. tag가 없는 repo는 섹션 계약대로 `notes`로 강등될 뿐 호출은 실패하지 않고, 네트워크는 건드리지 않는다.

- **agents 규약 블록 v10.** Curated multi-commit(`commit --plan`) 항목과 `--include=release`를 반영하고, "상태 질문은 raw git 프로브 체인 대신 context 한 호출" 문구를 명시로 강화했다. `gk agents install`로 CLAUDE.md/AGENTS.md 블록이 갱신된다.

### Fixed

- **Easy Mode가 에러 본문에 인용된 자식 프로세스 출력까지 번역하던 문제.** v0.84.0의 커맨드 위치 가드는 `git commit` 같은 호출 토큰만 보호했고, lint 출력·git stderr 같은 인용 블록 자체는 여전히 용어 치환을 통과했다(릴리스 중 실측: golangci-lint가 인용한 Go 소스가 `작업 갈래 (Branch) string`으로 깨짐). 이제 `(stderr=`/`(stdout=` 괄호 블록과 `exit code N:`/`exit status N:` 이후의 자식 출력 구간을 placeholder로 마스킹한 뒤 번역하고 원형 복원한다 — 주변 prose는 계속 번역된다. `gk do`가 자식 git 출력을 무조건 번역하던 경로도 제거했다(원시 출력은 증거이지 산문이 아니다).

## [0.84.0] - 2026-06-11

### Added

- **`gk promote` — 네트워크 없는 로컬 승격 명령.** `gk land --promote`에서 push를 뺀 짝: 변경이 있으면 먼저 AI 커밋하고, 현재 브랜치를 부모(gk-parent 메타데이터, 없으면 base)로 forward-merge한다. `gk promote <branch>`는 parent 체인을 한 홉씩 걸어 중간 브랜치도 함께 전진시키고, `--push`를 붙일 때만 홉마다 전진한 브랜치를 발행한다(`push --from <target>` — land --promote와 동일 동작). 받는 브랜치는 worktree가 없어도 된다: FF는 ref 갱신, 깨끗한 non-FF는 merge-tree 커밋, 진짜 충돌만 체크아웃을 요구한다. 통합이 로컬에서 끝나야 할 때(land는 너무 일찍 push) 쓰며, land와 같은 per-step 결과 계약을 따른다.

- **`gk config setup` — log/status 표시 레이어 picker와 AI 연결 보존.** 위자드가 라이브 프리뷰를 보여주며 log/status 시각화 레이어를 고르게 해 `log.vis`/`status.vis`/`log.graph`/`status.xy_style`을 처음부터 설정할 수 있다. 신규 레이어도 함께 들어왔다: log에 `wip`(스택 깊이 ≡N)·`squash`(⊟ 마커)·`breaking`(‼ 마커), status에 `wip`·`squash`(◈ debt)·`ancestry`(스택 깊이)·`collision`(⊠ worktree 겹침). setup을 다시 실행해도 기존 AI provider/endpoint/API key는 플래그나 명시적 확인 없이는 덮어쓰지 않고, 바뀌지 않은 값은 아예 쓰지 않아 멱등이다.

- **`land.promote` — land의 promote 단계를 config 기본값으로.** develop에서 마감할 때마다 `--promote`를 붙이는 워크플로라면 `land.promote: parent`(또는 `true` — YAML bool도 bare 의미로 허용)로 한 홉 승격이, `land.promote: main`처럼 브랜치명을 주면 parent 체인 워크가 기본이 된다. 명시 `--promote` 플래그는 언제나 config를 이기고, 새 `--no-promote`는 그 1회만 단계를 끈다(`false`/`none`/`off`도 config 쪽 해제값). dry-run 플랜과 JSON step 계약에는 기존 promote 단계 그대로 나타난다.

- **`ship.auto_confirm` / `ship.wait` — ship의 확인 프롬프트와 CI 대기를 config 기본값으로.** 매 릴리스마다 `-y`를 치는 사용자라면 `ship.auto_confirm: true`로 프롬프트 스킵이 기본이 된다 — 한 번만 다시 확인하고 싶을 때는 `--yes=false`. tag push 뒤의 watch/verify 파이프라인도 `--wait=false`(또는 `ship.wait: false`)로 건너뛸 수 있다 — ship은 push에서 끝나고, 건너뛴 단계의 명령은 NOTE로 출력해 CI가 돈 뒤 손으로 실행하게 한다(태그는 이미 공개돼 있으므로 명령은 그대로 유효하다). 두 키 모두 명시 플래그가 어느 극성이든 config를 이기는 `--graph` 해상도 규칙을 따르고, `ship --dry-run --json` 계약에는 해상된 `wait` 값이 실린다.

### Fixed

- **Easy Mode가 에러 본문 속 인용된 커맨드까지 번역하던 문제.** 에러 본문은 실패한 명령을 그대로 인용하는데(`aicommit: git commit -m … -- ghostty: exit code 1`), 용어 치환이 커맨드 토큰을 구분하지 못해 `git 변경사항 저장 (commit) -m …`처럼 복붙 불가능한 형태로 깨졌다. 이제 `git `/`gk `/`git-kit ` 호출 토큰 바로 뒤의 용어는 커맨드 위치로 보고 치환을 건너뛴다 — hint 줄의 번역 전면 우회와 같은 보호를 에러 prose 안의 커맨드로 확장한 것. 산문은 그대로 번역된다("Git commit 컨벤션" 같은 대문자 표기 포함 — 커맨드 라인은 소문자뿐이므로 소문자 호출 토큰만 가드).

- **net-zero WIP 체인에서 `gk commit`이 unwrap 후 "nothing to commit"으로 죽던 버그.** 나중 WIP가 앞선 WIP의 변경을 되돌린 체인(예: 설정 파일을 고쳤다가 원복)에서, 체인 파일 목록이 커밋별 합집합(`MergeChainFiles`)이라 내용 상쇄를 못 봤다 — AI가 그 경로로 커밋을 계획하고, unwrap(reset)하면 트리가 깨끗한데 `git commit -- <path>`가 exit 1로 죽었다(HEAD는 이미 풀린 채로). 이제 체인 파일 목록을 실제 `HEAD~N→HEAD` net diff(`ChainNetFiles`)로 구해 상쇄가 자연히 사라지고, 트리가 깨끗한 net-zero 체인은 AI 호출 없이 백업 ref 뒤에서 체인만 풀고 정상 종료한다("WIP chain nets to zero — unwrapped; nothing to commit" + `--abort` 복원 힌트). dirty 변경이 함께 있으면 그 변경만으로 평소처럼 진행한다.

## [0.83.0] - 2026-06-11

### Added

- **land:** --promote=<branch> walks the parent chain hop by hop

### Fixed

- **land:** bare --promote climbs one hop to the branch's parent

## [0.82.0] - 2026-06-11

### Added

- **`gk log --ahead/--behind --base`·`gk log --merged` — base 기준으로 "무엇이 아직 안 들어갔나".** `--ahead`/`--behind`는 지금까지 upstream(`@{u}`)만 봐서, `gk status`가 "ready to merge into main"이라 말하는 ↑N과 숫자가 어긋났다 — develop이 `origin/develop`과 동기화됐어도 main보다 앞서 있으면 `--ahead`는 0을 보고했다. `--base`를 더하면 status와 같은 base 해석기(`resolveBaseForStatus`)로 `<base>..HEAD` 범위를 펼쳐, 그 ↑N과 커밋 수가 정확히 일치한다. `--merged`는 같은 정보를 커밋별 마커로 — 아직 base에 없는 커밋 앞에 `○`를 찍는다(push 마커 `◇`와 같은 "행동이 필요할 때만 강조" 방식, `git rev-list <base>` 한 번의 집합 비교). status의 ready-to-merge 줄도 이제 `gk log --ahead --base`를 가리킨다. 마커는 push 집합과 마찬가지로 SHA 동일성 기반이라 squash/rebase로 머지된 커밋은 미머지로 보인다(legend에 명시).
- **`gk config set <key>+= <값>` / `<key>-= <값>` — 리스트 키 항목 추가·제거.** `log.vis`·`ai.commit.deny_paths` 같은 리스트 키는 scalar `set`을 거부해 지금까지 `gk config edit`로 손편집해야 했다. 이제 키 끝에 `+=`/`-=`를 붙여 한 항목만 제자리에서 넣거나 뺀다(`gk config set log.vis+= merged`) — 주석과 flow 스타일은 보존되고, `+=`는 멱등(이미 있으면 무시), 없는 항목 `-=`는 무동작이다. 연산자를 값이 아니라 키에 붙인 건 값이 `-`로 시작하면 cobra가 플래그로 오해하기 때문이다.
- **`gk land --promote` — 마감 트랜잭션 끝에 base 승격까지.** push 다음 단계로 현재 브랜치를 base에 forward-merge하고(`gk merge --into <base>`) 밀어 올린다 — develop에서 마감한 뒤 `gk merge --into main` + `gk push --from main`을 손으로 치던 흐름이 한 플래그가 됐다. 그냥 `--promote`는 설정된 base를, `--promote=<branch>`는 명시한 대상을 노린다. 충돌은 평소의 resolve/continue 계약으로 일시정지하고 그 단계를 실패로(복귀 경로와 함께) 보고하며, 이미 base 위라면 단계를 건너뛴다.

### Fixed

- **Easy Mode가 config 키·경로 안의 git 용어까지 번역하던 문제.** 한국어 Easy Mode의 용어 치환이 `\bcommit\b` 식 단어 경계 매칭을 써서, `ai.commit.model`처럼 점으로 이어진 식별자 안의 `commit`까지 잡아 `ai.변경사항 저장 (commit).model`로 깨뜨렸다(`gk config set`의 에러 메시지에서 드러났다). 매칭 앞뒤가 `.`/`/`로 다른 식별자 문자와 이어지면 코드로 보고 치환을 건너뛴다 — 문장을 끝내는 마침표(`commit.`)는 그대로 번역한다.

## [0.81.0] - 2026-06-11

### Added

- **`gk batch` — gk 명령 시퀀스를 JSON 계획 하나로 실행.** stdin(`--plan -`)이나 파일로 `{"steps":[{"args":["pull","--with-base"]},{"args":["push"]}]}` 계획을 받아 하위 명령을 자식 gk 프로세스로 순차 실행한다 — 에이전트의 N턴 워크플로우가 1콜이 된다. 스텝별 `on_failure: abort(기본)|continue`를 고를 수 있고, 게이팅 실패 시 잔여 스텝을 skipped로 채워 `failed_step`/`resume`과 함께 보고한다(land와 같은 계약, agent 모드에서 stdout은 결과 문서만). 충돌로 일시정지한 자식(exit 3)은 continue 정책이라도 계획을 멈춘다 — 미해결 pause 위에 다음 스텝을 쌓지 않는다. 검증은 실행 전 일괄로: 미지 하위 명령, 중첩 batch, flag로 시작하는 args, 20스텝 초과를 거부한다. `--plan-template`이 초안을, `--dry-run`이 실행 없는 미리보기를 낸다.
- **`gk context --include=diff,log,precheck,remotes` — 오리엔테이션에 후속 probe 융합.** context 한 호출에 섹션을 골라 붙인다(`--include=all`로 전부): `diff`는 미커밋 변경의 digest(파일별 ±라인·심볼, **untracked 포함**, 첫 커밋 전 unborn HEAD에서는 empty tree 기준), `log`는 최근 5커밋(sha/제목/작성자/시각), `precheck`는 다음 pull의 충돌 예보, `remotes`는 등록된 리모트별 현재 브랜치 드리프트(마지막 fetch 기준)와 비대칭 push URL. 세션 시작의 context→diff→log→precheck 4~5콜이 1콜이 된다. 수집할 수 없는 섹션은 에러 대신 `notes`로 강등된다 — 오리엔테이션 호출은 실패하지 않는다.
- **`gk pull --from <remote>[/<branch>]` — upstream 체인 밖 보조 리모트에서 통합.** 미러나 조직 fork처럼 추적 체인이 영원히 fetch하지 않는 리모트의 작업을, 기존 pull 레일(autostash·전략 해석·diverged 가드·충돌 계약) 그대로 가져온다. 브랜치를 생략하면 현재 브랜치와 같은 이름을 쓰고, tracking 설정은 건드리지 않는다 — 일회성 통합 소스다. 미등록 리모트는 오타가 네트워크 probe가 되지 않도록 등록 목록과 함께 거부한다.
- **`gk doctor` — 비대칭 push-only 리모트 감지.** 리모트의 pushurl이 fetch URL과 다르면 WARN을 낸다: push-only URL에서 PR로 머지된 작업은 fetch로 절대 내려오지 않고, 드리프트가 생기면 push가 non-fast-forward로 반쯤 실패하는 구성 footgun이다. 처방으로 `git remote add <name> <url>` 후 `gk pull --from <name>`을 안내한다. agents 규약 v8이 위 표면들(`--include`, `batch`, `--from`, doctor 항목)을 반영한다.

### Changed

- **up-to-date 결과줄이 커밋 시각과 제목까지 보여준다.** `gk pull`/`gk sync`의 `already up to date at <sha>`가 `Already up to date  ffdbbce1  06/10 17:13  release: v0.80.0` 형태가 됐다 — tip 커밋의 시각(올해가 아니면 `YY/` 접두)과 제목 전체를 한 줄에 싣는다. SHA만 보고 "이게 언제 적 커밋이지?"를 git log로 되묻던 후속 호출이 사라진다. 추가 비용은 이미 가리키고 있는 커밋 객체를 한 번 더 읽는 것뿐이다.

## [0.80.0] - 2026-06-10

### Changed

- **agents 규약 v6 — 명령 표기를 `gk`에서 `git-kit`으로 통일.** 에이전트 셸에서는 `gk`가 별칭에 가려지는 일이 흔해(oh-my-zsh가 `gk`를 gitk로 매핑) 같은 바이너리의 별칭-안전 이름인 `git-kit`을 규약 전반에 명시했다. 규약 서두에 그 이유를 한 줄로 못 박아 에이전트가 `gk`로 되돌아가지 않게 한다. `gk agents install`로 재설치하면 갱신된다.
- **hint·remedy·계약 필드의 명령 표기가 호출된 이름을 따라간다.** `git-kit pull`로 호출하면 에러 envelope의 `remedies[].command`·`hint`, 충돌 계약의 `resume`/`abort`, `gk context`의 `next_actions`, 사람용 HINT 블록과 충돌 안내까지 전부 `git-kit continue` 형태로 나온다 — 에이전트가 받은 remedy를 그대로 복사 실행해도 별칭 함정을 밟지 않는다. `gk`로 호출하면 기존과 동일(공백 비용), `gk-dev` 같은 개발 바이너리도 자기 이름을 따른다. 적용 범위는 **명령 제안 표면**뿐이다: `--help`·guide 같은 문서 표면은 정식 이름 `gk`를 유지하고, log/digest가 인용하는 저장소 데이터(커밋 제목 등)는 절대 재작성하지 않는다.
- **`git-kit` 별칭이 모든 설치·업데이트 경로에서 보장된다.** 지금까지 별칭-안전 이름 `git-kit`은 `install.sh`(curl)와 Homebrew cask만 깔아줬다. 이제 `gk update`의 manual 경로도 바이너리를 제자리 교체한 뒤 옆에 `git-kit` 별칭을 (재)링크한다 — 별칭 도입 이전에 깔린 install.sh나 cp 폴백으로 만들어진 사본도 신선한 symlink로 갱신된다(쓰기 불가 디렉터리는 `sudo ln -sf`로 승격, 실패해도 업그레이드 자체는 성공으로 둠). `make install`도 기본 dev 빌드에서 `gk-dev`와 함께 `git-kit-dev`를 깐다 — 별칭 이름은 바이너리 이름에서 도출되어(`gk`→`git-kit`, `gk-dev`→`git-kit-dev`) 개발 빌드가 Homebrew 소유의 바른 `git-kit` 이름을 가로채지 않는다. `make uninstall`은 짝이 되는 별칭만 지운다.

### Fixed

- **`gk commit -f`의 미리보기가 커밋 헤더의 type 접두사를 두 번 보여주던 문제.** AI가 가끔 `Subject`에 `feat(internal): …`처럼 Conventional-Commits 접두사를 통째로 넣어 반환하는데, 최종 커밋 메시지를 만드는 경로는 중복 접두사를 떼어내(`stripConventionalPrefix`) 정상이었지만 plan 미리보기·대화형 picker·완료 목록은 떼지 않은 채 `type: `을 다시 앞에 붙여 `feat: feat(internal): …`로 출력했다 — 미리보기와 실제 커밋이 어긋났다. 헤더 조립을 `Message.Header()` 한 곳으로 모아 세 표시 표면과 저장 경로가 모두 같은 정규화를 거치게 했다. 저장되는 커밋 메시지 자체는 이전에도 정상이었으므로 영향 없음 — 표시만 고쳤다.

## [0.79.0] - 2026-06-10

### Added

- **`gk forget` 내장 히스토리 재작성 엔진 — 이제 filter-repo 설치 없이 동작(기본값).** `git fast-export --no-data` 스트림을 gk가 직접 파싱·필터링해 `git fast-import`로 재구축한다 — 경로 제거 슬라이스 전용. 엔진 선택은 `--engine native|filter-repo` 또는 `.gk.yaml`의 `forget.engine`(플래그 우선, 기본 native; `gk config set forget.engine filter-repo`로 상시 전환). 빈 커밋 prune은 "필터된 delta가 비면 트리가 first parent와 같다"는 불변식 위에서만 일어나고(대체 커밋은 항상 first parent — `merge -s ours` 토폴로지에서 내용이 바뀌는 함정 차단), 전부 .xm류만 만지던 사이드 브랜치의 merge는 filter-repo와 동일한 규칙으로 붕괴·prune된다(내용 있는 merge는 redundant/중복 parent까지 그대로 유지). 검증은 differential testing으로: 12개 토폴로지(선형/루트 prune/merge 붕괴/evil merge/`-s ours`/동일 parent/태그 재지정/유니코드 경로/다중 브랜치)에서 filter-repo와 **브랜치·태그 SHA가 바이트 단위로 동일**함을 테스트로 고정했다. shallow clone과 refs/replace는 거부하고 명확한 에러로 위임 엔진을 안내한다. v1 미지원: 커밋 메시지 안의 SHA 참조 재작성.

- **`gk rebase --plan` — 에디터 없는 선언적 히스토리 편집.** `git rebase -i`의 에디터 세션을 JSON 계약으로 대체한다: `--plan-template`이 현재 범위(`--onto`, 기본 `@{u}`→원격 base)를 커밋별 `{action, commit, subject, pushed}` 초안으로 내보내고, 호출자가 pick/squash/fixup/reword/drop을 정한 plan을 `--plan -`(stdin)이나 파일로 되먹이면 gk가 검증 후 git 자체 rebase 머신을 미리 만든 todo로 구동한다(`GIT_SEQUENCE_EDITOR` 자기 지정, reword는 `commit --amend -F`로 quoting-safe, squash도 에디터를 열지 않음). 검증이 거부하는 것: 범위 내 커밋 누락(암묵적 pick 없음 — drop은 명시), 모호하거나 범위 밖 SHA, merge 커밋, 선두 squash/fixup, 메시지 없는 reword, 그리고 원격에 이미 있는 커밋의 재작성(첫 변경 지점 이후 전체에 적용, `--allow-pushed`로만 해제). 실행 전 `refs/gk/backup/<branch>/<ts>` 백업 ref를 쓰고, 충돌은 표준 일시정지 계약(exit 3, `gk continue`/`gk abort`)을 따른다. `--dry-run`은 todo 미리보기만, `--json`은 `{result, onto, pre, post, backup_ref}` 계약. agents 규약 v5에 "History editing" 항목 추가 — agent가 구조적으로 쓸 수 없던 마지막 git 표면이 기계 계약이 됐다.

### Fixed

- **`gk forget`이 이전 filter-repo 실행 흔적에 막히던 문제.** 하루 이상 지난 `.git/filter-repo/already_ran` 마커가 남아 있으면 filter-repo가 "이전 실행의 연속이냐"는 대화형 Y/N 프롬프트를 띄우는데, gk는 터미널 없이(stdin=/dev/null) 실행하므로 EOFError로 죽었다. gk forget의 매 실행은 독립적인 새 rewrite이므로 실행 전 마커만 제거해 fresh-run 의미론을 복원한다(commit-map 등 나머지 메타데이터는 보존).
- **`gk forget`의 롤백 안내가 실제로는 불가능했던 문제.** filter-repo가 `--all`로 모든 ref를 재작성하면서 gk의 백업 ref(`refs/gk/forget-backup/*`)까지 새 히스토리로 옮기고, 사후 gc가 옛 객체를 지워버려 manifest의 SHA가 dangling이 됐다 — 출력되던 "rollback:" 명령이 거짓이었다. 이제 위임 엔진은 브랜치·태그를 열거한 `--refs`로 실행해 refs/gk/*와 refs/remotes/*를 건드리지 않고 gc도 생략한다(결과 SHA는 전체 재작성과 동일함을 검증). 옛 객체는 백업 ref로 도달 가능하게 남으며, 디스크 회수는 확신이 선 뒤 `git gc --prune=now`로 직접 한다.

## [0.78.0] - 2026-06-10

### Added

- **`gk diff --digest` — 패치 없이 "무엇이 어디서 바뀌었나".** 파일별 변경 종류·±라인·hunk 수·**변경된 심볼 목록**(git hunk header의 함수 컨텍스트 — `.gitattributes` 불필요)을 파일당 한 줄로 요약하고, test/docs/ci/build 파일은 종류 태그를 단다. `--json`/agent 모드에서는 `{files:[{path, status, hunks, added, deleted, symbols, kind}], stat}` 계약 — agent가 status→diff→파일 Read를 반복하던 최빈 다턴 패턴이 1턴이 된다. 일반 `gk diff`와 같은 ref/경로 인자를 그대로 받는다(`--staged`, `HEAD~3`, `main..feature`).

- **`gk precheck` 충돌 예보 확장 (별칭 `forecast`).** 대상을 생략하면 다음 pull(추적 upstream, 없으면 원격 기준 브랜치)을 기준으로 read-only merge-tree 시뮬레이션을 돌린다 — "지금 당기면 충돌나나?"가 한 호출이 되어 시도→중단→재시도 루프가 사라진다. agent 모드/전역 `--json`에서 envelope 계약(`{ours, target, base, clean, conflicts[]}`)을 따른다. rebase는 merge 시뮬의 근사임을 문서에 명시.

- **`GK_AGENT=1` — agent 모드 전면 envelope.** env 하나로 모든 gk 호출이 통일 계약을 낸다: 성공은 `{schema, ok, result}`, 실패는 `{ok:false, error:{code, message, hint, remedies:[{command,safety}]}}`(stderr, exit code 유지). `error.code`는 추가-only 어휘(not-a-repo/dirty-tree/conflict/diverged/in-progress-op …)이고, 기존 `try:` 힌트는 remedies로 자동 승격된다. GK_AGENT 없이는 `--json` 출력이 이전과 바이트 단위로 동일하다. 충돌 일시정지(exit 3)는 에러가 아니라 resume 계약을 담은 결과다.
- **`gk land` — 세션 마감 한 방.** 변경이 있으면 `commit -f`(AI 그룹 커밋) → `pull --with-base` → `push`를 한 트랜잭션으로, 단계별 ✓와 함께 실행한다. 첫 실패에서 멈추고 `failed_step`과 정확한 복귀 명령을 알려주며, 고치고 다시 `gk land`를 치면 끝난 단계는 no-op으로 통과한다. `--cleanup`은 push 후 완전 머지된 브랜치와 그 worktree까지 회수(merged-only, protected 제외).
- **`gk doctor --fix` 확장.** 죽은 프로세스가 남긴 `index.lock`을 mtime 기반으로 stale/fresh 구분해 fresh면 절대 삭제를 제안하지 않고, 충돌 파일이 없는 고아 merge(MERGE_HEAD)는 "abort해도 잃을 것 없음"을 명시하며, 디렉터리가 사라진 worktree 등록을 감지해 prune을 제안한다 — 병렬 agent 환경에서 가장 흔한 stuck 3종.

- **`gk context` — 저장소 오리엔테이션 한 방.** 현재 브랜치·upstream·ahead/behind·변경 개수(staged/unstaged/untracked/conflicts)·진행 중인 rebase/merge(재개·중단 명령 포함)·기준 브랜치 드리프트·worktree·다음 행동 제안을 한 호출로 반환한다. `--json`이면 스키마 버전이 붙은 기계용 문서(필드는 추가-only) — AI 에이전트가 status/branch/log/worktree를 차례로 찔러보던 3~6턴이 1턴이 된다. 별칭 `gk ctx`.
- **`gk agents` — 에이전트 지침 파일에 gk 사용 규약 설치.** `print`(규약 블록 출력)/`install`(repo 루트 `CLAUDE.md`·`AGENTS.md`에 멱등 삽입·갱신)/`check`(버전 일치 점검, 어긋나면 비정상 종료+힌트). 규약 본문이 gk 바이너리에 내장돼 설치된 gk의 실제 표면과 항상 일치하고, 버전 마커 블록 바깥은 절대 수정하지 않는다. Claude·Codex 어느 쪽이든 같은 블록을 읽는다.
- **`gk pull --json` — 결과의 기계 계약.** `result`(updated/up-to-date/ahead-only/fetch-only/conflict)와 움직인 SHA, `--with-base` base 동기화 결과, 충돌 시 파일 목록+재개/중단 명령을 stdout JSON으로 낸다. 사람용 진행 출력은 stderr에 그대로 유지된다.

## [0.77.0] - 2026-06-10

### Added

- **`gk pull --with-base` — base 브랜치까지 한 번에 동기화.** develop에서 pull하면서 로컬 main 같은 base 브랜치를 checkout 없이 원격 끝으로 fast-forward한다(`pull.with_base: true`로 상시 활성, `--with-base=false`로 일회성 해제). 엄격히 FF만 수행: base가 분기됐거나, 다른 worktree에 체크아웃돼 있거나, 로컬에 없으면 자동 처리 대신 NOTE로 건너뛴다 — 로컬 커밋 유실이 구조적으로 불가능하다. 여러 머신을 오가는 아침 동기화가 `gk pull` 한 번으로 끝난다.
- **`gk ship` 릴리스 파이프라인 확장 — 검증부터 사후 확인까지 한 명령.** `ship.watch`(태그 push 후 차단형 CI 추적, 예: `gh run watch`)와 `ship.verify`(태그·tap·CDN 등 산출물 점검) 훅을 config로 등록하면 "Ship complete"가 전체 파이프라인 통과를 의미하게 된다. `ship.version_files`로 버전 파일 목록을 직접 지정할 수 있고(자동 감지 대체), `--dry-run --json`은 계획 전체(버전 추론 근거·changelog 초안·단계 목록)를 도구가 읽을 JSON으로 내보낸다. CHANGELOG의 `[Unreleased]`가 비어 있으면 conventional commit에서 섹션 초안을 만들어 확인 화면에 먼저 보여주고, 0.x에서는 breaking 추론이 관례대로 minor에 머문다(`--major`로만 v1 승급).

### Changed

- **gk 발신 안내를 `█ NOTE` / `█ HINT` 블록으로 통일.** pull/push처럼 git 원문과 gk 안내가 한 스트림에 섞이는 곳에서 발신자가 모호하던 `note:`/`hint:` 한 줄짜리를 섹션 블록으로 바꿨다. Easy Mode에서는 블록 아래에 쉬운 설명 한 줄이 붙는다(예: upstream 미연결 시 "위 명령을 한 번 실행해두면 다음부터 이 안내가 사라져요").
- **pull 결과줄에 브랜치 컬럼.** `--with-base`로 두 브랜치 상태가 한 출력에 나오므로 모든 결과줄이 `main`/`develop` 정렬 컬럼을 달고 나온다 — "already up to date"가 어느 브랜치 얘기인지 더 이상 헷갈리지 않는다.

### Fixed

- **tracking 설정은 있는데 캐시 ref만 없는 브랜치의 오진.** `branch.<name>.remote/merge`가 멀쩡해도 `refs/remotes/origin/<branch>`가 없으면(prune, 미fetch clone) pull이 "no upstream"으로 오진하고 base로 fallback했다. 이제 설정을 직접 읽어 구분하고, 설정된 upstream을 그대로 사용해 fetch가 캐시 ref를 복구한다. `gk log --behind/--ahead`도 같은 상태를 정확히 안내한다.
- **`--easy`/`--no-easy` 플래그가 무시되던 버그.** help 설치 경로가 cobra 플래그 파싱 전에 Easy Mode 엔진을 초기화해 플래그가 전부 zero인 상태로 캐시됐다 — config가 켜둔 Easy Mode를 `--no-easy`로 끌 방법이 없었다. 템플릿 설치를 렌더 시점으로 늦춰 해결.
- **`gk log --graph`에서 사라졌던 push boundary(`↑ N unpushed`)와 태그 룰 복원.** git `--graph` 출력을 빌리던 시절의 억제 결정이 self-drawn renderer 전환 후에도 남아 있었다. 같은 결로 `--graph=false`가 config `log.graph: true`를 못 이기던 것도 수정 — 이제 일회성으로 flat 뷰로 돌아갈 수 있다.
- **깨진 config에서 panic.** `pull:` 섹션이 중복되는 등 yaml 파싱이 실패하면 일부 명령이 nil 참조로 죽고 나머지는 조용히 기본값으로 돌았다. 이제 깨진 레이어만 건너뛰고 계속 동작하며, 어떤 파일의 몇 번째 줄이 왜 무시됐는지 프로세스당 한 번 경고한다(`gk config doctor` 안내 포함). repo 로컬 `.gk.yaml`의 파싱 실패도 더 이상 무음이 아니다.

## [0.76.0] - 2026-06-09

### Added

- **`gk snapshot` 추가 — 작업을 잃지 않는 비파괴 안전망.** 현재 작업트리(추적 변경 + untracked 새 파일, `.gitignore` 존중)를 `refs/wip/<branch>`에 스냅샷한다. `gk wip`과 달리 HEAD에 커밋하지 않아 작업트리·인덱스·브랜치 히스토리를 전혀 건드리지 않는다. shadow ref라 `git branch`에 안 보이고, push되지 않으며, `git gc`에도 살아남는다. 그 ref의 reflog가 곧 스냅샷 이력이다. `gk snapshots`(= `gk snapshot list`)로 시간순 목록을, `gk snapshot restore [n]`으로 복원한다(작업트리가 dirty하면 현재 상태를 먼저 스냅샷으로 백업한 뒤 복원하므로 아무것도 잃지 않는다). `gk snapshot -q`는 Stop hook 등 자동 트리거에서 쓰기 좋다.

## [0.75.0] - 2026-06-08

### Added

- **`gk config setup`이 커스텀 provider(kiro-api 등)를 지원.** 빌트인이 아닌 provider 이름을 고르면 endpoint·모델·API 키를 차례로 묻고 `ai.providers.<name>.*`에 기록한다. wire format은 `openai`로 자동 설정되며, endpoint는 기본값을 강요하지 않는다. `kiro-api`는 provider 모델과 commit 모델 모두 `kiro/claude-haiku-4.5`를 기본값으로 채운 편집 가능한 프롬프트로 보여주고, API 키는 요약 출력에서 마스킹한다. `--endpoint`/`--provider-model`/`--api-key`로 비대화 설정도 가능하다.

## [0.74.0] - 2026-06-08

### Added

- **`gk config`에 `set`·`unset`·`edit`·`path`·`setup`·`doctor` 추가 — 손편집 없이 설정 관리.** dot-key를 `yaml.Node` 기반으로 in-place 수정해 주석과 순서를 보존하며, 스키마에 없는 키는 거부한다. `setup`은 provider·모델·언어를 대화형으로 묻는 마법사이고, `doctor`는 알 수 없는 키와 누락된 provider API 키를 점검한다. 전역(`~/.config/gk/config.yaml`)과 저장소별(`.gk.yaml`)을 `--local`로 구분해 쓴다.
- **`ai.commit.lang` 추가 — commit 메시지 언어를 따로 지정.** 채팅/조언 명령(`do`/`ask`/`explain`)은 `ai.lang`을 따르되 commit만 다른 언어로 둘 수 있다. 우선순위는 `gk commit --lang` > `ai.commit.lang` > `ai.lang` > `output.lang`.
- **`gk ship`이 non-base 브랜치에서 base를 fast-forward 머지한 뒤 릴리스.** `develop` 등에서 ship하면 base(`main`)가 fast-forward 가능할 때 base를 그 브랜치 끝으로 전진시켜 base에서 태그한다. 히스토리가 분기되면 중단하고 통합을 안내하며, `--allow-non-base`로 기존처럼 현재 브랜치에 태그할 수도 있다.

### Fixed

- **git 저장소 밖에서 실행 시 오류 메시지 개선.** raw `fatal: not a git repository` 대신 "git 저장소가 아닙니다"라는 표준 안내와 힌트를 모든 명령에 일관 적용한다.

## [0.73.0] - 2026-06-07

### Added

- **`gk push`와 `gk ship`에 `-n`/`--no-verify` 추가 — 시크릿 스캔 우회.** `gk commit -n`과 일관되게, push될 커밋
  (`<remote>/<branch>..HEAD`)의 시크릿 패턴 스캔을 `-n` 한 글자로 건너뛴다. `gk push`에선 기존 `--skip-scan`과 동일
  동작이고(둘 다 유지), `gk ship`은 그동안 push 직전 시크릿 스캔을 우회할 방법이 전혀 없었는데 이제 `-n`으로 가능하다.
  참고로 `git push -n`은 `--dry-run`이지만 `gk push`엔 dry-run이 없어 충돌하지 않는다. AI 명령(`ask`·`explain`·`pr`
  등)의 privacy gate는 성격이 다른 가드라 기존의 `--skip-privacy`를 그대로 쓴다.

## [0.72.0] - 2026-06-07

### Changed

- **`gk commit --no-verify`(`-n`)가 privacy gate 임계값까지 함께 우회.** v0.71.0에선 `-n`이 노이즈·secret 가드만 꺼서,
  payload에 secret이 임계값(`ai.commit.privacy.max_secrets`, 기본 10)을 넘으면 privacy gate에서 다시 abort돼 "가드 일괄
  우회"라는 의도가 깨졌다. 이제 `-n`이 `--skip-privacy`를 포함해 privacy gate의 abort 임계값까지 끄므로 `gk commit -n`
  한 번으로 커밋 가드를 일괄 우회한다. 원격 provider로 나가는 payload의 redaction은 그대로 적용되어 LLM은 여전히 원본
  secret을 보지 못한다.

## [0.71.0] - 2026-06-07

### Added

- **`gk commit`에 커밋 가드 일괄 우회 — `-n`/`--no-verify`와 `-S all`(`--allow-secret-kind all`).** 지금까진 secret을
  종류별로 `--allow-secret-kind <kind>` 지정해야만 통과시킬 수 있었는데, 진짜 false positive가 여럿 모일 때를 위해
  전체 우회 경로를 더했다. `--no-verify`는 노이즈·secret 가드를, `--allow-secret-kind all`은 secret 가드를 통째로
  끈다. 두 경로 모두 우회된 secret을 stderr에 그대로 보고한 뒤 커밋에 포함하므로(실제 자격증명이면 즉시 폐기·재발급
  대상), 가드 우회를 흔히 누르는 `-f`에 묶지 않고 의도가 분명한 별도 플래그로 분리했다. 개별 `--allow-secret-kind
  <kind>`는 기존대로 조용히 무시한다. 원격 AI로 나가는 payload를 지키는 privacy gate는 우회 대상이 아니다.

## [0.70.0] - 2026-06-07

### Added

- **`gk`를 `git-kit`·`git kit`으로도 호출.** 모든 설치 방식이 같은 바이너리를 두 이름으로
  노출한다(Homebrew cask·`install.sh`는 자동, `go install`·수동 tar는 안내된 symlink 한 줄).
  oh-my-zsh의 `git` 플러그인이 `gk`를 `gitk`로 가리는 흔한 충돌을 alias 제거 없이 우회할 수
  있고, PATH의 `git-kit` 덕분에 `git kit …`이 git 네이티브 서브커맨드로 동작한다. help·usage·
  `--version`은 호출한 이름을 그대로 따른다(`git-kit push --help` → `git-kit push …`). 단,
  git이 인자 없는 `git kit --help`를 man 조회로 바꾸는 건 모든 커스텀 서브커맨드 공통이라,
  최상위 도움말은 `git kit help` 또는 `git-kit --help`를 쓴다.

## [0.69.0] - 2026-06-07

### Added

- **`gk local` 명령 신설 (별칭 `gk lo`).** "내 컴퓨터에만 있는 것"을 한 화면에 모은다 —
  작업 트리 변경(미스테이지·스테이지·충돌), 원격 어디에도 없는 미푸시 커밋, 스태시. 미푸시
  판정은 `@{upstream}` 우선, upstream이 없으면 모든 원격 추적 ref(`--remotes`)로 폴백하므로
  한 번도 push하지 않은 브랜치도 로컬 전용 커밋을 보여준다. 원격이 전혀 없으면
  `no remote to compare against`로 표시한다. `-n N`(섹션별 표시 개수), `--json` 지원.

### Changed

- **`gk status` 기본 출력에 로컬 변경 계층을 노출.** 기본 시각화 세트에 `local`(작업 트리
  변경 뱃지 `· 5 unstaged · 1 staged · 2 conflicts`)과 `since-push`(미푸시 나이·개수
  `· unpushed 2h (3c)`)를 추가했다. `↑A ↓B`와 합쳐 BRANCH 한 줄에서 로컬 변경 세 계층이
  각각 한 번씩 보인다. 둘 다 보일 게 없으면 침묵한다.
- **`gk log --safety`와 `gk status`의 미푸시 판정이 upstream 없이도 동작.** push 상태를
  `@{upstream}` 우선, 없으면 모든 원격 추적 ref(`--remotes`) 기준으로 판정한다. `gk log --safety`는
  이제 첫 pushed 커밋 위에 `──┤ ↑ N unpushed ├──` 경계선을 그려 로컬 전용 블록을 한눈에
  보여주고, `gk status`의 `since-push`는 upstream 없는 브랜치에서 `· unpushed Xh (Nc)`로
  표시한다. 원격이 전혀 없을 때만 판정 불가로 침묵한다.

## [0.68.0] - 2026-06-06

### Added

- **`gk ignore <path>` 명령 신설.** "이 파일을 git에 포함하지 않기"를 AI 없이 결정론으로
  처리한다. 지정한 경로를 `.gitignore`에 기록(디렉터리는 끝에 `/`)하고, 이미 추적 중이면
  `git rm --cached`로 추적만 해제해 작업 트리의 파일은 그대로 남긴다. `--commit`으로 한
  커밋에 마무리하고 `--dry-run`으로 미리본다. 이미 히스토리에 들어간 경로는 `gk forget`이
  담당한다.
- **`gk do` 결정론 패스트패스.** 흔하고 의도가 분명한 요청(파일 추적 제외, 히스토리에서
  제거, 마지막 커밋 취소)은 LLM 왕복 없이 정답 플랜으로 즉시 직행하고, 어느 recognizer도
  못 맞추면 기존 AI 경로로 폴백한다. 한국어 띄어쓰기 변형과 조사가 붙은 경로(`config.json를`)도
  인식한다. "...하고 push" 같은 복합 요청은 부분 플랜을 막기 위해 LLM에 위임한다.
- **`gk pull --ai`.** 통합 중 충돌이 나면 `gk resolve --ai`와 `gk continue`를 끝까지 구동해
  rebase/merge를 자동으로 마친다. `--fetch-only`와는 함께 쓸 수 없다(해결할 충돌이 없음).

### Changed

- **대화형 AI 출력이 `output.lang`을 따른다.** `gk do`/`ask`/`explain`(및 `status --ai`)의
  설명문은 이제 `output.lang`으로 출력되고, `ai.lang`은 git 아티팩트(커밋 메시지·pr·changelog)에만
  적용된다. 두 설정의 목적이 다르므로, `ai.lang: en` + `output.lang: ko`면 커밋은 영어로
  쓰면서 CLI 대화는 한국어로 받을 수 있다.
- **`gk do`가 진행 상황과 플랜 헤더를 보여준다.** provider 응답을 기다리는 동안 spinner가
  돌고(`--debug`에서는 디버그 로그와 겹치지 않도록 억제), 플랜 위에 provider·언어·dry-run
  미리보기 배지를 stderr에 표시한다(stdout은 파이프·`--json`용으로 깨끗하게 유지).
- **`gk forget` 후속 안내**가 `gk push --force`(gk-native, `--force-with-lease` + 시크릿
  스캔)를 우선 제시하고, 모든 커밋 SHA가 바뀐 공유 브랜치는 협업자가 re-clone 또는
  `git reset --hard` 해야 함을 경고한다.
- **`gk pull`**이 base 브랜치를 정하지 못할 때(upstream 미설정 / rewrite 후 diverged) 막연한
  "use --base" 대신 tracking 설정·`--base`·`gk push --force` 등 상황에 맞는 해결법을 안내한다.

## [0.67.0] - 2026-06-05

### Fixed

- **`gk worktree remove`가 locked worktree를 제거하지 못하던 문제 수정.** `--force`는
  git에 `--force`를 한 번만 넘겨 dirty/untracked만 강제하고 lock은 그대로 둔다(git은
  `-f -f`를 요구). 그래서 잠긴 worktree를 gk의 어떤 경로로도 못 지웠고, TUI의 "locked
  → --force" 안내도 실제로는 동작하지 않았다. 이제 lock 이유에 담긴 pid의 생존을
  확인해, 이미 죽은(stale) 잠금은 `--force`로 unlock 후 제거하고, 아직 살아있는 잠금은
  거부하며 새 `--force-locked` 플래그로만 강제한다 — 실행 중인 worktree(예: 동작 중인
  claude agent)를 실수로 지우는 것을 막는다.

## [0.66.0] - 2026-06-05

### Added

- **`gk worktree init`이 중첩 모노레포 매니페스트를 감지한다.** 루트에 매니페스트가
  없는 모노레포(예 `mesh-explorer-web/{frontend,backend}`)에서 설치 명령을 0개
  제안하던 문제를 고쳤다. 루트에 매니페스트가 있으면 그대로 쓰고(workspace 가정),
  없으면 서브디렉토리를 제한 깊이로 스캔(`node_modules`·`.venv`·빌드 디렉토리 제외)해
  각 프로젝트의 설치 명령을 `cd <dir> && …`로 감싸 제안한다. 생태계 억제(uv.lock이
  requirements.txt를 누르는 식)는 디렉토리별로 적용된다.
- **protected 브랜치를 linked worktree로 가져갈 때 확인을 묻는다.** linked worktree
  안에서 `gk sw main`을 하면 main이 그 worktree에 갇혀 다른 worktree에서 쓸 수 없게
  되는데(거의 항상 실수), 이제 확인 프롬프트로 막는다. 비대화형 스트림에서는
  `--detach`(보기만)/`--force`(강제) 힌트와 함께 거부한다. 메인 worktree·`-c`·
  `--detach`·`--force`는 그대로 허용된다.

### Changed

- **`gk sw`가 worktree 점유를 별도 `WORKTREE` 칸으로 분리한다.** 다른 worktree에
  체크아웃된 브랜치가 `wt: ↑ origin/main`처럼 UPSTREAM 칸에 합쳐져 "upstream이
  worktree"인 것처럼 읽히던 표시를, 점유 worktree의 basename을 담는 전용 칸으로
  옮겼다. 덕분에 UPSTREAM은 순수 출처 기술자만 남는다. 점유된 브랜치가 하나도 없으면
  칸이 나타나지 않아 일반 repo는 기존 4칸 레이아웃을 유지한다.

## [0.65.0] - 2026-06-05

### Added

- **`gk worktree init`으로 worktree 환경을 부트스트랩한다.** 새 worktree에는
  tracked 파일만 들어와 `.env`·`node_modules`·`.venv` 같은 gitignore된
  per-checkout 상태가 빈 채로 남는데, 이를 `.gk.yaml`의 `worktree.init`에 선언한
  세 가지로 복원한다 — `link`(main worktree에서 `.env` 등 시크릿을 symlink해 한
  곳에서 관리), `copy`(worktree마다 따로 편집할 파일을 복사), `run`(`npm ci`·`uv
  sync` 등 이 체크아웃의 lockfile 기준으로 의존성을 재설치하는 명령). virtualenv는
  `pyvenv.cfg`·shebang에 절대경로가 박혀 있고 `node_modules`는 브랜치마다 lockfile이
  달라 link/copy하면 깨지므로, 둘을 link/copy 대상에 넣으면 경고하고 `run` 사용을
  권한다. 적용은 멱등이라 재실행 시 누락분만 고치므로 실패한 셋업 단계의 재시도로도
  쓰인다.
- **`gk worktree add --init` / `--no-init`.** worktree를 만든 직후 같은
  부트스트랩을 수행한다. 대화형 환경에서는 적용 여부를 묻고, `--init`/`--no-init`로
  프롬프트를 생략한다. 비대화형에서 플래그 없이 실행하면 아무것도 실행하지 않고
  `gk wt init`을 안내한다.
- **`worktree.init` 미설정 시 패키지 매니페스트 자동 감지.**
  `package-lock.json`·`pnpm-lock.yaml`·`yarn.lock`·`uv.lock`·`requirements.txt`·`go.mod`
  등을 생태계 우선순위(`uv.lock`이 `requirements.txt`를 억제하는 식)로 인식해
  설치 명령과 `.env` link 후보를 제안하고, `--save`로 `.gk.yaml`에 기록한다.
  `--dry-run`은 link/copy/run을 실행 없이 미리 보여준다.

## [0.64.0] - 2026-06-03

### Added

- **`gk log --behind` / `--ahead` upstream 미리보기.** 안 가져온 incoming 커밋은
  `gk log --behind` (=`HEAD..@{u}`), 안 푸시한 outgoing 커밋은 `gk log --ahead`
  (=`@{u}..HEAD`)로 한 명령으로 본다 — 기존 `--graph --cc --impact` 등 시각화와
  그대로 조합된다. `--fetch`를 같이 주면 미리 `git fetch <remote> <branch>`를
  돌려 카운트를 최신 origin 기준으로 맞춘다(기본은 페치 없이 빠른 경로). 두
  플래그는 mutually exclusive이고 upstream이 설정 안 된 브랜치에서는 `git
  branch --set-upstream-to=origin/<branch>` 힌트로 에러를 낸다.
- **`gk status`의 NEXT 힌트가 behind-only 상태에서 `gk log --behind`를 함께
  안내한다.** ↓N 상태에서는 통합 전에 무엇이 들어올지 미리 보는 게 자연스러운
  단계라, `gk log --behind   ·   gk pull` 두 선택지를 같이 보여준다 — 발견
  가능성을 높이면서 한 줄로 유지한다.

## [0.63.0] - 2026-06-02

### Added

- **`gk push`·`gk ship` 비밀정보 스캔 결과에 파일 경로 표시.** 탐지 결과가
  의미 없는 blob 줄 번호(`line 3170`) 대신 `[generic-secret]
  internal/config/app.go:42`처럼 파일 경로와 줄 위치를 함께 보여줘 어떤 파일에서
  걸렸는지 바로 찾을 수 있다. 줄 번호는 `git log -p` diff 블록 기준 상대값이라
  실제 소스 줄과 다를 수 있으나 파일 경로는 정확하다.

### Changed

- **비밀정보 스캔 마스킹을 키워드가 아닌 값 기준으로 변경.** `secret = "..."`
  같이 캡처 그룹이 있는 패턴에서 키워드 앞부분(`SECR****`) 대신 실제 값의
  앞부분(`dev-****`)을 마스킹해 보여줘, 걸린 값이 진짜 비밀인지 오탐인지 한눈에
  판단할 수 있다.

### Fixed

- **하이픈/언더스코어로 표기한 개발용 기본값 오탐 제거.** placeholder 필터가
  구분자를 정규화해 `change-me`·`dev_secret`·`insecure` 같은 분리 표기를
  인식하므로, `_FALLBACK_SECRET = "dev-insecure-secret-change-me"` 류의 개발용
  기본값이 더 이상 `gk push`를 막지 않는다.

## [0.62.1] - 2026-06-02

### Fixed

- **detached HEAD에서 `gk status`의 다음 작업 힌트 수정.** 브랜치에서 분리된
  상태(detached HEAD)일 때 NEXT 칸이 의미 없는
  `git branch --set-upstream-to=origin/<branch>`를 제안하던 문제를 고쳐,
  `gk switch <branch>` / `gk branch <new>`로 브랜치에 다시 붙도록 안내한다.

## [0.62.0] - 2026-05-30

### Added

- **Easy Mode 쉬운 한국어 도움말 전면 적용.** `output.lang`이 `ko`이고 Easy
  Mode일 때 `gk <명령> --help`가 명령 한 줄 설명·`Long` 본문·플래그 설명은 물론
  구조 라벨(사용법/옵션/다른 이름/사용할 수 있는 명령/공통 옵션)까지 비개발자가
  읽을 수 있는 쉬운 한국어로 바뀐다. 전 명령을 덮으며, 브랜치명 같은 고유 명사는
  그대로 두고 "~해요" 류의 친절체 없이 명사형 위주의 간결한 톤을 쓴다.
- **Easy Mode AI 응답·로컬 상태 안내의 쉬운 한국어화.** `gk ask`/`do`/`explain`
  등 AI 응답과 `gk status`의 로컬 상태 조언이 `upstream`·`staged` 같은 git 전문
  용어를 "원격(서버)"·"커밋 준비됨"처럼 풀어 쓴 평이한 한국어로 출력된다. 고유
  명사는 보존한다.
- **commit AI 분류에서 빌드·의존성·캐시 노이즈 제외 + `.gitignore` 제안.**
  `.gitignore`가 없어 `node_modules`·`__pycache__`·`.venv`·`*.db` 등 비소스
  파일이 변경 집합에 대량 유입되면 AI 분류 대상(scope)에서 제외한다. TTY에서는
  감지한 패턴을 `.gitignore`에 추가할지 제안해, 응답이 잘리던 근본 원인을 정리할
  수 있게 한다. `build`/`dist`/`target`/`vendor`처럼 진짜 소스일 수 있는 경로는
  건드리지 않는다.
- **`gk commit --include-noise`.** 위 노이즈 가드를 건너뛰고 빌드 산출물·의존성·
  캐시 파일까지 그대로 커밋에 포함한다. 가드 없이 작업하고 싶을 때 우회용.

### Fixed

- **`gk ask`가 diff 리뷰가 아닌 Q&A로 답하도록 수정.** 질문 의도와 무관하게 변경
  사항을 코드 리뷰하듯 답하던 문제를 고쳐, 질문에 대한 답을 직접 돌려준다.
- **`ai.lang` 미설정 시 `output.lang`을 따르도록 수정.** 기본값 `en`이 강제돼
  한국어로 설정해도 AI가 영어로 답하던 문제. 이제 `ai.lang`이 비어 있으면
  `output.lang`을 따르고, scaffold가 만드는 기본 설정에서도 `ai.lang`을 강제하지
  않는다.
- **classify 응답 잘림 시 명확한 안내.** 변경 파일이 너무 많아 AI의 JSON 응답이
  중간에 끊기면 `invalid character` 같은 파서 원문 오류 대신, 응답이 잘렸음을
  알리고 커밋을 나누거나 `ai.commit.max_tokens`를 올리라고 안내한다.

## [0.61.0] - 2026-05-29

### Added

- **`gk status --ai`의 AI 호출 디버그 로그.** `-d`로 실행하면 캐시 hit("no AI
  call", 캐시 키와 비우는 명령 포함)·miss를 구분해 찍고, 실제 질의 직전
  `querying provider=X model=Y`를 출력한다. 어떤 모델로 요청했는지, 혹은 캐시라서
  호출이 없었는지 즉시 확인할 수 있다. commit 경로의 기존 `provider=… model=…`
  로그와 짝을 이뤄, 명령마다 실제 사용 모델(`ai.commit.model` vs
  `ai.<provider>.model`)을 추적할 수 있다.

### Fixed

- **`--repo` 플래그가 repo-local `.gk.yaml`을 찾도록 수정.** config 탐색이 현재
  작업 디렉터리의 `git rev-parse`로 repo 루트를 구해 `--repo`를 무시했다. 그래서
  `gk --repo /other <cmd>`가 `/other`의 `.gk.yaml`이 아니라 cwd의 설정(또는 없음)을
  읽어, 그 repo의 로컬 오버라이드(`ai.commit.model` 등)가 조용히 누락됐다. 이제
  `--repo`가 가리키는 worktree 루트를 기준으로 `.gk.yaml`을 찾고, `--repo`가 없을
  때만 cwd 루트로 폴백한다.

## [0.60.0] - 2026-05-29

### Added

- **사용자 지정 AI provider 등록.** `ai.providers.<name>` 맵(또는 얕은
  `ai.<name>` 블록)으로 임의 이름의 OpenAI 호환 게이트웨이를 등록할 수 있다.
  `format` 필드가 wire 프로토콜(기본 `openai`)을 결정한다. 예: `provider:
  kiro-api` + `kiro-api: { format: openai, endpoint: ..., model: kiro/auto }`.
  내장 provider 이름에 묶이지 않고 사내 프록시나 멀티모델 게이트웨이를 그대로
  붙일 수 있다.

- **AI 명령별 모델 선택 — `--model` 플래그와 `ai.commit.model`.**
  `gk commit`/`do`/`ask`/`explain`/`changelog`에 일회성 `--model` 플래그가,
  설정에 `ai.commit.model`이 생겼다. 커밋 메시지 생성처럼 기계적인 작업엔
  저렴하고 빠른 모델을 쓰면서 채팅·조언 명령(`do`/`ask`/`explain`,
  `status --ai`)은 큰 `ai.<provider>.model`을 유지할 수 있다. 우선순위는
  `--model` > `ai.commit.model` > `ai.<provider>.model` > 어댑터 기본값이며,
  HTTP provider에만 적용된다(CLI provider는 자체 모델 선택을 따른다).

- **`gk branch clean --worktrees`.** worktree가 점유한 브랜치를 해당 worktree를
  먼저 제거한 뒤 삭제한다. 미커밋·미추적 변경이 있는(dirty) worktree는 git이
  강제 없는 제거를 거부하므로 건너뛰고 경고만 남겨, 작업이 유실되지 않는다.

### Changed

- **`gk branch clean`·`gk switch`가 worktree 점유 브랜치를 인지.** git이 삭제를
  거부하는(다른 worktree가 체크아웃 중인) 브랜치를 `[worktree]` 마커로 표시하고
  기본 선택에서 제외하며, `gk wt remove`로 정리하라는 안내를 띄운다. 이전에는
  후보에 올라가 삭제 시도 후 실패했다.

- **`gk switch` picker가 필터를 유지한다.** 삭제 등 액션으로 picker가 재진입할
  때마다 입력했던 필터가 풀려 전체 목록으로 돌아가던 동작을 고쳤다. 이제 필터로
  좁힌 상태가 유지되어 같은 부분집합을 이어서 정리할 수 있다.

- **브랜치 삭제 시 protected 브랜치 보호.** `gk switch`(`d`/`D`)와
  `gk branch clean`이 `main`/`master`/`develop` 등 protected 브랜치를 기본
  차단한다. 현재 체크아웃된 브랜치는 어떤 경우에도 삭제할 수 없고, protected
  브랜치는 `--force`(switch picker에서는 `D`)로만 지울 수 있다. `gk branch
  clean`에서는 `--force`를 줘도 protected가 자동 선택되지 않고 목록에서 직접
  체크해야 하므로, 배치(`--yes`) 실행이 `main`을 실수로 지우는 사고를 막는다.

## [0.59.1] - 2026-05-28

### Fixed

- **`gk resolve --ai`가 `ai:` 설정이 있는 환경에서 설정 로딩에 실패하던 회귀
  수정.** v0.58.0에서 추가된 `--ai` 플래그 이름이 top-level `ai` 설정 섹션과
  겹쳐, `config.Load`의 `BindPFlags`가 `ai` 섹션(맵)을 플래그의 bool 값으로
  덮어쓰면서 `'ai' expected a map or struct, got "bool"`로 죽었다. `.gk.yaml`
  이나 전역 설정에 `ai:` 블록이 있으면 `--ai` 없이 `gk resolve`만 실행해도
  재현됐다. 이제 이름이 설정 섹션(`ai`, `commit`, `push`, `pull`, `branch`,
  `output` 등)과 충돌하는 플래그는 pflag→viper 바인딩에서 제외한다 — 해당
  플래그는 config 오버라이드가 아니라 명령 로컬 스위치이기 때문이다.

## [0.59.0] - 2026-05-28

### Added

- **HTTP AI provider의 키를 설정 파일에서 지정 가능.** `ai.anthropic`,
  `ai.openai`, `ai.nvidia`, `ai.groq` 블록에 `api_key` 필드가 생겼다. 값이
  있으면 해당 provider의 환경변수(`OPENAI_API_KEY` 등)보다 우선하고, 비어
  있으면 기존처럼 환경변수로 폴백한다. 커스텀 OpenAI 호환 엔드포인트나 개인
  설정(`~/.config/gk/config.yaml`)에서 토큰을 직접 넣을 때 쓴다 — 평문 yaml에
  키가 남으므로 공유/커밋되는 설정에는 환경변수를 권장한다. `gk doctor --ai`는
  설정 키를 인증 소스로 인식하되 값은 출력하지 않는다.
- **`-d` 디버그 로그가 HTTP AI 호출을 계측한다.** nvidia/openai/groq/anthropic
  provider로 나가는 매 요청마다 `[debug +…] ai <provider> model=<model>
  (<소요시간>, <상태>)` 한 줄을 찍는다 — git subprocess 로그와 동일한 형식.
  어떤 provider·model이 얼마나 걸렸는지(타임아웃·에러 포함) `--ai` 호출의
  지연을 한눈에 볼 수 있다. 프롬프트·키·응답 본문은 출력하지 않는다.

## [0.58.0] - 2026-05-28

### Added

- **`gk resolve --ai` shortcut flag.** `--ai`는 `--strategy ai`의 설탕 문법으로,
  모든 충돌을 AI 프로바이더로 한 번에 해소한다. 모순되는 조합은 조용히 한쪽을
  고르지 않고 에러로 막는다 — `--ai --no-ai`와
  `--ai --strategy ours|theirs`는 모두 거부된다.

### Changed

- **`gk update`의 brew 경로가 cask 설치를 인식한다.** 실행 중인 바이너리가
  Caskroom 아래에 있으면 `brew upgrade x-mesh/tap/gk`에 `--cask`를 자동으로
  덧붙인다 — tap이 v0.55에서 formula → cask로 이전된 것과 일치한다. 이 처리가
  없으면 이전된 tap에 대해 `brew upgrade`가 낡은 v0.54 formula를 계속 잡았다.

## [0.57.1] - 2026-05-27

### Fixed

- **`brew install --cask` 사용자가 macOS Gatekeeper 때문에 `gk` 바이너리가
  설치 직후 휴지통으로 사라지던 문제 수정.** GoReleaser가 발행하는 cask에
  `postflight` 훅을 추가해 `#{staged_path}/gk`의 `com.apple.quarantine`
  확장 속성을 제거한다. macOS Sequoia(15+)에서 첫 실행 시 "확인되지 않은
  개발자" 모달이 한 번 뜰 수 있으나, 더 이상 바이너리가 자동 격리되지는
  않는다. 코드사인/공증 없이 unsigned CLI를 brew로 배포하기 위한 우회로,
  `install.sh`로 수동 설치한 경우와 `gk update`의 manual 분기에는 영향이
  없다.
- **`internal/cli` `TestFormatError_*`가 개발자의 `~/.config/gk/config.yaml`에
  따라 결과가 갈리던 테스트 격리 결함 수정.** non-Easy Mode의 `gk:` 접두
  출력을 검증하는 두 테스트가 Easy Mode를 명시적으로 끄지 않아,
  `output.easy: true`가 설정된 환경에서 항상 실패했다. `disableEasyForTest(t)`
  헬퍼를 도입해 두 테스트가 사용자 설정과 무관하게 동작한다.

## [0.57.0] - 2026-05-27

### Added

- **`gk prompt-info` opt-in signals.** New `--include=wip,dirty,ahead,behind,state`
  flag emits space-separated tokens (`wip`, `±N`, `↑N`, `↓N`, `!<state>`)
  alongside the existing worktree marker. JSON output grows matching opt-in
  fields. Each signal is gated because every one adds at least one extra git
  call per prompt render — pick what you need.
- **WIP chain grouping in `gk log` viz output.** Consecutive WIP commits (2+)
  are now bracketed with `┌│└` gutter glyphs. Singletons stay unmarked. A
  faint `┊ ~Nh gap` line appears between adjacent rows whose author times
  differ by 4h+ — commits are never collapsed, only the time discontinuity
  is surfaced.
- **`gk log --full` flag.** Suppresses the default subject-trim. Without
  `--full`, long subjects on narrow TTYs are truncated with a faint `…`
  instead of wrapping, which previously broke gutter and tag-rule alignment.
  Output to a pipe or file is never trimmed, so machine consumers still see
  the full subject.

### Changed

- **`gk log` scope tally now sums to the full commit count.** The header
  line (`scope: N commits · …`) previously listed only Conventional-Commits
  classifications; it now falls back to `wip` / `release` / `merge` /
  `other` for everything else, so the buckets add up to N.

## [0.56.0] - 2026-05-26

### Changed

- **Success messages now lead with a green `✓`.** `gk stash` (push /
  pop / apply / drop), `gk reset`, `gk switch`, `gk branch delete`,
  `gk branch set-parent` / `unset-parent`, `gk init`, `gk wip` /
  `unwip`, `gk undo`, `gk commit` (created / restored), `gk
  timemachine restore`, and `gk config init` now print a single
  consistent shape — `✓ <verb> <target>` — with the verb plain and
  the target in green. NoColor / CI captures degrade to plain text
  the same way the existing cell color helpers do.
- **Post-action `next:` / `also:` / `hint:` nudge lines are
  consistently styled.** Label dimmed, runnable command (`gk …` /
  `git …`) cyan, trailing `(meta)` annotation dimmed. Applies across
  `gk merge --into`, `gk forget`, `gk guard init`, `gk precheck`,
  `gk next`, `gk do`, `gk commit` (abort hint, WIP-chain skip hint),
  and `gk ai` (privacy gate). Advisory prose without an embedded
  command keeps the label dim and leaves the body unstyled.
- **`gk status` sync hints now expose the count in Normal mode.** The
  `try: gk push` / `gk pull` / `gk sync` lines pick up the `↑N` /
  `↓N` annotation the catalog had only been showing in Easy Mode.
  Same wording in EN and KO.

### Fixed

- **`gk merge --into` cleanup hint no longer leaks `%!(EXTRA
  string=…)`.** The i18n `hint.merge.into.cleanup_source` Normal-mode
  template carried fewer `%s` placeholders than the caller passed, so
  `fmt.Sprintf` appended the leftover argument as a visible error
  fragment. Fixed by aligning template and caller arg counts.
- **Same class of mismatch closed for `gk status` sync hints.**
  `hint.status.ahead`, `hint.status.behind`, and `hint.status.diverged`
  Normal-mode templates had 0 `%s` but the caller passed 1–2 counts.
  Currently masked by an upstream guard but a latent fmt leak waiting
  for the guard to change.
- **`gk precheck` ANSI sequences are now cell-safe.** Replaced raw
  `\x1b[0m` full-reset codes with the partial `\x1b[39m` / `\x1b[22m`
  resets the rest of the cell color helpers use, so they compose with
  table backgrounds without breaking the row highlight.

### Internal

- New shared helpers `successLine` / `successLinef` and
  `stylizeHintLine` / `stylizeHintLabel` / `stylizeHintCommand`
  centralize the success-and-hint formatting so future commands pick
  it up without re-inventing ANSI.
- `colorOff()` introduced so `cellGreen` / `cellCyan` / `cellFaint`
  honor both fatih/color's `NoColor` global and gk's `--no-color`
  flag. Test harnesses get a stable plain-text path either way.
- New `TestMain` in `internal/cli` forces `flagNoColor=true` for the
  whole package so substring assertions no longer race with sibling
  tests that toggle color state.

## [0.55.0] - 2026-05-26

### Added

- **`gk commit --force-wip`** bypasses the per-commit "already pushed"
  gate during the WIP-chain auto-unwrap, so a stack of save-point
  commits already on the remote can still be rewritten. A pre-reset
  stderr warning calls out that `git push --force-with-lease` is
  required afterward.

### Changed

- **`gk commit` now unwraps WIP-like chains on protected branches.**
  Previously `develop` / `main` / `master` / `trunk` short-circuited
  the chain detector outright, leaving stacks of `WIP(...)` commits
  unaddressed and the feature looking silently dead. The per-commit
  push detection (`git branch -r --contains`) remains the safety net,
  so already-pushed history is still left untouched by default.
- **Skipped WIP chains now surface a hint.** When the detector finds
  WIP commits but refuses to unwrap (already pushed, detached HEAD,
  merge commit, or `--no-wip-unwrap`), `gk commit` prints a one-line
  `hint:` after the `no working-tree changes to commit` line instead
  of staying silent.

## [0.54.0] - 2026-05-23

### Added

- **`gk refresh` (alias `gk re`)** fast-forwards your long-lived branches
  (main, develop) to their remote counterparts in one command, without leaving
  the branch you are on. Each tracked branch only fast-forwards to its own
  remote — never a cross-branch rebase — so it is safe on shared branches: a
  diverged branch is skipped with a hint instead of being rewritten. The list
  resolves from `refresh.tracked` in `.gk.yaml`, falling back to the repo's
  main plus develop/dev when unset.
- **`gk next --run` (`-r`)** executes the single top recommended next step
  after confirmation. The command comes from gk's deterministic action
  allowlist (never free-form AI output): gk-native commands re-execute the gk
  binary, git commands run directly, and risky or non-TTY cases are refused.
- **`gk review --base <branch>`** reviews the whole branch from its fork point
  (merge-base) so the base branch's own commits don't pollute the review.
- **`gk status --ai --json`** emits the structured status facts (branch,
  counts, recommended commands) for editors and scripts, with no provider call.

### Changed

- **`--ai` output is now advisory, not just a summary.** `gk review` returns
  severity-ordered, actionable findings (`path:line`, what is wrong, why, and a
  concrete fix); `gk status --ai` and `gk next` lead with a single RECOMMEND +
  WHY + ALTERNATIVE instead of a flat menu; `gk pr` opens with the key takeaway
  and adds Reviewer-focus and Risks-&-mitigations sections. The shared system
  prompt now coaches (prioritize, state trade-offs) rather than describe.
- **AI calls are bounded and cheaper to repeat.** Every AI command honours a
  per-call timeout and `max_tokens`, caches results by repository state under
  `.git/gk-ai-cache` (an unchanged tree reuses the answer), and Anthropic
  requests use prompt caching for the fixed system prompt. `gk review` and
  `gk changelog` `--format json` now emit real structured JSON instead of raw
  text, and the provider auto-detect order is unified across commands
  (anthropic → openai → nvidia → groq → gemini → qwen → kiro).
- **`gk status --ai` is grounded and guarded.** It can include the working-tree
  diff (`ai.assist.include_diff`), flags hard-to-undo commands in the answer
  with a caution footer, and in `mode: auto` skips the provider entirely when
  the tree is idle.

### Fixed

- **`gk commit` no longer drops files the AI classifier leaves out.** Files the
  model omitted from every group were silently skipped and never committed, so
  one run could miss changes and force a re-run. The classifier now ignores
  phantom paths the model invents and sweeps any uncovered file into a fallback
  group, so a single `gk commit` covers everything in scope.
- **AI privacy and remote policy now apply to every command.** The privacy gate
  redacts the actual commit diff sent to providers (previously only a summary
  was redacted); `ai.commit.allow_remote=false` is enforced across
  pr/review/changelog/ask/explain/do/status/merge, not just commit; and `gk do`
  is hardened — it re-checks the git subcommand allowlist at execution time,
  treats `rm`/`restore`/`checkout -- <path>`/`stash drop` as dangerous, and
  redacts repository context before it leaves the process.
- **`gk doctor --ai` reports accurately.** It honours configured provider
  endpoints, distinguishes "key set (validity not verified)" from "endpoint
  reachable", and surfaces a provider's stderr (auth/quota) instead of a bare
  "empty response".

### Removed

- **`ai.chat.safety_confirm`** — the config field was a no-op (dangerous
  `gk do` commands always require confirmation), so it was removed rather than
  imply a toggle that never worked.

## [0.53.0] - 2026-05-20

### Changed

- **The `gk switch` picker's action hotkeys (`n`/`d`/`D`/`f`/`r`) now work
  after filtering.** Typing in the `/` filter used to swallow every key, so
  once you narrowed to a branch the only move was Enter (switch). `Esc` now
  stages: the first press leaves the filter box but keeps the narrowed list,
  so the hotkeys act on the highlighted row; a second `Esc` clears the filter
  and restores the full list; a third (or `Esc` with no active filter) cancels
  the picker. `q` and `Ctrl+C` still cancel immediately from any state. This
  applies to every `TablePicker`-based prompt, not just `gk switch`.

## [0.52.0] - 2026-05-20

### Added

- **`gk switch <name>` now offers to track or create when the branch is
  missing**, instead of dead-ending on git's `invalid reference`. On a miss it
  checks the remote: if `<name>` exists there it offers to fetch and track it
  (default yes — the branch demonstrably exists upstream); otherwise it offers
  to create the branch from HEAD (default no, so a typo like `gk sw mian`
  doesn't silently spawn a branch). Off a TTY it prints the matching hint
  (`gk sw --fetch <name>` or `gk sw -c <name>`) rather than prompting.

## [0.51.0] - 2026-05-20

### Added

- **The `gk switch` picker's `r` (remotes) key now fetches when the view is
  stale.** Pressing `r` means "show me the remote", so cached refs that hide a
  teammate's just-pushed branch are a trap. `r` now refreshes first when the
  last successful fetch is older than 60s (or never happened), and toggles
  instantly otherwise — staying offline-friendly: a failed fetch reveals the
  cached branches with a warning rather than blocking. The subtitle shows the
  freshness (`fetched 3m ago`, `never fetched`, or `fetch failed`). Staleness is
  judged from FETCH_HEAD content, not just its mtime, so a failed fetch never
  masquerades as fresh.

## [0.50.1] - 2026-05-20

### Fixed

- **`gk switch` no longer echoes git's `git rebase --quit` advice when it fails
  mid-operation.** v0.50.0 added the `gk continue` / `gk abort` hint but left
  git's own (wrong for gk) suggestion in the error body, and the wrapped
  `ExitError` printed the stderr twice. When gk recognizes the in-progress
  operation, the message is now a single clean line
  (`cannot switch to <branch>: a <op> is in progress`) followed by the hint.

### Docs

- **`gk guard` help no longer claims a "graceful fallback" when gitleaks is
  absent.** Without gitleaks the `secret_patterns` rule is a no-op that emits an
  info note — it does not run a built-in scan. The old wording could read as if
  secrets were still being checked.

## [0.50.0] - 2026-05-20

### Changed

- **`gk reset <ref>` now targets `<ref>` instead of silently ignoring it.**
  Previously a positional argument was dropped, so `gk reset main` ran a
  destructive reset to the *current branch's upstream* while pretending to
  target `main`. The positional ref is now an alias for `--to` (and is
  rejected when combined with `--to` / `--to-remote`).

### Fixed

- **`gk reset` and `gk switch` now point at `gk continue` / `gk abort` when a
  rebase, merge, cherry-pick, or revert is in progress.** On a detached HEAD
  mid-rebase, `gk reset` used to suggest `gk switch` — a dead end, since git
  refuses to switch branches while rebasing — and `gk switch` leaked git's own
  `git rebase --quit` advice. Both now detect the in-progress operation (via
  `gitstate`) and suggest the two real ways out.

## [0.49.0] - 2026-05-18

### Changed

- **`install.sh` now defaults to `~/.local/bin` instead of `/usr/local/bin`.**
  The previous default required a writable system path or a `sudo`
  escalation, then fell back to `~/.local/bin` anyway — so the common
  outcome was an inconsistent install location across machines. The
  user-owned directory is now the default and needs no `sudo`; the
  `sudo` path remains only as a fallback when `GK_INSTALL_DIR` is
  overridden to a system path. The manual `tar` snippet in the README
  was updated to match (`tar -xz -C ~/.local/bin`, no `sudo`).

## [0.47.0] - 2026-05-13

### Added

- **`gk prompt-info --format=segment` for unified prompt labels.** Emits
  `<repo>/<branch>` inside any git repo (and empty outside), designed to
  replace starship's `$directory` + `$git_branch` with a single, dedup-
  friendly segment. The JSON payload also gains a `repo` field on the
  same schema so prompt frameworks that compose their own segments can
  pull the project name without an extra `git rev-parse` round-trip.

- **`make install-gk` / `make uninstall-gk` Makefile targets.** Installs
  the dev build as the canonical `gk` (shadowing the Homebrew binary
  when `~/.local/bin` precedes `/opt/homebrew/bin` in `$PATH`). Wraps
  `make install INSTALL_NAME=gk` so the install logic stays in one
  place. The default `make install` still writes `gk-dev` to keep
  outside contributors safe from accidentally overriding Homebrew.

### Changed

- **`gk status` BRANCH header now identifies the project.** The header
  is prefixed with the repo name (`gk · main`) so captures and logs
  shared elsewhere carry their project context. The `@ <wt-name>`
  annotation is suppressed when it matches the current branch — the
  common case under `~/.gk/worktree/<repo>/<branch>` — and the `wt:`
  path line condenses `$HOME` to `~`.

- **`gk prompt-info` plain output collapses redundant worktree names.**
  When the worktree directory equals the current branch name, the
  output is now `wt` instead of `wt:<name>`. The branch name is already
  next door in the prompt, and `wt:improve-ux` next to a branch segment
  of `improve-ux` was triple-displaying the same token across cwd,
  branch, and worktree marker. The `wt:<name>` form is retained when
  the worktree directory disagrees with the branch (rare but worth
  surfacing).

### Internal

- `--path-format=absolute` (git 2.31+) hardens `detectPromptInfo` and
  `detectRepoName` against cwd vs `runner.Dir` drift, replacing a
  fragile `filepath.Abs` that silently resolved against process cwd
  instead of the runner's working directory.

- Format dispatch in `prompt-info` split out as `formatPromptInfo` so
  table-driven tests can exercise `plain`, `segment`, `json`, and
  unknown-format paths without spinning up a real git repo per case.

## [0.46.0] - 2026-05-13

### Added

- **`gk prompt-info` for shell prompt integration.** Emits a compact
  worktree indicator suitable for prompt themes (starship, p10k, plain
  zsh). Plain output is `wt:<basename>` inside a linked worktree and
  empty in the primary worktree or outside a repo, so PS1 stays clean
  in the common case and flags non-primary sessions when it matters.
  `--format=json` returns `{linked, name, path, branch}` for prompt
  frameworks that consume structured segments. Detection compares
  `git rev-parse --git-dir` against `--git-common-dir` (~30ms per call),
  fast enough for prompts that re-render on every keystroke; a `chpwd`
  cache pattern is documented for zero-overhead integration.

### Changed

- **`gk sw <branch>` blocked by another worktree now offers both paths.**
  The previous hint surfaced only `gk worktree remove` — destructive,
  and the wrong answer when the user just wants to use the branch.
  The hint now shows two options: `work on it there → cd <path>` and
  `bring it here → gk worktree remove <path>`. Dirty worktrees steer
  toward the cd path until the work is committed or stashed. The
  picker keeps its smart-handoff subshell flow, since selecting a
  locked branch in the picker is a clearer "take me there" signal.

- **`gk status -vv` surfaces other worktrees with cd-able paths.** The
  BRANCH block gains a `worktrees: <branch> @ <path>` listing (one per
  other linked worktree, HOME abbreviated to `~`). Gated behind `-vv`
  because the same information is intended to live in the shell prompt
  via `gk prompt-info` — surfacing it on every `gk st` would just be
  duplicate noise. Detached worktrees show `(detached)` in place of
  the branch name.

## [0.45.1] - 2026-05-11

### Added

- **`gk sw --fetch` and picker `f fetch` update remote branch refs before switching.**
  Remote-only rows still come from cached `refs/remotes/*` for fast startup,
  but users can now refresh from inside the switch UX instead of exiting to
  run `git fetch`. The fetch is scoped to the configured remote, prunes stale
  remote-tracking branches, skips tags and submodules, and opens/reopens the
  picker with remote rows visible. Direct usage such as
  `gk sw --fetch feat/new-remote-branch` also works when the branch was just
  created upstream. The switch filter also searches hidden remote-only rows,
  so `/ tmux` can surface `origin/tmux` even before pressing `r`.

## [0.45.0] - 2026-05-11

### Changed

- **`gk worktree list` and the `gk wt` TUI gain `gk sw`-style columns.**
  The legacy three-column layout (`PATH | SHA | BRANCH`) skipped the
  two questions you actually ask when juggling worktrees: where this
  branch came from, and how far it has drifted. The list now renders
  `BRANCH | SOURCE | DIFF | AGE | PATH | FLAGS`, with `★` marking the
  worktree the invocation runs from. `SOURCE` shows `⇄ <upstream>` when
  an upstream is tracked, otherwise `from <parent>@<sha>` — the fork
  point resolved through the same `branchparent` machinery that feeds
  `gk sw`. `DIFF` mirrors the upstream `↑X ↓Y` pair, `AGE` carries the
  compact `5m`/`2h`/`10d` last-commit ribbon, and long paths get a
  middle ellipsis with the basename preserved so the worktree name
  stays readable on narrow terminals. Inside the TUI the SOURCE cell
  uses the in-cell colour helpers (`cellCyan`, `cellFaint`) instead of
  fatih's `\x1b[0m` full-reset variants, so the cursor-row purple
  highlight bar no longer tears mid-cell on the active row. Global
  mode (`gk wt -g`) keeps the previous layout — cross-repo branch
  metadata is out of scope for the cross-project picker.

## [0.44.0] - 2026-05-11

### Changed

- **`gk status -v` now renders a dedicated BRANCH section.** The legacy
  rich-mode path extracted the first line of the captured body and
  shoved it into the section's summary slot, where dim wrapping at the
  section chrome collided with embedded bold/colour escapes — branch
  names regularly disappeared from the section the heading was named
  after, leaving stragglers like `█ BRANCH 22m abc1234` visible without
  any branch identity. A new `renderBranchSection` writes the branch
  line as the section body with full control over styling, and surfaces
  three identity hints the legacy renderer never had: the current
  worktree (`@ <name>` + `wt: <path>`) when running from a linked
  worktree, and the fork parent (`← <branch>`) resolved through
  `branchparent` so per-branch metadata wins over `origin/HEAD`. Both
  annotations are suppressed on the primary worktree / trunk to keep
  the common case terse; detached HEADs render as `⚠ detached at <sha>`.

## [0.43.0] - 2026-05-11

### Changed

- **`gk update` no longer hits the GitHub REST API on the happy path.**
  `brew` and `go-install` installs now short-circuit before any network
  call — both have their own version resolution, so the banner-only
  `current → latest` round-trip was burning the 60 req/hr anonymous
  quota for nothing. Manual installs (and `--check` / `--to`) resolve
  the latest tag from `https://github.com/x-mesh/gk/releases/latest` via
  the standard 302 redirect (the trick `install.sh` already uses); the
  api.github.com JSON endpoint is kept as a quiet fallback for proxied
  environments. Result: `gk update` from a brew install no longer fails
  with `403 rate limit exceeded`.

- **`gk status --watch` is now flicker-free and interactive.** The
  legacy `\033[H\033[2J` clear-then-paint loop produced a visible blank
  flash on every tick. Watch mode now runs as a bubbletea program
  against the alt-screen with full-frame buffered redraws — visually
  identical content costs zero repaint bytes. Adds a header status line
  (`gk watch · every 2s · last 14:23:01 · ● just changed · [keys]`) and
  live keyboard controls: `r` force refresh, `p` / `space` pause,
  `+` / `-` double / halve interval (clamped to `[250ms, 60s]`),
  `q` / `esc` / `Ctrl-C` quit. Lines that are new in the latest frame
  get a cyan `▎` left-gutter marker for ~1.5s after each transition so
  the eye lands on *what* changed; the 2-column gutter is reserved
  unconditionally so the body never shifts horizontally when the pulse
  drops. Hash equality runs on the ANSI-stripped, line-trimmed canonical
  form, so styling reorders no longer fire false-positive pulses.
  Non-TTY stdout (pipes, CI, redirection) keeps the legacy scroll-style
  reprint so `gk status --watch | tee log` still works. Set
  `GK_WATCH_DEBUG=1` to dump the four most recent normalized frames to
  `/tmp/gk-watch-frame-{0..3}.txt` for diagnosing rare false-positive
  pulses.

## [0.42.0] - 2026-05-11

### Changed

- **Rich-mode `gk status` no longer wraps sections in single-line boxes.**
  The legacy box renderer padded each line to a fixed width, which
  misaligned with wide-character content (한글, emoji, coloured glyphs)
  and pushed the right wall onto the next row on narrow or resized
  TTYs. Sections are now framed with a coloured `█` bar (default) or
  bracketed by horizontal rules, neither of which depends on body
  width. Choose between the two via `status.layout: bar | rule` in
  `.gk.yaml`; `bar` is the default. The `gk status -v` rich output
  also gains an inline title-row summary so the headline ("main →
  origin/main · ↑3 ↓0", "53 commits last 7 days", etc.) reads
  immediately without descending into the body.

- **`gk doctor` adopts the same rich-mode section UI.** Environment /
  Repository state / Summary now render as bar sections, with per-section
  pass/warn/fail counts hoisted into the title row and a severity-aware
  Summary chrome (orange when anything fails, mustard for warnings,
  olive when clean).

- **`gk pull` blocked / paused / diverged banners share the section
  vocabulary.** The diverged-refusal hint splits into DIVERGED + PICK
  ONE bar sections, and the in-progress / paused-conflict banners
  expand into PAUSED (or BLOCKED) + RESOLVE + optional BACKUP /
  AUTOSTASH sections. Conflict file lists and the inline conflict
  preview are captured into the parent section's body so they sit
  inside the diagnosis frame.

- **`gk merge` AI / local plan render as MERGE PLAN + VERDICT
  sections.** The target → current direction and conflict count live
  in the title's summary slot; the AI body keeps its
  SUMMARY / RISK / INSPECT / NEXT inline labels but is wrapped in a
  bar frame, and the trailing verdict is its own section whose chrome
  reflects the severity (orange when conflicts/HIGH risk, mustard for
  moderate, olive when clean).

- **`gk sync` STALE BASE warning and SYNCED result use bar sections.**
  Streaming progress lines (`fetching origin/main`,
  `integrating main into feature ...`) stay flat — only the diagnostic
  warning and the final result block carry section chrome.

- **`gk diff` per-file headers use horizontal-rule sections.** The
  legacy 60-char `─` separator is now a `── path ──` rule with
  status-tinted chrome (added → olive, deleted → orange, renamed/copied
  → violet, mode-only → faint).

- **`gk worktree list` gains a section header.** New
  `WORKTREES   N entries · M detached · K locked` summary above the
  table; the table body itself is unchanged.

### Added

- **`status.layout: bar | rule` config option** for rich-mode framing.
  Defaults to `bar`. Ignored when `status.density` is `normal`. Both
  layouts are independent of body width, so wide characters in branch
  names, file paths, or status badges no longer push the chrome out
  of alignment.

- **`status.density` and `status.layout` are now documented in
  `docs/config.md`** with their full value tables.

- **`internal/ui` section helpers** — `RenderSection`,
  `RenderNextAction`, `SectionColor`, plus intent-named colour vars
  (`SectionInfo`, `SectionCaution`, `SectionDiverged`, `SectionHealth`,
  `SectionAction`, `SectionMuted`) and a `KeepCase` opt for paths /
  proper nouns. Reused by status, doctor, pull, merge, sync, diff,
  and worktree so the same section name always means the same colour.

### Internal

- Removed the legacy `renderBox` / `renderNextActionBlock` helpers
  (~120 lines) and the `visibleWidth`-based padding logic that
  misaligned with wide characters. Replaced by `internal/ui/section.go`
  whose chrome is independent of body width.
- Refactored `renderActivityHeatmap` to return
  `(lines, total, ok)` so the total can be hoisted into the section's
  summary slot rather than stitched into the sparkline string.
- 18-case golden test suite for `internal/ui/section.go` covering
  bar / rule layouts, summary slot variants, NoColor, KeepCase, and
  a regression for body-width chrome bleed.

## [0.41.1] - 2026-05-10

### Fixed

- **`gk switch` no longer panics after an empty filter result.** The
  built-in table picker now normalizes a stale negative cursor before
  selecting a row, so filtering branches down to zero matches and then
  selecting a restored match no longer trips an `index out of range [-1]`
  panic.

### Internal

- **Diff color tests now work under `NO_COLOR=1` environments.** The
  test-only `forceColor` helper clears `NO_COLOR` before asserting ANSI
  output, so local shells and CI jobs that export the standard opt-out
  variable no longer fail unrelated release verification.

## [0.41.0] - 2026-05-09

### Added

- **`gk doctor --ai` flag.** Probes optional AI provider integrations
  (anthropic / openai / nvidia / groq API keys, gemini / qwen /
  kiro-cli binaries) without enabling the rest of `--verbose`. The
  existing `--verbose` flag still includes the AI rows, so `--ai`
  is the focused alias for users who only care about provider
  status.
- **Actionable hints on `gk pull` fetch failures.** When the
  underlying `git fetch` errors out, gk now rewrites the message
  with a copy-paste fix tailored to the failure mode: missing
  remote → `git remote add`; wrong remote URL → `git remote set-url`;
  remote ref not found → `git fetch <remote>` plus `gk pull --base`;
  permission/auth → credentials guidance; DNS/timeout → network
  hint with the raw `git fetch` command. The hint flows through
  `WithHint`, so `--json` clients see it in the `hint` field too.

### Changed

- **`gk status` verbose summary now reflects remote state.** The
  refs row used to always say `local refs · pass --fetch to refresh
  upstream`, even on a brand-new repo with no remote configured. It
  now reports `no remotes · add a remote before pull/fetch` for
  fresh repos and `local refs · set upstream or pass --fetch after
  choosing a remote` when a remote exists but the current branch
  has no upstream.

### Internal

- **`internal/cli/remote_hint.go`** centralizes the
  `git remote` / `git remote get-url` lookups shared by the new
  `gk pull` fetch-failure hint and the `gk status` refs summary.
- **`internal/diff/json_test.go`** adds a defensive `return` after
  `t.Fatal` so staticcheck (SA5011) no longer reports a possible
  nil dereference on the post-guard `len(dj.Files)` access.

## [0.40.0] - 2026-05-09

### Added

- **`gk next` command — plain-language status explanation.** Direct
  entry point for "what should I do now?". The assistant receives
  structured repo facts (branch, upstream, ahead/behind counts,
  conflict counts, short path preview) and returns a short plan plus
  recommendations drawn from gk's precomputed safe-command list. It
  does not receive patch contents. Falls back to a local rule-based
  plan when no AI provider is available. `--provider` and `--lang`
  override `ai.provider` / `ai.lang` for a single invocation.
- **`gk status --ai` flag.** Appends an AI explanation of the current
  state and next safe actions to the compact status output, using the
  same fact-only prompt as `gk next`. Not supported with `--json`
  (errors with a pointer to `gk next`) or `--watch`. `--provider` /
  `--lang` overrides mirror the rest of the AI surface.
- **`ai.assist` config section.** New `mode` (`off` | `suggest` |
  `auto`) controls whether AI help is attached to existing commands;
  `status` gates the `gk status` surface; `include_diff` is reserved
  for future richer prompts (the status assistant currently sends
  facts only, never patch contents).

## [0.39.1] - 2026-05-08

### Fixed

- **`gk st` cross-worktree scan now runs probes in parallel** instead
  of serially. Previously each non-current worktree triggered a
  synchronous `git rev-list --left-right --count` call, so a five-
  worktree repo could blow past the 50ms status latency budget. The
  scan now dispatches probes through a bounded worker pool (cap 4),
  preserving result order so the rendered hint is deterministic.
- **`easy.Engine.effectiveHints` no longer rebuilds the fallback
  HintGenerator on every call.** The disabled-mode path used to
  allocate a fresh `i18n.Catalog` and `EmojiMapper` per hint emission;
  it now caches the synthesized generator behind `sync.Once` so
  repeated `MergeIntoNextHint` / `PushSummaryHint` /
  `StatusCrossWorktreeHint` calls reuse the same instance.

### Internal

- **`git.FakeRunner.Run` is now thread-safe.** v0.39.0's parallelized
  cross-worktree scan exposed an unguarded `Calls` slice append in
  the test fake; a `sync.Mutex` now protects the recorder.
- **Tests added for v0.39.0 surface** flagged by post-release review:
  `gk push --json` schema (ahead and up-to-date cases), disabled-mode
  fallback for the three new `Engine` hint methods, and Easy Mode
  variants for all six new i18n keys (en + ko, with a regression
  guard against legacy emoji creeping back in).

## [0.39.0] - 2026-05-08

### Changed

- **`gk merge --into <branch>` prints next-step hints after a successful
  merge.** Both the worktree-bypass path (added in v0.38.0) and the
  worktree-delegated path now append `next: gk push --from <receiver>`,
  plus `also: gk branch delete <source> (fully merged)` when the source
  is fully merged into the receiver. Hints come from the i18n catalog
  (Korean and English shipped) and render in normal mode, not just Easy
  Mode.
- **`gk push` appends a one-line summary** matching other gk commands:
  `pushed N commit(s) to origin/main (abc1234)`, or
  `up-to-date with origin/main (abc1234)` when nothing was uploaded.
  Git's raw output stays above the summary so CI parsers and scripts
  that key off `To <url>` or `<old>..<new>` keep working. A new
  `--json` flag emits `{remote, branch, ahead, head}` instead and
  suppresses git's text output for automation. The ahead-count is
  computed via `git rev-list --count` before the push; if that call
  fails, gk falls back to ahead=0 instead of aborting.
- **`gk st` cross-worktree hint.** When the current worktree is in sync
  and clean, status no longer ends on a "nothing to do" placeholder.
  It scans the other worktrees in the repo and lists up to three with
  pending work (`worktree feat/x: ↑3  ·  worktree feat/y: ↓2  ·  +N more`),
  or prints `all clean across N worktree(s)` when every one is idle.
  Detection is divergence-only (`HEAD@{upstream}...HEAD` per worktree);
  dirty-tree checks are skipped to stay within the status latency budget.
  Per-worktree git failures drop silently so one broken upstream cannot
  blank out the whole hint.

## [0.38.0] - 2026-05-08

### Changed

- **`gk merge --into <branch>` no longer requires the receiver branch to
  be checked out in some worktree.** Previously the command refused with
  `no worktree has branch "X" checked out` whenever `git worktree list`
  did not show the receiver, forcing users to materialize a worktree
  even for routine "land my branch on local main" flows. The receiver
  is now updated directly in two cases:
  1. **Fast-forward** (receiver is an ancestor of the source) — runs
     `git update-ref refs/heads/<receiver> <source>` with no merge
     commit, no worktree, no working-tree mutation.
  2. **Non-fast-forward, conflict-free** — builds the merge tree with
     `git merge-tree`, packages it via `git commit-tree` (two parents:
     receiver, source), then `update-ref`. The receiver advances by one
     merge commit without any worktree being touched.

  Conflicts still require a worktree to resolve interactively, so when
  the precheck reports conflicts gk refuses with a hint pointing at
  `gk worktree add <path> <receiver>`. `--squash` is also gated to the
  worktree path for now (the in-memory squash variant is implementable
  but out of scope for this change). When the receiver *does* have a
  worktree, behavior is unchanged — the existing worktree path runs.

## [0.37.1] - 2026-05-06

### Fixed

- **`gk resolve` no longer refuses to help when the only signal is
  unmerged paths.** `git stash apply`, `git apply --3way`, and a few
  partial-reset paths leave unmerged stages in the index *without*
  writing any of the in-progress op markers (`MERGE_HEAD`,
  `rebase-merge/`, `CHERRY_PICK_HEAD`, etc.) that
  `gitstate.Detect` keys off. The previous gate fired before file
  collection and turned that exact case into a dead end —
  `gk pull`'s new pre-flight pointed users at `gk resolve`, only for
  `gk resolve` to claim "no merge/rebase/cherry-pick conflict in
  progress". Resolver now collects unmerged files first; falls back
  to the "merge" op type when the marker is missing; rejects only
  when both signals are absent (with an updated message that names
  the unmerged-paths half of the gate).
- **`guardWorkingTreeReady`'s remediation hint adapts to whether an
  op is actually in progress.** When `MERGE_HEAD` / rebase-merge /
  CHERRY_PICK_HEAD is set, the hint suggests
  `git merge|rebase|cherry-pick --continue|--abort` as before. When
  none is — the stash-apply case above — it suggests
  `git add <files> && git commit` and `git checkout -- <files>`
  instead, so users following the printed advice don't hit
  `fatal: No rebase in progress`.

## [0.37.0] - 2026-05-06

### Fixed

- **`gk pull` / `gk sync` / `gk merge` no longer mask the real cause
  of a stash failure when the working tree has unmerged paths.** On
  git 2.43, `git stash push` rejects an unmerged tree by exiting 1
  with an empty stderr, so callers that prompted for "stash &
  continue" first surfaced a meaningless `stash push: : exit code 1`
  several seconds after the user committed to the action. The three
  commands now run a `guardWorkingTreeReady` pre-check immediately
  after the dirty probe and refuse outright with a hint that names
  the conflicted files plus the right remediation
  (`gk resolve` / `git rebase --continue` / `git rebase --abort`).
  This is the case `gk doctor` already caught as a FAIL row — the
  pull/sync/merge surfaces now align with that diagnosis instead of
  re-discovering it after a wasted prompt.

### Changed

- **Stash-failure hints are now generated from live repo state
  instead of a fixed string.** When `git stash push` does fail past
  the new pre-check (race conditions, sparse checkouts, partial
  clones), `diagnoseStashFailure` walks the repo and picks the
  highest-priority cause it finds: stale `index.lock`, unmerged
  paths, or an in-progress rebase/merge/cherry-pick/bisect/revert.
  Falls through to a "reproduce directly with `git stash push -m
  gk-debug`" pointer when nothing distinctive is detected. Replaces
  the previous one-line `git failed to write the index. run gk
  doctor to inspect (lock file? in-progress merge?)` placeholder.

## [0.36.0] - 2026-05-06

### Changed

- **`gk doctor` baseline output is quieter and more honest.** The
  `fzf` row was removed — gk hasn't shelled out to the `fzf` binary
  since the bubbletea-based `TablePicker` shipped, so warning users
  to install it was misleading. The seven AI-integration rows
  (`anthropic`/`openai`/`nvidia`/`groq` API keys, plus the
  `gemini`/`qwen`/`kiro-cli` binaries) now surface only under
  `gk doctor --verbose`, leaving the default report focused on
  issues that actually block gk. On a typical machine this drops
  the WARN count from ~13 to ~4.

### Internal

- **Picker plumbing dead-code purge.** The unused `FzfPicker`,
  `FzfAvailable`, `writePreviewMap`, and `shellQuote` symbols in
  `internal/ui` were removed and `internal/ui/fzf.go` renamed to
  `picker.go` to match what is left (the shared `PickerItem` /
  `Picker` types and `FallbackPicker`). Stale `FzfPicker` mentions
  in `internal/cli/switch.go`, `internal/cli/worktree.go`, and
  `internal/ui/table_picker.go` doc comments now point at
  `FallbackPicker`. Drops a never-callable nil check from
  `internal/ui/formatter_test.go` so `staticcheck` stays clean.

## [0.35.0] - 2026-05-06

### Added

- **`gk forget --analyze --json`** for CI / dashboards. Single JSON
  document with `entries[]` (path, unique_blobs, total_bytes,
  largest_bytes, in_head) plus aggregate `total_bytes` and
  `history_only_bytes`. Skips the human header / footer / next-steps
  block when `--json` is set. Stable shape — new fields may be added
  but existing keys never change meaning.

- **`gk forget --analyze --sort <mode>`.** `size` (default) keeps the
  prior ranking; `churn` ranks by unique-blob count, surfacing
  rewrite-heavy paths whose individual blobs are small but whose
  cumulative weight matters (lock files, generated outputs); `name`
  is alphabetical for stable diffs across runs. Tie-breakers always
  fall back to alphabetical so identical inputs render identically.

- **`gk forget --analyze --interactive` / `-i`.** Multi-select picker
  built on the same `internal/ui.MultiSelectTUI` that powers branch
  pick. Toggle with space, enter to continue, esc to cancel. The
  chosen paths are fed straight into the standard rewrite pipeline,
  so the existing dirty-vs-target gate, backup ref, and
  confirmation prompt all still fire — interactive mode adds nothing
  destructive on its own; it just narrows the target list. Requires
  a TTY; non-TTY invocations surface a clear hint instead of
  silently dropping into a different mode.

  Each picker row reuses the path-truncation logic from the bar
  renderer so deeply nested paths stay readable, and the
  `(history-only)` marker is inlined so users can spot the
  highest-leverage rows during selection.

### Changed

- `forget.Audit` gained a `SortMode` parameter (was implicitly
  size-descending). Callers in tree updated; the new `ParseSortMode`
  helper turns the CLI flag string into the enum.

### Added

- **In-bar labels for `gk forget --analyze` output.** Each row now
  reads as a single line where the label (path / blob count / size /
  history-only flag) sits on top of a coloured background that covers
  exactly the entry's share of the heaviest entry. Same idea as `htop`
  CPU bars or `du-dust` size bars: length is the ratio, the text on
  top is always parseable. History-only buckets get a warm red
  background; live entries get navy blue.

  New `--bar=auto|filled|block|none` flag (default `auto`):
  - `auto`: filled on a colour TTY, plain on pipes / `--no-color` /
    redirects (so `gk forget --analyze | grep` stays clean).
  - `filled`: force the in-bar-label style even when stdout is not a
    detected TTY — useful for screenshots.
  - `block`: keep the label as plain text and append a sub-cell-
    precision block-glyph bar (`█▉▊▋▌▍▎▏░`) in a separate column.
    Survives monochrome terminals where backgrounds are not
    distinguishable.
  - `none`: original plain text rows.

  Other improvements alongside:
  - **Path truncation** — long paths are abbreviated mid-string with a
    `…` so the bar column stays aligned (`rca-database/.../pg_wal/000…0009`).
  - **Footer summary** — total bytes shown across visible buckets and
    history-only subtotal, so the user can size the long tail at a
    glance.
  - Terminal width is auto-detected via `golang.org/x/term`; falls
    back to 100 columns when the size lookup fails.

  Lipgloss does the rendering, mirroring `gk status -v` and other
  rich-mode surfaces. Colour is suppressed automatically when stdout
  is not a TTY, so piping the audit output into another command does
  not leak ANSI escapes.

## [0.33.0] - 2026-05-06

### Added

- **`gk forget --analyze` repo-wide audit fallback.** When `--analyze`
  is invoked with no positional targets and no `.gitignore`-derived
  auto-detect hits, gk now switches into an explore-the-landscape
  mode that scans every reachable object on every ref and prints the
  heaviest path buckets:
  - `--depth N` (default 1) groups results by the first N path
    segments. depth=0 lists individual files; depth=2 walks one level
    inside top-level dirs.
  - `--top N` (default 20) caps the result set, sorted by total
    bytes descending.
  - Each row shows `unique blobs / total / largest`, plus a
    `(history-only)` flag when the bucket no longer exists in HEAD —
    those are the highest-leverage forget targets because removing
    them from history reclaims space without affecting current work.
  - Streams `git rev-list --all --objects | git cat-file
    --batch-check` so even multi-million-object repos do not
    materialise the listing in memory.
  - The post-output hint walks the user from "I see what's heavy" to
    a concrete `gk forget --analyze <path>` (exact reclaim estimate)
    or `echo path/ >> .gitignore && gk forget` (rewrite).
  - `--analyze` no longer requires `git-filter-repo` on PATH because
    audit is read-only; the binary check moves into the rewrite
    branch only.

  Targeted `gk forget --analyze <path>` is unchanged.

## [0.32.2] - 2026-05-06

### Fixed

- **`gk forget` rejected the workflow it was designed for.** Adding a
  live-data directory (e.g. a PostgreSQL `pg_data/` checkout) to
  `.gitignore` and running `gk forget` aborted with
  `working tree has uncommitted changes; commit or stash first`,
  because the same files the user wanted to delete from history were
  flagged as M/D in `git status`. Telling the user to "stash first" is
  exactly wrong: the changes were going to be erased anyway.

  Fix: split the dirty-tree gate from the structural gate. Rebase /
  merge / cherry-pick still hard-block. Dirty entries are partitioned
  by location:
    - paths inside any forget target → ignored (filter-repo will erase
      them);
    - paths outside any target → still abort, with a hint surfacing up
      to five offending paths and suggesting commit/stash/narrow-target
      remediation.

  New `--force-dirty` flag lets users override the outside-target gate
  when they have reviewed the loss; filter-repo will reset those
  changes. The interactive review and backup steps are unchanged.

  `pathUnderAny` matches a target with or without a trailing slash and
  treats it as a directory cover, mirroring filter-repo's own path
  argument semantics.

## [0.32.1] - 2026-05-06

### Fixed

- **`gk pull` and `gk sync` could fail with "No stash entries found"
  after auto-stashing a dirty tree.** Trigger: the working tree was
  dirty for a reason `git stash push` silently skips by default —
  submodule pointer mismatch or a file-mode-bit-only diff. In those
  cases stash push exits 0 with `No local changes to save` printed to
  stdout, but our pre-check (`git status --porcelain -uno`) already
  considered the tree dirty, so the caller marked the stash as
  successful and tried to pop it minutes later, after fetch had
  finished. The pop blew up with the misleading "No stash entries
  found" error.

  Fix: a new `stashIfChanged` helper compares `refs/stash` before and
  after the push and reports the actual outcome. When stash created
  no entry, callers skip the pop and emit a debug line identifying
  the most likely cause via `describeDirtyButNotStashed`, which
  inspects `git submodule status` for `+`/`-` lines and `git diff
  --raw HEAD` for mode-bit changes. Applied at all four `git stash
  push` call sites: `gk pull --autostash`, `gk pull` interactive
  prompt, and the two `gk sync --autostash` paths.

  In practice this turns the most common failure mode after
  `gk sw <remote-only-branch>` (where the new branch's submodule
  pointer differs from the prior branch's) from a confusing pop
  error into a clean no-op with an actionable hint.

## [0.32.0] - 2026-05-06

### Added

- **`gk forget --analyze`.** Walks `git log --all --raw` for each target,
  collects unique post-image blob OIDs, and pipes them through
  `git cat-file --batch-check` so the cost of a forget can be estimated
  without rewriting anything. Output reports per-path unique blob count,
  total bytes, and largest single blob, plus a grand total. Implies
  `--dry-run`. Useful for asking "is the rewrite worth it?" before
  paying the SHA-churn tax.
- **`gk forget --keep <glob>` exclusions.** Repeatable flag using
  `filepath.Match` syntax (the same dialect as `ai.commit.deny_paths`).
  A keep pattern matches the path itself or any parent directory, so
  `--keep db/keep` strips `db/keep/seed.sql` and everything beneath it.
  Invalid patterns surface a clean diagnostic up front instead of
  silently failing to match.

### Changed

- **Forget backup refs are now shaped as
  `refs/gk/forget-backup/<branch>/<unix>`** (one ref per source
  branch/tag, with tags written as `tag-<name>`). The previous
  `refs/gk/forget-backup/<unix>/refs/heads/<name>` shape did not match
  the gitsafe `<kind>-backup/<branch>/<unix>` grammar, so backups were
  invisible to `gitsafe.ListBackups` and to `gk timemachine list`.
  The flat-text manifest under `.git/gk/forget-backup-<unix>.txt` is
  unchanged.
- **`gitsafe.ListBackups` now scans `refs/gk/forget-backup/`.** Combined
  with the ref shape change above, `gk timemachine list` surfaces
  forget rewrites alongside undo, wipe, and timemachine entries with no
  caller-side branching.

## [0.31.0] - 2026-05-06

### Added

- **`gk forget` removes paths from the entire git history.** New
  destructive command that delegates to `git filter-repo` for the actual
  rewrite, wrapped with gk-flavour safety:
  - **Auto-detect targets from `.gitignore`.** With no positional args,
    `gk forget` runs `git ls-files -i -c --exclude-standard` to find
    tracked files that are now covered by `.gitignore`, then filters
    those down to entries that actually appear in history. Turns the
    common `echo db/ >> .gitignore && gk forget` workflow into a
    one-line cleanup.
  - **Explicit path mode.** `gk forget db/ secrets.json` skips the
    auto-detect step and feeds the listed paths to filter-repo.
  - **Dual backup before rewriting.** Every branch and tag is mirrored
    to `refs/gk/forget-backup/<unix>/<original-ref>` and to a flat-text
    manifest at `.git/gk/forget-backup-<unix>.txt`. Rollback with
    `git update-ref --stdin < manifest` or pluck a single branch with
    `git update-ref refs/heads/main <backup-sha>`.
  - **Origin URL preserved.** `git filter-repo` deliberately wipes the
    origin remote to make accidental force-pushes harder; gk re-adds it
    after the rewrite so `git push --force-with-lease` works straight
    away. The exact force-push command is printed in the post-run hint.
  - Standard preflight: refuses on dirty trees and mid-rebase/merge,
    requires a TTY confirmation unless `--yes`, supports `--dry-run`.
  - filter-repo is required and not bundled. Missing-binary errors
    surface the install hint up front: `brew install git-filter-repo`
    or `pip install git-filter-repo`. We deliberately do not fall back
    to the deprecated `git filter-branch`.

## [0.30.1] - 2026-05-06

### Fixed

- **`gk update` aborted with "permission denied" when the install dir was
  not user-writable.** When the running binary lived at `/usr/local/bin/gk`
  (the install.sh default), the very first download step tried to create a
  sibling temp file via `os.CreateTemp(install.Dir, ...)` and failed before
  the sudo-escalating rename step ever ran. Stage downloads in the install
  dir only when it is writable for the current user, otherwise stage in
  `os.TempDir()` and let `AtomicReplaceWithSudo` move the file across
  filesystems via `sudo install -m 0755`. Added `update.PickStagingDir` so
  callers do not need to track which path was chosen.

## [0.30.0] - 2026-05-06

### Added

- **`gk update` self-update.** New command that detects how the running
  binary was installed and dispatches accordingly:
  - **brew** (binary lives under `/opt/homebrew`, `/usr/local/Cellar`,
    `/usr/local/Homebrew`, or `/home/linuxbrew/.linuxbrew`) → forwards to
    `brew upgrade x-mesh/tap/gk`.
  - **manual** (anything else, typically `/usr/local/bin/gk` or
    `~/.local/bin/gk` from `install.sh`) → fetches the latest release tag
    from GitHub, downloads `gk_<os>_<arch>.tar.gz` and `checksums.txt`,
    verifies sha256, extracts into a sibling `gk.new`, and renames in
    place. The previous binary is preserved at `<target>.bak`. When the
    install dir is not user-writable, `sudo install -m 0755 …` is invoked
    with stdin/stdout/stderr passed through so the password prompt works.
  - **go-install** (binary lives under `$GOPATH/bin` or `$HOME/go/bin`) →
    prints `go install github.com/x-mesh/gk/cmd/gk@latest` rather than
    overwriting the user's Go-managed bin.

  Flags: `--check` exits 0 when up-to-date or 1 when newer is available
  (no download, suitable for cron/CI gates); `--force` reinstalls even at
  the latest version; `--to vX.Y.Z` pins a specific release for manual
  installs. Honours the global `--dry-run`.

  Tar extraction rejects entries whose basename is not `gk` or that
  contain `..`, so a hostile mirror cannot drop arbitrary files next to
  the running binary. Archive size is capped at 64 MiB and `checksums.txt`
  at 64 KiB.

## [0.29.1] - 2026-05-06

### Fixed

- **`gk commit` and other git-driven commands could fail with "Author identity
  unknown" inside containers and other minimal environments.** The internal
  `ExecRunner.buildCmd` was overwriting the child process environment with
  only the guard variables (`LC_ALL`, `LANG`, `GIT_OPTIONAL_LOCKS`,
  `GIT_TERMINAL_PROMPT`), dropping `HOME`, `USER`, `PATH`, and
  `SSH_AUTH_SOCK`. Without `HOME`, git could not locate `~/.gitconfig`, so
  on hosts where `hostname` is `(none)` (typical for unprivileged
  containers) git fell back to a synthetic identity like
  `user@host.(none)` and aborted the commit. The runner now layers
  `os.Environ()` first, then the guard variables, then any caller-supplied
  `ExtraEnv`, so guard semantics still win for duplicate keys while parent
  state is preserved.

## [0.29.0] - 2026-05-06

### Added

- **POSIX install script.** `curl -fsSL https://raw.githubusercontent.com/x-mesh/gk/main/install.sh | sh`
  auto-detects OS and architecture, downloads the matching archive from the
  latest release, verifies the published `sha256`, and installs the binary
  to `/usr/local/bin` (falling back to `~/.local/bin` when the default is
  not writable). Pin a specific release with `GK_VERSION=v0.29.0` and
  override the install path with `GK_INSTALL_DIR=$HOME/.local/bin`.

### Changed

- **Stable archive URLs.** `.goreleaser.yaml` now produces archives named
  `gk_<os>_<arch>.tar.gz` instead of `gk_<version>_<os>_<arch>.tar.gz`, so
  `https://github.com/x-mesh/gk/releases/latest/download/gk_linux_amd64.tar.gz`
  resolves consistently across releases. Homebrew users see no change
  because goreleaser regenerates the formula from the same template, but
  scripts that hardcoded versioned download URLs need an update. The new
  `install.sh` relies on this naming, so `GK_VERSION` pins only work for
  v0.29.0 and later.
- **README prose pass.** Both `README.md` and `README.ko.md` were rewritten
  for naturalness (denser bullet headers, fewer em-dashes, less AI-toned
  vocabulary) and a misstatement about AI provider transport was corrected:
  `anthropic`, `openai`, `nvidia`, and `groq` call their respective APIs
  directly over HTTP, while `gemini`, `qwen`, and `kiro-cli` are driven as
  external CLI subprocesses.

## [0.28.0] - 2026-05-04

### Added

- **`gk status -v` divergence diagram** — when the current branch is
  ahead/behind its upstream, the rich-mode output now includes a
  small ASCII branch graph showing both rays meeting at the merge
  base. Up to six commits per side are drawn explicitly; counts
  beyond that collapse to a `…` ellipsis. The block is omitted when
  there is no upstream or both counts are zero (`↑0 ↓0` would render
  as two empty rays).

  ```
  ┌─ divergence ────────────────────────────┐
  │    o─o─o   ↑3 you                       │
  │   /                                     │
  │ ──●  merge-base 86d3aac                 │
  │   \                                     │
  │    o─o     ↓2 origin                    │
  └─────────────────────────────────────────┘
  ```

- **`gk status -v` 7-day activity heatmap** — a sparkline + day-of-
  week strip summarising commits over the last seven local days,
  scaled to the busiest day's count. Today is rightmost so the eye
  lands on "now" first; an empty range renders as flat `▁` cells
  with `0 commits`. Fetch-free (`git log` only) so the block adds
  no network cost.

  ```
  ┌─ activity 7d ───────────────────────────┐
  │ ▂ ▅ █ ▄ ▁ ▂ ▂   23 commits              │
  │ T W T F S S M                           │
  └─────────────────────────────────────────┘
  ```

### Internal

- New file `internal/cli/status_richblocks.go` with
  `renderDivergenceDiagram` (uses `git merge-base HEAD <upstream>`
  for the SHA label) and `renderActivityHeatmap` (uses `git log
  --since=7.days.ago --pretty=format:%cd --date=unix`).

## [0.27.0] - 2026-05-04

### Added

- **`gk status` rich density mode** — `gk status -v` (or
  `status.density: rich` in `.gk.yaml`) wraps the branch line and the
  working-tree body in square boxes (`┌─ branch ─┐` / `┌─ working
  tree ─┐`) and appends a highlighted next-action strip with a
  one-line "why" beneath. The next-action selector covers the full
  steady-state matrix — conflicts, dirty + diverged, dirty + behind,
  dirty alone, ahead, behind, diverged, no-upstream, in-sync — and
  emits a single concrete command for each. Rich mode is opt-in: the
  default `gk status` output is unchanged, JSON output is unchanged,
  and `--json` always wins. Verbose-summary diagnostics that used to
  fire on `-v` are now gated behind `-vv` so the visual layer and the
  technical-detail layer stop fighting for the same screen.

### Changed

- **`gk status` always shows the last commit age + SHA**. The
  previous code suppressed the `· last commit Nm/Nh` tail when the
  HEAD commit was under 24 hours old on the assumption that "active
  branches commit multiple times per day, so it's noise". User
  feedback: status is the "current state at a glance" command — the
  exact case where the user just committed is the *most* relevant
  moment to see the SHA and freshness, not the least. The 24h gate
  is removed; `lastCommitAgo` now renders unconditionally.

### Internal

- New helper `internal/cli/status_box.go` (`renderBox`,
  `renderNextActionBlock`) plus `flushRichStatus` /
  `suggestNextAction` / `filterLegacyNextHints` /
  `stripANSIEscapes` in `status.go` for the rich-mode pipeline.
  `StatusConfig.Density` is the new mapstructure key.

## [0.26.0] - 2026-05-04

### Added

- **`gk do`, `gk explain`, `gk ask` — natural-language assist commands**
  built on the existing AI provider plumbing (`nvidia → gemini → qwen
  → kiro-cli`). `gk do "<intent>"` turns Korean/English natural
  language into a vetted git/gk command sequence, dry-runs by default,
  and gates dangerous ops (force push, hard reset, history rewrite)
  behind an extra confirmation prompt. `gk explain "<error>"` parses
  the error text, surfaces likely cause, recovery steps, and a
  prevention tip; `--last` repurposes the helper to walk the user
  through the previous command they ran. `gk ask "<question>"` answers
  git/gk concept questions with concrete examples drawn from the
  current repo state (real branch names, commit shas, file paths).
  Provider resolution mirrors `gk commit`: `--provider` flag → 
  `ai.provider` config → auto-detect. Lives under `internal/aichat/`
  with safety classifiers, repo-context collection, and full unit
  coverage; the CLI surface is `internal/cli/ai_{do,explain,ask}.go`.

### Changed

- **`internal/aichat` cleanup** — dropped two unused `dbg` helpers on
  `ErrorAnalyzer` / `QAEngine` and ran `gofmt -w` over the package so
  `golangci-lint run` is clean.

## [0.25.0] - 2026-05-03

### Changed

- **`gk pull` upstream resolution prefers same-name remote ref over the
  base branch**. When the current branch had no `@{u}` configured, gk
  previously fell straight back to the repo's base branch — so running
  `gk pull` on `develop` silently fetched `origin/main` and the user
  saw an unrelated ref being updated. Now gk first checks whether
  `<remote>/<currentBranch>` exists in the local ref cache; if so,
  that ref is used as the fetch target and stderr suggests
  `git branch --set-upstream-to=<remote>/<branch>`. When neither
  tracking nor a same-name cached ref is available, the fallback to
  the base branch is preserved but stderr now spells out exactly what
  is happening, including the `git fetch && git branch
  --set-upstream-to` command pair to recover.

### Fixed

- **`gk status` raw, locale-leaking error in non-git directory**.
  Running `gk status` outside a repository printed the literal
  porcelain command and git's translated stderr (e.g. `git status
  --porcelain=v2 -z --작업 갈래 (branch): exit code 128: fatal: not
  a git repository`). The error is now caught at the call site and
  rendered as `gk status: git 저장소가 아닙니다` with a hint to run
  `git init` or change directory. Detection lives in a shared
  `isNotAGitRepoError` helper (`internal/cli/errhint.go`) that walks
  the error chain plus `git.ExitError`'s stderr, so other commands
  can adopt the same friendly treatment without duplicating the
  string match.

## [0.24.2] - 2026-05-03

### Fixed

- **`gk commit` secret-gate misreports markdown headings as filenames**.
  When the staged payload included a markdown `### Foo` line (e.g. a
  `### 첫 호출` heading inside a README), the file-boundary parser
  treated it as a new file marker, so finding output rendered as
  `[builtin] generic-secret @ 첫 호출:21 — toke***` instead of
  pointing at the actual source path. The aggregated payload now
  uses a `>>> gk-file <path> <<<` sentinel that cannot collide with
  H3 headings (`internal/secrets.PayloadFileHeader`), and
  `renderFindings` falls back to `(unknown file, payload line N)` if
  the header parser fails. Same sentinel is shared by `gk push`'s
  `scanDiffAdditions` for consistent reporting.

## [0.24.0] - 2026-04-30

### Removed

- **Korean subcommand aliases** (`gk 상태` / `gk 저장` / `gk 갈래` / …).
  Registration ran inside `PersistentPreRunE`, but cobra resolves the
  subcommand name *before* PreRun fires, so the aliases never reached
  the dispatch table — they appeared in docs but always failed with
  `unknown command "상태"`. Dropping the dead code (`internal/easy/
  alias.go` + tests + the `easy.RegisterAliases` call). Easy Mode
  itself is unaffected; only the never-functional alias surface is
  gone.

### Added

- **More Korean Easy Mode hints in `gk status`** — when the working
  tree is otherwise clean, the status footer now surfaces a contextual
  next-step hint based on upstream divergence: `✨ 작업 폴더가
  깨끗합니다` (in sync), `📤 서버에 올릴 커밋이 N개 있습니다 → gk
  push` (ahead), `📥 서버에 새 커밋이 N개 있습니다 → gk pull`
  (behind), `🔀 양쪽에 새 커밋 있음 → gk sync` (diverged). Driven
  off the same `output.hints` knob (`verbose` / `minimal` / `off`).

## [0.23.0] - 2026-04-30

### Added

- **Easy Mode** — opt-in beginner-friendly output layer. Translates a
  curated set of git terminology to Korean equivalents wrapped with the
  English original in parens (`commit` → `변경사항 저장 (commit)`),
  prefixes status sections with emoji (`📋` / `❌` / `💡` / etc.), and
  appends contextual next-step hints from a fallback-chained i18n
  catalog. Off by default. Activation precedence: `--no-easy` flag >
  `--easy` flag > `output.easy` in config > `GK_EASY` env. Disabled
  paths short-circuit before any catalog or term-mapper construction
  so the cold-start cost is a single boolean check.
- **`gk guide [<workflow>]`** — standalone interactive walkthrough of
  common git workflows (init / first commit / push / merge conflict /
  undo). Renders steps with title, description, and run-able command
  in cyan. Independent of Easy Mode — works with any output config.
- **Korean command aliases under Easy Mode** — `gk 상태` / `gk 저장` /
  `gk 올리기` / `gk 가져오기` / `gk 동기화` / `gk 되돌리기` /
  `gk 갈래` / `gk 검사` / `gk 안내`. Registered via cobra's native
  `command.Aliases` field, so the entire subcommand tree (e.g.
  `gk 갈래 list`) resolves through to the original command without
  duplication. English-priority conflict guard refuses to register an
  alias that would shadow an existing English subcommand.
- **`internal/i18n` package** — message catalog with English and
  Korean tables, mode-aware lookup (`ModeEasy` / `ModeMinimal` /
  `ModeOff`), and a fallback chain (requested-lang → en → key
  passthrough). Format-string args propagate via `Getf`.
- **`output.*` config keys** — `output.easy` (bool, default false),
  `output.lang` (BCP-47 short code, default "ko"), `output.emoji`
  (bool, default true), `output.hints` (`verbose` | `minimal` | `off`,
  default `verbose`). Matching env shortcuts: `GK_EASY`, `GK_LANG`,
  `GK_EMOJI`, `GK_HINTS`.
- **`--easy` / `--no-easy` global flags** — per-invocation override
  of the config / env activation. `--no-easy` wins over `--easy` so
  scripts that hardcode disable can survive a globally-enabled config.

### Fixed

- **Easy Mode hint commands no longer get rewritten by term
  translation**. `status.go` and `errhint.go` previously ran
  `TranslateTerms` over already-translated catalog hints, so
  `→ gk commit` rendered as `→ gk 변경사항 저장 (commit)` —
  `\bcommit\b` matched the literal command token in the hint string,
  defeating the very suggestion the hint was supposed to surface.
  Hints now bypass `TranslateTerms`; only raw error text and
  unstructured git output flow through it.
- **`TermMapper.Translate` is idempotent**. The wrapping format
  `<translated> (<term>)` left `<term>` exposed to `\b<term>\b`
  on a second pass because `(` and `)` are non-word characters that
  count as word boundaries; double-applying the function nested the
  parentheticals (`(((commit)))…`). The replacement now uses
  position-aware substitution that skips matches surrounded by parens.
- **Korean aliases no longer reparent the English subcommand tree**.
  `RegisterAliases` previously built a fresh `*cobra.Command` per alias
  and called `aliasCmd.AddCommand(sub)` for every child of the
  original — cobra's `AddCommand` sets `sub.parent = aliasCmd`, which
  silently broke `CommandPath()` and completion for the original
  (running `gk branch list --help` would print the path as
  `gk 갈래 list`). Aliases are now appended to `original.Aliases`,
  the cobra-native pattern that keeps the subtree intact and is
  idempotent on re-registration.
- **Easy Mode error formatter wires emoji**. `errhint.go` previously
  built `ui.NewEasyFormatter(nil, ...)` twice inside a no-op
  conditional, so `FormatError` could never prefix the error / hint
  with `❌` / `💡` — Easy Mode's error output was missing the
  emoji it was advertising. New `Engine.Emoji()` accessor exposes
  the underlying mapper; the dead branch is gone.

### Internal

- **`RegisterAliases` idempotent on re-registration** — safe to call
  multiple times during tests or alternate cobra-tree boots.
- **Lint cleared** — gofmt (alias.go, hints_test.go), staticcheck
  SA5011 (alias_test.go added defensive `return` after `rapid.Fatalf`),
  errcheck (guide.go `bold.Fprintf` / `cyan.Fprintf` returns
  explicitly discarded with a comment documenting the
  best-effort-stdout-write contract).

## [0.22.0] - 2026-04-30

### Added

- **`gk diff`** — terminal-friendly diff viewer with color, line numbers,
  word-level highlights, and an optional interactive file picker
  (`-i`/`--interactive`). Honors `--staged`, `--stat`, `-U <n>`,
  `--no-pager`, `--no-word-diff`, and `--json`. Pager auto-invoked when
  output is a TTY; positional args (`<ref>`, `<ref>..<ref>`, `-- <path>`)
  pass through to `git diff`.
- **`gk diff` "no changes" banner** — when nothing matches the selected
  comparison, gk prints which trees were compared (`(working tree ↔
  index · 기본)`) and probes the *other* side: shows
  `staged 변경 N 파일 — gk diff --staged` when default-mode finds
  nothing but staging has work, or `unstaged 변경 있음 — gk diff` when
  `--staged` is empty but the working tree dirty. Universal alternates
  `gk diff HEAD` and `gk diff <ref>` always rendered.
- **`gk pull --rebase` / `--merge`** — shorthand for `--strategy rebase`
  / `--strategy merge`, and explicit consent for diverged-history pulls
  (see "Changed" below).
- **`gk pull --fetch-only`** — preferred name for fetch-without-integrate;
  `--no-rebase` retained as a deprecated alias.
- **`gk sync --fetch`** — opt-in one-shot: fetch `<remote>/<base>`,
  fast-forward `refs/heads/<base>`, then integrate. Combines the
  network-refresh and rebase-onto-base steps that previously required
  two commands.
- **Backup ref before history-rewriting integrations** — `gk pull
  --rebase` / `--merge` writes `refs/gk/backup/<branch>/<unix-ts>`
  pointing at the pre-integration tip and prunes entries older than
  30 days (preserving the newest 5). `git reset --hard <ref>` restores.
- **Inline conflict region preview in `gk pull` / `gk continue`** —
  paused integrations show the first conflict region with file line
  numbers, side markers (`◀` HEAD / `▶` incoming / `·` context), and
  a one-line summary of remaining regions. The same inline preview
  fires when `gk continue` is invoked while markers are still in the
  working tree.
- **`gk pull` early refusal on paused operations** — invoking `gk pull`
  while a rebase / merge / cherry-pick is in progress now refuses with
  the same banner instead of forwarding into the autostash path (where
  it produced an opaque "could not write index" error from git).
- **`gk resolve` TUI improvements** — line numbers, side labels with
  branch name / commit subject, region progress
  (`region 1/4 · lines 188–200`), and option labels with line counts
  (`ours — keep HEAD (5 lines)`,
  `theirs — accept cd98609 (subject) (5 lines)`). The legacy `-/+`
  diff formatter (`FormatHunkDiff`) stays as a fallback for callers
  without parsed regions.
- **Conflict-recovery banner surfaces `gk resolve`** — `gk pull`,
  `gk continue`, and the in-progress refusal banner now lead with
  `gk resolve` (AI-assisted) and `gk resolve --strategy ours|theirs`
  shortcuts before the manual edit recipe.
- **`gk sync` stale-base hint** — when `refs/heads/<base>` differs
  from `<remote>/<base>`, both `gk sync` and `gk status` surface
  `⚠ local main differs from origin/main (↑N local · ↓M origin)` with
  remediation hints (`git checkout main && gk pull` or
  `gk sync --fetch`).

### Changed

- **`gk sync` integrates against local `<base>` by default**. The
  v0.21 default was `<remote>/<base>` (silent fetch + integrate). Now
  sync is offline-by-default; the user's local base is the integration
  source. `gk sync --fetch` is the explicit one-shot opt-in.
  `--no-fetch` retained as a no-op alias for old scripts.
- **`gk pull` refuses to auto-rebase on diverged histories without
  explicit consent**. Previously the default strategy was `rebase`,
  which silently rewrote local SHAs when local commits hadn't been
  pushed yet. Now divergence triggers a refusal banner listing the
  at-risk local commits and the three resolution paths
  (`--rebase` / `--merge` / `--fetch-only`); explicit `--rebase` /
  `--merge` flags or `pull.strategy` config bypass the gate.
- **`Pull.Strategy` default value is empty** in `Defaults()`. The
  previous `"rebase"` default masked the resolver's `default` source
  signal that the new diverged-refusal logic relies on. The effective
  strategy when nothing is set remains `rebase`.

### Fixed

- **Submodule entries no longer leak into `gk commit` groupings**.
  `parsePorcelainV2` drops every `S<c><m><u>` sub-field record across
  ordinary, rename, and unmerged categories. Submodule pointer commits
  stay deliberate — the user must `git add <path>` them explicitly.
- **`gk pull` works when `@{u}` is set but `origin/HEAD` is not**.
  `runPullCore` now tries the branch's tracking ref first and only
  falls back to `DefaultBranch` detection when no upstream is
  configured. Previously a missing `origin/HEAD` (and no
  `develop`/`main`/`master`) failed with "could not determine default
  branch" even though `git rev-parse @{u}` would have answered.
- **CJK / multibyte labels no longer corrupt the conflict banner**.
  `renderConflictSide` truncated `displayLabel` via byte slicing
  (`displayLabel[:57]`), which split mid-codepoint for Korean /
  Japanese / Chinese / emoji branch names and emitted invalid UTF-8.
  Replaced with a rune-aware truncation; `headerRule` width also
  switched from `len()` to `utf8.RuneCountInString`.
- **AI strategy whitespace tolerated**. `buildResolveOptions` now
  trims `ai.Strategy` before lowering, so `"theirs "` / `" Theirs"`
  no longer silently miss the default-highlight check.
- **`gk sync --no-fetch --fetch` rejected as contradictory** instead
  of silently fetching. Three combinations now error:
  `--fetch-only + --fetch`, `--no-fetch + --fetch`,
  `--no-fetch + --fetch-only`.
- **`gk sync` integration count separates self-FF from base**. The
  summary's `+N commits` line previously absorbed the self-FF delta
  (commits picked up from `origin/<self>`) into the rebase-onto-base
  count. `preHEAD` is now captured after self-FF, and the count uses
  `pre..base` (commits brought in from base) so rebase no longer
  inflates it with rewritten local SHAs.

### Internal

- **`internal/diff` package** — unified-diff parser (round-trippable),
  renderer with word-diff, diffstat, JSON output. ~1700 lines impl +
  ~3600 lines tests (parse / render / format / stat / json / worddiff
  / property).
- **Word-diff LCS DP table bounded** — `wordDiffMaxLineBytes` (4 KB) +
  `wordDiffMaxCells` (1 M cells) prevent OOM on minified-bundle diffs
  that would otherwise allocate gigabytes. `buildSpans` switched from
  per-call `map[int]bool` to a two-pointer walk for zero-alloc span
  construction.
- **Diff scanner cap raised** to 64 MB (was 1 MB), absorbing realistic
  generated lockfiles / minified bundles without falling back to
  raw-byte output.

## [0.21.1] - 2026-04-30

### Fixed

- **릴리스 바이너리에 `-dirty` 마커가 박히던 문제**. v0.21.0이 태그 커밋에서
  깔끔하게 빌드됐는데도 `gk --version` 출력이 `commit <sha>-dirty`로 표시.
  - `.goreleaser.yaml`: `builds[].flags`에 `-buildvcs=false`, `-trimpath`
    추가. goreleaser의 `go mod tidy` before-hook이 빌드 샌드박스의 go.sum을
    일시적으로 변경해 `vcs.modified=true`가 BuildInfo에 임베드되던 경로 차단.
  - `cmd/gk/main.go`: `vcsFallback`이 ldflags로 채워진 commit에도 BuildInfo의
    `vcs.modified`를 보고 `-dirty`를 붙이던 가드 결함 수정.
    `vcsFallbackFromSettings`로 순수 함수 분리 + `fromVCS` bool 가드 추가 —
    `vcs.modified`는 같은 호출에서 `vcs.revision`으로 commit을 채운 경우에만
    적용.
  - 단위 테스트 6건 (`cmd/gk/main_test.go`)으로 v0.21.0 회귀 시나리오 + ldflags
    precedence + plain `go build`의 dirty 마킹을 모두 커버.

## [0.21.0] - 2026-04-30

### Added

- **`gk status` — base 출처 라벨**. `from <base>` 라인에 `default` /
  `configured` / `guessed` 라벨이 붙어 base 브랜치가 어디서 결정됐는지 한눈에
  보입니다. 내부 source 상수(`origin/HEAD`, `git config`, `.gk.yaml`,
  `GK_BASE_BRANCH`, `fallback`)는 그대로 유지되며, `-v` /
  `--explain-base`에서 기술 라벨로 노출됩니다.
- **`gk status --explain-base`** — base 결정 근거 다층 진단 블록. 모든 config
  layer + 캐시된 `origin/HEAD` + (옵션) live origin + 로컬 fallback 후보를
  나열하고 채택된 행에 ✓ 마커, 불일치 시 action hint를 표시합니다.
- **`gk status --explain-base --fetch-default`** — `git ls-remote --symref`
  한 번 호출로 라이브 origin/HEAD를 조회해 캐시본과 비교합니다.
  `SSH_ASKPASS=` / `GCM_INTERACTIVE=never`로 강화된 runner에서 실행돼 인증
  다이얼로그로 status가 멈추지 않습니다.
- **Base mismatch footer** — `cfg.BaseBranch`(.gk.yaml/git config/env)가
  캐시된 `origin/HEAD`와 다르면 `⚠ base 'X' (configured) ≠ origin default
  'Y'`와 `git remote set-head origin -a` 힌트가 출력됩니다.
- **Tracking mismatch footer** — `branch.<name>.merge`가 `refs/heads/Y`를
  가리키는데 로컬 이름이 `X`면 `⚠ tracking mismatch: local 'X' pushes to
  'origin/Y'` 경고와 `git branch --set-upstream-to=…` / `git push -u …` fix
  힌트, 그리고 per-branch 억제 방법을 함께 표시합니다.
- **`branch.<name>.gk-tracking-ok=true`** — triangular workflow / personal
  fork 등 트래킹 비대칭이 의도된 경우 per-branch로 tracking warning을 끕니다.
  대소문자 구분 없음 (`true`/`True`/`TRUE`).
- **`gk status -v`에 `[base]` 진단 라인** — `resolved=… source=…
  origin/HEAD=… cfg=…` key=value 한 줄. 미스매치 / origin/HEAD unset 시 ⚠
  꼬리표가 붙습니다.

### Changed

- **`gk status` base 해석을 단일 호출로 hoist**. 이전에는 `runStatusOnce`가
  `resolveBaseForStatus`를 최대 3회 호출하던 것을 `BaseResolution`을 1회 계산
  후 `renderBaseDivergence` / `renderStatusVerboseSummary`에 인자로 전달하도록
  refactor. 매 status 4-10개 git subprocess가 줄었습니다.
- **Tracking 검출이 단일 `git config --get-regexp`로 통합**. 이전에는 3개
  별도 lookup(`gk-tracking-ok`, `merge`, `remote`)이었으나 1회 spawn으로
  줄였습니다.
- **`--legend` "base" 섹션** — 새 라벨 어휘(`default` / `configured` /
  `guessed`)와 mismatch footer 설명을 반영합니다.

## [0.20.0] - 2026-04-29

### Added

- **`gk status --json`** — 머신 판독용 JSON 출력. `repo`/`branch`/`upstream`/
  `ahead`/`behind`/`clean`/`next` 헤더, `counts`(committable/split/staged/
  modified/untracked/conflicts/dirty_submodules), `entries[]`, `submodules[]`.
  모든 사람-가독 문자열은 `stripControlChars`로 sanitize됩니다.
- **`gk status --exit-code`** — 셸 스크립트용 종료 코드: 0=clean, 1=dirty,
  2=submodule-only, 3=conflicts, 4=behind. 우선순위는 conflict > dirty >
  submodule-only > behind > clean. `--watch`와 동시 사용은 거부됩니다.
- **`gk status --watch [--watch-interval D]`** — 인터럽트 전까지 N초 간격으로
  상태를 갱신. 기본 2s. `--json`/`--exit-code`와 충돌 시 거부.
- **서브모듈 worktree-only dirtiness 분류 (`KindSubmodule`).** porcelain v2의
  `.M S.M.` / `.M S..U` 레코드(superproject `git add`로 commit 불가능한 nested
  변경)를 감지해 별도 카테고리로 표시합니다. `gk commit`도 분류 결과에서
  drop합니다. `IsSubmoduleWorktreeDirtinessOnly` 헬퍼는 `internal/git`에서
  export되며 `internal/aicommit/gather.go`도 이를 호출합니다.

### Changed

- **`compactUpstreamSuffix`가 항상 `<remote>/<branch>` 전체를 표시.** 이전에는
  로컬 브랜치 이름과 upstream 브랜치 이름이 일치하면 `→ origin`으로 줄였으나,
  `main → origin` 같은 모호한 출력을 막기 위해 dedup 로직을 제거했습니다.
- **`StatusEntry`에 `Sub` 필드 추가.** porcelain v2의 submodule 필드(`N...` /
  `S.M.` 등)를 보존합니다. `parseRenamedEntry`/`parseUnmergedEntry`도
  `Sub`를 읽어 rename·unmerged 서브모듈도 `KindSubmodule`로 분류합니다.
- **`renderSubmoduleSection` 시그니처에서 `ctx`가 첫 인자로 이동.** Go convention
  준수.
- **`runStatus`의 `os.Exit` 호출이 `statusExitFunc` 인디렉션으로 분리됨.**
  테스트에서 종료 코드를 검증할 수 있도록.

## [0.19.0] - 2026-04-29

### Fixed

- **Rename groupings now stay in a single commit.** `gk commit`이 staged
  rename(`git mv` 등)을 처리할 때, AI grouper가 새 경로만 그룹에 emit하면
  원본 삭제 측이 `git commit -- <pathspec>`에 포함되지 않아 인덱스에
  dangling staged deletion으로 남던 버그를 수정. `ApplyMessages`는 이제
  commit 루프 진입 전 `git diff --cached --name-status -z -M`로 staged
  rename pair(`new → orig`)를 한 번 수집하고, 각 그룹의 commit pathspec을
  expand해 원본 삭제 측 경로를 함께 커밋합니다. 새 헬퍼는
  `internal/aicommit/apply.go`의 `stagedRenamePairs`/`expandRenamePairs`.

### Changed

- **AI 분류 prompt와 Gemini diff 헤더에 rename 원본 경로가 노출됩니다.**
  `provider.FileChange`에 `OrigPath` 필드 추가 — classifier prompt는
  `- new.go [renamed from old.go]`, diff 헤더는
  `--- new.go (renamed from old.go)`로 출력. LLM이 rename을 delete+add
  페어로 오해해 그룹을 분리하는 빈도를 줄이는 것이 목적입니다.

## [0.18.0] - 2026-04-29

### Added

- **브랜치별 fork-parent 메타데이터 (`gk branch set-parent`/`unset-parent`).**
  Stacked workflow 사용자가 `git config branch.<name>.gk-parent <parent>`로
  실제 부모 브랜치를 등록하면, `gk status`가 main 대신 parent 기준으로
  ahead/behind를 출력합니다 (`from feat/parent ↑2 ↓0 → ready to merge into feat/parent`).
  - Write-time 검증: self/cycle (depth ≤10)/non-branch/tag/존재 안 함 모두 거부.
    오타는 Levenshtein 기반 fuzzy 제안 ("did you mean 'main'?").
    Remote-tracking ref 거부는 실제 `git remote` 목록 기반 — 휴리스틱 아님.
  - parent 가리키는 브랜치가 삭제된 경우 stderr에 1-line 경고 후 base로
    silent fallback — status 출력 자체는 base 라인으로 유지됩니다.
  - 신규 패키지 `internal/branchparent/`. Phase 1은 storage + status 통합만;
    추론 알고리즘 (reflog 기반 자동 parent 감지) 및 `gk switch`/`gk worktree`
    parent 인지는 Phase 2 예정. sync/merge/ship은 의도적으로 제외 — 변경
    명령에는 명시적 `--base` 인자가 더 안전합니다.
- **`gk status`의 `base` 시각화 레이어 기본 활성화.** 이전에는 `--vis base`로
  opt-in해야 했던 `from <trunk> ↑N ↓M [hint]` 라인이 기본 출력. 액션 힌트도
  추가됐습니다 — `→ ready to merge into main` (ahead-only, clean tree),
  `→ behind main: gk sync` (behind-only), `→ main moved: gk sync` (diverged).
  - **Perf 영향:** 일반 사용자의 `gk status` baseline이 약 +6-12ms 증가합니다
    (`git rev-list --left-right` 1회 + `git config --get` 1회 추가 spawn).
    parent metadata가 설정된 브랜치에서는 추가로 `git rev-parse --verify` 1회
    더 호출됩니다 (~+1-2ms). 기존 ≤10ms budget을 약간 넘기지만, 머지 판단
    신호의 가시성 향상이 비용을 정당화합니다. opt-out하려면 `.gk.yaml`의
    `status.vis`에서 `base`를 제외하세요.

### Changed (BREAKING)

- **`gk sync`가 "현재 브랜치를 base로 따라잡기"로 재정의됨.** 기본 전략은 rebase.
  v0.6의 `gk sync`는 "fetch + 현재 브랜치를 `origin/<self>`로 FF"였는데, 이는
  사용자가 가장 흔히 원하는 인텐트(피처 브랜치를 trunk로 따라잡기)와 어긋나
  있었습니다. 재설계로 gk의 통합 커맨드 3개가 서로 겹치지 않게 정리됩니다:
  `sync`(base → 현재, 기본 rebase), `pull`(`@{u}` ↔ 현재), `merge <x>`(머지
  커밋을 동반한 의도적 통합).
  - 신규 플래그: `--base`, `--strategy rebase|merge|ff-only`.
  - `.gk.yaml`의 `sync.strategy`는 신규 키 — `pull.strategy`와 분리.
  - **Self-FF (always-on):** `origin/<self>`가 로컬보다 strictly ahead일 때,
    base 통합 전에 자동 FF. diverge 시 조용히 스킵.
  - **`--upstream-only` (deprecated, v0.8 제거):** v0.6 동작을 한 사이클
    유지. stderr에 한 줄 deprecation 안내. CI 로그용 무음화는
    `GK_SUPPRESS_DEPRECATION=1`. v0.8 이후엔 `gk pull`을 사용.
  - **`--all` 제거.** 모든 로컬 브랜치를 base로 rebase하는 동작은 위험하고
    드물게 의도된 것이라 제거. 필요하면 shell 루프로 수동 처리.
  - 충돌 처리는 동일 — `gk continue` / `gk abort` / `gk resolve`로 재개.
  - 자세한 내용은 `docs/commands.md#gk-sync` 및 `docs/rfc-sync-redesign.md`.

## [0.15.0] - 2026-04-28

### Added

- **`gk ship` release automation.** 새 명령은 `status`, `dry-run`, `squash`, `auto`, `patch|minor|major` 모드를 지원하고, clean/base-branch 확인, 최신 tag 기준 SemVer bump 추론(`feat` → minor, breaking → major, 그 외 patch), local-only squash, configured preflight 실행, `VERSION`/`package.json`/`marketplace.json` version bump, `CHANGELOG.md [Unreleased]` 승격, release commit, annotated tag 생성, branch/tag push까지 묶습니다. `v*` tag push는 기존 GitHub Actions release workflow를 트리거하므로 GoReleaser 기반 GitHub Release/Homebrew tap 배포까지 이어집니다.
- **`gk merge <target>` AI-planned guarded merge.** 실제 `git merge` 전에 `merge-tree` 기반 precheck를 실행하고 AI-assisted merge plan을 기본 출력합니다. Provider가 없으면 동일 git facts 기반 fallback plan을 출력합니다. 충돌이 예측되면 plan을 보여주고 merge를 차단하며, `--plan-only`, `--no-ai`, `--provider`, `--ff-only`, `--no-ff`, `--no-commit`, `--squash`, `--skip-precheck`, `--autostash`를 지원합니다.

## [0.14.1] - 2026-04-27

### Internal

- **Dead code 제거.** `internal/cli/init.go`의 미사용 `//go:embed templates/ai/{CLAUDE,AGENTS,kiro-*}.md` directive 16줄, `internal/initx/aictx.go`의 미사용 `claudeMDTemplate` / `agentsMDTemplate` raw string 변수 약 160줄 (`kiro*Template` 3종은 v0.13.0의 `gk init --kiro`에서 사용 중이므로 유지), `internal/cli/log.go`의 미사용 `must` 제네릭 헬퍼, `internal/cli/status.go`의 미사용 `colorXY` 한 줄 함수가 모두 v0.13.0 redesign 이후 호출처가 사라진 dead code였습니다. 외부 동작에 영향 없음.
- **`golangci-lint --fix` 적용.** `staticcheck QF1001`(De Morgan 단순화)을 `internal/cli/log.go:resolveLogVis`, `internal/cli/status.go:454`, `internal/cli/ai_commit_test.go:64`에 적용 (semantic equivalent). gofmt 정렬을 `internal/aicommit/privacy_gate{,_test}.go`, `internal/ai/provider/{groq,nvidia,fallback_test,summarizer_test}.go`, `internal/cli/{log,status,worktree,ai_review,init,ai_commit_test,ai_changelog_test,ai_pr_test,status_test}.go`, `internal/initx/{aictx,configgen,writer,writer_test,analyzer_test}.go`, `internal/policy/policy_test.go`에 일괄 복원 — 이전 formatter run으로 드리프트했던 struct field 주석 정렬을 canonical 형태로 통일.

### Tooling

- **`/release` skill을 defaults-first single-gate 흐름으로 재작성** (`.claude/skills/release/SKILL.md`). Phase 1-6 (PREFLIGHT / PROPOSE / CONFIRM / EXECUTE / VERIFY / REPORT) 구조로 정리하고, 이전에 4번 호출되던 `AskUserQuestion`(release 전략 / 버전 / CHANGELOG / 커밋 구조)을 Phase 3 단일 게이트로 통합. 버전 bump · CHANGELOG 본문 · 커밋 구조를 working tree와 `[Unreleased]` 상태에서 자동 추론하고 사용자는 한 번만 확정합니다. 또한 `golangci-lint`를 hard preflight requirement로 추가. binary에는 포함되지 않는 개발 도구 변경입니다.

## [0.14.0] - 2026-04-27

### Changed

- **`gk ai <subcommand>`가 `gk <subcommand>`로 평탄화되었습니다 (breaking).** `commit`, `pr`, `review`, `changelog`이 root command에 직접 위치합니다 — `gk commit`, `gk pr`, `gk review`, `gk changelog`. 4개 명령은 non-AI counterpart가 없어 namespacing 이득이 없었고 `ai` 글자의 마찰만 남았기 때문입니다. `--show-prompt` flag도 root persistent flag로 이동되어 모든 상위 명령에서 redacted-payload audit를 그대로 사용 가능합니다. 에러 메시지 prefix도 개정되었습니다 (`"ai commit: ..."` → `"commit: ..."`). `README.md`, `README.ko.md`, `docs/commands.md`, `docs/config.md` 모두 새 명령 형태로 갱신되었습니다.

### Removed

- **`gk ai` parent command 및 `AICmd()` exported accessor.** alias는 제공하지 않습니다 — `gk ai commit` 등을 쓰던 스크립트/CI는 새 top-level 형태로 수정해야 합니다. rename은 mechanical하므로 sed 수준 교체(`gk ai ` → `gk `)로 충분합니다.

## [0.13.1] - 2026-04-26

### Fixed

- **Secret-gate false positives on `generic-secret`.** The catch-all `key/secret/token=...` regex was firing on obvious placeholders in checked-in samples and templates. The scan now skips lines containing `your_`, `your-`, `<your`, `example`, `placeholder`, `xxx`, `changeme`, `replace_me`, `todo`, `fixme`, `dummy`, `sample`, `test_key`, `test_secret`, `fake_key`, or `fake_secret`. Real-key patterns (AKIA, ghp_, sk-…) are unaffected — they ride dedicated kinds, not `generic-secret`.
- **`gk ai commit` aborting on test fixtures.** The `isTestFile` check used by the secret gate now recognizes `_test.rs`, `_test.py`, `_spec.rb`, `*.test.tsx`, `*.test.jsx`, plus any path under `testdata/`, `tests/`, `__tests__/`, `fixtures/`, or `test_fixtures/`. Files whose basename contains `test`, `mock`, `fake`, `fixture`, `example`, `redact`, `sample`, `stub`, or `dummy` are also treated as fixtures. Mock data and redaction examples no longer block commit runs.

### Changed

- **`gk init` default IDE gitignore patterns include `.claude/`** alongside `.idea/`, `.vscode/`, `.cursor/`, `.kiro/`, `.xm/`, `.omc/`. New repos scaffolded with `gk init` won't accidentally check in their per-IDE Claude Code settings.

### Docs

- **Linux manual-download instructions** added to both `README.md` and `README.ko.md`. Homebrew remains the recommended path on macOS, but Linux users now have a copy-pasteable curl-and-tar one-liner (amd64 + arm64) plus a manual three-step fallback.
- **`README.ko.md` synced with v0.13.0.** Adds the Groq provider row, updates the auto-detect order to `nvidia → groq → gemini → qwen → kiro-cli`, and lists the `ai.groq:` block in the example `.gk.yaml`. The `--provider` flag enumeration is also brought into line.
- **`/release` skill (`.claude/skills/release/SKILL.md`) auto-syncs README + docs/commands.md by default** when the CHANGELOG promotion exposes a missing command or flag. The skill drafts entries from structured sources (`gk <cmd> --help`, the promoted CHANGELOG section, Cobra `Use`/`Short`/`Long` strings, recent commits) and surfaces the diff for review before the release commit. The previous "ask first, never auto-generate prose" rule is replaced with transcription guidance — match flag descriptions to `--help`, mark uncertainty with `<!-- review: ... -->` instead of guessing, and never invent flags that have no source backing. Auto-drafting stays scoped to structured surface; tutorials and rationale narratives still belong to a human editor.

## [0.13.0] - 2026-04-26

### Added

- **`gk init` redesigned as a one-shot project bootstrap.** Running `gk init` now analyzes the repository (language stack, frameworks, build tools, CI configs) and scaffolds three artifacts in a single pass: a `.gitignore` baseline (language/IDE/security rules, optionally augmented by AI-suggested project-specific patterns via the new `GitignoreSuggester` capability), a repo-local `.gk.yaml` with sensible defaults including the `ai.commit.deny_paths` baseline, and (with `--kiro`) `.kiro/steering/{product,tech,structure}.md` for Kiro-compatible assistants. An interactive [huh](https://github.com/charmbracelet/huh) form previews the analysis result and the planned writes before anything touches the filesystem; non-TTY callers (CI, piped output) fall back automatically. Use `--only gitignore|config|ai` to run a single target, `--dry-run` to preview, `--force` to overwrite. `CLAUDE.md` and `AGENTS.md` are no longer scaffolded — Claude Code and Jules generate (and continually refresh) their own context files, so a static template would be stale before its first commit.
- **`internal/initx` package** — `analyzer.go` (filesystem-driven detection of language stack / frameworks / build tools / CI configs), `configgen.go` (`.gk.yaml` rendering from `AnalysisResult`), `gitignore.go` (language/IDE/security baseline), `ai_gitignore.go` (provider-suggested augmentation), `aictx.go` (Kiro steering files), and `writer.go` (atomic write with skip-if-exists semantics). Each module is independently testable and consumed by `gk init`.
- **`gk config init`** — relocated `gk init config` under the canonical `config` namespace. Same flags (`--force`, `--out <path>`), same auto-init behavior on first `gk` run. `gk init config` is preserved as a backward-compatible alias and now delegates to this command.
- **Groq AI provider** (`internal/ai/provider/groq.go`) — HTTP provider talking to the Groq Chat Completions API (OpenAI-compatible). Reads `GROQ_API_KEY` from the environment; default model `llama-3.3-70b-versatile`. Slotted into the auto-detect order **after** `nvidia` and **before** the CLI-shelling providers: `nvidia → groq → gemini → qwen → kiro-cli`. Implements `Classifier`, `Summarizer`, and `GitignoreSuggester` capabilities by sharing the HTTP invoke path with `Nvidia`.
- **`GitignoreSuggester` optional capability** (`internal/ai/provider/gitignore.go`) — providers can suggest project-specific `.gitignore` patterns from a filesystem snapshot. Implemented for `nvidia`, `groq`, `gemini`, `qwen`, and `kiro`. The system prompt is conservative — only patterns that are NOT already covered by the standard language/IDE/security baseline. Detected via type assertion, mirroring the `Summarizer` pattern, so providers without the capability are skipped silently.

### Changed

- **Secret-gate findings now carry the originating file path and a file-relative line number** for built-in scanner hits. The aggregated diff payload is parsed for `### path` and `diff --git a/X b/X` headers and each builtin finding is mapped back to its file. Brings parity with the `gitleaks` adapter, which already reported per-file location. Output is now navigable when the gate aborts a `gk ai commit` run.
- **Auto-detect provider order** is now `nvidia → groq → gemini → qwen → kiro-cli` (was `nvidia → gemini → qwen → kiro-cli`). HTTP providers come first because they have no install-time prerequisites beyond an environment variable.
- **`AIConfig` gains an `AIGroqConfig` block** (`model`, `endpoint`, `timeout`) parallel to `AINvidiaConfig`. Default timeout is 60s; defaults are written into `Defaults()` so the field is always present even when the user has not configured it.
- **README provider table and config snippets** now list `groq` alongside `nvidia` as a no-binary HTTP option, with the corresponding `ai.groq:` block in the example `.gk.yaml`.

### Internal

- The `gk init ai` subcommand survives as a hidden alias for backward compatibility, but no longer emits `CLAUDE.md` / `AGENTS.md` — those files are now self-managed by the assistants themselves.
- `init_config.go` is reduced to a one-line backward-compat shim (`var runInitConfig = runConfigInit`) so existing tests continue to compile.

## [0.12.0] - 2026-04-26

### Added

- **`gk ai pr`** — generate a structured PR description (Summary, Changes, Risk Assessment, Test Plan) from the commits on the current branch. `--output clipboard` copies the result directly via the platform clipboard; `--dry-run` previews the redacted prompt without invoking the provider; `--lang` controls the output language. Pulls the same provider/privacy-gate plumbing as `gk ai commit` so secrets and `deny_paths` matches never leave the machine.
- **`gk ai review`** — AI-powered code review on the staged diff (`git diff --cached`) or an arbitrary range (`--range ref1..ref2`). Returns a per-file finding list with severity (`error` / `warn` / `info`), a one-line rationale, and an optional fix suggestion. `--format json` emits NDJSON for CI consumption; the default human format groups findings under their file headers.
- **`gk ai changelog`** — generate a Keep-a-Changelog-style block grouped by Conventional Commit type from a commit range. Defaults to `<latest-tag>..HEAD`; override via `--from` / `--to`. Useful for drafting release notes — the output is meant as a starting point for human editing, not the final word.
- **NVIDIA provider** (`internal/ai/provider/nvidia.go`) — first-class HTTP provider that calls the NVIDIA Chat Completions API directly. No external binary required; reads `NVIDIA_API_KEY` from the environment. Now the **default** in the auto-detect chain (`nvidia → gemini → qwen → kiro-cli`), so a fresh install with the API key set works out of the box. Implements both `Classifier` and the new `Summarizer` capability.
- **Privacy Gate for remote providers.** Every payload routed to a `Locality=remote` provider passes through the gate, which redacts `internal/secrets` matches and `deny_paths` glob hits with tokenized placeholders (`[SECRET_1]`, `[PATH_1]`) before the prompt leaves the machine. Aborts when more than 10 secrets are detected (signal that something is fundamentally wrong). Use the new global `--show-prompt` flag on any `gk ai` subcommand to inspect the exact redacted payload that would be sent.
- **Provider Fallback Chain.** When no explicit `--provider` is given, gk tries each available provider in auto-detect order and moves to the next on failure (network error, missing API key, CLI not installed, exhausted quota). The chain is short-circuited only by user-cancelable errors (e.g. user denies the privacy-gate confirmation). Restored after the v0.11.x revert; `internal/ai/provider/fallback.go` is now covered by dedicated tests.
- **Summarizer capability.** Providers that opt in (currently only `nvidia`) can pre-summarize oversized diffs before classification, so very large working trees no longer overflow the model's context window. Other providers will gain support in future releases.
- **`--show-prompt`** — global flag on the `gk ai` command tree. Prints the exact (privacy-gate-redacted) payload that would be sent to the provider and exits without making the network call. Useful for auditing what gk is about to share and for debugging prompt regressions.

### Changed

- **`gk ai commit` classifier prompt prefers fewer groups.** The system instruction now explicitly tells the classifier to keep related changes (implementation + its config + its docs) in a single group and to split only when files serve clearly different purposes. Reduces the rate of overzealous splits where a single coherent change was sliced into 3-4 noise commits.
- **Secret scan skips test files.** `summariseForSecretScan` now ignores files matching `_test.go`, `*.test.ts`, `*.test.js`, `*.spec.ts`, `*.spec.js`. Unit tests for the scanner itself contain intentional fake secrets (e.g. `AKIA…` strings as test fixtures), and the previous behavior aborted `gk ai commit` whenever those files appeared in the working tree. The files are still passed to the AI classifier — only the gate skips them.
- **`gk doctor` now reports an `nvidia` provider row** alongside `gemini`, `qwen`, and `kiro-cli`. Detects whether `NVIDIA_API_KEY` is set in the environment and surfaces a one-line auth hint when it is not.

### Performance

- **AI provider call path tightened.** `internal/ai/provider/httpclient.go` consolidates request construction and response parsing for HTTP-backed providers (currently nvidia), trimming a hot allocation per call. CLI-shelling providers (`gemini`, `qwen`, `kiro`) had their `runner` factored out so subprocess spawn + stdin pipe + stdout drain reuse a single `runner.Exec` path instead of duplicating boilerplate per provider.

### Fixed

- **Privacy gate now applies to all remote providers**, not just `gk ai commit`. Earlier, `gk ai pr` / `gk ai review` / `gk ai changelog` could route raw diffs straight to a remote model on certain code paths. Every `gk ai` subcommand now goes through the same gate.

### Internal

- `internal/ai/provider/factory.go` — provider construction unified behind a single factory; covers nvidia, gemini, qwen, kiro, fake, and the fallback wrapper.
- `internal/aicommit/privacy_gate.go` — extracted from `ai_commit.go` so the gate is shared by every `gk ai` subcommand.
- Test coverage: new tests for `factory`, `fallback`, `httpclient`, `nvidia`, `summarizer`, `privacy_gate`, `ai_changelog`, `ai_pr`, `ai_review`, and a top-level `ai_integration_test.go` that wires a fake provider through the full `commit/pr/review/changelog` paths.
- `gopkg.in/yaml.v3` and related dependencies vendored via `go.mod`; `Makefile` gains a property-based-test build target.
- Repo-local `.gk.yaml` — ships an explicit `ai.commit.deny_paths` baseline (`.env*`, `*.pem`, `id_rsa*`, `credentials.json`, `*.pfx`, `*.kdbx`, `*.keystore`, `service-account*.json`, `terraform.tfstate*`) so the gate has a sensible default even before users edit their config.

## [0.11.0] - 2026-04-23

### Added

- **Global `-d, --debug` flag (and `GK_DEBUG=1` env var).** Every subcommand gains a diagnostic log channel to stderr, rendered in dim gray so the stream visually recedes behind real command output. Lines are tagged with `[debug +N.NNNs]` showing elapsed time since the first debug call, so wall time attribution is immediate — e.g. `[debug +0.042s] ai commit: classify ok — 3 groups` vs `[debug +2.815s] ai commit: compose ok — 3 message(s) in 2.773s` tells you the model call is the hot path. Root-level `PersistentPreRunE` installs two subprocess hooks (`git.ExecHook` and `provider.ExecHook`) on every invocation, so every git command and every AI CLI call is logged with its argv, duration, and exit status — no per-command opt-in. Stage boundaries are annotated in `pull` (base/upstream/strategy resolution, dirty check, ff-optimization), `push` (protected/secret-scan/argv), `clone` (spec→URL→target), `worktree add` (raw→resolved→managed layout), and `ai commit` (provider/preflight/gather/gate/classify/compose).
- **Spinner feedback for long stages in `gk ai commit`.** Previously the command sat silently while the classifier or composer waited on an external AI CLI. Now each stage (secret-gate scan, classify, compose) prints a status line and starts a 150ms-delayed braille spinner on stderr, reusing the pattern from `gk status`'s quiet fetch. Non-TTY stderr (CI, piped output) stays clean — the status lines remain but the animation is suppressed. Spinner code lives in `internal/ui/spinner.go` and is available for future long-running commands.

## [0.10.0] - 2026-04-23

### Added

- **`gk init config` + first-run auto-init.** A fully-commented YAML template now lands at `$XDG_CONFIG_HOME/gk/config.yaml` (fallback `~/.config/gk/config.yaml`) the first time any `gk` command runs, so users have a single, discoverable file to edit instead of guessing field names from `gk config show`. The auto-init prints one `gk: created default config at <path>` line to stderr on creation and is silent on every subsequent run. Explicit `gk init config [--force] [--out <path>]` is the discoverable counterpart — regenerate the template, write a repo-local `.gk.yaml`, or opt into `--force` for a clean reset. Disable the auto-init entirely with `GK_NO_AUTO_CONFIG=1`; write failures (read-only home, sandbox, bad XDG path) are swallowed so gk always runs. Template covers every supported section including the new `ai:` block.
- **`gk ai commit`** — cluster working-tree changes (staged + unstaged + untracked) into semantic commit groups via an external AI CLI (`gemini`, `qwen`, `kiro-cli`) and apply one Conventional Commit per group. Provider resolves via `--provider` → `ai.provider` in config → auto-detect (`gemini → qwen → kiro-cli`); each adapter calls the CLI over stdin (`-p` / positional / `--no-interactive`) so no LLM API keys live inside `gk`. Interactive TUI review by default, `-f/--force` skips review, `--dry-run` previews only, `--abort` restores HEAD to the latest `refs/gk/ai-commit-backup/<branch>/<unix>` ref. Safety rails run on every invocation: `internal/secrets` + `gitleaks` (when installed) gate every payload and abort on findings; `deny_paths` globs keep `.env*`, `*.pem`, `id_rsa*`, `credentials.json`, `*.kdbx`, lockfiles, and `terraform.tfstate` out of provider prompts; `gitstate.Detect` refuses to run mid-rebase / merge / cherry-pick; `commit.gpgsign=true` without a `user.signingkey` aborts before the LLM is ever invoked; a path-based classifier (`_test.go`, `docs/*.md`, CI yamls, lockfiles) overrides the provider's type pick to prevent "test classified as feat" hallucinations; and every generated message is validated with `internal/commitlint.Lint` with up to two retries threading the lint issues back into the prompt. Provider/version recording via `AI-Assisted-By` trailer and `.git/gk-ai-commit/audit.jsonl` logging are both opt-in (`ai.commit.trailer` / `ai.commit.audit`, default off). Flags: `-f/--force`, `--dry-run`, `--provider`, `--lang`, `--staged-only`, `--include-unstaged`, `--allow-secret-kind`, `--abort`, `--ci`, `-y/--yes`. `gk doctor` now reports a row per provider (install + auth hint) and explicitly distinguishes the `kiro-cli` headless binary from the `kiro` IDE launcher.

## [0.9.0] - 2026-04-23

### Added

- **`gk wt` interactive TUI.** Running `gk wt` (or `gk worktree`) without a subcommand opens a loop over the worktree list with actions for cd / remove / add-new.
  - **cd** spawns a fresh `$SHELL` inside the selected worktree (like `nix-shell`) — type `exit` to return to the original shell at its original cwd. Inside the subshell `$GK_WT` and `$GK_WT_PARENT_PWD` expose the path contract. Pass `--print-path` to opt into the shell-alias pattern instead: `gwt() { local p="$(gk wt --print-path)"; [ -n "$p" ] && cd "$p"; }`.
  - **remove** understands dirty/locked/stale states: dirty/locked worktrees get a follow-up "force-remove anyway?" prompt; stale admin entries auto-prune; after a clean remove gk offers to delete the orphan branch.
  - **add new** resolves orphan-branch collisions inline with a three-way choice (reuse / delete-and-recreate / cancel), so a prior failed `worktree add -b` no longer leaves users locked out.
  - Non-TTY callers get the usual help output.
- **`gk worktree add` managed base directory.** Relative name arguments now land under `<worktree.base>/<worktree.project>/<name>` (default `~/.gk/worktree/<basename>/<name>`) instead of the caller's cwd. Absolute paths still passthrough. Two clones with the same basename (e.g. `work/gk` and `personal/gk`) can disambiguate via `worktree.project` in `.gk.yaml`. Intermediate directories are created automatically; subdir names like `feat/api` are preserved under the managed root.
- **`gk status --xy-style labels|glyphs|raw`** — per-entry state column is now self-documenting by default. The cryptic two-letter porcelain code (`??`, `.M`, `MM`, `UU`) is replaced with word labels (`new`, `mod`, `staged`, `conflict`) on every row. Pass `--xy-style glyphs` for a compact one-cell marker (`+` `~` `●` `⚔` `#`), or `--xy-style raw` / `status.xy_style: raw` to restore the previous git-literate rendering. Glyph mode collapses states into five broad categories for dashboard density; label mode preserves per-action granularity. Also fixes a latent bug where `DD`/`AA` unmerged conflicts were colored yellow instead of red.
- **`gk pull` post-integration summary.** Previously `gk pull` ended with a terse `integrating origin/main (ff-only)...` line even when it pulled in a dozen commits — the user had to run `git log` separately to see what actually changed. The new summary prints the pre/post HEAD range, commit count, a one-line listing of each new commit (SHA, subject, author, short age; capped at 10 with a `+N more` footer), and a `--shortstat` diff summary. When nothing changed, a single `already up to date at <sha>` line confirms HEAD. `gk pull --no-rebase` (fetch-only) now reports how many upstream commits are waiting and whether HEAD has diverged, replacing the opaque `done (fetch only)` message.
- **`gk clone <owner/repo | alias:owner/repo | url> [target]`** — short-form URL expansion for cloning. Bare `owner/repo` expands to `git@github.com:owner/repo.git` (SSH by default; configurable via `clone.default_protocol`/`clone.default_host`). `--ssh`/`--https` flip protocol for a single invocation. Scheme URLs (`https://`, `ssh://`, `git://`, `file://`) and SCP-style `user@host:path` strings pass through unchanged. New config:
  - `clone.hosts` — alias table so `gk clone gl:group/svc` resolves to `git@gitlab.com:group/svc.git` (per-alias `host` + optional `protocol`).
  - `clone.root` — opt-in Go-style layout; when set, bare `owner/repo` lands at `<root>/<host>/<owner>/<repo>`.
  - `clone.post_actions` — run `hooks-install` and/or `doctor` inside the fresh checkout once the clone succeeds. Failures warn but never fail the clone.
  - `--dry-run` prints the resolved URL + target and exits without touching the network.
- **`gk status -f, --fetch`** — opt-in upstream fetch. Debounced, 3-second hard timeout, silent on failure (all safety bounds from the previous auto-fetch path remain intact).
- **narrow-TTY adaptation for `gk status` and `gk log`**: tree compresses 3-cell indent to 2-cell under 60 cols and drops the `(N)` subtree badge under 40 cols; types-chip budget-truncates tail tokens with a `+N more` suffix; heatmap directory column caps at `ttyW-22` with rune-aware ellipsis (fixes mid-codepoint truncation on CJK path names); `gk log --calendar` caps weeks at `(ttyW-4)/4`.

### Changed

- **`gk status` fetch is now opt-in.** The quiet upstream fetch introduced in v0.6.0 used to run on every invocation, which surfaced confusing noise (and `fatal: ...` fallout) on repos with no remote, detached HEAD, or an unreachable remote. New default: zero network activity — `gk status` reads only local state. Pass `-f` / `--fetch` to refresh the upstream ref for the ↑N ↓N counts. To restore the old always-fetch behavior, set `status.auto_fetch: true` in `.gk.yaml`.
- **Removed**: `--no-fetch` flag and `GK_NO_FETCH` env var — both existed only as opt-outs for the now-removed default.

## [0.8.0] - 2026-04-23

### Added

- **`gk init ai`** — scaffolds `CLAUDE.md` and `AGENTS.md` in the repository root so AI coding assistants (Claude Code, Jules, Copilot Workspace, Gemini CLI, etc.) have immediate project context. Pass `--kiro` to also scaffold `.kiro/steering/product.md`, `tech.md`, and `structure.md` for Kiro-compatible assistants. Files are skipped (not overwritten) when they already exist; `--force` opts in to overwrite. `--out <dir>` writes to a custom directory instead of the repo root.
- **`gk log --legend`** — prints a one-time glyph/color key for every active log visualization layer (`--vis cc`, `--vis safety`, `--vis impact`, etc.) and exits. Mirrors `gk status --legend`.

## [0.7.0] - 2026-04-23

### Added

- **`gk timemachine`** — new command tree that surfaces every recoverable HEAD state (reflog + `refs/gk/*-backup/`) and lets you restore any of them safely.
  - `gk timemachine restore <sha|ref>` — mixed/hard/soft/keep reset with an atomic backup ref written first. Flags: `--mode soft|mixed|hard|auto` · `--dry-run` · `--autostash` · `--force`. In-progress rebase/merge/cherry-pick states are refused even with `--force`. Full safety invariants live in [`docs/roadmap-v2.md`](docs/roadmap-v2.md#tm-18-runner-call-map).
  - `gk timemachine list` — unified timeline (`reflog` + `backup` + opt-in `stash` + opt-in `dangling`) newest-first, with `--kinds`, `--limit`, `--all-branches`, `--branch`, `--since`, `--dangling-cap`, and `--json` (NDJSON) for scripting. The `dangling` source runs `git fsck --lost-found`; the default cap is 500 entries so large repos do not hang.
  - `gk timemachine list-backups` — just the gk-managed backup refs, with `--kind` filter and `--json`.
  - `gk timemachine show <sha|ref>` — commit header + diff stat (or `--patch`) for any timeline entry; auto-prepends a `gk backup: kind=… branch=… when=…` line when the ref is under `refs/gk/*-backup/`.
  - Every restore prints the backup ref + a ready-to-paste `gk timemachine restore <backupRef>` revert hint.
- **`internal/gitsafe`** — new shared package that centralizes the "backup ref + reset" dance. `gitsafe.Restorer` implements a 6-step atomic contract (snapshot → backup → autostash → reset → pop → verify) with structured `RestoreError` stages for precise failure reporting. `gitsafe.DecideStrategy` codifies the hard-reset decision table so CLI and TUI consume one contract. Used internally by `gk undo`, `gk wipe`, and `gk timemachine restore`.
- **`internal/timemachine`** — unified `Event` stream type and source readers (`ReadHEAD`, `ReadBranches`, `ReadBackups`) plus `Merge` / `Limit` / `FilterByKind` utilities. Consumed by `gk timemachine list`.
- **`gk guard check`** — first policies-as-code surface. Evaluates every registered rule in parallel and prints sorted violations (error → warn → info) in human or `--json` NDJSON format. Ships one rule (`secret_patterns`) that delegates to gitleaks when installed and emits an info-level no-op violation otherwise. Exit codes: 0 clean / 1 warn / 2 error.
- **`gk guard init`** — scaffolds `.gk.yaml` in the repo root with a fully-commented `policies:` block.
- **`gk hooks install --pre-commit`** — new hook that wires `gk guard check` as a git `pre-commit` hook so policy rules run automatically before every commit. `selectHooks` was refactored to iterate `knownHooks()` generically so future hooks only need a `hookSpec` entry and a flag — no branch edits. Every rule stub (`secret_patterns`, `max_commit_size`, `required_trailers`, `forbid_force_push_to`, `require_signed`) is commented-out so the file is valid YAML from day one and users opt in explicitly. Also documents the `.gk/allow.yaml` per-finding suppression convention. Flags: `--force` (overwrite) · `--out <path>` (custom destination).
- **`internal/policy`** — new package hosting the `Rule` interface, `Registry`, and `Violation` schema. Rules declare `Name()` + `Evaluate(ctx, Input)`; the Registry runs them in parallel and sorts results deterministically.
- **`internal/policy/rules.SecretPatternsRule`** — the first rule. Thin adapter: calls `scan.RunGitleaks` and maps `GitleaksFinding` → `policy.Violation`.
- **`internal/scan`** — new package for secret-scanner adapters. Ships `FindGitleaks`, `ParseGitleaksFindings`, `RunGitleaks(ctx, opts)` (exit 1 = findings, not error), and `ErrGitleaksNotInstalled` sentinel. Per the 2026-04-22 probe, gk prefers the industry-standard gitleaks over a rebuilt scanner.

### Changed

- **`gk wipe` now runs a preflight check.** A repo with a rebase/merge/cherry-pick in progress used to let `gk wipe --yes` plough ahead and leave a half-broken state; it now refuses with the same `in-progress … run 'gk continue' or 'gk abort' first` message `gk undo` has always produced.
- **`gk undo` preflight refactored** to use `internal/gitsafe`. No user-visible behavior change; the old `*git.ExecRunner` type-assertion (which silently disabled in-progress detection under `FakeRunner` in tests) was replaced with an explicit `WithWorkDir` option.
- **`gk doctor` gains a `gk backup refs` row.** Counts refs under `refs/gk/*-backup/`, breaks down by kind (`undo`/`wipe`/`timemachine`), and surfaces the age of the oldest/newest — so a repo accumulating stale backup refs is visible at a glance.
- **`gk doctor` gains a `gitleaks` row.** Detects the `gitleaks` binary and its version. Lays groundwork for the gk-guard secret-scanner evaluator (post-probe decision: prefer the industry-standard gitleaks over a rebuilt scanner). WARN when absent with a brew/go install suggestion.

### Removed

- Private `backupRefName` / `wipeBackupRefName` / `safeBranchSegment` / `updateRef` / `resolveRef` helpers in `internal/cli/` — callers now use the exported `gitsafe.BackupRefName` / `gitsafe.Restorer` / `gitsafe.ResolveRef` equivalents. Ref naming format and stdout hints are byte-compatible with v0.6.

### Docs

- [`docs/commands.md`](docs/commands.md) gains a full **gk timemachine** section covering `list`, `list-backups`, and `restore` with flag tables, JSON schema, and examples.
- [`docs/roadmap-v2.md`](docs/roadmap-v2.md) remains the canonical design reference for the v2 surface (62 leaves, ship slices, Restorer runner call map, TM-14 decision table, kill criteria from the probe).
- TODO: document `gk push`, `gk sync`, `gk precheck`, `gk preflight`, `gk doctor`, `gk hooks`, `gk undo`, `gk restore`, `gk edit-conflict`, `gk lint-commit`, `gk branch-check` in `docs/commands.md` (pre-existing gaps inherited from 0.2.0 / 0.3.0).

## [0.6.0] - 2026-04-22

### Added

- `gk status` default rendering is now tree-based with a staleness-aware branch line. The shipped `status.vis` default is `[gauge, bar, progress, tree, staleness]`, so bare `gk status` already looks distinctly un-like `git status`: ahead/behind becomes a divergence gauge, file state becomes a stacked composition bar, cleanup reads as a progress meter, the file list is a path trie with collapsed single-child chains, and `· last commit 3d ago` plus `(14d old)` markers surface abandoned WIP automatically. The classic sectioned output is still one flag away (`gk status --vis none`).
- `gk status --vis base` — appends a second `from <trunk> [gauge]` line on feature branches showing divergence from the repo's mainline (resolved via `base_branch` config → `refs/remotes/<remote>/HEAD` → `main`/`master`/`develop`). Suppressed on the base branch itself. One `git rev-list --left-right --count` call (~5–15 ms).
- `gk status --vis since-push` — appends `· since push 2h (3c)` to the branch line when the current branch has unpushed commits. Age is the oldest unpushed commit; count is total unpushed. One `git rev-list @{u}..HEAD --format=%ct` call (~5 ms).
- `gk status --vis stash` — adds a `stash: 3 entries · newest 2h · oldest 5d · ⚠ 2 overlap with dirty` summary when the stash is non-empty. Overlap warning intersects the top stash's files with current dirty paths so the common `git stash pop` footgun is visible before you trigger it. 1–2 git calls (~5–10 ms total).
- `gk status --vis heatmap` — 2-D density grid above the entry list: rows are top-level directories, columns are `C` conflicts / `S` staged / `M` modified / `?` untracked, each cell scales ` `→`░`→`▒`→`▓`→`█` with the peak count. Purpose-built for 100+ dirty-file states where the tree scrolls off-screen. Zero extra git calls (pure aggregation over porcelain output).
- `gk status --vis glyphs` — prepends a semantic file-kind glyph to every entry (flat + tree): `●` source · `◐` test · `◆` config · `¶` docs · `▣` binary/asset · `↻` generated/vendored · `⊙` lockfile · `·` unknown. Classification is pure path matching (lockfile > generated > test > docs > config > binary > source) so a `package-lock.json` is `⊙` not `◆ JSON` and `foo_test.go` is `◐` not `●`. Zero file I/O, zero git calls.
- `gk status --top N` — truncates the entry list to the first N rows, sorted alphabetically for stable output, and emits a faint `… +K more (total · showing top N)` footer so the truncation is never silent. Composes with every viz layer; default `0` means unlimited.
- `gk status --no-fetch` — skip the quiet upstream fetch for this invocation. Also honored via `GK_NO_FETCH=1` or `status.auto_fetch: false` in `.gk.yaml`. The fetch itself was introduced in v0.6.0: by default `gk status` does a short, strictly-bounded fetch of the current branch's upstream so ↑N ↓N reflects the live remote (see "Changed" below for the full contract).
- `gk log` default rendering switches to a viz-aware pipeline. The shipped `log.vis` default is `[cc, safety, tags-rule]`, so bare `gk log` now shows a Conventional-Commits glyph column (`▲` feat · `✕` fix · `↻` refactor · `¶` docs · `·` chore · `◎` test · `↑` perf · `⊙` ci · `▣` build · `←` revert · `✧` style) with an inline-colored subject prefix and a trailing `types: feat=4 fix=1` tally, plus a left-margin rebase-safety marker (`◇` unpushed / `✎` amended / blank when already pushed), plus `──┤ vX.Y.Z (3d) ├──` rules before tagged commits.
- `gk log` relative age column is now compact (`6d` / `3m` / `1h` / `now` / `3mo` / `2y`) instead of git's verbose `6 days ago`. Saves 8–10 characters per row and disambiguates minutes (`m`) from months (`mo`).
- `gk log --impact` — appends an eighths-bar scaled to per-commit `+adds -dels` size.
- `gk log --hotspots` — marks commits that touch the repo's top-10 most-churned files from the last 90 days with `🔥`.
- `gk log --trailers` — appends a `[+Alice review:Bob]` roll-up parsed from `Co-authored-by:` / `Reviewed-by:` / `Signed-off-by:` trailers.
- `gk log --lanes` — replaces the commit list with per-author horizontal swim-lanes on a shared time axis; top 6 authors keep their own lane, the rest collapse into an `others` lane.
- `gk log --pulse` — prints a commit-rhythm sparkline above the log (one cell per day, `▁▂▃▄▅▆▇█` scaled to the peak, `·` for zero).
- `gk log --calendar` — prints a 7-row × N-week heatmap above the log (`░▒▓█` scaled to the busiest bucket, capped at 26 weeks).
- `gk log --tags-rule` — inserts a cyan `──┤ v0.4.0 (3d) ├────` separator line before any commit whose short SHA matches a tag. Handles annotated tags via `%(*objectname:short)`.
- `gk log --cc` / `--safety` — can be combined or subtracted via append semantics: `gk log --impact` keeps the default set and adds impact; `gk log --cc=false` peels cc off the default; `gk log --vis cc,impact` replaces the default entirely.
- `gk sw` with no argument now lists both local AND remote-only tracking branches in the picker. Local entries render with `●` in green; remote-only entries render with `○` in cyan and auto-run `git switch --track <remote>/<name>` when chosen, creating the local tracking branch in one step. `refs/remotes/*/HEAD` aliases are filtered; remote entries whose short name matches a local branch are hidden.
- Auto-fetch progress spinner on stderr. When `gk status` fetches and the call is slow enough to notice (>150 ms), a single-line braille-dot spinner (`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`) animates on stderr with a `fetching <remote>...` label. Non-TTY stderr (pipes, CI, `2>file`) disables it so pipelines stay clean.
- `make install` / `make uninstall` targets. Default `INSTALL_NAME=gk-dev` writes to `$(PREFIX)/bin/gk-dev` so a local build never shadows the Homebrew-managed `gk`. Override with `make install INSTALL_NAME=gk` to replace both.
- Config: `log.vis`, `status.vis`, `status.auto_fetch` keys. Both viz defaults are fully configurable via `.gk.yaml` — projects can pin their own layer set.

### Changed

- `gk status` now auto-fetches the current branch's upstream before reading porcelain output so `↑N ↓N` counts reflect the actual remote state, not the last-cached view. Scope is strictly bounded: single upstream ref only (no `--all`, no tags, no submodule recursion, no `FETCH_HEAD` write); 3-second hard timeout via context; `GIT_TERMINAL_PROMPT=0` + empty `SSH_ASKPASS` block credential prompts from hijacking the terminal; stderr discarded so remote chatter never interleaves with output; silent on every error path. Debounced with a per-repo marker file (`$GIT_COMMON_DIR/gk/last-fetch`) — subsequent invocations within a 3-second window skip the network round-trip entirely. Fast path checks `.git/gk/last-fetch` directly with zero git spawns, so warm calls measured at ~17 ms (vs ~60 ms cold). Opt out with `--no-fetch`, `GK_NO_FETCH=1`, or `status.auto_fetch: false`.
- `gk status` default visualization expanded from `[gauge, bar, progress]` (v0.5.0) to `[gauge, bar, progress, tree, staleness]`. Bare `gk status` now looks distinctly un-like `git status` — see Added above.
- `gk log` auto-detects viz intent: when the default `log.vis` is active, rendering switches from git's raw pretty-format to gk's layered pipeline. Explicit `--format <fmt>` alone suppresses the default (so the raw pretty-format stays in control); `--format` combined with an explicit viz flag preserves the viz (the user explicitly asked for both).
- Log CC glyphs redesigned to be uniformly single-cell geometric Unicode (`▲✕↻¶·◎↑⊙▣←✧`) instead of gitmoji (`✨🐛♻📝🧹🧪🚀🤖🏗↩💄`). Emoji varied in cell width across fonts, broke column alignment, and felt tonally at odds with the rest of the CLI. Geometric glyphs stay 1 cell wide in every modern monospace font and avoid collision with the safety column's `◆/◇/✎/!` markers.
- Log safety column no longer prints a glyph for the `already pushed` state — only `◇` (unpushed), `✎` (amended-in-last-hour), and blank. On an active branch virtually every commit is already pushed, so the old `◆` filled every row and drowned out the signal. The column width is preserved so alignment stays intact.
- `log` viz flag semantics are append-by-default: an individual flag like `--impact` stacks on top of the configured default; `--vis <list>` replaces it entirely; `--vis none` empties the baseline. This matches user intuition ("add impact to my normal view") over v0.5.0's "explicit = replace" semantics.
- `--vis gauge` on a clean tree now renders `[·······│·······] in sync` instead of nothing. Same for `--vis bar` → `tree: [·················] (clean)` and `--vis progress` → `clean: [██████████] 100% nothing to do`. Previously these layers silently skipped on clean trees, making users unsure whether the flag took effect.
- `--vis safety` on a pushed commit now renders a blank column (not `◆`) so only notable push-states draw attention.

### Performance

- `gk status` warm-call latency improved from ~60 ms to ~17 ms via a two-step optimization: (1) upstream + git-common-dir lookup collapsed into a single `git rev-parse --abbrev-ref HEAD@{u} --git-common-dir` call, and (2) a fast-path `os.Stat` on the debounce marker that skips every git spawn when the last fetch is under 3 s old. Repeated `gk st` invocations within the debounce window now run faster than the previous no-fetch path (~21 ms) because the upstream lookup is also skipped.

### Tooling

- Release workflow (this skill) now runs documentation-sync verification in Step 3b before cutting the tag. Extracts every `gk <cmd>` / `--flag` token from the promoted version section and checks both `README.md` and `docs/commands.md` for coverage; missing tokens trigger an `AskUserQuestion` to either document now or track via a TODO line.

## [0.5.0] - 2026-04-22

### Added

- `gk status --vis <list>` — opt-in visualizations for the working-tree summary. Accepts a comma-list or repeated flags; all are composable on top of the existing sectioned output.
  - `gauge` — replaces `↑N ↓N` with a fixed-width divergence gauge `[▓▓│····]`, green ahead blocks and red behind blocks anchored at the upstream marker. Narrows to 3 slots/side under 80-col TTYs.
  - `bar` — stacked composition bar of conflicts/staged/modified/untracked counts, each segment using a distinct block glyph (`▓█▒░`) so the bar stays readable under `--no-color`.
  - `progress` — one-line "how close to clean" meter (staged / total) plus a remaining-verb list (`resolve N · stage N · commit N · discard-or-track N`).
  - `types` — one-line extension histogram (`.ts×6 .md×2 .lock×1`). Collapses `package-lock.json` / `go.sum` / `Cargo.lock` / `Gemfile.lock` / `Pipfile.lock` / `poetry.lock` / `composer.lock` / `pnpm-lock.yaml` / `yarn.lock` into a single `.lock` kind; falls back to basename for extensionless files (`Makefile`, `Dockerfile`). Dims binary/lockfile kinds. Suppressed above 40 distinct kinds.
  - `staleness` — annotates the branch line with `· last commit 3d ago` and appends `(14d old)` to untracked entries older than a day. Ages collapse to the largest unit with 1–3 digits (`45s`, `12m`, `3h`, `11d`, `6w`, `4mo`, `2y`).
  - `tree` — replaces the flat sections with a path trie. Single-child directory chains collapse (`src/api/v2/auth.ts` renders as one leaf) to avoid deep indentation. Directory rows carry a subtree-count badge `(N)`.
  - `conflict` — appends `[N hunks · both modified]` (or `added by them`, `deleted by us`, etc.) to each conflicts entry. Hunk count is derived from `<<<<<<<` markers in the worktree file; conflict kind maps from the porcelain XY code.
  - `churn` — appends an 8-cell sparkline to each modified entry showing per-commit add+del totals for its last 8 commits, oldest-left / newest-right. Suppressed when the dirty tree has more than 50 files.
  - `risk` — flags high-risk modified entries with `⚠` and re-sorts the section so the hottest files are on top. Score is `current diff LOC + distinct-author-count-over-30d × 10`, threshold 50.

- `gk log` visualization flags — all composable and independent of each other; they layer on top of the default pretty-format log.
  - `--pulse` — commit-rhythm sparkline strip printed above the log, bucketed per day across the `--since` window. Zero-activity days render as `·`, active days scale to `▁▂▃▄▅▆▇█` relative to the peak, followed by `(N commits, peak Tue)`.
  - `--calendar` — 7-row × N-col heatmap (Mon..Sun by ISO week) using `░▒▓█` scaled to the busiest bucket. Capped at 26 weeks for terminal sanity.
  - `--tags-rule` — post-processes log stdout and inserts a cyan `──┤ v0.4.0 (3d ago) ├───` rule before any commit whose short-SHA matches a tag. Handles annotated tags via `%(*objectname:short)`.
  - `--impact` — eighths-bar `████▊ +412 −38` scaled to the run's peak diff size. Numstats come from a second `git log --numstat --format=%H` pass to keep the primary record stream simple.
  - `--cc` — Conventional-Commits glyph prefix (`✨` feat · `🐛` fix · `♻` refactor · `📝` docs · `🧹` chore · `🧪` test · `🚀` perf · `🤖` ci · `🏗` build · `↩` revert · `💄` style) + a `types: feat=4 chore=1` footer tallying the types in the visible range.
  - `--safety` — `◆` already-pushed · `◇` unpushed · `✎` amended-in-last-hour. Batched via `git rev-list @{upstream}` and a reflog scan; no per-commit git calls.
  - `--hotspots` — `🔥` on commits that touch any of the repo's top-10 most-touched files from the last 90 days (minimum 5 touches to qualify as a hotspot).
  - `--trailers` — `[+Alice review:Bob]` roll-up parsed from `Co-authored-by:` / `Reviewed-by:` / `Signed-off-by:` trailers in the commit body.
  - `--lanes` — alternate view: one horizontal swim-lane per author with `●` markers on a shared time axis. Top 6 authors keep their own lane; the tail collapses into a synthetic `others` lane. Width follows TTY (floor 10 cols), name column capped at 15 chars.

- `ui.TTYWidth()` exported from `internal/ui` so subcommands can adapt layouts to the terminal width.

### Changed

- `gk status` branch line no longer emits `↑N ↓N` when `--vis gauge` is active — the gauge carries the same information in a richer form.

## [0.4.0] - 2026-04-22

### Added

- `gk wipe [--yes] [--dry-run] [--include-ignored]` — discard ALL local changes and untracked files (`git reset --hard HEAD` + `git clean -fd`, or `-fdx` with `--include-ignored`). Before wiping, gk records a backup ref at `refs/gk/wipe-backup/<branch>/<unix>` so local commits remain recoverable (untracked files are not). Requires TTY confirmation or `--yes`; `--dry-run` prints the plan without touching the tree. Absorbs the oh-my-zsh `gpristine` / `gwipe` pattern with a safety net.
- `gk wip` / `gk unwip` — quick throwaway commit for context switching. `gk wip` stages every tracked change (`git add -A`) and commits with subject `--wip-- [skip ci]`, skipping hooks and signing for speed. `gk unwip` refuses unless HEAD's subject starts with `--wip--`, then runs `git reset HEAD~1` so the changes return to the working tree. Mirrors oh-my-zsh's `gwip` / `gunwip` with an explicit refusal guard.
- `gk reset --to-remote` — hard-reset the current branch to `<remote>/<current-branch>` regardless of the configured upstream. Useful when a branch has drifted from origin but has no `branch.<name>.merge` set. Mutually exclusive with `--to`. Absorbs oh-my-zsh's `groh` (`git reset origin/$(git_current_branch) --hard`) with the same confirm + dry-run safety as `gk reset`.
- `gk branch list --gone` — filter to branches whose upstream has been deleted on the remote. Detects the `[gone]` track state via `for-each-ref --format='…%00%(upstream:track)'`. Complements the existing `--stale <N>` / `--merged` filters.
- `gk branch list --unmerged` — mirror of `--merged`; lists branches NOT merged into the base (`git branch --no-merged <base>`). Mutually exclusive with `--merged`.
- `gk branch clean --gone` — delete local branches whose upstream is gone while respecting the protected list (current branch, configured `branch.protected`). Pairs with `--force` to use `branch -D` when a gone branch carries unmerged commits. Absorbs oh-my-zsh's `gbgd` / `gbgD`.
- `gk switch -m` / `--main` and `-d` / `--develop` — jump to the repo's canonical main or develop branch without typing its name. `--main` resolves via `client.DefaultBranch` first (honors `refs/remotes/<remote>/HEAD`) then falls back to local `main` or `master`; `--develop` tries `develop` then `dev`. Mutually exclusive; incompatible with a branch argument or `--create`. Absorbs `gcm` / `gcd` / `gswm` / `gswd`.
- `gk push` — when the current branch has no configured upstream, push now auto-adds `--set-upstream` so the first push wires it up. Removes the `fatal: The current branch has no upstream branch` speed bump without needing a separate alias. Absorbs oh-my-zsh's `ggsup` behavior.
- README: Install section documents the oh-my-zsh `git` plugin alias conflict (`alias gk='\gitk --all --branches &!'`, `alias gke='\gitk --all ...'`) and points to `unalias gk gke 2>/dev/null` as the resolution.
- Release skill (`.claude/skills/release/SKILL.md`): new **Step 3b — Documentation sync verification** between the CHANGELOG rewrite and the tag push. Parses `gk <cmd>` / `gk <cmd> --flag` tokens out of the just-promoted version section and requires each one to appear in `README.md` and `docs/commands.md`; a binary-vs-docs drift pass using `gk --help` is offered as an optional sanity check. Gaps block the release by default; the skill asks before proceeding with TODOs.

## [0.3.0] - 2026-04-22

### Changed

- Error output now includes a `hint:` line when the command can suggest a concrete next step. Implemented via `cli.WithHint(err, hint)` + `cli.FormatError(err)`; hint is extracted through `errors.Unwrap` chains so wrapping with `fmt.Errorf("%w")` still surfaces the hint. `cmd/gk/main.go` renders both lines. Initial hint sites: `gk precheck` unknown target (suggests `git fetch` / typo), `gk sync` dirty tree (`gk sync --autostash`), `gk pull` dirty tree (`gk pull --autostash`).

### Added

- `gk hooks install [--commit-msg] [--pre-push] [--all] [--force]` / `gk hooks uninstall` — write/remove thin POSIX shim scripts under `.git/hooks/`. Installed hooks carry a `# managed by gk` marker; the installer refuses to overwrite any hook missing the marker unless `--force` is passed (which writes a timestamped `.bak` backup first). Honors `core.hooksPath` and worktree `--git-common-dir`. Currently installs `commit-msg` → `gk lint-commit` and `pre-push` → `gk preflight`. Updates `gk doctor`'s remediation hint so it points at the installer.
- `gk doctor [--json]` — non-invasive environment report. Seven checks with PASS/WARN/FAIL status and copy-paste fix hints: git version (>= 2.38 required, >= 2.40 preferred), pager (delta → bat → less), fzf, editor ($GIT_EDITOR/$VISUAL/$EDITOR resolution), config (validates all load layers + reports repo-local `.gk.yaml`), and hook install state for `commit-msg` and `pre-push`. Exit 0 unless any FAIL row is present. `--json` emits machine-readable output for CI/onboarding scripts.
- `gk sync [--all] [--fetch-only] [--no-fetch] [--autostash]` — fetch remotes and fast-forward local branches to their configured upstreams. Never creates merge commits, never rebases. Current branch uses `git merge --ff-only`; other branches (`--all`) are advanced via `git update-ref` after an `is-ancestor` check. Diverged branches return a new `DivergedError` (exit 4) with a clear hint to use `gk pull`. Default fetch scope is `--all --prune`; narrows to a configured `remote` when set and `--all` is not passed.
- `gk precheck <target>` — dry-run a merge without touching the working tree. Runs `git merge-tree --write-tree --name-only --merge-base` and reports conflicted paths. Exit 0 clean, exit 3 on conflicts, exit 1 on unknown target. Supports `--base <ref>` to override the auto-computed merge-base and `--json` for CI consumption. Rejects refs starting with `-` to prevent argv injection.
- `internal/cli/precheck.go` — new `scanMergeConflicts` helper, shared with preflight's `no-conflict` alias. Prefers `--name-only` on git ≥ 2.40; falls back to `<<<<<<<` marker parsing for git 2.38/2.39 (reports paths as non-enumerable on that path).

### Fixed

- `runBuiltinNoConflict` (preflight's `no-conflict` step) — migrated to the shared `scanMergeConflicts` helper, which passes `--merge-base <oid>` as a flag. Latent bug: the prior 3-positional form (`merge-tree <base> <ours> <theirs>`) was removed in recent git and failed with a usage dump. Now reports the specific conflict count in the error message.

## [0.2.0] - 2026-04-21

### Added

**Safer rebasing**

- `gk undo` — reflog-based HEAD restoration. Shows recent reflog entries in a picker (fzf when available, numeric fallback otherwise) and runs `git reset --mixed <sha>` to the chosen point. Working tree is always preserved.
- Automatic backup ref at `refs/gk/undo-backup/<branch>/<unix>` before every undo. The command prints `git reset --hard <ref>` to revert the undo trivially.
- Preflight guards: blocks undo when the tree is dirty or a rebase/merge/cherry-pick is in progress, steering the user to `gk continue` / `gk abort`.
- Flags: `--list` (script-safe, print only), `--limit N`, `--yes` (skip confirmation), `--to <ref>` (skip picker, for automation).

- `gk restore --lost` — surfaces dangling commits and blobs from `git fsck --lost-found --unreachable`, sorted newest-first with subject + short SHA. Prints ready-to-paste `git cherry-pick` / `git branch <name> <sha>` hints.

- `gk edit-conflict` / `gk ec` — opens `$EDITOR` at the first `<<<<<<<` marker. Editor-aware cursor jump for vim / nvim / vi / emacs / nano / micro (via `+N`), VS Code / Code-Insiders (via `--goto file:N`), sublime / helix (via `file:N`). Falls back to bare path for unknown editors. `--list` mode prints paths only for scripting.

**Preflight & conventions**

- `gk lint-commit [<rev-range>|--file PATH|--staged]` — validates commit messages against Conventional Commits. Installable as a commit-msg hook (`gk lint-commit --file $1`). Six rules: header-invalid, type-empty, type-enum, scope-required, subject-empty, subject-max-length.

- `gk branch-check [--branch NAME] [--patterns REGEX,...]` — enforces branch-naming patterns. Default pattern: `^(feat|fix|chore|docs|refactor|test|perf|build|ci|revert)/[a-z0-9._-]+$`. Branches on the protected list (main/master/develop) bypass the check. Prints an example branch name when the pattern has a clear prefix group.

- `gk push [REMOTE] [BRANCH] [--force] [--skip-scan] [--yes]` — guarded push wrapper.
  - Scans the commits-to-push diff (`<remote>/<branch>..HEAD`) with built-in secret patterns: AWS access/secret keys, GitHub classic + fine-grained tokens, Slack tokens, OpenAI keys, private-key PEM headers, and generic `key/secret/token/password` literal assignments.
  - Protected-branch force pushes require typing the exact branch name at the prompt (`--yes` skips it only when a TTY is available).
  - `--force` routes through `--force-with-lease` to avoid clobbering upstream.

- `gk preflight [--dry-run] [--continue-on-failure] [--skip NAME,...]` — runs the configured step sequence. Built-in aliases: `commit-lint`, `branch-check`, `no-conflict` (pre-merge scan via `git merge-tree --write-tree`). User-defined steps execute as `sh -c` commands and surface output on failure.

**CLI ecosystem hooks**

- `internal/ui/pager.go` — pager detection library. Priority: `GK_PAGER` → `PAGER` → PATH lookup (`delta` → `bat` → `less`). Tuned default args per binary, respects `NO_COLOR`, auto-passes TTY width to delta.
- `internal/ui/fzf.go` — reusable `Picker` interface with `FzfPicker` (stdin pipe + `--preview`) and `FallbackPicker` (numeric prompt). `NewPicker()` auto-selects based on `fzf` availability and TTY state. Consumed by `gk undo`.
- `internal/reflog` — Conventional Commits-independent reflog parser. `Read()` pulls via `git reflog --format=...`, `Parse()` handles the NUL/RS-delimited raw bytes, and `classifyAction()` maps messages into 11 coarse-grained actions (reset/commit/merge/rebase/checkout/pull/push/branch/cherry-pick/stash/unknown).

**Config extensions**

- `commit.{types, scope_required, max_subject_length}` — Conventional Commits rule set.
- `push.{protected, secret_patterns, allow_force}` — push safety rails.
- `preflight.steps[{name, command, continue_on_failure}]` — ordered check list with built-in aliases.
- `branch.{patterns, allow_detached}` — naming policy alongside the existing `stale_days` / `protected`.
- Sensible defaults ship in `config.Defaults()` so every new command works out of the box without a `.gk.yaml` file.

### Changed

- `internal/git/client.go` — fixed off-by-one in `parsePorcelainV2` for untracked entries (`tok[3:]` → `tok[2:]`); the path's first character was being dropped.
- `.goreleaser.yaml` — removed placeholder comments now that the tap repo is real.

### Fixed

- `internal/ui/fzf_test.go` — `TestFzfPicker_SkipWhenNoFzf` no longer hangs on non-TTY environments. Now skips when stdout/stdin are not a TTY and wraps the Pick call in a 2-second context timeout as a safety net.

### Tooling

- `.claude/skills/release/SKILL.md` — `/release` slash command automates: prerequisite checks → version bump prompt → local validation → CHANGELOG migration → tag + push → GitHub Actions monitoring → Homebrew tap verification. Diagnostic matrix for 401 / 403 / 422 failure modes with concrete recovery actions.

[Unreleased]: https://github.com/x-mesh/gk/compare/v0.76.0...HEAD
[0.76.0]: https://github.com/x-mesh/gk/compare/v0.75.2...v0.76.0
[0.75.0]: https://github.com/x-mesh/gk/compare/v0.74.0...v0.75.0
[0.74.0]: https://github.com/x-mesh/gk/compare/v0.73.0...v0.74.0
[0.73.0]: https://github.com/x-mesh/gk/compare/v0.72.0...v0.73.0
[0.72.0]: https://github.com/x-mesh/gk/compare/v0.71.0...v0.72.0
[0.71.0]: https://github.com/x-mesh/gk/compare/v0.70.0...v0.71.0
[0.70.0]: https://github.com/x-mesh/gk/compare/v0.69.0...v0.70.0
[0.69.0]: https://github.com/x-mesh/gk/compare/v0.68.0...v0.69.0
[0.68.0]: https://github.com/x-mesh/gk/compare/v0.67.0...v0.68.0
[0.67.0]: https://github.com/x-mesh/gk/compare/v0.66.0...v0.67.0
[0.66.0]: https://github.com/x-mesh/gk/compare/v0.65.0...v0.66.0
[0.65.0]: https://github.com/x-mesh/gk/compare/v0.64.0...v0.65.0
[0.64.0]: https://github.com/x-mesh/gk/compare/v0.63.0...v0.64.0
[0.63.0]: https://github.com/x-mesh/gk/compare/v0.62.1...v0.63.0
[0.62.1]: https://github.com/x-mesh/gk/compare/v0.62.0...v0.62.1
[0.62.0]: https://github.com/x-mesh/gk/compare/v0.61.0...v0.62.0
[0.61.0]: https://github.com/x-mesh/gk/compare/v0.60.0...v0.61.0
[0.60.0]: https://github.com/x-mesh/gk/compare/v0.59.1...v0.60.0
[0.59.1]: https://github.com/x-mesh/gk/compare/v0.59.0...v0.59.1
[0.59.0]: https://github.com/x-mesh/gk/compare/v0.58.0...v0.59.0
[0.58.0]: https://github.com/x-mesh/gk/compare/v0.57.1...v0.58.0
[0.53.0]: https://github.com/x-mesh/gk/compare/v0.52.0...v0.53.0
[0.52.0]: https://github.com/x-mesh/gk/compare/v0.51.0...v0.52.0
[0.51.0]: https://github.com/x-mesh/gk/compare/v0.50.1...v0.51.0
[0.50.1]: https://github.com/x-mesh/gk/compare/v0.50.0...v0.50.1
[0.50.0]: https://github.com/x-mesh/gk/compare/v0.49.0...v0.50.0
[0.49.0]: https://github.com/x-mesh/gk/compare/v0.48.0...v0.49.0
[0.48.0]: https://github.com/x-mesh/gk/compare/v0.47.0...v0.48.0
[0.47.0]: https://github.com/x-mesh/gk/compare/v0.46.0...v0.47.0
[0.20.0]: https://github.com/x-mesh/gk/compare/v0.19.0...v0.20.0
[0.19.0]: https://github.com/x-mesh/gk/compare/v0.18.0...v0.19.0
[0.18.0]: https://github.com/x-mesh/gk/compare/v0.17.5...v0.18.0
[0.14.1]: https://github.com/x-mesh/gk/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/x-mesh/gk/compare/v0.13.1...v0.14.0
[0.13.1]: https://github.com/x-mesh/gk/compare/v0.13.0...v0.13.1
[0.13.0]: https://github.com/x-mesh/gk/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/x-mesh/gk/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/x-mesh/gk/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/x-mesh/gk/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/x-mesh/gk/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/x-mesh/gk/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/x-mesh/gk/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/x-mesh/gk/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/x-mesh/gk/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/x-mesh/gk/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/x-mesh/gk/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/x-mesh/gk/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/x-mesh/gk/releases/tag/v0.1.0

## [0.1.0] - 2026-04-20

### Added

- `gk pull` — fetch + rebase with auto base-branch detection (`origin/HEAD` → `develop` → `main` → `master`). Supports `--base`, `--no-rebase`, and `--autostash`.
- `gk log` / `gk slog` — customizable short log with `--since` shortcuts (`1w`, `3d`, `12h`), `--graph`, `--limit`, and `--format`.
- `gk status` / `gk st` — concise working tree status using `git status --porcelain=v2 -z`. Shows staged, unstaged, untracked, and conflicted files plus ahead/behind counts.
- `gk branch list` — list local branches with `--stale <N>` and `--merged` filters.
- `gk branch clean` — delete merged branches while respecting the configured protected list. Supports `--dry-run` and `--force`.
- `gk branch pick` — interactive branch picker (TUI prompt with plain-list fallback for non-TTY).
- `gk continue` — continue an in-progress rebase, merge, or cherry-pick after conflict resolution. Supports `--yes` to skip prompt.
- `gk abort` — abort an in-progress rebase, merge, or cherry-pick and restore previous state. Supports `--yes` to skip prompt.
- `gk config show` — print the fully resolved configuration as YAML.
- `gk config get <key>` — print a single config value by dot-notation key.
- Config loading priority: built-in defaults → `~/.config/gk/config.yaml` (XDG) → repo-local `.gk.yaml` → `git config gk.*` → `GK_*` environment variables → CLI flags.
- Global automation flags: `--dry-run`, `--json`, `--no-color`, `--repo`, `--verbose`.
- Per-command automation flags: `--yes` (continue/abort), `--autostash` (pull).
- Safety: `LC_ALL=C` and `GIT_OPTIONAL_LOCKS=0` enforced on all git calls; `core.quotepath=false` set; user-supplied refs validated with `git check-ref-format` and separated by `--` to prevent argv injection.
- Exit code convention: 0 success, 1 general error, 2 invalid input, 3 conflict, 4 config error, 5 network error.
- goreleaser configuration for cross-platform builds (darwin/linux × amd64/arm64) and Homebrew tap distribution.
