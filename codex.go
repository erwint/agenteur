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

	"nhooyr.io/websocket"
)

func codexSocket(socket string) string {
	if socket != "" {
		return socket
	}
	if socket = os.Getenv("COAI_CODEX_SOCKET"); socket != "" {
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	socket = codexSocket(socket)
	d := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{DialContext: func(c context.Context, _, _ string) (net.Conn, error) { return d.DialContext(c, "unix", socket) }}
	defer tr.CloseIdleConnections()
	c, _, err := websocket.Dial(ctx, "ws://localhost", &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		return fmt.Errorf("Codex app-server %s: %w", socket, err)
	}
	defer c.CloseNow()
	send := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return c.Write(ctx, websocket.MessageText, b)
	}
	receive := func(id int) error {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return err
			}
			var v struct {
				ID    *int            `json:"id"`
				Error json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(data, &v); err != nil {
				return err
			}
			if v.ID == nil || *v.ID != id {
				continue
			}
			if len(v.Error) != 0 && string(v.Error) != "null" {
				return fmt.Errorf("Codex app-server: %s", v.Error)
			}
			return nil
		}
	}
	if err = send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"clientInfo":   map[string]string{"name": "coai-coordinator", "version": version},
		"capabilities": map[string]bool{"experimentalApi": true},
	}}); err != nil {
		return err
	}
	if err = receive(1); err != nil {
		return err
	}
	if err = send(map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	if method == "" {
		return nil
	}
	if err = send(map[string]any{"id": 2, "method": method, "params": params}); err != nil {
		return err
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
