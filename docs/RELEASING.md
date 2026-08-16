# Releasing gk

`gk ship` is the release entry point. It owns preflight checks, version
selection, the release commit, tag and branch pushes, GitHub Actions tracking,
and post-release artifact verification. Do not create or push release tags by
hand during the normal release flow.

## One-time setup

1. The `x-mesh/homebrew-tap` repository must contain a `Casks/` directory.
2. Create a fine-grained PAT scoped to that repository with **Contents: Read
   and write** and **Metadata: Read-only**.
3. Store it in this repository as `HOMEBREW_TAP_GITHUB_TOKEN`:

   ```bash
   gh secret set HOMEBREW_TAP_GITHUB_TOKEN -R x-mesh/gk
   gh secret list -R x-mesh/gk
   ```

The release workflow uses `GITHUB_TOKEN` for the GitHub Release and the
separate PAT to update `x-mesh/homebrew-tap/Casks/gk.rb`.

## Prepare the release

Build the workspace binary once and use it for the entire release. The
installed `gk` may be the previous release.

```bash
make build
./bin/gk ship --dry-run --json
```

The plan reports the inferred SemVer bump, target version, release range,
CHANGELOG draft, branch/tag operations, and configured watch/verify steps. If
the working tree is dirty, finish and commit that work before planning again.

Review `CHANGELOG.md` under `[Unreleased]`. When the generated draft needs
editing, write the final release notes there, commit the documentation change,
then rerun the plan so it describes the exact commit that will ship.

To request a specific bump or version:

```bash
./bin/gk ship patch --dry-run --json
./bin/gk ship minor --dry-run --json
./bin/gk ship --version 0.137.0 --dry-run --json
```

## Publish

After reviewing the final plan:

```bash
./bin/gk ship -y
```

The repository-local `.gk.yaml` defines the release gates. A successful ship:

1. runs commit/branch/conflict checks, lint, unit/race tests, and E2E tests;
2. creates the release commit and tag and performs guarded pushes;
3. waits for `.github/workflows/release.yml`;
4. verifies the GitHub Release assets;
5. verifies the Homebrew cask version and published checksums.

Do not use `--skip-preflight` to force a release. Fix the failure, commit it,
and rerun the dry-run plan.

## Local packaging check

GoReleaser can validate packaging without publishing:

```bash
goreleaser check
goreleaser release --snapshot --clean
```

Archives are written to `dist/` as lowercase, versionless stable names such as
`gk_darwin_arm64.tar.gz` and `gk_linux_amd64.tar.gz`, together with
`checksums.txt`. They include the `gk` binary and `LICENSE`.

## Verify or resume

`gk ship -y` already waits and verifies. If an external failure interrupts the
watch after the tag was pushed, do not create the tag again. Inspect and rerun
the existing workflow, then execute the failed verification command printed by
ship. Useful checks are:

```bash
TAG="$(git describe --tags --abbrev=0)"
gh run list --branch "$TAG" --limit 5
gh release view "$TAG" -R x-mesh/gk
brew info --cask x-mesh/tap/gk
```

## Failure guide

| Symptom | Action |
|---|---|
| preflight lint/test/E2E failure | Fix and commit the code; rerun the plan |
| no workflow found after the discovery window | Confirm the tag exists on `origin`, then inspect Actions; do not re-ship |
| release workflow failure | Fix the workflow cause and rerun the existing run |
| tap-cask verification failure | Inspect GoReleaser logs and `Casks/gk.rb`; rerun verification after correction |
| checksum mismatch | Treat as a failed release; compare the Release asset and cask SHA before installing |
| macOS Gatekeeper removes the binary | Check the cask post-install `xattr` hook; current releases are not notarized |

## Exceptional rollback

Published tags are immutable during normal operation. Rollback deletes public
release state and therefore requires explicit maintainer approval. Prefer a
follow-up patch release whenever possible. If deletion is explicitly approved,
remove the GitHub Release and remote tag deliberately and reconcile the
Homebrew cask; never force-push `main`.
