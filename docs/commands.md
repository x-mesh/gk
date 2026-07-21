# gk Command Reference

All subcommands accept the following global flags:

| Flag | Description |
|------|-------------|
| `-d, --debug` | Emit diagnostic logs (every git subprocess, CLI AI-provider subprocess, and HTTP AI-provider call — the latter logged as `ai <provider> model=<model>` — each with its duration and exit status, plus stage boundaries per command: base/upstream/strategy resolution in `pull`, URL resolution in `clone`, path resolution in `worktree add`, secret-scan results in `push`, etc.) to stderr in dim gray. Also honored via `GK_DEBUG=1`. Each log line is prefixed with `[debug +N.NNNs]` showing elapsed time since the first debug call, so a glance reveals where wall time is being spent. |
| `--dry-run` | Print actions without executing |
| `--json` | JSON output where supported |
| `--no-color` | Disable color output |
| `--repo <path>` | Path to git repo (default: current directory) |
| `--verbose` | Verbose output |

### Progress feedback for long operations

Operations that would otherwise hang the terminal silently while gk waits on an external process (`git fetch`, AI provider CLIs, gitleaks scan) now render a braille-dot spinner on stderr. The first frame is delayed 150ms, so sub-150ms calls never flash a spinner that would only clear itself. Spinners are suppressed when stderr is not a TTY (pipes, CI, `2>file`), and a line announcing each long stage is printed before the spinner starts so the transcript stays readable even without the animation.

---

## gk ship

Run the final release gate: require a clean working tree, infer or accept the next SemVer tag, optionally squash local-only commits, bump release metadata, promote `CHANGELOG.md`'s `[Unreleased]` notes, create an annotated tag, and push the branch plus tag. In this repository, pushing `v*` tags triggers the GitHub Actions release workflow and GoReleaser publishes the GitHub Release plus Homebrew tap update.

### Synopsis

```
gk ship [status|dry-run|squash|auto|patch|minor|major] [flags]
```

### Modes

| Mode | Description |
|------|-------------|
| `gk ship` | Interactive release: print the plan, run preflight, update metadata, commit, tag, push |
| `gk ship auto` | Same as default, but skips confirmation (`--yes`) |
| `gk ship status` | Read-only summary of commits since the latest tag and the inferred next tag |
| `gk ship dry-run` | Full plan preview without preflight, metadata writes, tag, or push |
| `gk ship squash` | Squash commits since the latest tag into one local commit; no bump, tag, or push |
| `gk ship patch\|minor\|major` | Release with an explicit bump type |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--version <vX.Y.Z>` | auto | Explicit release version; `v` prefix is optional |
| `--major` | false | Bump the latest tag by one major version |
| `--minor` | false | Bump the latest tag by one minor version |
| `--patch` | false | Bump the latest tag by one patch version |
| `--no-release` | false | Push the branch without creating or pushing a release tag |
| `--push` | true | Push the branch and release tag; pass `--push=false` to tag locally only |
| `--skip-preflight` | false | Skip configured preflight checks |
| `--preflight` | false | Run only the configured preflight checks (lint/test/…) and exit — validate before a real ship. Builds no plan, so it works on a dirty tree and never tags or pushes. Runs every step (not fail-fast) so one call surfaces all problems; exits non-zero on failure (chainable: `gk ship --preflight && gk ship -y`). With `--json`/`GK_AGENT` emits `{result, steps:[{name,command,ok}], failed_step}` (exit 0 — branch on `result`). |
| `-n`, `--no-verify` | false | Skip the secret-pattern scan before pushing (matches `gk commit -n` / `gk push -n`) |
| `--allow-dirty` | false | Allow shipping with a dirty working tree |
| `--allow-non-base` | false | Allow release tags from a non-base branch |
| `-y`, `--yes` | false | Skip the final confirmation prompt. `ship.auto_confirm: true` makes this the default; `--yes=false` restores the prompt for one run |
| `--wait` | true | Run the post-tag watch/verify pipeline. `--wait=false` returns right after the push and prints the skipped commands; `ship.wait: false` makes that the default, `--wait` re-enables it for one run |
| `--dry-run` | false | Print the ship plan without tagging or pushing |
| `--json` (global) | false | With `--dry-run`, emit the release plan as JSON (branch, bump + 0.x downgrade marker, next tag, version files, changelog draft, preflight/watch/verify steps). Refused without `--dry-run` |

### Metadata updates

`gk ship` bumps every file listed in `ship.version_files` (paths relative to the repo root). Each entry is either a bare path — the format is inferred from the filename — or a `{path, pattern, key}` mapping for formats with no native handler. Native handlers (bare path is enough):

| File | What it rewrites |
|------|------------------|
| `VERSION` | the whole file |
| `package.json` / `marketplace.json` | the `"version"` field |
| `pyproject.toml` | `version` under `[project]` or `[tool.poetry]` only — dependency pins are left alone |
| `Cargo.toml` | `version` under `[package]` only |
| `*.py` | the `__version__ = "…"` assignment |
| `pubspec.yaml` / `Chart.yaml` | the top-level `version:` key |

For anything else, give the entry a `pattern` (a literal template with one `{version}` placeholder — works on any text file) or a `key` (a dotted key path into a YAML file, comments preserved):

```yaml
ship:
  version_files:
    - pyproject.toml                       # native handler
    - path: src/myapp/__init__.py
      pattern: '__version__ = "{version}"' # any text file
    - path: helm/Chart.yaml
      key: appVersion                      # dotted YAML key path
```

When the list is unset, ship falls back to the **first** auto-detected file in repo root: `VERSION`, `package.json`, `marketplace.json`, `pyproject.toml`, `Cargo.toml`, then `pubspec.yaml`. A listed file whose format has no handler and no `pattern`/`key` is an error, not a silent skip — ship refuses rather than tag a release whose version never moved. `gk init` seeds `ship.version_files` from the manifests it detects, so most projects never write this by hand. If no version file exists, the release is tag-only. When `CHANGELOG.md` contains a non-empty `## [Unreleased]` section, `gk ship` promotes that section into `## [X.Y.Z] - YYYY-MM-DD` and commits the metadata before tagging. When `[Unreleased]` is **empty**, ship drafts the section from the conventional commits in the release range (`feat` → Added, `refactor`/`perf` → Changed, `fix` → Fixed, breaking commits marked `(breaking)`); the draft is shown in the plan and at the confirm gate before anything is written. Commits with other types (`docs`, `chore`, `ci`, …) stay out of the draft — if nothing maps, the changelog is left untouched as before.

### Version inference

When no explicit version or bump flag is provided, `gk ship` reads commits since the latest tag:

| Commit shape | Bump |
|--------------|------|
| `feat!:` or `BREAKING CHANGE:` | major (minor while on 0.x) |
| `feat:` / `feat(scope):` | minor |
| everything else | patch |

While the latest tag is still `v0.*`, an inferred breaking change bumps the **minor** version (SemVer 0.x convention) and the plan notes the downgrade — graduating to v1.0.0 is always an explicit `--major` / `--version` decision.

### Release pipeline (config)

The `ship:` config section extends the release beyond the git half. All three lists reuse the preflight step shape (`name`, `command`, `continue_on_failure`):

```yaml
ship:
  watch:                      # after the tag push — blocking CI tracking
    - name: ci
      command: gh run watch $(gh run list --workflow release --limit 1 --json databaseId --jq '.[0].databaseId') --exit-status
  verify:                     # after watch — post-release checks
    - name: cdn
      command: curl -fsI https://github.com/you/repo/releases/download/$(git describe --tags --abbrev=0)/checksums.txt
  version_files:              # explicit version files (replaces auto-detection)
    - VERSION
    - pyproject.toml           # [project]/[tool.poetry] version, dependency pins untouched
    - path: src/app/__init__.py
      pattern: '__version__ = "{version}"'
  auto_confirm: true          # default false — skip the confirm prompt (as if -y); --yes=false escapes once
  wait: false                 # default true — false returns right after the push, skipping watch/verify
```

Watch steps run only when a release tag was actually pushed; a failure aborts with a rerun hint (the tag is already public — re-shipping is not the fix). Verify failures name the failing step and pass its output through. `continue_on_failure: true` marks a step advisory in either list. The whole pipeline appears in `gk ship --dry-run` and the `--json` plan (which also carries the resolved `wait` value), and "Ship complete" only prints after every hook has passed.

With `wait: false` (or `--wait=false`) ship ends at the push: the release is published but untracked, and the skipped watch/verify commands are printed as a NOTE to run manually once CI has had time to run. An explicit `--wait` re-enables the pipeline for one run when the config turns it off.

### Examples

```bash
# Preview the release without mutating anything
gk ship dry-run

# Read current ship status
gk ship status

# Ship an inferred release after confirmation
gk ship

# Non-interactive patch release
gk ship patch --yes

# Use an exact version
gk ship --version 0.15.0 --yes

# Machine-readable plan for agent tooling (pairs with -y for the real run)
gk ship --dry-run --json

# Squash local-only commits since the latest tag
gk ship squash --yes

# Push branch only, no tag/release
gk ship --no-release --yes
```

---

## gk sync

Catch the current branch up to its base branch (e.g., `main`). When the
current branch is already an ancestor of the base, sync fast-forwards;
otherwise it rebases by default. Use `--strategy merge` to integrate with
a merge commit, or `--strategy ff-only` to refuse divergence.

`gk sync` is the command for the most common feature-branch workflow:
"my branch fell behind `main` while I worked on it; pull `main`'s new
commits in." For "fetch the same branch from the remote and integrate"
(the multi-machine / collaborative-branch case), use [`gk pull`](#gk-pull).

### Synopsis

```
gk sync [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--base <branch>` | auto-detect | Base branch to sync onto |
| `--strategy <mode>` | `rebase` | `rebase`, `merge`, or `ff-only` |
| `--autostash` | false | Stash dirty changes before integration, pop after |
| `--fetch` | false | Fetch `<remote>/<base>` and fast-forward `refs/heads/<base>` before integrating (one-shot opt-in; default behaviour is offline) |
| `--fetch-only` | false | Fetch `<remote>/<base>` and ff local `<base>`; skip integration |
| `--no-fetch` | false | **Deprecated.** Default behaviour is now no-fetch; flag retained as a no-op alias. Mutually exclusive with `--fetch` and `--fetch-only`. |
| `--upstream-only` | false | **Deprecated.** Legacy v0.6 FF-to-`origin/<self>` behaviour. Removed in v0.8. |

### Strategy resolution

First match wins:

1. `--strategy` flag
2. `sync.strategy` in `.gk.yaml`
3. `git config pull.rebase` (`true`→rebase, `false`→merge)
4. default: `rebase`

### Self-FF (always-on)

When `origin/<self>` is strictly ahead of the local branch — e.g., another
machine pushed earlier — gk fast-forwards before integrating the base. If
`origin/<self>` has diverged from local, the self-FF step is skipped and
the divergence is resolved by the base integration. This makes
`gk sync` safe to run on multi-machine workflows without thinking about
order: a single command catches up to both your remote self *and* your
base.

### Base branch auto-detection

When `--base` is not set, gk resolves the base in priority order:

1. recorded `gk-parent` — the branch this one forked from (`gk wt add` records it
   automatically; `gk branch set-parent` sets it manually). Only an explicit
   `gk-parent` is used here — sync never adopts a reflog-inferred parent as a
   rebase target. A branch cut from `develop` therefore syncs onto `develop`.
2. `base_branch` config
3. `origin/HEAD` (remote default branch)
4. `develop` → `main` → `master` fallback

### Examples

```bash
# Catch up onto auto-detected base
gk sync

# Sync onto a specific branch
gk sync --base develop

# Use a merge commit instead of rebase
gk sync --strategy merge

# Refuse to rebase — only fast-forward
gk sync --strategy ff-only

# Stash dirty work before sync
gk sync --autostash

# Just fetch and report (no integration)
gk sync --fetch-only

# Legacy v0.6 behaviour (deprecated; removed in v0.8)
gk sync --upstream-only
```

### Conflict handling

If a rebase or merge produces conflicts, gk pauses with a clear hint:
`run gk continue, gk abort, or git rebase --continue to resolve`. Resume
or abort with the same plumbing as `gk pull` and `gk merge`.

### Output

After a successful integration, gk prints a compact summary:

```
self-ff: a1b2c3d → e4f5g6h  (origin/feat/x was ahead)
rebased feat/x onto main  e4f5g6h → 7h8i9j0  (+3 commits · rebase)
12 files changed, 240 insertions(+), 18 deletions(-)
```

The first line appears only when the self-FF step actually moved HEAD.
The middle line shows the verb ("rebased", "merged", "fast-forwarded"),
the branch and base, the pre/post HEADs, the commit count, and the
strategy used. When `requested != actual` (e.g., rebase requested but the
ancestor short-circuit collapsed it to a fast-forward), the strategy
reads `rebase → ff-only`.

`--fetch-only` instead reports ahead/behind against the upstream and
hints at the integrate command.

### Migration from v0.6

The v0.6 `gk sync` was "fetch + FF current branch to `origin/<self>`". v0.7
re-targets the command at the more common intent (catch up to base) and
exposes the old behaviour behind `--upstream-only` for one release. The
flag is removed in v0.8; use `gk pull` for the same effect.

The `--all` flag from v0.6 is removed — rebasing every local branch onto
the base is dangerous, and the FF-only fallback added little. Iterate
manually with a shell loop if you need it.

### Notes

- Refuses to start with tracked working-tree changes unless `--autostash`
  is set. Interactive terminals get a stash-or-skip prompt.
- Refuses to run when the current branch *is* the base branch — there is
  nothing to catch up to. Switch branches first.
- Set `GK_SUPPRESS_DEPRECATION=1` to silence the `--upstream-only` notice
  in CI logs.

---

## gk refresh

Fast-forward long-lived branches to their remote counterparts in one command,
without leaving the branch you are on. Each tracked branch only fast-forwards
to its own remote (`main ←ff── origin/main`, `develop ←ff── origin/develop`) —
it never rebases or merges across branches, so it is safe on shared branches:
a diverged branch is skipped with a hint instead of being rewritten.

Branches you are not standing on move via `update-ref` (the working tree is
untouched), so `gk refresh` works even from a feature branch with a dirty tree.

Alias: `gk re`.

### Synopsis

```
gk refresh [branch...] [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--no-fetch` | false | Skip the network fetch; fast-forward against already-cached remote refs |

### Target resolution

First match wins:

1. positional args (`gk refresh main release/1.x`)
2. `refresh.tracked` in `.gk.yaml`
3. dynamic — the repo's main branch (origin/HEAD → main → master) plus
   develop/dev when they exist locally

### Examples

```
gk refresh                 # ff main + develop to their remotes
gk re                      # alias
gk refresh main            # only main
gk refresh --no-fetch      # use cached remote refs (offline)
```

---

## gk precheck (alias: forecast)

Conflict forecast before integrating: `git merge-tree` simulation between HEAD and the target — working tree, index, and refs untouched. Without a target it checks the upstream (`@{u}`), falling back to the remote base branch — "will my next pull conflict?" in one read-only call, replacing the try→abort→retry loop. The simulation is a merge; a rebase replays commits one by one so its conflicts can differ in detail, but the file set is the practical forecast either way.

The forecast names **which function** would conflict, not just which file (git ≥ 2.40): the merge-tree result tree already carries conflict markers inside conflicted blobs, so precheck reads them straight from the object db and attributes each marker to its enclosing definition — `✗ app.py · in process`. JSON gains an append-only `details: [{path, symbols[]}]` alongside the unchanged `conflicts[]`; capped at 20 files / 256KB per blob, and any unreadable file just omits its symbols. `gk context --include=precheck` carries the same details.

```
gk precheck                  # forecast the next pull (@{u}, else remote base)
gk forecast origin/main      # explicit target
gk precheck develop --json   # {ours, target, base, clean, conflicts[], details[]}
```

Exit codes: 0 clean · 2 invalid input · 3 conflicts predicted. With `--json`/`GK_AGENT=1` the result follows the agent envelope; exit 3 still signals "conflicts" (a forecast, not an error).

## gk land

The session-closing compound verb: commit what's dirty (AI-grouped via `gk commit -f`), `gk pull --with-base`, and `gk push` — one transaction with per-step ✓ output. The first failure stops the run and names the failed step plus the exact resume path; re-running `gk land` after the fix is safe (completed steps degrade to no-ops).

| Flag | Default | Description |
|------|---------|-------------|
| `--with-base` | true | Fast-forward the local base branch during the pull step (`--with-base=false` to skip) |
| `--to` | (off) | After pushing, forward-merge the current branch into a target via the FF-only promote machinery (`gk merge --into <target>` + `gk push --from <target>`). Three forms: `--to parent` advances **one hop** to the branch's parent (`branch.<name>.gk-parent`, else the configured base); `--to base` merges straight into the configured base in one direct hop; `--to <branch>` chain-walks the parent stack hop by hop up to `<branch>` (advancing each intermediate — the same as the standalone `gk promote <branch>`). A target outside the parent chain is rejected. A conflict pauses with the normal resolve/continue contract and the failed step reports a resume path; re-running skips an already-merged target. `land.promote` in config makes this a default: `parent` (or `true`) for the one-hop semantics, a branch name for the chain walk. Note `base` is a `--to` keyword only — in `land.promote` config write the base branch's **actual name** (e.g. `main`); a literal `base` there is read as a branch named `base` and fails. See [config: `land.promote`](config.md#landpromote). |
| `--no-push` | false | Local wrap-up: skip the push step **and** any integration push — commit + pull + local merge only. The folded form of the old local-promote flow (integrate now, publish later from the receiving branch). |
| `--promote` | (off) | **DEPRECATED** alias for `--to` (kept one release; a soft stderr hint fires when used). Bare `--promote` = one hop to parent/base; `--promote=<branch>` = chain walk to `<branch>`. Existing flows and `land.promote` config keep working unchanged — prefer `--to parent\|base`, or `gk promote <branch>` for the multi-hop walk. |
| `--no-promote` | false | Skip the promote step for this run — the per-invocation escape from a `land.promote` config default |
| `--autostash` | false | During the `--to`/promote merge, stash a dirty **receiver** worktree (the parent checkout someone left mid-edit) around the merge and pop it after, instead of refusing with `working tree has tracked changes`. Forwarded to the underlying `gk merge --into`. Config default: `land.autostash: true`; `--autostash=false` opts out for one run. See [config: `land.autostash`](config.md#landautostash). |
| `--cleanup` | false | After pushing, delete fully-merged branches and reclaim their worktrees (merged-only, protected branches excluded) |
| `--json` (global) | false | Emit `{steps:[{name,result}], failed_step?, resume?}` on stdout; step progress moves to stderr |

## gk promote

The local half of `gk land --to`: commit what's dirty (AI-grouped via `gk commit -f`), then forward-merge the current branch into its parent/base — **no pull, no push**. Built for worktree-centric flows where integration happens locally (merge into `develop` now, publish later from `develop`) and `land`'s mandatory network steps are exactly what you don't want.

```bash
gk promote          # commit → merge into the parent (gk-parent metadata, else the configured base)
gk promote main     # walk the parent chain hop by hop: feat→develop→main, each boundary merged
gk promote --push   # also publish each advanced branch (push --from <target>) — land --to's behavior
```

Bare `gk promote` climbs **one hop** — the same target resolution as `gk status`'s ready-to-merge line and `land --to parent`. `gk promote <branch>` walks the parent chain until `<branch>`; a target outside the chain is an error (use `gk merge --into` for a one-off direct merge). The receiving branch does not need a worktree: a fast-forward updates the ref directly, a clean non-FF merge commits via merge-tree, and only a real conflict requires a checkout. Conflicts pause with the normal resolve/continue contract; re-running is safe (a clean tree skips commit, merged hops merge nothing). Already on the target → a quiet no-op that leaves the dirty tree alone.

| Flag | Default | Description |
|------|---------|-------------|
| `--push` | false | After each hop's merge, also publish the advanced branch (`push --from <target>`) |
| `--autostash` | false | Stash a dirty **receiver** worktree (the parent checkout) around each hop's merge and pop it after, instead of refusing with `working tree has tracked changes`. Config default: `promote.autostash: true`; `--autostash=false` opts out for one run. See [config: `promote.autostash`](config.md#promoteautostash). |
| `--json` (global) | false | Emit `{steps:[{name,result}], failed_step?, resume?}` on stdout (same contract as `gk land`); step progress moves to stderr |

## gk batch

Executes gk sub-commands in the order a JSON plan lists them — the generalized sibling of `gk land`: land is the fixed session-closing sequence, batch is whatever sequence the caller declares. A multi-step agent workflow (commit → pull → push → tag) becomes one call.

```bash
gk batch --plan-template               # starter plan (JSON) to edit
gk batch --plan - < plan.json          # validate + execute
echo '{"steps":[{"args":["pull"]},{"args":["push"]}]}' | gk batch --plan -
```

Plan schema: `{"steps":[{"args":["pull","--with-base"], "name?", "on_failure?", "worktree?"}]}` — `args` is the full gk argv for the step (sub-command first); `on_failure` is `"abort"` (default, stops the plan) or `"continue"` (records the failure and moves on); `worktree` runs the step in a specific worktree instead of the repo root — an absolute worktree path, or a branch name resolved to the worktree checked out on it — so one transaction can span worktrees (e.g. commit in `feat-a`, sync in `feat-b`). A `worktree` reference that resolves to no registered worktree fails the step under its `on_failure` policy.

Validation happens before any step executes: an unknown sub-command, a nested `batch`, args starting with a flag, or more than 20 steps reject the whole plan. A gating failure skip-marks the remaining steps and reports `failed_step`/`resume`. A child that pauses for conflict resolution (exit 3) stops the plan even under `on_failure: continue` — the next step must not stack on an unresolved pause.

| Flag | Default | Description |
|------|---------|-------------|
| `--plan <path\|->` | — | JSON plan: a file path, or `-` for stdin |
| `--plan-template` | false | Emit a starter plan (JSON) and exit |
| `--dry-run` (global) | false | Print the step list without executing |
| `--json` (global) | false | Emit `{result, steps:[{name,command,result,exit_code}], failed_step?, resume?}` on stdout; child output moves to stderr |

`result` is `completed` (all ok), `partial` (failures were marked `continue` and the plan ran to the end), or `failed`.

## GK_AGENT=1 — agent mode

`export GK_AGENT=1` turns on agent mode for every gk invocation: `--json` is implied, JSON payloads are wrapped in a uniform envelope (`{schema, state, ok, result}` on success), and failures print `{state:"error", ok:false, error:{code, message, hint, remedies:[{command,safety}]}}` to stderr while keeping the normal exit codes. `state` is the primary dispatch key: `ok`, `paused`, `blocked`, or `error`; `ok` remains a derived alias for existing consumers. `error.code` is a stable, append-only vocabulary (`not-a-repo`, `dirty-tree`, `conflict`, `diverged`, `in-progress-op`, ...). Without GK_AGENT, explicit `--json` output stays command-specific. A paused state with a resume contract (pull/merge conflict, exit 3) is a result, not an error.

## gk context

One-call repository orientation — current branch, upstream and ahead/behind, dirty counts (staged/unstaged/untracked/conflicts), any in-progress rebase/merge with its resume/abort commands, base-branch drift vs its remote, linked worktrees, and suggested `next_actions`.

Each linked worktree carries its own status: `current` (the worktree this call runs from), `parent`/`ahead`/`behind` (where it diverged), and a `dirty` block **present only when that worktree holds uncommitted work** — so a non-empty `dirty` anywhere in `worktrees` is the one-call answer to "which worktree has unfinished work?". `gk worktree list --json` carries the same per-worktree enrichment.

With the global `--json` flag the output is a stable, schema-versioned document intended for AI agents: one call replaces the usual `git status`/`branch`/`log`/`worktree list` probe sequence. Fields are append-only; breaking changes bump `schema`.

`--include` fuses the usual follow-up probes into the same document — one call instead of six: `diff` (uncommitted changes as a digest with per-file ±lines and symbols, untracked files included; before the first commit the empty tree stands in for HEAD), `log` (the last 5 commits), `precheck` (merge-tree forecast for the next pull), `conflict` (current unmerged files with operation kind, conflict type, hunk counts, stage blobs, and `symbols` — the enclosing function/entity of each conflict marker, weave-style: "`process` 양쪽 수정" beats "app.py 충돌"), `remotes` (every registered remote with the current branch's drift as of the last fetch, plus asymmetric push URLs — see `gk doctor`), `release` (what is still unreleased: when a release branch resolves, the commits on HEAD not yet on it — `origin/<base>..HEAD` — plus the since-tag total and `already_on_base` so squash-merged work already shipped no longer inflates the count; falls back to since-tag-only when no base resolves, and still appears when the repo has no tags but a base does). One more section, `github`, counts the repo's open PRs/issues (plus PRs awaiting your review) via the GitHub search API. `gk context` (bare) shows it by default, and `--include=github` is an explicit refresh; it stays **excluded from `all`**. All GitHub-count surfaces (`gk context`, `gk status --vis github`) share one on-disk cache (`.git/gk-github-cache`) governed by a per-surface policy in `github.counts` — `off` | `cache` (never fetch) | `ttl` (fetch when older than `ttl_minutes`, default 3) | `force` (always fetch). Defaults keep bare `gk context` and `gk status` offline (`cache`) and let `--include=github` refresh on the TTL (`ttl`); `gk pr`/`gk issue` on the current repo warm the cache for free. Fetches run under a 5s timeout; `github.as_of` records the fetch time. A section that cannot be collected degrades to a `notes` entry instead of failing the call.

`--delta` answers a **repeat** orientation with only what moved: the first call in a worktree returns the full document tagged `delta:"baseline"` (and records the snapshot under `~/.gk/context-ledger/`, keyed by the normalized worktree path); an unchanged repeat collapses to `{delta:"unchanged", unchanged:true, delta_base}`; a changed repeat carries just the core fields that differ (a field that vanished — e.g. `in_progress` after a rebase finishes — surfaces as `null` rather than silently disappearing). `--include` sections are never delta'd: they ride along fresh on every call. Any ledger problem (missing, corrupt, unwritable home) silently degrades to the full response, and the ledger directory is swept with a 7-day TTL on every save. The non-delta output is byte-identical to before.

```bash
gk context                                  # human one-screen summary (alias: gk ctx)
gk context --json                           # agent contract
gk context --include=diff,log,precheck      # fuse follow-up probes (or --include=all)
gk context --include=conflict --json         # unmerged paths + stages + hunk counts
gk context --include=remotes --json         # per-remote drift + asymmetric push URLs
gk context --include=github --json           # open PR/issue counts (opt-in, network, cache-first)
gk status --vis github                       # same counts on a status line — cache-only, never fetches
gk context --delta --json                   # repeat orientation: only what changed since the last call
```

## gk agents

Manages the gk usage contract inside agent instruction files (`CLAUDE.md`, `AGENTS.md`). The default contract is compact and contains only the rules agents need to route git through git-kit correctly; `--full` keeps the longer reference block available. The paragraph is embedded in the gk binary — it always matches the installed gk's real surface — and is fenced with versioned markers; nothing outside the block is touched.

Two scopes: the **repo root** (`CLAUDE.md` / `AGENTS.md`, the default) and the **per-agent global files** that every project inherits — Claude's `$CLAUDE_CONFIG_DIR/CLAUDE.md` (default `~/.claude/CLAUDE.md`) and Codex's `$CODEX_HOME/AGENTS.md` (default `~/.codex/AGENTS.md`), selected with `--global`.

| Subcommand | Description |
|------------|-------------|
| `gk agents print [--full] [--tuned]` | Print the compact contract block to stdout; `--full` prints the detailed reference block, `--tuned` appends one data-backed line naming your top raw-git turn leak |
| `gk agents install [--file <path>] [--full] [--tuned]` | Insert or refresh the compact block in `CLAUDE.md` + `AGENTS.md` at the repo root (idempotent); `--full` installs the detailed block, `--tuned` adds the data-backed leak line |
| `gk agents install --global [--full]` | Insert or refresh the block in the global files (`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`); parent dirs are created as needed |
| `gk agents check` | Report block status + version for **both** scopes — local (when inside a repo) and global. Version drift (an installed block from an older gk) exits non-zero with an install hint; a scope that simply isn't installed is reported but doesn't fail the default view |
| `gk agents check --global` | Report only the global files (here a missing block also fails, since you targeted it explicitly) |
| `gk agents hook install [--mode block\|collapse\|warn] [--no-prompt] [--stop-commit] [--stop-only] [--global] [--dry-run]` | Register the Claude Code hooks in `settings.json`: a PreToolUse(Bash) hook that steers raw git to git-kit at the moment a command runs, plus a UserPromptSubmit prefetch hook that injects git orientation for a git-action prompt (`--no-prompt` opts out of the latter), plus — with `--stop-commit` — a Stop hook that checkpoints the session with `gk commit --wip`. `--stop-only` registers *just* the checkpoint and leaves the other two events untouched. Default `.claude/settings.json`; `--global` for `~/.claude/settings.json` |
| `gk agents hook uninstall [--global] [--dry-run]` | Remove every gk-managed hook (revert), preserving all other hooks |
| `gk agents hook status` | Report install state + mode for all three events, local and global |

With `--json` / `GK_AGENT=1`, `check` emits one structured result with `files[]`, `drift`, `absent`, `needs_install`, and `install_commands`. Explicit missing targets report `state:"blocked"` in that result so agents can install or stop without parsing a second error envelope. `install` reports each target's `action` (`created`, `updated`, `unchanged`) and version.

`--tuned` (on `print` and `install`) composes the compact block plus one data-backed guidance line naming the top raw-git turn leak recorded in `~/.gk/audit-history.jsonl`, so the contract points at the specific habit this environment leaks most. The tuned block carries a `v22+tuned` fence marker that `gk agents check` accepts by marker version alone (not exact content), so a tuned block never reads as drift. With no recorded history, `--tuned` warns and falls back to the plain compact block; because it composes the compact block, it cannot be combined with `--full`.

`gk agents hook` is the **enforcement** companion to the **instruction** block above: where the contract block (a markdown paragraph) advises, the hook acts at the point of a tool call. It is Claude Code specific (settings.json), unlike the contract block which any markdown-reading agent inherits. The registered command invokes `gk agents hook run`, which classifies the pending Bash command with the same mapping `gk session audit` and `gk hint` use. Three modes: **warn** (default — the command still runs, a note is surfaced to the agent via `additionalContext`), **collapse** (`--mode collapse` — a lone covered command is still only advised, but a second same-group probe is *denied*: the repeated orientation the audit shows is the biggest turn sink is blocked so the agent folds it into one git-kit call, while a one-off `git status` stays cheap), and **block** (`--mode block` — every covered raw git is denied so the agent retries with git-kit). Every gk-managed PreToolUse and UserPromptSubmit command has a 5-second timeout, so a stalled hook cannot hold the agent indefinitely. Edits are surgical (tidwall sjson/gjson): only the gk entry is added or removed, all other hooks and settings are preserved byte-for-byte, a `.bak` is written first, file permissions are kept, and `--dry-run` previews without writing. The handler is fail-open — a non-Bash tool, a command with no git-kit equivalent, read-only plumbing, an empty command, or unreadable stdin all defer silently to the normal permission flow. (Claude merges PreToolUse hooks from project + global settings, so install into one scope, not both.) Beyond the single-command mapping, the handler reads the live session transcript (Claude passes its path on stdin) and, when the pending command continues a recent same-group raw run, adds a real-time **collapse nudge** — fold it and the prior call(s) into one git-kit call — the prevention companion to [`gk session audit --metric=turns`](#turn-reduction-metric---metricturns). Advisories are deduped per session: a given finding kind is injected at most once per session (a stateless transcript-tail check), so a repeated raw command doesn't repeat the same note — the collapse nudge is never deduped, and a missing/unreadable transcript keeps the old always-advise behavior. In `collapse`/`block` mode a deny's reason is a single line naming the replacement command.

**Prompt prefetch (UserPromptSubmit).** `install` also registers `gk agents hook run --prompt` on the UserPromptSubmit event (skip with `--no-prompt`; `uninstall` removes both, `status` reports both). Where the PreToolUse hook reacts to a tool call the agent already chose, this one fires earlier — before the model starts thinking. If the prompt reads as an explicit git-action request (a conservative bilingual gate: "커밋해줘" / "rebase develop" fire; "commit to this plan" / questions about git never do — unclear input does nothing), it injects a one-line orientation (branch, upstream ↑↓, dirty counts, any paused operation with its resume command) as `additionalContext`, capped at 800 chars, so the agent's first tool call doesn't have to re-derive it. The payload is built from lightweight probes (measured warm p95 ~29ms firing, ~18ms passing — it never runs the full `gk context` collector), a `[gk prefetch]` marker in the transcript tail suppresses duplicate injections within a session, and every failure path emits nothing and exits 0 — the hook can never block a prompt.

**Session checkpoint (Stop).** `--stop-commit` registers a third hook, `gk agents hook run --stop`, on the Stop event: when a session ends with uncommitted work it runs [`gk commit --wip`](#checkpoint-mode---wip), leaving one `WIP(scope): <summary>` commit that a later `gk commit` folds into real Conventional Commits. It is **opt-in** because, unlike the other two, it writes to git history — a plain `install` never registers it, and re-running `install` without the flag removes an existing one.

The handler is fail-open in the strictest sense: no repo, no provider, a compose timeout, a non-zero exit — every path prints at most one stderr line and exits 0, because a session-end hook that can fail the session is worse than one that occasionally skips a checkpoint. It bails immediately when `stop_hook_active` is set, so a Stop-driven continuation cannot append one commit per loop. The checkpoint runs as a child process (clean cobra flag state, enforceable deadline) under a 120s ceiling written into the settings entry's own `timeout`, so a hung provider cannot hold the session open; hitting it loses nothing, since the files remain in the working tree for the next session to checkpoint.

`--stop-only` registers the checkpoint and nothing else: existing PreToolUse/UserPromptSubmit entries — gk's own or anyone else's — are left byte-for-byte alone, and no new ones are added. This is the right form when the steering hook already lives in another scope, since Claude merges PreToolUse across project and global settings and installing it in both double-fires it. It implies `--stop-commit`.

Scope note: the checkpoint inherits `gk commit`'s default scope (staged + unstaged + untracked), so files created during the session are included. Machine-local files that should never be committed belong in `.gitignore` or `ai.commit.deny_paths`.

## gk hint

Maps a single raw `git` command to the git-kit verb that covers it — the single source of truth behind `gk agents hook` and the same mapping `gk session audit` reports. The command text comes from the arguments, or from stdin when none are given:

```bash
gk hint "git status --short"
echo "$TOOL_CMD" | gk hint --json
```

| Flag | Effect |
|------|--------|
| `--json` (or `GK_AGENT=1`) | Emit `{covered, kind, severity, covered_by, suggestion, matched}` instead of the one human line |
| `--exit-code` | Exit 1 when a git-kit replacement exists (0 otherwise), so a hook script can branch on the status without parsing output |

A command containing several git segments reports the highest-severity covered pattern. Read-only plumbing (`git rev-parse`, `git config --get`, `git cat-file`, …), the `git diff`/`show` family, commands already on git-kit, and non-git commands all report not-covered.

## gk session audit

Reads local Codex and Claude JSONL session logs, extracts shell commands from
tool calls, and reports where agents still fall back to raw `git`, short `gk`
aliases, or shell chains that git-kit can absorb.

With no path arguments it scans the newest session files under:

- `~/.codex/sessions`
- `~/.claude/projects`
- `~/.claude/sessions`

Pass files or directories to audit a specific subset. The command is local and
read-only; it never sends session contents anywhere.

```bash
gk session audit
gk session audit --max-files 50
gk session audit --since 30d         # only sessions modified in the last 30 days
gk session audit ~/.codex/sessions/2026/06/22/session.jsonl --json
```

`--since <window>` (`30d`, `12h`, …) keeps only session files modified inside
the window, so the numbers describe *now* instead of averaging all history —
without it, sessions that predate a guidance fix dilute the adoption rate and
hide whether the fix is landing. The report echoes the cutoff in a top-level
`since` field (and a `window:` line in the human output) plus a note counting
the files the filter skipped.

The JSON schema reports per-file counts, totals, and aggregated findings such
as `raw-context-probes`, `raw-conflict-probes`, `raw-release-sequence`,
`raw-commit-sequence`, `raw-branch-switch`, `raw-worktree`, `raw-full-diff`,
`raw-diff-check`, `raw-unstage`, `raw-apply` (covered by git-kit apply, collapse
group `apply`), `gk-short-alias`, `shell-chain`, and `uncovered-raw-git`.
Each finding carries a `status`:

- `covered`: git-kit already has a replacement; read `covered_by`.
- `partial`: git-kit covers the common path, but the finding still names a remaining workflow gap.
- `gap`: session evidence points to a missing git-kit feature; read `gap`. The `uncovered-raw-git` finding collects every raw-git subcommand with no recognized git-kit mapping (read-only plumbing excluded) and carries a `subcommands` breakdown (`{"stash":4,"apply":3,…}`) — the roadmap signal for which verbs to build or classify. Its `evidence` keeps one sample per subcommand (rare subcommands are never starved by frequent ones), and `one_shot` lists the subcommands whose raw form is a single call — real coverage holes, but a gk verb there saves ~0 turns, so rank by what's *not* in `one_shot` before building.

Each `shell-chain` finding's evidence carries a synthesized `plan`: a ready-to-run
`git-kit batch --plan -` payload (`{"steps":[{"args":[...]}]}`) that replaces the
observed `git … && git …` chain, plus an `omitted` list naming the non-git-kit
segments (`echo`, `grep`, `cd`, …) that batch cannot carry. The human output
prints it as a `batch plan:` line.

The report also includes an `adoption` block — `git_invocations`, `git_kit`,
`rate` (git-kit's share of all git-shaped calls), `covered_raw_hits` (raw-git
hits that already have a git-kit path, i.e. pure habit leaks), and
`uncovered_raw_hits` (raw git with no git-kit mapping, kept separate so the rate
is not dragged down by plumbing that git-kit never intends to wrap). Rerun the
audit over time and watch `rate` climb and `covered_raw_hits` fall to track
whether guidance changes are landing.

A `projects` array breaks the same adoption numbers down per project —
most raw git first, so the top entries are the contract/hook install
targets (`gk agents install` / `gk agents hook install` there). Claude
sessions attribute to their workspace directory name; Codex sessions have
no project marker and pool under `codex-sessions`. The human output prints
the top five as `raw git by project`.

### JSON output size (`--files` / `--full` / `--summary`)

The JSON / agent payload is token-lean by default: `files[]` (the per-file
breakdown) is omitted, `runs[].commands` are capped at 3 entries × 120 chars
with the remainder folded into a `(+N more)` marker, and `findings[].evidence`
is capped at 2 samples. These caps apply **only to the JSON/agent output** —
the human report and the recorded `--trend` history always see the full data.

- `--files` restores the per-file `files[]` breakdown.
- `--full` restores the previous exact payload: `files[]` plus uncapped run commands and evidence.
- `--summary` emits only the decision-grade subset (totals, adoption, top projects, findings without evidence).

`--full` and `--summary` are mutually exclusive. A session-audit JSON payload
over 16 KiB is emitted compact (no indentation) to save bytes.

### Turn-reduction metric (`--metric=turns`)

The occurrence counts above measure *how often* raw git appears; `--metric=turns`
(or `both`) measures what git-kit actually exists for — *turns saved*. Turn
reduction only happens when raw git is split across separate tool calls (turns):
`git status`, then `git log`, then `git diff` in three turns collapses to one
`gk context` (2 turns saved), whereas the same three in one `git status && git
log && git diff` chain is already one turn (0 saved). The turn metric derives a
real turn boundary per source — a Claude assistant message id, a Codex
`function_call` batch (parallel calls share a turn) — then finds local
`collapsible run`s of same-group raw git across adjacent turns and reports
`estimated_turns_saved`, a per-gk-call breakdown, and `adoption`-style `rate`.
Failed-then-retried calls (`is_error`), different repos, and the same verb aimed
at different objects (`git show A` then `git show B` — paging) are not collapsed.

```bash
gk session audit --metric=turns          # add the turn view to the report
gk session audit --metric=turns --viz    # draw collapsible runs as a turn-graph (●─●)
gk session audit --metric=turns --record # append this run to ~/.gk/audit-history.jsonl
gk session audit --trend                 # show the saved-turns trend (sparkline) from recorded runs
```

`--record` and `--trend` both imply `--metric=turns`. With `--json` /
`GK_AGENT=1`, `--trend` attaches the recorded history as a top-level `trend`
array in the result instead of the sparkline. The metric is opt-in and
additive: without `--metric` the occurrence output and JSON schema are
unchanged. The same turn classification powers a real-time nudge in
[`gk agents hook`](#gk-agents-hook): when a pending raw git command continues a
recent raw run, the PreToolUse hook suggests folding them into one git-kit call.

Under `GK_AGENT=1`, the report is wrapped in the standard `{state, ok, result}`
envelope.

## gk session digest

Compress a single agent session's git activity into a short resume/handoff
block, so a resumed or handed-off agent reads it instead of re-running the
status/log/diff orientation probes.

### Synopsis

```
gk session digest [transcript-file] [--last[=N]]
```

Pass an explicit Claude/Codex JSONL transcript path, or `--last` to digest a
session file under the default session roots (the same roots `gk session
audit` scans).

### `--last[=N]`

`--last` (bare) digests the newest session file; `--last=N` digests the
N-th-newest. **The value must use `=`** — `--last=2`, not `--last 2` (a
space-separated `2` is read as the transcript-path argument).

Run from **inside** a live agent session, the newest file is that session's own
transcript (it is appended every turn), so use `--last=2` for the **previous**
session's handoff.

### Digest contents

- repos touched (with per-repo command counts)
- branches created / switched to
- commit subjects (most recent last)
- integration attempts, the verbs used, and whether any errored (with the last error)
- an unfinished-work signal (the turn, command, and reason)
- re-probed command groups, each with the single gk call that collapses them

The command is local and read-only. With `--json` / `GK_AGENT=1` it emits the
standard machine-readable envelope.

## gk pull

Fetch and rebase the current branch onto the base branch.

### Synopsis

```
gk pull [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--base <branch>` | auto-detect | Base branch to rebase onto (only consulted when `@{u}` is unset) |
| `--strategy <mode>` | `rebase` | `rebase`, `merge`, `ff-only`, or `auto` |
| `--rebase` | false | Shorthand for `--strategy rebase`; also acts as explicit consent on diverged history |
| `--merge` | false | Shorthand for `--strategy merge`; also acts as explicit consent on diverged history |
| `--from <remote>[/<branch>]` | — | Pull from a specific remote instead of the upstream — a mirror or org fork the tracking chain never fetches. Branch defaults to the current branch's name; tracking config stays untouched. Unregistered remotes are rejected with the registered list |
| `--fetch-only` | false | Fetch only, do not integrate |
| `--no-rebase` | false | **Deprecated** alias for `--fetch-only` |
| `--autostash` | **on** | Stash dirty (tracked) changes before integration, then pop with index state preserved — **the default**. Force it on for one run even when `pull.autostash` is off in config. |
| `--no-autostash` | false | Restore the pre-autostash gate: prompt for `stash & continue` on a TTY, refuse on a non-TTY. Same as `pull.autostash: false` / `GK_PULL_AUTOSTASH=0` for one run. |
| `--with-base` | false | Also fast-forward the local base branch (e.g. `main`) to its remote tip after the fetch — no checkout involved. Config default: `pull.with_base: true`; `--with-base=false` opts out for one run. Strictly FF-only: a diverged base, a base checked out in another worktree, or a missing local base is skipped with a NOTE. Base fetches use an explicit remote-tracking refspec, so narrow/single-branch fetch configs still refresh `origin/<base>` on the first run. Skipped under `--fetch-only` |
| `--json` (global) | false | Emit the machine-readable result on stdout (`result`: `updated`/`up-to-date`/`ahead-only`/`fetch-only`/`conflict`, moved SHAs, `base` outcomes, conflict files + resume/abort commands). The human progress stream stays on stderr |
| `-v`, `--verbose` | (count) | Show upstream, strategy, and integration details; repeat for diagnostics |

### Base branch auto-detection

When `--base` is not set and `base_branch` is not configured, gk probes in this order:

1. `origin/HEAD` (remote default branch)
2. `develop`
3. `main`
4. `master`

### Examples

```bash
# Fetch and rebase onto auto-detected base branch
gk pull

# Rebase onto a specific branch
gk pull --base develop

# Fetch only, skip rebase
gk pull --no-rebase

# Restore the old gate: prompt on a TTY, refuse on a non-TTY
gk pull --no-autostash

# Morning multi-machine sync: pull develop AND fast-forward local main
gk pull --with-base

# Preview what would happen without executing
gk pull --dry-run
```

### Notes

- A dirty (tracked) working tree is auto-stashed by default: gk stashes before integration and pops after, with `--index` so already-staged hunks stay staged when the pop succeeds — the common no-conflict case flows through with a `stashed N / restored N` status line and no prompt. The pop is the one place a real conflict with your local edits surfaces, and the one place pull then stops (non-zero, the stash preserved). Turn this off with `--no-autostash` (or `pull.autostash: false` / `GK_PULL_AUTOSTASH=0`) to restore the old gate: prompt for `stash & continue` on a TTY, refuse on a non-TTY.
- Runs `git fetch <remote> <base>` then `git rebase origin/<base>`.
- On conflict, gk pauses and prompts. Use `gk continue` or `gk abort` to resume.

### Post-integration summary

When the integration succeeds, gk prints a compact block describing what actually changed:

```
updated 6ab13b03 → 67208ff8  (+3 commits · ff-only)
  67208ff  feat: commit 3  <alice · 2h>
  8beb369  feat: commit 2  <alice · 5h>
  e3422b1  feat: commit 1  <alice · 1d>
3 files changed, 12 insertions(+), 4 deletions(-)
```

- Range is the pre/post HEAD pair. Long commit lists are capped at 10 entries with a `… +N more` footer.
- When nothing changed (HEAD already matched upstream), gk prints `already up to date at <sha>` instead of the range block.
- `gk pull --no-rebase` (fetch-only) reports waiting commits:
  - `fetched origin/main: +2 commits waiting  (run gk pull to integrate)` when only behind.
  - `fetched origin/main: ↑N local · ↓M upstream  (diverged — run gk pull to rebase/merge)` when both sides have diverged.

---

## gk diff

Terminal-friendly diff viewer that wraps `git diff` with color, line numbers, intra-line word highlights, and an optional interactive file picker. The parsed output (file → hunk → line model) drives a renderer with `◀` / `▶` / `·` markers; a pager auto-launches when stdout is a TTY.

### Synopsis

```
gk diff [flags] [<ref>] [<ref>..<ref>] [-- <path>...]
```

Positional arguments and `--` paths are forwarded to `git diff` unchanged, so the comparison vocabulary is identical to git's:

| Invocation | Compares |
|---|---|
| `gk diff` | working tree ↔ index (unstaged changes) |
| `gk diff --staged` | index ↔ HEAD (staged changes) |
| `gk diff HEAD` | working tree ↔ HEAD (staged + unstaged) |
| `gk diff main` | working tree ↔ `main` |
| `gk diff a..b` | commit `a` ↔ commit `b` |
| `gk diff -- path/` | scope to path |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--staged` | false | Show staged changes (`git diff --cached`) |
| `--stat` | false | Prefix the output with a per-file diffstat (proportional bars + line counts) |
| `-i`, `--interactive` | false | Open a file picker; selected file shows in a scrollable viewport with hunk fold/unfold (Tab) and `n`/`p` navigation |
| `-U`, `--context <n>` | `3` | Context lines per hunk |
| `--no-pager` | false | Disable the auto-pager (`$GK_PAGER` / `$PAGER` / `less`) |
| `--no-word-diff` | false | Disable intra-line word-level highlights |
| `--raw-patch` | false | Emit the raw unified patch. With `--json`, emit `{schema, patch, parsed}` so agents can get both the original patch body and the structured parse in one call |
| `--check` | false | Check whitespace/conflict-marker problems without rendering the patch. With `--json`, emit `{schema, result, clean, count, problems[]}` |
| `--json` | (global) | Emit a structured JSON document; implies `--no-color --no-pager` |

### "No changes" hint

When the comparison produces empty output, `gk diff` prints a banner naming the trees compared and probes the *other* side:

```
변경사항 없음  (working tree ↔ index · 기본)
  hint: staged 변경 3 파일 — gk diff --staged
  또는: gk diff HEAD     (staged + unstaged 합쳐서)
        gk diff <ref>   (다른 commit/branch와 비교)
```

The smart hint surfaces only when probing the unused side reveals work; for explicit-ref invocations (`gk diff main`) the probe is suppressed and only the universal alternates render.

### Word-diff bounds

Intra-line highlights run an LCS DP table sized `(m+1)*(n+1)` ints, where `m`/`n` are token counts. To prevent OOM on minified-bundle / generated-file diffs, two guards skip word-diff and fall back to a whole-line "Changed" highlight:

- Either side longer than 4 KB.
- Token product (`(m+1)*(n+1)`) over 1 M cells.

The whole-line marker still appears with `◀` / `▶`; only the intra-line span detail is suppressed.

### Examples

```
gk diff                       # unstaged changes in the working tree
gk diff --staged              # what `git commit` would record
gk diff HEAD                  # staged + unstaged together
gk diff main..HEAD            # everything since branching
gk diff -i                    # interactive file picker → per-file viewer
gk diff --stat                # diffstat prefix + diff body
gk diff --json                # machine-readable output (implies no color, no pager)
gk diff --raw-patch --json -- internal/cli/pull.go
gk diff --check --json        # whitespace/conflict-marker check
gk diff -U10                  # 10 context lines per hunk
gk diff -- internal/ui/       # restrict to a path
```

### Exit codes

`0` regardless of whether changes were found — `gk diff` is a *viewer*, not a status check. Use `gk status --exit-code` when you need an exit-code-driven dirty/clean signal. Exception: `gk diff --check` mirrors `git diff --check` and exits non-zero when whitespace/conflict-marker problems are found; under `GK_AGENT=1` the JSON envelope reports `state:"blocked"` with the structured problem list.

---

### --digest — semantic summary

`gk diff --digest` answers "what changed, where" without the patch body: one line per file with the change kind, ±lines, hunk count, and the **symbols** (function contexts from git's hunk headers — no `.gitattributes` needed) the change touched. Non-source files are tagged `[test]` / `[docs]` / `[ci]` / `[build]`.

```
M  internal/cli/pull.go    +56 −12  ·3   func runPullCore(...), func emitPullJSON(...)
A  internal/cli/land.go   +280 −0   ·1
M  docs/commands.md         +9 −0   ·2   [docs]
   3 files · 6 hunks · +345 −12
```

With `--json` / `GK_AGENT=1` it emits the agent contract `{schema, files:[{path, status, hunks, added, deleted, symbols[], kind}], stat}` — the most frequent multi-turn agent pattern (status → diff --stat → per-file reads) in one call. Accepts the same ref/path arguments as plain `gk diff` (`--staged`, `HEAD~3`, `main..feature`, `-- path`).

When an agent needs the exact unified patch text instead of only parsed hunks, use `gk diff --raw-patch --json -- <path>`. The result is `{schema, patch, parsed}` and respects the same ref/path arguments, so it replaces the usual `git diff -- <path>` fallback without leaving the gk JSON contract.

`gk diff --check --json` replaces raw `git diff --check` probes. It reports `clean`, `count`, and `problems[]` with `path`, `line`, `kind`, `message`, and the offending added line when git provides one.

## gk merge

Precheck, explain, and merge a target branch into the current branch. `gk merge` runs the same merge-tree conflict scan as `gk precheck`, prints an AI-assisted merge plan by default, then invokes `git merge` with guarded defaults.

### Synopsis

```
gk merge <target> [flags]
gk merge [<source>] --into <receiver> [flags]
```

`gk merge <target>` merges `<target>` into the current branch — the
direction matches `git merge`. To go the other way (land the current
branch on a different branch), use `--into`.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--ff-only` | false | Allow only fast-forward merges |
| `--no-ff` | false | Create a merge commit even when fast-forward is possible |
| `--no-commit` | false | Perform the merge but stop before creating the commit |
| `--squash` | false | Squash changes from target without creating a merge commit |
| `--skip-precheck` | false | Skip the merge-tree conflict precheck |
| `--autostash` | false | Stash tracked changes before merge and pop afterwards |
| `--no-ai` | false | Skip the merge plan summary |
| `--plan-only` | false | Print the merge plan without running `git merge` |
| `--into <branch>` | "" | Land the source branch (default: current) into `<branch>` |
| `--provider <name>` | config | Override `ai.provider` for the merge plan |

### `--into` worktree handling

When `--into <branch>` is given, gk first looks for a worktree that has
`<branch>` checked out:

- **Worktree found** — gk runs the merge inside that worktree (same
  precheck, same conflict-resolution flow as a normal `gk merge`).
  `--squash`, `--no-commit`, and conflict resolution via
  `gk continue` / `gk abort` all work as usual.
- **No worktree** — gk uses a worktree-free path:
  - Fast-forward case: `git update-ref` jumps the receiver ref to the
    source. No working tree is touched.
  - Conflict-free non-fast-forward case: an in-memory merge tree is
    built with `git merge-tree`, wrapped into a merge commit via
    `git commit-tree` (two parents: receiver, source), then `update-ref`.
  - Conflict case: gk refuses with a hint to materialize a worktree
    (`gk worktree add <path> <receiver>`) and resolve interactively.
  - `--squash` is currently only supported on the worktree path.

### Next-step hints

After a successful `gk merge --into <receiver>` (either path), gk prints
two indented hint lines to stderr:

```
  next: gk push --from <receiver>
  also: gk branch clean — <source> is fully merged
```

The cleanup hint is suppressed when source equals receiver, and when
`<source>` is a protected branch (`branch.protected`, plus the resolved
base branch) — being fully merged does not make trunk worth deleting.
It points at `gk branch clean` rather than naming a per-branch delete
verb, because gk has none; the merged source shows up as one of its
candidates. Hints render
in normal mode too; Easy Mode swaps the wording for a friendlier
description with emoji. Pass `--no-easy` to disable Easy Mode rendering;
the hints themselves still print using the normal-mode catalog.

### Merge plan

`gk merge` builds a plan from:

- merge-tree conflict results
- `git log --oneline HEAD..<target>`
- `git diff --stat HEAD..<target>`
- `git diff --name-status HEAD..<target>`

When an AI summarizer is available, the payload goes through the same privacy gate as other AI commands and is summarized as a merge plan. If no provider is available, gk prints a local fallback plan from the same git facts. If conflicts are predicted, the plan is printed and the actual merge is blocked.

### Examples

```bash
# Precheck, then merge main into the current branch
gk merge main

# Explain what would merge, without touching the tree
gk merge main --plan-only

# Fast-forward only
gk merge origin/main --ff-only

# Prepare a squash merge
gk merge feature/foo --squash

# Merge with tracked local changes
gk merge main --autostash

# Land the current branch on local main, even if main has no worktree
gk merge --into main

# Same, but explicit source
gk merge feat/x --into main
```

Automatic conflict correction is intentionally not part of the default merge path. After a paused conflict, `gk resolve --ai` remains an explicit opt-in because it mutates user code and can silently choose the wrong semantic side.

---

## gk rebase

Declarative history editing — `git rebase -i` with the editor session replaced by a JSON contract. The caller (a human script or an AI agent) states each commit's fate; gk validates the plan against the real history and drives git's own rebase machinery with a pre-built todo. Nothing ever opens an editor: reword messages travel in files (`git commit --amend -F`), squash accepts git's combined message.

### Workflow

```bash
gk rebase --plan-template               # 1. current range as a JSON draft (all "pick")
# 2. edit actions / messages / order — the judgment step
gk rebase --plan - < plan.json          # 3. validate + execute
gk rebase --plan plan.json --dry-run    #    preview the todo, touch nothing
```

The template emits `{schema, onto, commits:[{action, commit, subject, pushed}]}` oldest-first; feed back either the same object or a bare array. Actions: `pick` · `squash` · `fixup` · `reword` (requires `message`) · `drop`. Commits may be addressed by any unambiguous SHA prefix.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--plan <file\|->` | — | JSON plan to execute (`-` = stdin) |
| `--plan-template` | false | Emit the current range as a plan draft and exit |
| `--onto <ref>` | `@{u}`, else remote base | Base of the rebase range (`<onto>..HEAD`) |
| `--allow-pushed` | false | Permit rewriting commits that already exist on a remote (force-push required afterwards) |

### Validation — what gets refused

Every rule exists to stop silent history mangling:

- every commit in the range must be addressed **exactly once** — a forgotten commit is an error, not an implicit pick; dropping must be explicit
- unknown, ambiguous, or out-of-range SHAs
- merge commits in the range (cannot be replayed by this engine)
- `squash`/`fixup` as the first entry (nothing to meld into)
- `reword` without a `message`; a `message` on any other action
- rewriting commits that exist on a remote, unless `--allow-pushed` — the guard starts at the first deviation from the original order, since everything after it is rewritten even under `pick`

### Safety & failure

A backup ref (`refs/gk/backup/<branch>/<ts>`) is written before anything moves. On conflict the standard paused contract applies: report the paused state, then continue after manual resolution or an explicitly requested `gk resolve`; use `gk resolve --ai` only as AI conflict-resolution opt-in, preferably after `--dry-run`. A paused command exits 3 and is listed as a result under `--json`, not an error. A rebase/merge already in progress is refused up front.

```bash
gk rebase --plan plan.json --json
# → {result: completed|conflict|dry-run, onto, pre, post, backup_ref, todo?, conflict?}
```

---

## gk clone

Clone a repository with short-form URL expansion.

### Synopsis

```
gk clone [owner/repo | alias:owner/repo | url] [target] [flags]
```

With no positional argument, gk opens an interactive **browse-and-pick** over the repositories under your configured account profiles — see [No arguments](#no-arguments--browse-and-pick) below.

### Dispatch order

gk inspects the first positional argument in this order and stops at the first match:

1. **Scheme URL** (`http://`, `https://`, `ssh://`, `git://`, `file://`) — handed to `git clone` unchanged.
2. **SCP-style URL** (`user@host:path`) — handed to `git clone` unchanged.
3. **Alias shorthand** (`alias:owner/repo` where `alias` is listed under `clone.hosts` in config) — expanded against the alias's host and protocol. When the alias carries an `owner`, the shorter `alias:repo` also works — the owner is completed from the profile (an ownerless alias fails that form with a "no owner configured" hint).
4. **Bare shorthand** (`owner/repo`) — expanded against `clone.default_host` and `clone.default_protocol`.

A trailing `.git` on shorthands is tolerated and reattached by gk so the final URL is always canonical.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--ssh` | false | Force SSH URL form for this invocation. Mutually exclusive with `--https`. |
| `--https` | false | Force HTTPS URL form for this invocation. Mutually exclusive with `--ssh`. |
| `--dry-run` (global) | false | Print the resolved URL and target directory, then exit without calling `git`. |

### Config (`.gk.yaml` → `clone:`)

```yaml
clone:
  default_protocol: ssh        # or https; SSH by default
  default_host: github.com
  root: ~/work                 # optional Go-style layout: <root>/<host>/<owner>/<repo>
  hosts:
    gl: { host: gitlab.com, protocol: ssh }
    work: { host: git.company.internal, protocol: https }
    personal: { host: github.com, owner: JINWOO-J }               # account profile
    corp: { host: github.com, owner: acme, ssh_host: github.com-acme }
  post_actions: [hooks-install, doctor]
```

- `root` — when set, bare `gk clone owner/repo` drops the checkout at `<root>/<host>/<owner>/<repo>` instead of the current directory. An explicit `[target]` positional always wins over this.
- `hosts` — per-alias `host` + optional `protocol` (falls back to `default_protocol` when omitted). Unknown aliases are passed to `git` verbatim in case they encode something git already understands (e.g., `host:port/path`). Optional `owner` turns the alias into an account profile (`alias:repo` completes the owner; `gk init` lists it in the remote picker), and optional `ssh_host` swaps an `~/.ssh/config` Host alias into ssh URLs for multi-account key separation — see [`clone.hosts`](config.md#clonehosts).
- `post_actions` — run gk subcommands inside the fresh checkout once the clone succeeds. Supported values: `hooks-install` (runs `gk hooks install --all`), `doctor` (runs `gk doctor`). Failures print a warning but do not fail the clone.

### Examples

```bash
gk clone JINWOO-J/playground           # → git@github.com:JINWOO-J/playground.git
gk clone --https JINWOO-J/playground   # → https://github.com/JINWOO-J/playground.git
gk clone gl:group/service              # → git@gitlab.com:group/service.git (via alias)
gk clone personal:playground           # → git@github.com:JINWOO-J/playground.git (owner from profile)
gk clone git@host:team/proj.git        # SCP URL passes through unchanged
gk clone https://example.com/x/y       # scheme URL passes through unchanged
gk clone --dry-run foo/bar             # prints url + target, no network call
gk clone                               # browse + pick from configured account profiles
```

### No arguments — browse and pick

`gk clone` with no positional argument lists the repositories under every `clone.hosts` profile that has an `owner` set and resolves to **github.com**, then lets you pick one from a filterable interactive list; the chosen `owner/repo` is cloned through the same dispatch above (so the profile's protocol / `ssh_host` still apply).

The repository list is fetched from `api.github.com` **directly over HTTP — no `gh` binary is required**. An API token is resolved in this order, and the first hit wins:

1. `GH_TOKEN`
2. `GITHUB_TOKEN`
3. `gh`'s own stored auth — `~/.config/gh/hosts.yml` (or `$GH_CONFIG_DIR`), read as a plain file, so a prior `gh auth login` is reused even when the CLI itself isn't on `PATH`.

With none of those, the request is unauthenticated: public repositories only, subject to GitHub's 60-requests/hour anonymous rate limit. SSH keys authenticate the git wire protocol (clone/push), **not** this REST API, so they are not a token source here — without a token, a profile's private repositories do not appear in the list. Profiles whose host is not github.com (e.g. a GitLab alias) are skipped in this mode; clone them by typing `owner/repo` or a URL directly.

---

## gk log

Show a short, colorful commit log.

### Synopsis

```
gk log [revisions] [-- <path>...] [flags]
gk slog [revisions] [-- <path>...] [flags]
```

`slog` is an alias for `log`.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--format <fmt>` | (config `log.format`) | git `--pretty=format` string |
| `--graph` | false | Include topology graph |
| `-n, --limit <N>` | 20 | Max number of commits (0 = unlimited) |
| `--since <duration>` | | Show commits since this time |
| `--pulse` | false | Print a commit-rhythm sparkline above the log |
| `--calendar` | false | Print a 7-row × N-week heatmap above the log |
| `--tags-rule` | false | Insert a `──┤ v0.4.0 (3d ago) ├──` rule before each tagged commit |
| `--impact` | false | Append an eighths-bar scaled to per-commit `+adds -dels` |
| `--cc` | false | Prepend a geometric type glyph (`▲` feat · `✕` fix · `↻` refactor · `¶` docs · `·` chore · `◎` test · `↑` perf · `⊙` ci · `▣` build · `←` revert · `✧` style) + inline-color the matching subject prefix + append a `types: feat=4 fix=1` tally |
| `--safety` | false | Mark notable push-state: `◇` unpushed · `✎` recently amended · blank for the normal "already pushed" case so the column stays quiet until something deserves attention. Push state resolves against `@{upstream}`, falling back to **any** remote-tracking ref (`--remotes`) when no upstream is set, so local-only branches still mark correctly. A `──┤ ↑ N unpushed ├──` divider is drawn just above the first already-pushed commit, so the local-only block reads at a glance. |
| `--merged` | false | Prefix each commit with a base-integration marker: `○` for commits **not yet on the base branch**, blank for those already merged. Resolves the base the same way `gk status` does, so it marks exactly the commits `gk log --ahead --base` would list; skipped when the current branch *is* the base. SHA-identity based like `--safety` — a squash/rebase-merged commit reads as unmerged. Independent of this opt-in marker, a `──┤ ↑ N unmerged → <base> ├──` divider is drawn **by default** just above the first commit already on the base, so any branch ahead of its base shows the boundary at a glance (suppressed on the base branch itself, where every commit is trivially merged). When it coincides with the `--safety` push boundary the two collapse into a single `──┤ ↑ N unpushed · unmerged → <base> ├──` rule. |
| `--hotspots` | false | Mark commits that touch the repo's top-10 most-churned files |
| `--trailers` | false | Append a `[+Alice review:Bob]` roll-up from commit trailers |
| `--lanes` | false | Replace the commit list with per-author swim-lanes on a time axis |
| `--vis <list>` | `cc,safety,tags-rule` (from `log.vis`) | Visualization set (comma-list or repeated). Any explicit viz flag (`--vis` or an individual flag like `--cc`) overrides the configured default. Pass `--vis none` to disable all layers; setting `--format` alone also suppresses the default. |
| `--legend` | false | Print a one-time glyph/color key for every active visualization layer and exit. Mirrors `gk status --legend`. |
| `--behind` | false | Show commits the upstream has that HEAD does not (=`HEAD..@{u}`; preview before `gk pull`). Errors when the current branch has no upstream configured. Mutually exclusive with `--ahead`. |
| `--ahead` | false | Show commits HEAD has that the upstream does not (=`@{u}..HEAD`; preview before `gk push`). Same upstream / mutual-exclusion rules as `--behind`. |
| `--fetch` | false | With `--behind`/`--ahead`, run `git fetch <remote> <branch>` first so the range reflects current origin state. Off by default to keep `gk log` fast; pair with `--behind` when the count might be stale. |
| `--base` | false | With `--ahead`/`--behind`, compare against the **base branch** (resolved like `gk status`) instead of the upstream. `gk log --ahead --base` lists exactly the commits behind status's "ready to merge into &lt;base&gt;" line; `--behind --base` shows the reverse. Must be combined with `--ahead` or `--behind`; alone it errors. |
| `--ai` | false | Explain the shown commit range in plain language with AI, appended below the list — a reading companion for whatever range/pathspec `gk log` already selected, not a release-note generator (that's `gk changelog`). Grounded in the same deterministic signals `--hotspots`/`--wip`/`--breaking`/`--cc` compute (hotspot files, WIP chains, breaking commits, CC type tally, merged/unmerged vs base) so the model cites facts instead of inventing them. Composes with every other flag (`--graph`, `--lanes`, viz layers, `--json`). Large ranges are capped at 150 commits sent to the model (aggregate signals still cover the full range); a note is printed when truncated. |
| `--provider <name>` | | Override `ai.provider` for `--ai` |
| `--lang <code>` | | Override the AI summary language for `--ai` (`en`, `ko`, ...) — defaults to `output.lang` |
| `--no-cache` | false | With `--ai`, ignore any cached answer and query the provider again. Suppresses the cache *read* only — the fresh answer is still stored. Also available on `gk status`, `gk pr new`, `gk review`, `gk changelog`, and `gk merge`. |

### `gk log --ai`

Reads whatever commits `gk log`'s own filters selected (`--since`/`--limit`/pathspec/revision args, including `--ahead`/`--behind`/`--base` ranges) and appends an AI narrative section beneath the existing output — the list itself is never replaced, so `--ai` composes with piped/grepped output and every render mode (plain, `--graph`, viz layers, `--lanes`).

The facts sent to the model are structured, not raw commit text: commit subjects/authors (capped at the 150 most recent when the range is larger — aggregate counts below still reflect the full range), Conventional Commit type tally, breaking-commit count + sample subjects, squash/WIP-chain counts, hotspot files (skipped when a pathspec narrows the range, so a scoped `gk log -- path --ai` never leaks unrelated file names), and merged/unmerged counts against the resolved base (skipped when the current branch *is* the base, matching `--merged`'s own guard). This keeps the summary honest — it cites what's in the payload rather than inferring from bare subjects the way a plain "summarize these commits" prompt would.

Standard AI pipeline: `ai.commit.allow_remote` gates remote providers (skips gracefully with a stderr note, not a hard failure — `gk log` itself never fails because `--ai` couldn't run), the privacy gate redacts secrets/`deny_paths` before any remote payload, and answers are cached under `.git/gk-ai-cache/log/` keyed on the redacted payload + language + provider + Easy Mode state. Every `--ai` surface shares this pipeline, so `--no-cache` and the provider/model credit footer behave identically across them.

```bash
gk log --ai                        # summarize the default range
gk log --since 1w --ai --lang en   # narrower range, English summary
gk log --json --ai                 # {"entries": [...], "ai_summary": {"text", "provider", "model", "lang", "cached"}}
```

`gk log --json` **without** `--ai` is unchanged — still a bare `[]LogEntry` array. The wrapped `{entries, ai_summary}` shape only appears when `--ai` is combined with `--json`; `ai_summary` is omitted (not sent as null) when the AI call didn't run.

### Default visualization layers

When `gk log` is invoked with no viz flag, it applies the set in `log.vis`
(default `[cc, safety, tags-rule]`). The resolver works in two steps:

**Step 1 — baseline**
- `--vis <list>` replaces the baseline entirely (the "start fresh" form).
- `--vis none` empties the baseline.
- `--format <fmt>` with nothing else suppresses the baseline so the raw
  pretty-format stays in control.
- Otherwise the configured `log.vis` is the baseline.

**Step 2 — individual flags layer on top**
- `--cc`, `--impact`, `--safety`, ... (true) add the name to the set.
- `--cc=false` removes it from the set (handy to peel one layer off the
  default without rewriting the full list).

Concrete examples:

| Command | Effective set |
|---------|---------------|
| `gk log` | `cc, safety, tags-rule` (from config) |
| `gk log --impact` | `cc, safety, tags-rule, impact` (default + impact) |
| `gk log --cc=false --impact` | `safety, tags-rule, impact` (drop cc, add impact) |
| `gk log --vis cc,impact` | `cc, impact` (replace) |
| `gk log --vis cc,impact --trailers` | `cc, impact, trailers` |
| `gk log --vis none` | (none) |
| `gk log --vis none --impact` | `impact` (start empty, add impact) |
| `gk log --format "%H %s"` | (none — raw pretty-format wins) |
| `gk log --format "%H" --cc` | `cc` (format suppresses default; --cc re-enables one layer) |

### Since shortcuts

The `--since` flag accepts git-native strings and short forms:

| Input | Equivalent |
|-------|-----------|
| `1w` | `1 week ago` |
| `3d` | `3 days ago` |
| `12h` | `12 hours ago` |
| `"last monday"` | `last monday` |

### Examples

```bash
# Show last 20 commits (default)
gk log

# Show commits from the past week
gk log --since 1w

# Show last 50 commits with graph
gk log -n 50 --graph

# Show commits touching a specific file
gk log -- README.md

# Show commits on a specific branch
gk log main

# Show commits since 3 days ago on a path
gk log --since 3d -- internal/

# Visualizations (all composable)
gk log --pulse --since 30d                # sparkline of daily commit counts
gk log --calendar --since 12w             # 7-row × 12-week heatmap
gk log --tags-rule                        # separator row before each tagged commit
gk log --cc --impact                      # CC glyphs + per-commit LOC bars
gk log --safety --hotspots --trailers     # push state + hotspot marker + trailer roll-up
gk log --lanes                            # author swim-lanes instead of commit list
```

### Notes

- The default format shows: short hash, relative age, author name, subject, and ref decorations.
- Override the format permanently via `log.format` in your config file. See [docs/config.md](config.md).
- `--json` outputs a JSON array of commit objects.

---

## gk find (alias: search)

One-call history search: `gk find <query>` runs three searches **at the same time**, across every ref, and reports which one matched.

| mode | what it asks | raw equivalent |
| --- | --- | --- |
| `message` | the commit message mentions it | `git log --grep` |
| `content` | the commit added or removed it | `git log -S` (the "pickaxe") |
| `path` | the commit touched a matching file | `git log -- '*<query>*'` |

The gap it closes is **not** "gk log lacks `--grep`" — adding that flag would be a 1:1 swap. It is that you cannot know *which* query will hit, so the hunt costs a turn per guess:

```
git log --all --oneline --grep="OTLP exporter"      # nothing
git log --all --oneline | grep -i otlp             # nothing
git log --all -p -S "OTLPExporter" -- internal/    # found it
```

`gk find OTLPExporter` is that same hunt in one call. Commits matched by more than one mode rank first (the strongest signal there is), then by recency; each row is tagged with what matched, so "the message never mentions it but the code changed here" is visible rather than re-derived with another query.

```bash
gk find OTLPExporter                    # all three modes, every ref
gk find "fleet watch" --since 2w        # narrow by time
gk find tildePath --path internal/cli   # narrow to a subtree
gk find --path docs/commands.md         # no query: the history of a path
gk find OTLP --json                     # agent contract
```

Flags: `-n/--limit` (default 20) · `--since` · `--author` · `--path` · `--ref` (search one ref instead of all) · `--no-message` / `--no-content` / `--no-path` to narrow the fan-out — `--no-content` drops the pickaxe, which is the slow mode on large repos because it must diff every commit.

A mode that fails (an unknown `--ref`, say) is reported in `failed` alongside whatever the other modes found: a partial answer beats no answer, but it must never read as a complete one.

**What `gk find` does not answer:** "what is in B that is not in A" (`git log A..B`). That is a range comparison, not a search. Use `gk log --ahead` / `--behind` (add `--base` to compare against the base branch instead of the upstream) for those.

---

## gk chat

Talk to your repository. Unlike `gk ask` (one answer from pre-collected
context), chat runs an **agentic loop**: the model calls read-only tools
itself — `git_log`, `git_show`, `git_diff` (digest-first), `git_blame`,
`git_grep`, `file_read`, `file_list`, `git_status` (structured dirty/
staged/conflict/stash/in-progress-op summary), `git_snapshot_list` /
`git_snapshot_diff` (gk's `refs/wip` safety net — see `gk snapshot`, not
`git stash`), and `git_context` (re-collects the same repo-orientation
snapshot the system prompt starts with, for when state may have drifted
mid-conversation) — to investigate before answering, and every tool call
is shown as a one-line feed. Ask "when and why did this function
change?" and it chains log → blame → file reads on its own, citing the
SHAs and `file:line` evidence it actually saw.

Text answers stream to the terminal token-by-token as the model writes
them. A round that calls tools (there's no final text yet to stream)
falls back to a normal non-stream request for that round; JSON/agent
mode (`--json`/`GK_AGENT=1`) never streams, since the envelope needs the
whole answer at once.

### Synopsis

```
gk chat                    # interactive REPL
gk chat "<question>"       # one-shot answer
gk chat --continue         # resume the most recent session
gk chat --session <id>     # resume a specific session
gk chat sessions           # list sessions (id, started, title, turns)
gk chat sessions prune     # expire old session files (opt-in)
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--provider <name>` | (config `ai.provider`, else auto) | Tool-calling HTTP providers only: `anthropic`, `openai`, `groq`, `nvidia`. CLI-based providers (gemini/qwen/kiro) are not supported — use `gk ask` with those. The provider is fixed for the whole session (vendor tool-call IDs are not portable mid-conversation) |
| `--model <name>` | | One-shot model override for this run |
| `--lang <code>` | (`output.lang`) | Answer language (`en`, `ko`, …) |
| `--continue` | false | Resume the last session from `.git/gk-chat/` — a missing or corrupt previous session degrades to a fresh one with a warning, never an error |
| `--session <id>` | | Resume a specific session by id (see `gk chat sessions` for ids). Mutually exclusive with `--continue`. Opening either target re-marks it as the `--continue` pointer, so resuming an older session here also makes it the next bare `--continue`'s target |

One provider round is budgeted by `ai.chat.round_timeout` (default `120s`, distinct from `ai.chat.timeout`'s 30s single-shot budget): a chat round carries tool definitions, repo context, accumulated results, and the adapter's internal 5xx retries, so a proxy with occasional slow/500 responses needs the headroom. `round_timeout` only widens the provider's retry-*loop* deadline (`RetryBudget`) — each individual HTTP attempt keeps its own smaller, independent timeout, so one hung attempt can no longer eat the whole round budget and starve the retries it was meant to cover. On a timeout the error names the knob.

A brand-new session's very first round may fail before any provider-specific state exists yet (no vendor tool-call IDs committed to history) — that one round is retried against the next tool-calling candidate in the provider chain before giving up. Once the session has any history (this turn or a resumed one), a failure is returned as-is instead: switching providers mid-conversation would corrupt the vendor's tool-call ID space. Separately, a reply that requests a tool call violating the tool's schema (unknown tool name, missing required argument) gets exactly one semantic reprompt — the model's own bad reply plus a synthetic error result are fed back once; a call that's still broken after that surfaces as a normal dispatch error on the next round.

### REPL

↑/↓ walk the session's question history (shell-style line editing on a
real terminal) — `--continue`/`--session` seed this history from the
resumed session's own prior user turns, so the arrow keys reach past
questions from an earlier run too, not just this process's. Tab
completes a `/` command once it uniquely matches a prefix (`/clear`,
`/compact`, `/exit`, `/help`, `/quit`, `/rename`, `/tokens`).

`/help` lists commands. `/clear` resets the conversation context — a
marker is recorded so `--continue` sees the same empty context (the file
keeps the full record for audit). `/rename <title>` sets the session's
display title, used by `gk chat sessions` in place of the
first-user-message fallback (a control record, like `/clear`'s marker —
it costs one JSONL line and never touches prior history; renaming twice
keeps the latest title). `/tokens` breaks the current context down into
system prompt / history / tool-results (chars and an approximate token
count) against `ai.chat.history_budget`, and the same 80%-of-budget
check runs automatically after every turn once history gets that full.
`/compact` asks the provider to summarize every turn except the most
recent two into a single synthetic message — the two most recent turns
are always kept verbatim, so the investigation state you're mid-way
through is never paraphrased away — and records the fold in the session
file so a later `--continue` replay sees the compacted history, not the
original transcript; a provider without summarization support reports
that plainly instead of attempting it. `/exit` or Ctrl-D quits. Ctrl-C
during a turn cancels **that turn only**; at the prompt it exits. A
failed turn (provider error, timeout) never kills the session — it is
rolled back whole (and marked so `--continue` agrees), keeping the
conversation valid for the vendor APIs; retry or rephrase.

Sessions persist turn-by-turn as append-only JSONL under
`.git/gk-chat/sessions/` (worktree-safe via `rev-parse --git-path`), so a
crash costs at most the line being written.

### `gk chat sessions`

Lists every session under `.git/gk-chat/sessions/*.jsonl`, newest first —
no separate index file, this is a directory scan plus a lightweight
per-file pass. Each row shows the id, when it started, a title (an
explicit `/rename`, else the first user message truncated to 60
characters), and how many turns it holds; a `*` marks the session `gk chat
--continue` would resume. Under `GK_AGENT=1`/`--json` it returns an array
of `{id, started_at, title, turns, current}`. A session file predating
`/rename` (no title record at all) lists fine — the fallback just applies.

```
gk chat sessions
```

#### `gk chat sessions prune`

Deletes session files whose last activity (file mtime) is older than a
retention window — **off by default**. Unlike `gk snapshot prune` (which
falls back to a hardcoded 7-day window when neither a flag nor config is
set), `gk chat sessions prune` with nothing configured is a genuine no-op:
conversation history is not disk-safety-net material the way `refs/wip`
snapshots are, so accidental deletion by default is the wrong tradeoff.
Opt in with `--keep-days N` or `ai.chat.session_retention_days` in
`.gk.yaml` (flag wins when both are set). The session `--continue` would
resume is never pruned, even if it falls outside the window.

```
gk chat sessions prune --keep-days 30
```

| Flag | Default | Description |
|------|---------|-------------|
| `--keep-days <n>` | 0 (no pruning) | Expire sessions inactive for this many days. Falls back to `ai.chat.session_retention_days` when not passed |

### System context

Every session's system prompt carries a `REPO_CONTEXT` block collected
once at session start: branch, detached-HEAD state, upstream,
ahead/behind, a dirty summary (staged/unstaged/untracked/conflicts), any
in-progress rebase/merge/cherry-pick/revert (with its resume/abort
commands), base-branch drift, the latest tag, and how many linked
worktrees exist — the same orientation `gk context` collects. In a
long-running REPL conversation this can go stale (you start a rebase in
another terminal, switch branches, …); the `git_context` tool lets the
model re-collect the identical snapshot on demand instead of trusting a
stale one or guessing.

Setting `ai.chat.auto_context: true` additionally injects a `REPO_MAP`
block — a directory tree of tracked files built from `git ls-files`,
capped at 3 levels of nesting and 300 file lines (deeper subtrees
collapse to `...`, an overflow past the file cap adds a trailing count)
— so "what does this project look like?" doesn't cost a
`file_list`/`git_grep` round trip. Off by default: it spends prompt
tokens on every session even when the question never touches repo
layout. `deny_paths` is applied to the tree, so turning `auto_context`
on never names a file the tools would refuse to name.

### Security model

The sandbox — not the prompt — is the enforcement boundary:

- **Read-only, repo-only.** File access resolves symlinks *before* the
  containment check, blocks `.git/`, and refuses submodule/other-worktree
  paths. Git subcommands are whitelisted (log/show/diff/blame/grep) with
  execution-time argument validation (no flag injection via refs or
  patterns).
- **deny_paths applies to history too.** `git show`/`git diff` output is
  split per file and blocks touching denied paths are withheld (with an
  explicit note); `git grep` excludes them structurally via
  `:(exclude,glob)` pathspecs. A denied file's content cannot reach the
  provider through any tool, including historic commits.
- **Redact-before-persist.** Every tool result passes secret redaction
  (including vendor token patterns: `ghp_`, `xox`, `sk-`, AWS, PEM)
  before it is sent to the provider *or* written to the session file.
- **Limits are global-config-only.** `ai.chat.max_tool_rounds` (15),
  `ai.chat.tool_result_cap` (32KB), and `ai.chat.deny_paths` are honored
  from the global config only — a cloned repo's `.gk.yaml` cannot raise
  chat's budget or touch its deny surface (same trust boundary as
  `resolve.verify`). The effective deny list is always a union with the
  built-in defaults. The remote policy (`ai.commit.allow_remote`) is
  re-checked every round, so flipping it off mid-session takes effect
  immediately.
- Per turn: max 15 tool rounds, 192KB cumulative tool output, and
  identical repeated calls are refused after 2 executions.

### Agent mode

The REPL requires an interactive terminal and is refused under
`GK_AGENT`/`--json`/CI. One-shot works everywhere; under agent mode it
returns `{answer, tool_calls[], provider, model, lang, session_id,
rounds, tokens_used}` in the standard envelope.

A failed one-shot turn still emits this envelope on stdout (`answer`
empty, the rest filled in with whatever the turn produced before it gave
up), plus the standard error envelope (`hint` + `error.remedies[]`) on
stderr, classified by what actually failed:

- Round budget exhausted (`ai.chat.max_tool_rounds`) → `state: blocked`,
  with remedies to raise the round budget or retry with a faster
  `--model`.
- A single round timing out (`ai.chat.round_timeout`) → `state: error`,
  with the same `round_timeout`/`--model` remedies (retrying the
  identical turn will not help by itself).
- Every candidate provider failing to start a brand-new session →
  `state: blocked`, pointing at `gk doctor`.
- Ctrl-C / context cancellation → `state: error`, no remedy.

---

## gk status

Show concise working tree status.

### Synopsis

```
gk status [flags]
gk st [flags]
```

`st` is an alias for `status`.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--vis <list>` | `gauge,progress,base,tree,staleness` (from `status.vis`) | Visualization layers (comma-list or repeated). Pass `--vis none` to disable all layers for a single invocation. Values: `gauge`, `bar`, `progress`, `types`, `staleness`, `tree`, `conflict`, `churn`, `risk`, `base`, `since-push`, `stash`, `heatmap`, `glyphs`. |
| `-f`, `--fetch` | false | Fetch the current branch's upstream before reporting ↑N ↓N. Off by default — `gk status` does no network activity unless this flag (or `status.auto_fetch: true`) is set. |
| `--xy-style` | `labels` (from `status.xy_style`) | Per-entry state column: `labels` (`new`/`mod`/`staged`/`conflict`, self-documenting, default), `glyphs` (`+` `~` `●` `⚔` `#`, compact), or `raw` (git's two-character code like `??`/`.M`/`UU`). |
| `--top N` | 0 (unlimited) | Limit the entry list to N paths after action-priority sorting: conflicts → staged → modified → untracked, then path. A `… +K more (total · showing top N)` footer surfaces the hidden remainder so truncation is never silent. |
| `--exit-code` | false | Exit with a status-specific code after printing: `0` clean, `1` committable dirty, `2` submodule-only dirty, `3` conflicts, `4` behind upstream. |
| `--watch` | false | Live change-feed: a timeline of **which files — and which functions — change** as you or an AI agent edit the tree. Each change appends a `new` / `re-touched` / `cleared` event (glyph + `· funcName` + `+N −M` stat + time; the function names come from git's own hunk contexts, no setup or external tooling) under a compact status header (repo · branch ⇄ upstream ↑↓ · HEAD short-sha + age + subject · file count + total ±; the age chip reads `now` for a commit under a minute old, so a commit landing mid-watch is visible immediately). Press `[s]` for the full status dashboard (the rich tree / divergence / activity blocks) in place of the feed. Not supported with `--json` or `--exit-code`. |
| `--watch-interval <n>` | `2s` | Polling-fallback / heartbeat interval for `--watch`, given as **seconds** (`--watch-interval 2`, the common form) or a duration (`500ms`, `2s`, `1m`). Clamped to `[250ms, 60s]`. **Passing it implies `--watch`** — it has no meaning otherwise. Only the fallback rate when fsnotify is unavailable; with fsnotify active it's just a slow safety-net heartbeat. |

`--watch` keys (TTY only): `s` toggle the full status dashboard · `r` force refresh · `p`/`space` pause/resume · `c` clear the feed · `+`/`-` double/halve the polling interval · `q`/`esc`/`Ctrl-C` quit. The trigger is **fsnotify** (reacts the instant a file changes, idle cost ≈ 0); when it can't be set up — an unsupported platform, a tree larger than the descriptor budget, or a setup error — `--watch` **falls back to interval polling**. fsnotify watches the working tree recursively, skipping `.gitignore`d directories (`node_modules`, …) and `.git`, and follows directories created at runtime. The header shows the active mode (`● live (fsnotify)` or `every Ns (poll)`) and a `● just changed` accent (~1.5s) on each transition. Snapshots call `git status --porcelain -z` / `diff -U0` with `--no-optional-locks` so polling never contends with a concurrent `git add`. Piped (non-TTY) output is an append-only `tail -f` stream of event lines (`HH:MM:SS  ~ path  · funcName  +N −M  note`), so `gk st --watch | tee feed.log` keeps working.
| `--ai` | false | Append a plain-language AI explanation of the current state and next safe actions. Not supported with `--json` or `--watch`; falls back to a local plan if no provider is available. |
| `--provider <name>` | `ai.provider` | Provider override for `--ai`. |
| `--lang <code>` | `output.lang` / `ai.lang` | Language override for `--ai`, e.g. `en` or `ko`. |

### Per-entry state column (`--xy-style`)

The two-letter porcelain code git emits (`??`, `.M`, `MM`, `UU`) is cryptic when half the tree shares the same state. `gk status` resolves each entry to one of three representations:

| Mode | Example row | Best for |
|------|-------------|----------|
| `labels` (default) | `├─ new       docs/intro.md` | self-documenting — no lookup required |
| `glyphs` | `├─ + docs/intro.md` | compact dashboards, dense trees |
| `raw` | `├─ ?? docs/intro.md` | git-literate users, scripting off the output |

Label mapping (labels mode):

| XY | Label | XY | Label | XY | Label |
|----|-------|----|-------|----|-------|
| `??` | `new` | `M.` | `staged` | `.M` | `mod` |
| `!!` | `ignored` | `A.` | `added` | `.D` | `del` |
| `UU`/`AU`/`UA`/`UD`/`DU` | `conflict` | `D.` | `deleted` | `.R` | `ren` |
| `DD`/`AA` | `conflict` | `R.` | `renamed` | `.T` | `typ` |
| `MM`/`AM` | `split` (staged + unstaged) | `C.` | `copied` | `.C` | `cop` |

Glyph mapping (glyphs mode) collapses to five categories: `+` new, `#` ignored, `●` staged (any action), `~` worktree-dirty, `◉` both (staged + worktree), `⚔` conflict. Granularity is deliberately lower — glyph mode trades per-action precision for visual density.

Colors (dim gray / green / yellow / red) are applied consistently across all three modes, so switching styles never loses the category cue.

### Next action, submodules, and exit codes

`gk status` prints a `next:` hint using gk commands: `gk resolve` for conflicts, `gk commit --dry-run` for committable work, `gk status -v` for submodule-only dirtiness, and `gk sync` / `gk push` for clean branches that are behind/ahead.

Submodule worktree dirtiness is separated from committable superproject changes. If the submodule has untracked or modified files inside but the superproject gitlink did not change, the main tree remains clean and status renders a `submodules:` section. Use `-vv` to read each dirty submodule's branch and internal counts without changing directories.

### Cross-worktree hint (in-sync clean state)

When the current worktree is fully in sync (no `↑/↓`) and the working
tree is clean, `gk st` no longer ends on a flat "nothing to do"
placeholder. It scans the other worktrees in the same repository and
reports up to three with pending work:

```
worktree feat/x: ↑3  ·  worktree main: ↓2  ·  +N more
```

When every worktree is also clean: `all clean across N worktree(s)`.

Detection is divergence-only — each worktree is queried with
`git -C <path> rev-list --left-right --count HEAD@{upstream}...HEAD`.
Dirty-tree checks are intentionally skipped to keep the latency budget
intact. Any per-worktree git failure causes that entry to be silently
dropped rather than blanking the whole hint.

### AI next-step explanation

Use `gk status --ai` when the compact status is not enough and you want
the state translated into a short plan:

```bash
gk status --ai
gk st --ai --lang ko
```

The assistant receives structured facts such as branch, upstream,
ahead/behind counts, conflict counts, and a short path preview. It does
not receive patch contents. It may only recommend commands from gk's
precomputed safe command list.

### BRANCH section (rich mode)

Rich-mode `gk status` (`-v`) renders a dedicated **BRANCH** section that always surfaces the current branch name, upstream, divergence, and last-commit age — and tags the worktree when the invocation runs from a linked (non-primary) one.

```
█  BRANCH
   gk · feature/tmux ← main  @ tmux  ⇄ origin/feature/tmux  ↑0 ↓0  · last commit 22m abc1234
   wt: ~/work/project/agentic/gk/tmux
```

The `<repo> ·` prefix names the project derived from `--git-common-dir`, so captures and logs shared elsewhere carry their project context. The `← <parent>` segment names the fork parent, resolved through `branchparent` so per-branch metadata wins over `origin/HEAD`; it is suppressed on the trunk itself and when the resolver can't pin a parent down. The `@ <wt-name>` annotation and the `wt: <path>` line are suppressed when the current worktree is the primary one; the annotation is additionally suppressed when the worktree directory name matches the current branch (the common case under `~/.gk/worktree/<repo>/<branch>`) so the same token doesn't appear twice. The `wt:` path condenses `$HOME` to `~`. Detached HEADs render as `⚠ detached at <sha>`.

### Upstream fetch (opt-in)

By default `gk status` reads only local state — no network call. Pass `-f`/`--fetch` to refresh the current branch's upstream ref (the one recorded in `branch.<name>.remote` / `branch.<name>.merge`) before reading porcelain output, so the ↑N ↓N counts reflect the live remote rather than the last-cached view. The fetch is intentionally scoped and safe:

- Only the single upstream ref is fetched — no `--all`, no `--tags`, no submodule recursion, no FETCH_HEAD write.
- A 3-second hard timeout means a slow or flaky remote never blocks status beyond that budget.
- `GIT_TERMINAL_PROMPT=0` + empty `SSH_ASKPASS` prevent credential prompts from hijacking the terminal.
- stderr is discarded so `remote: …` chatter does not interleave with status output.
- On any failure (offline, auth expired, timeout) the fetch is silently dropped and status renders with the local cached view.
- Debounced: repeated `-f` invocations within a 3-second window reuse the previous fetch rather than hitting the network again.

To always fetch without typing the flag, set `status.auto_fetch: true` in `.gk.yaml` (or `~/.config/gk/config.yaml`). When upstream is not configured (detached HEAD, brand-new branch, no remote) the fetch is skipped silently.

#### `--vis` values

| Value | Effect |
|-------|--------|
| `gauge` | Replaces `↑N ↓N` with a divergence gauge `[▓▓│····]` (ahead on the left, behind on the right, upstream marker in the middle). |
| `bar` | Stacked `[▓████▒▒░░░]` bar whose segments are proportional to conflicts/staged/modified/untracked counts. |
| `progress` | `clean: [███░░░░░░░] 30%  stage 5 · commit 3 · resolve 1 · discard-or-track 1` — staged ratio + remaining-verb list. |
| `types` | Extension histogram (`.ts×6 .md×2 .lock×1`). Collapses known lockfile basenames to `.lock`; dims binary/lockfile kinds. Suppressed above 40 distinct kinds. |
| `staleness` | Annotates the branch line with `· last commit Xd ago`, and every dirty entry — changed and untracked alike — with its last write as a relative age (`· 12m`, `· now` under a minute) — one lstat per displayed entry plus one `rev-parse --show-toplevel` so root-relative paths resolve from any subdirectory. mtime is "last touched": a checkout or formatter refreshes it too. The JSON output carries the same signal as `entries[].modified_at` (RFC3339; absent for deleted paths). |
| `tree` | Replaces the flat sections with a hierarchical path trie. Single-child directory chains collapse; directory rows carry a subtree-count badge `(N)`. |
| `conflict` | Appends `[N hunks · both modified]` to each conflicts entry. Hunk count is derived from `<<<<<<<` markers in the worktree file. |
| `churn` | Appends an 8-cell sparkline to each modified entry (per-commit add+del totals over the file's last 8 commits). Suppressed when the dirty tree has more than 50 files. |
| `risk` | Flags high-risk modified entries with `⚠` and re-sorts the section so the hottest files are on top. Score is `diff LOC + distinct-authors-over-30d × 10`, threshold 50. |
| `base` | Appends a second `  from <trunk> [gauge]` line on feature branches showing how far the current branch has diverged from its base (or fork-parent, see below), plus a short action hint: `→ ready to merge into <base>` (ahead-only, clean tree), `→ behind <base>: gk sync` (behind-only), or `→ <base> moved: gk sync` (diverged). When `gk branch set-parent` has recorded a fork-parent for the current branch and the parent ref still exists, that parent replaces the trunk in this line — stacked workflows see `from feat/parent ↑2` instead of `from main ↑12`. Otherwise base resolves from `base_branch` config → `refs/remotes/<remote>/HEAD` → `main`/`master`/`develop`. If the recorded parent has been deleted, `gk status` writes a one-line `warning: parent <X> not found (deleted?); using <base>` to stderr and falls back to the trunk. Suppressed when the current branch *is* the base or HEAD is detached. Costs one `git rev-list --left-right --count` call (~5–15 ms). |
| `local` | **On by default.** Appends a working-tree change badge to the branch line — `· 5 unstaged · 1 staged · 2 conflicts` (zero layers omitted; hidden entirely on a clean tree). The unpushed layer is owned by `since-push` and `↑A ↓B`, so the badge intentionally omits it — between the three, the branch line shows every local-change layer exactly once. Submodule entries are excluded. Cost: 0 (pure aggregation over porcelain output already parsed). |
| `since-push` | **On by default.** Appends `· since push Xh (Nc)` to the branch line when there are unpushed commits, showing the age of the oldest one and the total unpushed count. When no upstream is configured it falls back to "commits on no remote-tracking ref" and switches the label to `· unpushed Xh (Nc)`, so local-only branches still report their count (matches `gk log --safety`). Suppressed on up-to-date branches and when the repo has no remotes at all. Cost: one `git rev-list @{u}..HEAD --format=%ct` call (~5 ms), or `git rev-list HEAD --not --remotes` on the no-upstream fallback. |
| `stash` | Adds a `  stash: 3 entries · newest 2h · oldest 5d · ⚠ 2 overlap with dirty` line when the stash is non-empty. Overlap warning checks whether the top stash touches any currently-dirty file (the common `git stash pop` footgun). Cost: one `git stash list` call + one `git stash show --name-only stash@{0}` when overlap-check applies (~5–10 ms total). |
| `heatmap` | Prints a 2-D density grid above the entry list: rows = top-level directory, columns = `C` conflicts / `S` staged / `M` modified / `?` untracked. Each cell glyph scales (` `/`░`/`▒`/`▓`/`█`) with the peak count for the current state. Designed for large-repo triage — at 100+ dirty files the flat tree scrolls off-screen but the heatmap stays a single block. Cost: 0 (pure aggregation over porcelain output). |
| `glyphs` | Prepends a semantic file-kind column to every entry (flat + tree): `●` source · `◐` test · `◆` config · `¶` docs · `▣` binary/asset · `↻` generated/vendored · `⊙` lockfile · `·` unknown. Kind is derived from path (extension, basename, prefix) — zero I/O, zero git calls. Combines well with the XY porcelain column: kind tells you *what* the file is for, XY tells you *what git thinks of it*. |

### Examples

```bash
# Show working tree status
gk status

# Short alias
gk st

# JSON output
gk status --json

# Default viz (gauge + bar + progress)
gk status

# Disable all viz for a single run
gk status --vis none

# Override the default: only the divergence gauge
gk status --vis gauge

# Multiple visualizations (either syntax works)
gk status --vis gauge,bar,progress
gk status --vis gauge --vis bar --vis progress

# Hierarchical view with conflict detail
gk status --vis tree,conflict

# Risk-weighted sort plus churn sparklines
gk status --vis risk,churn
```

### Output format

Output groups files by state:

- **Staged** — changes added to the index
- **Unstaged** — tracked files with uncommitted modifications
- **Untracked** — new files not yet added
- **Conflicted** — files with merge/rebase conflicts

Also shows ahead/behind commit counts relative to the upstream branch.

When `--vis tree` is active, the flat sections are replaced by a single hierarchical tree.

### Notes

- Uses `git status --porcelain=v2 -z` internally for reliable, locale-independent parsing.
- `LC_ALL=C` is enforced for all git calls.

---

## gk local

Roll up everything that exists **only on this machine** into one screen: working-tree changes (unstaged / staged / conflicts), commits that are on no remote (unpushed), and stash entries. A focused companion to `gk status` for the single question "what have I got locally that isn't safe yet?".

Alias: `gk lo`.

### Synopsis

```
gk local [-n N] [--all] [--json]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-n`, `--limit` | `10` | Max commits/files to list per section (`0` = unlimited). |
| `--all` | off | Scan every local branch and worktree instead of just the current branch. |
| `--json` | off | Emit a structured report instead of the text view. |

### `--all` — repo scope

The default scope is the current branch, which **cannot answer "is any work stranded on this machine"**: a branch that was never pushed is invisible from where you stand, and from every other machine too. `--all` widens the scan to every local branch and every worktree.

`--json` reports `scope` (`"branch"` or `"repo"`) so a caller knows what `clean` covers, and under `--all` adds a `branches[]` breakdown. `clean` becomes the repo-wide rollup: false if any branch has unpushed commits, any worktree is dirty, the stash is non-empty, or any branch's state could not be determined.

Anything unverifiable is never reported clean. A branch whose worktree is missing from disk, or whose status failed, is marked `unknown: true` and forces `clean: false` — a reassuring answer that was never checked is worse than admitting the gap. For the same reason `--all` does not reuse the picker's dirty-state helper: that one caps itself at 200 ms and treats a timeout as clean, and it drops untracked files as noise. Here an untracked file *is* the stranded work.

```bash
# Before switching machines: is anything stuck here?
gk local --all

# One boolean for a script or a handoff gate
gk local --all --json | jq .clean
```

```
LOCAL  — 4 branch(es) scanned, repo-wide
  cleanup-branch                     no upstream · 2 unpushed  (cleanup-branch)
  develop                            1 unstaged  (gk)
  wt/init-guards                     no upstream · 1 unpushed
  (stash)                            1 entr(ies)
  hint: push what should travel — gk push --set-upstream — before switching machines
```

Only branches with something stranded are listed; a roster of clean branches would bury the answer. `--json` still carries every branch.

### Unpushed resolution

The unpushed list resolves against `@{upstream}` first, then falls back to "commits on no remote-tracking ref" (`git log HEAD --not --remotes`) when no upstream is configured — so branches that were never pushed still report their local-only commits. When the repo has no remotes at all, push state is undeterminable and the section prints `no remote to compare against` (and `--json` sets `unpushed_known: false`). This matches the fallback used by `gk log --safety` and `gk status`'s `since-push`/`local` layers.

### Examples

```bash
# What exists only locally right now?
gk local

# Unlimited listing, machine-readable
gk local -n 0 --json
```

### Output

```
feature  — local only
  working tree  2 unstaged · 1 staged
      ~ a.txt
      + c.go
  unpushed  2 commits
      ◇ 71d47d4  local 2  (5 minutes ago)
      ◇ c61b92b  local 1  (5 minutes ago)
  stash  1 entry
```

A clean, fully-pushed repo prints `✓ nothing local-only — everything is committed and pushed`.

---

## gk next

Explain the current repository state and next safe actions in plain
language. This is the direct "what should I do now?" entry point and
uses the same assistant as `gk status --ai`.

### Synopsis

```
gk next [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--run`, `-r` | false | Execute the single top recommended next step after confirmation. The command is taken from gk's deterministic action allowlist (never free-form AI output); risky commands and non-TTY sessions are refused with a copy-paste hint. |
| `--provider <name>` | `ai.provider` | Provider override. |
| `--lang <code>` | `output.lang` / `ai.lang` | Language override, e.g. `en` or `ko`. |

If the AI provider is unavailable, `gk next` prints a local next-step
plan instead of failing the workflow.

---

## gk branch

Branch management helpers.

### Synopsis

```
gk branch <subcommand> [flags]
```

### Subcommands

| Subcommand | Description |
|-----------|-------------|
| `list` | List branches with optional stale/merged filters |
| `clean` | Delete merged branches (respecting protected list) |
| `pick` | Interactively choose a branch to checkout |

---

### gk branch list

List local branches with optional filters.

#### Synopsis

```
gk branch list [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--merged` | false | Only show branches merged into base |
| `--unmerged` | false | Only show branches NOT merged into base (mirror of `--merged`) |
| `--gone` | false | Only show branches whose upstream has been deleted on the remote |
| `-s, --stale <N>` | 0 (all) | Only show branches with last commit older than N days |

`--merged` and `--unmerged` are mutually exclusive.

#### Examples

```bash
# List all local branches
gk branch list

# List branches not touched in 30+ days
gk branch list --stale 30

# List branches already merged into base
gk branch list --merged

# List branches NOT merged into base
gk branch list --unmerged

# List branches whose remote was deleted (typical after a PR merge + branch delete)
gk branch list --gone

# Combine filters
gk branch list --stale 14 --merged
```

---

### gk branch clean

Delete local branches, skipping protected branches.

#### Synopsis

```
gk branch clean [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | false | Show what would be deleted without deleting |
| `--force` | false | Use `git branch -D` (force delete) instead of `-d` |
| `--gone` | false | Target branches whose upstream is gone instead of merged ones |
| `--worktrees` | false | Also delete branches checked out in a worktree (removes the clean worktree first; dirty ones are skipped) |

#### Examples

```bash
# Preview which merged branches would be deleted
gk branch clean --dry-run

# Delete merged branches
gk branch clean

# Clean up branches whose remote was deleted (after PR merges)
gk branch clean --gone

# Force-delete branches (even if not fully merged)
gk branch clean --force
```

#### Notes

- The currently checked-out branch is never deletable.
- Protected branches (`main`, `master`, `develop` by default) are blocked by default. With `--force` they surface in the picker marked `[protected]` and left unchecked — you must tick them manually, so a batch `--yes` run never deletes `main` by accident. Configure the list via `branch.protected`.
- Branches checked out in a worktree show a `[worktree]` marker and are skipped unless `--worktrees` is passed (which removes the holding worktree first, skipping dirty ones with a warning).
- `--gone` uses the `%(upstream:track)` field of `git for-each-ref` to identify branches marked `[gone]` — typically the ones whose PR was merged and the remote branch deleted.

---

### gk branch pick

Interactively choose a local branch and check it out.

#### Synopsis

```
gk branch pick [flags]
```

#### Flags

No command-specific flags. All global flags apply.

#### Examples

```bash
# Open interactive branch picker
gk branch pick
```

#### Notes

- Presents a filterable list of local branches using an interactive TUI prompt.
- Falls back to a simple numbered list when running in a non-TTY environment.
- Use `--dry-run` to see which branch would be checked out without switching.

### gk branch set-parent

Record the fork-parent of the current branch in `branch.<current>.gk-parent`. Once set, `gk status` compares divergence against the parent instead of the repository's mainline — useful for stacked workflows where a feature branch is forked off another feature branch rather than from `main`.

When no parent is recorded, display surfaces (`gk status`, the worktree base comparison) also try to **infer** one from history: the branch's creation point (its oldest reflog entry) is looked up with `for-each-ref --contains`, and if exactly one other local branch contains it, that branch is used as the parent (source: `inferred`). Zero or multiple candidates — the normal case in shared-trunk repos where `main` and `develop` both contain the branchpoint — fall back to the configured base, so inference never guesses. Merge destinations are exempt by design: `gk land` / `gk promote` hop targets only ever come from explicit `gk-parent` metadata or the trunk fallback, never from inference.

#### Synopsis

```
gk branch set-parent <parent>
```

#### Validations

The write is rejected (with a non-zero exit code and a one-line message) when:

- `<parent>` is empty
- `<parent>` equals the current branch (no self-parent)
- `<parent>` looks like a remote-tracking ref (`origin/main`, `upstream/...`, `fork/...`) — use the local branch name instead
- `<parent>`'s ref name is malformed (rejected by `git check-ref-format`)
- `<parent>` is a tag, not a branch
- `<parent>` does not exist locally — the closest local branch is suggested via Levenshtein fuzzy match
- assigning would create a cycle in the parent chain, or the chain would exceed 10 hops

#### Examples

```bash
# Mark feat/auth-jwt as the parent of the current branch
gk branch set-parent feat/auth-jwt

# Common typo — gk suggests the closest match
gk branch set-parent mian
# → branch "mian" does not exist; did you mean "main"?
```

### gk branch unset-parent

Clear the fork-parent metadata of the current branch (`git config --unset branch.<current>.gk-parent`). Idempotent: succeeds silently when no parent is set. Status output reverts to base-relative divergence on the next invocation.

#### Synopsis

```
gk branch unset-parent
```

#### Examples

```bash
# Stop tracking a parent (e.g., after the parent branch was merged into main)
gk branch unset-parent
```

---

## gk switch

Switch to another branch. When no name is given, opens an interactive picker that lists both local branches and remote-only tracking branches — picking a remote-only entry creates a local tracking branch automatically (equivalent to `git switch --track <remote>/<branch>`). Pressing `r` in the picker reveals the remote-only rows, fetching first when the cached `refs/remotes/*` view is stale so you see what is actually on the remote; pass `--fetch` up front to refresh and show them immediately.

`gk switch <name>` for a branch that doesn't exist locally no longer dead-ends on git's `invalid reference`: gk checks the remote and offers to fetch and track `<remote>/<name>` when it exists there, otherwise offers to create the branch from HEAD (creation defaults to "no" so a typo doesn't spawn a branch). Off a TTY it prints the matching hint (`gk sw --fetch <name>` or `gk sw -c <name>`) instead of prompting.

### Picker layout

```
●  feature/api-v2       → origin/feature/api-v2    2d    ← local branch (filled green)
●  hotfix/login          -                          5h    ← local, no upstream
○  release/v2            (from origin)              3d    ← remote-only (hollow cyan)
○  experimental          (from origin)              1w
```

- `●` — local branch. Pass to `git switch <name>` directly.
- `○` — exists only on a remote. `gk sw` auto-runs `git switch --track <remote>/<name>` to create the local tracking branch.
- Remote entries whose short name already matches a local branch are hidden (avoid duplicate picks).
- `refs/remotes/<remote>/HEAD` aliases are filtered.
- Sorted recent-first within each group; local first, then remote-only.
- Picker hotkeys: `r` shows remote-only branches — fetching first when the last successful fetch is stale (older than 60s) or never happened, and toggling instantly otherwise; `f` always forces a `git fetch --prune` for the configured remote. A failed fetch still reveals the cached branches with a warning. When remotes are shown, the subtitle reports freshness (`fetched 3m ago`, `never fetched`, or `fetch failed`).
- Each hotkey has a `ctrl` alias — `ctrl+r`, `ctrl+f`, `ctrl+d`, `ctrl+n` — that fires **while the `/` filter is focused**, where bare letters are swallowed as filter text. Both forms work in nav mode. The alias is read before the filter input sees the key, so it wins over any editing binding on the same combo.
- Filtering also searches hidden remote-only branches; `/ tmux` can surface `origin/tmux` even before pressing `r`.
- `d` deletes with `git branch -d`. When git refuses (unmerged, protected, or the default branch) the confirm prompt is promoted to a **force** prompt quoting that reason, and accepting runs `git branch -D`. There is no separate force key: a terminal cannot encode `ctrl+D` distinctly from `ctrl+d`. Rejections git would refuse either way — the current branch, a remote row — are never promoted.
- While typing a `/` filter, single letters feed the filter box, so only the `ctrl` aliases fire. `Esc` stages out: the first press leaves the filter box but keeps the narrowed list (so `n`/`d`/`f`/`r` act on the highlighted row), a second `Esc` clears the filter and restores the full list, and a third (or `Esc` with no active filter) cancels. `q` / `Ctrl+C` cancel immediately from any state.


### Synopsis

```
gk switch [branch] [flags]
```

Alias: `gk sw`.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-c, --create` | false | Create a new branch with the given name before switching (`git switch -c`) |
| `-f, --force` | false | Discard local changes (`git switch --discard-changes`) |
| `--detach` | false | Detach HEAD at the ref instead of switching to a branch |
| `--fetch` | false | Refresh remote branches before switching; when opening the picker, show remote-only rows immediately |
| `-m, --main` | false | Switch to the detected main/master branch — no branch argument needed |
| `-d, --develop` | false | Switch to the `develop` / `dev` branch — no branch argument needed |

`--main` and `--develop` are mutually exclusive and incompatible with a positional `branch` argument or `--create`.

### Keyword resolution

`--main` resolves in this order:

1. `client.DefaultBranch(remote)` (honors `refs/remotes/<remote>/HEAD`, then looks for `develop`/`main`/`master`)
2. Local `main`
3. Local `master`

`--develop` resolves in this order:

1. Local `develop`
2. Local `dev`

Both exit with an error when no candidate exists in the repo.

### Examples

```bash
# Interactive picker — local + remote-only branches, recent-first
gk switch

# Direct switch
gk switch feat/login

# Fetch first, then switch to a newly-created remote branch
gk switch --fetch feat/new-from-remote

# Create and switch in one step
gk switch -c feat/billing

# Jump to the canonical main branch (works for both main- and master-based repos)
gk switch -m

# Jump to develop (falls back to 'dev')
gk switch -d

# Remote-only pick becomes: git switch --track origin/release/v2
# → local `release/v2` tracking `origin/release/v2` is created, then switched
```

---

## gk reset

Fetch the current branch's upstream and hard-reset the working tree to it. **Destructive.**

### Synopsis

```
gk reset [<ref>] [flags]
```

A positional `<ref>` is an alias for `--to` — `gk reset main` is the same as
`gk reset --to main`.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--to <ref>` | upstream | Override target ref (e.g. `origin/main`); default uses the configured upstream |
| `--to-remote` | false | Reset to `<remote>/<current-branch>` regardless of configured upstream |
| `--remote <name>` | config.remote / `origin` | Remote to fetch from |
| `-y, --yes` | false | Skip confirmation prompt (required for non-TTY automation) |
| `--clean` | false | Also run `git clean -fd` to remove untracked files |
| `--dry-run` | false | Print what would happen without fetching or resetting |

`--to`, `--to-remote`, and a positional `<ref>` are mutually exclusive.

### Examples

```bash
# Reset to the branch's tracked upstream (prompts for confirmation)
gk reset

# Reset to a specific ref via positional alias (same as --to main)
gk reset main

# Preview without touching anything
gk reset --dry-run

# Reset to origin/<current> even if no upstream is configured
gk reset --to-remote --yes

# Reset to an explicit ref
gk reset --to origin/main --yes

# Reset and wipe untracked files in one step
gk reset --yes --clean
```

### Notes

- Requires either a TTY confirmation or `--yes`; non-TTY callers without `--yes` fail fast.
- Runs `git fetch <remote> <ref>` before the reset so the target is up to date.
- This command only rewrites HEAD — it does NOT create a backup ref. Use `gk undo` afterwards if you need to recover; reflog still has the pre-reset HEAD.

---

## gk restore

Recover work that is no longer reachable from any branch — a commit orphaned by a reset, a rebase that dropped a commit, a deleted branch.

### Synopsis

```
gk restore --lost [--limit N]
```

`--lost` scans `git fsck --lost-found` for dangling commits and blobs and renders them as a restorable list with cherry-pick hints, so the recovery is a read of one list instead of a raw fsck dump you have to decode.

### Flags

| Flag | Description |
| --- | --- |
| `--lost` | Show unreachable commits/blobs found by `git fsck --lost-found` |
| `--limit N` | Maximum entries to show (default 20) |

### Notes

- This is the verb behind raw `git fsck --lost-found` / `--unreachable` / `--dangling`: `gk session audit` maps those forms here.
- It only *surfaces* lost work; nothing is written. Restore an entry with the cherry-pick command it prints.
- For "I want to go back to a moment", not "I lost a commit", see `gk undo` (reflog picker) and `gk timemachine`.

---

## gk undo

Read git reflog, pick a past HEAD state, and reset to it after recording a
backup ref at `refs/gk/undo-backup/<branch>/<unix>`. Undoing the undo is the
same command run twice.

### Synopsis

```
gk undo [--to <ref>] [--soft | --hard] [--list] [--limit N] [-y]
```

Without `--to`, an interactive picker lists recent reflog entries. `--to
<ref>` (e.g. `HEAD@{3}`) skips the picker and resets straight there.

### Reset modes

| Mode | Flag | HEAD | Index | Working tree |
|------|------|------|-------|--------------|
| mixed (default) | — | moves | reset to target | preserved (your edits become unstaged) |
| soft | `--soft` | moves | **untouched** | **untouched** |
| hard | `--hard` | moves | reset to target | reset to target (**current edits gone**) |

`--soft` is the uncommit move: HEAD rewinds but the undone commits' changes
stay staged — use it before a squash or to rewrite a commit message. In a
non-interactive run (`--json` / `GK_AGENT` / no TTY), a bare `gk undo --soft`
with no `--to` defaults to `HEAD~1` — "uncommit the last commit"; interactive
runs keep the picker so `--soft` stays a mode, not a separate flow. `--soft`
and `--hard` are mutually exclusive, and all three modes write the same backup
ref.

### Agent mode

With `--json` / `GK_AGENT=1` the reset emits `{schema, result, from, to,
backup_ref, mode}` — `mode` is `"soft"`, `"mixed"`, or `"hard"`, so an agent
can tell which reset ran. `--list` emits `{schema, entries[]}` instead, and an
empty reflog is reported as `state:"blocked"` rather than bare prose on a
success exit.

### Examples

```bash
# Interactive picker over recent reflog entries
gk undo

# Uncommit the last commit, keeping its changes staged
gk undo --soft

# Rewind two commits, discarding working-tree changes
gk undo --to 'HEAD@{2}' --hard --yes

# Just print the reflog (no prompt, no reset)
gk undo --list
```

### Notes

- A dirty tree is not a hard stop in `--mixed`/`--hard`: gk offers to stash & auto-pop around the reset. `--soft` never stashes — a dirty tree is the point of uncommitting, and stashing would destroy exactly what you want kept.
- An in-progress rebase/merge/cherry-pick always blocks; finish or `gk abort` first.
- Recover from any undo with `git reset --hard <backup_ref>` (the command prints the ref).

---

## gk wipe

Discard ALL local changes AND untracked files. **Destructive — stronger than `gk reset`.**

### Synopsis

```
gk wipe [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-y, --yes` | false | Skip confirmation prompt |
| `--dry-run` | false | Print what would happen without wiping |
| `--include-ignored` | false | Also remove ignored files (`git clean -fdx` instead of `-fd`) |

### What it does

1. Writes a backup ref at `refs/gk/wipe-backup/<branch>/<unix>` pointing at the pre-wipe HEAD.
2. Runs `git reset --hard HEAD`.
3. Runs `git clean -fd` (or `-fdx` with `--include-ignored`).

Local commits remain recoverable via the backup ref (`git reset --hard refs/gk/wipe-backup/<branch>/<unix>`). Untracked files are **not** recoverable — they bypass git entirely.

### Examples

```bash
# Preview
gk wipe --dry-run

# Non-interactive wipe
gk wipe --yes

# Also remove .gitignore'd files (e.g. build artefacts in node_modules)
gk wipe --yes --include-ignored
```

### Notes

- Use `gk reset` if you only need to rewind HEAD and keep untracked files.
- The backup ref survives `git gc` as long as it is referenced; delete it with `git update-ref -d refs/gk/wipe-backup/<branch>/<unix>` once you no longer need it.

---

## gk wip

Create a throwaway `--wip-- [skip ci]` commit so you can switch contexts without losing work.

### Synopsis

```
gk wip
```

### What it does

1. `git add -A` — stages every tracked change, including deletions.
2. `git commit --no-verify --no-gpg-sign -m "--wip-- [skip ci]"` — skips hooks and signing for speed.

If the working tree is clean (nothing to commit), it reports `nothing to wip — working tree is clean` and exits 0.

### Examples

```bash
gk wip                # stash-like save without using the stash stack
git switch other-branch
# ... do something else, then come back ...
gk switch -
gk unwip              # restore the working tree
```

---

## gk wip repair

Rewrite one buried, unpushed WIP commit as AI-generated commits.

`gk wip` / `gk unwip` only reach a WIP commit sitting at HEAD. Once other
commits land on top, the WIP is buried in the first-parent history and
neither command can touch it. `gk wip repair` finds that buried WIP,
rebuilds only its diff into semantic commits through `gk commit`'s AI path,
then replays the later commits on top.

### Synopsis

```
gk wip repair [commit] [--yes] [--provider <name>] [--model <name>] [--lang <name>]
```

### What it does

1. Walks HEAD's first-parent history looking for a WIP-subject commit that is *not* HEAD itself (a HEAD WIP belongs to `gk commit -f`, not this command).
2. Refuses if the target or any commit between it and HEAD is already on a remote branch — rewriting pushed history is out of scope here.
3. Refuses if a merge commit sits between the target and HEAD — replaying merges would need rebase-merges, which this command intentionally does not support.
4. Without `--yes`, prints the plan (commit, subject, number of later commits to replay) and stops.
5. With `--yes`: writes a backup ref, checks out the WIP's parent in a throwaway detached worktree, runs `gk commit -f --no-wip-unwrap` there through the normal AI classify/apply path, then `git rebase --onto` the result to replay every later commit on the current branch.

### Flags

| Flag | Default | Effect |
|------|---------|--------|
| `-y, --yes` | false | Perform the rewrite after showing the plan. Without it, `wip repair` only prints the plan |
| `--provider <name>` | | AI provider forwarded to the temporary `gk commit` run |
| `--model <name>` | | AI model forwarded to the temporary `gk commit` run |
| `--lang <name>` | | AI output language forwarded to the temporary `gk commit` run |

### Examples

```bash
# Show the rewrite plan for the nearest buried WIP
gk wip repair

# Target a specific WIP commit and execute
gk wip repair a1b2c3d --yes
```

### Notes

- The working tree must be clean before `--yes` runs.
- A backup ref is written before the branch moves, so a failed replay leaves the original tip recoverable.
- Merge descendants and already-pushed commits are refused outright rather than attempted and rolled back.

---

## gk unstage

Drop files from the staging area without touching their working-tree
contents — the safe, index-only `git reset [-q] HEAD -- <paths>` form. With
no paths, everything staged is dropped. A clean index is a reported no-op.
History-moving resets (`--soft`/`--hard`, `HEAD~1`) are deliberately out of
scope: use `gk undo` (reflog restore with a backup ref) for those.

### Synopsis

```
gk unstage [path...]
```

With `--json` / `GK_AGENT=1` the result reports `{unstaged, files[]}` —
the exact set that left the index. `gk session audit` classifies the raw
unstage forms as covered by this verb (`raw-unstage`); resets that move
the branch stay in the `uncovered-raw-git` gap.

---

## gk apply

Apply patch files, retrying with progressively looser strategies when a plain
apply fails — so you don't hand-cycle `git apply` flags.

### Synopsis

```
gk apply <patch-file>... [--staged | --cached] [--check] [--reverse]
```

### Remediation ladder

Each patch walks a fixed strategy ladder and stops at the first rung that
applies; the result records which rung succeeded (`plain`, `recount`,
`recount+unidiff-zero`, `3way`):

1. **plain** — `git apply` as-is.
2. **recount** (`--recount`) — hunk header line counts are off.
3. **recount + unidiff-zero** (`--recount --unidiff-zero`) — zero-context patch.
4. **3way** (`--3way`) — fall back to a 3-way merge against the blobs the patch names.

In `--staged` mode on git < 2.35 (which can't combine `--cached` with
`--3way`), the 3-way rung is skipped rather than surfaced as a bogus flag error.

### Modes and flags

| Flag | Effect |
|------|--------|
| `--staged` / `--cached` | Apply to the index only, leaving the working tree untouched (`git apply --cached`) |
| `--check` | Probe without applying: report which strategy *would* apply each patch. Also predicts 3-way conflicts — a would-conflict 3-way check is normalized to a failure so `--check` matches the real outcome |
| `--reverse` | Apply the patch backwards (undo an applied patch) |

The global `--dry-run` maps onto `--check`. Both probe each patch against the
**current** tree independently, while a real multi-patch run applies patches
sequentially — so a stacked series can fail the probe yet apply for real.

### Atomic multi-patch

Multiple patches are all-or-nothing: if any patch exhausts the ladder, the run
rolls back every already-applied patch (the index and, in worktree mode, the
touched files restored from a pre-run snapshot) before reporting the failure.
A patch that reverse-applies cleanly is reported as **already applied**, with a
`--reverse` remedy instead of a generic context-mismatch error.

### Agent mode

With `--json` / `GK_AGENT=1` the result is:

```json
{
  "schema": 1,
  "result": "applied",
  "applied": [{"patch": "/abs/fix.patch", "strategy": "recount"}],
  "failed": null,
  "rolled_back": false
}
```

`result` distinguishes a real run (`applied`) from a `--check` probe (`check`)
and a global-`--dry-run` probe (`dry-run`), following the land/rebase
convention. `failed` is `null` when every patch applied, or `{patch, error}`
for the one that exhausted the ladder; `rolled_back` reports whether the
atomic rollback of the earlier patches succeeded.

### Examples

```bash
# Apply to the working tree (plain git apply scope)
gk apply fix.patch

# Apply to the index only
gk apply --staged fix.patch

# Probe without applying — which strategy would each patch need?
gk apply --check a.patch b.patch

# Undo an applied patch
gk apply --reverse fix.patch
```

### Notes

- The worktree rollback snapshot honours `.gitignore` (same as `gk snapshot`), so a patch that touched an ignored untracked file is not restored.
- `gk session audit` classifies raw `git apply` as covered by this verb (`raw-apply`, collapse group `apply`).

---

## gk unwip

Undo a WIP commit created by `gk wip`.

### Synopsis

```
gk unwip
```

### What it does

1. Reads HEAD's subject via `git log -1 --format=%s`.
2. Refuses unless the subject starts with `--wip--`.
3. Runs `git reset HEAD~1` so the committed changes return to the working tree.

### Examples

```bash
gk unwip
```

### Notes

- The refusal is intentional — `unwip` will never rewind a non-wip commit, so it is safe to run on top of a branch where you're not sure what's at HEAD.
- Pairs with `gk wip`; these commands are not intended for stash-like stacking. Use `git stash` if you need a stack.

---

## gk snapshot

Save a non-destructive safety-net snapshot of the working tree to `refs/wip/<branch>` without touching your working tree, index, or branch history.

### Synopsis

```
gk snapshot [-m <note>] [-q]
gk snapshot list            # alias: gk snapshots
gk snapshot restore [n] [-m <note>]
gk snapshot diff [n] [--stat]
gk snapshot prune [--keep-days <n>] [--all]
gk snapshot hook install|status|uninstall [--project] [--settings <path>]
```

### Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `-m`, `--message <note>` | `""` | Note recorded with the snapshot (shown in `gk snapshots`) |
| `-q`, `--quiet` | false | Suppress output (for hooks); still errors on failure |

### What it does

1. Stages the full working tree — tracked changes plus untracked files, respecting `.gitignore` — into a throwaway index, so the real index is never touched.
2. Writes that as a commit and points `refs/wip/<branch>` at it via `git update-ref --create-reflog`. The ref's reflog is the snapshot history.

Unlike `gk wip`, nothing is committed to your branch. The shadow ref never appears in `git branch`, is not pushed, and survives `git gc`. If the working tree is clean it reports `nothing to snapshot — working tree is clean` and exits 0. Detached HEAD is refused — there is no stable branch ref to anchor `refs/wip/` to.

### Restore

`gk snapshot restore [n]` restores snapshot `n` (default `0`, the latest) into the working tree and index. If the tree is dirty, the current state is first saved as a fresh snapshot so nothing is lost. Files present now but absent from the snapshot are left untouched.

### Diff

`gk snapshot diff [n]` shows what changed between snapshot `n` (default `0`) and the current working tree — the same direction a restore would apply, so added lines are what a restore would bring back. `--stat` renders a summary instead of the full patch.

### Prune / retention

`gk snapshot prune` expires snapshot reflog entries older than the retention window and deletes a branch's `refs/wip/<branch>` ref when every entry expired. The window resolves `--keep-days` → `snapshot.retention_days` in `.gk.yaml` → `7`. `--all` prunes every branch's snapshots instead of just the current one.

Setting `snapshot.retention_days: <days>` (default `0` = off) also auto-expires quietly after every `gk snapshot` save, so hook-driven snapshots don't accumulate forever. Auto-retention is best-effort — a failed expire never fails the save.

### Trigger automation (Claude Code Stop hook)

`gk snapshot hook install` writes a Claude Code **Stop hook** running `gk snapshot -q` into `~/.claude/settings.json`, so every finished AI turn checkpoints the working tree. `--project` targets this repository's `.claude/settings.json` instead; `--settings <path>` overrides the file entirely.

The installer only ever **appends** — existing Stop hooks and unrelated settings keys are preserved byte-meaning-for-byte-meaning, install is idempotent, and a settings file that fails to parse is refused rather than rewritten. `gk snapshot hook status` reports the current state; `uninstall` removes only the gk-managed entry (dropping a matcher group only when the removal leaves it empty).

### Examples

```bash
gk snapshot                      # save the current working tree
gk snapshot -m "before refactor" # save with a note
gk snapshots                     # list snapshots for this branch
gk snapshot restore              # restore the latest
gk snapshot restore 2            # restore an older one
gk snapshot diff                 # what changed since the latest snapshot
gk snapshot prune --keep-days 14 # expire entries older than two weeks
gk snapshot hook install         # auto-snapshot after every Claude Code turn
```

### Notes

- Designed as an automatic safety net: `gk snapshot hook install` wires the Claude Code Stop hook for you; any other trigger can call `gk snapshot -q` directly.
- The snapshot lives outside `refs/heads`, so it is purely local and never interferes with push, rebase, or `git branch`.

---

## gk worktree

Worktree management helpers. Wraps `git worktree` with an opinionated JSON output.

### Synopsis

```
gk worktree <subcommand> [flags]
```

Alias: `gk wt`.

### Subcommands

| Subcommand | Description |
|-----------|-------------|
| *(none)* | Interactive TUI — list, add, remove, cd. See below. |
| `list` | List worktrees (table or `--json`) |
| `add <name\|path> [branch]` | Create a worktree (managed base for relative names, passthrough for absolute paths) |
| `acquire <branch>` | Create or reuse an initialized worktree and print its path |
| `remove <path>` | Remove a worktree |
| `prune` | Prune worktree administrative records |
| `run <branch> -- <cmd>` | Create (or reuse) a worktree, run a command in it, optionally reclaim it with `--cleanup` |
| `finish` | Commit/promote the current worktree branch, optionally cleaning up the worktree |
| `cleanup` | Bulk-remove safe, finished worktrees |

### Interactive mode (`gk wt`)

Running `gk wt` or `gk worktree` without a subcommand opens an interactive picker that loops until you quit. Actions:

- **cd** — spawns a new `$SHELL` inside the selected worktree so you can work in it immediately. Type `exit` to return to your original shell at its original cwd. Inside the subshell, `$GK_WT` holds the worktree path and `$GK_WT_PARENT_PWD` holds where you came from. (See `--print-path` below for scripting workflows.)
- **remove** — confirm prompt → `git worktree remove <path>`. Dirty/locked worktrees get a follow-up "force-remove anyway?" prompt; stale admin entries auto-prune. After a clean remove you're also offered to delete the branch if no other worktree holds it.
- **add new** — the form opens on the create-new-branch toggle, since that choice selects between two different flows.
  - **new branch** — name, branch name, base ref. The base-ref field names the current branch as its default (`blank = <branch>`) — leaving it blank cuts the new branch from where you stand, not from main. A newly created branch records its fork parent (`gk-parent`), same as `gk worktree add`. Name collisions with an orphan branch surface an inline three-way choice (reuse / delete & recreate / cancel) instead of a dead-end error.
  - **existing** — opens a branch picker instead of asking you to type a name, then suggests the worktree name from the branch you chose (`feat/relay-agent-notify` → `relay-agent-notify`). Rows are ordered **newest commit first** — unlike `gk sw`, which lists locals alphabetically — because this list answers "which one was I working on". `●` local, `○` remote-only, `⊗` already checked out somewhere: occupied rows stay listed and name their worktree, but selecting one is refused (git allows a branch in one worktree only, and that includes the branch you are standing on). Remote branches are hidden until `ctrl+r`, yet join the list as soon as you type a filter. Picking one runs `git worktree add --track -b <name> <path> <remote>/<name>`, creating the local branch and its upstream in the same command. `Esc` at the name prompt returns to the picker rather than cancelling the whole form.
  - In both flows the name is resolved through the same managed-base rules as `gk worktree add` (see below).

Flags:

| Flag | Description |
|------|-------------|
| `--print-path` | On the **cd** action, write the chosen path to stdout instead of spawning a subshell. Use this for shell-alias wrappers that need to `cd` the parent shell itself. |

Shell-alias pattern (when you prefer staying in one shell):

```sh
# ~/.zshrc or ~/.bashrc
gwt() { local p="$(gk wt --print-path)"; [ -n "$p" ] && cd "$p"; }
```

On a non-interactive stdin/stdout (CI, piped input) the TUI falls back to printing this help instead of drawing a dead UI.

### gk worktree add

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-b, --new` | false | Create a new branch named `[branch]` at `--from` |
| `--from <ref>` | HEAD | Base ref for the new branch |
| `--detach` | false | Detach HEAD in the worktree instead of tracking a branch |

The success line names what landed where, so the base of a new branch is never a guess: `added worktree at <path> (new branch feat/x from main@8bd48c9)` — new branches are cut from **HEAD (the branch you run the command on)** unless `--from` says otherwise, never implicitly from main.

`--dry-run` describes the plan and touches nothing — no directory, no `git worktree add`, no init — printing `would add worktree at <path> …`. With `--json` it returns the same machine-readable result (`{path, branch, parent, created, managed, dry_run, init}`) the real run emits, so an agent reads `result.path` straight from the envelope instead of scraping the success line; under `--json` the init bootstrap runs silently (its link/copy/run log would otherwise corrupt the envelope) and reports only `init: done | skipped`.

A newly created branch also records its fork parent (`branch.<name>.gk-parent = <base>`), the same metadata `gk branch set-parent` writes — creation is the only moment the parent is known for certain (git's own reflog only says "Created from HEAD"). `gk status`, the `SOURCE` column, and `gk land --to` then resolve against the real parent instead of the trunk. Recording is skipped when the base is not a local branch (detached HEAD, remote ref, raw SHA) and is undone with `gk branch unset-parent`.

### gk worktree acquire

Agent-friendly setup: create or reuse a managed worktree for `<branch>`, run
`worktree.init` by default, and print the ready path.

```bash
gk worktree acquire feat/api --json
gk worktree acquire feat/api --from develop --no-init
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--from <ref>` | HEAD | Base ref when creating a new branch |
| `--init` | true | Run `worktree init` after create/reuse |
| `--no-init` | false | Skip bootstrap |

With `--json` (or `GK_AGENT=1`) the result is
`{path, branch, parent, created, reused, init}` so an agent can set its next
tool call's `workdir` directly from `result.path`.

#### Managed base directory

Relative path arguments are placed under a managed layout so worktrees for different projects do not collide:

```
<worktree.base>/<worktree.project>/<name>
```

- `worktree.base` defaults to `~/.gk/worktree`. Override in `.gk.yaml` or via the `GK_WORKTREE_BASE` env var. Leading `~/` is expanded against `$HOME`.
- `worktree.project` defaults to the repo's git-toplevel basename (`/Users/me/work/gk` → `gk`). Set it explicitly when two clones share the same basename (e.g. `work-gk` vs `personal-gk`).
- An **absolute path** always wins and is used verbatim — useful for one-off placements outside the managed layout.

Examples:

```bash
gk worktree add ai-commit -b ai-commit
# → ~/.gk/worktree/<project>/ai-commit, new branch 'ai-commit' off HEAD

gk worktree add feat/api -b feat/api
# → ~/.gk/worktree/<project>/feat/api (subdir preserved)

gk worktree add /tmp/exp -b hotfix
# → /tmp/exp (absolute path used as-is)
```

Project layout on disk:

```
~/.gk/worktree/
├── gk/
│   ├── ai-commit/
│   ├── feat/api/
│   └── bugfix/
├── playground/
│   └── spike-1/
└── ai-commit/
    └── review/
```

### gk worktree remove

| Flag | Default | Description |
|------|---------|-------------|
| `-f, --force` | false | Force remove even when the worktree is dirty or locked |

### gk worktree rename

Moves a linked worktree to a new location (`git worktree move`) and, with `--with-branch`, also renames the branch it holds (`git branch -m`, which carries the branch's upstream and `gk-parent` config across). Aliased as `gk worktree mv`.

`<worktree>` matches by managed name (the path's last segment), an absolute/relative path, or the checked-out branch name. `<new-name>` is resolved through the same managed layout as `gk worktree add` — a plain name lands under `<worktree.base>/<project>/<name>`; an absolute path is used verbatim. By default only the directory moves; the branch is untouched.

The main worktree can't be renamed. A locked worktree is refused unless its lock holder is gone (`--force`) or you override a live one (`--force-locked`), mirroring `gk worktree remove`. Renaming the worktree you're currently standing in prints a hint to `cd` to the new path.

```bash
gk worktree rename ai-commit ai-commit-v2            # move directory only
gk worktree rename feat-x feat-y --with-branch       # move directory + rename branch
gk worktree rename feat-x /tmp/exp                   # move to an absolute path
gk worktree rename ai-commit ai-commit-v2 --dry-run  # preview
```

| Flag | Default | Description |
|------|---------|-------------|
| `--with-branch` | false | Also rename the checked-out branch to `<new-name>` (`git branch -m`) |
| `-f, --force` | false | Unlock and move a worktree whose lock holder is no longer running |
| `--force-locked` | false | Move even when the lock holder is still running (dangerous) |
| `--print-path` | false | Print the new absolute path on success (for `cd $(…)` wrappers) |

With `--json` (or `GK_AGENT=1`) the result is `{old_path, new_path, old_branch, new_branch, with_branch, managed}`.

### gk worktree run

Runs a command inside a worktree for `<branch>` — the CLI form of an isolated, parallel task (the single-shot sibling of the Workflow worktree-isolation pattern). If a worktree is already checked out on `<branch>` it is reused; otherwise gk creates one (managed-base layout, `gk-parent` recorded) before running. Pass `--init` to run `worktree.init` for both newly created and reused worktrees. The command runs with the worktree as its working directory, and **gk exits with the command's exit code**.

Everything after `--` is the command, run directly (not through a shell), so chain with an explicit shell when you need operators:

```bash
gk worktree run feat/api -- go test ./...
gk worktree run hotfix -- make build                  # new branch + worktree off HEAD
gk worktree run feat/api --cleanup -- sh -c 'npm ci && npm test'
```

With `--cleanup`, a command that **succeeds** (exit 0) has its worktree removed — and, when this call created the branch, the branch deleted too. A **failing** command always leaves the worktree in place for inspection.

| Flag | Default | Description |
|------|---------|-------------|
| `--from <ref>` | HEAD | Base ref when creating a new branch |
| `--cleanup` | false | Remove the worktree when the command succeeds (and delete the branch if this call created it) |
| `--init` | false | Run `worktree init` before the command, including reused worktrees |
| `--no-init` | false | Skip `worktree init` on create |

With `--json` (or `GK_AGENT=1`) the result is `{path, branch, created, init, command, exit_code, removed}`; the command's own stdout moves to stderr so gk's stdout carries only the envelope.

### gk worktree finish

Wraps up the branch checked out in the current worktree. By default it runs
`gk promote` (commit, then merge one hop into `gk-parent` or the base) without
pushing. With `--push`, it runs `gk land --to <target>` instead.

```bash
gk worktree finish --to parent --cleanup
gk worktree finish --to base --push --cleanup --delete-branch
gk worktree finish --to develop --gate "xm panel {patch} --json" --gate-phase before --cleanup
```

| Flag | Default | Description |
|------|---------|-------------|
| `--to <target>` | `parent` | `parent`, `base`, or a branch |
| `--push` | false | Use `gk land --to <target>` instead of local `gk promote` |
| `--cleanup` | false | Remove the current linked worktree after a successful finish |
| `--delete-branch` | false | After `--cleanup`, delete the finished branch with `git branch -d` |
| `--autostash` | false | Pass `--autostash` to `promote`/`land` |
| `--gate <template>` | — | Quality-gate command run against the merge patch (whitespace-tokenized, no shell). e.g. `"xm panel {patch} --json"` |
| `--gate-arg <token>` | — | Gate command as explicit argv tokens (repeatable); canonical alternative to `--gate` for precise quoting |
| `--panel-review` | false | Alias for `--gate "xm panel {patch} --json"` |
| `--gate-phase before\|after\|both` | `before` | When to run the gate relative to the merge |
| `--gate-timeout <duration>` | `0` | Kill the gate command after this duration (e.g. `10m`); 0 = no timeout |
| `--gate-keep-patch` | false | Keep the temporary gate patch file instead of deleting it |
| `--resume-accept` | false | Accept a prior after-gate pause: skip merge/gate and run cleanup only |

`--cleanup` refuses to remove the main worktree. With `--json` (or
`GK_AGENT=1`) the result is `{mode, branch, to, path, cleanup, removed}` with
`branch_deleted` when requested.

#### Quality gate (`--gate`)

A gate runs an external review command against the exact patch a worktree merge
produces, and blocks/pauses the finish on failure. gk acquires the target lock
first, pins the target tip under it, then builds the patch — so the patch the
gate reviews is byte-for-byte what merges even when multiple worktrees finish in
parallel. The command is tokenized and run with no shell; each `{token}`
substitutes as a single argv element, so a value with spaces or shell
metacharacters cannot inject. Available tokens: `{patch}`, `{source}`,
`{target}`, `{base_sha}`, `{head_sha}`, `{target_before_sha}`,
`{target_after_sha}`, `{phase}`.

- **before** gate failure → nothing merges, `state:"blocked"` (target unchanged).
- **after** gate failure → the merge stands, `state:"paused"` (exit 3), cleanup
  is held, and `result.gate.recover` carries the resume/abort pair
  (`--resume-accept` to accept, or a rewind command to abort). `--resume-accept`
  only cleans up when the branch is actually merged into its target.
- `--push` cannot combine with `--gate-phase after|both` — an after gate cannot
  hold an integration that `--push` already published.

The target lock lives at `<git-common-dir>/gk/locks/` and each gate run writes
an audit record to `<git-common-dir>/gk/worktree-gate/<run-id>-<phase>.json`,
both shared across linked worktrees.

### gk worktree cleanup

Bulk reclaim safe, finished worktrees. Without `-y`, cleanup is a dry-run
report. The safe default skips the current worktree, dirty worktrees, live
locks, protected branches, detached/bare entries, and branches not merged into
their `gk-parent` or base.

```bash
gk worktree cleanup --merged --stale 7d --json
gk worktree cleanup --merged --stale 7d --delete-branches -y
```

| Flag | Default | Description |
|------|---------|-------------|
| `--merged` | true | Only remove branches merged into parent/base |
| `--stale <age>` | — | Require branch tip older than an age (`7d`, `12h`, `30m`) |
| `--delete-branches` | false | Delete the local branch after removing its worktree |
| `-y, --yes` | false | Actually remove candidates |
| `--force-stale-locks` | false | Unlock/remove stale locked worktrees |
| `--discard-dirty` | false | Destructively remove dirty worktrees with `git worktree remove --force` |

With `--json` (or `GK_AGENT=1`) the result is
`{dry_run, candidates, removed, skipped, failed}`.

### List columns

`gk worktree list` is built around one question — *can I delete this worktree?*
Each row pairs the branch's tip with its parent and the distance between them:

```
█  WORKTREES   4 entries
     BRANCH          HEAD     PARENT   VS PARENT  AGE  PATH
   ★ main            559ceab  -        -          2h   /Users/jinwoo/work/project/agentic/gk [dirty +2]
     fix-bug         559ceab  main     ● same     10d  /Users/jinwoo/.gk/worktree/gk/fix-bug
     old-spike       a1b2c3d  main     ● merged   3w   /Users/jinwoo/.gk/worktree/gk/old-spike
     improve-ux      d6c5d89  main     ↑2 ↓66     11d  /Users/jinwoo/.gk/worktree/gk/improve-ux
```

| Column | Meaning |
|---|---|
| `★` | The worktree this invocation runs from. |
| `BRANCH` | Local branch checked out in the worktree (or `(detached HEAD)` / `(bare)`). |
| `HEAD` | The worktree's checked-out commit, 7 chars — same form `gk log` and `gk context` print. Shown for detached and bare worktrees too. |
| `PARENT` | The branch this one is measured against: the recorded `gk-parent` (`gk branch set-parent`, or whatever `gk wt add` recorded), else a parent inferred from history — inferred ones render faint. `-` when neither resolves. |
| `VS PARENT` | Standing against `PARENT`. `● same` (green) — the two tips are the same commit. `● merged` (green) — the parent has everything this branch has and moved on. `↑2` / `↑2 ↓66` (yellow) — commits that live only here. `-` when the parent is unknown or its ref is gone. |
| `AGE` | Compact age of the branch's last commit (`5m`, `2h`, `10d`). |
| `PATH` | Absolute worktree path; long temp paths get a middle ellipsis with the basename preserved. |
| `FLAGS` | `[dirty +N]` (uncommitted paths), `[locked]`, `[prunable]`. |

Green `VS PARENT` means the branch holds no commit its parent lacks — the
precondition for reclaiming the worktree. It is not the whole verdict:
`[dirty +N]` marks work that was never committed at all, and a green row with
that flag still has something to lose. `gk worktree cleanup` applies the full
check (dirty, locks, protected branches) and is the safe way to act in bulk.

`--json` adds `parent`, `parent_source` (`explicit` / `inferred`),
`parent_ahead`, `parent_behind`, and `parent_state`
(`same` / `merged` / `ahead` / `diverged`) alongside the existing
upstream-relative `ahead` / `behind`.

The interactive TUI (run `gk worktree` with no subcommand) carries the same
reading: `BRANCH | PARENT | VS PARENT | HASH | AGE | PATH | FLAGS`. It is the
screen where `[d]` actually removes a worktree, so the standing is on every
row before the cursor gets there, and the removal prompt states it again —
a branch holding commits its parent lacks flips that confirm to default-No.
On a narrowing terminal `VS PARENT` outranks `AGE`: age only hints that a
worktree is abandoned, the parent standing says whether removing it loses
anything. `--global` (`g`) drops both parent columns — another project's
parents aren't resolvable from this repo.

### Examples

```bash
# JSON list for scripts
gk worktree list --json

# Track an existing branch under the managed base
gk worktree add feat-login feat/login

# New branch in a managed worktree off HEAD
gk worktree add feat-review -b feat/review

# New branch off a specific base (still managed)
gk worktree add hotfix -b hotfix/1.2.3 --from origin/main

# Absolute path wins — bypasses managed layout for this call
gk worktree add /tmp/gk-spike -b spike/wip

# Remove cleanly
gk worktree remove ~/.gk/worktree/gk/feat-login
```

---

## gk watch

Live supervision at whatever altitude fits where you run it (alias: `gk w`): inside a repo with several worktrees — or with the multi-repo flags — it opens the **dashboard**; with exactly one worktree it goes straight into the [`gk status --watch`](#gk-status) change feed; **outside any repo** — say the parent directory holding all your projects — it scans one level down and opens the dashboard over every repo it finds (`cd ~/work && gk watch` is the whole invocation; the view starts on the **active** filter — worktrees someone is plausibly in right now, header reads `5/21 repos` when hiding — with `--filter all` or one press of `f` showing everything; clean repos start folded, `space` unfolds). `gk fleet` is the **deprecated former name**, kept one release as a hidden alias (a stderr notice points here; the only behavioral difference is that `fleet` never auto-routes to the single-worktree feed). Config keys stay under `fleet.*`.

The dashboard shows every worktree at once — branch, ahead/behind, dirty/conflict state, the last-changed file, and which one is current. Built for supervising parallel work (e.g. several AI agents each in their own worktree); answers "who is dirty / stuck / behind" without a per-worktree status probe. Reuses the same enrichment `gk worktree list` uses (porcelain parse + ahead/behind + a consolidated per-worktree change scan).

The TUI renders a coloured table; each row rolls up to a `status` (`clean` / `dirty` / `conflict` / `paused` / `ahead` / `behind` / `diverged`). Below it a merged **change feed** streams which files changed in which worktree as they happen (`e` toggles; the startup dirty set is a silent baseline — only changes from then on are shown, ring-capped at 200). When filesystem watches can be established the dashboard reacts to edits instantly and the poll drops to a 12s heartbeat; otherwise it polls on `--interval`. The process-wide watch budget is allocated by **activity**, not headcount: worktrees that plausibly have someone in them (current checkout, dirty, paused op, or moved within the last hour — plus the zoomed worktree) divide the whole budget, idle ones ride the heartbeat, and the first change the heartbeat detects promotes an idle worktree to active so it gains a watcher one poll later. Re-planned every poll, so the allocation follows the work. All probes run with `GIT_OPTIONAL_LOCKS=0` so they never contend on `index.lock` with the agents editing those trees.

**How much is being written** reads on three levels, all from the scan the dashboard already runs (no extra git calls). Each worktree row carries its uncommitted diffstat (`+31 −3`), a repo group line sums its worktrees (`7 files  +31 −3` — a folded repo still reports its volume), and the header totals the visible fleet as two clusters on different time axes. **Not shipped yet** groups what is waiting to leave the worktree: the uncommitted diffstat (`~ 34 files +272 −6`, resets to zero when an agent commits) and the unpushed commit count (`↑2 unpushed`, committed but not pushed — the worktrees' `ahead` summed). **Flow** is **Δ**, the churn since watch started (`Δ +130 −6 · 5m`), which a commit does *not* erase — one cluster answers "how much is stacked up right now", the other "how much work went by while I was watching". A single dim `│` divides the two, drawn only when there is pending work to separate from flow. Δ counts the positive movement of each file's counts per poll, so a file touched five times is counted once per actual change, not five times over (summing the feed lines would do the latter — their `+/−` are cumulative against HEAD). What was already dirty when watch started is a baseline, not churn. Line counts come from the same diff runs as the feed's, so `--feed-stats=false` leaves the file counts and drops the `+/−`; a narrow terminal drops segments right-to-left (flow first, then unpushed, then uncommitted — never the count), and the `│` shows only while the pending segments it divides are all present.

Under `--json` (or `GK_AGENT=1`) it instead emits a one-shot machine-readable snapshot of the same data — the contract a GUI/agent polls, with the TUI as its consumer. `--events` streams changes as NDJSON instead (see below).

### Event stream (`--events`)

For an orchestrator, polling `--json` snapshots and diffing them is busywork — `--events` does the diff server-side and streams one NDJSON event per line: `file-changed` (`file`, `note: new|re-touched|cleared`; with `--feed-stats` also `added`/`removed` and `symbols` — the changed-function names from git's hunk contexts), `status-changed` (`from`/`to`), `op-start`/`op-end` (`operation`), and `land-ready`. Every event carries `ts`, `repo`, `branch`, and `path` (the worktree). Under `GK_AGENT=1` a single `{"schema":1,"state":"streaming","result":{"mode":"fleet-events"}}` header frame precedes the events so envelope consumers recognize the mode switch. Runs until interrupted; driven by filesystem events when available, with the same heartbeat fallback as the TUI.

```
GK_AGENT=1 gk watch --events | while read -r ev; do …; done
```

The opt-in `fleet.notify` config maps a transition to a shell hook (`sh -c`, with `GK_FLEET_KIND/BRANCH/PATH/REPO/OPERATION` in the environment; output discarded). Keys: `conflict` (a worktree hit conflicts), `paused` (an operation stopped mid-way), `land_ready` (a branch became fully merged into base). Hooks fire from both the TUI and `--events`.

### Multi-repo mode

By default `gk watch` watches the current repo's worktrees; run from a non-repo directory it auto-scans one level down (equivalent to `--scan . --depth 1`). For supervising agents spread across **separate repositories** (e.g. `~/work/project/agentic/{gk,aic-rust,…}`), opt into multi-repo mode with `--repos`, `--scan`, or `--all`. The snapshot then spans every discovered repo; the TUI groups worktrees under a per-repo header you can fold/unfold, and `--json` stays a flat array — each entry tagged with `repo`/`repo_root` so a consumer groups with `jq 'group_by(.repo_root)'`.

Discovery dedups by `git rev-parse --git-common-dir`, so a repo reached via a symlink or one of its linked worktrees collapses to a single entry. A repo that fails or times out (3s) becomes one synthetic `status:"error"` entry rather than silently vanishing. fleet stays local-only (never fetches) and runs its probes with `GIT_OPTIONAL_LOCKS=0` so it does not contend on `index.lock` with the agents editing those repos. A bare run inside a repo stays single-repo even if `fleet.repos`/`fleet.scan` are configured — config auto-activates multi-repo only when you run from outside any repo; use `--all` to force it from inside one.

### Synopsis

```
gk watch [--interval <seconds>] [--feed-stats] [--events]
         [--repos <path,…>] [--scan <dir,…>] [--all] [--depth <n>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--interval <seconds>` | `2` (single) / `5` (multi) | Poll interval in TUI mode (demoted to a 12s heartbeat while filesystem watches are active) |
| `--feed-stats` | `true` | +/− line counts and changed-function names on change-feed events, `gk status --watch` style. `--feed-stats=false` (or `fleet.feed_stats: false`) disables the two `git diff -U0` runs per **dirty** worktree per poll |
| `--events` | `false` | Stream fleet changes as NDJSON events instead of a dashboard (for orchestrators) |
| `--repos <path,…>` | — | Explicit repo paths to watch (enables multi-repo) |
| `--scan <dir,…>` | — | Directory roots searched for git repos (enables multi-repo) |
| `--all` | `false` | Watch sibling repos of the current repo; with no `--scan`/`--repos` it uses `fleet.repos`/`fleet.scan`, else scans the current repo's parent directory |
| `--depth <n>` | `2` | Max scan recursion depth for `--scan` |
| `--filter <mode>` | `active` (multi) / `all` (single) | Initial view filter: `all` \| `active` (current checkout · dirty · paused op · moved <1h) \| `busy` \| `stuck`. Also `fleet.filter` config; `f` cycles at runtime |

### Config (`fleet.*`)

```yaml
fleet:
  repos: [~/work/project/agentic/gk, ~/work/project/agentic/aic-rust]
  scan:  [~/work/project/agentic]   # roots searched depth-deep for repos
  depth: 2
  exclude: ["node_modules", "vendor", ".archive"]   # dir-name globs skipped while scanning
  interval: 2
  feed_stats: true                  # ±counts + changed-function names on feed events (default on; false to disable)
  notify:                           # opt-in transition hooks (sh -c, GK_FLEET_* env)
    conflict:   osascript -e 'display notification "conflict" with title "gk fleet"'
    paused:     ~/bin/notify-stuck.sh
    land_ready: ~/bin/reap-candidate.sh
```

### Keys (TUI)

`j`/`k` (or ↓/↑) move the cursor · `enter` cycles the cursor panel (status fields → that worktree's own live change feed → off; the fields view shows recent events and, for a land-ready branch, the suggested `gk worktree remove`) · `w` zooms into the selected worktree's live feed **in place** — the embedded `gk status --watch` view, where `esc` (or `w`) pops back to the table, `[` / `]` hop to the previous/next worktree, and `q` quits; fleet keeps gathering in the background so the table is fresh the moment you pop back · `e` toggles the change feed · `f` cycles the view filter (all→active→busy→stuck; multi-repo starts on active) · `s` cycles the sort (default→activity→status) · `r` refreshes now · `q` (or esc) quits. In multi-repo mode `space` folds/unfolds a repo group (clean repos start folded), and `enter` cycles the same cursor panel for worktree rows.

### Watcher allocation

Filesystem watchers cost descriptors, so they are a finite, process-wide budget rather than something every worktree gets. Active worktrees (plus the zoomed one, which you are staring at) are granted watchers up to that budget; the rest — and any subtree that outgrows its share while watch is running — still refresh on the heartbeat poll, so a shortage degrades the *cadence*, never the data. The footer states the split: `watch <watched>/<eligible> · budget <used>/<total>`, with `polling fallback` when an eligible worktree has no watcher and `saturated` when demand exceeds the budget outright. On descriptor exhaustion (`EMFILE`/`ENFILE`) a watcher releases everything it holds and its worktree drops to polling — the descriptors it was holding are what starves the rest of the process.

With `--json` / `GK_AGENT=1` the result is an array of `{repo, repo_root, path, branch, current, ahead, behind, dirty, status, last_change, files, added, removed, …}` (one snapshot, no polling; `added`/`removed` are 0 under `--feed-stats=false`). A non-TTY shell (pipe/redirect/CI) prints a static one-shot table — grouped by repo in multi-repo mode — instead of starting the interactive program.

---

## gk prompt-info

Emit a compact label for shell prompt integration. Three formats cover the common needs: a minimal linked-worktree marker (`plain`), a unified `<repo>/<branch>` label suitable for replacing starship's `$directory` + `$git_branch` (`segment`), and a structured payload for prompt frameworks that compose their own segments (`json`).

### Synopsis

```
gk prompt-info [--format=plain|segment|json]
```

### Flags

| Flag | Description |
|------|-------------|
| `--format` | `plain` (default), `segment`, or `json`. |

### Output

| Location | `--format=plain` | `--format=segment` | `--format=json` |
|----------|------------------|--------------------|-----------------|
| Outside a git repo | *(empty)* | *(empty)* | `{"linked":false}` |
| Inside any git repo (primary worktree) | *(empty)* | `<repo>/<branch>` | `{"linked":false,"repo":"<repo>","branch":"<branch>"}` |
| Inside a linked worktree, dir name == branch name | `wt` | `<repo>/<branch>` | `{"linked":true,"repo":"<repo>","name":"<basename>","path":"<full-path>","branch":"<branch>"}` |
| Inside a linked worktree, dir name != branch | `wt:<basename>` | `<repo>/<branch>` | (same as above) |
| Detached HEAD inside a repo | as above (marker if linked, else empty) | `<repo>` | `{"linked":...,"repo":"<repo>"}` |

Plain output deduplicates `wt:<name>` to bare `wt` when the worktree directory name equals the branch (gk's default `~/.gk/worktree/<repo>/<branch>` layout makes this the common case) — the branch name is already in the prompt next door, so repeating it would just be noise. The unabbreviated `wt:<name>` is kept for the rare divergent case where the suffix still carries information.

`<repo>` is derived from `git rev-parse --git-common-dir`: the parent directory's basename for regular repos (`<repo>/.git`) and the `.git`-stripped basename for bare repos (`<repo>.git`).

Exit status is always `0` unless `--format` is invalid — prompts can pipe the output unconditionally without risking a non-zero rendering glitch.

### Detection

`gk prompt-info` compares `git rev-parse --git-dir` against `git rev-parse --git-common-dir`. When they differ, the current directory is a linked worktree. This is much faster than enumerating worktrees, so it's safe to call from a prompt that re-renders on every keystroke (~30ms per call).

### Examples

```bash
# zsh — yellow ⎇ wt segment only inside linked worktrees
function gk_wt() {
  local info=$(gk prompt-info 2>/dev/null)
  [[ -n "$info" ]] && print -n " %F{yellow}⎇ $info%f"
}
PROMPT='... $(gk_wt) ... '

# Zero-overhead variant: refresh only on cd
typeset -g __gk_wt_cache
autoload -Uz add-zsh-hook
__gk_wt_refresh() { __gk_wt_cache=$(gk prompt-info 2>/dev/null); }
add-zsh-hook chpwd __gk_wt_refresh
__gk_wt_refresh
```

```toml
# starship — replace $directory + $git_branch with a single <repo>/<branch>
# label, keep a yellow ⎇ wt bubble for linked worktrees. Disable the
# built-in directory and git_branch segments first.
[directory]
disabled = true
[git_branch]
disabled = true

[custom.gk_context]
when = 'git rev-parse --git-dir > /dev/null 2>&1'
command = 'gk prompt-info --format=segment'
format = '[ $output ]($style)'
style = "fg:#FFFFFF bg:#6C5CE7"
shell = ["zsh", "--no-rcs"]

[custom.cwd_fallback]
when = '! git rev-parse --git-dir > /dev/null 2>&1'
command = 'basename "$PWD"'
format = '[ $output ]($style)'
style = "fg:#FFFFFF bg:#6C5CE7"
shell = ["zsh", "--no-rcs"]

[custom.gk_worktree]
command = 'gk prompt-info'
when = '[ -n "$(gk prompt-info 2>/dev/null)" ]'
format = '[ ⎇ $output ]($style)'
style = "fg:#1D3557 bg:#FFD166"
shell = ["zsh", "--no-rcs"]
```

```bash
# Scripting: extract branch or repo via jq
gk prompt-info --format=json | jq -r '.branch // empty'
gk prompt-info --format=json | jq -r '.repo // empty'
```

---

## gk resolve

Resolve the current conflicts — and finish the operation. After a full resolution, `gk resolve` drives `git <op> --continue` itself; in batch mode (`--strategy` / `--ai`) later picks that conflict again are re-resolved with the same strategy until the rebase/cherry-pick completes. One command takes a paused multi-pick rebase from conflict to done.

### Synopsis

```
gk resolve [files...] [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--strategy` | (interactive) | Apply one strategy to all conflicts: `ours`, `theirs`, `ai`, `safe` |
| `--ai` | false | Explicit opt-in shortcut for `--strategy ai` (AI decides per hunk: ours / theirs / merged, with a rationale) |
| `--safe` | false | Shortcut for `--strategy safe` — the deterministic tier ONLY: hunks with a provably safe answer are resolved (identical sides; trailing-whitespace/line-ending-only differences — internal spacing and indentation are meaningful and excluded; one side unchanged from base; union files like `CHANGELOG.md`/`go.sum` when both sides are additions — go.sum refuses conflicting hashes for the same module@version). Base info is reconstructed **in memory** from the index stages when the worktree markers carry none (git's default conflict style), so the base-aware rules work in every repo — the worktree is only trusted if it byte-matches a pristine re-merge, never overriding hand edits. Files mixing provable and semantic hunks are resolved **partially** (safe hunks fixed, the rest keeping markers, file left unmerged). No AI provider needed, nothing guessed |
| `--no-continue` | false | Stop after resolving; print the `gk continue` hint instead of running it |
| `--dry-run` | false | Show the resolution diff without modifying files (never continues) |
| `--no-backup` | false | Skip `.orig` backup file creation |
| `--no-ai` | false | Disable AI analysis in interactive mode |

### Examples

```bash
# Take the incoming side everywhere and finish the whole rebase
gk resolve --strategy theirs

# Preview what the AI would do first
gk resolve --ai --dry-run

# AI-assisted: per-hunk semantic resolution, then continue to completion
gk resolve --ai

# Old two-step flow
gk resolve --strategy theirs --no-continue
gk continue
```

### Verification gate (batch mode)

Batch resolutions (`--strategy` / `--ai` / `--safe`) are **written but not staged** until a verification gate passes: a conflict-marker scan always runs, plus any `resolve.verify` commands (shell commands run at the repo root — e.g. `["go build ./..."]`). The gate also runs on every later-pick round of a multi-pick rebase. **`resolve.verify` and `resolve.union_files` are honored from the GLOBAL config only** — the repo being resolved must not be able to run shell commands or widen the auto-merge surface (same trust boundary as `init.ai_gitignore`); a repo-local attempt is ignored with a note. On failure, everything gk changed is restored exactly (`git checkout -m`, possible because the unmerged index stages are never cleared before the gate) — including delete/modify resolutions, whose worktree removal is performed immediately (so verify sees the real result) but whose index deletion is deferred; files whose existing markerless content was merely accepted are never touched. The operation stays paused with `verify_failed` in the JSON report — an auto-resolution attempt costs nothing when it's wrong. AI side-picks (`ours`/`theirs`) are additionally applied **verbatim from the hunk**: a model that claims a side but returns edited text has its payload discarded — a corruption class no confidence score can catch.

The mechanical tier also runs as a **pre-pass inside `--ai`**, so deterministic hunks never reach the provider; and with `resolve.rerere` (default on) git's rerere is enabled and recorded resolutions are applied first — repeat conflicts resolve at zero cost (skipped for explicit `--strategy ours|theirs`, which promise a pure side-take).

### Confidence gate (`resolve.min_confidence`)

With a positive `resolve.min_confidence` (global config only), every AI hunk resolution carries a model-reported confidence, and hunks below the gate are **not applied**: the file is written partially resolved — confident hunks replaced, unsure hunks keeping their original markers — never staged, and reported in `remaining`. The withheld answers ride along as `proposals[]` (file, 1-based hunk index, strategy, confidence, rationale, resolved lines) in the paused JSON report, so an agent can review and apply them directly instead of asking a human to "resolve it". An unreported confidence counts as below a positive gate. See [`resolve.*` config](config.md#resolvererere).

### Notes

- The continue step runs only after a **full** resolution: every conflicted path cleared, a real operation in progress, no `--dry-run`/`--no-continue`. A file-filtered run (`gk resolve a.go`) that leaves other paths unmerged never continues. A `--safe` run with remaining semantic conflicts reports them in `remaining` and pauses.
- **Delete/modify and markerless conflicts** are handled from the index stages (`:1` base, `:2` ours, `:3` theirs), not from worktree text: `--strategy ours|theirs` restores the chosen side or deletes the file when that side removed it; explicit `--ai` sends both sides (with deletion flags) to the provider, which decides keep / delete / merge and prints its rationale. Binary conflicts stay manual (`ours`/`theirs` only). A markerless file whose both stages exist is accepted as a manual resolution and staged as-is.
- All git calls and file IO are anchored at the worktree root — resolve works from a repo subdirectory and with `--repo <path>` from outside the repo.
- A pick whose resolution becomes empty (its content already exists upstream — common with `--strategy ours`) is skipped via `git <op> --skip` instead of failing on the empty commit. Merges are never skipped — an empty merge commit still records the ancestry join.
- Interactive (TUI) mode also continues after the session, but a *later* pick that conflicts goes back to you instead of looping — you already chose to decide hunk by hunk.
- An `edit`/`break` rebase step pauses with a note; finish it and run `gk continue`.
- With `--json` / `GK_AGENT=1` the result is `{resolved, total, rounds, skipped_empty, done, state, resume}`.
- `*.orig` backups stay in the working tree and are never part of any commit.

---

## gk bisect

Find the first commit where a regression appears by binary search between a known-good and known-bad ref. Unlike raw `git bisect` — which is stateful and checks out each candidate in your working tree, an easy place for an agent to get lost — `gk bisect` runs the search in a throwaway detached worktree, so your tree and HEAD are never touched.

The command after `--` classifies each commit: exit 0 = good, non-zero = bad (exit 125 = skip, git's convention for "can't test this one").

### Synopsis

```
gk bisect --good <ref> --bad <ref> -- <command>   # automatic
gk bisect --good <ref> --bad <ref>                # manual: pause on each candidate
gk bisect good | bad | skip                       # advance a manual bisect
gk bisect reset                                    # end a manual bisect, remove its worktree
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--good <ref>` | — | A ref where the regression is absent (required) |
| `--bad <ref>` | `HEAD` | A ref where the regression is present |

### Modes

- **Automatic** (`-- <command>`): delegates to `git bisect run` in the worktree and returns the culprit in one call: `{culprit:{sha,subject,author,date}, good, bad, tested}`.
- **Manual** (no `--` command): starts a bisect and pauses with `state:"paused"` on the first candidate, reporting the worktree to test and `{current, remaining, resume}`. Test it, then `gk bisect good|bad|skip` to advance; the session persists in `<git-common-dir>/gk/bisect.json` across invocations until the culprit is found or `gk bisect reset` ends it. `gk context` shows an active bisect (a `bisect` field + next actions) and `gk watch` flags the worktree's state as `bisect`.

---

## gk continue

Continue the current rebase, merge, or cherry-pick after resolving conflicts. `gk resolve` runs this step automatically — reach for `gk continue` after resolving conflicts by hand (manual edits + `git add`).

### Synopsis

```
gk continue [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--yes` | false | Skip confirmation prompt and continue immediately |

### Examples

```bash
# Continue with interactive confirmation
gk continue

# Continue without prompting (useful in scripts)
gk continue --yes
```

### Notes

- Detects the in-progress operation (`rebase`, `merge`, `cherry-pick`, or `revert`) automatically.
- Never opens an editor: the prepared commit message is used as-is (a `GIT_EDITOR=true` guard on every git subprocess), so it cannot hang waiting for vim on a captured pipe.
- Only staged changes become part of the resolved commit. Unstaged edits and untracked files (e.g. `*.orig` backups from `gk resolve`) stay in the working tree — a NOTE warns when such leftovers exist.
- On success prints `✓ <op> complete`, or `still in progress` when the operation legitimately pauses again (an `edit`/`break` rebase step). With `--json` / `GK_AGENT=1` the result is `{action, done}`.

---

## gk abort

Abort the current rebase, merge, or cherry-pick and restore the previous state.

### Synopsis

```
gk abort [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--yes` | false | Skip confirmation prompt and abort immediately |

### Examples

```bash
# Abort with interactive confirmation
gk abort

# Abort without prompting
gk abort --yes
```

### Notes

- Detects and aborts `rebase`, `merge`, or `cherry-pick` automatically.
- Restores the working tree and branch to the state before the operation began.

---

## gk config

Read gk configuration.

### Synopsis

```
gk config <subcommand> [flags]
```

### Subcommands

| Subcommand | Description |
|-----------|-------------|
| `show` | Print the fully resolved configuration as YAML |
| `get <key>` | Print a single config value by dot-notation key |
| `init` | Scaffold the default `~/.config/gk/config.yaml` template (or a repo-local `.gk.yaml` via `--out`) |
| `set <key> <value>` | Not yet implemented |

---

### gk config show

Print the resolved configuration, merging all layers.

#### Synopsis

```
gk config show [flags]
```

#### Examples

```bash
# Show full resolved config
gk config show

# Output as JSON
gk config show --json
```

#### Sample output

```yaml
base_branch: ""
remote: origin
log:
  format: '%h %s %cr <%an>'
  graph: false
  limit: 20
ui:
  color: auto
  prefer: ""
branch:
  stale_days: 30
  protected:
    - main
    - master
    - develop
```

---

### gk config get

Print the value of a single configuration key.

#### Synopsis

```
gk config get <key> [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--source` | false | Also print where the value comes from (`local` / `global` / `default`) |

#### Examples

```bash
# Get the remote name
gk config get remote

# Show the value and which file supplies it.
gk config get ai.commit.model --source

# Get the log limit
gk config get log.limit

# Get the protected branch list
gk config get branch.protected
```

#### Notes

- Keys use dot notation matching the YAML structure (e.g., `log.format`, `branch.stale_days`).
- Returns exit code 4 if the key is unknown.

---

### gk config init

Write a fully-commented YAML template that documents every supported field (`ai`, `commit`, `log`, `status`, `branch`, `clone`, `worktree`, …). Without `--out`, the file lands at `$XDG_CONFIG_HOME/gk/config.yaml` (fallback `~/.config/gk/config.yaml`). Existing files are never overwritten unless `--force` is passed.

A silent auto-init runs on every `gk` invocation and creates the same file the first time it is missing. `gk config init` is the explicit, discoverable counterpart — useful for regenerating, writing to a custom path, or producing a repo-local override file.

`gk init config` is preserved as a backward-compatible alias and now delegates to this command.

#### Synopsis

```
gk config init [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | false | Overwrite an existing file |
| `--out <path>` | `$XDG_CONFIG_HOME/gk/config.yaml` | Write to a custom path (e.g. `.gk.yaml` for a repo-local override) |

#### Examples

```bash
# Regenerate the default global config (fails if the file already exists).
gk config init

# Overwrite the existing global config with a fresh template.
gk config init --force

# Seed a repo-local override file.
gk config init --out .gk.yaml

# Disable the first-run auto-init entirely (CI, sandboxes).
export GK_NO_AUTO_CONFIG=1
```

---

### gk config set

Set a single configuration value. The target file is edited in place — comments, key order, and blank lines are preserved — and created from the documented template (global) or a minimal header (`.gk.yaml`, with `--local`) when it does not exist yet. Keys absent from the schema are rejected. List keys (e.g. `log.vis`, `ai.commit.deny_paths`) cannot take a scalar via a plain `set`; use the `+=` / `-=` **key operators** to add or remove a single item in place (`gk config set log.vis+= merged`), or `gk config edit` for bulk changes. `+=` is idempotent (a duplicate is a no-op); `-=` on an absent item does nothing. The operator is a key suffix, not a value prefix, so the value never starts with `-`.

#### Synopsis

```
gk config set <key>[+=|-=] <value> [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--local` | false | Write to the repo-local `.gk.yaml` instead of the global config |

#### Examples

```bash
# Set the commit-only model in the global config.
gk config set ai.commit.model kiro/claude-haiku-4.5

# Set a repo-local override.
gk config set --local status.density compact

# Booleans and ints are written unquoted.
gk config set ai.commit.audit true

# Add or remove a single list item (comments + flow style preserved).
gk config set log.vis+= merged
gk config set log.vis-= base
```

---

### gk config unset

Remove a key, reverting it to its built-in default. A section left empty by the removal is dropped too (no dangling `status: {}`). Unsetting an absent key is a no-op.

#### Synopsis

```
gk config unset <key> [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--local` | false | Operate on the repo-local `.gk.yaml` instead of the global config |

#### Examples

```bash
gk config unset ai.commit.model
```

---

### gk config edit

Open the config file in your editor (creates it first if missing). Uses `$VISUAL`, then `$EDITOR`, falling back to `vim` / `vi` / `nano`.

#### Synopsis

```
gk config edit [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--local` | false | Edit the repo-local `.gk.yaml` instead of the global config |

---

### gk config path

Show which config files apply (global, repo-local) and whether each exists, in precedence order (later wins).

#### Synopsis

```
gk config path
```

---

### gk config setup

Interactive wizard for the most common settings — provider, commit model and language, output language, and easy mode — written after a single confirmation. Every answer can be pre-supplied as a flag, which both powers scripts/CI and lets non-interactive shells apply only the flags given. Choosing a provider name outside the built-in set (e.g. `kiro-api`) additionally prompts for an endpoint, model, and API key, stored under `ai.providers.<name>`.

#### Synopsis

```
gk config setup [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--provider <name>` | — | AI provider (`kiro-api`, `anthropic`, `openai`, `groq`, …) |
| `--endpoint <url>` | — | API endpoint URL (custom provider only) |
| `--provider-model <id>` | — | Model ID for a custom provider |
| `--api-key <key>` | — | API key for a custom provider (stored in config) |
| `--commit-model <id>` | — | Model used only for `gk commit` |
| `--commit-lang <code>` | — | Language for `gk commit` messages only |
| `--lang <code>` | — | Output language (`ko`, `en`) |
| `--easy` | false | Plain-language output for non-developers |
| `--yes` | false | Skip the final confirmation |
| `--local` | false | Write to the repo-local `.gk.yaml` instead of the global config |

#### Examples

```bash
# Interactive — prompts for each setting.
gk config setup

# Non-interactive: apply only the flags given.
gk config setup --provider anthropic --lang en --yes

# Custom OpenAI-compatible gateway.
gk config setup --provider kiro-api \
  --endpoint https://<gateway>/v1/chat/completions \
  --api-key sk-... --yes
```

#### Notes

- Built-in providers (`anthropic`, `openai`, `groq`, `nvidia`, `gemini`, `qwen`, `kiro`) skip the endpoint/key prompts — they have known endpoints and read their key from a fixed env var.
- `kiro-api` defaults both its provider model (`ai.providers.kiro-api.model`) and the commit model (`ai.commit.model`) to `kiro/claude-haiku-4.5`, shown as editable prompts rather than set silently.
- The API key is stored in the config file (an env var is the alternative) and masked in the confirmation summary.

---

### gk config doctor

Validate the config files rather than the git environment (that is `gk doctor`). Reports keys absent from the schema (typos a hand-edit can introduce — `set` blocks them, but direct editing does not) and a provider configured without its API-key environment variable.

#### Synopsis

```
gk config doctor
```

#### Examples

```bash
gk config doctor
```

---

## gk guard

Declarative repo policies. Rules live in `.gk.yaml` under the `policies:` block (v0.9 MVP ships the `secret_patterns` rule; more land incrementally).

### gk guard check

Run every registered policy rule and report violations.

#### Synopsis

```
gk guard check [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Emit NDJSON violations (one per line) for scripting |

#### Exit codes

| Code | Condition |
|:----:|-----------|
| 0 | no violations (or info-only) |
| 1 | at least one warning |
| 2 | at least one error |

#### Rules shipped in v0.9

| Name | Severity | Behavior |
|------|----------|----------|
| `secret_patterns` | `error` | Runs `gitleaks` (when present) and maps each finding to a Violation. When gitleaks is absent, emits a single `info` Violation so users see why the rule is a no-op. `gk doctor` detects the binary. |

#### Examples

```bash
# Human-readable table
gk guard check

# NDJSON for CI
gk guard check --json | jq 'select(.severity == "error")'
```

---

### gk guard init

Scaffold `.gk.yaml` in the repository root with a fully-commented `policies:` block. Uncomment and tune rules you want to enforce, then run `gk guard check`.

#### Synopsis

```
gk guard init [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | `false` | Overwrite existing `.gk.yaml` |
| `--out` | `<repo>/.gk.yaml` | Write to a custom path instead |

#### Rules scaffolded (all commented-out)

| Name | Description |
|------|-------------|
| `secret_patterns` | Gitleaks-backed secret scanning (full history or staged) |
| `max_commit_size` | Reject commits above a line / file count threshold |
| `required_trailers` | Enforce git trailers (Signed-off-by, Jira-Ticket, …) |
| `forbid_force_push_to` | Block force-pushes to protected branches (pre-push hook) |
| `require_signed` | Require GPG/SSH-signed commits |

The generated file also includes an allow-list comment block pointing to `.gk/allow.yaml` for per-finding suppressions with justification and expiry.

#### Examples

```bash
# Create .gk.yaml in the current repo
gk guard init

# Overwrite an existing file
gk guard init --force

# Preview by writing to a temp path
gk guard init --out /tmp/gk.yaml && cat /tmp/gk.yaml
```

---

## gk timemachine

Browse and restore historical repo states from a unified event stream (HEAD reflog + per-branch reflogs + gk backup refs). Every restore writes a fresh backup ref first, so the operation is reversible.

### gk timemachine list

List timeline events newest-first.

#### Synopsis

```
gk timemachine list [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--kinds <list>` | `reflog,backup` | Comma-separated source kinds: `reflog`, `backup`, `stash` (opt-in), `dangling` (opt-in, expensive) |
| `--limit N` | `50` | Max events (0 = unlimited) |
| `--all-branches` | `false` | Include reflogs from every local branch (default: HEAD only) |
| `--branch <name>` | `""` | Filter to a single branch (or `HEAD`); applies to reflog + backup events |
| `--since <duration>` | `0` | Filter to events at or after (now − duration). Go duration syntax (e.g. `24h`, `168h` for a week) |
| `--dangling-cap N` | `500` | Max dangling commits to surface when `--kinds` includes `dangling` (0 = unlimited; `git fsck` is O(objects)) |
| `--json` | `false` | Emit NDJSON (one entry per line) for scripting |

#### JSON schema

Each line is a single JSON object:

```json
{"kind":"reflog","ref":"HEAD@{1}","oid":"a1b2c3d…","old_oid":"e4f5a6…","when_unix":1700000000,"when_iso":"2023-11-14T22:13:20Z","subject":"reset: moving to HEAD~1","action":"reset"}
{"kind":"backup","ref":"refs/gk/undo-backup/main/1700000000","oid":"a1b2c3d…","when_unix":1700000000,"when_iso":"2023-11-14T22:13:20Z","subject":"undo-backup @ main","backup_kind":"undo","branch":"main"}
```

Fields `old_oid`, `action`, `backup_kind`, `branch` are omitted when empty.

#### Examples

```bash
# Default view (reflog + backups, HEAD only, last 50)
gk timemachine list

# All branches, NDJSON piped into jq
gk timemachine list --all-branches --json | jq 'select(.kind=="backup")'

# Only reflog entries, deeper history
gk timemachine list --kinds reflog --limit 200

# Include the stash stack too (opt-in)
gk timemachine list --kinds reflog,backup,stash

# Only events on the `feature` branch in the last 24 hours
gk timemachine list --branch feature --since 24h

# Week's worth of backups on main
gk timemachine list --branch main --kinds backup --since 168h
```

---

### gk timemachine list-backups

List gk-managed backup refs (`refs/gk/*-backup/`) newest-first. Each entry can be restored via `gk timemachine restore <ref>`.

#### Synopsis

```
gk timemachine list-backups [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Emit NDJSON |
| `--kind <name>` | `""` | Filter by kind: `undo`, `wipe`, `timemachine`, `forget`, `ai-commit` |

#### Examples

```bash
gk timemachine list-backups
gk timemachine list-backups --kind undo --json
```

---

### gk timemachine show

Resolve a SHA or ref and print commit details + diff.

#### Synopsis

```
gk timemachine show <sha|ref> [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--patch` | `false` | Show full diff instead of stat only |

When the ref is a gk-managed backup ref (`refs/gk/*-backup/...`), a `gk backup: kind=… branch=… when=…` descriptor line is prepended.

#### Examples

```bash
# Stat-only summary for HEAD
gk timemachine show HEAD

# Full patch for an older reflog entry
gk timemachine show HEAD@{3} --patch

# Inspect a backup ref produced by gk undo
gk timemachine show refs/gk/undo-backup/main/1700000000
```

---

### gk timemachine restore

Restore HEAD to the given SHA or ref. A backup ref is written at the current tip before any mutation, so every restore is reversible.

#### Synopsis

```
gk timemachine restore <sha|ref> [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--mode soft\|mixed\|hard\|auto` | `auto` | `git reset` mode. `auto` picks Mixed on clean trees, Keep on dirty trees without autostash, Hard+stash on dirty trees with `--autostash` |
| `--dry-run` | `false` | Print the plan and exit; do not touch the repo |
| `--autostash` | `false` | When the tree is dirty, stash before reset and pop after |
| `--force` | `false` | Allow hard reset on a dirty tree without autostash (discards uncommitted changes) |

#### Safety invariants

1. **Backup-before-restore.** A fresh ref is written at `refs/gk/timemachine-backup/<branch>/<unix>` before any HEAD motion.
2. **In-progress guard.** Restore refuses during rebase / merge / cherry-pick / revert / bisect. `--force` does **not** override this.
3. **Dirty-tree ordering.** With `--autostash`, the order is: backup → stash → reset → pop. If stash fails, the backup ref is rolled back and no reset is attempted.
4. **Recovery hints.** Every failure mode surfaces the exact `gk timemachine restore <backupRef>` command to revert.

#### Examples

```bash
# Restore HEAD to 3 steps back (mixed reset, safe for clean trees)
gk timemachine restore HEAD@{3}

# Hard restore to a tagged release with autostash (dirty tree OK)
gk timemachine restore v1.0.0 --mode hard --autostash

# Preview the plan without touching the repo
gk timemachine restore abc1234 --mode hard --dry-run
```

---

## gk guide

Step-by-step walkthrough of common git workflows for new users. Independent of Easy Mode — works in any output configuration.

### Synopsis

```
gk guide [<workflow>] [flags]
```

When `<workflow>` is omitted, `gk guide` prints the workflow menu. Passing the name skips the menu and starts that flow directly.

### Workflows

| Name | Description |
|------|-------------|
| `init` | First-time repo bootstrap and initial commit |
| `commit` | Stage and record changes |
| `push` | Publish a branch to a remote |
| `merge` | Bring a feature branch into main |
| `conflict` | Resolve a merge / rebase conflict |
| `undo` | Recover from a recent mistake (reflog / backup refs) |

The exact list is sourced from the in-binary `defaultWorkflows` table; run `gk guide` with no argument to see the current set.

### Examples

```
gk guide                # interactive menu
gk guide commit         # straight to the "first commit" walkthrough
gk guide undo           # recovery flow
```

### Notes

- Each step prints a title (bold), a short description, and an optional command (cyan) the user can run.
- Output goes to stdout; stderr is reserved for any provider warnings.
- The command is read-only — it never modifies the repository.

---

## gk init

One-shot project bootstrap. Analyzes the repository (language stack, frameworks, build tools, CI configs) and scaffolds the artifacts a new repo usually needs:

1. `.gitignore` — language/IDE/security baseline, optionally augmented by AI-suggested project-specific patterns when `--ai-gitignore` is passed (or [`init.ai_gitignore: true`](config.md#initai_gitignore) makes that the default). Languages are detected from marker files up to three directories deep — `go.mod`, `package.json`, `requirements.txt`/`pyproject.toml`/`setup.py`, `Cargo.toml`, `pom.xml`/`build.gradle`, `Gemfile`, `composer.json`, `Package.swift`, `pubspec.yaml`, `CMakeLists.txt` — each contributing its build-output patterns (Swift's `.build/`/`.swiftpm/`, Dart's `.dart_tool/`, CMake's `CMakeFiles/`, …).
2. `.gk.yaml` — repo-local gk configuration with a sensible default `ai.commit.deny_paths` block.
3. AI context files — `.kiro/steering/{product,tech,structure}.md` when `--kiro` is passed (`CLAUDE.md` / `AGENTS.md` are intentionally left to the assistants themselves).
4. `origin` remote — when the repo has none, init offers to wire it from a [`clone.hosts` account profile](config.md#clonehosts), a direct `owner/repo`, or a URL (see [Remote connection](#remote-connection)).

The default flow opens an interactive [huh](https://github.com/charmbracelet/huh) form that previews the analysis and the planned writes, then asks for confirmation. Non-TTY callers (CI, piped output) fall back to the plan-write path automatically; `--dry-run` previews the plan and exits without writing files or running `git init`.

### Synopsis

```
gk init [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | false | Overwrite existing files instead of merging or skipping. Never rewrites an existing remote |
| `--kiro` | false | Also scaffold `.kiro/steering/product.md`, `tech.md`, and `structure.md` for Kiro-compatible assistants |
| `--ai-gitignore` | false | After confirmation, ask the configured AI provider for extra `.gitignore` patterns. This sends bounded project metadata, so it is opt-in — set [`init.ai_gitignore: true`](config.md#initai_gitignore) in the **global** config to make it the default (repo-local `.gk.yaml` is ignored for this key, so an untrusted checkout can't enable the remote call; an explicit `--ai-gitignore[=false]` still wins). When a provider is available but the option is off, init prints a one-line hint after scaffolding |
| `--only <target>` | _(all)_ | Generate only one target. Accepts `gitignore`, `config`, `ai`, or `remote` |
| `--remote <spec>` | _(none)_ | Connect `origin` without prompting. Accepts a `clone.hosts` alias, `alias:repo`, `owner/repo`, or a full URL |
| `--name <project>` | directory name | Project name for the origin URL when `--remote` names a bare alias. Defaults to the sanitized directory basename |
| `--ssh` / `--https` | false | One-shot protocol override for the origin URL (mutually exclusive; wins over the profile and `clone.default_protocol`) |
| `--dry-run` (global) | false | Print the plan — including the resolved remote URL — without touching the filesystem or git config |

### Generated files

| File | Trigger | Purpose |
|------|---------|---------|
| `.gitignore` | always (unless `--only` filters) | Baseline rules; with `--ai-gitignore`, conservative AI-suggested project-specific patterns are merged after confirmation |
| `.gk.yaml` | always (unless `--only` filters) | Repo-local config with `ai.commit.deny_paths` baseline |
| `.kiro/steering/product.md` | `--kiro` | Product overview and goals |
| `.kiro/steering/tech.md` | `--kiro` | Tech stack, architecture decisions, coding standards |
| `.kiro/steering/structure.md` | `--kiro` | Repository layout and import rules |

`CLAUDE.md` and `AGENTS.md` are no longer scaffolded — Claude Code and Jules generate (and continually refresh) their own context files, so a static template would be stale before its first commit.

### Remote connection

When the repository has no `origin`, init adds one step before the final confirm. Register account profiles once in [`clone.hosts`](config.md#clonehosts) (an alias with an `owner` field) and the step becomes two keystrokes:

```
? connect origin to:
  ❯ personal   git@github.com:JINWOO-J/<name>.git
    work       https://github.com/42tape/<name>.git
    direct…    (owner/repo or URL)
    skip       (no remote)
? project name: [my-service▌]        ← sanitized directory name, Enter to accept

Remote:
  • origin → git@github.com:JINWOO-J/my-service.git (add)
? proceed with initialization? (y/N) ← the one confirm covers files and remote
```

Behaviour notes:

- **Protocol is never asked.** It resolves profile `protocol` → `clone.default_protocol` → `ssh`, and each picker row previews the final URL. `--ssh` / `--https` override for one run.
- **Picker rows follow your config's declaration order** (global file first, repo-local additions after), so the profile you list first is the default under the cursor.
- **Existing `origin` short-circuits the step** — the summary shows `origin → <url> (existing)` and nothing is touched, `--force` included. Re-running init is idempotent.
- With **no profiles registered**, the picker offers only `skip` (default) and `direct…`. After a direct entry is wired, init offers to save the account into the global `clone.hosts` so the next init is a pick.
- **Esc at any step skips the remote step** — cancellation is never an error, and declining the final confirm skips the remote add along with the file writes.
- After a successful add, init prints the `gh repo create <owner>/<name> --private --source . --push` follow-up for repos that do not exist on the host yet. Creating the remote repository stays out of scope.
- **Unknown aliases are errors here**, not passthroughs like `gk clone` — `git remote add` would record a typo verbatim and it would only surface at the first pull. Full `scheme://` and `user@host:` URLs still pass through untouched.
- Non-TTY / agent runs skip the step unless `--remote` is given; `--only=remote` without `--remote` in that mode returns `state:"blocked"` with the exact remedy. The JSON result carries `result.remote = {status, name, url, alias}` (`added` / `existing` / `skipped` / `dry-run` / `failed`).
- Not to be confused with `gk worktree init`, which bootstraps a fresh worktree's gitignored state (`.env`, dependencies) and has nothing to do with remotes.

### Examples

```bash
# Full bootstrap — open the TUI and confirm.
gk init

# Add Kiro steering documents.
gk init --kiro

# Only generate the gitignore (skip config + AI context).
gk init --only gitignore

# Ask the configured AI provider for extra ignore patterns after confirmation.
gk init --ai-gitignore

# CI / unattended use — preview, then force-write.
gk init --dry-run
gk init --force --only config

# Wire origin from a clone.hosts profile, project name = directory name.
gk init --remote personal

# Same, but name the project explicitly and force https for this run.
gk init --remote personal --name my-service --https

# Add only the remote to an existing repo (owner/repo or URL also work).
gk init --only remote --remote 42tape/service
```

### Backward compatibility

| Old form | New form | Status |
|----------|----------|--------|
| `gk init ai` | `gk init --kiro` | Available as a hidden alias for compatibility; the `CLAUDE.md`/`AGENTS.md` scaffolds are no longer emitted |
| `gk init config` | `gk config init` | Backward-compatible alias delegates to the canonical command |

---

## AI-powered commands

AI-assisted workflows. The `nvidia` and `groq` providers call their respective Chat Completions APIs directly over HTTP; other providers (`gemini`, `qwen`, `kiro-cli`) are driven as external CLI subprocesses. No API key lives inside gk — credentials are read from `NVIDIA_API_KEY`, `GROQ_API_KEY`, or each CLI's own auth path.

Provider resolution order (all commands):
1. `--provider` flag
2. `ai.provider` in config (`.gk.yaml` or `~/.config/gk/config.yaml`)
3. Auto-detect in order: `nvidia → groq → gemini → qwen → kiro-cli`

Optional capabilities exposed via type-asserted interfaces:
- **`Summarizer`** — pre-summarize oversized diffs before classification (currently `nvidia`, `groq`).
- **`GitignoreSuggester`** — suggest project-specific `.gitignore` patterns from filesystem context. Used only by `gk init --ai-gitignore`. Implemented for `nvidia`, `groq`, `gemini`, `qwen`, `kiro`.

### gk commit

Group working-tree changes (staged + unstaged + untracked) into semantic commit plans via an AI CLI and apply one Conventional Commit per plan. Interactive TUI review by default; `-f/--force` skips review, `--dry-run` previews only, `--abort` restores HEAD to the latest `refs/gk/ai-commit-backup/<branch>/<unix>` ref.

#### Synopsis

```
gk commit [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--force` | false | Apply commits without interactive review (secret gate still blocks) |
| `-i`, `--interactive` | false | Interactively group working-tree files into commits in a TUI (no AI): each round pick a file set + type its Conventional-Commit message, confirm an empty selection to finish. Builds the same commit plan as `--plan`, so validation, secret gate, and the backup-ref apply are identical; unselected files stay in the tree. Needs a TTY (use `--plan` in scripts) |
| `--dry-run` | false | Print the plan and exit without committing |
| `--provider <name>` | config | Override `ai.provider` (`nvidia` \| `groq` \| `gemini` \| `qwen` \| `kiro`) |
| `--lang <code>` | `en` | Override `ai.lang` (BCP-47 short code: `en`, `ko`, …) |
| `--staged-only` | false | Only consider already-staged changes |
| `--include-unstaged` | true | Include unstaged + untracked changes (mutually exclusive with `--staged-only`) |
| `--include-noise` | false | Include build output / dependency / cache files normally excluded (`node_modules`, `__pycache__`, `*.db`, …); skips the `.gitignore` guard |
| `-S`, `--allow-secret-kind <kind>` | none | Suppress secret findings of the given kind (repeatable); the special value `all` bypasses every finding |
| `-n`, `--no-verify` | false | Bypass the noise + secret guards **and** the privacy-gate abort threshold (implies `--skip-privacy`); findings are reported on stderr, then committed. Payload redaction to remote AI still applies |
| `--abort` | false | Restore HEAD to the latest ai-commit backup ref and exit |
| `--plan <file\|->` | — | Create commits from a JSON plan instead of the AI: `{"commits":[{"message","files":[...]}]}` — deterministic, no LLM call. See "Curated plan mode" below |
| `--plan-template` | false | Emit the current working-tree changes as a commit-plan draft (JSON) and exit |
| `--wip` | false | Write **one** checkpoint commit headed `WIP(scope): <summary>` instead of a semantic history. Skips classification (one commit by definition) so it costs a single provider call, and never fails on the AI — no provider, a timeout, or a bad response all degrade to `WIP(scope): checkpoint — N files (no AI summary)` rather than refusing to commit. Implies `--force` and `--no-wip-unwrap`; the secret scan and noise guard still apply. See "Checkpoint mode" below |
| `--no-wip-unwrap` | false | Skip detection/unwrap of WIP-like commits in the HEAD chain |
| `--force-wip` | false | Unwrap the WIP chain even when some commits are already pushed (rewrites pushed history; rerun `git push --force-with-lease` afterward) |
| `--ci` | false | CI mode — require `--force` or `--dry-run`, never prompt |
| `-y`, `--yes` | false | Accept every prompt (alias for `--force` when non-TTY) |

#### Checkpoint mode (`--wip`)

`--wip` is the cheap half of a two-step history. It saves the current state as one labelled checkpoint now; a later plain `gk commit` folds the whole chain into real Conventional Commits.

```bash
gk commit --wip     # WIP(remote): trace why a pane's cwd falls back to the spawn dir
gk commit --wip     # WIP(retrieval): log when a PWD action drops on a missing surface
# …later, once the work has a shape:
gk commit -f        # unwraps both and rewrites them as feat/fix commits
```

Why it exists: an unattended checkpoint (an agent Stop hook, a pre-switch save) has different constraints from a real commit. It must be cheap enough to run every time, and it must never lose work.

| | `gk wip` | `gk commit --wip` | `gk commit` |
|---|---|---|---|
| Message | fixed `--wip-- [skip ci]` | `WIP(scope): <AI summary>` | full Conventional Commit |
| Provider calls | 0 | 1 (compose only) | 2+ (classify + compose per group) |
| Commits written | 1 | 1 | N, grouped semantically |
| Secret scan | no (`--no-verify`) | yes | yes |
| On AI failure | n/a | commits with a plain message | aborts |

The header spelling is deliberate: `WIP(...)` matches the WIP-chain patterns `gk commit` unwraps, so the round trip closes without configuration. Because `--wip` implies `--no-wip-unwrap`, repeated checkpoints stack instead of rewriting each other.

#### Curated plan mode (`--plan` / `--plan-template`)

When the caller — typically an agent — decides the grouping instead of the AI, the plan contract creates N commits in one deterministic call (the `gk rebase --plan` philosophy applied to commit creation):

```bash
gk commit --plan-template            # emit dirty files as a JSON draft
# split the draft into groups, then:
gk commit --plan - <<'EOF'
{"schema":1,"commits":[
  {"message":"feat(auth): add OAuth flow","files":["internal/auth/oauth.go","internal/auth/oauth_test.go"]},
  {"message":"docs: oauth setup notes","files":["docs/oauth.md"]}
]}
EOF
```

- **File-level granularity**: each file appears in exactly one commit; splitting one file across commits (hunk-level) is not supported.
- **Validation up front**: duplicate files, files without a working-tree change, empty/malformed messages (Conventional Commit rules from `commit.*` config) are rejected before anything is committed — fix the plan, nothing happened. Files the plan does not cover stay dirty.
- **Same safety rails**: the secret scan runs on the plan's files; `--no-verify` / `--allow-secret-kind` behave as in the AI flow. A backup ref is written first (live runs only — `--dry-run` writes nothing, so it never retargets `--abort`), and the result contract (`{result, commits:[{message,files,result,sha}], failed_at?, backup_ref}`) reports per-commit outcomes — `partial` when a mid-plan commit fails.
- **`--abort` caveat**: abort restores HEAD to the backup ref with `git reset --hard` (the shared ai-commit abort contract) — the plan's commits leave the branch, and their files do **not** reappear as dirty working-tree changes (the commits remain recoverable via the backup ref / reflog).
- `allow_empty: true` on an entry creates an empty commit (`--allow-empty`) instead of failing on a no-change group — refused when the index has staged changes outside the plan, since a pathspec-less commit would swallow them.

#### Safety rails (every run)

| Gate | Source | Behaviour |
|------|--------|-----------|
| Secret scan | `internal/secrets` + `gitleaks` (when installed) | Abort on any finding; opt a kind out with `--allow-secret-kind <kind>`, or bypass everything with `--allow-secret-kind all` / `-n`/`--no-verify` (findings still reported, then committed) |
| Deny paths | `ai.commit.deny_paths` globs | Matching files (`.env*`, `*.pem`, `id_rsa*`, `credentials.json`, `*.kdbx`, lockfiles, `terraform.tfstate*`) never leave the process |
| Noise guard | built-in path patterns | Build output / deps / caches / local DBs (`node_modules/`, `__pycache__/`, `.venv/`, `*.pyc`, `*.db`, `.DS_Store`, …) are excluded from the AI scope; on a TTY gk offers to add them to `.gitignore`. Opt out with `--include-noise` |
| Git state | `gitstate.Detect` | Refuse to run mid-rebase / mid-merge / mid-cherry-pick |
| GPG sign | `commit.gpgsign` check | Abort if signing is on but no `user.signingkey` |
| Backup ref | `refs/gk/ai-commit-backup/<branch>/<unix>` | Written before the first commit; `--abort` restores HEAD |
| Conventional lint | `internal/commitlint.Parse/Lint` | Each message validated; up to 2 provider retries with feedback injected |
| Path-rule override | `_test.go`, `docs/*.md`, CI yamls, lockfiles | Always reclassified to `test`/`docs`/`ci`/`build` even if the provider picks otherwise |

#### Backup refs

Every live run writes `refs/gk/ai-commit-backup/<branch>/<unix>` **before** the first commit, pointing at the **pre-commit HEAD** (the state `--abort` rolls back to). Two things worth knowing:

- **To see what the run produced, diff the ref against the new tip:** `git diff <ref>..HEAD`. Diffing *backwards from* the ref (`git diff <ref>~2..<ref>`) instead shows pre-snapshot history — not the new commits — which is why it looks empty or wrong. (`gk timemachine show <ref>` shows the **rollback-target commit** itself — the pre-commit HEAD, i.e. its own diff against its parent — *not* the commits the run added, so it is not a substitute for `git diff <ref>..HEAD`.)
- **Recovery** is `gk commit --abort` (restores HEAD to the latest such ref) or, by hand, `git reset --hard <ref>`.

`--abort` itself is a hard reset — it discards the working tree, not just the commit. Since the run may have fully succeeded (the abort is just to redo the message or grouping), `--abort` first writes a second safety-net ref at the **pre-abort HEAD**, `refs/gk/ai-commit-abort-backup/<branch>/<unix>` — pruned the same way as the run-start backup — and prints its path plus the `git reset --hard` command to bring it back, so the just-aborted commit is always recoverable, not just when luck leaves it un-garbage-collected.

Retention is automatic and best-effort: on the next commit a backup ref is pruned only once it is **both** older than 30 days **and** beyond the 10 newest (the same conservative policy gk's other backup families use). Recent snapshots within 30 days are always kept, so a burst of commits can hold one ref per commit until they age out — they don't accumulate for the life of the clone, but they aren't hard-capped at 10 either. List them with `gk timemachine list`.

#### Examples

```bash
# Preview the plan without committing.
gk commit --dry-run

# Force-commit with gemini, English messages.
gk commit -f --provider gemini

# Include a specific secret kind you've decided to allow.
gk commit --allow-secret-kind generic-secret

# Bypass every guard (findings are reported, then committed — rotate any real credential).
gk commit --no-verify

# Recover from a mid-apply failure.
gk commit --abort

# CI mode — force the plan without prompting.
gk commit --ci --force
```

See the "AI commit" section in the main `README.md` for provider install/auth instructions (`gemini`, `qwen`, `kiro-cli`) and full config examples.

---

### gk pr

List open pull requests via the GitHub search API. (The AI PR-description
generator moved to `gk pr new` — see below.)

Auth is resolved from `GH_TOKEN` / `GITHUB_TOKEN` / a prior `gh auth login`
(read from `~/.config/gh/hosts.yml`; no `gh` binary is invoked). Without a
token only public results show, under a lower rate limit. When `gh` stored
its token in the OS keyring, expose it with `export GH_TOKEN=$(gh auth token)`.

#### Synopsis

```
gk pr [flags]        # open PRs in the current repo (owner/repo from origin)
gk pr --org [name]   # open PRs across a whole org/account, one search
gk pr --mine         # only PRs you opened
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--org [name]` | — | Search the whole org/account instead of the current repo. Owner priority: the value you pass > `github.owner` in config > origin's owner. `--org` and `--org=acme`/`--org acme` both work. |
| `--mine` | false | Restrict to items you authored (`author:@me`; needs a token) |
| `--review` | false | Only PRs awaiting your review (`review-requested:@me`; needs a token) |
| `--assigned` | false | Only items assigned to you (`assignee:@me`; needs a token) |
| `--author <user>` | — | Only items opened by `<user>` |
| `--assignee <user>` | — | Only items assigned to `<user>` |
| `--label <name>` | — | Filter by label (repeatable; `label:<name>`) |
| `-q, --query <raw>` | — | Extra raw GitHub search qualifiers, appended verbatim (e.g. `is:draft`) |
| `--sort <key>` | `updated` | Sort key: `updated`, `created`, or `comments` |
| `--limit <n>` | 0 | Cap the number of results (0 = no cap) |
| `--state <s>` | `open` | Which items: `open`, `closed`, or `all` |
| `--web` | false | Open the results in the browser as a github.com search instead of listing |
| `--pick` | false | Force the interactive picker (already the default in a terminal) |
| `--list` | false | Print the static list instead of opening the interactive picker |
| `--links` | false | Make the `PR#`/`issue#` token a clickable terminal hyperlink (OSC 8) to its URL; ignored on pipes/non-TTY. The URL is always in `--json`. |
| `--url` | false | Show the full item URL as a trailing column. A bare `https://` URL is auto-linked by virtually every terminal (including ones that don't support `--links`' OSC 8, e.g. Warp). |
| `--json` | false | Emit the machine-readable list (agent envelope under `GK_AGENT=1`) |

#### Interactive by default

In a terminal, `gk pr` opens the **table picker** rather than printing a static
list — the same convention as `gk switch`, `gk worktree`, and `gk clone`. Type to
filter; the typed filter survives an action and the picker re-opens on it.

| Key | Action |
|-----|--------|
| `enter` | Open the item in the browser |
| `c` | Check out the PR locally (`gk pr checkout`) |
| `y` | Copy the item URL |
| `o` | Open the **scope layer** — pick this repo, your own account, any org you belong to, or the inbox |
| `a` | Toggle state: open ↔ all |

Non-interactive runs are untouched: `--json`, `GK_AGENT=1`, a pipe, or CI all
print the static list (the gate is `promptAllowed()`). `--list` forces the static
list in a terminal; `--pick` forces the picker. An empty scope shows an inline
"press o to widen the scope" row instead of nothing.

#### What it does

One `/search/issues` call resolves the whole scope server-side — an org
query spans every repo without a per-repo loop. The search API has its own
rate bucket (30/min authenticated, 10/min anonymous), separate from the core
5000/hour, so interactive listing never trips a 429.

#### Examples

```bash
# Open PRs in the current repo
gk pr

# Every open PR across the org (owner from config or origin)
gk pr --org
gk pr --org acme          # a specific org/account

# Only the PRs you opened, closed ones included
gk pr --mine --state all

# Daily triage: what needs my review, what's assigned to me
gk pr --review
gk pr --assigned

# Filter by author / label / raw qualifier, sorted and capped
gk pr --org --author octocat --label bug --sort created --limit 20
gk issue -q 'is:draft'

# Open the results in the browser, or pick one to open
gk pr --org --web
gk pr --pick

# Machine-readable for an agent
GK_AGENT=1 gk pr --json
```

`gk issue` and `gk inbox` take the same query/output flags (`--label`, `-q`,
`--sort`, `--limit`, `--web`, `--pick`, `--links`, `--url`, `--json`); the
scope-relative filters (`--review`, `--assigned`, `--author`, `--assignee`) are
`gk pr`/`gk issue` only.

---

### gk pr checkout

Fetch a pull request's branch and switch to it locally.

```
gk pr checkout <number> [--branch <name>] [--remote <name>]
```

Fetches the PR head via GitHub's `refs/pull/<n>/head` — which exists for every
PR, **including forks** — into a local branch (default `pr/<n>`) and switches to
it. Uses **git only**: no GitHub API call and no token (git's own SSH/credential
auth applies), so it works on private repos without `GH_TOKEN`.

```bash
gk pr checkout 98                 # → local branch pr/98
gk pr checkout 98 --branch review # custom local branch name
```

---

### gk pr new

Generate a structured PR description from the current branch's commits
relative to the base branch. (This was `gk pr` before listing took that name.)

#### Synopsis

```
gk pr new [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--output <target>` | `stdout` | Where to send the result: `stdout` or `clipboard` |
| `--dry-run` | false | Print the prompt that would be sent without calling the provider |
| `--provider <name>` | config | Override `ai.provider` (`nvidia` \| `groq` \| `gemini` \| `qwen` \| `kiro`) |
| `--lang <code>` | `en` | Override `ai.lang` (BCP-47 short code: `en`, `ko`, …) |

#### What it does

1. Computes the diff range from `merge-base(HEAD, base_branch)..HEAD`.
2. Collects commit messages in that range.
3. Calls the provider's Summarize capability with Kind="pr".
4. Outputs a PR body containing: Summary, Changes, Risk Assessment, and Test Plan.

If the current branch has no commits ahead of the base branch, prints a message and exits with code 0.

#### Examples

```bash
# Generate PR description to stdout
gk pr new

# Copy to clipboard
gk pr new --output clipboard

# Preview the prompt without calling the provider
gk pr new --dry-run

# Use a specific provider and language
gk pr new --provider nvidia --lang ko
```

---

### gk issue

List open issues via the GitHub search API. Same scope flags as `gk pr`
(`--org [name]`, `--mine`, `--state`, `--json`); only the type differs.

```bash
gk issue                 # open issues in the current repo
gk issue --org acme      # across a whole org/account
gk issue --mine          # only issues you opened
```

---

### gk inbox

Everything on GitHub that involves you — open PRs and issues you authored,
are assigned, are requested to review, or are mentioned on — across every
repository, in a single `involves:@me` search. Requires a token to resolve
`@me`.

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pr` | false | Only pull requests |
| `--issue` | false | Only issues |
| `--state <s>` | `open` | Which items: `open`, `closed`, or `all` |
| `--json` | false | Machine-readable list |

```bash
gk inbox                 # all open PRs + issues involving you
gk inbox --pr            # just the PRs
GK_AGENT=1 gk inbox --json
```

---

### gk review

AI-powered code review on staged changes or a commit range.

#### Synopsis

```
gk review [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--range <ref1>..<ref2>` | | Review the diff between two refs instead of staged changes |
| `--base <branch>` | | Review the whole branch from its fork point (`merge-base <branch> HEAD`), so the base branch's own commits don't pollute the review |
| `--format <fmt>` | `text` | Output format: `text` or `json` |
| `--dry-run` | false | Print the prompt that would be sent without calling the provider |
| `--provider <name>` | config | Override `ai.provider` (`anthropic` \| `openai` \| `nvidia` \| `groq` \| `gemini` \| `qwen` \| `kiro`) |

#### What it does

Diff selection (first match wins): `--range` > `--base` (merge-base) > staged
(`git diff --cached`). It then calls the provider's Summarize capability with
Kind="review" and renders **actionable findings**: a verdict
(`approve`/`comment`/`changes_requested`), then each finding's severity
(`critical`/`high`/`medium`/`low`), location (`path:line`), what is wrong, why
it matters, and a concrete fix. `--format json` emits the same as structured
JSON; if the provider doesn't return the findings contract, the raw text is
shown instead.

If the diff is empty, prints a message indicating no changes to review and exits with code 0.

#### Examples

```bash
# Review staged changes
gk review

# Review a commit range
gk review --range main..HEAD

# JSON output for tooling
gk review --format json

# Preview the prompt
gk review --dry-run
```

---

### gk changelog

Generate a changelog from a range of commits, grouped by Conventional Commit type.

#### Synopsis

```
gk changelog [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--from <ref>` | latest tag | Start of the commit range (default: latest tag reachable from HEAD) |
| `--to <ref>` | `HEAD` | End of the commit range |
| `--format <fmt>` | `markdown` | Output format: `markdown` or `json` |
| `--dry-run` | false | Print the prompt that would be sent without calling the provider |
| `--provider <name>` | config | Override `ai.provider` (`nvidia` \| `groq` \| `gemini` \| `qwen` \| `kiro`) |

#### What it does

1. Collects commits in the `--from..--to` range.
2. Calls the provider's Summarize capability with Kind="changelog".
3. Outputs entries grouped by type: Features, Bug Fixes, Documentation, etc.

If no commits exist in the specified range, prints a message and exits with code 0.

#### Examples

```bash
# Changelog from latest tag to HEAD (markdown)
gk changelog

# Changelog between specific refs
gk changelog --from v1.0.0 --to v1.1.0

# JSON output
gk changelog --format json

# Preview the prompt
gk changelog --dry-run
```

---

## gk drivers

Maps file extensions to git's **built-in language diff drivers** in `.git/info/attributes` — the repo-local attributes file git consults in addition to any versioned `.gitattributes`. Nothing lands in the working tree, teammates are unaffected, and linked worktrees share the one file (it lives in the common git dir).

Why: git's hunk headers carry a function context (`@@ ... @@ def foo(...)`), and everything in gk that names *what* changed reads it — the live-feed symbols of `gk status --watch` and `gk watch`, `gk diff --digest`, and the conflict symbols of `gk context --include=conflict`. Without a driver mapping, git's generic heuristic can't read CSS selectors or indented Python methods; with it, those names resolve correctly (`~ cost.css · .credit-expiry +1 -1`).

Only built-in git drivers are referenced (`python`, `golang`, `rust`, `css`, `kotlin`, …) — no git config is written. The block is fenced with marker comments; `install` is idempotent, `uninstall` removes only the block (and deletes the file when gk's block was all it held). Inspired by weave's `setup --local`.

```
gk drivers install      # write the fenced rule block into .git/info/attributes
gk drivers status       # installed or not (+path)
gk drivers uninstall    # remove the block, restore the pre-install state
```

---

## gk hooks

Manage git hook shim scripts under `.git/hooks/`. Each shim calls a `gk` subcommand so the hooks stay thin and the logic lives in gk.

gk-managed hooks contain a marker comment. The installer refuses to overwrite a hook that lacks the marker unless `--force` is passed (which writes a timestamped `.bak` before overwriting).

### gk hooks install

Write one or more shim scripts into `.git/hooks/`.

#### Synopsis

```
gk hooks install [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pre-commit` | `false` | Install the `pre-commit` hook → `gk guard check` |
| `--commit-msg` | `false` | Install the `commit-msg` hook → `gk lint-commit` |
| `--pre-push` | `false` | Install the `pre-push` hook → `gk preflight` |
| `--all` | `false` | Install every hook gk knows about |
| `--force` | `false` | Overwrite foreign hooks (backs up first) |

#### Installed hooks

| Hook | Invokes | Purpose |
|------|---------|---------|
| `pre-commit` | `gk guard check` | Policy rules: secrets, size, trailers |
| `commit-msg` | `gk lint-commit --file "$1"` | Conventional Commits linting |
| `pre-push` | `gk preflight` | Configured preflight sequence |

#### Examples

```bash
# Wire up guard + commit-message linting
gk hooks install --pre-commit --commit-msg

# Full suite in one shot
gk hooks install --all

# Overwrite an existing foreign hook
gk hooks install --pre-commit --force
```

---

### gk hooks uninstall

Remove gk-managed hook shims. Refuses to remove hooks not written by gk.

#### Synopsis

```
gk hooks uninstall [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pre-commit` | `false` | Remove the `pre-commit` hook |
| `--commit-msg` | `false` | Remove the `commit-msg` hook |
| `--pre-push` | `false` | Remove the `pre-push` hook |
| `--all` | `false` | Remove every gk-managed hook |

#### Examples

```bash
gk hooks uninstall --all
gk hooks uninstall --pre-commit
```

---

## gk follow

Foreground watcher that polls a **remote** branch and, each time it advances,
hard-resets the local checkout to the remote tip (a GitOps mirror) and runs a
hook command once. It is "git-sync + watchexec" with zero infra — for dev
boxes, agent sandboxes, and single-container deploys where ArgoCD/CI is
overkill. Not to be confused with `gk status --watch`, which is a *local*
file-change feed.

### Synopsis

```
gk follow [branch] [-- <hook> args...] [flags]
```

If `branch` is omitted, `gk follow` uses the current branch. Detached HEAD
requires an explicit branch.

Each cycle: read the remote SHA with a cheap `git ls-remote` (no fetch unless it
moved); if it changed, run the safety-gated mirror (backup → fetch →
`reset --hard`) and then the hook. The hook runs synchronously, so runs never
overlap.

### Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--remote` | config remote, else `origin` | remote to watch |
| `--interval` | `30s` | poll interval — bare seconds (`30`) or a duration (`500ms`, `1m`) |
| `--run` | — | hook command run via `sh -c` on each change |
| `--discard-dirty` | `false` | allow the hard reset to discard uncommitted local changes (DESTRUCTIVE) |
| `--once` | `false` | run exactly one cycle (check, maybe update+hook) then exit |
| `--engine` | config `follow.engine`, else `auto` | change-detection engine: `ref` (git ls-remote) \| `events` (GitHub PR/issue) \| `auto` |
| `--on` | — | GitHub event trigger for the events engine (repeatable) |

Hook precedence: a trailing `-- <cmd> args...` wins over `--run`. With neither,
the cycle still mirrors (ref) / logs (events) but runs no hook.

### Engines: ref vs events

The default `ref` engine watches a **branch SHA** via `git ls-remote` — git-native,
needs no GitHub token (SSH/credential-helper authenticates), and always reads the
true current tip. It answers "where is the branch now?".

The `events` engine (opt in with `--on`) polls the repo's **GitHub Events API** —
PR/issue/review activity that never touches a branch SHA (opened, labeled,
reviewed, issue closed…). It answers "what happened?". It uses a conditional
**ETag** (a 304 is free), persists a cursor so a restart never re-fires past
events, and **baselines** on first run so it does not fire the hook on the
existing backlog.

`--on <trigger>` (repeatable, matches any):

```
pr:merged          pr:opened      pr:closed      pr:label=<name>   pr:review[=state]
issue:opened       issue:closed   issue:label=<name>              issue:comment
```

Default trigger (no `--on` in events mode) is `pr:merged`. A branch argument
filters `pr:merged` to that base and enables **mirror-on-merge**:

```bash
# deploy when a PR merges into main: mirror main, then run the hook
gk follow main --on pr:merged -- ./deploy.sh

# notify on any PR labeled "deploy" (no branch = event→hook, non-destructive)
gk follow --on pr:label=deploy --run './notify.sh'
```

The hook receives the event as environment variables: `GK_TRIGGER`,
`GK_EVENT_TYPE`, `GK_EVENT_ACTION`, `GK_ACTOR`, `GK_PR_NUMBER` / `GK_PR_TITLE` /
`GK_PR_BASE` / `GK_PR_HEAD` / `GK_PR_MERGED`, `GK_ISSUE_NUMBER` / `GK_ISSUE_TITLE`,
`GK_LABEL`, `GK_REVIEW_STATE`.

**`auto` engine + token fallback.** With no `--on`, `auto` is the ref engine (no
token needed). With `--on` and a token, it is the events engine. With `--on` and
**no token**, it falls back to the ref engine for merge-only triggers (watching
the branch for any advance — a loud warning notes the lost PR-level granularity),
and for API-only triggers (labels/issues/reviews) it uses the events engine but
warns it works on public repos only. Auth comes from `GH_TOKEN` / `GITHUB_TOKEN`
/ `gh auth login`.

### Safety

The update is deliberately destructive — the local checkout is treated as
disposable and reset to the remote tip — so it is fenced the same way every gk
verb that moves HEAD is:

- **Backup before reset.** A backup ref of the current HEAD is written *before*
  every reset (`refs/gk/follow-backup/<branch>/<unix>`). Recover the previous
  tip with `git reset --hard <backup-ref>` (or `gk timemachine`).
- **Dirty refusal.** If the working tree has uncommitted changes (tracked or
  untracked), the reset is refused and the cycle reports an error — pass
  `--discard-dirty` to mirror anyway.
- **No anchorless reset.** If HEAD cannot be resolved but the repo has history,
  the cycle aborts rather than resetting with no recovery anchor. A genuinely
  empty repo (no commits) mirrors without a backup.
- **Backoff.** A non-zero hook exit (or a cycle error) backs the poll off
  exponentially (`interval` .. `10×interval`) so a broken commit cannot thrash;
  a clean cycle resets it.
- **Graceful stop.** SIGINT/SIGTERM stop the loop cleanly (exit 0).

### Examples

```
# Mirror the current branch every 30s and re-run the test suite on each change
gk follow -- make test

# Watch a release branch on a deploy box, redeploy on each push
gk follow release --interval 15s -- ./deploy.sh

# One-shot reconcile (cron): check once, mirror+hook if changed, then exit
gk follow main --once -- make deploy

# Allow the mirror to discard local edits (true disposable box)
gk follow main --discard-dirty -- systemctl restart myapp
```

### Container

`gk follow` is designed to be supervised, not daemonized. The repo ships a
root `Dockerfile` that builds a minimal image (git + ssh + gk) with
`ENTRYPOINT ["gk", "follow"]`:

```
docker build -t gk-follow .
docker run --rm --restart=always \
  -v "$PWD:/repo" -v ~/.ssh:/root/.ssh:ro \
  gk-follow -- make deploy
```

The base image is intentionally minimal; a hook that needs a toolchain
(`make`, `node`, …) should `FROM` it and add what it needs.

---

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Invalid input / bad arguments |
| 3 | Conflict — manual resolution required |
| 4 | Configuration error |
| 5 | Network / remote error |
