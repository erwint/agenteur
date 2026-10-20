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

// sessionStart runs as the plugin's SessionStart hook. When Claude Code
// resumes a conversation whose agent is still in a session, it submits the
// join skill again: a monitor started by a skill does not survive a restart,
// and invoking the skill starts agenteur watch anew.
func sessionStart(b *Broker, in io.Reader, out io.Writer) error {
	var hook struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
	}
	if err := json.NewDecoder(in).Decode(&hook); err != nil {
		return err
	}
	if hook.Source != "resume" || hook.SessionID == "" {
		return nil
	}
	agents, err := b.ClaudeAgents(hook.SessionID, 0)
	if err != nil || len(agents) == 0 {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":      "SessionStart",
		"initialUserMessage": fmt.Sprintf("/agenteur:join %s %s", agents[0].Session, agents[0].ID),
	}})
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
