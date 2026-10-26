#!/bin/sh
set -eu
root=${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}}
# Never download or replace Codex itself: hosts may use a protected launcher.
# Keep all diagnostics off MCP stdout.
# Codex runs MCP servers inside its app-server daemon, so the daemon usually
# answers already. Start it only when it does not: `codex` on PATH may be a
# wrapper that refuses to run inside the daemon's sandbox, or that replaces a
# daemon it did not start, which would take this thread down with it.
if ! "$root/scripts/run.sh" --check-codex 2>/dev/null; then
  if [ -z "${AGENTEUR_CODEX_SOCKET:-}${COAI_CODEX_SOCKET:-}" ]; then
    codex app-server daemon start >&2
  fi
  "$root/scripts/run.sh" --check-codex
fi
exec "$root/scripts/run.sh" "$@"
