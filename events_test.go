package main

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMessageEventDeliveredOnce(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	if err := b.Send(Message{Session: "s", From: "a", To: "b", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	events, err := b.PendingEvents("s", "b")
	if err != nil || len(events) != 1 || events[0].From != "a" || events[0].Text != "hi" {
		t.Fatalf("%v %v", events, err)
	}
	if ok, err := b.ClaimEvent(events[0]); !ok || err != nil {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := b.ClaimEvent(events[0]); ok {
		t.Fatal("event claimed twice")
	}
	if events, _ = b.PendingEvents("s", "b"); len(events) != 0 {
		t.Fatal(events)
	}
}

func TestTaskEventsFollowLifecycle(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	id, err := b.Start(Task{Session: "s", From: "req", To: "work", Description: "build it"})
	if err != nil {
		t.Fatal(err)
	}
	if events, _ := b.PendingEvents("s", "req"); len(events) != 0 {
		t.Fatal("requester notified of its own assignment")
	}
	events, _ := b.PendingEvents("s", "work")
	if len(events) != 1 || events[0].Status != "pending" || events[0].From != "req" || events[0].Text != "build it" {
		t.Fatal(events)
	}
	if ok, _ := b.ClaimEvent(events[0]); !ok {
		t.Fatal("assignment not claimed")
	}
	if err := b.ClaimTask(id); err != nil {
		t.Fatal(err)
	}
	if events, _ := b.PendingEvents("s", "work"); len(events) != 0 {
		t.Fatal(events)
	}
	if err := b.Complete(id, "built"); err != nil {
		t.Fatal(err)
	}
	events, _ = b.PendingEvents("s", "req")
	if len(events) != 1 || events[0].Status != "completed" || events[0].From != "work" || events[0].Text != "built" {
		t.Fatal(events)
	}
	if events[0].Key() != "agenteur-task-"+id+"-completed" {
		t.Fatal(events[0].Key())
	}
	if ok, _ := b.ClaimEvent(events[0]); !ok {
		t.Fatal("completion not claimed")
	}
	if err := b.UnclaimEvent(events[0]); err != nil {
		t.Fatal(err)
	}
	if events, _ = b.PendingEvents("s", "req"); len(events) != 1 {
		t.Fatal("unclaimed event not pending again")
	}
}

func TestMigrationDoesNotReplayHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE agents(session TEXT,id TEXT,kind TEXT,cwd TEXT,thread_id TEXT,socket TEXT,joined TEXT,PRIMARY KEY(session,id));
		CREATE TABLE messages(id TEXT PRIMARY KEY,session TEXT,sender TEXT,recipient TEXT,text TEXT,created TEXT,acked INTEGER DEFAULT 0);
		CREATE TABLE tasks(id TEXT PRIMARY KEY,session TEXT,sender TEXT,recipient TEXT,description TEXT,status TEXT,result TEXT,created TEXT,updated TEXT);
		INSERT INTO messages VALUES('1','s','a','b','old','2026-01-01T00:00:00Z',0);
		INSERT INTO tasks VALUES('2','s','b','a','old','completed','r','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	b := NewBroker(path)
	for _, agent := range []string{"a", "b"} {
		if events, err := b.PendingEvents("s", agent); err != nil || len(events) != 0 {
			t.Fatalf("%s: %v %v", agent, events, err)
		}
	}
	// Reopening an already migrated database is a no-op.
	NewBroker(path)
}

func TestClaudeSessionFollowsLatestJoin(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	for _, a := range []Agent{
		{Session: "s", ID: "old", ClaudeSession: "sid", ClaudePID: 7},
		{Session: "s", ID: "other", ClaudeSession: "other-sid", ClaudePID: 8},
		{Session: "s", ID: "new", ClaudeSession: "sid", ClaudePID: 7},
	} {
		if err := b.Join(a); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []struct {
		sid string
		pid int
	}{{"sid", 0}, {"", 7}} {
		agents, err := b.ClaudeAgents(q.sid, q.pid)
		if err != nil || len(agents) != 1 || agents[0].ID != "new" {
			t.Fatalf("%v: %v %v", q, agents, err)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWatchPrintsEventLines(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	if err := b.Join(Agent{Session: "s", ID: "me", ClaudeSession: "sid"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Send(Message{Session: "s", From: "peer", To: "me", Text: "two\nlines"}); err != nil {
		t.Fatal(err)
	}
	if err := watchOnce(b, "sid", 0, failWriter{}); !isWriteErr(err) {
		t.Fatalf("got %v", err)
	}
	var out bytes.Buffer
	if err := watchOnce(b, "sid", 0, &out); err != nil {
		t.Fatal(err)
	}
	line := out.String()
	if strings.Count(line, "\n") != 1 || !strings.HasPrefix(line, "agenteur message ") || !strings.Contains(line, `from peer: "two\nlines"`) {
		t.Fatalf("%q", line)
	}
	out.Reset()
	if err := watchOnce(b, "sid", 0, &out); err != nil || out.Len() != 0 {
		t.Fatalf("redelivered: %q %v", out.String(), err)
	}
}

func TestJoinSeesRunningWatcher(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	if ok, _ := b.Watching("sess", 42); ok {
		t.Fatal("watching before any watcher ran")
	}
	if err := watchOnce(b, "", 42, io.Discard); err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.Watching("sess", 42); !ok {
		t.Fatal("watcher for the same Claude process not seen")
	}
	if ok, _ := b.Watching("other", 7); ok {
		t.Fatal("another Claude session counted as watched")
	}
}

func TestLeaveStopsDelivery(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	if err := b.Join(Agent{ID: "a", Session: "s", ClaudePID: 42}); err != nil {
		t.Fatal(err)
	}
	if err := b.Send(Message{Session: "s", From: "b", To: "a", Text: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Leave("s", "a"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := watchOnce(b, "", 42, &out); err != nil || out.Len() != 0 {
		t.Fatalf("delivered after leaving: %q %v", out.String(), err)
	}
	if err := b.Leave("s", "a"); err == nil {
		t.Fatal("leaving twice reported success")
	}
}

func TestOthersHearJoinsRoleChangesAndLeaves(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	if err := b.Join(Agent{ID: "web", Session: "s", ClaudePID: 42}); err != nil {
		t.Fatal(err)
	}
	lines := func() string {
		var out strings.Builder
		if err := watchOnce(b, "", 42, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	steps := []struct {
		do   func() error
		want string
	}{
		{func() error { return b.Join(Agent{ID: "api", Session: "s", Role: "owns the REST API"}) }, `agenteur agent api joined session "s" with role: "owns the REST API"` + "\n"},
		{func() error { return b.Join(Agent{ID: "api", Session: "s"}) }, ""},
		{func() error { return b.Join(Agent{ID: "api", Session: "s", Role: "owns the REST API"}) }, ""},
		{func() error { return b.Join(Agent{ID: "api", Session: "s", Role: "reviews"}) }, `agenteur agent api in session "s" changed its role to: "reviews"` + "\n"},
		{func() error { return b.Leave("s", "api") }, `agenteur agent api left session "s"` + "\n"},
	}
	for i, st := range steps {
		if err := st.do(); err != nil {
			t.Fatal(err)
		}
		if got := lines(); got != st.want {
			t.Fatalf("step %d: got %q, want %q", i, got, st.want)
		}
	}
	if m, _ := b.Messages("s", "web"); len(m) != 0 {
		t.Fatalf("notices in inbox: %v", m)
	}
	if st, _ := b.Snapshot("s"); len(st.Messages) != 0 {
		t.Fatalf("notices in session history: %v", st.Messages)
	}
}

func TestListAgentsGroupsSessionsAndShowsMonitors(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	defer b.db.Close()
	for _, a := range []Agent{
		{ID: "web", Session: "s", Kind: "claude", ClaudePID: 42, Role: "owns the web frontend"},
		{ID: "gone", Session: "s", Kind: "claude", ClaudePID: 43},
		{ID: "api", Session: "t", Kind: "codex"},
	} {
		if err := b.Join(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.WatcherSeen("", 42); err != nil {
		t.Fatal(err)
	}
	all, err := participants(b, "")
	if err != nil || len(all) != 2 || all[0]["session"] != "s" || all[1]["session"] != "t" {
		t.Fatalf("%v %v", all, err)
	}
	s := all[0]["agents"].([]map[string]any)
	if len(s) != 2 || s[0]["agent"] != "web" || s[0]["role"] != "owns the web frontend" || s[0]["events_monitor"] != true || s[1]["events_monitor"] != false {
		t.Fatal(s)
	}
	if _, ok := all[1]["agents"].([]map[string]any)[0]["events_monitor"]; ok {
		t.Fatal("codex agent reported a Claude monitor")
	}
	one, err := participants(b, "t")
	if err != nil || len(one) != 1 || one[0]["session"] != "t" {
		t.Fatalf("%v %v", one, err)
	}
	none, err := participants(b, "missing")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("%v %v", none, err)
	}
}
