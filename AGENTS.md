<!-- gk:agents:begin v24 — managed by `gk agents install`; edit outside this block -->
## Git workflow (git-kit)

Use git-kit for git workflows whenever it has a path. In agent tool calls, run the full binary name with agent mode every time:
`GK_AGENT=1 git-kit ...`
`gk` can be shell-aliased, and environment variables do not persist across calls.

Minimum rules:
- Orient with `git-kit context` before git work; add `--include=diff,log,precheck,remotes,release` instead of separate status/log/diff probes.
- Prefer git-kit verbs over raw git: `commit`, `land`, `pull --with-base`, `sync`, `merge`, `rebase --plan`, `diff --digest`, `diff --raw-patch --json`, `find`, `worktree ...`, `ship`, `batch --plan -`.
- Keep read-only plumbing raw when needed: `git rev-parse`, `git config --get`, `git cat-file`, `git ls-files`.
- For commit + pull + push, use `git-kit land`; for local-only integration use `git-kit promote`; for releases inspect `git-kit ship --dry-run --json` before `git-kit ship -y`.
- Agent-mode output is `{state, ok, result, error}`. Branch on `state`, not prose: `ok`, `paused`, `blocked`, `error`; `ok` is only `state=="ok"`.
- A paused merge/rebase/conflict is not done. Use the resume/abort command in the result. When explicitly asked to resolve conflicts, use `git-kit resolve`; otherwise report the paused state and await direction.
- On failure, run the first `error.remedies[]` command after checking its `safety`; avoid retrying raw git variations.
<!-- gk:agents:end -->
