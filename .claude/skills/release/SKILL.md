---
name: release
description: Release workflow for gk — auto-infers version bump and CHANGELOG from working tree, single-confirm gate, then commits/tags/pushes/watches and verifies the Homebrew tap. Use when the user says "release", "cut a release", "ship v0.x", or "/release".
---

# Release workflow for gk

The goal is a green GitHub Release page **AND** an updated `x-mesh/homebrew-tap/Formula/gk.rb`. Anything short of that is incomplete.

## Operating principle

**Defaults-first, single-gate.** Auto-infer every decision (version bump, CHANGELOG body, commit structure), present them in one summary, ask once. Don't fan out into 4 separate `AskUserQuestion` calls. Don't create a `TaskList` — the flow is linear.

When in doubt: pick the safe default (e.g. include uncommitted work, treat surface removal as breaking) and let the user override at the single gate.

## Phase 1 — PREFLIGHT (one bash call)

Collect everything needed for inference in a single shell run. Abort with one-line summary on any prereq failure.

```bash
set -e
git fetch origin main --quiet 2>/dev/null
echo "=== prereqs ==="
[ -z "$(git status --porcelain | grep -v '^??')" ] && echo "tree=clean" || echo "tree=dirty"
echo "branch=$(git branch --show-current)"
[ "$(git rev-parse main 2>/dev/null)" = "$(git rev-parse origin/main 2>/dev/null)" ] && echo "sync=ok" || echo "sync=behind"
gh auth status >/dev/null 2>&1 && echo "gh=ok" || echo "gh=fail"
command -v golangci-lint >/dev/null && echo "lint=ok" || echo "lint=missing"
gh secret list -R x-mesh/gk 2>/dev/null | grep -q HOMEBREW_TAP_GITHUB_TOKEN && echo "secret=ok" || echo "secret=missing"

echo "=== context ==="
echo "last_tag=$(git tag --list 'v*' --sort=-v:refname | head -1)"
echo "today=$(date +%Y-%m-%d)"
echo "=== diff stat ==="
git diff --shortstat
git diff --cached --shortstat
echo "=== unreleased section ==="
awk '/^## \[Unreleased\]/{flag=1;next}/^## \[/{flag=0}flag' CHANGELOG.md | sed '/^$/d' | head -20
```

Hard requirements: `branch=main`, `sync=ok`, `gh=ok`, `secret=ok`, `lint=ok`. Abort if any fails.

`tree=dirty` is **not** a failure — just a fact for inference (Phase 2).

## Phase 2 — PROPOSE (auto-infer, no questions)

Decide three things from the Phase 1 output. **Do not call AskUserQuestion for these — pick defaults.** The user can override at Phase 3.

### Version bump

| Signal | Bump |
|---|---|
| `[Unreleased]` has `Removed`, `(breaking)`, `!:` commits, or removed public surface (deleted exported symbol, deleted command/flag) | **minor** in 0.x, **major** in 1.x+ |
| `[Unreleased]` has `Added` or `Changed` (new feature, behavior change) | **minor** |
| Only `Fixed` / `Docs` / `Internal` | **patch** |
| `[Unreleased]` empty AND working tree has code changes | infer from diff: new file with new symbols → minor; bug fix → patch; surface removal → minor (0.x) |
| `[Unreleased]` empty AND tree clean | abort: nothing to release |

If the user passed `/release patch|minor|major|X.Y.Z`, that overrides the inference. Otherwise compute `NEW_VERSION` from `LAST_TAG` + bump.

### CHANGELOG body

- If `[Unreleased]` has entries → promote as-is.
- If empty + uncommitted changes → draft entries from `git diff` + commit messages, matching existing style (bold lead-in noun phrase, one paragraph each, no marketing voice). Cite the renamed/added/removed surface concretely.
- Mark uncertainty with `<!-- review: ... -->` rather than guessing.

### Commit structure

- **Tree dirty + code changes** → 2 commits: a `feat(...)` / `fix(...)` / `refactor(...)!:` for the substantive change, then `chore(release): vX.Y.Z` for CHANGELOG.
- **Tree dirty + only README/docs/CHANGELOG changes** → 1 commit `chore(release): vX.Y.Z`.
- **Tree clean** → 1 commit `chore(release): vX.Y.Z` for the CHANGELOG bump.

### Docs sync

For every `gk <cmd>` or `--<flag>` token in the new CHANGELOG section, verify it appears in `README.md` and `docs/commands.md`. Auto-draft any gap from `gk <cmd> --help` + Cobra `Use`/`Short`/`Long`. **Transcribe — don't editorialize.** Stay scoped to structured surface (synopsis, flag table); no tutorials or rationale.

If a token has no `--help` backing, surface it at Phase 3 instead of fabricating.

## Phase 3 — CONFIRM (single AskUserQuestion)

Show the inference summary in markdown above the prompt (the `question` field is invisible on dark terminals), then ask once.

Format:

```
## Release plan: vX.Y.Z

**Bump**: {patch|minor|major} (last: vA.B.C)  ← reason
**CHANGELOG diff**:
  +## [X.Y.Z] - YYYY-MM-DD
  +### Changed
  +- ...

**Commits to land** (2):
  1. feat(...): ... — N files
  2. chore(release): vX.Y.Z — CHANGELOG.md

**Docs gaps**: none / drafted for `gk foo`, `--bar`
**Will run**: go vet + go test -race → 2 commits → tag → push → watch → verify tap
```

Then `AskUserQuestion` with three options:

- **진행 (Recommended)** — execute Phase 4-5 as proposed
- **수정** — user types what to change (bump, CHANGELOG wording, commit message, etc.); re-propose
- **취소** — abort

If the user picks "수정", treat their next message as the override and loop back to Phase 3 with the corrected plan. Don't re-ask everything from scratch — only re-summarize the changed parts.

## Phase 4 — EXECUTE (linear, fail-loud)

Run sequentially. On any failure, surface the error and stop — do not auto-retry except where noted.

```bash
# Validate
golangci-lint run
go test ./... -race -cover
command -v goreleaser >/dev/null && goreleaser check || true
```

If `golangci-lint` is not installed, abort with:
> `golangci-lint not found — install via: brew install golangci-lint`

If tests or lint fail: report the failing package + first failed test line, stop. Do not tag.

```bash
# Edit CHANGELOG.md (promote [Unreleased] → [X.Y.Z], update compare links)
# Edit README.md / docs/* if Phase 2 drafted any doc gaps

# Stage + commit (1 or 2 commits per Phase 2)
git add <feature files>
git commit -m "<feat/fix/refactor message>"   # only if tree was dirty with code
git add CHANGELOG.md README.md docs/
git commit -m "chore(release): vX.Y.Z"

git push origin main
git tag -a "vX.Y.Z" -m "vX.Y.Z"
git push origin "vX.Y.Z"
```

Use `git add <specific files>` not `git add -A` — secrets/binaries leak that way.

## Phase 5 — VERIFY (watch via API, verify via ssh+CDN)

```bash
sleep 5
RUN_ID=$(gh run list -R x-mesh/gk --workflow release --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$RUN_ID" -R x-mesh/gk --exit-status
```

On non-zero exit, fetch the failure log and match against the table below. Retry up to 2× via `gh run rerun $RUN_ID --failed`, then escalate.

| Error pattern | Fix |
|---|---|
| `401 Bad credentials` (tap API) | `HOMEBREW_TAP_GITHUB_TOKEN` expired — regenerate PAT, `gh secret set`, rerun |
| `403 Resource not accessible` | PAT lacks `contents: write` on `x-mesh/homebrew-tap`. Recreate fine-grained PAT |
| `422 already_exists` (asset upload) | Stale partial release. `gh release delete vX.Y.Z -R x-mesh/gk --yes`, rerun. Do NOT delete the tag |
| `goreleaser check` fails | Fix `.goreleaser.yaml`, amend tag commit, force-push tag |

Then verify via ssh + CDN — **no GitHub API calls**. The API quota is shared across the whole machine and the watch above already burned some, so verify is intentionally API-free to avoid secondary rate limits.

```bash
# 1. tag landed
git ls-remote --tags git@github.com:x-mesh/gk.git "vX.Y.Z"

# 2. tap formula bumped (shallow clone, throwaway path)
TAP_TMP="/tmp/gk-tap-vX.Y.Z"
rm -rf "$TAP_TMP"
git clone --depth 1 git@github.com:x-mesh/homebrew-tap.git "$TAP_TMP" \
  && grep -E '^\s*(version|url ")' "$TAP_TMP/Formula/gk.rb" \
  && rm -rf "$TAP_TMP"

# 3. release asset reachable on the CDN
curl -sI "https://github.com/x-mesh/gk/releases/download/vX.Y.Z/checksums.txt" | head -1
```

Expect: a `refs/tags/vX.Y.Z` line, `version "X.Y.Z"` plus 4 archive URLs in the formula, and an `HTTP/2 302` (or 200) on the checksums HEAD.

## Phase 6 — Report

```
✅ Released vX.Y.Z

  brew install x-mesh/tap/gk     # new
  brew upgrade x-mesh/tap/gk     # existing
  gk --version                   # expect: gk version vX.Y.Z

Release: https://github.com/x-mesh/gk/releases/tag/vX.Y.Z
Formula: https://github.com/x-mesh/homebrew-tap/blob/main/Formula/gk.rb
```

## Arguments

| Input | Effect |
|---|---|
| (none) | full auto-infer |
| `patch` / `minor` / `major` | override bump, still auto-infer everything else |
| `X.Y.Z` or `vX.Y.Z` | override version exactly |
| `--dry-run` (after the version) | run Phases 1-3 and stop before any commit/push |

## Rollback

If a published release is bad:

```bash
gh release delete "vX.Y.Z" -R x-mesh/gk --yes
git tag -d "vX.Y.Z"
git push origin ":refs/tags/vX.Y.Z"
# manually edit Formula/gk.rb in x-mesh/homebrew-tap if it landed
```

Then fix the underlying issue and re-run `/release` with the same version.

## Hard boundaries

- **Never force-push main.** If push is rejected, stop and ask.
- **Never skip tests** with `--no-verify`. If they fail, the release is cancelled.
- **Never commit to `x-mesh/homebrew-tap` directly.** goreleaser is the only writer.
- **Never delete the tag remotely** unless the user invoked rollback explicitly.
- **Never bump to a version ≤ last tag.**
- **Never use `git add -A`** — stage specific files only.

## Anti-patterns (do not do these)

- ❌ Calling `AskUserQuestion` 4× (release strategy / bump / CHANGELOG / commit structure). Combine into one Phase-3 gate.
- ❌ Creating a `TaskList`. The flow is linear; status is communicated in text.
- ❌ Splitting prereqs into 5 separate bash calls. One shell block does it.
- ❌ Asking the user to write the CHANGELOG body. Auto-draft from diff, let them amend at Phase 3.
- ❌ Verbose mid-flow narration ("now I'll commit", "now I'll push"). State the plan once at Phase 3, then execute quietly and report at Phase 6.
- ❌ Re-reading CHANGELOG / running `gh release view` 3 different ways. One read, one write, one verify.

## Future-proofing

`goreleaser` will eventually deprecate `brews:` for `homebrew_casks:`. When that happens:
1. Update `.goreleaser.yaml`
2. Update Phase 5's failure-mode table if error shapes change
3. Verify locally: `goreleaser release --snapshot --clean`
