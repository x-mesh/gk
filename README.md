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
- **Local-first rebase** — `gk sync` rebases the current branch onto local `<base>` offline. `gk sync --fetch` is the explicit one-shot when the user wants the network too. Stale-base hint when local `<base>` differs from `<remote>/<base>`.
- **Diverged-pull safety net** — `gk pull` refuses to silently rewrite local SHAs when histories have diverged, presenting `--rebase` / `--merge` / `--fetch-only` as explicit choices. `pull.strategy` config (or the explicit flags) bypasses the gate. Every history-rewriting integration writes a `refs/gk/backup/<branch>/<ts>` ref first.
- **Conventional-Commits-aware hooks** — `gk hooks install` wires `commit-msg` → `gk lint-commit`, `pre-push` → `gk preflight`, and `pre-commit` → `gk guard check`. Managed hooks carry a marker, so re-installation is idempotent and foreign hooks are never clobbered without `--force`.
- **Health at a glance** — `gk doctor` reports PASS/WARN/FAIL on git version, pager, fzf, `$EDITOR`, config validity, hook state, gitleaks install, and gk backup-ref accumulation — with copy-paste fix commands.
- **Easy Mode for new users** — `--easy` (or `output.easy: true` / `GK_EASY=1`) translates technical git terminology into Korean equivalents wrapped with the original (`commit` → `변경사항 저장 (commit)`), prefixes status sections with emoji, and surfaces contextual next-step hints. Korean command aliases (`gk 갈래`, `gk 상태`, `gk 저장`, …) work across the full subcommand tree. `gk guide` walks first-time users through git workflows step-by-step, independent of Easy Mode.
- **Actionable errors** — most errors print a second-line `hint:` with the concrete next command.

## Install

### Homebrew tap (recommended)

```bash
brew install x-mesh/tap/gk
# upgrade later:
brew upgrade x-mesh/tap/gk
```

### Linux / manual download

Download the latest binary from [GitHub Releases](https://github.com/x-mesh/gk/releases/latest):

```bash
# amd64
curl -sL https://github.com/x-mesh/gk/releases/latest/download/gk_$(curl -s https://api.github.com/repos/x-mesh/gk/releases/latest | grep tag_name | cut -d '"' -f4 | sed 's/^v//')_linux_amd64.tar.gz | tar xz -C /usr/local/bin gk

# arm64
curl -sL https://github.com/x-mesh/gk/releases/latest/download/gk_$(curl -s https://api.github.com/repos/x-mesh/gk/releases/latest | grep tag_name | cut -d '"' -f4 | sed 's/^v//')_linux_arm64.tar.gz | tar xz -C /usr/local/bin gk
```

Or manually:

```bash
# 1. Go to https://github.com/x-mesh/gk/releases/latest
# 2. Download gk_<version>_linux_amd64.tar.gz (or arm64)
# 3. Extract and move to PATH:
tar xzf gk_*.tar.gz
sudo mv gk /usr/local/bin/
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
gk pull                      # fetch + integrate @{u}; refuses on diverged
gk pull --rebase             # explicit consent: replay local on top of upstream
gk pull --fetch-only         # fetch without integrating
gk merge main                # precheck + merge main into current branch
gk sync                      # rebase onto local <base> (offline)
gk sync --fetch              # one-shot: fetch + ff local <base> + rebase
gk diff                      # color, line-numbered, word-level diff viewer
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
gk ship dry-run           # preview squash/version/changelog/tag/push plan
```

## Commands

### Daily
| Command | Alias | Description |
|---|---|---|
| `gk clone <owner/repo \| alias:owner/repo \| url>` | | Clone with short-form URL expansion. Bare `owner/repo` expands to `git@github.com:owner/repo.git` (ssh default, configurable). `--ssh`/`--https` override. `clone.hosts` maps aliases (`gl:`, `work:`). Optional `clone.root` + `clone.post_actions: [hooks-install, doctor]`. |
| `gk pull` | | Fetch + integrate the current branch's upstream (`@{u}`). Refuses on diverged histories without explicit consent; `--rebase` / `--merge` / `--fetch-only` choose; `--strategy rebase\|merge\|ff-only\|auto` for direct override. Writes a backup ref before any history-rewriting integration. Conflict pauses surface inline previews + `gk resolve` shortcuts |
| `gk diff` | | Terminal-friendly diff viewer with color, line numbers, and word-level highlights. `-i`/`--interactive` opens a file picker; `--staged`, `--stat`, `-U <n>`, `--no-pager`, `--no-word-diff`, `--json`. `<ref>`, `<ref>..<ref>`, `-- <path>` pass through to `git diff` |
| `gk merge <target>` | | Precheck, AI-plan, and merge a target branch into the current branch. Supports `--plan-only`, `--no-ai`, `--ff-only`, `--no-ff`, `--no-commit`, `--squash`, `--autostash` |
| `gk sync` | | Rebase the current branch onto local `<base>` (offline by default). `--fetch` for the explicit one-shot: fetch `<remote>/<base>`, fast-forward `refs/heads/<base>`, then integrate. Stale-base hint when local `<base>` differs from `<remote>/<base>` |
| `gk status` | `gk st` | Concise working-tree status (staged / unstaged / untracked / conflicted + ahead/behind), submodule-aware with `next:` hints. Pass `-f`/`--fetch` to refresh ↑N ↓N, `--watch` to refresh continuously, or `--exit-code` for scripts. Opt-in `--vis gauge,bar,progress,types,staleness,tree,conflict,churn,risk` overlays |
| `gk log` | `gk slog` | Short colored commit log; `--since 1w`, `--graph`, `--limit N`. Opt-in `--pulse`, `--calendar`, `--tags-rule`, `--impact`, `--cc`, `--safety`, `--hotspots`, `--trailers`, `--lanes` visualizations |

### Branches
| Command | Alias | Description |
|---|---|---|
| `gk branch list` | | List branches with `--stale <N>` / `--merged` / `--unmerged` / `--gone` filters |
| `gk branch clean` | | Delete merged branches while respecting protected list; `--gone` targets branches whose upstream was deleted |
| `gk branch pick` | | Interactive branch picker with non-TTY fallback |
| `gk branch set-parent <parent>` | | Record `branch.<current>.gk-parent = <parent>` so `gk status` compares divergence against the actual parent (stacked workflows). Validates against self/cycle (depth ≤10), tags, and non-existent refs; suggests the closest local branch on typos. |
| `gk branch unset-parent` | | Remove the `gk-parent` config entry. Idempotent. Status output reverts to base-relative divergence. |
| `gk branch-check` | | Validate current branch name against configured patterns |
| `gk switch [name]` | `gk sw` | Switch branches; `-m`/`--main` jumps to detected main, `-d`/`--develop` to develop/dev |

### Worktree
| Command | Alias | Description |
|---|---|---|
| `gk worktree` (no sub) | `gk wt` | Interactive TUI — list, add, remove, and cd into worktrees. `cd` spawns a `$SHELL` in the target dir (`exit` returns). `--print-path` flips to the `gwt() { cd "$(gk wt --print-path)"; }` alias pattern. |
| `gk worktree add <name>` | | Relative names resolve under `<worktree.base>/<worktree.project>/<name>` (default `~/.gk/worktree/<repo>/<name>`); absolute paths passthrough. Orphan-branch collisions surface an inline reuse/delete/cancel prompt. |
| `gk worktree list` | | Table or `--json` listing parsed from `git worktree list --porcelain` |
| `gk worktree remove <path>` | | Removes worktree; dirty/locked get a force prompt, stale admin entries auto-prune |

### Safety
| Command | Description |
|---|---|
| `gk push` | Guarded push: secret scan + protected-branch enforcement; `--force` routes through `--force-with-lease` |
| `gk ship` | Release ship gate: status/dry-run/squash modes, SemVer inference, version/CHANGELOG release commit, guarded branch/tag push. Tag push triggers the release workflow |
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
| `gk commit` | Group WIP (staged + unstaged + untracked) into semantic commit plans via an AI CLI and apply them. `-f/--force` skips review, `--dry-run` previews only, `--abort` restores HEAD to the latest backup ref. See **AI commit** section below |
| `gk pr` | Generate a structured PR description (Summary, Changes, Risk Assessment, Test Plan) from branch commits. `--output clipboard` copies directly; `--dry-run` previews the prompt |
| `gk review` | AI-powered code review on staged changes (`git diff --cached`) or a commit range (`--range ref1..ref2`). `--format json` for structured output |
| `gk changelog` | Generate a changelog grouped by Conventional Commit type from a commit range. `--from`/`--to` refs; defaults to latest tag..HEAD |

### Onboarding / config
| Command | Description |
|---|---|
| `gk guide [<workflow>]` | Step-by-step walkthrough of common git workflows (init → first commit, push, merge conflict, undo) for new users. Optional positional `<workflow>` skips the menu and starts that flow directly |
| `gk doctor` | Environment health report (git/pager/fzf/editor/config/hooks/gitleaks/backup-refs/ai-providers) with fix commands; `--json` for CI |
| `gk init [--only <target>] [--kiro] [--force]` | Analyze the project and scaffold `.gitignore`, `.gk.yaml`, and AI context files (`CLAUDE.md`, `AGENTS.md`) in one step. `--only gitignore\|config\|ai` narrows the run; `--kiro` also writes `.kiro/steering/`; an interactive huh form previews the plan before writing |
| `gk config init [--force] [--out <path>]` | Scaffold the commented YAML template at `$XDG_CONFIG_HOME/gk/config.yaml` (also auto-created on first `gk` run; skip with `GK_NO_AUTO_CONFIG=1`). Replaces `gk init config`, which remains as a backward-compatible alias |
| `gk hooks install [--commit-msg\|--pre-push\|--pre-commit\|--all] [--force]` | Write gk-managed hook shims under `.git/hooks/` (`--pre-commit` wires `gk guard check`) |
| `gk hooks uninstall [...]` | Remove gk-managed hooks (refuses to delete foreign ones) |
| `gk config show` | Print fully resolved config as YAML |
| `gk config get <key>` | Print a single config value by dot-path |

See [docs/commands.md](docs/commands.md) for full flag reference and [CHANGELOG.md](CHANGELOG.md) for per-release details.

## AI commit

`gk commit` analyses the current working tree (staged + unstaged + untracked), groups the changes into semantic commit plans via an external AI CLI, and applies one Conventional Commit per plan.

### Provider setup

`gk commit` drives **already-installed** AI CLI binaries — it never talks to remote LLM APIs directly, so no API key lives inside `gk`.

| Provider | Install | Auth |
|---|---|---|
| `anthropic` (Anthropic Claude) — **default** | No binary needed | `export ANTHROPIC_API_KEY=...` |
| `openai` (OpenAI) | No binary needed | `export OPENAI_API_KEY=...` |
| `nvidia` (NVIDIA) | No binary needed | `export NVIDIA_API_KEY=...` |
| `groq` (Groq) | No binary needed | `export GROQ_API_KEY=...` |
| `gemini` (Google) | `npm i -g @google/gemini-cli` or `brew install gemini-cli` | `export GEMINI_API_KEY=...` or run `gemini` once for OAuth |
| `qwen` (Alibaba) | `npm i -g @qwen-code/qwen-code` | `qwen auth qwen-oauth` or `export DASHSCOPE_API_KEY=...` |
| `kiro-cli` (AWS Kiro headless — note: **not** the `kiro` IDE launcher) | See [kiro.dev/docs/cli/installation](https://kiro.dev/docs/cli/installation) | `export KIRO_API_KEY=...` (Kiro Pro) or IDE OAuth session |

> **anthropic**, **openai**, **nvidia**, and **groq** call their respective Messages / Chat Completions APIs directly over HTTP — no external binary required. Other providers (`gemini`, `qwen`, `kiro-cli`) are driven as external CLI subprocesses.

Auto-detect order (when `ai.provider` is empty): **anthropic → openai → nvidia → groq → gemini → qwen → kiro-cli**. When no explicit `--provider` is given, a **Fallback Chain** tries each available provider in order, automatically moving to the next on failure.

Run `gk doctor` to verify each provider's install + auth status.

### Flags

```
gk commit [flags]

      --abort                      restore HEAD to the latest ai-commit backup ref and exit
      --allow-secret-kind strings  suppress secret findings of the given kind (repeatable)
      --ci                         CI mode — require --force or --dry-run, never prompt
      --dry-run                    show the plan and exit without committing
  -f, --force                      apply commits without interactive review
      --include-unstaged           include unstaged + untracked changes (default true)
      --lang string                override ai.lang (en|ko|...)
      --provider string            override ai.provider (anthropic|openai|nvidia|groq|gemini|qwen|kiro)
      --staged-only                only consider already-staged changes
  -y, --yes                        accept every prompt (alias for --force when non-TTY)
```

### Config

```yaml
# .gk.yaml (or ~/.config/gk/config.yaml)
ai:
  enabled: true              # master off-switch; GK_AI_DISABLE=1 also disables
  provider: ""               # "" = auto-detect (anthropic → openai → nvidia → groq → gemini → qwen → kiro-cli)
  lang: "en"                 # message language (BCP-47 short)
  anthropic:                 # Anthropic Claude — HTTP direct (Messages API), no binary needed
    # model: "claude-sonnet-4-5-20250929"  # default
    # endpoint: "https://api.anthropic.com/v1/messages"
    # timeout: "60s"
  openai:                    # OpenAI — HTTP direct (Chat Completions), no binary needed
    # model: "gpt-4o-mini"  # default
    # endpoint: "https://api.openai.com/v1/chat/completions"
    # timeout: "60s"
  nvidia:                    # NVIDIA provider — HTTP direct, no binary needed
    # model: "meta/llama-3.1-8b-instruct"  # default
    # endpoint: "https://integrate.api.nvidia.com/v1/chat/completions"
    # timeout: "60s"
  groq:                      # Groq provider — HTTP direct (OpenAI-compatible), no binary needed
    # model: "llama-3.3-70b-versatile"  # default
    # endpoint: "https://api.groq.com/openai/v1/chat/completions"
    # timeout: "60s"
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
- **Privacy Gate** — for remote providers (`Locality=remote`), automatically redacts secrets, deny_paths matches, and sensitive patterns from the outbound payload. Replaces matches with tokenized placeholders (`[SECRET_1]`, `[PATH_1]`). Aborts if >10 secrets detected. Use `--show-prompt` on any subcommand to inspect the redacted payload. Audit logging to `.gk/ai-audit.jsonl` when `ai.commit.audit` is enabled.
- **Deny paths** — matching files (`.env`, private keys, tfstate, …) are dropped before the payload leaves the process.
- **Git-state guard** — refuses to run mid-rebase / mid-merge / mid-cherry-pick so `MERGE_MSG` is never overwritten.
- **Backup ref** — each run writes `refs/gk/ai-commit-backup/<branch>/<unix>` before committing; `gk commit --abort` restores HEAD there.
- **Conventional lint loop** — `internal/commitlint.Parse/Lint` validates every message; failures trigger up to two provider retries with feedback injected into the prompt.
- **Path-rule override** — `_test.go`, `docs/*.md`, `.github/workflows/*.yml`, and lockfiles are always reclassified to `test`/`docs`/`ci`/`build` even if the provider picks a different type.

### Quick example

```bash
# Dry-run: see the plan without committing.
gk commit --dry-run

# Commit one-shot (no TUI).
gk commit --force --provider gemini

# Recover from a partial failure.
gk commit --abort
```

## pr / review / changelog

These commands use the provider's **Summarizer** capability. All shipped providers (`anthropic`, `openai`, `nvidia`, `groq`, `gemini`, `qwen`, `kiro-cli`) implement Summarizer.

### `gk pr`

Generate a structured PR description from the current branch's commits relative to the base branch.

```bash
gk pr                          # output to stdout
gk pr --output clipboard       # copy to clipboard
gk pr --dry-run                # preview the prompt
gk pr --provider nvidia --lang ko
```

Flags: `--output` (stdout|clipboard), `--dry-run`, `--provider`, `--lang`

### `gk review`

AI-powered code review on staged changes or a commit range.

```bash
gk review                      # review staged diff
gk review --range main..HEAD   # review a commit range
gk review --format json        # structured JSON output
```

Flags: `--range`, `--format` (text|json), `--dry-run`, `--provider`

### `gk changelog`

Generate a changelog from a range of commits, grouped by Conventional Commit type.

```bash
gk changelog                   # latest tag..HEAD, markdown
gk changelog --from v1.0.0 --to v1.1.0
gk changelog --format json
```

Flags: `--from`, `--to`, `--format` (markdown|json), `--dry-run`, `--provider`

## Global flags

| Flag | Description |
|---|---|
| `-d, --debug` | Emit diagnostic logs to stderr (also via `GK_DEBUG=1`). Each line carries an elapsed-since-start prefix, so you can see at a glance which stage is spending wall time. |
| `--dry-run` | Print actions without executing |
| `--easy` | Enable Easy Mode for this invocation (Korean term translation + emoji + hints). Equivalent to `GK_EASY=1` |
| `--no-easy` | Disable Easy Mode for this invocation, even if config / env enabled it |
| `--json` | JSON output where supported |
| `--no-color` | Disable color output |
| `--repo <path>` | Path to git repo (default: current directory) |
| `--verbose` | Verbose output |

### Easy Mode env vars

| Var | Default | Description |
|---|---|---|
| `GK_EASY` | unset | `1` / `true` enables Easy Mode globally; `0` / `false` forces it off |
| `GK_LANG` | `ko` | Message catalog language (BCP-47 short code; `en` and `ko` shipped) |
| `GK_EMOJI` | `true` | Prefix status sections with emoji (`📋` / `❌` / `💡`) |
| `GK_HINTS` | `verbose` | Hint verbosity: `verbose` / `minimal` / `off` |

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
