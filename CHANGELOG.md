# Changelog

All notable changes to gk will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/x-mesh/gk/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/x-mesh/gk/releases/tag/v0.1.0
