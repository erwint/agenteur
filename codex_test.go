package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"nhooyr.io/websocket"
)

// fakeCodex serves the app-server handshake plus one call per connection.
// Calls whose method is in failing are answered with that error message.
func fakeCodex(t *testing.T, reject bool, failing ...string) (string, <-chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "coai-ws-")
	if err != nil {
		t.Fatal(err)
	}
	// macOS Unix socket paths have a small length limit.
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	requests := make(chan map[string]any, 16)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		read := func() map[string]any {
			_, b, err := c.Read(r.Context())
			if err != nil {
				t.Error(err)
				return nil
			}
			var v map[string]any
			if err := json.Unmarshal(b, &v); err != nil {
				t.Error(err)
			}
			return v
		}
		write := func(v any) {
			b, _ := json.Marshal(v)
			if err := c.Write(r.Context(), websocket.MessageText, b); err != nil {
				t.Error(err)
			}
		}
		init := read()
		requests <- init
		write(map[string]any{"method": "test/notification"})
		if reject {
			write(map[string]any{"id": 1, "error": map[string]any{"message": "denied"}})
			return
		}
		write(map[string]any{"id": 1, "result": map[string]any{}})
		requests <- read()
		call := read()
		requests <- call
		for i := 0; i+1 < len(failing); i += 2 {
			if call["method"] == failing[i] {
				write(map[string]any{"id": 2, "error": map[string]any{"code": -32600, "message": failing[i+1]}})
				return
			}
		}
		write(map[string]any{"id": 2, "result": map[string]any{}})
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); os.RemoveAll(dir) })
	return socket, requests
}

func TestCodexInjectionProtocol(t *testing.T) {
	socket, requests := fakeCodex(t, false)
	if err := injectCodex(context.Background(), socket, "thread-123", "result text"); err != nil {
		t.Fatal(err)
	}
	init := <-requests
	params := init["params"].(map[string]any)
	if params["capabilities"].(map[string]any)["experimentalApi"] != true {
		t.Fatal("experimental API not enabled")
	}
	if (<-requests)["method"] != "initialized" {
		t.Fatal("missing initialized notification")
	}
	call := <-requests
	if call["method"] != "thread/inject_items" {
		t.Fatal(call)
	}
	p := call["params"].(map[string]any)
	item := p["items"].([]any)[0].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if p["threadId"] != "thread-123" || item["type"] != "message" || item["role"] != "user" || content["type"] != "input_text" || content["text"] != "result text" {
		t.Fatal(p)
	}
}

func TestCodexInitializationError(t *testing.T) {
	socket, _ := fakeCodex(t, true)
	if err := injectCodex(context.Background(), socket, "thread", "text"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("got %v", err)
	}
}

func TestCodexRegistrationChecksThread(t *testing.T) {
	socket, requests := fakeCodex(t, false)
	if err := checkCodexThread(context.Background(), socket, "known-thread"); err != nil {
		t.Fatal(err)
	}
	<-requests
	<-requests
	call := <-requests
	if call["method"] != "thread/read" || call["params"].(map[string]any)["threadId"] != "known-thread" {
		t.Fatal(call)
	}
}

func TestCodexUnavailable(t *testing.T) {
	if err := injectCodex(context.Background(), filepath.Join(t.TempDir(), "missing"), "thread", "result"); err == nil {
		t.Fatal("missing socket reported as success")
	}
	if err := checkCodexThread(context.Background(), "", ""); err == nil {
		t.Fatal("empty thread accepted")
	}
}

func call(requests <-chan map[string]any) map[string]any {
	<-requests // initialize
	<-requests // initialized
	return <-requests
}

func TestCodexResultGoesToRequester(t *testing.T) {
	socket, requests := fakeCodex(t, false)
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	defer b.db.Close()
	requester := Agent{Session: "s", ID: "requester", ThreadID: "requester-thread", Socket: socket}
	for _, a := range []Agent{requester, {Session: "s", ID: "worker", ThreadID: "worker-thread", Socket: "missing-worker-socket"}} {
		if err := b.Join(a); err != nil {
			t.Fatal(err)
		}
	}
	id, err := b.Start(Task{Session: "s", From: "requester", To: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(id, "done"); err != nil {
		t.Fatal(err)
	}
	if err := deliverCodex(context.Background(), b, requester); err != nil {
		t.Fatal(err)
	}
	c := call(requests)
	p := c["params"].(map[string]any)
	text := p["input"].([]any)[0].(map[string]any)["text"].(string)
	if c["method"] != "thread/queue/add" || p["threadId"] != "requester-thread" || p["clientUserMessageId"] != "coai-task-"+id+"-completed" || !strings.Contains(text, `completed by worker: "done"`) {
		t.Fatal(c)
	}
	// Delivered once: a second pass has nothing to send.
	if err := deliverCodex(context.Background(), b, requester); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatal("event delivered twice")
	}
}

func TestCodexEphemeralThreadFallsBackToInjection(t *testing.T) {
	socket, requests := fakeCodex(t, false, "thread/queue/add", "ephemeral thread does not support queued submissions: t")
	if err := queueCodex(context.Background(), socket, "t", "k", "hello"); err != nil {
		t.Fatal(err)
	}
	if m := call(requests)["method"]; m != "thread/queue/add" {
		t.Fatal(m)
	}
	if m := call(requests)["method"]; m != "thread/inject_items" {
		t.Fatal(m)
	}
}

func TestCodexDeliveryFailureKeepsEvent(t *testing.T) {
	b := NewBroker(filepath.Join(t.TempDir(), "state.db"))
	defer b.db.Close()
	a := Agent{Session: "s", ID: "a", ThreadID: "thread", Socket: filepath.Join(t.TempDir(), "absent")}
	if err := b.Join(a); err != nil {
		t.Fatal(err)
	}
	id, err := b.Start(Task{Session: "s", From: "a", To: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(id, "durable result"); err != nil {
		t.Fatal(err)
	}
	if err := deliverCodex(context.Background(), b, a); err == nil {
		t.Fatal("missing socket reported as delivered")
	}
	events, err := b.PendingEvents("s", "a")
	if err != nil || len(events) != 1 || events[0].Text != "durable result" {
		t.Fatalf("event not kept for retry: %v %v", events, err)
	}
}

func TestCallerThreadComesFromToolCallMeta(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "from-env")
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Meta: mcp.Meta{"threadId": "from-meta"}}}
	if got := callerThread(req, "codex"); got != "from-meta" {
		t.Fatalf("got %q", got)
	}
	empty := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	if got := callerThread(empty, "codex"); got != "from-env" {
		t.Fatalf("got %q", got)
	}
	if got := callerThread(empty, "claude"); got != "" {
		t.Fatalf("claude agent got thread %q", got)
	}
}
