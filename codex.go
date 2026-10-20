package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"nhooyr.io/websocket"
)

func codexSocket(socket string) string {
	if socket != "" {
		return socket
	}
	if socket = setting("CODEX_SOCKET"); socket != "" {
		return socket
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
		home = filepath.Join(home, ".codex")
	}
	return filepath.Join(home, "app-server-control", "app-server-control.sock")
}

// Each exchange has a deadline and its own connection; a disconnected Codex
// must not block durable task completion indefinitely.
func codexCall(ctx context.Context, socket, method string, params any) error {
	_, err := codexRequest(ctx, socket, method, params)
	return err
}

// codexRequest is codexCall that also returns the response's result.
func codexRequest(ctx context.Context, socket, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	socket = codexSocket(socket)
	d := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{DialContext: func(c context.Context, _, _ string) (net.Conn, error) { return d.DialContext(c, "unix", socket) }}
	defer tr.CloseIdleConnections()
	c, _, err := websocket.Dial(ctx, "ws://localhost", &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		return nil, fmt.Errorf("Codex app-server %s: %w", socket, err)
	}
	defer c.CloseNow()
	send := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return c.Write(ctx, websocket.MessageText, b)
	}
	receive := func(id int) (json.RawMessage, error) {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return nil, err
			}
			var v struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(data, &v); err != nil {
				return nil, err
			}
			if v.ID == nil || *v.ID != id {
				continue
			}
			if len(v.Error) != 0 && string(v.Error) != "null" {
				return nil, fmt.Errorf("Codex app-server: %s", v.Error)
			}
			return v.Result, nil
		}
	}
	if err = send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"clientInfo":   map[string]string{"name": "agenteur", "version": version},
		"capabilities": map[string]bool{"experimentalApi": true},
	}}); err != nil {
		return nil, err
	}
	if _, err = receive(1); err != nil {
		return nil, err
	}
	if err = send(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	if method == "" {
		return nil, nil
	}
	if err = send(map[string]any{"id": 2, "method": method, "params": params}); err != nil {
		return nil, err
	}
	return receive(2)
}

func checkCodexThread(ctx context.Context, socket, thread string) error {
	if thread == "" {
		return fmt.Errorf("Codex thread ID is required")
	}
	return codexCall(ctx, socket, "thread/read", map[string]any{"threadId": thread})
}

func injectCodex(ctx context.Context, socket, thread, message string) error {
	if thread == "" {
		return fmt.Errorf("Codex thread ID is required")
	}
	return codexCall(ctx, socket, "thread/inject_items", map[string]any{
		"threadId": thread,
		"items":    []map[string]any{{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": message}}}},
	})
}

// queueCodex submits text as a user message on the thread's queue: an idle
// thread starts a turn at once, and a busy one runs it after the current turn
// completes. Ephemeral threads have no queue, so the text is only appended to
// their history.
func queueCodex(ctx context.Context, socket, thread, key, text string) error {
	if thread == "" {
		return fmt.Errorf("Codex thread ID is required")
	}
	err := codexCall(ctx, socket, "thread/queue/add", map[string]any{
		"threadId":            thread,
		"clientUserMessageId": key,
		"input":               []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}},
	})
	if err != nil && strings.Contains(err.Error(), "does not support queued submissions") {
		return injectCodex(ctx, socket, thread, text)
	}
	return err
}

// deliverCodex pushes an agent's pending events to its registered thread.
// The app-server does not deduplicate clientUserMessageId, so each event is
// claimed before it is sent and returned to the queue if sending fails.
func deliverCodex(ctx context.Context, b *Broker, a Agent) error {
	events, err := b.PendingEvents(a.Session, a.ID)
	if err != nil {
		return err
	}
	for _, e := range events {
		if ok, err := b.ClaimEvent(e); err != nil || !ok {
			continue
		}
		if err := queueCodex(ctx, a.Socket, a.ThreadID, e.Key(), e.Line()); err != nil {
			_ = b.UnclaimEvent(e)
			return err
		}
	}
	return nil
}

// loadedThreads returns the threads the app-server has loaded. Those are the
// ones open in a Codex session: the app-server unloads a thread once no client
// subscribes to it.
func loadedThreads(ctx context.Context, socket string) (map[string]bool, error) {
	loaded := map[string]bool{}
	params := map[string]any{}
	for {
		r, err := codexRequest(ctx, socket, "thread/loaded/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := json.Unmarshal(r, &page); err != nil {
			return nil, err
		}
		for _, id := range page.Data {
			loaded[id] = true
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return loaded, nil
		}
		params = map[string]any{"cursor": *page.NextCursor}
	}
}

// deliverOpenThreads pushes events to every registered Codex agent whose
// thread is open. A resumed thread gets a new MCP process that has not joined,
// so this is what reaches it. Events are claimed, so any number of processes
// may do this at once.
func deliverOpenThreads(ctx context.Context, b *Broker) error {
	agents, err := b.CodexAgents()
	if err != nil {
		return err
	}
	waiting := map[string][]Agent{}
	for _, a := range agents {
		events, err := b.PendingEvents(a.Session, a.ID)
		if err != nil {
			return err
		}
		if len(events) > 0 {
			waiting[a.Socket] = append(waiting[a.Socket], a)
		}
	}
	for socket, agents := range waiting {
		loaded, err := loadedThreads(ctx, socket)
		if err != nil {
			return err
		}
		for _, a := range agents {
			if !loaded[a.ThreadID] {
				continue
			}
			if err := deliverCodex(ctx, b, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// callerThread returns the Codex thread a tool call came from. Codex sends it
// in the request's _meta and does not set CODEX_THREAD_ID for MCP servers;
// the variable is only a fallback for agents of kind codex.
func callerThread(req *mcp.CallToolRequest, kind string) string {
	if req != nil && req.Params != nil {
		if id, _ := req.Params.Meta["threadId"].(string); id != "" {
			return id
		}
	}
	if kind == "codex" {
		return os.Getenv("CODEX_THREAD_ID")
	}
	return ""
}
