#!/bin/sh
# Releases the binary or the plugin. See "Versions" in AGENTS.md.
#
#   scripts/release.sh binary X.Y.Z
#       Tags vX.Y.Z on HEAD, pushes the tag, and waits until GoReleaser has
#       published the archives, so a plugin requiring X.Y.Z can follow.
#
#   scripts/release.sh plugin X.Y.Z [--requires A.B.C]
#       Sets the plugin version in both manifests (and, with --requires,
#       scripts/requires-coai), checks the rules, and commits. Push it yourself.
set -eu
cd "$(dirname "$0")/.."

usage() { sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
semver() { echo "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo "release: $1 is not X.Y.Z" >&2; exit 2; }; }

[ $# -ge 2 ] || usage
what=$1 version=$2
shift 2
semver "$version"

if [ -n "$(git status --porcelain)" ]; then
  echo "release: the working tree has uncommitted changes" >&2
  exit 1
fi

case "$what" in
binary)
  [ $# -eq 0 ] || usage
  tag="v$version"
  if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
    echo "release: $tag already exists" >&2
    exit 1
  fi
  git tag -a "$tag" -m "Release $tag"
  git push origin "$tag"
  asset="https://github.com/erwint/coai/releases/download/$tag/coai_linux_amd64.tar.gz"
  echo "release: waiting for GoReleaser to publish $tag"
  i=0
  until curl -fsIL -o /dev/null "$asset"; do
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then
      echo "release: $tag not published after 10 minutes; check the Release workflow" >&2
      exit 1
    fi
    sleep 10
  done
  echo "release: $tag is published"
  ;;
plugin)
  requires=""
  if [ $# -gt 0 ]; then
    [ "$1" = "--requires" ] && [ $# -eq 2 ] || usage
    requires=$2
    semver "$requires"
  fi
  for m in .claude-plugin/plugin.json .codex-plugin/plugin.json; do
    # Only the first "version" is the manifest's own.
    V="$version" perl -0pi -e 's/"version"\s*:\s*"[^"]*"/"version": "$ENV{V}"/' "$m"
  done
  [ -z "$requires" ] || echo "$requires" > scripts/requires-coai
  git fetch -q --tags origin
  scripts/check-versions.sh
  git commit -q -am "Release plugin $version" -m "Plugin $version requires coai $(cat scripts/requires-coai) or newer."
  echo "release: committed plugin $version; push with: git push origin HEAD"
  ;;
*)
  usage
  ;;
esac
