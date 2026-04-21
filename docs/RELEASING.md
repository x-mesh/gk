# Releasing gk

## First-time setup

### 1. Create the Homebrew tap repository

Create a new GitHub repository named `homebrew-tap` under the user/org that will own the tap:

```bash
gh repo create <owner>/homebrew-tap --public --description "Homebrew tap for gk"
```

Initialize with a `Formula/` directory and a placeholder README.

> **Note**: Update `repository.owner` and `homepage` in `.goreleaser.yaml` to match your actual GitHub username/organization before releasing.

### 2. Generate a PAT for tap updates

The default `GITHUB_TOKEN` cannot push to a different repository. Create a fine-grained PAT with write permissions to the tap repo:

1. GitHub → Settings → Developer settings → Personal access tokens → Fine-grained tokens → Generate new token
2. Repository access: only the `<owner>/homebrew-tap` repo
3. Permissions:
   - Contents: **Read and write**
   - Metadata: **Read-only**
4. Copy the generated token

### 3. Add the secret to the gk repository

```bash
gh secret set HOMEBREW_TAP_GITHUB_TOKEN --body "<token>" -R <owner>/gk
```

Verify the secret is set:

```bash
gh secret list -R <owner>/gk
```

## Local validation

### Install goreleaser (macOS)

```bash
brew install goreleaser
```

### Validate configuration

```bash
goreleaser check
```

### Local snapshot build (no publish)

```bash
goreleaser release --snapshot --clean
# or: make release-snapshot  (if Makefile target exists)
```

Produces binaries in `dist/`. Good for pre-tag verification:

```
dist/
  gk_Darwin_arm64.tar.gz
  gk_Darwin_x86_64.tar.gz
  gk_Linux_arm64.tar.gz
  gk_Linux_x86_64.tar.gz
  checksums.txt
```

Test the built binary:

```bash
./dist/gk_darwin_arm64_v1/gk --version
```

## Releasing

### Publishing v0.1.0

```bash
# Ensure clean working tree
git status

# Create and push the tag
git tag v0.1.0
git push origin v0.1.0
```

The release workflow (`.github/workflows/release.yml`) will automatically:

1. Validate `.goreleaser.yaml` via `goreleaser check`
2. Build darwin/linux x amd64/arm64 archives
3. Create the GitHub Release with assets and `checksums.txt`
4. Update `<owner>/homebrew-tap` with `Formula/gk.rb`

### Verifying the release

After the workflow completes:

```bash
brew tap <owner>/tap
brew install gk
gk --version
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `403` on tap push | Default GITHUB_TOKEN used | Ensure `HOMEBREW_TAP_GITHUB_TOKEN` secret is set |
| `not a repository` | Tap repo not created | Create `<owner>/homebrew-tap` first |
| `checksums.txt` missing | goreleaser failed mid-run | Check Actions job logs; re-run workflow |
| macOS Gatekeeper warning | Unsigned binary | Users install via `brew` (no quarantine) |
| `goreleaser check` fails | Config syntax error | Run `goreleaser check` locally and fix errors |

## Rolling back a release

```bash
# Delete tag locally and remote
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0

# Delete the GitHub Release via gh
gh release delete v0.1.0 -y

# Remove the formula from tap (manual PR or direct push to homebrew-tap repo)
```
