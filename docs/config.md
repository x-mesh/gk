# gk Configuration

gk resolves configuration by merging multiple sources. Higher layers override lower ones.

## Priority Order

| Priority | Source | Example |
|----------|--------|---------|
| 1 (lowest) | Built-in defaults | `remote: origin`, `log.limit: 20` |
| 2 | Global YAML file | `~/.config/gk/config.yaml` |
| 3 | Repo-local YAML file | `.gk.yaml` in the git working-tree root |
| 4 | git config entries | `git config gk.remote upstream` |
| 5 | Environment variables | `GK_REMOTE=upstream` |
| 6 (highest) | CLI flags | `--base develop` |

## Global Config File

Default location (XDG):

```
~/.config/gk/config.yaml
```

Override the directory with `XDG_CONFIG_HOME`:

```bash
export XDG_CONFIG_HOME=/custom/path
# gk reads /custom/path/gk/config.yaml
```

## Repo-Local Config File

Place a `.gk.yaml` file in the root of any git repository to apply project-specific settings:

```
my-project/
├── .gk.yaml       ← repo-local config
├── .git/
└── ...
```

Repo-local settings override the global file but are overridden by git config, environment variables, and CLI flags.

## git config Integration

Set individual keys via `git config`:

```bash
# Set for the current repo only
git config gk.remote upstream
git config gk.base_branch develop

# Set globally for all repos
git config --global gk.log.limit 50
git config --global gk.ui.color always
```

Key names use dot notation matching the YAML field paths.

## Environment Variables

All config fields can be set via environment variables. The naming convention is:

```
GK_<FIELD_PATH_UPPERCASED_WITH_UNDERSCORES>
```

Nested fields replace the `.` separator with `_`:

| Config field | Environment variable |
|-------------|---------------------|
| `base_branch` | `GK_BASE_BRANCH` |
| `remote` | `GK_REMOTE` |
| `log.format` | `GK_LOG_FORMAT` |
| `log.graph` | `GK_LOG_GRAPH` |
| `log.limit` | `GK_LOG_LIMIT` |
| `ui.color` | `GK_UI_COLOR` |
| `ui.prefer` | `GK_UI_PREFER` |
| `branch.stale_days` | `GK_BRANCH_STALE_DAYS` |
| `branch.protected` | `GK_BRANCH_PROTECTED` |

Example:

```bash
export GK_LOG_LIMIT=50
export GK_UI_COLOR=always
gk log
```

## Config Fields Reference

### `base_branch`

| | |
|-|-|
| Type | string |
| Default | `""` (auto-detect) |
| Env var | `GK_BASE_BRANCH` |
| CLI flag | `--base` (on `gk pull`) |

The base branch used by `gk pull` for fetch and rebase. When empty, gk auto-detects in this order: `origin/HEAD` → `develop` → `main` → `master`.

```yaml
base_branch: develop
```

---

### `remote`

| | |
|-|-|
| Type | string |
| Default | `origin` |
| Env var | `GK_REMOTE` |

The git remote used for fetch and rebase operations.

```yaml
remote: origin
```

---

### `log.format`

| | |
|-|-|
| Type | string |
| Default | `%h %s %cr <%an>` |
| Env var | `GK_LOG_FORMAT` |
| CLI flag | `--format` (on `gk log`) |

A git `--pretty=format` string. Supports all standard git format placeholders and color directives.

```yaml
log:
  format: '%C(yellow)%h%C(reset) %C(green)(%ar)%C(reset) %C(bold blue)<%an>%C(reset) %s%C(auto)%d%C(reset)'
```

See `git help log` for the full list of format placeholders.

---

### `log.graph`

| | |
|-|-|
| Type | boolean |
| Default | `false` |
| Env var | `GK_LOG_GRAPH` |
| CLI flag | `--graph` (on `gk log`) |

When true, includes the topology graph (equivalent to `git log --graph`).

```yaml
log:
  graph: false
```

---

### `log.limit`

| | |
|-|-|
| Type | integer |
| Default | `20` |
| Env var | `GK_LOG_LIMIT` |
| CLI flag | `-n` / `--limit` (on `gk log`) |

Maximum number of commits to show. Set to `0` for unlimited.

```yaml
log:
  limit: 20
```

---

### `ui.color`

| | |
|-|-|
| Type | string |
| Default | `auto` |
| Env var | `GK_UI_COLOR` |
| CLI flag | `--no-color` (disables) |

Controls color output. Valid values:

| Value | Behavior |
|-------|---------|
| `auto` | Color when output is a TTY; no color when piped |
| `always` | Always colorize output |
| `never` | Never colorize output |

`NO_COLOR` environment variable is always respected regardless of this setting.

```yaml
ui:
  color: auto
```

---

### `ui.prefer`

| | |
|-|-|
| Type | string |
| Default | `""` (prompt interactively) |
| Env var | `GK_UI_PREFER` |
| CLI flag | `--prefer` |

Default conflict resolution preference. Valid values:

| Value | Behavior |
|-------|---------|
| `""` | Always prompt interactively (when TTY is available) |
| `ours` | Automatically accept the current branch's version |
| `theirs` | Automatically accept the incoming version |

```yaml
ui:
  prefer: ""
```

---

### `branch.stale_days`

| | |
|-|-|
| Type | integer |
| Default | `30` |
| Env var | `GK_BRANCH_STALE_DAYS` |
| CLI flag | `-s` / `--stale` (on `gk branch list`) |

Number of days since the last commit before a branch is considered stale. Used by `gk branch list --stale`.

```yaml
branch:
  stale_days: 30
```

---

### `branch.protected`

| | |
|-|-|
| Type | list of strings |
| Default | `[main, master, develop]` |
| Env var | `GK_BRANCH_PROTECTED` |

Branches that `gk branch clean` will never delete. The currently checked-out branch is also always protected, regardless of this list.

```yaml
branch:
  protected:
    - main
    - master
    - develop
```

## Inspecting the Resolved Config

To see the final merged configuration that gk will use:

```bash
gk config show
```

To check a specific value:

```bash
gk config get log.limit
gk config get branch.protected
```
