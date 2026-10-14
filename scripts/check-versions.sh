#!/bin/sh
# Checks the version rules in AGENTS.md. Run by CI; usable locally too.
#
#   scripts/check-versions.sh [base]
#
# With a base commit, also checks that a change to plugin files since base
# came with a plugin version bump.
set -eu
cd "$(dirname "$0")/.."

# The files the plugins run or read at runtime. Changing any of them changes
# what users get, so it needs a new plugin version. Keep in step with AGENTS.md.
PLUGIN_FILES=".claude-plugin/plugin.json .codex-plugin/plugin.json .claude-mcp.json scripts/run.sh scripts/run-codex.sh scripts/requires-coai skills"

fail=0
error() { echo "check-versions: $*" >&2; fail=1; }

# The first "version" field in a manifest, read from a file or stdin.
manifest_version() { perl -ne 'if (/"version"\s*:\s*"([^"]*)"/) { print $1; exit }' "$@"; }

older() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    split(a, x, "."); split(b, y, ".")
    for (i = 1; i <= 3; i++) { if (x[i] + 0 < y[i] + 0) exit 0; if (x[i] + 0 > y[i] + 0) exit 1 }
    exit 1
  }'
}

claude=$(manifest_version .claude-plugin/plugin.json)
codex=$(manifest_version .codex-plugin/plugin.json)
[ -n "$claude" ] || error ".claude-plugin/plugin.json has no version"
[ "$claude" = "$codex" ] || error "plugin versions differ: Claude $claude, Codex $codex"

for m in .claude-plugin/marketplace.json .agents/plugins/marketplace.json; do
  if grep -q '"version"' "$m"; then
    error "$m sets a version; the manifests' is the only one"
  fi
done

# Users install coai@coai in either client.
for m in .claude-plugin/marketplace.json .agents/plugins/marketplace.json; do
  name=$(perl -ne 'if (/"name"\s*:\s*"([^"]*)"/) { print $1; exit }' "$m")
  [ "$name" = coai ] || error "$m names the marketplace $name, not coai"
done

# A plugin must never require a binary nobody can download yet.
required=$(cat scripts/requires-coai)
latest=$(git tag -l 'v[0-9]*' --sort=-v:refname | head -n 1)
if [ -z "$latest" ]; then
  error "no release tags found (fetch with tags to check scripts/requires-coai)"
elif older "${latest#v}" "$required"; then
  error "scripts/requires-coai is $required, but the newest release is $latest: release the binary first"
fi

base=${1:-}
if [ -n "$base" ] && git cat-file -e "$base^{commit}" 2>/dev/null; then
  # shellcheck disable=SC2086 # PLUGIN_FILES is a list of paths
  changed=$(git diff --name-only "$base" HEAD -- $PLUGIN_FILES)
  before=$(git show "$base:.claude-plugin/plugin.json" 2>/dev/null | manifest_version || true)
  if [ -n "$changed" ] && [ "$before" = "$claude" ]; then
    error "plugin files changed since $(git rev-parse --short "$base") without a plugin version bump (still $claude):"
    echo "$changed" | sed 's/^/  /' >&2
  fi
fi

[ "$fail" = 0 ] && echo "check-versions: plugin $claude, requires coai $required, newest release $latest"
exit "$fail"
