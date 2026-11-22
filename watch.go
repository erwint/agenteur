package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// watch runs as a Claude Code plugin monitor. Claude Code turns each line it
// prints into a notification that wakes the session, so it prints nothing but
// events. Claude Code starts monitors without CLAUDE_PLUGIN_* variables but
// with the session's own CLAUDE_CODE_SESSION_ID and CLAUDE_PID, which
// join_session records for the agent.
func watch(b *Broker) error {
	sid := os.Getenv("CLAUDE_CODE_SESSION_ID")
	pid, _ := strconv.Atoi(os.Getenv("CLAUDE_PID"))
	if sid == "" && pid == 0 {
		return errors.New("agenteur watch: not running inside a Claude Code session")
	}
	// Report a closed stdout as a write error instead of dying on SIGPIPE, so
	// the event that failed to print is returned to the queue.
	signal.Ignore(syscall.SIGPIPE)
	for {
		if err := watchOnce(b, sid, pid, os.Stdout); err != nil && isWriteErr(err) {
			return err
		}
		time.Sleep(time.Second)
	}
}

// sessionStart runs as the plugins' SessionStart hook for resumed
// conversations, whose agent is still in a session but whose new process has
// not joined, and asks the agent to invoke the join skill again.
//
// Claude Code: a monitor started by a skill does not survive a restart, and
// joining starts agenteur watch anew. The hook is an asyncRewake hook: what it
// writes to wakeOut reaches the model at once, and wake reports that it
// should, which the caller signals with exit code 2. Interactive Claude Code
// ignores a SessionStart initialUserMessage, so that cannot do it.
//
// Codex: the hook's session_id is the thread ID. A SessionStart hook cannot
// start a turn, so the request goes to contextOut as additionalContext, which
// the model reads in the thread's next turn. Joining makes the thread's new
// agenteur process deliver to it.
func sessionStart(b *Broker, in io.Reader, wakeOut, contextOut io.Writer) (wake bool, err error) {
	var hook struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
	}
	if err := json.NewDecoder(in).Decode(&hook); err != nil {
		return false, err
	}
	if hook.Source != "resume" || hook.SessionID == "" {
		return false, nil
	}
	agents, err := b.ClaudeAgents(hook.SessionID, 0)
	if err != nil {
		return false, err
	}
	if len(agents) > 0 {
		a := agents[0]
		_, err = fmt.Fprintf(wakeOut, "This conversation was resumed. Its agent %q is still in agenteur session %q, but the event monitor stopped with the old process, so no messages or task updates reach it. Invoke the agenteur:join skill now with the arguments `%s %s` and no role, which keeps the current one, then tell the user in one line that you rejoined.\n", a.ID, a.Session, a.Session, a.ID)
		return err == nil, err
	}
	a, ok, err := b.ThreadAgent(hook.SessionID)
	if err != nil || !ok {
		return false, err
	}
	msg := fmt.Sprintf("This Codex thread was resumed. It is agent %q in agenteur session %q, but its new agenteur process has not joined, so messages and task updates for it may not arrive. Before anything else in your next turn, invoke the agenteur:join skill with the arguments `%s %s` and no role, which keeps the current one, then tell the user in one line that you rejoined.", a.ID, a.Session, a.Session, a.ID)
	return false, json.NewEncoder(contextOut).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "SessionStart", "additionalContext": msg}})
}

type writeErr struct{ error }

func isWriteErr(err error) bool {
	var w writeErr
	return errors.As(err, &w)
}

func watchOnce(b *Broker, sid string, pid int, w io.Writer) error {
	// Lets join_session tell whether this session will see its events.
	if err := b.WatcherSeen(sid, pid); err != nil {
		return err
	}
	agents, err := b.ClaudeAgents(sid, pid)
	if err != nil {
		return err
	}
	for _, a := range agents {
		events, err := b.PendingEvents(a.Session, a.ID)
		if err != nil {
			return err
		}
		for _, e := range events {
			if ok, err := b.ClaimEvent(e); err != nil || !ok {
				continue
			}
			if _, err := fmt.Fprintln(w, e.Line()); err != nil {
				_ = b.UnclaimEvent(e)
				return writeErr{err}
			}
		}
	}
	return nil
}
