#!/bin/sh
set -eu
root=${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}}
# Never download or replace Codex itself: hosts may use a protected launcher.
# Keep all diagnostics off MCP stdout.
if [ -z "${AGENTEUR_CODEX_SOCKET:-}${COAI_CODEX_SOCKET:-}" ]; then
  codex app-server daemon start >&2
fi
"$root/scripts/run.sh" --check-codex
exec "$root/scripts/run.sh" "$@"
