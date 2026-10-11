#!/bin/sh
set -eu
root=${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}}
bin="$root/bin/coai"
if [ ! -x "$bin" ]; then
  mkdir -p "$root/bin"
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in amd64|x86_64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "unsupported architecture: $arch" >&2; exit 1 ;; esac
  case "$os" in darwin) release_os=Darwin ;; linux) release_os=Linux ;; *) echo "unsupported operating system: $os" >&2; exit 1 ;; esac
  url="https://github.com/erwint/coai/releases/download/v0.2.3/coai_0.2.3_${release_os}_${arch}.tar.gz"
  tmp="$bin.download.$$"
  curl -fsSL "$url" -o "$tmp.tar.gz"
  tar -xzf "$tmp.tar.gz" -C "$root/bin" coai
  rm -f "$tmp.tar.gz"
  chmod 755 "$bin"
fi
exec "$bin" "$@"
