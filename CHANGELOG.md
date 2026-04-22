# Changelog

All notable changes to gk will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- TODO: document `gk push`, `gk sync`, `gk precheck`, `gk preflight`, `gk doctor`, `gk hooks`, `gk undo`, `gk restore`, `gk edit-conflict`, `gk lint-commit`, `gk branch-check` in `docs/commands.md` (pre-existing gaps inherited from 0.2.0 / 0.3.0).

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

[Unreleased]: https://github.com/x-mesh/gk/compare/v0.3.0...HEAD
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
