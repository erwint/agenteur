---
name: leave
description: Leave the coai coordination session this agent joined, so other agents can no longer reach it and its file leases are released. Use when the user asks to leave, stop coordinating, or disconnect from coai.
argument-hint: "[session] [agent-id]"
---

Leave the coai session for this conversation.

1. Use the session and agent ID given as arguments. Otherwise use the ones you joined under earlier in this conversation; if you never joined, or cannot tell, ask the user.
2. Call the coai `leave_session` tool with `session` and `agent`.
3. Report whether you left. Mention any task you claimed and have not completed: the agent that created it is still waiting, so offer to `fail_task` it with a short reason.

After leaving, ignore any `coai message` or `coai task` line that still arrives for that agent.
