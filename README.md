<p align="center">
  <img src="assets/gk-logo.jpeg" alt="gk" width="220">
</p>

# gk — git helper

A lightweight Go git helper for daily pull/log/status/branch workflows, with a focus on **safe operations** (dry-run previews, reflog-backed undo, secret-scanning push) and **ergonomic diagnostics** (`doctor`, `precheck`, `sync`).

[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![Release](https://img.shields.io/github/v/release/x-mesh/gk)](https://github.com/x-mesh/gk/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Why gk?

- **Safer pushes by default** — `gk push` scans the commits-to-push diff for AWS / GitHub / Slack / OpenAI keys and PEM bodies; protected-branch force pushes require typing the exact branch name.
- **Reflog-backed undo** — `gk undo` picks a past HEAD from the reflog (fzf or numeric picker), resets to it, and leaves a backup ref at `refs/gk/undo-backup/<branch>/<unix>` so every undo is trivially reversible.
- **Dry-run any merge** — `gk precheck <target>` runs `git merge-tree` and reports conflicted paths without touching your working tree (exit 3 on conflicts for CI).
- **One-shot fast-forward** — `gk sync` fetches remotes and fast-forwards the current branch (or every tracked branch with `--all`). Never creates merge commits; diverged branches fail cleanly with a `gk pull` hint.
- **Conventional-Commits-aware hooks** — `gk hooks install` wires `commit-msg` → `gk lint-commit` and `pre-push` → `gk preflight`. Managed hooks carry a marker, so re-installation is idempotent and foreign hooks are never clobbered without `--force`.
- **Health at a glance** — `gk doctor` reports PASS/WARN/FAIL on git version, pager, fzf, `$EDITOR`, config validity, and hook install state — with copy-paste fix commands.
- **Actionable errors** — most errors print a second-line `hint:` with the concrete next command.

## Install

### Homebrew tap (recommended)

```bash
brew install x-mesh/tap/gk
# upgrade later:
brew upgrade x-mesh/tap/gk
```

### go install

```bash
go install github.com/x-mesh/gk/cmd/gk@latest
```

Requires **git ≥ 2.38** (for `merge-tree --write-tree`; ≥ 2.40 preferred so `gk precheck` can enumerate conflicted paths by name). Run `gk doctor` after install to verify.

## Quickstart

```bash
# Daily driver
gk pull              # fetch + rebase, auto-detects base branch
gk sync              # fetch + fast-forward only (never rebases)
gk status            # concise working-tree summary
gk log               # short, colorful commit log

# Safety
gk precheck main     # dry-run merge into main; exits 3 if conflicts
gk push              # scans diff for secrets, enforces protected-branch rules
gk undo              # pick a past HEAD from the reflog and restore

# Onboarding
gk doctor            # report env health + fix commands
gk hooks install --all   # wire commit-msg + pre-push hooks

# Conventions
gk lint-commit --staged    # validate commit message vs Conventional Commits
gk branch-check            # enforce branch naming rules
gk preflight               # run the configured check sequence
```

## Commands

### Daily
| Command | Alias | Description |
|---|---|---|
| `gk pull` | | Fetch + rebase onto the auto-detected base branch |
| `gk sync` | | Fetch + fast-forward only; `--all` for every tracked branch |
| `gk status` | `gk st` | Concise working-tree status (staged / unstaged / untracked / conflicted + ahead/behind) |
| `gk log` | `gk slog` | Short colored commit log; `--since 1w`, `--graph`, `--limit N` |

### Branches
| Command | Alias | Description |
|---|---|---|
| `gk branch list` | | List branches with `--stale <N>` / `--merged` filters |
| `gk branch clean` | | Delete merged branches while respecting protected list |
| `gk branch pick` | | Interactive branch picker with non-TTY fallback |
| `gk branch-check` | | Validate current branch name against configured patterns |

### Safety
| Command | Description |
|---|---|
| `gk push` | Guarded push: secret scan + protected-branch enforcement; `--force` routes through `--force-with-lease` |
| `gk precheck <target>` | Dry-run merge conflict scan via `git merge-tree`; exit 3 on conflicts; `--json` for CI |
| `gk preflight` | Run configured check sequence (`commit-lint`, `branch-check`, `no-conflict`, or shell commands) |
| `gk lint-commit` | Validate commit message against Conventional Commits; `--staged`, `--file PATH`, `<rev-range>` |

### Recovery
| Command | Alias | Description |
|---|---|---|
| `gk undo` | | Reflog-based HEAD restore; leaves backup ref at `refs/gk/undo-backup/...` |
| `gk restore --lost` | | Surface dangling commits and blobs with cherry-pick hints |
| `gk edit-conflict` | `gk ec` | Open `$EDITOR` at the first `<<<<<<<` marker with editor-aware cursor jump |
| `gk continue` | | Continue interrupted rebase/merge/cherry-pick |
| `gk abort` | | Abort interrupted rebase/merge/cherry-pick |

### Onboarding / config
| Command | Description |
|---|---|
| `gk doctor` | Environment health report (git/pager/fzf/editor/config/hooks) with fix commands; `--json` for CI |
| `gk hooks install [--commit-msg\|--pre-push\|--all] [--force]` | Write gk-managed hook shims under `.git/hooks/` |
| `gk hooks uninstall [...]` | Remove gk-managed hooks (refuses to delete foreign ones) |
| `gk config show` | Print fully resolved config as YAML |
| `gk config get <key>` | Print a single config value by dot-path |

See [docs/commands.md](docs/commands.md) for full flag reference and [CHANGELOG.md](CHANGELOG.md) for per-release details.

## Global flags

| Flag | Description |
|---|---|
| `--dry-run` | Print actions without executing |
| `--json` | JSON output where supported |
| `--no-color` | Disable color output |
| `--repo <path>` | Path to git repo (default: current directory) |
| `--verbose` | Verbose output |

## Configuration

gk reads configuration from multiple sources in priority order (highest wins):

1. CLI flags
2. `GK_*` environment variables
3. `git config gk.*` entries
4. `.gk.yaml` in the repo root
5. `~/.config/gk/config.yaml` (XDG)
6. Built-in defaults

See [docs/config.md](docs/config.md) for all fields. A sample config is at [examples/config.yaml](examples/config.yaml).

## Exit codes

| Code | Meaning |
|:-:|---|
| 0 | Success |
| 1 | General error |
| 2 | Invalid input (unknown ref, bad flag) |
| 3 | Conflict (merge/rebase/precheck) |
| 4 | Diverged (cannot fast-forward) |
| 5 | Network error |

Scripts can rely on these being stable across releases.

## Development

```bash
git clone https://github.com/x-mesh/gk.git
cd gk

make build          # outputs to bin/gk
make test           # go test ./... -race -cover
make lint           # golangci-lint run
make fmt            # gofmt + go mod tidy
```

Requires Go 1.25+ and git 2.38+.

## License

[MIT](LICENSE)
