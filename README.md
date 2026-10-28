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

A resumed conversation keeps its place in the session: after `claude --resume` the agent is woken and joins again by itself, and `codex resume` receives the events that waited while it was closed. Start a new conversation and it joins as a new agent.

To leave, use `/agenteur:leave` in Claude Code or `$agenteur:leave` in Codex. The agent is removed from the session, its file leases are released, and nothing more is delivered to it. Its messages and tasks stay in the session's history.

## Update

The plugin and the binary update separately.

Codex updates the plugin by itself: each time it starts, it upgrades git marketplaces and reinstalls plugins whose version changed. Claude Code does so only once auto-update is on for the marketplace, which it is not by default for marketplaces you add: open `/plugin`, Manage marketplaces, `agenteur`, Enable auto-update. Or set it in `~/.claude/settings.json`, which is easy to copy between machines:

```json
"extraKnownMarketplaces": {
  "agenteur": {
    "source": { "source": "github", "repo": "erwint/agenteur" },
    "autoUpdate": true
  }
}
```

To update the plugin by hand, in Claude Code:

```text
/plugin marketplace update agenteur
/plugin update agenteur@agenteur
```

In Codex:

```bash
codex plugin marketplace upgrade agenteur
codex plugin add agenteur@agenteur
```

Both clients install a new plugin only when its version number has changed. Restart open sessions to load it. `/plugin` in Claude Code and `codex plugin list` in Codex show the installed version.

The binary updates itself: agenteur checks for a newer release at most once a day and replaces `~/.local/bin/agenteur`, so new sessions use the new version. Run `~/.local/bin/agenteur update` to update at once, `~/.local/bin/agenteur version` to see the version, or set `AGENTEUR_NO_UPDATE=1` to turn self-updates off. A plugin that needs a newer binary than the one installed updates it before starting.

## Moving from coai

coai is now agenteur, with a new repository, marketplace and plugin: `coai@coai` from `erwint/coai` becomes `agenteur@agenteur` from `erwint/agenteur`. Neither Claude Code nor Codex moves an installed plugin to another marketplace, and the coai plugin gets no further updates, so each machine switches once.

The binary has already moved by itself: coai's last release installs agenteur as `~/.local/bin/coai`, so sessions started by the coai plugins keep working. When you join a session through the coai plugin, the agent offers to switch the plugins for you and runs the commands below once you agree.

To switch by hand, in a shell (install agenteur first, so a failed step leaves a working plugin):

```bash
# Claude Code
claude plugin marketplace add erwint/agenteur
claude plugin install agenteur@agenteur
claude plugin uninstall coai@coai
claude plugin marketplace remove coai

# Codex
codex plugin marketplace add erwint/agenteur
codex plugin add agenteur@agenteur
codex plugin remove coai@coai
codex plugin marketplace remove coai
```

Or inside Claude Code: `/plugin marketplace add erwint/agenteur`, `/plugin install agenteur@agenteur`, `/plugin uninstall coai@coai`, `/plugin marketplace remove coai`.

Then restart open sessions and join again with `/agenteur:join` (Claude Code) or `$agenteur:join` (Codex). The skills are now `agenteur:join`, `agenteur:leave` and `agenteur:list`. To have Claude Code keep the plugin up to date, turn on auto-update under `/plugin`, Manage marketplaces, `agenteur`, Enable auto-update; Codex upgrades git marketplaces each time it starts.

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

**Claude Code.** Run the plugin's `join` skill (`/agenteur:join`). Invoking it starts the plugin monitor `agenteur watch`. Claude Code turns each line the monitor prints into a notification that wakes the session, with no `--channels` flag. The monitor and the MCP server share the session's `CLAUDE_CODE_SESSION_ID`, which `join_session` records so the monitor finds its agent. Monitors run only in interactive sessions. While the monitor runs, Claude Code lists it as background work when you exit. If no monitor runs when a Claude agent joins, the join result says so. A monitor started by a skill does not come back when a conversation is resumed, so the plugin's `SessionStart` hook (`agenteur session-start`, for `resume` only) wakes a resumed conversation whose agent is still in a session and asks it to invoke the `join` skill again. The terminal shows this as "Stop hook feedback".

**Codex.** The Codex plugin runs `scripts/run-codex.sh`, which verifies the app-server handshake and, when no daemon answers, starts the managed daemon with `codex app-server daemon start` first. Codex must already have its managed standalone installation; agenteur does not install or replace the Codex executable. A custom `AGENTEUR_CODEX_SOCKET` skips daemon startup and verifies that socket instead.

`join_session` registers the calling Codex thread by itself: Codex passes the thread with every tool call. Registration checks that the thread is readable through the app-server; check `codex_registered` and `codex_error` in the join result, and use `register_codex_thread` to register a thread explicitly.

The Codex agent's own agenteur process delivers its events to the registered thread with `thread/queue/add`. A resumed thread keeps its ID but gets a new agenteur process, which has not joined. Until it does, that process delivers for every registered thread the app-server lists as loaded (`thread/loaded/list`), which are the threads open in some Codex session. An idle thread starts a turn at once, and a busy one runs the event after its current turn. Ephemeral threads have no queue, so events are appended there with `thread/inject_items` and wait for the next turn. If the app-server is unreachable, delivery is retried with backoff, and the data stays available through `inbox`, `await_task`, and `list_session`.

## Status

When events do not arrive, use `/agenteur:status` in Claude Code or `$agenteur:status` in Codex, or run `~/.local/bin/agenteur status` in a shell. It shows the version and database in use, whether the Codex app-server answers and which threads it has open, the agenteur processes delivering Codex events (with their last error), and for each agent whether its monitor or thread is up and how many events wait for it. The skill reports what the conversation's own agenteur process sees; in Codex that process runs inside the app-server daemon, whose environment can differ from your shell's.

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
