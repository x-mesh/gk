# Changelog

All notable changes to gk will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/x-mesh/gk/compare/v0.10.0...HEAD
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
