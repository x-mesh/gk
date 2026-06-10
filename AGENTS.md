<!-- gk:agents:begin v5 — managed by `gk agents install`; edit outside this block -->
## Git workflow (gk)

This repository is driven with gk, an agent-native git CLI. Set `export GK_AGENT=1` once: every command then emits a uniform envelope — `{ok, result}` on success, `{ok:false, error:{code, message, remedies:[{command,safety}]}}` on failure — so you branch on fields, never parse prose. Prefer gk over raw git:

- **Orient first**: `gk context` — one call returns branch, upstream, ahead/behind, dirty counts, any in-progress rebase/merge (with resume/abort commands), base-branch drift, worktrees, and `next_actions`. Use it instead of probing with git status/branch/log.
- **Wrap up**: `gk land` — commit (AI-grouped), pull --with-base, push as one transaction with per-step results; on failure the result names `failed_step` and the resume command. `--cleanup` also reclaims fully-merged branches and their worktrees.
- **Sync**: `gk pull` (add `--with-base` to also fast-forward the local base branch, FF-only). On conflict the result lists the files plus the exact resume/abort commands.
- **Forecast before integrating**: `gk precheck [target]` — read-only merge-tree simulation (no target = the next pull). Clean → integrate; conflicts listed → pick a strategy first instead of try→abort.
- **Inspect changes**: `gk diff --digest` — per-file change kind, ±lines, hunk count, and the changed symbols, without the patch body. Same ref/path arguments as plain diff (`--staged`, `HEAD~3`, `main..feature`). Read the full patch only for the files the digest makes interesting.
- **Commit / push**: `gk commit -f` groups changes into conventional commits; `gk push` scans for secrets before pushing.
- **History editing**: never open `git rebase -i` (the editor session is unusable for you). Instead: `gk rebase --plan-template` emits the commit range as JSON (action/commit/subject/pushed), you decide each commit's fate (pick/squash/fixup/reword/drop), then `gk rebase --plan -` validates it (every commit addressed, pushed commits guarded) and drives git's own rebase with a backup ref.
- **Conflicts**: `gk resolve --ai`, then `gk continue` (abort with `gk abort`). A paused state is a result (exit 3), not an error.
- **Release**: `gk ship --dry-run` to read the full plan (version, changelog draft, pipeline steps); `gk ship -y` executes everything — preflight, version/CHANGELOG, tag, push, CI watch, artifact verify.
- **Stuck repo** (stale index.lock, orphan merge, prunable worktrees): `gk doctor --fix`.
- On any failure run the first entry of `error.remedies` (check `safety` first) instead of retrying variations.
<!-- gk:agents:end -->
