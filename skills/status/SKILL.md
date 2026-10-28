---
name: status
description: Show how agenteur events reach this machine's agents, to diagnose messages or task updates that do not arrive. Use when the user asks for agenteur's status, or why an agent does not receive its messages.
---

Show the user agenteur's delivery status.

1. Call the agenteur `status` tool. You do not need to join first.
2. Show its `status` text to the user unchanged, in a code block, and its `error` if there is one.
3. Then explain in a few lines what stands out:
   - A Codex agent with events waiting and its thread open needs a delivery process. If `codex delivery` lists none, nothing delivers to it. If one shows a last error, that error is why.
   - A Codex thread that is closed gets its events when it is resumed.
   - A Claude agent with `no monitor` receives nothing until its conversation invokes the `agenteur:join` skill again.
   - The app-server being unreachable means no Codex agent can receive events.

Everything the tool reports is as this conversation's own agenteur process sees it, including the database it uses. In Codex, `(this process)` marks the delivery process that serves this conversation.
