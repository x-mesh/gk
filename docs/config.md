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

### `output.easy`

| | |
|-|-|
| Type | boolean |
| Default | `false` |
| Env var | `GK_EASY` (also auto-bound `GK_OUTPUT_EASY`) |
| CLI flag | `--easy` / `--no-easy` |

Master switch for Easy Mode. When enabled, gk translates a curated set of git terminology to the configured language wrapped with the English original (`commit` → `변경사항 저장 (commit)`), prefixes status sections with emoji, and appends contextual next-step hints. `--no-easy` always wins, then `--easy`, then config / env.

```yaml
output:
  easy: true
```

---

### `output.lang`

| | |
|-|-|
| Type | string (BCP-47) |
| Default | `ko` |
| Env var | `GK_LANG` (also auto-bound `GK_OUTPUT_LANG`) |
| CLI flag | none |

Message-catalog language. Two catalogs ship: `en`, `ko`. Unknown values fall back to English with a one-line warning on stderr. Distinct from `ai.lang`, which controls AI-generated commit messages.

```yaml
output:
  lang: ko
```

---

### `output.emoji`

| | |
|-|-|
| Type | boolean |
| Default | `true` |
| Env var | `GK_EMOJI` (also auto-bound `GK_OUTPUT_EMOJI`) |
| CLI flag | none |

Whether to prefix status sections, hints, and error labels with emoji (`📦` / `✏️` / `🆕` / `💥` / `💡` / `❌`). Independent of `output.easy` — set `false` to keep section headers plain even with Easy Mode on (e.g. for terminals with poor emoji rendering).

```yaml
output:
  emoji: false
```

---

### `output.hints`

| | |
|-|-|
| Type | string |
| Default | `verbose` |
| Env var | `GK_HINTS` (also auto-bound `GK_OUTPUT_HINTS`) |
| CLI flag | none |

Verbosity of contextual next-step hints (e.g. the trailing `💡 다음 단계: …` line on `gk status`).

| Value | Behavior |
|-------|---------|
| `verbose` | Full sentence with command — `💡 다음 단계: 변경사항을 저장하려면 → gk commit` |
| `minimal` | Command only — `gk commit` |
| `off` | Suppress hints entirely |

```yaml
output:
  hints: minimal
```

---

### `status.density`

| | |
|-|-|
| Type | string |
| Default | `normal` |
| CLI flag | `-v` / `--verbose` (escalates to `rich` for one call) |

Controls how much information `gk status` packs into the terminal.

| Value | Behavior |
|-------|---------|
| `normal` | Compact single-line summary — branch, working tree counts, next-step hint |
| `rich` | Branch / working tree / divergence / 7-day activity / next-action sections; surfaces SHA + commit age unconditionally |

```yaml
status:
  density: rich
```

---

### `status.layout`

| | |
|-|-|
| Type | string |
| Default | `bar` |
| CLI flag | none |

Selects how rich-mode sections are framed. Ignored when `status.density` is `normal`. Both layouts are independent of body width — wide-character content (한글, emoji, coloured glyphs) cannot misalign the chrome, which the legacy box layout it replaced did not guarantee.

| Value | Behavior |
|-------|---------|
| `bar` | Each section title is prefixed with a coloured `█` bar; body indented two spaces below |
| `rule` | Each section title sits between horizontal rules (`── TITLE ──────`) sized to `min(64, TTY width)` |

```yaml
status:
  density: rich
  layout: rule
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

### `worktree.base` / `worktree.project`

| | |
|-|-|
| Type | string |
| Default | `base: ~/.gk/worktree`, `project: ""` (repo toplevel basename) |

Controls where `gk worktree add <name>` places a relative name. The managed layout is `<base>/<project>/<name>` so worktrees live outside the main checkout. Set `project` explicitly when two clones share a basename. An absolute path passed to `add` always wins and bypasses this layout.

```yaml
worktree:
  base: ~/.gk/worktree
  project: ""        # defaults to the repo toplevel basename
```

### `worktree.init`

| | |
|-|-|
| Type | block with `link`, `copy`, `run` lists |
| Default | none (auto-detected from package manifests) |

Declares how a freshly created worktree is bootstrapped, so the gitignored, per-checkout state a new worktree lacks — secrets (`.env`), dependency trees (`node_modules`), virtualenvs (`.venv`) — is reconstituted instead of left empty. Applied by `gk worktree init` and by `gk worktree add --init`.

The three keys map to three different resource types — conflating them is the usual mistake:

| Key | Action | Use for | Never use for |
|-----|--------|---------|---------------|
| `link` | symlink each path from the main worktree | secrets / shared config (`.env`) managed in one place, kept in sync | virtualenvs, `node_modules` |
| `copy` | copy each path from the main worktree | per-worktree editable state (a `.env` whose port differs per checkout) | virtualenvs, `node_modules` |
| `run` | run each shell command **in** the new worktree, in order | `npm ci`, `uv sync`, `python -m venv` — anything regenerated against this checkout's lockfile | — |

A virtualenv bakes absolute paths into `pyvenv.cfg`/shebangs, and `node_modules` can carry a different branch's lockfile — both break when linked/copied, so put them in `run`. gk emits a warning if it sees `.venv`/`venv`/`node_modules` under `link`/`copy`.

All three are idempotent: re-running `gk worktree init` fixes only what's missing (existing correct symlinks and present copy targets are left alone, install commands are safe to repeat), so it doubles as a "retry the failed setup step" command.

When `worktree.init` is absent, `gk worktree init` detects the project's manifests (`package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `uv.lock`, `poetry.lock`, `requirements.txt`, `pyproject.toml`, `go.mod`, `Gemfile.lock`, `composer.lock`) and proposes a block you can persist with `--save`. A root manifest is authoritative (workspace assumption); when the root has none, gk scans subdirectories (bounded depth, skipping `node_modules`/`.venv`/build dirs) and wraps each nested project's command in `cd <dir> && …` — covering monorepos whose `frontend/`·`backend/` carry their own manifests.

```yaml
worktree:
  init:
    link:
      - .env
    copy:
      - .env.ports        # per-worktree editable copy
    run:
      - npm ci
      - uv venv && uv pip install -r requirements.txt
```

### `clone.hosts`

| | |
|-|-|
| Type | map of alias → `{host, protocol, owner, ssh_host}` |
| Default | empty |
| CLI flags | `--ssh` / `--https` (one-shot protocol override on `gk clone` and `gk init`) |

Alias table for multi-host and multi-account users, shared by `gk clone` (shorthand expansion) and `gk init` (origin remote wiring). Each entry can carry:

| Key | Meaning |
|-----|---------|
| `host` | Hostname inserted into the URL (falls back to `clone.default_host`) |
| `protocol` | `ssh` or `https` (falls back to `clone.default_protocol`, then `ssh`) |
| `owner` | Account/org name. Turns the alias into an **account profile**: `gk clone <alias>:repo` completes the owner, and `gk init` lists the alias in its remote picker |
| `ssh_host` | An `~/.ssh/config` `Host` alias (e.g. `github.com-work`) swapped into **ssh URLs only** — the standard trick for a second account with its own key on the same host. https URLs and path layouts keep the canonical `host` |

```yaml
clone:
  default_protocol: ssh
  default_host: github.com
  hosts:
    personal: { host: github.com, owner: JINWOO-J }
    work:     { host: github.com, owner: 42tape, protocol: https }
    corp:     { host: github.com, owner: acme, ssh_host: github.com-acme }
    gl:       { host: gitlab.com }   # host-only alias still works
```

Merging note: config layers combine **field by field** (deep merge), not entry by entry. A repo-local `.gk.yaml` that sets only `protocol` on the `work` alias inherits the global entry's `host` and `owner` — it does not redefine the alias from scratch. Aliases only present in one layer survive untouched.

Ordering: wherever gk presents aliases to pick from (e.g. `gk init`'s account picker), they appear in **declaration order** — the global file's order first, then repo-local additions. Put your most-used account at the top of `hosts:` and it sits under the cursor.

Aliases without `owner` behave exactly as before the field existed; `alias:repo` on such an alias fails with a "no owner configured" hint instead of guessing.

### `land.promote`

| | |
|-|-|
| Type | string |
| Default | `""` (promote step off) |
| CLI flag | `--to parent\|base` / `--promote` (override) · `--no-promote` (skip for one run) |

Turns the `gk land` promote step on by default, as if every run passed the flag: after the push, the current branch is forward-merged into a target and that target is pushed too (so `gk land` alone also publishes the base/parent). Recognised values:

| Value | Behavior |
|-------|----------|
| `""` / `false` / `0` / `none` / `off` | Promote only when `--to` / `--promote` is passed |
| `parent` / `true` / `1` | One hop to the branch's `gk-parent` (else the configured base) — same as `--to parent`. YAML booleans are tolerated so `promote: true` does the intuitive thing |
| *a branch name* (e.g. `main`) | Walk the parent chain hop by hop until that branch — same as `gk promote <branch>` then push (advances intermediate branches in a stack too) |

> **`base` is not a config keyword.** To target the base branch, write its **actual name** (`main`, `develop`, …). A literal `promote: base` is read as "a branch named `base`" and fails to resolve. The `--to base` *flag* accepts the word `base`; the `land.promote` config value does not — there is no single config value equivalent to `--to base`, so use the base branch's real name.

An explicit `--to` / `--promote` flag always wins over this; `--no-promote` skips the step for one run. Set it from the environment with `GK_LAND_PROMOTE` (same values) for CI/scripts without a config file.

```yaml
land:
  promote: main      # gk land also merges the current branch into main and pushes it
```

See [`gk land`](commands.md#gk-land) for the full flag semantics.

---

### `land.autostash`

| | |
|-|-|
| Type | bool |
| Default | `false` |
| CLI flag | `--autostash` / `--autostash=false` (override) |
| Env | `GK_LAND_AUTOSTASH` |

Makes the `gk land --to` / promote merge step stash a dirty **receiver** worktree — the parent checkout someone left mid-edit — around the merge and pop it after, as if every run passed `--autostash`, instead of refusing with `working tree has tracked changes`. Resolution is explicit-flag > config > default `false`: with it on, `--autostash=false` opts out for one run. Set it from the environment with `GK_LAND_AUTOSTASH=1`.

```yaml
land:
  autostash: true     # gk land --to never blocks on a dirty parent checkout
```

---

### `promote.autostash`

| | |
|-|-|
| Type | bool |
| Default | `false` |
| CLI flag | `--autostash` / `--autostash=false` (override) |
| Env | `GK_PROMOTE_AUTOSTASH` |

The `gk promote` counterpart of [`land.autostash`](#landautostash): stashes a dirty receiver worktree around each hop's merge and pops it after, for worktree flows where the parent checkout is routinely dirty. Resolution is explicit-flag > config > default `false`.

```yaml
promote:
  autostash: true     # gk promote never blocks on a dirty parent checkout
```

See [`gk promote`](commands.md#gk-promote) for the full flag semantics.

---

### `pull.autostash`

| | |
|-|-|
| Type | bool |
| Default | `true` |
| CLI flag | `--no-autostash` (off for one run) · `--autostash` (force on) |
| Env | `GK_PULL_AUTOSTASH` |

Controls whether `gk pull` auto-stashes a dirty (tracked) working tree around integration. **On by default**: pull stashes before integrating and pops after (with `--index`, so staged hunks stay staged), so the common no-conflict case flows through with a `stashed N / restored N` status line instead of an interactive prompt — and a non-TTY (CI) run no longer refuses. The pop is the one place a real conflict with local edits surfaces, and the one place pull then stops. Set `false` (or pass `--no-autostash`, or `GK_PULL_AUTOSTASH=0`) to restore the old gate: prompt for `stash & continue` on a TTY, refuse on a non-TTY.

```yaml
pull:
  autostash: false    # restore the pre-autostash interactive gate
```

---

### `ship.auto_confirm`

| | |
|-|-|
| Type | bool |
| Default | `false` |
| CLI flag | `-y` / `--yes` (override) · `--yes=false` (force the prompt for one run) |
| Env | `GK_SHIP_AUTO_CONFIRM` |

Skip the final confirmation gate by default, as if every `gk ship` passed `-y`. An explicit `--yes=false` still forces the prompt for a single invocation. Pairs with `ship.wait` to make `gk ship` fully non-interactive in CI.

```yaml
ship:
  auto_confirm: true
```

---

### `ship.wait`

| | |
|-|-|
| Type | bool |
| Default | `true` |
| CLI flag | `--wait` / `--wait=false` |
| Env | `GK_SHIP_WAIT` |

Run the post-tag CI-watch + artifact-verify pipeline (default `true`). `false` returns right after the push — the release is published but unwatched, and `gk ship` prints the skipped steps as a note. Use it when CI is slow or you verify out of band.

```yaml
ship:
  wait: false
```

See [`gk ship`](commands.md#gk-ship) for the full pipeline.

---

### `ai.assist`

| | |
|-|-|
| Type | object |
| Default | `mode: off`, `status: true`, `include_diff: false` |
| Env var | `GK_AI_ASSIST_MODE`, `GK_AI_ASSIST_STATUS` |
| CLI flag | `--ai` on `gk status` forces one run regardless of mode |

Controls AI help attached to existing commands. `gk next` and
`gk status --ai` always try the configured AI provider first and fall
back to a local next-step plan if no provider is available.

Valid `mode` values:

| Value | Behavior |
|-------|----------|
| `off` | No automatic AI help. Explicit commands still work. |
| `suggest` | Show a small hint pointing to `gk next`. |
| `auto` | Automatically run AI help for enabled surfaces such as `gk status`. |

```yaml
ai:
  assist:
    mode: suggest
    status: true
    include_diff: false
```

### `ai.nvidia.model`

| | |
|-|-|
| Type | string |
| Default | `meta/llama-3.1-8b-instruct` |

LLM model identifier sent in the Chat Completions request. Any model available on the NVIDIA API can be used.

```yaml
ai:
  nvidia:
    model: "meta/llama-3.1-8b-instruct"
```

---

### `ai.nvidia.endpoint`

| | |
|-|-|
| Type | string |
| Default | `https://integrate.api.nvidia.com/v1/chat/completions` |

Chat Completions API URL. Override when using a self-hosted or proxy endpoint.

```yaml
ai:
  nvidia:
    endpoint: "https://integrate.api.nvidia.com/v1/chat/completions"
```

---

### `ai.nvidia.timeout`

| | |
|-|-|
| Type | string (Go duration) |
| Default | `60s` |

HTTP request timeout as a Go duration string (e.g. `30s`, `2m`). This is the total deadline across all retries.

```yaml
ai:
  nvidia:
    timeout: "60s"
```

---

### `ai.<provider>.api_key`

| | |
|-|-|
| Type | string |
| Default | `""` (read the env var instead) |
| Applies to | `anthropic`, `openai`, `nvidia`, `groq` |

Supplies the bearer token in config instead of an environment variable. When non-empty it **takes precedence** over the provider's env var (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `NVIDIA_API_KEY`, `GROQ_API_KEY`); when empty, gk falls back to the env var as before.

```yaml
ai:
  provider: openai
  openai:
    endpoint: "https://api.openai.com/v1/chat/completions"
    model: "gpt-4o-mini"
    api_key: "sk-..."
```

> **Security:** a key written here lives in a plaintext YAML file that is easy to commit or share by accident. Prefer the environment variable for shared or version-controlled configs; reserve `api_key` for a private `~/.config/gk/config.yaml` or a custom OpenAI-compatible proxy that needs a non-standard token. `gk doctor --ai` reports the key as present (`ai.<provider>.api_key set`) without ever printing its value.

---

### `ai.providers.<name>`

| | |
|-|-|
| Type | map of objects |
| Default | `{}` |

Registers **custom, user-named providers**. Use this to point `provider:` at any name (e.g. an in-house gateway) instead of the built-in set (`anthropic`, `openai`, `nvidia`, `groq`, `gemini`, `qwen`, `kiro`).

When `provider:` names a value that is **not** in the built-in set, gk looks it up under `ai.providers.<name>` and builds it from the entry's `format` (the wire protocol it speaks). Built-in names keep working unchanged and do **not** need an `ai.providers` entry.

| Field | Type | Default | Notes |
|-|-|-|-|
| `format` | string | `openai` | Wire protocol adapter: `openai`, `anthropic`, `nvidia`, or `groq` |
| `endpoint` | string | adapter default | Chat Completions URL of your gateway |
| `model` | string | adapter default | Model identifier sent in the request |
| `timeout` | string (Go duration) | `60s` | Per-request HTTP timeout |
| `api_key` | string | `""` (env var) | Bearer token; precedence and security caveat match `ai.<provider>.api_key` above |

```yaml
ai:
  provider: my-gateway
  providers:
    my-gateway:
      format: openai   # omit to default to openai
      endpoint: "https://your-gateway.example.com/v1/chat/completions"
      model: "your-model"
      # api_key: "..."
```

> **Note:** gk builds the custom provider through the `format` adapter, so internal output that echoes the provider name (e.g. `gk doctor --ai`, `gk status --ai`) may show the `format` (`openai`) rather than the custom name (`my-gateway`). An unregistered custom name surfaces as an `unknown provider` error.

---

### `ai.commit.model`

| | |
|-|-|
| Type | string |
| Default | `""` (use `ai.<provider>.model`) |
| CLI flag | `--model` (on `gk commit`) |

Overrides the model for `gk commit` only. Commit-message generation is a mechanical task that a small, fast model handles well, so this lets a cheaper model run commits while the chat/advice commands (`gk do` / `ask` / `explain`, `gk status --ai`) keep the larger `ai.<provider>.model`. Empty falls back to the provider's configured model. Honoured by HTTP providers (`anthropic`, `openai`, `nvidia`, `groq`, and custom `providers`); CLI providers (`gemini`, `qwen`, `kiro`) own their own model selection and ignore it.

Resolution order (highest first): `--model` flag → `ai.commit.model` → `ai.<provider>.model` → adapter default.

```yaml
ai:
  provider: kiro-api
  kiro-api:
    format: openai
    endpoint: "https://your-gateway.example.com/v1/chat/completions"
    model: "kiro/auto"            # default for do/ask/explain/status --ai
  commit:
    model: "kiro/claude-haiku-4.5"  # cheaper/faster model for commits only
```

---

### `ai.commit.lang`

| | |
|-|-|
| Type | string |
| Default | `""` (follow `ai.lang`) |
| CLI flag | `--lang` (on `gk commit`) |

Language for `gk commit` messages only. Set it to keep commits in one language while the chat/advice commands (`gk do` / `ask` / `explain`) keep `ai.lang`. Empty falls back to `ai.lang`, which itself follows `output.lang`.

Resolution order (highest first): `gk commit --lang` → `ai.commit.lang` → `ai.lang` → `output.lang`.

```yaml
ai:
  lang: "ko"          # do/ask/explain answer in Korean
  commit:
    lang: "en"        # but commit messages are written in English
```

---

### `--model` (CLI flag)

A one-shot model override for a single run, available on `gk commit`, `gk do`, `gk ask`, `gk explain`, and `gk changelog`. Wins over both `ai.commit.model` and `ai.<provider>.model`. HTTP providers only. Use it to try a larger model for one tricky question without editing config:

```bash
gk ask "why does this rebase keep conflicting?" --model kiro/auto
gk commit --model kiro/claude-haiku-4.5
```

---

### `NVIDIA_API_KEY` (environment variable)

Required when using the nvidia provider. Set this to your NVIDIA API key:

```bash
export NVIDIA_API_KEY=nvapi-xxxxxxxxxxxxxxxxxxxx
```

The nvidia provider reads this variable at runtime and sends it as a Bearer token in the Authorization header. When unset, `gk` falls back to the next provider in the auto-detect order.

---

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
