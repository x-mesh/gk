---
name: release
description: Release workflow for gk — bumps version, updates CHANGELOG, tags, pushes, and monitors GitHub Actions until the Homebrew tap (x-mesh/homebrew-tap) is updated. Use when the user says "release", "cut a release", "ship v0.x", or "/release".
---

# Release workflow for gk

Invoke when the user asks to cut a new release (`/release`, "새 버전 내자", "v0.2.0 릴리즈" 등).

The goal is a green GitHub Release page **AND** an updated `x-mesh/homebrew-tap/Formula/gk.rb` that `brew install x-mesh/tap/gk` picks up. Anything short of that is incomplete.

## Prerequisites (fail fast if missing)

Run these checks first. Abort with a clear error if any fails.

```bash
# Working tree clean
[ -z "$(git status --porcelain)" ] || { echo "working tree not clean — commit or stash first"; exit 1; }

# On main
[ "$(git branch --show-current)" = "main" ] || { echo "must be on main branch"; exit 1; }

# Up-to-date with origin/main
git fetch origin main --quiet
LOCAL=$(git rev-parse main)
REMOTE=$(git rev-parse origin/main)
[ "$LOCAL" = "$REMOTE" ] || { echo "main is not in sync with origin/main — pull or push first"; exit 1; }

# gh CLI auth
gh auth status >/dev/null 2>&1 || { echo "gh CLI not authenticated"; exit 1; }

# HOMEBREW_TAP_GITHUB_TOKEN secret exists on x-mesh/gk
gh secret list -R x-mesh/gk | grep -q HOMEBREW_TAP_GITHUB_TOKEN || {
  echo "HOMEBREW_TAP_GITHUB_TOKEN secret missing. See docs/RELEASING.md"
  exit 1
}
```

If any check fails, stop and report — do not proceed.

## Step 1 — Pick the new version

Determine the latest tag:

```bash
LAST_TAG=$(git tag --list 'v*' --sort=-v:refname | head -1)
echo "Last release: ${LAST_TAG:-<none>}"
```

Use `AskUserQuestion` to pick the bump. Print the last tag in the markdown BEFORE the prompt (the question field is invisible on dark terminals). Options:

- `patch` — bug fixes only (v0.1.0 → v0.1.1)
- `minor` — new features, backward compatible (v0.1.0 → v0.2.0)
- `major` — breaking changes (v0.1.0 → v1.0.0)
- `explicit` — user types exact version (Other)

Compute `NEW_VERSION` from the choice. Strip leading `v`.

## Step 2 — Local validation

Run the full validation gauntlet. **Abort on any failure.**

```bash
go vet ./...
go test ./... -race -cover
# golangci-lint is optional — skip cleanly if missing
command -v golangci-lint >/dev/null && golangci-lint run || echo "(golangci-lint skipped)"
command -v goreleaser >/dev/null && goreleaser check || echo "(goreleaser skipped)"
```

If tests fail, surface the failure concisely and stop. Do not tag a broken tree.

## Step 3 — Update CHANGELOG.md

The file follows Keep a Changelog. Transform in-place:

```
## [Unreleased]                          ## [Unreleased]
                                →
<entries>                                ## [${NEW_VERSION}] - YYYY-MM-DD
                                         <entries>
```

Update the compare links at the bottom:

```
[Unreleased]: https://github.com/x-mesh/gk/compare/v${NEW_VERSION}...HEAD
[${NEW_VERSION}]: https://github.com/x-mesh/gk/compare/${LAST_TAG}...v${NEW_VERSION}
```

(For the very first release: `[${NEW_VERSION}]: https://github.com/x-mesh/gk/releases/tag/v${NEW_VERSION}`)

If `## [Unreleased]` is empty (no entries since the last release), ask the user to confirm — an empty release is usually a mistake.

Today's date: use `date +%Y-%m-%d` in the running environment.

## Step 4 — Commit + push + tag

```bash
git add CHANGELOG.md
git commit -m "chore(release): v${NEW_VERSION}"
git push origin main

git tag -a "v${NEW_VERSION}" -m "v${NEW_VERSION}"
git push origin "v${NEW_VERSION}"
```

## Step 5 — Watch the release workflow

Tag push triggers `.github/workflows/release.yml`. Poll:

```bash
# wait a beat for GH to register the workflow run
sleep 5
RUN_ID=$(gh run list -R x-mesh/gk --workflow release --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$RUN_ID" -R x-mesh/gk --exit-status
```

If `gh run watch` returns non-zero, diagnose:

```bash
gh run view "$RUN_ID" -R x-mesh/gk --log-failed | grep -E "error|fail|401|403|404|422" | tail -20
```

Common failure modes and fixes:

| Error | Fix |
|---|---|
| `401 Bad credentials` on tap API | `HOMEBREW_TAP_GITHUB_TOKEN` missing or expired — regenerate PAT, re-run `gh secret set`, then `gh run rerun $RUN_ID --failed` |
| `403 Resource not accessible by PAT` | PAT lacks `contents: write` on `x-mesh/homebrew-tap`. Recreate fine-grained PAT with the right scope. |
| `422 already_exists` on asset upload | Previous partial release left stale assets. `gh release delete v${NEW_VERSION} -R x-mesh/gk --yes`, then `gh run rerun $RUN_ID --failed`. Do NOT delete the tag. |
| `goreleaser check` fails | Fix `.goreleaser.yaml`, amend tag commit, force-push tag. (Rare — `goreleaser check` also runs locally in step 2.) |

Retry up to 2 times, then escalate to the user with the concrete error.

## Step 6 — Verify the tap

```bash
# Formula file exists in tap
gh api repos/x-mesh/homebrew-tap/contents/Formula/gk.rb --jq '.download_url'

# Release has all 5 assets (4 archives + checksums.txt)
gh release view "v${NEW_VERSION}" -R x-mesh/gk --json assets --jq '.assets | length'
# should print 5
```

If either check fails, the tap did NOT update — report the issue to the user.

## Step 7 — Report

Final summary to the user:

```
✅ Released v${NEW_VERSION}

Install:
  brew install x-mesh/tap/gk
  # or upgrade existing:
  brew upgrade x-mesh/tap/gk

Verify:
  gk --version    # expect: gk version v${NEW_VERSION}

Links:
  Release: https://github.com/x-mesh/gk/releases/tag/v${NEW_VERSION}
  Formula: https://github.com/x-mesh/homebrew-tap/blob/main/Formula/gk.rb
```

## Arguments

If the user passed an argument to `/release` (e.g. `/release minor` or `/release 0.2.0`):
- `patch` / `minor` / `major` → skip the AskUserQuestion in Step 1, use directly
- Looks like `X.Y.Z` or `vX.Y.Z` → use as the explicit version
- Otherwise → proceed with the interactive flow

## Rollback

If the release is broken after publish (e.g. wrong content, bad binary):

```bash
gh release delete "v${NEW_VERSION}" -R x-mesh/gk --yes
git tag -d "v${NEW_VERSION}"
git push origin ":refs/tags/v${NEW_VERSION}"
# manually remove Formula/gk.rb entry in x-mesh/homebrew-tap if present
```

Then fix the underlying issue and re-run `/release` with the same version.

## Boundaries — what NOT to do

- **Never force-push main.** If push is rejected, ask the user to resolve — don't work around.
- **Never skip tests** with `--no-verify` or equivalent. If tests fail, the release is cancelled.
- **Never commit to `x-mesh/homebrew-tap` directly.** goreleaser is the only writer — bypassing it creates drift.
- **Never delete the tag remotely** unless the user explicitly requests rollback (Step 7 above).
- **Never bump to a version ≤ last tag.** Abort with a clear error.

## Future-proofing

`goreleaser` will eventually deprecate the `brews:` block in favor of `homebrew_casks:`. When that happens:
1. Update `.goreleaser.yaml` accordingly
2. Update this skill's failure-mode table if the error shape changes
3. Re-verify with a snapshot: `goreleaser release --snapshot --clean`
