---
name: release
description: Release workflow for gk — thin layer over `gk ship`. Ship plans (--dry-run --json), the skill polishes CHANGELOG prose and docs, one confirm gate, then `gk ship -y` runs preflight/commit/tag/push/CI-watch/verify. Use when the user says "release", "cut a release", "ship v0.x", or "/release".
---

# Release workflow for gk — thin layer over `gk ship`

The goal is a green GitHub Release page **AND** an updated `x-mesh/homebrew-tap/Casks/gk.rb`. The deterministic half of that — preflight checks, version inference, CHANGELOG promotion, release commit, tag, push, CI watch, artifact verification — is **all owned by `gk ship`** (configured in `.gk.yaml` under `preflight:` and `ship:`). This skill adds only what needs judgment: CHANGELOG prose, docs sync, the single confirm gate, and failure diagnosis.

## Operating principle

**Ship plans, you polish, one gate, ship executes.** Never reimplement what ship does — no manual tag/push/watch bash. If ship's plan looks wrong, fix the input (CHANGELOG, config, flags), not the pipeline.

Works from `develop` directly: ship fast-forwards `main` and releases from it (stops if diverged → run `gk sync` first).

## Phase 1 — PLAN (one command)

```bash
gk ship --dry-run --json        # add patch|minor|major / --version X.Y.Z if the user overrode
```

Uncommitted work? Commit it first (`gk commit -f` or ask the user) — ship requires a clean tree, and the release range must contain the work. Then re-run the plan.

Parse the JSON: `branch`, `base`, `merge_to_base`, `latest_tag` → `next_tag` (`bump`, `bump_downgraded_0x`), `commit_count`, `changelog` + `changelog_draft`, `preflight`/`watch`/`verify` step lists. If the user passed `patch|minor|major|X.Y.Z`, forward it to both this dry-run and the final run.

## Phase 2 — POLISH (the only AI work)

1. **CHANGELOG prose** — read `CHANGELOG.md` `[Unreleased]`:
   - Non-empty → light copyedit only if clearly broken; otherwise leave as-is.
   - Empty → take `changelog_draft` from the plan as raw material and rewrite it into the repo's established style (bold lead-in noun phrase, one paragraph each, Korean body, no marketing voice). **Write the result into the `[Unreleased]` section** — ship will promote exactly what you wrote. Mark uncertainty with `<!-- review: ... -->` rather than guessing.
2. **Docs sync** — for every `gk <cmd>` / `--<flag>` token in the new section, verify it appears in `README.md` and `docs/commands.md`. Draft gaps from `gk <cmd> --help` — transcribe, don't editorialize. Surface tokens with no `--help` backing instead of fabricating.
3. If you edited files, commit them (`docs:`/`chore:` commit), then re-run `gk ship --dry-run --json` so the plan reflects reality.

## Phase 3 — CONFIRM (single AskUserQuestion)

Show in markdown: `latest_tag → next_tag` (+bump reason, 0.x downgrade note if set), the final CHANGELOG section, docs edits made, and the pipeline step names (preflight / watch / verify from the plan). Then one AskUserQuestion: **진행 (Recommended)** / **수정** (loop back with the override) / **취소**.

## Phase 4 — EXECUTE (one command)

```bash
gk ship -y                      # forward any version override the user chose
```

Ship runs everything: preflight (lint, race tests, goreleaser check) → release commit → base fast-forward → tag → push → `ship.watch` (blocks on the GitHub Actions run) → `ship.verify` (tag on remote, tap cask version, CDN checksums). "Ship complete" means the tap is verified — there is no separate verify phase.

Stream the output to the user. Do not run `gh run watch` or clone the tap yourself — ship already did.

## Phase 5 — REPORT

```
✅ Released vX.Y.Z

  brew install x-mesh/tap/gk     # new
  brew upgrade x-mesh/tap/gk     # existing

Release: https://github.com/x-mesh/gk/releases/tag/vX.Y.Z
Cask:    https://github.com/x-mesh/homebrew-tap/blob/main/Casks/gk.rb
```

## Failure diagnosis (agent judgment — ship reports, you fix)

Ship surfaces the failing step name + command output verbatim. Match and remediate:

| Failure | Fix |
|---|---|
| preflight `lint`/`test` | Fix the code, commit, re-run `/release`. Never `--skip-preflight` to force a release |
| watch: `401 Bad credentials` (tap API) | `HOMEBREW_TAP_GITHUB_TOKEN` expired — regenerate PAT, `gh secret set`, then rerun the printed watch command |
| watch: `403 Resource not accessible` | PAT lacks `contents: write` on `x-mesh/homebrew-tap` — recreate fine-grained PAT |
| watch: `422 already_exists` (asset upload) | Stale partial release: `gh release delete vX.Y.Z -R x-mesh/gk --yes`, rerun the workflow (`gh run rerun --failed`). Do NOT delete the tag |
| verify: `tap-cask` | Workflow green but tap stale — inspect goreleaser logs; rerun the printed verify command after fixing |
| verify: `cdn-checksums` | CDN lag — retry the printed command; escalate if it persists past a few minutes |

The tag is already public once watch/verify run — remediation means rerunning the printed command or fixing credentials, **never re-shipping the same version**.

## Rollback (explicit user request only)

```bash
gh release delete "vX.Y.Z" -R x-mesh/gk --yes
git tag -d "vX.Y.Z" && git push origin ":refs/tags/vX.Y.Z"
# manually revert Casks/gk.rb in x-mesh/homebrew-tap if it landed
```

## Arguments

| Input | Effect |
|---|---|
| (none) | full flow, ship infers the bump |
| `patch` / `minor` / `major` / `X.Y.Z` | forwarded to ship (`gk ship patch -y` / `--version X.Y.Z`) |
| `--dry-run` | Phases 1–3 only, no execution |

## Hard boundaries

- Never force-push main; never `--skip-preflight` / `-n` to force a release; never commit to `x-mesh/homebrew-tap` directly; never delete a remote tag outside explicit rollback; never re-ship an already-pushed version.
- Use `git add <specific files>` for the polish commit — never `git add -A`.
