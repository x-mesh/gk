# Changelog

All notable changes to gk will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/x-mesh/gk/compare/v0.20.0...HEAD
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
