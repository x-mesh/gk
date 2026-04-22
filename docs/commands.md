# gk Command Reference

All subcommands accept the following global flags:

| Flag | Description |
|------|-------------|
| `--dry-run` | Print actions without executing |
| `--json` | JSON output where supported |
| `--no-color` | Disable color output |
| `--repo <path>` | Path to git repo (default: current directory) |
| `--verbose` | Verbose output |

---

## gk pull

Fetch and rebase the current branch onto the base branch.

### Synopsis

```
gk pull [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--base <branch>` | auto-detect | Base branch to rebase onto |
| `--autostash` | false | Stash dirty changes before rebase, pop after |
| `--no-rebase` | false | Only fetch, do not rebase |

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

# Stash uncommitted changes, rebase, then restore
gk pull --autostash

# Preview what would happen without executing
gk pull --dry-run
```

### Notes

- Requires a clean working tree unless `--autostash` is set. If the tree is dirty and `--autostash` is not set, gk exits with an error and prints guidance.
- Runs `git fetch <remote> <base>` then `git rebase origin/<base>`.
- On conflict, gk pauses and prompts. Use `gk continue` or `gk abort` to resume.

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
| `--safety` | false | Mark notable push-state: `◇` unpushed · `✎` recently amended · blank for the normal "already pushed" case so the column stays quiet until something deserves attention |
| `--hotspots` | false | Mark commits that touch the repo's top-10 most-churned files |
| `--trailers` | false | Append a `[+Alice review:Bob]` roll-up from commit trailers |
| `--lanes` | false | Replace the commit list with per-author swim-lanes on a time axis |
| `--vis <list>` | `cc,safety,tags-rule` (from `log.vis`) | Visualization set (comma-list or repeated). Any explicit viz flag (`--vis` or an individual flag like `--cc`) overrides the configured default. Pass `--vis none` to disable all layers; setting `--format` alone also suppresses the default. |

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
| `--vis <list>` | `gauge,bar,progress,tree,staleness` (from `status.vis`) | Visualization layers (comma-list or repeated). Pass `--vis none` to disable all layers for a single invocation. Values: `gauge`, `bar`, `progress`, `types`, `staleness`, `tree`, `conflict`, `churn`, `risk`, `base`, `since-push`, `stash`, `heatmap`, `glyphs`. |
| `--no-fetch` | false | Skip the quiet upstream fetch that keeps ↑N ↓N counts current. Also honored via `GK_NO_FETCH=1` or `status.auto_fetch: false`. |
| `--top N` | 0 (unlimited) | Limit the entry list to the first N paths (alphabetically sorted for stable output); a `… +K more (total · showing top N)` footer surfaces the hidden remainder so the truncation is never silent. Composes with every viz layer. |

### Upstream auto-fetch

By default, `gk status` attempts a short fetch of the current branch's upstream ref (the one recorded in `branch.<name>.remote` / `branch.<name>.merge`) before reading porcelain output, so the ↑N ↓N counts reflect the actual remote state rather than the last-cached view. The fetch is intentionally scoped and safe:

- Only the single upstream ref is fetched — no `--all`, no `--tags`, no submodule recursion, no FETCH_HEAD write.
- A 3-second hard timeout means a slow or flaky remote never blocks status beyond that budget.
- `GIT_TERMINAL_PROMPT=0` + empty `SSH_ASKPASS` prevent credential prompts from hijacking the terminal.
- stderr is discarded so `remote: …` chatter does not interleave with status output.
- On any failure (offline, auth expired, timeout) the fetch is silently dropped and status renders with the local cached view.

Disable globally with `status.auto_fetch: false`, per-invocation with `--no-fetch`, or via `GK_NO_FETCH=1`. When upstream is not configured (detached HEAD or brand-new branch) the fetch is skipped without network activity.

#### `--vis` values

| Value | Effect |
|-------|--------|
| `gauge` | Replaces `↑N ↓N` with a divergence gauge `[▓▓│····]` (ahead on the left, behind on the right, upstream marker in the middle). |
| `bar` | Stacked `[▓████▒▒░░░]` bar whose segments are proportional to conflicts/staged/modified/untracked counts. |
| `progress` | `clean: [███░░░░░░░] 30%  stage 5 · commit 3 · resolve 1 · discard-or-track 1` — staged ratio + remaining-verb list. |
| `types` | Extension histogram (`.ts×6 .md×2 .lock×1`). Collapses known lockfile basenames to `.lock`; dims binary/lockfile kinds. Suppressed above 40 distinct kinds. |
| `staleness` | Annotates the branch line with `· last commit Xd ago` and untracked entries older than a day with `(14d old)`. |
| `tree` | Replaces the flat sections with a hierarchical path trie. Single-child directory chains collapse; directory rows carry a subtree-count badge `(N)`. |
| `conflict` | Appends `[N hunks · both modified]` to each conflicts entry. Hunk count is derived from `<<<<<<<` markers in the worktree file. |
| `churn` | Appends an 8-cell sparkline to each modified entry (per-commit add+del totals over the file's last 8 commits). Suppressed when the dirty tree has more than 50 files. |
| `risk` | Flags high-risk modified entries with `⚠` and re-sorts the section so the hottest files are on top. Score is `diff LOC + distinct-authors-over-30d × 10`, threshold 50. |
| `base` | Appends a second `  from <trunk> [gauge]` line on feature branches showing how far the current branch has diverged from the repo's mainline. Base resolves from `base_branch` config → `refs/remotes/<remote>/HEAD` → `main`/`master`/`develop`. Suppressed when the current branch *is* the base. Costs one `git rev-list --left-right --count` call (~5–15 ms). |
| `since-push` | Appends `· since push Xh (Nc)` to the branch line when there are unpushed commits, showing the age of the oldest one and the total unpushed count. Suppressed on up-to-date branches and when no upstream is configured. Cost: one `git rev-list @{u}..HEAD --format=%ct` call (~5 ms). |
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

- Protected branches (`main`, `master`, `develop` by default) are never deleted.
- Configure the protected list via `branch.protected` in your config file.
- The currently checked-out branch is always skipped.
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

---

## gk switch

Switch to another branch. When no name is given, opens an interactive fzf-backed picker that lists both local branches and remote-only tracking branches — picking a remote-only entry creates a local tracking branch automatically (equivalent to `git switch --track <remote>/<branch>`).

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
gk reset [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--to <ref>` | upstream | Override target ref (e.g. `origin/main`); default uses the configured upstream |
| `--to-remote` | false | Reset to `<remote>/<current-branch>` regardless of configured upstream |
| `--remote <name>` | config.remote / `origin` | Remote to fetch from |
| `-y, --yes` | false | Skip confirmation prompt (required for non-TTY automation) |
| `--clean` | false | Also run `git clean -fd` to remove untracked files |
| `--dry-run` | false | Print what would happen without fetching or resetting |

`--to` and `--to-remote` are mutually exclusive.

### Examples

```bash
# Reset to the branch's tracked upstream (prompts for confirmation)
gk reset

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
| `list` | List worktrees (table or `--json`) |
| `add <path> [branch]` | Create a worktree at `<path>` checking out `[branch]` (or HEAD) |
| `remove <path>` | Remove a worktree |
| `prune` | Prune worktree administrative records |

### gk worktree add

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-b, --new` | false | Create a new branch named `[branch]` at `--from` |
| `--from <ref>` | HEAD | Base ref for the new branch |
| `--detach` | false | Detach HEAD in the worktree instead of tracking a branch |

### gk worktree remove

| Flag | Default | Description |
|------|---------|-------------|
| `-f, --force` | false | Force remove even when the worktree is dirty or locked |

### Examples

```bash
# JSON list for scripts
gk worktree list --json

# Add a worktree that tracks an existing branch
gk worktree add ../gk-feat feat/login

# Create a new branch in a new worktree off HEAD
gk worktree add -b ../gk-review feat/review

# Create a new branch off a specific base
gk worktree add -b ../gk-hotfix hotfix/1.2.3 --from origin/main

# Remove cleanly
gk worktree remove ../gk-feat
```

---

## gk continue

Continue the current rebase, merge, or cherry-pick after resolving conflicts.

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

- Detects the in-progress operation (`rebase`, `merge`, or `cherry-pick`) automatically.
- In a non-TTY environment without `--yes`, gk aborts safely instead of hanging.
- Exits with code 3 if there is a conflict that must be resolved first.

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

#### Examples

```bash
# Get the remote name
gk config get remote

# Get the log limit
gk config get log.limit

# Get the protected branch list
gk config get branch.protected
```

#### Notes

- Keys use dot notation matching the YAML structure (e.g., `log.format`, `branch.stale_days`).
- Returns exit code 4 if the key is unknown.

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
| `--kind <name>` | `""` | Filter by kind: `undo`, `wipe`, `timemachine` |

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

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Invalid input / bad arguments |
| 3 | Conflict — manual resolution required |
| 4 | Configuration error |
| 5 | Network / remote error |
