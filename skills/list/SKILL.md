---
name: list
description: Show the user which agents take part in an agenteur coordination session, with their roles, or list every session when none is named. Use when the user asks who is in a session, which agents are connected, or which sessions exist.
argument-hint: "[session]"
---

Show the participants of an agenteur session.

1. Use the session given as an argument. Otherwise use the session this conversation joined, if any. Otherwise list every session.
2. Call the agenteur `list_agents` tool with `session`, or with no session to list every session. You do not need to join first.
3. Show the result to the user as a table per session: agent ID, kind, role, and when it joined. Mark this conversation's own agent. For Claude agents, `events_monitor: false` means no conversation is currently listening for that agent's events, usually because its conversation ended without leaving; say so in a final column or note.

If the named session has no agents, say so and list the sessions that do exist.
