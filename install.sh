#!/bin/sh
# gk installer — POSIX sh
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/x-mesh/gk/main/install.sh | sh
#
# Env overrides:
#   GK_VERSION=v0.29.0       pin a specific version (default: latest)
#   GK_INSTALL_DIR=/path     install location (default: /usr/local/bin, falls back to ~/.local/bin)

set -eu

REPO="x-mesh/gk"
BIN="gk"

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
default_dir=/usr/local/bin
target_dir=${GK_INSTALL_DIR:-$default_dir}

install_to() {
  dir=$1
  mkdir -p "$dir" 2>/dev/null || true
  if [ -w "$dir" ]; then
    install -m 0755 "$tmp/$BIN" "$dir/$BIN"
  elif [ "$dir" = "$default_dir" ] && command -v sudo >/dev/null 2>&1; then
    info "$dir not writable — using sudo"
    sudo install -m 0755 "$tmp/$BIN" "$dir/$BIN"
  else
    return 1
  fi
}

if ! install_to "$target_dir"; then
  fallback="$HOME/.local/bin"
  info "falling back to $fallback"
  install_to "$fallback" || err "install failed (no writable target)"
  target_dir=$fallback
fi

info "installed: $target_dir/$BIN ($version)"

case ":$PATH:" in
  *":$target_dir:"*) ;;
  *) printf "\n  Add %s to PATH:\n    export PATH=\"%s:\$PATH\"\n\n" "$target_dir" "$target_dir" ;;
esac
