<p align="center">
  <img src="assets/gk-logo.jpeg" alt="gk" width="520">
</p>

<p align="center">
  <strong>English</strong> · <a href="README.ko.md">한국어</a>
</p>

# gk — git helper

A lightweight Go git helper for daily pull/log/status/branch workflows, with a focus on **safe operations** (reflog-backed undo, time-machine restore, policies-as-code) and **ergonomic diagnostics** (`doctor`, `precheck`, `sync`).

[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![Release](https://img.shields.io/github/v/release/x-mesh/gk)](https://github.com/x-mesh/gk/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Why gk?

- **Safer pushes by default** — `gk push` scans the commits-to-push diff for AWS / GitHub / Slack / OpenAI keys and PEM bodies; protected-branch force pushes require typing the exact branch name.
- **Time machine for HEAD** — `gk timemachine list` surfaces every recoverable state (reflog + gk backup refs). `gk timemachine restore <sha|ref>` resets safely: atomic backup ref written first, autostash support, refuses mid-rebase/merge.
- **Reflog-backed undo** — `gk undo` picks a past HEAD from the reflog (fzf or numeric picker), resets to it, and leaves a backup ref at `refs/gk/undo-backup/<branch>/<unix>` so every undo is trivially reversible.
- **Policies as code** — `gk guard check` evaluates repo policy rules (secret scanning, commit size, required trailers) in parallel; `gk guard init` scaffolds `.gk.yaml` with commented stubs. Wire as a pre-commit hook with `gk hooks install --pre-commit`.
- **Dry-run any merge** — `gk precheck <target>` runs `git merge-tree` and reports conflicted paths without touching your working tree (exit 3 on conflicts for CI).
- **One-shot fast-forward** — `gk sync` fetches remotes and fast-forwards the current branch (or every tracked branch with `--all`). Never creates merge commits; diverged branches fail cleanly with a `gk pull` hint.
- **Flexible pull strategy** — `gk pull --strategy rebase|merge|ff-only|auto` overrides the default per-invocation; resolves upstream from `@{u}` first, auto-switches to `merge --ff-only` when fast-forward is possible.
- **Conventional-Commits-aware hooks** — `gk hooks install` wires `commit-msg` → `gk lint-commit`, `pre-push` → `gk preflight`, and `pre-commit` → `gk guard check`. Managed hooks carry a marker, so re-installation is idempotent and foreign hooks are never clobbered without `--force`.
- **Health at a glance** — `gk doctor` reports PASS/WARN/FAIL on git version, pager, fzf, `$EDITOR`, config validity, hook state, gitleaks install, and gk backup-ref accumulation — with copy-paste fix commands.
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

### oh-my-zsh users: alias conflict

oh-my-zsh's `git` plugin defines `gk` as a `gitk` launcher, which shadows the `gk` binary. Drop the conflicting aliases in your `~/.zshrc` after oh-my-zsh loads:

```zsh
unalias gk gke 2>/dev/null
```

## Quickstart

```bash
# Daily driver
gk clone JINWOO-J/playground # expand to git@github.com:JINWOO-J/playground.git
gk pull                      # fetch + rebase, auto-detects upstream
gk pull --strategy ff-only   # fast-forward only; errors if histories diverged
gk sync                      # fetch + fast-forward only (never rebases)
gk status                    # concise working-tree summary
gk log                       # short, colorful commit log

# Safety
gk precheck main     # dry-run merge into main; exits 3 if conflicts
gk push              # scans diff for secrets, enforces protected-branch rules
gk undo              # pick a past HEAD from the reflog and restore

# Time machine
gk timemachine list          # all recoverable HEAD states (reflog + backups)
gk timemachine restore <sha> # safe reset — writes backup ref first

# Policies
gk guard init        # scaffold .gk.yaml with commented policy stubs
gk guard check       # evaluate all policy rules; exit 0/1/2

# Onboarding
gk doctor            # report env health + fix commands
gk hooks install --all       # wire commit-msg + pre-push + pre-commit hooks

# Conventions
gk lint-commit --staged    # validate commit message vs Conventional Commits
gk branch-check            # enforce branch naming rules
gk preflight               # run the configured check sequence
```

## Commands

### Daily
| Command | Alias | Description |
|---|---|---|
| `gk clone <owner/repo \| alias:owner/repo \| url>` | | Clone with short-form URL expansion. Bare `owner/repo` expands to `git@github.com:owner/repo.git` (ssh default, configurable). `--ssh`/`--https` override. `clone.hosts` maps aliases (`gl:`, `work:`). Optional `clone.root` + `clone.post_actions: [hooks-install, doctor]`. |
| `gk pull` | | Fetch + integrate upstream. `--strategy rebase\|merge\|ff-only\|auto`; resolves `@{u}` first; auto-switches to `--ff-only` when HEAD is already an ancestor |
| `gk sync` | | Fetch + fast-forward only; `--all` for every tracked branch |
| `gk status` | `gk st` | Concise working-tree status (staged / unstaged / untracked / conflicted + ahead/behind). Pass `-f`/`--fetch` to refresh ↑N ↓N from the remote. Opt-in `--vis gauge,bar,progress,types,staleness,tree,conflict,churn,risk` overlays |
| `gk log` | `gk slog` | Short colored commit log; `--since 1w`, `--graph`, `--limit N`. Opt-in `--pulse`, `--calendar`, `--tags-rule`, `--impact`, `--cc`, `--safety`, `--hotspots`, `--trailers`, `--lanes` visualizations |

### Branches
| Command | Alias | Description |
|---|---|---|
| `gk branch list` | | List branches with `--stale <N>` / `--merged` / `--unmerged` / `--gone` filters |
| `gk branch clean` | | Delete merged branches while respecting protected list; `--gone` targets branches whose upstream was deleted |
| `gk branch pick` | | Interactive branch picker with non-TTY fallback |
| `gk branch-check` | | Validate current branch name against configured patterns |
| `gk switch [name]` | `gk sw` | Switch branches; `-m`/`--main` jumps to detected main, `-d`/`--develop` to develop/dev |

### Safety
| Command | Description |
|---|---|
| `gk push` | Guarded push: secret scan + protected-branch enforcement; `--force` routes through `--force-with-lease` |
| `gk precheck <target>` | Dry-run merge conflict scan via `git merge-tree`; exit 3 on conflicts; `--json` for CI |
| `gk preflight` | Run configured check sequence (`commit-lint`, `branch-check`, `no-conflict`, or shell commands) |
| `gk lint-commit` | Validate commit message against Conventional Commits; `--staged`, `--file PATH`, `<rev-range>` |

### Policies
| Command | Description |
|---|---|
| `gk guard check` | Evaluate all policy rules in parallel; human or `--json` output; exit 0 clean / 1 warn / 2 error |
| `gk guard init` | Scaffold `.gk.yaml` with a fully-commented `policies:` block; `--force` to overwrite, `--out` for custom path |

### Recovery
| Command | Alias | Description |
|---|---|---|
| `gk timemachine list` | | Unified timeline of reflog + gk backup refs; `--kinds reflog,backup,stash,dangling`, `--json` (NDJSON) |
| `gk timemachine restore <sha\|ref>` | | Safe reset: writes backup ref first, then resets; `--mode soft\|mixed\|hard\|auto`, `--dry-run`, `--autostash` |
| `gk timemachine list-backups` | | gk-managed backup refs only; `--kind undo\|wipe\|timemachine`, `--json` |
| `gk timemachine show <sha\|ref>` | | Commit header + diff stat (or `--patch`) for any timeline entry |
| `gk undo` | | Reflog-based HEAD restore; leaves backup ref at `refs/gk/undo-backup/...` |
| `gk reset` | | Hard-reset current branch to its upstream; `--to-remote` uses `<remote>/<current>` |
| `gk wipe` | | `reset --hard` + `clean -fd`; backs up pre-wipe HEAD at `refs/gk/wipe-backup/...` |
| `gk restore --lost` | | Surface dangling commits and blobs with cherry-pick hints |
| `gk edit-conflict` | `gk ec` | Open `$EDITOR` at the first `<<<<<<<` marker with editor-aware cursor jump |
| `gk continue` | | Continue interrupted rebase/merge/cherry-pick |
| `gk abort` | | Abort interrupted rebase/merge/cherry-pick |
| `gk wip` / `gk unwip` | | Quick throwaway WIP commit for context switching; `unwip` restores changes to the working tree |

### AI
| Command | Description |
|---|---|
| `gk ai commit` | Group WIP (staged + unstaged + untracked) into semantic commit plans via an AI CLI and apply them. `-f/--force` skips review, `--dry-run` previews only, `--abort` restores HEAD to the latest backup ref. See **AI commit** section below |

### Onboarding / config
| Command | Description |
|---|---|
| `gk doctor` | Environment health report (git/pager/fzf/editor/config/hooks/gitleaks/backup-refs/ai-providers) with fix commands; `--json` for CI |
| `gk init ai [--kiro] [--force] [--out <dir>]` | Scaffold `CLAUDE.md` + `AGENTS.md` (and optionally `.kiro/steering/`) so AI coding assistants have immediate project context |
| `gk hooks install [--commit-msg\|--pre-push\|--pre-commit\|--all] [--force]` | Write gk-managed hook shims under `.git/hooks/` (`--pre-commit` wires `gk guard check`) |
| `gk hooks uninstall [...]` | Remove gk-managed hooks (refuses to delete foreign ones) |
| `gk config show` | Print fully resolved config as YAML |
| `gk config get <key>` | Print a single config value by dot-path |

See [docs/commands.md](docs/commands.md) for full flag reference and [CHANGELOG.md](CHANGELOG.md) for per-release details.

## AI commit

`gk ai commit` analyses the current working tree (staged + unstaged + untracked), groups the changes into semantic commit plans via an external AI CLI, and applies one Conventional Commit per plan.

### Provider setup

`gk ai commit` drives **already-installed** AI CLI binaries — it never talks to remote LLM APIs directly, so no API key lives inside `gk`.

| Provider | Install | Auth |
|---|---|---|
| `gemini` (Google) | `npm i -g @google/gemini-cli` or `brew install gemini-cli` | `export GEMINI_API_KEY=...` or run `gemini` once for OAuth |
| `qwen` (Alibaba) | `npm i -g @qwen-code/qwen-code` | `qwen auth qwen-oauth` or `export DASHSCOPE_API_KEY=...` |
| `kiro-cli` (AWS Kiro headless — note: **not** the `kiro` IDE launcher) | See [kiro.dev/docs/cli/installation](https://kiro.dev/docs/cli/installation) | `export KIRO_API_KEY=...` (Kiro Pro) or IDE OAuth session |

Run `gk doctor` to verify each provider's install + auth status.

### Flags

```
gk ai commit [flags]

      --abort                      restore HEAD to the latest ai-commit backup ref and exit
      --allow-secret-kind strings  suppress secret findings of the given kind (repeatable)
      --ci                         CI mode — require --force or --dry-run, never prompt
      --dry-run                    show the plan and exit without committing
  -f, --force                      apply commits without interactive review
      --include-unstaged           include unstaged + untracked changes (default true)
      --lang string                override ai.lang (en|ko|...)
      --provider string            override ai.provider (gemini|qwen|kiro)
      --staged-only                only consider already-staged changes
  -y, --yes                        accept every prompt (alias for --force when non-TTY)
```

### Config

```yaml
# .gk.yaml (or ~/.config/gk/config.yaml)
ai:
  enabled: true              # master off-switch; GK_AI_DISABLE=1 also disables
  provider: ""               # "" = auto-detect (gemini → qwen → kiro-cli)
  lang: "en"                 # message language (BCP-47 short)
  commit:
    mode: "interactive"      # interactive | force | dry-run (CLI flags override)
    max_groups: 10
    max_tokens: 24000
    timeout: "30s"
    allow_remote: true       # set false to block all three shipped providers (Locality=remote)
    trailer: false           # true → append "AI-Assisted-By: <provider>@<version>" to each commit
    audit: false             # true → append JSONL to .git/gk-ai-commit/audit.jsonl
    deny_paths:              # globs always skipped before anything leaves the process
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
```

### Safety rails (every run)

- **Secret gate** — runs `internal/secrets.Scan` plus `gitleaks` (when installed) over the payload; any finding aborts, even with `--force`. Use `--allow-secret-kind <kind>` per-run to whitelist a specific kind.
- **Deny paths** — matching files (`.env`, private keys, tfstate, …) are dropped before the payload leaves the process.
- **Git-state guard** — refuses to run mid-rebase / mid-merge / mid-cherry-pick so `MERGE_MSG` is never overwritten.
- **Backup ref** — each run writes `refs/gk/ai-commit-backup/<branch>/<unix>` before committing; `gk ai commit --abort` restores HEAD there.
- **Conventional lint loop** — `internal/commitlint.Parse/Lint` validates every message; failures trigger up to two provider retries with feedback injected into the prompt.
- **Path-rule override** — `_test.go`, `docs/*.md`, `.github/workflows/*.yml`, and lockfiles are always reclassified to `test`/`docs`/`ci`/`build` even if the provider picks a different type.

### Quick example

```bash
# Dry-run: see the plan without committing.
gk ai commit --dry-run

# Commit one-shot (no TUI).
gk ai commit --force --provider gemini

# Recover from a partial failure.
gk ai commit --abort
```

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
| 1 | General error · `gk guard check`: warn-level violations present |
| 2 | Invalid input (unknown ref, bad flag) · `gk guard check`: error-level violations present |
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
