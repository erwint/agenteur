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

// sessionStart runs as the plugin's SessionStart hook. A monitor started by a
// skill does not survive a restart, so when Claude Code resumes a conversation
// whose agent is still in a session, it asks the agent to invoke the join
// skill again, which starts agenteur watch anew. The hook is an asyncRewake
// hook: what it writes to out reaches the model, and wake reports that it
// should, which the caller signals with exit code 2. Interactive Claude Code
// ignores a SessionStart initialUserMessage, so that cannot do it.
func sessionStart(b *Broker, in io.Reader, out io.Writer) (wake bool, err error) {
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
	if err != nil || len(agents) == 0 {
		return false, err
	}
	a := agents[0]
	_, err = fmt.Fprintf(out, "This conversation was resumed. Its agent %q is still in agenteur session %q, but the event monitor stopped with the old process, so no messages or task updates reach it. Invoke the agenteur:join skill now with the arguments `%s %s` and no role, which keeps the current one, then tell the user in one line that you rejoined.\n", a.ID, a.Session, a.Session, a.ID)
	return err == nil, err
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
