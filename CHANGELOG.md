# Changelog

All notable changes to gk will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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

[Unreleased]: https://github.com/x-mesh/gk/compare/v0.2.0...HEAD
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
