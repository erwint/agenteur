#!/bin/sh
# Starts coai for both plugins and the Claude monitor. Diagnostics go to
# stderr: stdout is the MCP stream, or the monitor's event stream.
set -eu
root=${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}}

# A build from scripts/build-plugin.sh runs local code during development.
if [ -x "$root/bin/coai" ]; then
  exec "$root/bin/coai" "$@"
fi

# Otherwise one shared install outside the versioned plugin caches, so Claude
# Code and Codex run the same binary and coai's daily self-update reaches both.
# Another coai on PATH is not used: a pre-0.3 release lacks the watch command.
dir="${HOME}/.local/bin"
if [ -x "$dir/coai" ]; then
  exec "$dir/coai" "$@"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in darwin|linux) ;; *) echo "coai: unsupported operating system: $os" >&2; exit 1 ;; esac
arch=$(uname -m)
case "$arch" in amd64|x86_64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "coai: unsupported architecture: $arch" >&2; exit 1 ;; esac
mkdir -p "$dir"
tmp=$(mktemp -d "$dir/.coai-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "https://github.com/erwint/coai/releases/latest/download/coai_${os}_${arch}.tar.gz" | tar -xz -C "$tmp" coai
chmod 755 "$tmp/coai"
# Rename into place so a concurrent first start never runs a partial file.
mv -f "$tmp/coai" "$dir/coai"
echo "coai: installed $("$dir/coai" version) to $dir/coai" >&2
trap - EXIT
rm -rf "$tmp"
exec "$dir/coai" "$@"
