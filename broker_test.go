package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"nhooyr.io/websocket"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskAwait(t *testing.T) {
	b := NewBroker(t.TempDir() + "/state.json")
	task := Task{Session: "s", From: "a", To: "b", Description: "work"}
	id, err := b.Start(task)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(20 * time.Millisecond); _ = b.Complete(id, "done") }()
	got, err := b.Await(context.Background(), id)
	if err != nil || got.Result != "done" {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestSharedStateAndAgentUpdate(t *testing.T) {
	path := t.TempDir() + "/state.json"
	a := NewBroker(path)
	if err := a.Join(Agent{Session: "shared", ID: "claude", Kind: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := NewBroker(path).Join(Agent{Session: "shared", ID: "codex", Kind: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(Message{Session: "shared", From: "claude", To: "codex", Text: "please review"}); err != nil {
		t.Fatal(err)
	}
	s, err := a.Snapshot("shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Agents) != 2 || len(s.Messages) != 1 || s.Messages[0].Text != "please review" {
		t.Fatalf("unexpected state: %#v", s)
	}
	if err := a.Join(Agent{Session: "shared", ID: "claude", Kind: "claude-2"}); err != nil {
		t.Fatal(err)
	}
	s, err = a.Snapshot("shared")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range s.Agents {
		if agent.ID == "claude" && agent.Kind != "claude-2" {
			t.Fatalf("agent was not updated: %#v", agent)
		}
	}
}

func TestAwaitCancellation(t *testing.T) {
	b := NewBroker(t.TempDir() + "/state.json")
	id, err := b.Start(Task{Session: "s", From: "a", To: "b", Description: "never"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := b.Await(ctx, id); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestFileLease(t *testing.T) {
	b := NewBroker(t.TempDir() + "/state.db")
	if err := b.Claim("s", "a", "main.go", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := b.Claim("s", "b", "main.go", time.Minute); err == nil {
		t.Fatal("expected conflicting lease")
	}
	if err := b.Release("s", "a", "main.go"); err != nil {
		t.Fatal(err)
	}
	if err := b.Claim("s", "b", "main.go", time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestInjectCodex(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "codex.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		for i := 0; i < 3; i++ {
			_, data, _ := c.Read(r.Context())
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if m["method"] == "initialized" {
				continue
			}
			b, _ := json.Marshal(map[string]any{"id": m["id"], "result": map[string]any{}})
			_ = c.Write(r.Context(), websocket.MessageText, b)
		}
	})}
	go srv.Serve(ln)
	defer srv.Shutdown(context.Background())
	if err := injectCodex(context.Background(), sock, "thread-1", "finished"); err != nil {
		t.Fatal(err)
	}
}
