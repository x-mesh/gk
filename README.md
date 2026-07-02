<p align="center">
  <img src="assets/gk-logo.jpeg" alt="gk" width="520">
</p>

<p align="center">
  <strong>English</strong> · <a href="README.ko.md">한국어</a>
</p>

# gk — git helper

A small Go helper for everyday pull/log/status/branch work. It leans on two ideas: keep destructive operations recoverable (reflog-backed undo, time-machine restore, policies-as-code), and make diagnostics fast to reach for (`doctor`, `precheck`, `sync`).

[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![Release](https://img.shields.io/github/v/release/x-mesh/gk)](https://github.com/x-mesh/gk/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Why gk?

- **Safer pushes by default.** `gk push` scans the commits-to-push diff for AWS, GitHub, Slack, and OpenAI keys, plus PEM bodies. To force-push a protected branch you have to type the branch name yourself.
- **Time machine for HEAD.** `gk timemachine list` shows every recoverable state (reflog + gk backup refs). `gk timemachine restore <sha|ref>` writes a backup ref before it resets, autostashes if you ask, and refuses to run when a rebase or merge is already in progress.
- **Reflog-backed undo.** `gk undo` picks a past HEAD from the reflog (fzf or a numeric picker), resets to it, and leaves a backup ref at `refs/gk/undo-backup/<branch>/<unix>`. Undoing the undo is the same command run twice.
- **Policies as code.** `gk guard check` runs repo policy rules in parallel: secret scanning, commit size, required trailers, and so on. `gk guard init` scaffolds `.gk.yaml` with commented stubs you can uncomment selectively. Wire it into pre-commit with `gk hooks install --pre-commit`.
- **Dry-run any merge.** `gk precheck <target>` runs `git merge-tree` and reports conflicting paths without touching your working tree. CI gets exit 3 on conflict.
- **Local-first rebase.** `gk sync` rebases the current branch onto local `<base>` without touching the network. `gk sync --fetch` is the one-shot when you want the fetch too. If local `<base>` has fallen behind `<remote>/<base>`, you get a stale-base hint instead of a silent stale rebase.
- **Diverged-pull safety net.** When histories have diverged, `gk pull` stops and asks instead of silently rewriting your SHAs. The choices are `--rebase`, `--merge`, or `--fetch-only`; setting `pull.strategy` (or passing the flag) bypasses the prompt. Any integration that rewrites history writes a `refs/gk/backup/<branch>/<ts>` ref before it does so.
- **Conventional-Commits-aware hooks.** `gk hooks install` wires `commit-msg` → `gk lint-commit`, `pre-push` → `gk preflight`, and `pre-commit` → `gk guard check`. Managed hooks carry a marker, so reinstalling is idempotent and a foreign hook is never overwritten without `--force`.
- **Health at a glance.** `gk doctor` reports PASS/WARN/FAIL on git version, pager, `$EDITOR`, config validity, hook state, gitleaks install, and gk backup-ref accumulation. Each WARN/FAIL line carries a copy-paste fix command; `--ai` or `--verbose` adds optional AI-integration rows.
- **Self-update across install methods.** `gk update` detects whether the running binary came from Homebrew, `install.sh`, or `go install`, then takes the right path: forwards to `brew upgrade x-mesh/tap/gk` (auto-adds `--cask` when the binary lives under Caskroom, since the tap migrated formula → cask at v0.55), downloads + sha256-verifies + atomic-renames the binary in place (with a `.bak` fallback and a `sudo` step when `/usr/local/bin` is not user-writable) then re-links the `git-kit` alias beside it, or prints the matching `go install …@latest` command. `gk update --check` exits 1 when a newer release is available so cron and CI can gate on it.
- **Forget paths from history.** Committed a DB dump or a secrets file by mistake? Add the path to `.gitignore` and run `gk forget` — gk auto-detects tracked-but-ignored paths and hands them to `git filter-repo` (the modern replacement for `filter-branch`), with a backup ref + flat-text manifest written before the rewrite so you can roll back with `git update-ref --stdin`. Explicit paths (`gk forget db/ secrets.json`) bypass the auto-detect step. `--dry-run` previews the affected commit count without rewriting.
- **Easy Mode for new users.** `--easy` (or `output.easy: true` / `GK_EASY=1`) translates git terminology into Korean while keeping the original in parentheses (`commit` → `변경사항 저장 (commit)`), prefixes status sections with emoji, and tacks a context-aware next-step hint onto the last line. Even on a clean tree, the ↑/↓ counter feeds hints like `📤 서버에 올릴 커밋 N개 → gk push`. `gk guide` is a separate step-by-step git walkthrough that works whether or not Easy Mode is on.
- **Errors that tell you what to do.** Most errors print a second `hint:` line with the next command to run.

## Install

### Homebrew tap (recommended)

```bash
brew install --cask x-mesh/tap/gk
# upgrade later:
brew upgrade --cask x-mesh/tap/gk
```

The tap was migrated from a formula to a cask at v0.55 — the `--cask`
flag is required on macOS and Linux to pick up the current release.
If you installed before v0.55 and `brew upgrade` keeps reporting
v0.54.0, run `brew uninstall --formula x-mesh/tap/gk` once, then the
cask install above. `gk update` knows the difference and forwards the
right flag automatically.

### Linux / manual download

One-liner. Auto-detects your OS and arch, verifies sha256:

```bash
curl -fsSL https://raw.githubusercontent.com/x-mesh/gk/main/install.sh | sh
```

Pin a version or override the install dir:

```bash
curl -fsSL https://raw.githubusercontent.com/x-mesh/gk/main/install.sh \
  | GK_VERSION=v0.29.0 GK_INSTALL_DIR=/usr/local/bin sh
```

Or grab it manually from [GitHub Releases](https://github.com/x-mesh/gk/releases/latest). Filenames don't include the version, so the download URL stays the same across releases:

```bash
# linux amd64 — swap for linux_arm64 / darwin_amd64 / darwin_arm64 as needed
mkdir -p ~/.local/bin
curl -fsSL https://github.com/x-mesh/gk/releases/latest/download/gk_linux_amd64.tar.gz \
  | tar -xz -C ~/.local/bin gk
```

### go install

```bash
go install github.com/x-mesh/gk/cmd/gk@latest
```

Requires **git ≥ 2.38** (for `merge-tree --write-tree`; ≥ 2.40 preferred so `gk precheck` can enumerate conflicted paths by name). Run `gk doctor` after install to verify.

### Command name: `gk`, `git-kit`, or `git kit`

**Homebrew and the `install.sh` one-liner install the binary under two names**, so you always have a way in even when `gk` is taken:

| invoke | notes |
|--------|-------|
| `gk …` | short, the default |
| `git-kit …` | identical, never alias-shadowed |
| `git kit …` | git execs the `git-kit` binary as a native subcommand |

Installed with `go install` or a manual tarball instead? Those drop only `gk` — create the second name yourself once (point it at wherever the `gk` binary lives; for `go install` that's `$(go env GOPATH)/bin`):

```bash
ln -sf gk ~/.local/bin/git-kit
```

This matters because oh-my-zsh's `git` plugin defines `gk` as a `gitk` launcher, which shadows the `gk` binary. You don't have to fight it — just reach for one of:

- **Use `git kit` (or `git-kit`)** — works immediately, no config change.
- **Or drop the alias** in your `~/.zshrc`, after oh-my-zsh loads:

  ```zsh
  unalias gk gke 2>/dev/null
  ```

Help and usage text follow whichever name you typed, so `git kit push --help` reads `git-kit push …`, not `gk push …`. (One git quirk: the bare `git kit --help` gets turned into a man-page lookup — as it does for every custom subcommand. For the top-level help use `git kit help` or `git-kit --help`.)

## Quickstart

```bash
# Daily driver
gk clone JINWOO-J/playground # expand to git@github.com:JINWOO-J/playground.git
gk pull                      # fetch + integrate @{u}; refuses on diverged
gk pull --rebase             # explicit consent: replay local on top of upstream
gk pull --with-base          # also fast-forward local main from origin (FF-only, no checkout)
gk context --json            # one-call orientation for agents: branch/sync/dirty/next_actions
gk context --include=all     # + diff digest, recent log, pull forecast, per-remote drift fused in
gk pull --from tape42        # integrate from a secondary remote (mirror/org fork); tracking untouched
gk batch --plan - <plan.json # run several gk commands as one transaction from a JSON plan
gk agents install            # keep the compact gk usage contract in CLAUDE.md / AGENTS.md
gk agents install --global   # ...or in the global ~/.claude/CLAUDE.md + ~/.codex/AGENTS.md
gk agents check              # block status + version, local AND global scope (JSON under GK_AGENT)
gk precheck                  # forecast: will my next pull conflict? (read-only merge-tree)
gk diff --digest             # what changed, where: files · hunks · changed symbols, no patch body
gk rebase --plan-template    # history editing as a JSON contract: squash/reword/drop, no editor
gk commit --plan-template    # curated multi-commit as a JSON contract: you group, gk commits
gk land                      # wrap up: commit -f → pull --with-base → push, one transaction
gk promote                   # local wrap-up: commit → forward-merge into the parent, no push
GK_AGENT=1 gk <cmd>          # agent mode: uniform {state, ok, result|error} envelope
gk pull --fetch-only         # fetch without integrating
gk merge main                # precheck + merge main into current branch
gk sync                      # rebase onto local <base> (offline)
gk sync --fetch              # one-shot: fetch + ff local <base> + rebase
gk refresh                   # ff main + develop to their remotes (gk re); never leaves your branch
gk diff                      # color, line-numbered, word-level diff viewer
gk status                    # concise working-tree summary
gk next                      # plain-language status explanation and next steps
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
| `gk clone <owner/repo \| alias:owner/repo \| url>` | | Clone with short-form URL expansion. Bare `owner/repo` expands to `git@github.com:owner/repo.git` (ssh default, configurable). `--ssh`/`--https` override. `clone.hosts` maps aliases (`gl:`, `work:`); an alias with an `owner` becomes an account profile — `alias:repo` completes the owner, and optional `ssh_host` swaps an `~/.ssh/config` Host alias into ssh URLs for multi-account key separation. Optional `clone.root` + `clone.post_actions: [hooks-install, doctor]`. |
| `gk pull` | | Fetch + integrate the current branch's upstream (`@{u}`). Refuses on diverged histories without explicit consent; `--rebase` / `--merge` / `--fetch-only` choose; `--strategy rebase\|merge\|ff-only\|auto` for direct override. A dirty (tracked) tree is auto-stashed around integration by default — `stashed N / restored N`, no prompt, CI no longer refuses; `--no-autostash` (or `pull.autostash: false`) restores the old prompt/refuse gate. Writes a backup ref before any history-rewriting integration. Conflict pauses surface inline previews and explicit `gk resolve` follow-up commands; AI conflict resolution remains opt-in (`gk resolve --ai --dry-run` first when risk matters). |
| `gk diff` | | Terminal-friendly diff viewer with color, line numbers, and word-level highlights. `-i`/`--interactive` opens a file picker; `--staged`, `--stat`, `-U <n>`, `--no-pager`, `--no-word-diff`, `--json`. `<ref>`, `<ref>..<ref>`, `-- <path>` pass through to `git diff` |
| `gk merge <target>` | | Precheck, AI-plan, and merge a target branch into the current branch. Supports `--plan-only`, `--no-ai`, `--ff-only`, `--no-ff`, `--no-commit`, `--squash`, `--autostash` |
| `gk rebase` | | Declarative `git rebase -i` without the editor session: `--plan-template` emits the commit range as JSON, `--plan <file\|->` validates and executes it (pick/squash/fixup/reword/drop). Every commit must be addressed exactly once, merge commits are refused, pushed commits are guarded behind `--allow-pushed`; a backup ref is written first and conflicts pause with the standard `gk continue`/`gk abort` contract |
| `gk sync` | | Rebase the current branch onto local `<base>` (offline by default). `--fetch` for the explicit one-shot: fetch `<remote>/<base>`, fast-forward `refs/heads/<base>`, then integrate. Stale-base hint when local `<base>` differs from `<remote>/<base>` |
| `gk status` | `gk st` | Concise working-tree status (staged / unstaged / untracked / conflicted + ahead/behind), submodule-aware with `next:` hints. Pass `--ai` for a plain-language explanation, `-f`/`--fetch` to refresh ↑N ↓N, `--watch` for a live change-feed (fsnotify timeline of which files change as you/an agent edit, with the latest commit's short-sha, age, and subject pinned in the header so you can tell a fresh commit from an old one; `[s]` toggles the full status dashboard), or `--exit-code` for scripts. Opt-in `--vis gauge,bar,progress,types,staleness,tree,conflict,churn,risk` overlays |
| `gk log` | `gk slog` | Short colored commit log; `--since 1w`, `--graph`, `--limit N`. `--behind` / `--ahead` (with `--fetch` to refresh, `--base` to compare against the base branch instead of `@{u}`) preview incoming/outgoing commits before `gk pull` / `gk push`. A `──┤ ↑ N unmerged → <base> ├──` divider marks where the branch runs ahead of its base, drawn by default whenever you're off the base branch (and merged with the `--safety` push divider when they land on the same row). Opt-in `--pulse`, `--calendar`, `--tags-rule`, `--impact`, `--cc`, `--safety`, `--merged`, `--hotspots`, `--trailers`, `--lanes` visualizations |
| `gk local` | `gk lo` | One screen for everything that lives only on this machine: working-tree changes (unstaged/staged/conflicts), unpushed commits (uses `@{u}`, falls back to any remote-tracking ref when there's no upstream), and stash. `-n N`, `--json` |

### Branches
| Command | Alias | Description |
|---|---|---|
| `gk branch list` | | List branches with `--stale <N>` / `--merged` / `--unmerged` / `--gone` filters |
| `gk branch clean` | | Delete merged branches while respecting protected list; `--gone` targets branches whose upstream was deleted |
| `gk branch pick` | | Interactive branch picker with non-TTY fallback |
| `gk branch set-parent <parent>` | | Record `branch.<current>.gk-parent = <parent>` so `gk status` compares divergence against the actual parent (stacked workflows). Validates against self/cycle (depth ≤10), tags, and non-existent refs; suggests the closest local branch on typos. |
| `gk branch unset-parent` | | Remove the `gk-parent` config entry. Idempotent. Status output reverts to base-relative divergence. |
| `gk branch-check` | | Validate current branch name against configured patterns |
| `gk switch [name]` | `gk sw` | Switch branches; `--fetch` refreshes remote branches first, `-m`/`--main` jumps to detected main, `-d`/`--develop` to develop/dev. Branches checked out elsewhere show a `WORKTREE` column; moving a protected branch into a linked worktree asks for confirmation (`--detach` to just view it). When the terminal is too narrow the picker drops the lowest-priority columns whole (a `+N cols · widen` note shows the count) — BRANCH and AGE survive longest |

### Worktree
| Command | Alias | Description |
|---|---|---|
| `gk worktree` (no sub) | `gk wt` | Interactive TUI — list (with `HASH` + `AGE` columns), add, remove, and cd into worktrees. `cd` spawns a `$SHELL` in the target dir (`exit` returns). `--print-path` flips to the `gwt() { cd "$(gk wt --print-path)"; }` alias pattern. Narrow terminals drop the lowest-priority columns whole (HASH first), keeping BRANCH and AGE longest. |
| `gk worktree add <name>` | | Relative names resolve under `<worktree.base>/<worktree.project>/<name>` (default `~/.gk/worktree/<repo>/<name>`); absolute paths passthrough. Orphan-branch collisions surface an inline reuse/delete/cancel prompt. After creating, offers to bootstrap the worktree (`--init`/`--no-init` skip the prompt). |
| `gk worktree acquire <branch>` | | Agent setup path: create or reuse a managed worktree for `<branch>`, run `worktree.init` by default, and return the ready path (`--json` gives `{path, branch, created, reused, init}`). |
| `gk worktree init [path]` | | Reconstitute a worktree's gitignored state from `worktree.init` in `.gk.yaml`: `link` (symlink secrets like `.env`), `copy` (per-worktree files), `run` (`npm ci`, `uv sync`). Idempotent, so it doubles as a setup-retry. With no config, detects package manifests — at the root, or in nested monorepo projects (`frontend/`, `backend/`) when the root has none — and proposes a block (`--save` to persist, `--dry-run` to preview). |
| `gk worktree list` | | Table or `--json` listing parsed from `git worktree list --porcelain` |
| `gk worktree remove <path>` | | Removes worktree; dirty gets a force prompt. A locked worktree is gated on whether its lock holder is still running: a stale lock (dead pid) unlocks+removes under `--force`; a live one is refused and needs `--force-locked` to override. Stale admin entries auto-prune |
| `gk worktree run <branch> -- <cmd>` | | Create (or reuse) a worktree for `<branch>`, run `<cmd>` with the worktree as its cwd, and exit with the command's code — the single-shot CLI form of an isolated parallel task. `--cleanup` reclaims the worktree when the command succeeds (and deletes the branch if this call created it); `--from <ref>` bases a new branch, `--init` now bootstraps reused worktrees too. |
| `gk worktree finish` | | Agent wrap-up path from inside a worktree: run `gk promote` by default (or `gk land --to <target>` with `--push`), then optionally `--cleanup` the linked worktree and `--delete-branch`. With `--gate "<cmd> {patch}"` (or `--panel-review`) it runs an external quality gate against the exact merge patch under a target lock — `--gate-phase before` blocks the merge on failure, `after` pauses it (exit 3) with a resume/abort contract. |
| `gk worktree cleanup` | | Bulk reclaim safe finished worktrees. Default is a dry-run report; `-y` applies. Skips current, dirty, live-locked, protected, detached/bare, and unmerged worktrees; `--stale 7d` and `--delete-branches` narrow/apply cleanup policy. |
| `gk fleet` | | Live supervision dashboard for parallel work — every worktree at once (branch, ahead/behind, dirty/conflict, last-changed file, current), built for several AI agents each in a worktree. A merged **change feed** under the table shows which files changed in which worktree as they happen (`e` toggle; `--feed-stats` adds +/− counts); with filesystem watches the dashboard reacts to edits instantly and polling drops to a heartbeat. `j`/`k` move, `enter` detail (recent events + reap suggestion), `w`→`gk status --watch` drill-down, `f`/`s` filter (all→busy→stuck) and sort (default→activity→status), `r` refresh, `q` quit. **Multi-repo**: `--repos`/`--scan`/`--all` (or `fleet.*` config) span separate repositories — worktrees group per repo (fold with `space`), `--json` stays a flat array tagged `repo`/`repo_root`. `--json` (or `GK_AGENT=1`) emits a one-shot snapshot; **`--events`** streams NDJSON transitions (file-changed / status-changed / op-start / op-end / land-ready) for orchestrators, and `fleet.notify` maps conflict/paused/land_ready to shell hooks. |
| `gk prompt-info` | | Emit a compact label for shell prompts. `plain` (default) prints `wt` (or `wt:<basename>` when the dir disagrees with the branch) inside a linked worktree, empty otherwise. `--format=segment` prints `<repo>/<branch>` for any repo — designed to replace starship's `$directory` + `$git_branch` with a single deduplicated label. `--format=json` returns `{linked, repo, name, path, branch}`. Detection via `git rev-parse --git-dir` vs `--git-common-dir` (~30ms). |

### Safety
| Command | Description |
|---|---|
| `gk push` | Guarded push: secret scan + protected-branch enforcement; `--force` routes through `--force-with-lease`; `-n`/`--skip-scan` skips the secret scan |
| `gk ship` | Release pipeline: status/dry-run/squash modes, SemVer inference (0.x keeps breaking at minor), version/CHANGELOG release commit (drafts the section from commits when [Unreleased] is empty), guarded branch/tag push, then config-driven `ship.watch` (CI tracking) and `ship.verify` (artifact checks). `--dry-run --json` emits the machine-readable plan; `--preflight` runs just the checks (validate before a real ship) |
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
| `gk ignore <path>...` | | Deterministic (no AI) "stop including this file in git": appends each path to `.gitignore` (directories get a trailing `/`) and runs `git rm --cached` on any tracked path so it stops being tracked while the working-tree file stays. `--commit` finalizes it in one commit; `--dry-run` previews. For erasing a path that is already in history, use `gk forget`. Also reachable via natural language: `gk do "<file>을 git에 포함하고 싶지 않아"` routes here deterministically |
| `gk forget [path...]` | | Remove paths from the entire git history. Two engines (`--engine`, config `forget.engine`): the default `native` is gk's built-in rewrite (fast-export→filter→fast-import in-process, no external install, differentially tested SHA-identical to filter-repo); `filter-repo` delegates to `git filter-repo` (requires it on PATH) — needed for inputs native refuses (shallow clones, replace refs). Both engines rewrite only branches and tags — backup refs and remote-tracking refs stay untouched — and keep pre-rewrite objects reachable, so the printed rollback genuinely works (reclaim disk later with `git gc --prune=now`). With no args, auto-detects tracked-but-ignored paths from `.gitignore`. Writes a backup ref `refs/gk/forget-backup/<branch>/<unix>` (visible in `gk timemachine list`) plus a flat manifest under `.git/gk/` before the rewrite; the native engine also writes a commit map (old→new SHA). `--analyze` reports unique blob count + total bytes per target without rewriting; with no targets it falls through to a repo-wide audit grouped by `--depth N` (default 1) capped at `--top N` (default 20). `--sort size\|churn\|name` switches ranking (churn surfaces rewrite-heavy paths). `--bar=auto\|filled\|block\|none` chooses bar style (htop / du-dust style on TTY). `--json` emits a stable machine-readable document for CI. `-i`/`--interactive` opens a multi-select picker over audit results and feeds the chosen paths back into the rewrite pipeline. `--keep <glob>` excludes paths from the forget set (`filepath.Match`, repeatable). Dirty entries inside forget targets are accepted; outside changes abort unless `--force-dirty`. Honours `--dry-run` and `--yes` |
| `gk wipe` | | `reset --hard` + `clean -fd`; backs up pre-wipe HEAD at `refs/gk/wipe-backup/...` |
| `gk restore --lost` | | Surface dangling commits and blobs with cherry-pick hints |
| `gk edit-conflict` | `gk ec` | Open `$EDITOR` at the first `<<<<<<<` marker with editor-aware cursor jump |
| `gk continue` | | Continue interrupted rebase/merge/cherry-pick |
| `gk abort` | | Abort interrupted rebase/merge/cherry-pick |
| `gk bisect` | | Find the commit that introduced a regression by binary search, run in a throwaway detached worktree so your tree/HEAD stay untouched. Automatic with `--good <ref> --bad <ref> -- <command>` (delegates to `git bisect run`, returns the culprit), or manual: omit `--` to pause on each candidate and step with `gk bisect good\|bad\|skip` (`reset` to end). `gk context`/`gk fleet` surface an active bisect. |
| `gk wip` / `gk unwip` | | Quick throwaway WIP commit for context switching; `unwip` restores changes to the working tree |
| `gk snapshot` / `gk snapshots` | | Non-destructive safety-net snapshot of the working tree to `refs/wip/<branch>` (untracked included, `.gitignore` respected); never touches HEAD/index, never pushes. `restore [n]` brings one back, auto-backing-up a dirty tree first. `-q` suits a Stop hook |

### Continuous

| Command | Alias | Description |
|---|---|---|
| `gk follow [branch] [-- <hook>...]` | | Foreground watcher that polls a **remote** branch and, each time it advances, hard-resets the local checkout to the remote tip (GitOps mirror) and runs a hook once. Omit `branch` to follow the current branch. Zero-infra "git-sync + watchexec" for dev boxes, agent sandboxes, and single-container deploys — supervised externally (systemd/docker/k8s), no built-in daemon. A backup ref is written before every reset (recover with `git reset --hard <backup-ref>`); an uncommitted working tree is refused unless `--discard-dirty`. A non-zero hook exit backs the poll off exponentially. `--remote`, `--interval` (default 30s), `--run`, `--once`. Not to be confused with `gk status --watch` (local file-change feed). Ships as a container (`Dockerfile`). |

### AI
| Command | Description |
|---|---|
| `gk commit` | Group WIP (staged + unstaged + untracked) into semantic commit plans via an AI CLI and apply them. `-f/--force` skips review, `-i/--interactive` groups files into commits by hand in a TUI (no AI; builds the same plan as `--plan`), `--dry-run` previews only, `--abort` restores HEAD to the latest backup ref. See **AI commit** section below |
| `gk next` | Explain the current repository state in plain language and suggest safe next commands. Falls back to a local rule-based plan when no AI provider is available. `--run`/`-r` executes the top recommended step (from gk's deterministic allowlist, never free-form AI output) after confirmation; risky commands are refused |
| `gk pr` | Generate a structured PR description (Summary, Changes, Risk Assessment, Test Plan) from branch commits. `--output clipboard` copies directly; `--dry-run` previews the prompt |
| `gk review` | AI-powered code review on staged changes (`git diff --cached`) or a commit range (`--range ref1..ref2`). `--format json` for structured output |
| `gk changelog` | Generate a changelog grouped by Conventional Commit type from a commit range. `--from`/`--to` refs; defaults to latest tag..HEAD |

### Onboarding / config
| Command | Description |
|---|---|
| `gk guide [<workflow>]` | Step-by-step walkthrough of common git workflows (init → first commit, push, merge conflict, undo) for new users. Optional positional `<workflow>` skips the menu and starts that flow directly |
| `gk doctor` | Environment health report (git/pager/editor/config/hooks/gitleaks/backup-refs) with fix commands; `--ai` or `--verbose` adds AI-provider rows; `--json` for CI |
| `gk update [--check] [--force] [--to <vX.Y.Z>]` | Self-update. Detects brew / `install.sh` / `go install` from `os.Executable()` and dispatches: brew → `brew upgrade x-mesh/tap/gk` (auto-adds `--cask` when the binary is under Caskroom, since the tap moved formula → cask at v0.55); manual → download `gk_<os>_<arch>.tar.gz` from the release, verify against `checksums.txt`, atomic rename next to the running binary then re-link the `git-kit` alias (sudo escalates when needed); go-install → print the `go install …@latest` hint. `--check` exits 0/1 without downloading; `--to` pins a specific tag (manual installs only) |
| `gk init [--only <target>] [--kiro] [--ai-gitignore] [--force]` | Analyze the project and scaffold `.gitignore` plus `.gk.yaml` in one step. `--only gitignore\|config\|ai` narrows the run; `--kiro` writes `.kiro/steering/`; `--ai-gitignore` asks the configured AI provider for extra ignore patterns after confirmation; an interactive huh form previews the local plan before writing. When the repo has no `origin`, init offers to wire one from a `clone.hosts` account profile (picker in config declaration order) or a direct `owner/repo`/URL; non-interactive runs use `--remote <alias\|alias:repo\|owner/repo\|url>` (+`--name`, `--ssh`/`--https`), and the JSON result carries `result.remote` |
| `gk agents [print\|install\|check]` | Keep gk's usage contract — the block that tells an agent to route git through gk instead of probing with raw git — current inside instruction files. `install` writes the compact block to `CLAUDE.md`/`AGENTS.md` at the repo root; `--global` targets the shared `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md` instead (parent dirs created as needed); `--full` opts into the longer reference block. `check` reports each file's block status and version across both scopes — local and global — and flags any left on an older gk; `print` dumps the compact block to stdout (`--full` for the detailed block). The contract ships inside the binary, so it always matches the installed gk |
| `gk agents hook [install\|uninstall\|status]` | The **enforcement** companion to the contract block: register a Claude Code PreToolUse(Bash) hook in `settings.json` that steers raw git to git-kit at the point of a command (`--mode warn` default surfaces a note; `--mode block` denies covered raw git). Surgical, non-destructive settings edit (only the gk entry, `.bak` first, `--dry-run` to preview); `--global` for `~/.claude/settings.json`. `uninstall` reverts. Backed by `gk hint` |
| `gk hint [command]` | Map one raw `git` command (argument or stdin) to the git-kit verb that covers it — the mapping behind `gk agents hook` and `gk session audit`. `--json` emits `{covered, kind, covered_by, suggestion, matched}`; `--exit-code` exits 1 when a replacement exists. Plumbing (`rev-parse`, `config`, …) and already-git-kit commands report not-covered |
| `gk session audit [paths...]` | Read local Codex/Claude JSONL sessions (local-only, nothing leaves the machine) and report where agents still fall back to raw git: adoption rate, covered/uncovered findings with per-subcommand breakdown (`one_shot` marks low-turn-leverage gaps), and a ready-to-run `batch --plan` payload per shell chain. `--since 30d` windows the corpus by file mtime; `--metric=turns\|both` adds the turn-reduction view (`--viz` graph); `--record` appends to `~/.gk/audit-history.jsonl` and `--trend` reads it back (in `--json`, as `result.trend[]`); `--max-files N` caps the newest files scanned |
| `gk config init [--force] [--out <path>]` | Scaffold the commented YAML template at `$XDG_CONFIG_HOME/gk/config.yaml` (also auto-created on first `gk` run; skip with `GK_NO_AUTO_CONFIG=1`). Replaces `gk init config`, which remains as a backward-compatible alias |
| `gk hooks install [--commit-msg\|--pre-push\|--pre-commit\|--all] [--force]` | Write gk-managed hook shims under `.git/hooks/` (`--pre-commit` wires `gk guard check`) |
| `gk hooks uninstall [...]` | Remove gk-managed hooks (refuses to delete foreign ones) |
| `gk config show` | Print fully resolved config as YAML |
| `gk config get <key>` | Print a single config value by dot-path |

See [docs/commands.md](docs/commands.md) for full flag reference and [CHANGELOG.md](CHANGELOG.md) for per-release details.

## AI commit

`gk commit` looks at the current working tree (staged + unstaged + untracked), asks an AI to split the changes into separate commits, and applies one Conventional Commit per group.

### Provider setup

For `anthropic`, `openai`, `nvidia`, and `groq`, `gk commit` calls the API directly over HTTP. Your env-var key goes straight to the provider, and `gk` never stores it. For `gemini`, `qwen`, and `kiro-cli`, `gk commit` shells out to the already-installed CLI and lets it handle auth.

| Provider | Install | Auth |
|---|---|---|
| `anthropic` (Anthropic Claude) — **default** | No binary needed | `export ANTHROPIC_API_KEY=...` |
| `openai` (OpenAI) | No binary needed | `export OPENAI_API_KEY=...` |
| `nvidia` (NVIDIA) | No binary needed | `export NVIDIA_API_KEY=...` |
| `groq` (Groq) | No binary needed | `export GROQ_API_KEY=...` |
| `gemini` (Google) | `npm i -g @google/gemini-cli` or `brew install gemini-cli` | `export GEMINI_API_KEY=...` or run `gemini` once for OAuth |
| `qwen` (Alibaba) | `npm i -g @qwen-code/qwen-code` | `qwen auth qwen-oauth` or `export DASHSCOPE_API_KEY=...` |
| `kiro-cli` (AWS Kiro headless, not the `kiro` IDE launcher) | See [kiro.dev/docs/cli/installation](https://kiro.dev/docs/cli/installation) | `export KIRO_API_KEY=...` (Kiro Pro) or IDE OAuth session |

Auto-detect order (when `ai.provider` is empty): `anthropic → openai → nvidia → groq → gemini → qwen → kiro-cli`. Without an explicit `--provider`, the fallback chain walks this list and moves to the next provider on failure.

Run `gk doctor --ai` (or `gk doctor --verbose`) to verify each provider's install + auth status.

### Flags

```
gk commit [flags]

      --abort                      restore HEAD to the latest ai-commit backup ref and exit
  -S, --allow-secret-kind strings  suppress secret findings of the given kind (repeatable; 'all' bypasses every finding)
      --ci                         CI mode — require --force or --dry-run, never prompt
      --dry-run                    show the plan and exit without committing
  -f, --force                      apply commits without interactive review
      --force-wip                  unwrap WIP chain even when some commits are already pushed (rewrites pushed history; requires force-push afterward)
      --include-unstaged           include unstaged + untracked changes (default true)
      --lang string                override ai.lang (en|ko|...)
  -n, --no-verify                  bypass the noise + secret guards and the privacy-gate abort threshold (secrets are reported, then committed; payload redaction to remote AI still applies)
      --no-wip-unwrap              skip detection/unwrap of WIP-like commits in HEAD chain
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
  providers:                 # custom, user-named providers (point `provider:` at any name)
    my-gateway:              # built from `format` below; built-in names need no entry here
      format: openai         # wire protocol: openai | anthropic | nvidia | groq (default openai)
      endpoint: "https://your-gateway.example.com/v1/chat/completions"
      model: "your-model"
      # api_key: "..."
  commit:
    mode: "interactive"      # interactive | force | dry-run (CLI flags override)
    max_groups: 10
    # model: "kiro/claude-haiku-4.5"  # cheaper/faster model for commits only; chat/advice keep ai.<provider>.model. One-shot: gk commit --model <id>
    max_tokens: 24000
    timeout: "120s"          # total deadline across retries; classify on a large tree is slow
    allow_remote: true       # repo-wide: set false to block remote providers for EVERY AI command, not just commit
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
  assist:
    mode: "off"              # off | suggest | auto
    status: true             # enables gk status --ai / gk next surfaces
    include_diff: false      # add the (truncated, privacy-gated) diff so the assistant reasons about WHAT changed
    diff_budget: 8000        # max diff bytes when include_diff is on
    max_tokens: 1200         # response cap
    timeout_secs: 8          # per-call timeout; falls back to local guidance on timeout
    cache: true              # cache by repo state (.git/gk-ai-cache); unchanged tree reuses the answer
```

`gk status --ai` is grounded on structured repo facts and refuses to hallucinate
destructive commands: the prompt forbids them, and any `reset --hard` / `push --force`
that still slips into the answer is flagged with a caution footer. `mode: auto` skips
the provider entirely when the tree is idle (clean + in sync). `gk status --ai --json`
prints the structured facts (branch, counts, recommended commands) for editors/scripts
without calling a provider.

### Safety rails (every run)

- **Secret gate.** `internal/secrets.Scan` plus `gitleaks` (if installed) scan the payload. Any finding aborts the commit, including under `--force`. Pass `--allow-secret-kind <kind>` to whitelist one kind for the current run. To bypass everything, use `--allow-secret-kind all` or `-n/--no-verify` — but each finding is reported on stderr and then written into history as-is, so rotate any real credential. Redaction to the remote AI always applies; `-n` additionally lifts the privacy gate's abort threshold (next item).
- **Privacy gate.** For remote providers (`Locality=remote`), the outbound payload is scrubbed: secrets, paths matching `deny_paths`, and sensitive patterns are replaced with tokens like `[SECRET_1]` or `[PATH_1]`. The run aborts if more than ten secrets show up in a single payload — raise `ai.commit.privacy.max_secrets`, or skip that threshold with `--skip-privacy` (`-n`/`--no-verify` implies it; redaction is unaffected either way). `--show-prompt` lets you inspect the redacted version. With `ai.commit.audit` on, the redactions are logged to `.gk/ai-audit.jsonl`.
- **Deny paths.** Files like `.env`, private keys, and tfstate are dropped before the payload leaves the process.
- **Git-state guard.** `gk commit` refuses to run while a rebase, merge, or cherry-pick is in progress, so `MERGE_MSG` never gets overwritten.
- **Backup ref.** Each run writes `refs/gk/ai-commit-backup/<branch>/<unix>` before committing. `gk commit --abort` restores HEAD to it.
- **Conventional lint loop.** Every generated message goes through `internal/commitlint.Parse/Lint`. On failure the previous lint errors are injected into the next prompt and the provider is asked again, up to two retries.
- **Path-rule override.** `_test.go`, `docs/*.md`, `.github/workflows/*.yml`, and lockfiles are always reclassified to `test` / `docs` / `ci` / `build`, even if the provider picks a different type.

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
make install        # installs ~/.local/bin/{gk-dev,git-kit-dev} (safe default, no Homebrew collision)
make install-gk     # installs ~/.local/bin/{gk,git-kit} (use the dev build AS gk)
make test           # go test ./... -race -cover
make lint           # golangci-lint run
make fmt            # gofmt + go mod tidy
```

Requires Go 1.25+ and git 2.38+.

## License

[MIT](LICENSE)
