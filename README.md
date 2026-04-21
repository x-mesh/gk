# gk — git helper

A lightweight Go git helper for daily pull/log/status/branch workflows.

[![Go Version](https://img.shields.io/badge/go-1.23+-blue.svg)](https://golang.org/dl/)
[![CI](https://img.shields.io/badge/CI-passing-brightgreen.svg)](https://github.com/x-mesh/gk/actions)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Why gk?

- **One command for fetch + rebase** — `gk pull` auto-detects your base branch so you never type `git rebase origin/main` again.
- **Readable commit history at a glance** — `gk log` prints a color-coded short log with relative timestamps and branch refs.
- **Conflict recovery without memorizing flags** — `gk continue` and `gk abort` work across rebase, merge, and cherry-pick.
- **Branch hygiene made easy** — `gk branch clean` removes merged branches while protecting `main`, `master`, and `develop`.

## Install

### go install

```bash
go install github.com/x-mesh/gk/cmd/gk@latest
```

### Homebrew tap

```bash
brew install x-mesh/tap/gk
```

## Quickstart

```bash
# Fetch and rebase onto the auto-detected base branch
gk pull

# Show the last 20 commits with color and relative dates
gk log

# Show concise working tree status
gk status

# List all local branches, marking stale ones (>30 days)
gk branch list --stale 30

# Pick a branch interactively and check it out
gk branch pick
```

## Commands

| Command | Alias | Description |
|---------|-------|-------------|
| `gk pull` | | Fetch and rebase onto base branch |
| `gk log` | `gk slog` | Show short colorful commit log |
| `gk status` | `gk st` | Show concise working tree status |
| `gk branch list` | | List branches with filters |
| `gk branch clean` | | Delete merged branches |
| `gk branch pick` | | Interactively checkout a branch |
| `gk continue` | | Continue interrupted rebase/merge/cherry-pick |
| `gk abort` | | Abort interrupted rebase/merge/cherry-pick |
| `gk config show` | | Print resolved configuration as YAML |
| `gk config get <key>` | | Print a single config value |

See [docs/commands.md](docs/commands.md) for full flag reference and examples.

## Global Flags

These flags apply to every subcommand:

| Flag | Description |
|------|-------------|
| `--dry-run` | Print actions without executing |
| `--json` | JSON output where supported |
| `--no-color` | Disable color output |
| `--repo <path>` | Path to git repo (default: current directory) |
| `--verbose` | Verbose output |

## Configuration

gk reads configuration from multiple sources in priority order (highest wins):

1. CLI flags
2. `GK_*` environment variables
3. `git config gk.*` entries
4. `.gk.yaml` in the repo root
5. `~/.config/gk/config.yaml` (XDG)
6. Built-in defaults

See [docs/config.md](docs/config.md) for all fields. A sample config file is at [examples/config.yaml](examples/config.yaml).

## Development

```bash
git clone https://github.com/x-mesh/gk.git
cd gk

# Build
make build          # outputs to bin/gk

# Test
make test           # go test ./... -race -cover

# Lint
make lint           # golangci-lint run

# Format
make fmt            # gofmt + go mod tidy
```

Requires Go 1.23+ and git 2.30+.

## License

[MIT](LICENSE)
