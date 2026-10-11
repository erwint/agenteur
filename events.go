package main

import (
	"database/sql"
	"fmt"
	"strconv"
)

// Event is something an agent should be woken for: a message addressed to
// it, a task assigned to it, or a task it requested reaching a final state.
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
		return "coai-task-" + e.ID + "-" + e.Status
	}
	return "coai-message-" + e.ID
}

// Line renders the event as a single self-describing line: Claude monitors
// deliver one notification per stdout line.
func (e Event) Line() string {
	switch {
	case e.Kind == "message":
		return fmt.Sprintf("coai message %s in session %q from %s: %s (reply with send_message; ack_message when handled)", e.ID, e.Session, e.From, strconv.Quote(e.Text))
	case e.Status == "pending":
		return fmt.Sprintf("coai task %s in session %q assigned to you by %s: %s (claim_task, then complete_task or fail_task)", e.ID, e.Session, e.From, strconv.Quote(e.Text))
	default:
		return fmt.Sprintf("coai task %s in session %q %s by %s: %s", e.ID, e.Session, e.Status, e.From, strconv.Quote(e.Text))
	}
}

// PendingEvents lists undelivered events for one agent, oldest first.
func (b *Broker) PendingEvents(session, agent string) ([]Event, error) {
	var out []Event
	rows, err := b.db.Query(`SELECT id,sender,text FROM messages WHERE session=? AND recipient=? AND notified=0 AND acked=0 ORDER BY created`, session, agent)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e := Event{Kind: "message", Session: session}
		if err = rows.Scan(&e.ID, &e.From, &e.Text); err != nil {
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
	if e.Kind == "message" {
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
	if e.Kind == "message" {
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
