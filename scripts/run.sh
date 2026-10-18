#!/bin/sh
# Starts agenteur for both plugins and the Claude monitor. Diagnostics go to
# stderr: stdout is the MCP stream, or the monitor's event stream.
set -eu
root=${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}}

# A build from scripts/build-plugin.sh runs local code during development.
if [ -x "$root/bin/agenteur" ]; then
  exec "$root/bin/agenteur" "$@"
fi

# Otherwise one shared install outside the versioned plugin caches, so Claude
# Code and Codex run the same binary and its daily self-update reaches both.
# Another agenteur on PATH is not used, so the version check below holds.
dir="${HOME}/.local/bin"
bin="$dir/agenteur"

install() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in darwin|linux) ;; *) echo "agenteur: unsupported operating system: $os" >&2; exit 1 ;; esac
  arch=$(uname -m)
  case "$arch" in amd64|x86_64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "agenteur: unsupported architecture: $arch" >&2; exit 1 ;; esac
  mkdir -p "$dir"
  tmp=$(mktemp -d "$dir/.agenteur-install.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL "https://github.com/erwint/agenteur/releases/latest/download/agenteur_${os}_${arch}.tar.gz" | tar -xz -C "$tmp" agenteur
  chmod 755 "$tmp/agenteur"
  # Rename into place so a concurrent first start never runs a partial file.
  # An agenteur link that coai's last update left points at ~/.local/bin/coai;
  # the rename replaces the link, not the file old coai plugins still run.
  mv -f "$tmp/agenteur" "$bin"
  echo "agenteur: installed $("$bin" version) to $bin" >&2
  trap - EXIT
  rm -rf "$tmp"
}

# Install on first use, and replace a binary that cannot run at all, since it
# cannot update itself.
if [ ! -x "$bin" ] || ! "$bin" version >/dev/null 2>&1; then
  install
fi

# The plugin may rely on binary features newer than the installed agenteur,
# which otherwise catches up only on its daily self-update.
# requires-agenteur names the oldest release this plugin works with; an older
# install updates first.
required=$(cat "$root/scripts/requires-agenteur" 2>/dev/null || true)
older() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    split(a, x, "."); split(b, y, ".")
    for (i = 1; i <= 3; i++) { if (x[i] + 0 < y[i] + 0) exit 0; if (x[i] + 0 > y[i] + 0) exit 1 }
    exit 1
  }'
}
if [ -n "$required" ]; then
  have=$("$bin" version 2>/dev/null || echo 0.0.0)
  if older "$have" "$required"; then
    "$bin" update >&2 || true
    have=$("$bin" version 2>/dev/null || echo 0.0.0)
    # Starting an older agenteur beats not starting: most tools still work.
    if older "$have" "$required"; then
      echo "agenteur: this plugin needs agenteur $required or newer, but $have could not be updated" >&2
    fi
  fi
fi

exec "$bin" "$@"
