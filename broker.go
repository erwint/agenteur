package main

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Message struct {
	ID, Session, From, To, Text string
	Created                     time.Time
	Acked                       bool
}
type Task struct {
	ID, Session, From, To, Description, Status, Result string
	Created, Updated                                   time.Time
}
type Agent struct {
	ID, Session, Kind, CWD, ThreadID, Socket string
	ClaudeSession                            string `json:",omitempty"`
	ClaudePID                                int    `json:",omitempty"`
	Joined                                   time.Time
}
type state struct {
	Agents   []Agent   `json:"agents"`
	Messages []Message `json:"messages"`
	Tasks    []Task    `json:"tasks"`
	Leases   []Lease   `json:"leases"`
}
type Lease struct {
	Session, Agent, Path string
	Expires              time.Time
}
type Broker struct {
	db *sql.DB
	mu sync.Mutex
}

func NewBroker(path string) *Broker {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		panic(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS agents(session TEXT,id TEXT,kind TEXT,cwd TEXT,thread_id TEXT,socket TEXT,joined TEXT,PRIMARY KEY(session,id)); CREATE TABLE IF NOT EXISTS messages(id TEXT PRIMARY KEY,session TEXT,sender TEXT,recipient TEXT,text TEXT,created TEXT,acked INTEGER DEFAULT 0); CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,session TEXT,sender TEXT,recipient TEXT,description TEXT,status TEXT,result TEXT,created TEXT,updated TEXT); CREATE TABLE IF NOT EXISTS leases(session TEXT,path TEXT,agent TEXT,expires TEXT,PRIMARY KEY(session,path));`); err != nil {
		panic(err)
	}
	if err = migrate(db); err != nil {
		panic(err)
	}
	return &Broker{db: db}
}

// migrate adds columns introduced after the first schema. Rows that predate
// the notified columns count as already delivered, so upgrading does not
// replay a database's history into running sessions.
func migrate(db *sql.DB) error {
	for _, m := range []struct{ table, column, backfill string }{
		{"agents", "claude_session TEXT NOT NULL DEFAULT ''", ""},
		{"agents", "claude_pid INTEGER NOT NULL DEFAULT 0", ""},
		{"messages", "notified INTEGER NOT NULL DEFAULT 0", "UPDATE messages SET notified=1"},
		{"tasks", "notified TEXT NOT NULL DEFAULT ''", "UPDATE tasks SET notified=status"},
	} {
		if _, err := db.Exec(`ALTER TABLE ` + m.table + ` ADD COLUMN ` + m.column); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return err
		}
		if m.backfill != "" {
			if _, err := db.Exec(m.backfill); err != nil {
				return err
			}
		}
	}
	return nil
}
func id() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }
func (b *Broker) Join(a Agent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// A Claude session's watcher follows whichever agent joined from it last.
	if a.ClaudeSession != "" || a.ClaudePID != 0 {
		if _, e := b.db.Exec(`UPDATE agents SET claude_session='',claude_pid=0 WHERE (claude_session=? AND ?<>'') OR (claude_pid=? AND ?<>0)`, a.ClaudeSession, a.ClaudeSession, a.ClaudePID, a.ClaudePID); e != nil {
			return e
		}
	}
	_, e := b.db.Exec(`INSERT INTO agents(session,id,kind,cwd,thread_id,socket,joined,claude_session,claude_pid) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(session,id) DO UPDATE SET kind=excluded.kind,cwd=excluded.cwd,thread_id=excluded.thread_id,socket=excluded.socket,joined=excluded.joined,claude_session=excluded.claude_session,claude_pid=excluded.claude_pid`, a.Session, a.ID, a.Kind, a.CWD, a.ThreadID, a.Socket, time.Now().UTC().Format(time.RFC3339Nano), a.ClaudeSession, a.ClaudePID)
	return e
}
func (b *Broker) RegisterThread(s, a, t, sk string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.db.Exec(`UPDATE agents SET thread_id=?,socket=? WHERE session=? AND id=?`, t, sk, s, a)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent %s not found", a)
	}
	return nil
}
func (b *Broker) Send(m Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, e := b.db.Exec(`INSERT INTO messages(id,session,sender,recipient,text,created,acked,notified) VALUES(?,?,?,?,?,?,0,0)`, id(), m.Session, m.From, m.To, m.Text, time.Now().UTC().Format(time.RFC3339Nano))
	return e
}
func (b *Broker) Ack(i string) error {
	_, e := b.db.Exec(`UPDATE messages SET acked=1 WHERE id=?`, i)
	return e
}
func (b *Broker) Claim(session, agent, path string, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().UTC()
	exp := now.Add(ttl).Format(time.RFC3339Nano)
	_, _ = b.db.Exec(`DELETE FROM leases WHERE expires < ?`, now.Format(time.RFC3339Nano))
	var owner string
	e := b.db.QueryRow(`SELECT agent FROM leases WHERE session=? AND path=?`, session, path).Scan(&owner)
	if e == nil && owner != agent {
		return fmt.Errorf("path already claimed by %s", owner)
	}
	_, e = b.db.Exec(`INSERT INTO leases(session,path,agent,expires) VALUES(?,?,?,?) ON CONFLICT(session,path) DO UPDATE SET agent=excluded.agent,expires=excluded.expires`, session, path, agent, exp)
	return e
}
func (b *Broker) Release(session, agent, path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, e := b.db.Exec(`DELETE FROM leases WHERE session=? AND path=? AND agent=?`, session, path, agent)
	return e
}
func (b *Broker) Renew(session, agent, path string, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.db.Exec(`UPDATE leases SET expires=? WHERE session=? AND path=? AND agent=?`, time.Now().UTC().Add(ttl).Format(time.RFC3339Nano), session, path, agent)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fmt.Errorf("lease not found")
	}
	return nil
}
func (b *Broker) Start(t Task) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t.ID = id()
	n := time.Now().UTC().Format(time.RFC3339Nano)
	_, e := b.db.Exec(`INSERT INTO tasks(id,session,sender,recipient,description,status,result,created,updated,notified) VALUES(?,?,?,?,?,?,?,?,?,'')`, t.ID, t.Session, t.From, t.To, t.Description, "pending", "", n, n)
	return t.ID, e
}
func (b *Broker) Complete(i, r string) error {
	return b.SetTaskStatus(i, "completed", r)
}
func (b *Broker) SetTaskStatus(i, status, result string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	x, e := b.db.Exec(`UPDATE tasks SET status=?,result=?,updated=? WHERE id=?`, status, result, time.Now().UTC().Format(time.RFC3339Nano), i)
	if e != nil {
		return e
	}
	n, _ := x.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %s not found", i)
	}
	return nil
}
func (b *Broker) ClaimTask(i string) error { return b.SetTaskStatus(i, "running", "") }
func (b *Broker) Messages(session, agent string) ([]Message, error) {
	rows, e := b.db.Query(`SELECT id,session,sender,recipient,text,created,acked FROM messages WHERE session=? AND recipient=? AND acked=0 ORDER BY created`, session, agent)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var c string
		var a int
		if e = rows.Scan(&m.ID, &m.Session, &m.From, &m.To, &m.Text, &c, &a); e != nil {
			return nil, e
		}
		m.Created, _ = time.Parse(time.RFC3339Nano, c)
		m.Acked = a != 0
		out = append(out, m)
	}
	return out, nil
}
func (b *Broker) Await(c context.Context, i string) (Task, error) {
	for {
		var t Task
		var cr, up string
		e := b.db.QueryRowContext(c, `SELECT id,session,sender,recipient,description,status,result,created,updated FROM tasks WHERE id=?`, i).Scan(&t.ID, &t.Session, &t.From, &t.To, &t.Description, &t.Status, &t.Result, &cr, &up)
		if e == nil && t.Status != "pending" {
			t.Created, _ = time.Parse(time.RFC3339Nano, cr)
			t.Updated, _ = time.Parse(time.RFC3339Nano, up)
			return t, nil
		}
		if e != nil && e != sql.ErrNoRows {
			return t, e
		}
		select {
		case <-c.Done():
			return t, c.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
func (b *Broker) Snapshot(s string) (state, error) {
	var o state
	q := `SELECT session,id,kind,cwd,COALESCE(thread_id,''),COALESCE(socket,''),joined,claude_session,claude_pid FROM agents`
	a := []any{}
	if s != "" {
		q += ` WHERE session=?`
		a = append(a, s)
	}
	r, e := b.db.Query(q, a...)
	if e != nil {
		return o, e
	}
	for r.Next() {
		var x Agent
		var j string
		if e = r.Scan(&x.Session, &x.ID, &x.Kind, &x.CWD, &x.ThreadID, &x.Socket, &j, &x.ClaudeSession, &x.ClaudePID); e != nil {
			r.Close()
			return o, e
		}
		x.Joined, _ = time.Parse(time.RFC3339Nano, j)
		o.Agents = append(o.Agents, x)
	}
	r.Close()
	q = `SELECT id,session,sender,recipient,text,created,acked FROM messages`
	a = nil
	if s != "" {
		q += ` WHERE session=?`
		a = []any{s}
	}
	r, e = b.db.Query(q, a...)
	if e != nil {
		return o, e
	}
	for r.Next() {
		var m Message
		var c string
		var ack int
		if e = r.Scan(&m.ID, &m.Session, &m.From, &m.To, &m.Text, &c, &ack); e != nil {
			r.Close()
			return o, e
		}
		m.Created, _ = time.Parse(time.RFC3339Nano, c)
		m.Acked = ack != 0
		o.Messages = append(o.Messages, m)
	}
	r.Close()
	q = `SELECT id,session,sender,recipient,description,status,result,created,updated FROM tasks`
	a = nil
	if s != "" {
		q += ` WHERE session=?`
		a = []any{s}
	}
	r, e = b.db.Query(q, a...)
	if e != nil {
		return o, e
	}
	for r.Next() {
		var t Task
		var c, u string
		if e = r.Scan(&t.ID, &t.Session, &t.From, &t.To, &t.Description, &t.Status, &t.Result, &c, &u); e != nil {
			r.Close()
			return o, e
		}
		t.Created, _ = time.Parse(time.RFC3339Nano, c)
		t.Updated, _ = time.Parse(time.RFC3339Nano, u)
		o.Tasks = append(o.Tasks, t)
	}
	r.Close()
	q = `SELECT session,path,agent,expires FROM leases`
	a = nil
	if s != "" {
		q += ` WHERE session=?`
		a = []any{s}
	}
	r, e = b.db.Query(q, a...)
	if e != nil {
		return o, e
	}
	for r.Next() {
		var l Lease
		var x string
		if e = r.Scan(&l.Session, &l.Path, &l.Agent, &x); e != nil {
			r.Close()
			return o, e
		}
		l.Expires, _ = time.Parse(time.RFC3339Nano, x)
		o.Leases = append(o.Leases, l)
	}
	r.Close()
	return o, nil
}
