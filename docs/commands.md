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

No command-specific flags. All global flags apply.

### Examples

```bash
# Show working tree status
gk status

# Short alias
gk st

# JSON output
gk status --json
```

### Output format

Output groups files by state:

- **Staged** — changes added to the index
- **Unstaged** — tracked files with uncommitted modifications
- **Untracked** — new files not yet added
- **Conflicted** — files with merge/rebase conflicts

Also shows ahead/behind commit counts relative to the upstream branch.

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
| `-s, --stale <N>` | 0 (all) | Only show branches with last commit older than N days |

#### Examples

```bash
# List all local branches
gk branch list

# List branches not touched in 30+ days
gk branch list --stale 30

# List branches already merged into base
gk branch list --merged

# Combine filters
gk branch list --stale 14 --merged
```

---

### gk branch clean

Delete merged local branches, skipping protected branches.

#### Synopsis

```
gk branch clean [flags]
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | false | Show what would be deleted without deleting |
| `--force` | false | Use `git branch -D` (force delete) instead of `-d` |

#### Examples

```bash
# Preview which branches would be deleted
gk branch clean --dry-run

# Delete merged branches
gk branch clean

# Force-delete branches (even if not fully merged)
gk branch clean --force
```

#### Notes

- Protected branches (`main`, `master`, `develop` by default) are never deleted.
- Configure the protected list via `branch.protected` in your config file.
- The currently checked-out branch is always skipped.

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

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Invalid input / bad arguments |
| 3 | Conflict — manual resolution required |
| 4 | Configuration error |
| 5 | Network / remote error |
