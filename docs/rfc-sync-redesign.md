# RFC: `gk sync` Redesign — From Upstream-FF to Base Catch-Up

| Field | Value |
|-------|-------|
| Status | Draft |
| Author | improve-ux |
| Date | 2026-04-29 |
| Target | v0.7 (post-rebase, pre-v1.0) |
| Affected commands | `sync`, `pull`, `merge` |

## Summary

Repurpose `gk sync` from "fetch + fast-forward to upstream (`@{u}`)" into "catch the current branch up to its base branch (e.g., `main`), via fast-forward when possible and rebase otherwise." This aligns the command's name with the user's most common intent ("get me current with where things are going") and gives the gk command surface three non-overlapping roles.

## Motivation

### Problem 1 — `sync` semantics don't match user intuition

Today, `gk sync`:
- fetches remotes
- fast-forwards local branches to their **upstream** (`origin/<self>`)
- refuses to rebase or merge — diverged → exit 4 → "use `gk pull`"

When a user says "sync this branch", they almost always mean "bring it up to date with `main`", not "fast-forward from `origin/<self>`". The latter is a narrow case (multi-machine push/pull on the same branch) that `gk pull` already covers.

### Problem 2 — There's no command for the most common intent

The dominant feature-branch workflow needs an operation that means *"my branch has fallen behind `main` while I worked; pull `main`'s new commits in"*. Today the user has to:

- `gk merge main` (creates a merge commit — undesirable for feature branches)
- `git rebase main` (drops out of gk)
- `gk pull --base main` (does **not** work — `pull` resolves `@{u}` first and ignores `--base` when `@{u}` exists)

There's no single, idiomatic gk command for the most common branch-update intent.

### Problem 3 — Wrong hint on diverge

When `gk sync` detects divergence, it points users to `gk pull`. But `gk pull` integrates with `@{u}` (e.g., `origin/<self>`), not with `main`. So the user often hits a second confusing failure before discovering they wanted `gk merge main` or `git rebase main`.

## Current state (commands and their scope)

| Command | Direction | Strategy | Notes |
|---|---|---|---|
| `gk sync` | `origin/<self>` → local self | FF only | Hard-fails on divergence |
| `gk pull` | `@{u}` → current branch | rebase / merge / ff-only | Falls back to `<remote>/<base>` only when `@{u}` is absent |
| `gk merge <target>` | `<target>` → current branch | merge commit (FF/no-FF/squash) | Includes precheck + AI plan |

Gap: no command for **"base branch → current branch via rebase"**.

## Proposed design

### New semantics for `gk sync`

> **`gk sync`** — bring the current branch up to date with its base.

```
gk sync                         # fetch base, integrate (rebase or FF)
gk sync --base main             # explicit base
gk sync --strategy merge        # base → current via merge commit
gk sync --strategy ff-only      # refuse divergence, FF only (legacy parity)
gk sync --autostash             # stash → integrate → pop
gk sync --no-fetch              # skip fetch, integrate from already-fetched ref
```

### Behaviour

1. Resolve base
   - `--base` flag, else `.gk.yaml` `base_branch`, else `client.DefaultBranch` (already used by `gk pull`).
2. Fetch base
   - `git fetch <remote> <base>` unless `--no-fetch`.
3. Self-FF first (free)
   - If `origin/<self>` exists and current is its ancestor, FF current to `origin/<self>` (preserves the only useful part of the old behaviour).
4. Integrate base into current
   - If current is ancestor of `<remote>/<base>` → `git merge --ff-only <remote>/<base>`.
   - Else → `git rebase <remote>/<base>` (default), or `git merge` (`--strategy merge`).
   - `--strategy ff-only` rejects divergence with a clear message and a hint toward `--strategy rebase`.
5. Conflict path
   - Reuse existing `gk continue` / `gk abort` / `gk resolve` plumbing. No new conflict surface.

### Strategy resolution (priority)

1. `--strategy` flag
2. `.gk.yaml` `sync.strategy`
3. Default: `rebase`

(Mirrors `gk pull`'s strategy resolution chain for muscle-memory consistency.)

### Three commands, three intents

After redesign:

| Command | Intent | Default strategy |
|---|---|---|
| `gk sync` | "Catch my branch up to its base." | rebase |
| `gk pull` | "Sync with the same branch on the remote." | rebase (config-driven) |
| `gk merge <x>` | "Deliberately integrate `<x>` here, with a merge commit." | merge |

No overlap. Each maps to a single mental model.

## Tradeoffs

**Pros**
- Command name matches user intent; the most common workflow (feature-branch catch-up) gets a one-word command.
- Eliminates the "diverge → use gk pull → still wrong" funnel.
- No new top-level command — same surface area, clearer semantics.

**Cons / risks**
- **BC break.** Anyone who scripted `gk sync` against its FF-only contract will see different behaviour. Mitigated by `--strategy ff-only` parity flag and one release of `--upstream-only` legacy alias (see migration RFC).
- **`--all` semantics.** Today `gk sync --all` FFs every local branch. Rebasing every local branch onto `main` is dangerous. Decision: drop `--all` from new sync, or keep it FF-only. Decided in (b).
- **The "origin/<self> ahead of local" case** (someone pushed to my branch from another machine) loses its dedicated command. Recovered by `gk pull` (which already does this) or by step 3 (self-FF) of the new sync.

## Alternatives considered

### A. Add a new `gk update` command, keep `sync` unchanged

Rejected. Doesn't fix the underlying confusion (sync still misleads), grows surface area, forces users to memorize which command means what.

### B. Add `--onto <base>` to `gk pull`

Rejected. Muddies `pull`'s "upstream sync" identity. Users would have to remember which flag changes the integration target. Long-term DX cost.

### C. Make `sync` take a positional arg: `gk sync` (=`@{u}`) vs `gk sync main` (=base)

Possible. Cleaner than (B) but requires teaching the dual-mode behaviour and the no-arg case still defaults to the less-common intent. Considered for compatibility hybrid — see (c).

## Migration plan (overview)

Detailed in [RFC: sync migration plan](./rfc-sync-migration.md) (= deliverable (c)).

- v0.7: ship new sync with `--strategy ff-only` and an `--upstream-only` flag that preserves the old behaviour byte-for-byte. Emit a one-line deprecation hint when `--upstream-only` is used.
- v0.8: keep `--upstream-only` working but mark it deprecated in `--help`.
- v0.9 / v1.0: remove `--upstream-only`. Old behaviour reachable only via `gk pull` (which already covers it).

## Out of scope

- Adding a low-level `gk rebase` command. Possible follow-up but not required for this redesign.
- Changing `gk pull`. Pull's contract (sync with `@{u}`) is sound; only its hint text on diverge gets updated to point at `gk sync` when appropriate.
- AI-assisted rebase plan (analogue of `gk merge`'s AI plan). Tracked separately.

## Decisions (resolved 2026-04-29)

1. **`gk sync --all` — drop.** Rebasing every local branch onto base is dangerous and rarely intended. Users who need it can script `for branch in ...; do gk sync; done`.
2. **Self-FF — always-on.** When `origin/<self>` is strictly ahead of local self, FF before integrating base. Safe (only when no divergence) and removes a footgun without surface cost.
3. **Config key — `sync.strategy` (new).** `pull.strategy` and `sync.strategy` are separate keys in `.gk.yaml`. Different intent (catch-up vs upstream-track) ⇒ different knob. Default: `rebase`.
4. **`--upstream-only` lifetime — one minor release (v0.7 only).** Ships in v0.7 with deprecation hint, removed in v0.8. Pre-v1 BC tolerance is high; longer windows add code without value.

## References

- `internal/cli/sync.go` — current implementation
- `internal/cli/pull.go` — strategy resolution and conflict plumbing to reuse
- `internal/cli/merge.go` — direction semantics reference
- `docs/commands.md` — `gk sync` is currently undocumented; new sync ships with full doc entry
