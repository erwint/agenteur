# AGENTS.md

## Versions

coai has two versions, and they change independently.

- **Plugin version:** the `version` field in `.claude-plugin/plugin.json` and `.codex-plugin/plugin.json`. Claude Code and Codex install a plugin update only when this string changes.
- **Binary version:** taken from the git tag at build time (`-X main.version`). It is never written in the source. Installed binaries update themselves from `releases/latest` once a day.

### When to bump the plugin version

Bump it when a change touches anything the plugin delivers:

- `.claude-plugin/plugin.json` or `.codex-plugin/plugin.json` (other than the version itself)
- `.claude-mcp.json`
- `scripts/`
- `skills/`

Do not bump it for Go code, tests, the README, `CLAUDE.md`, `AGENTS.md`, or CI. Those reach users as a new binary, not as a plugin update.

Rules:

- Keep both manifests at the same version.
- Never add `version` to the entry in `.claude-plugin/marketplace.json`: the manifest overrides it, so a second copy can only drift.
- Bump once per push, not once per commit. If `git diff origin/main -- .claude-plugin/plugin.json` already shows a bump, leave it.
- Make the bump in the same commit as the plugin change, or in a follow-up commit before pushing.
- Use semver:
  - **Patch:** fixes and wording changes in scripts or skills.
  - **Minor:** a new skill, monitor, hook or MCP server, or a new launcher behaviour.
  - **Major:** renaming or removing a skill or another component users invoke.

### Releasing the binary

- To release, tag the commit with `git tag -a vX.Y.Z -m "Release vX.Y.Z"` and push the tag. GoReleaser builds and publishes the archives. Don't edit any file to set the binary version.
- Don't tag a release for a plugin-only change.
- If a plugin change needs a new binary feature (as the `watch` monitor needed 0.3.0), push the release tag first and the plugin bump after.
- Even then, existing installs pick up the new binary only on their next daily self-update. If the gap matters, have the plugin call `coai update` before relying on the feature.
