---
name: release
description: Release workflow for gk — bumps version, updates CHANGELOG, auto-syncs README + docs/commands.md for any new commands/flags, tags, pushes, and monitors GitHub Actions until the Homebrew tap (x-mesh/homebrew-tap) is updated. Use when the user says "release", "cut a release", "ship v0.x", or "/release".
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

If `## [Unreleased]` is empty (no entries since the last release), run the diff between `${LAST_TAG}..HEAD` to see if user-visible changes slipped in. Do **not** auto-generate CHANGELOG entries from commits — ask the user to write them. Releases without a CHANGELOG are a UX regression. If the diff is truly internal (CI, tests, tooling), confirm with the user and proceed.

Today's date: use `date +%Y-%m-%d` in the running environment.

## Step 3b — Documentation sync (auto-update by default)

Every new user-facing command or flag that ships needs to appear in both `README.md` (Commands table) and `docs/commands.md` (reference section). After the CHANGELOG is promoted to the new version section, **scan for gaps and write the docs in the same release commit by default** — releasing with code/docs drift is a UX regression.

### 1. Detect gaps

Extract command/flag tokens from the just-promoted version block:

```bash
# Everything between the new version header and the next version header.
NEW_SECTION=$(awk "/^## \\[${NEW_VERSION}\\]/{flag=1;next}/^## \\[/{flag=0}flag" CHANGELOG.md)

# Unique `gk <cmd>` and `gk <cmd> --flag` mentions.
NEW_CMDS=$(echo "$NEW_SECTION" | grep -oE '`gk [a-z][a-z-]+( --[a-z-]+)?`' | sort -u)
```

Also diff against the binary surface to catch tokens the CHANGELOG missed:

```bash
go run ./cmd/gk --help | awk '/Available Commands:/,/Flags:/' \
  | awk 'NR>1 && $1!="Flags:" && $1!="help" && $1!="completion" && NF>0 {print $1}' \
  | sort -u > /tmp/gk-help.txt
grep -oE '^## gk [a-z-]+' docs/commands.md | awk '{print $3}' | sort -u > /tmp/gk-docs.txt
comm -23 /tmp/gk-help.txt /tmp/gk-docs.txt   # commands in binary, missing in docs
```

For each gap, check:

1. **README.md** — Commands table contains the command or flag.
2. **docs/commands.md** — either a `## gk <cmd>` section, or the flag mentioned under its parent command.

### 2. Default action — write the docs draft, then show the diff

When gaps exist, **draft the missing entries automatically** and proceed. The draft is a starting point, not the final word; the user reviews via the eventual `git diff` and can amend before the release commit.

Pull facts from these structured sources, in this order. Prefer transcription over invention:

| Source | Use for |
|---|---|
| `go run ./cmd/gk <cmd> --help` | Synopsis, flag table (name, default, one-line description) |
| The promoted CHANGELOG section | The "what's new" framing (one-liner per command) |
| Cobra `Use` / `Short` / `Long` strings in source | Authoritative description prose |
| Recent commits touching the new code path | Motivation / context for non-obvious choices |

Drafting rules:

- **Transcribe, don't editorialize.** A flag's description should match `--help` output, not paraphrase it.
- **Match existing style.** Read the surrounding section's heading depth, table format, and example block layout before writing — consistency beats elegance.
- **Keep prose terse.** One paragraph of intent, one synopsis block, one flag table, one short examples block. No marketing language.
- **Mark uncertainty.** If a behavior is unclear from `--help` and source, leave a `<!-- review: <question> -->` HTML comment instead of guessing — easier to grep than to debug a wrong claim.
- **Never invent flags or behaviors.** If a token from the CHANGELOG has no `--help` or source backing, surface it to the user instead of fabricating docs.

After drafting, surface a diff summary so the user can intervene:

> Drafted docs for `gk <cmd>` and `--<flag>` in README.md and docs/commands.md. Review the staged diff before the release commit lands. Tell me to revise or revert if anything is off.

### 3. When to ask first instead of auto-writing

Skip the auto-draft and use `AskUserQuestion` only when:

- The new command's purpose is genuinely unclear from `--help` and recent commits (rare — usually means the code itself is under-documented).
- The CHANGELOG entry uses prose the user hand-tuned (e.g. marketing voice for a flagship feature) and the docs need to match that voice.
- The user has explicitly said "I'll write the docs" earlier in the session.

In those cases the existing options apply: pause and wait, or append a `- TODO: document <token>` line under `## [Unreleased]` and proceed.

### 4. Boundaries

Auto-drafting is scoped to **transcribing structured surface**. It is not a license to write tutorials, ADRs, or rationale narratives — those still belong to a human editor. If the gap is bigger than "this flag is missing from the table," ask first.

## Step 4 — Commit + push + tag

```bash
git add CHANGELOG.md
# Include any docs/README updates made during Step 3b.
git add README.md docs/
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
