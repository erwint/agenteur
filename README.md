# agenteur

`agenteur` lets coding agents work together. Claude Code, Codex CLI and other MCP clients join a shared session, send each other messages and tasks, and lease files, and each agent is woken when something arrives for it. Every client runs its own MCP process; all of them share one local SQLite database (WAL mode). Tasks and messages are durable, and `await_task` blocks until another agent publishes a result.

agenteur was called coai until version 1.0. See [Moving from coai](#moving-from-coai).

## Install

The repository is a plugin marketplace named `agenteur` for both Claude Code and Codex. No repository clone or Go toolchain is needed.

Claude Code (inside a session):

```text
/plugin marketplace add erwint/agenteur
/plugin install agenteur@agenteur
```

Codex CLI (0.162 or newer):

```bash
codex plugin marketplace add erwint/agenteur
codex plugin add agenteur@agenteur
```

Restart open sessions afterwards. Register agenteur through the plugin, not with `codex mcp add`: the plugin's launcher starts the managed Codex app-server daemon and checks its handshake, without which Codex receives no events.

The first time either plugin starts, it downloads the latest release binary to `~/.local/bin/agenteur` (macOS or Linux, amd64 or arm64; needs `curl` and `tar`). Both plugins share that binary, and both clients share one coordination database; set `AGENTEUR_STATE` when sessions need a non-default location.

## Join a session

Claude Code:

```text
/agenteur:join <session> <agent-id> [role]
```

Codex has no slash commands for plugin skills. Mention the skill instead, or ask in plain words ("join agenteur session myproject as codex-api"):

```text
$agenteur:join <session> <agent-id> [role]
```

Anything after the agent ID is the agent's role, such as `/agenteur:join myproject claude-web owns the web frontend`. Roles are optional; the other agents see them in `join_session` and `list_session` results and use them to decide whom to ask or assign work to. Agents already in the session are told when another agent joins, changes its role or leaves.

From then on, messages and task updates for the agent arrive by themselves. In Codex, the join result should say `codex_registered: true`; otherwise events are not delivered and the agent has to check `inbox`.

To see who is in a session, use `/agenteur:list [session]` in Claude Code or `$agenteur:list [session]` in Codex. It shows each agent's kind, role and join time, and works without joining. Without a session name it shows the session you joined, or every session if you haven't joined one. For Claude agents it also shows whether their event monitor is running; an agent whose conversation ended without leaving is still listed but no longer receives events.

To leave, use `/agenteur:leave` in Claude Code or `$agenteur:leave` in Codex. The agent is removed from the session, its file leases are released, and nothing more is delivered to it. Its messages and tasks stay in the session's history.

## Update

The plugin and the binary update separately.

Plugin, in Claude Code:

```text
/plugin marketplace update agenteur
/plugin update agenteur@agenteur
```

Plugin, in Codex:

```bash
codex plugin marketplace upgrade agenteur
codex plugin add agenteur@agenteur
```

Both clients install a new plugin only when its version number has changed. Restart open sessions to load it. `/plugin` in Claude Code and `codex plugin list` in Codex show the installed version.

The binary updates itself: agenteur checks for a newer release at most once a day and replaces `~/.local/bin/agenteur`, so new sessions use the new version. Run `~/.local/bin/agenteur update` to update at once, `~/.local/bin/agenteur version` to see the version, or set `AGENTEUR_NO_UPDATE=1` to turn self-updates off. A plugin that needs a newer binary than the one installed updates it before starting.

## Moving from coai

coai's last release turned every installed coai into agenteur: `~/.local/bin/coai` now runs agenteur, so sessions started by the coai plugins keep working, and their join results tell the agent how to switch. Switching takes one step per client:

Claude Code:

```text
/plugin uninstall coai@coai
/plugin marketplace remove coai
/plugin marketplace add erwint/agenteur
/plugin install agenteur@agenteur
```

Codex:

```bash
codex plugin remove coai@coai
codex plugin marketplace remove coai
codex plugin marketplace add erwint/agenteur
codex plugin add agenteur@agenteur
```

Remove the coai plugin rather than keeping both: each would start its own server with the same tools. Sessions, agents, messages and tasks carry over, because agenteur keeps using coai's database (`<config>/coai/state.json`) on machines that have one. The `COAI_STATE`, `COAI_CODEX_SOCKET` and `COAI_NO_UPDATE` settings keep working; their `AGENTEUR_` names take precedence. Event lines now start with `agenteur` instead of `coai`.

## Use without the plugins

```bash
go build -o agenteur .
codex mcp add agenteur -- /absolute/path/agenteur
claude mcp add --transport stdio agenteur -- /absolute/path/agenteur
```

This gives an agent the tools but no event delivery: Claude Code needs the plugin's monitor and Codex the plugin's launcher for that.

Set `AGENTEUR_STATE` to share a database explicitly. The default is `agenteur/state.db` in `$XDG_CONFIG_HOME` or the platform's user config directory, or coai's `coai/state.json` there on machines that ran coai. Keep it on a local filesystem; the database is created with private permissions.

Agents first call `join_session` with a common session name, a unique agent ID and optionally a role. Use `leave_session`, `create_task`, `send_message`, `ack_message`, `await_task`, `complete_task`, `claim_files`, `release_files`, `list_agents`, and `list_session` to coordinate. `await_task` is cancellation-aware and uses durable polling, so it works across separately spawned MCP processes. When a client supplies an MCP progress token, it also receives a heartbeat before the durable wait.

## Event delivery

Agents are woken for a message addressed to them, a task assigned to them, the outcome (completed, failed, cancelled) of a task they created, and another agent joining, changing its role or leaving. Each event is delivered once. The `notified` columns in the database record delivery across all processes, and an event that fails to deliver goes back into the queue. Events are one line each and describe themselves, e.g. `agenteur message <id> in session "demo" from codex-1: "please review main.go" (...)`. The server's MCP instructions tell agents how to respond.

**Claude Code.** Run the plugin's `join` skill (`/agenteur:join`). Invoking it starts the plugin monitor `agenteur watch`. Claude Code turns each line the monitor prints into a notification that wakes the session, with no `--channels` flag. The monitor and the MCP server share the session's `CLAUDE_CODE_SESSION_ID`, which `join_session` records so the monitor finds its agent. Monitors run only in interactive sessions. While the monitor runs, Claude Code lists it as background work when you exit. If no monitor runs when a Claude agent joins, the join result says so.

**Codex.** The Codex plugin runs `scripts/run-codex.sh`, which starts the managed daemon with `codex app-server daemon start` and verifies the app-server handshake. Codex must already have its managed standalone installation; agenteur does not install or replace the Codex executable. A custom `AGENTEUR_CODEX_SOCKET` skips daemon startup and verifies that socket instead.

`join_session` registers the calling Codex thread by itself: Codex passes the thread with every tool call. Registration checks that the thread is readable through the app-server; check `codex_registered` and `codex_error` in the join result, and use `register_codex_thread` to register a thread explicitly.

The Codex agent's own agenteur process delivers its events to the registered thread with `thread/queue/add`. An idle thread starts a turn at once, and a busy one runs the event after its current turn. Ephemeral threads have no queue, so events are appended there with `thread/inject_items` and wait for the next turn. If the app-server is unreachable, delivery is retried with backoff, and the data stays available through `inbox`, `await_task`, and `list_session`.

To verify a connection without sending a message:

```bash
./scripts/run.sh --check-codex
```

## Develop

Requires Go 1.27 or newer.

```bash
make test
make lint
make build
```

To try local changes through the plugins, build the bundled binary and install from the clone; the plugins then run `bin/agenteur` instead of the release binary:

```bash
git clone git@github.com:erwint/agenteur.git
cd agenteur
./scripts/build-plugin.sh
codex plugin marketplace add "$PWD"      # or, in Claude Code: /plugin marketplace add ./
```

Releases are built by GoReleaser through GitHub Actions with the repository's `GITHUB_TOKEN`. The binary's version comes from the tag, and the plugin has its own version in its manifests:

```bash
scripts/release.sh binary 1.1.0                    # tag, push, wait for the archives
scripts/release.sh plugin 1.1.0 --requires 1.1.0   # bump both manifests, commit, tag plugin-v1.1.0; then push
```

See "Versions" in `AGENTS.md` for when each one changes.
