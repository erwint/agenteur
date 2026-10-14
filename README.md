# coai coordinator

`coai` is a local, shared MCP coordination server for Claude Code, Codex CLI, and other MCP clients. Each client gets its own MCP process, while all processes share one SQLite database (WAL mode). Tasks and messages are durable; `await_task` blocks until another agent publishes a result.

## Build and release

Requires Go 1.27 or newer.

```bash
make test
make lint
make build
```

Releases are built by GoReleaser through GitHub Actions. The binary's version comes from the tag, and the plugin has its own version in its manifests:

```bash
scripts/release.sh binary 0.4.0                    # tag, push, wait for the archives
scripts/release.sh plugin 0.5.0 --requires 0.4.0   # bump both manifests, commit, tag plugin-v0.5.0; then push
```

See "Versions" in `AGENTS.md` for when each one changes.

The release workflow uses the repository's `GITHUB_TOKEN`; no personal token is required.

## Install as a plugin

The repository is a plugin marketplace, `erwint-tools`, for both Claude Code and Codex.

Claude Code (inside a session):

```text
/plugin marketplace add erwint/coai
/plugin install coai@erwint-tools
```

Codex CLI:

```bash
codex plugin marketplace add erwint/coai
codex plugin add coai@erwint-tools
```

To update, run `/plugin update coai@erwint-tools` in Claude Code, or `codex plugin marketplace upgrade erwint-tools` followed by `codex plugin add coai@erwint-tools` for Codex. Both install a new plugin version only when its version number changes.

On first launch the plugin installs the latest release binary to `~/.local/bin/coai`. The Claude and Codex plugins share that binary. After that, coai checks for a newer release at most once a day and replaces itself, so new sessions use the new version; set `COAI_NO_UPDATE=1` to turn this off, or run `coai update` by hand. No repository clone or local Go toolchain is required.

In Claude Code, `/coai:join <session> <agent-id>` joins a session and starts event delivery. The Codex plugin's launcher starts the managed app-server daemon and verifies its handshake before exposing MCP tools; registering a bare `coai` with `codex mcp add` skips that, so Codex would not receive events.

If you are developing locally, build the bundled MCP binary and install from the clone:

```bash
git clone git@github.com:erwint/coai.git
cd coai
./scripts/build-plugin.sh
codex plugin marketplace add "$PWD"      # or, in Claude Code: /plugin marketplace add ./
```

Both clients share the same coordination database; set `COAI_STATE` explicitly when sessions need a non-default location.

## Build and configure

```bash
go build -o coai .
codex mcp add coai -- /absolute/path/coai
claude mcp add --transport stdio coai -- /absolute/path/coai
```

Set `COAI_STATE` to share a database explicitly (the default is `$XDG_CONFIG_HOME/coai/state.json` or the platform user config directory; the filename is retained for compatibility). Keep it on a local filesystem; the database is created with private permissions.

Agents should first call `join_session` with a common session name and unique agent ID. Codex agents should then call `register_codex_thread` with their app-server `thread_id` and optional socket path. Use `create_task`, `send_message`, `ack_message`, `await_task`, `complete_task`, `claim_files`, `release_files`, and `list_session` to coordinate. `await_task` is cancellation-aware and uses durable polling, so it works across separately spawned MCP processes. When a client supplies an MCP progress token, it also receives a heartbeat before the durable wait.

## Event delivery

Agents are woken for three kinds of events: a message addressed to them, a task assigned to them, and the outcome (completed, failed, cancelled) of a task they created. Each event is delivered once. The `notified` columns in the database record delivery across all processes, and an event that fails to deliver goes back into the queue. Events are one line each and describe themselves, e.g. `coai message <id> in session "demo" from codex-1: "please review main.go" (...)`. The server's MCP instructions tell agents how to respond.

**Claude Code.** Run the plugin's `join` skill (`/coai:join [session] [agent-id]`). Invoking it starts the plugin monitor `coai watch`. Claude Code turns each line the monitor prints into a notification that wakes the session, with no `--channels` flag. The monitor and the MCP server share the session's `CLAUDE_CODE_SESSION_ID`, which `join_session` records so the monitor finds its agent. Monitors run only in interactive sessions. While the monitor runs, Claude Code lists it as background work when you exit.

## Codex app-server

The Codex plugin runs `scripts/run-codex.sh`, which starts the managed daemon with `codex app-server daemon start` and verifies the app-server handshake. Codex must already have its managed standalone installation; CoAI does not install or replace the Codex executable. If startup fails, the plugin reports the error instead of claiming push delivery is active. A custom `COAI_CODEX_SOCKET` skips daemon startup and verifies that socket instead.

Call `join_session` with `kind: "codex"`. CoAI registers `CODEX_THREAD_ID` automatically when inherited from the client; otherwise supply `thread_id` explicitly or use `register_codex_thread`. Registration checks that the thread is readable through the app-server. Check `codex_registered` and `codex_error` in the join result. Installing a plugin alone does not identify which shared coordination session an agent should join.

The Codex agent's own CoAI process delivers its events to the registered thread with `thread/queue/add`. An idle thread starts a turn at once, and a busy one runs the event after its current turn. Ephemeral threads have no queue, so events are appended there with `thread/inject_items` and wait for the next turn. If the app-server is unreachable, delivery is retried with backoff, and the data stays available through `inbox`, `await_task`, and `list_session`.

To verify a connection without sending a message:

```bash
./scripts/run.sh --check-codex
```

For local plugin development, run `./scripts/build-plugin.sh` before installing the plugin directory so it includes the updated `bin/coai`. The release downloader is pinned to the published release; it does not download uncommitted development changes.
