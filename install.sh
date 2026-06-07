#!/bin/sh
# gk installer — POSIX sh
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/x-mesh/gk/main/install.sh | sh
#
# Env overrides:
#   GK_VERSION=v0.29.0       pin a specific version (default: latest)
#   GK_INSTALL_DIR=/path     install location (default: ~/.local/bin)

set -eu

REPO="x-mesh/gk"
BIN="gk"
# Second name installed alongside `gk`. The `gk` name is shadowed by a shell
# alias on common setups (oh-my-zsh's git plugin sets `gk=gitk`), and a
# `git-kit` on PATH also makes `git kit …` resolve as a native git subcommand.
ALT="git-kit"

err() { printf "gk-install: %s\n" "$*" >&2; exit 1; }
info() { printf "gk-install: %s\n" "$*"; }

# --- detect os/arch ---------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) err "unsupported architecture: $arch" ;;
esac
case "$os" in
  linux|darwin) ;;
  *) err "unsupported OS: $os (try 'go install github.com/x-mesh/gk/cmd/gk@latest')" ;;
esac

command -v curl >/dev/null 2>&1 || err "curl is required"
command -v tar  >/dev/null 2>&1 || err "tar is required"

# --- pick version -----------------------------------------------------------
version=${GK_VERSION:-}
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$version" ] || err "could not determine latest release tag"
fi
case "$version" in v*) ;; *) version="v$version" ;; esac

asset="${BIN}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${version}"

# --- download + verify ------------------------------------------------------
tmp=$(mktemp -d 2>/dev/null || mktemp -d -t gk-install)
trap 'rm -rf "$tmp"' EXIT INT TERM

info "downloading ${asset} (${version})"
curl -fsSL "${base}/${asset}"        -o "$tmp/$asset"        || err "download failed: ${base}/${asset}"
curl -fsSL "${base}/checksums.txt"   -o "$tmp/checksums.txt" || err "download failed: ${base}/checksums.txt"

expected=$(awk -v f="$asset" '$2 == f {print $1}' "$tmp/checksums.txt")
[ -n "$expected" ] || err "checksum entry not found for $asset"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
else
  err "no sha256 tool available (install coreutils or shasum)"
fi
[ "$expected" = "$actual" ] || err "checksum mismatch (expected $expected, got $actual)"

tar -xzf "$tmp/$asset" -C "$tmp" "$BIN" || err "extract failed"

# --- install ----------------------------------------------------------------
default_dir="$HOME/.local/bin"
target_dir=${GK_INSTALL_DIR:-$default_dir}

# link_alt creates the `git-kit` name next to the installed `gk`. A relative
# symlink keeps it valid if the bin dir is later moved; a plain copy is the
# fallback for filesystems without symlink support. Never fatal — the primary
# `gk` install has already succeeded by the time we get here.
link_alt() {
  dir=$1
  ln -sf "$BIN" "$dir/$ALT" 2>/dev/null \
    || cp "$tmp/$BIN" "$dir/$ALT" 2>/dev/null \
    || return 1
}

install_to() {
  dir=$1
  mkdir -p "$dir" 2>/dev/null || true
  if [ -w "$dir" ]; then
    install -m 0755 "$tmp/$BIN" "$dir/$BIN"
    link_alt "$dir" || true
  elif command -v sudo >/dev/null 2>&1; then
    # Only reached when the user overrides GK_INSTALL_DIR to a system path.
    info "$dir not writable — using sudo"
    sudo install -m 0755 "$tmp/$BIN" "$dir/$BIN"
    sudo ln -sf "$BIN" "$dir/$ALT" 2>/dev/null \
      || sudo cp "$tmp/$BIN" "$dir/$ALT" 2>/dev/null || true
  else
    return 1
  fi
}

install_to "$target_dir" || err "install failed (no writable target: $target_dir)"

info "installed: $target_dir/$BIN (also as $ALT) ($version)"

case ":$PATH:" in
  *":$target_dir:"*) ;;
  *) printf "\n  Add %s to PATH:\n    export PATH=\"%s:\$PATH\"\n\n" "$target_dir" "$target_dir" ;;
esac
