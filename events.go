package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// Event is something an agent should be woken for: a message addressed to
// it, a task assigned to it, a task it requested reaching a final state, or
// another agent joining, changing its role or leaving (a notice).
// Events are derived from the messages and tasks tables; the notified columns
// record delivery so that every event is pushed once across all processes.
type Event struct {
	Kind, ID, Session, From, Status, Text string
	// prev is the task's notified value before ClaimEvent, restored by UnclaimEvent.
	prev string
}

// Key identifies one delivery of an event, so a task produces distinct keys
// for its assignment and its completion.
func (e Event) Key() string {
	if e.Kind == "task" {
		return "agenteur-task-" + e.ID + "-" + e.Status
	}
	return "agenteur-" + e.Kind + "-" + e.ID
}

// Line renders the event as a single self-describing line: Claude monitors
// deliver one notification per stdout line.
func (e Event) Line() string {
	switch {
	case e.Kind == "message":
		return fmt.Sprintf("agenteur message %s in session %q from %s: %s (reply with send_message; ack_message when handled)", e.ID, e.Session, e.From, strconv.Quote(e.Text))
	case e.Kind == noticeJoined && e.Text == "":
		return fmt.Sprintf("agenteur agent %s joined session %q (no role given)", e.From, e.Session)
	case e.Kind == noticeJoined:
		return fmt.Sprintf("agenteur agent %s joined session %q with role: %s", e.From, e.Session, strconv.Quote(e.Text))
	case e.Kind == noticeRole:
		return fmt.Sprintf("agenteur agent %s in session %q changed its role to: %s", e.From, e.Session, strconv.Quote(e.Text))
	case e.Kind == noticeLeft:
		return fmt.Sprintf("agenteur agent %s left session %q", e.From, e.Session)
	case e.Status == "pending":
		return fmt.Sprintf("agenteur task %s in session %q assigned to you by %s: %s (claim_task, then complete_task or fail_task)", e.ID, e.Session, e.From, strconv.Quote(e.Text))
	default:
		return fmt.Sprintf("agenteur task %s in session %q %s by %s: %s", e.ID, e.Session, e.Status, e.From, strconv.Quote(e.Text))
	}
}

// PendingEvents lists undelivered events for one agent, oldest first.
func (b *Broker) PendingEvents(session, agent string) ([]Event, error) {
	var out []Event
	// Notices are stored acknowledged, so they never wait in an inbox.
	rows, err := b.db.Query(`SELECT id,kind,sender,text FROM messages WHERE session=? AND recipient=? AND notified=0 AND (acked=0 OR kind<>'message') ORDER BY created`, session, agent)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e := Event{Session: session}
		if err = rows.Scan(&e.ID, &e.Kind, &e.From, &e.Text); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	rows.Close()
	rows, err = b.db.Query(`SELECT id,sender,recipient,description,status,result,notified FROM tasks WHERE session=? AND notified<>status AND ((status='pending' AND recipient=?) OR (status IN ('completed','failed','cancelled') AND sender=?)) ORDER BY updated`, session, agent, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var from, to, desc, result string
		e := Event{Kind: "task", Session: session}
		if err = rows.Scan(&e.ID, &from, &to, &desc, &e.Status, &result, &e.prev); err != nil {
			return nil, err
		}
		// Assignments come from the requester; outcomes from the worker.
		e.From, e.Text = from, desc
		if e.Status != "pending" {
			e.From, e.Text = to, result
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClaimEvent marks an event delivered. It reports false when another process
// claimed it first or the task has changed state since it was listed.
func (b *Broker) ClaimEvent(e Event) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var r sql.Result
	var err error
	if e.Kind != "task" {
		r, err = b.db.Exec(`UPDATE messages SET notified=1 WHERE id=? AND notified=0`, e.ID)
	} else {
		r, err = b.db.Exec(`UPDATE tasks SET notified=status WHERE id=? AND status=? AND notified=?`, e.ID, e.Status, e.prev)
	}
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}

// UnclaimEvent returns an event to the queue after a failed delivery.
func (b *Broker) UnclaimEvent(e Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var err error
	if e.Kind != "task" {
		_, err = b.db.Exec(`UPDATE messages SET notified=0 WHERE id=?`, e.ID)
	} else {
		_, err = b.db.Exec(`UPDATE tasks SET notified=? WHERE id=? AND notified=?`, e.prev, e.ID, e.Status)
	}
	return err
}

// ClaudeAgents returns the agents joined from one Claude Code session,
// matched by session ID or, as a fallback, by the Claude process ID.
func (b *Broker) ClaudeAgents(sessionID string, pid int) ([]Agent, error) {
	rows, err := b.db.Query(`SELECT session,id FROM agents WHERE (claude_session=? AND ?<>'') OR (claude_pid=? AND ?<>0)`, sessionID, sessionID, pid, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err = rows.Scan(&a.Session, &a.ID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CodexAgents returns the agents registered with a Codex thread.
func (b *Broker) CodexAgents() ([]Agent, error) {
	rows, err := b.db.Query(`SELECT session,id,thread_id,COALESCE(socket,'') FROM agents WHERE COALESCE(thread_id,'')<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err = rows.Scan(&a.Session, &a.ID, &a.ThreadID, &a.Socket); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// WatcherSeen records that agenteur watch is running for a Claude session.
func (b *Broker) WatcherSeen(sessionID string, pid int) error {
	_, err := b.db.Exec(`INSERT INTO watchers(claude_session,claude_pid,seen) VALUES(?,?,?) ON CONFLICT(claude_session,claude_pid) DO UPDATE SET seen=excluded.seen`, sessionID, pid, time.Now().Unix())
	return err
}

// Watching reports whether agenteur watch ran for a Claude session within the
// last few seconds. Without it, the session's events are never shown.
func (b *Broker) Watching(sessionID string, pid int) (bool, error) {
	var n int
	err := b.db.QueryRow(`SELECT count(*) FROM watchers WHERE ((claude_session=? AND ?<>'') OR (claude_pid=? AND ?<>0)) AND seen>=?`, sessionID, sessionID, pid, pid, time.Now().Add(-5*time.Second).Unix()).Scan(&n)
	return n > 0, err
}

// Notice kinds, stored in messages.kind next to ordinary messages.
const (
	noticeJoined = "joined"
	noticeRole   = "role"
	noticeLeft   = "left"
)

// notify tells every other agent in a session about one agent. The caller
// holds b.mu.
func (b *Broker) notify(session, agent, kind, text string) error {
	_, err := b.db.Exec(`INSERT INTO messages(id,session,sender,recipient,text,created,acked,notified,kind) SELECT ?||'-'||id,?,?,id,?,?,1,0,? FROM agents WHERE session=? AND id<>?`, id(), session, agent, text, time.Now().UTC().Format(time.RFC3339Nano), kind, session, agent)
	return err
}
