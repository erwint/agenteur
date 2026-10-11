# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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

- Claude: `.claude-plugin/plugin.json` points to `.claude-mcp.json`, which runs `scripts/run.sh`, and declares the `events` monitor (`run.sh watch`). `run.sh` runs `bin/coai` if a local build exists. Otherwise it runs `~/.local/bin/coai`, which is shared by both plugins, installing the latest release there on first use. It deliberately ignores any other `coai` on `PATH`, since pre-0.3 releases lack `watch`.
- Codex: `.codex-plugin/plugin.json` runs `scripts/run-codex.sh`. That script starts `codex app-server daemon start` (unless `COAI_CODEX_SOCKET` is set), runs `--check-codex`, then execs `run.sh`. It must never install or replace the Codex binary. Diagnostics must go to stderr, because stdout is the MCP stream.
- **Versions are not written in the source.** The binary's `version` is stamped from the git tag (`-X main.version`, by GoReleaser and by `git describe` in `make build` / `build-plugin.sh`), so releasing is only tagging. Archives have no version in their names, so `run.sh` installs from `releases/latest/download/` and `update.go` checks `releases/latest` once a day and replaces the binary. Builds whose version is `dev` or a `git describe` string are never replaced. The Claude manifest and marketplace entry leave out `version`, so plugin updates track commits. `.codex-plugin/plugin.json` has to keep one because Codex names its cache by it: bump it when the plugin files change.
