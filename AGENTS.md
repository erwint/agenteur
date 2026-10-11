# AGENTS.md

Guidance for coding agents (Claude Code, Codex, and others) working in this repository.

## What this is

`coai` is a single-binary Go MCP server (stdio transport) that lets Claude Code, Codex CLI, and other MCP clients coordinate through durable tasks, messages, and advisory file leases. Every client spawns its **own** `coai` process; the processes share state only through one SQLite database (WAL mode, `modernc.org/sqlite`, pure Go, no cgo).

## Commands

```bash
make test                     # go test ./...
make lint                     # go vet ./...
make build                    # stripped ./coai binary
./scripts/build-plugin.sh     # build bin/coai, which the plugin launchers use instead of downloading a release
go test -run TestFileLease .  # single test (everything is in package main at the repo root)
./scripts/run.sh --check-codex  # verify the Codex app-server connection and exit
```

CI (`.github/workflows/ci.yml`) runs test, vet, and build. Releases are made by pushing a `vX.Y.Z` tag; GoReleaser publishes the archives.

## Architecture

- `main.go` dispatches the `watch` subcommand and registers every MCP tool as a thin wrapper over `Broker`. Tool results are JSON text built by `text(...)`, and errors are usually returned **in the payload** (`"error": ...`, `"codex_error": ...`) rather than as MCP errors. Keep that convention.
- `broker.go` holds the SQLite schema (created inline in `NewBroker`) and all persistence. IDs come from `time.Now().UnixNano()`. The default DB path is `<UserConfigDir>/coai/state.json`, or `COAI_STATE` / `-state`. The `.json` name is kept for compatibility even though the file is SQLite.
- Cross-process waiting works by polling. `Broker.Await` polls the tasks table every 200ms until the status leaves `pending`.
- Push delivery (`events.go`): events (a message to the agent, a task assigned to it, the final state of a task it created) are derived from the messages and tasks tables. Delivery state lives in the `notified` columns. Every sender calls `ClaimEvent` (compare-and-set) before delivering and `UnclaimEvent` if delivery fails, so each event goes out once across processes. Columns added after the first schema go through `migrate()`, which marks existing rows as delivered.
- Claude delivery (`watch.go`): the plugin monitor runs `coai watch`, which prints one line per event. Each line becomes a notification that wakes the session, so it must print nothing else. It finds its agents through `CLAUDE_CODE_SESSION_ID`/`CLAUDE_PID`, which `join_session` stores on the agent row. A later join from the same Claude session takes them over. The monitor starts when the `join` skill (`skills/join`) is invoked.
- Codex delivery (`codex.go`): the Codex agent's own MCP process polls each second and sends events with `thread/queue/add` (falling back to `thread/inject_items` for ephemeral threads). The app-server does not deduplicate `clientUserMessageId`, so the claim is what prevents duplicates. Each app-server call opens a fresh WebSocket on the Unix socket (`COAI_CODEX_SOCKET`, else `$CODEX_HOME/app-server-control/app-server-control.sock`) with a 10s deadline and the `initialize`/`initialized` handshake. Threads are verified with `thread/read` before being registered.
- Tests stand up fake app-servers on temporary Unix sockets (see `codex_test.go`, `broker_test.go`).

## Plugin packaging

- Claude: `.claude-plugin/plugin.json` points to `.claude-mcp.json`, which runs `scripts/run.sh`, and declares the `events` monitor (`run.sh watch`). `run.sh` runs `bin/coai` if a local build exists. Otherwise it runs `~/.local/bin/coai`, which is shared by both plugins, installing the latest release there on first use. It deliberately ignores any other `coai` on `PATH`, since pre-0.3 releases lack `watch`. Before starting coai it updates the binary if it is older than `scripts/requires-coai`.
- Codex: `.codex-plugin/plugin.json` runs `scripts/run-codex.sh`. That script starts `codex app-server daemon start` (unless `COAI_CODEX_SOCKET` is set), runs `--check-codex`, then execs `run.sh`. It must never install or replace the Codex binary. Diagnostics must go to stderr, because stdout is the MCP stream.
- **Versions:** the binary's version comes from the git tag (`-X main.version`, set by GoReleaser and by `git describe` in `make build` / `build-plugin.sh`). Archives have no version in their names, so `run.sh` installs from `releases/latest/download/`, and `update.go` checks `releases/latest` once a day and replaces the binary. Builds whose version is `dev` or a `git describe` string are never replaced. The plugin version is separate: see [Versions](#versions) below.

## Versions

coai has two versions, and they change independently.

- **Plugin version:** the `version` field in `.claude-plugin/plugin.json` and `.codex-plugin/plugin.json`. Claude Code and Codex install a plugin update only when this string changes.
- **Binary version:** taken from the git tag at build time (`-X main.version`). It is never written in the source. Installed binaries update themselves from `releases/latest` once a day.
- **Required binary:** `scripts/requires-coai` names the oldest coai release the plugin works with. Before starting coai, `run.sh` runs `coai update` when the installed binary is older, instead of waiting for the daily check.

`scripts/check-versions.sh` enforces the rules below and runs in CI on every push and pull request. Run it before pushing.

### When to bump the plugin version

Bump it when a change touches a file the plugins run or read at runtime. The authoritative list is `PLUGIN_FILES` in `scripts/check-versions.sh`:

- `.claude-plugin/plugin.json` and `.codex-plugin/plugin.json` (other than the version itself)
- `.claude-mcp.json`
- `scripts/run.sh`, `scripts/run-codex.sh` and `scripts/requires-coai`
- `skills/`

Don't bump it for Go code, tests, the README, `AGENTS.md`, CI, or the development scripts (`build-plugin.sh`, `check-versions.sh`, `release.sh`). Those reach users as a new binary or not at all. If you add a file the plugins use at runtime, add it to `PLUGIN_FILES`.

How to bump:

- Use `scripts/release.sh plugin X.Y.Z`. It sets the version in both manifests, runs the checks, commits, and tags the commit `plugin-vX.Y.Z`. Push the commit and the tag yourself.
- Keep both manifests at the same version.
- Never add `version` to the entry in `.claude-plugin/marketplace.json`: the manifest overrides it, so a second copy can only drift.
- Bump once per push, not once per commit. If `git diff origin/main -- .claude-plugin/plugin.json` already shows a bump, leave it. Bump in the same commit as the plugin change, or in a follow-up commit before pushing.
- Use semver:
  - **Patch:** fixes and wording changes in scripts or skills.
  - **Minor:** a new skill, monitor, hook or MCP server, a new launcher behaviour, or a raised `requires-coai`.
  - **Major:** renaming or removing a skill or another component users invoke.

### When to raise `requires-coai`

Raise it when a plugin file starts using something the binary gained in a later release: a subcommand, a flag, a tool, or a line format the monitor or skills depend on. Don't raise it just because a newer binary exists; the daily self-update already delivers that.

Raising it changes `scripts/requires-coai`, so the plugin version goes up too. In order:

1. Merge the binary change and release it: `scripts/release.sh binary X.Y.Z`. This tags, pushes the tag and waits until GoReleaser has published the archives.
2. Release the plugin with the new requirement: `scripts/release.sh plugin A.B.C --requires X.Y.Z`, then push.

Doing it the other way round would ship a plugin that requires a binary nobody can download. `check-versions.sh` fails if `requires-coai` is newer than the newest release tag.

### Releasing the binary

- Use `scripts/release.sh binary X.Y.Z` (or tag `vX.Y.Z` and push the tag). GoReleaser builds and publishes the archives. Don't edit any file to set the binary version.
- Don't tag a binary release for a plugin-only change, and don't bump the plugin for a binary-only change.
- Tags: `vX.Y.Z` is a binary release and triggers GoReleaser. `plugin-vX.Y.Z` only marks a plugin release. Version stamping (`git describe --match 'v[0-9]*'`, and `GORELEASER_CURRENT_TAG` in the release workflow) ignores plugin tags.
