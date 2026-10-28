package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"
)

// status prints what delivers events to whom: the Codex app-server and the
// threads it has open, the agenteur processes delivering Codex events, and
// for each agent whether its monitor or thread is up and how many events
// wait for it. It reads only, so it is safe to run next to live sessions.
func status(ctx context.Context, b *Broker, path string, w io.Writer) error {
	exe, _ := os.Executable()
	fmt.Fprintf(w, "agenteur %s at %s\ndatabase: %s\n", version, exe, path)

	type threads struct {
		loaded map[string]bool
		err    error
	}
	sockets := map[string]threads{}
	loaded := func(socket string) threads {
		socket = codexSocket(socket)
		t, ok := sockets[socket]
		if !ok {
			t.loaded, t.err = loadedThreads(ctx, socket)
			sockets[socket] = t
		}
		return t
	}
	socket := codexSocket("")
	if t := loaded(""); t.err != nil {
		fmt.Fprintf(w, "codex app-server: unreachable: %v\n", t.err)
	} else {
		fmt.Fprintf(w, "codex app-server %s: %d threads open\n", socket, len(t.loaded))
	}

	pollers, err := b.Pollers(time.Now().Add(-5 * time.Second))
	if err != nil {
		return err
	}
	if len(pollers) == 0 {
		fmt.Fprintln(w, "codex delivery: no agenteur process runs under Codex")
	} else {
		fmt.Fprintln(w, "codex delivery:")
	}
	for _, p := range pollers {
		what := "not joined, delivers for every open thread"
		if p.Agent != "" {
			what = fmt.Sprintf("joined as %s in %q", p.Agent, p.Session)
		}
		if p.Error != "" {
			what += "; last error: " + p.Error
		}
		if p.PID == os.Getpid() {
			what += " (this process)"
		}
		fmt.Fprintf(w, "  pid %d (agenteur %s): %s\n", p.PID, p.Version, what)
	}

	snap, err := b.Snapshot("")
	if err != nil {
		return err
	}
	if len(snap.Agents) == 0 {
		fmt.Fprintln(w, "no agents")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	session := ""
	for _, a := range snap.Agents {
		if a.Session != session {
			session = a.Session
			fmt.Fprintf(tw, "session %q:\n", session)
		}
		var state string
		switch {
		case a.ThreadID != "":
			t := loaded(a.Socket)
			switch {
			case t.err != nil:
				state = "thread " + a.ThreadID + ", app-server unreachable"
			case t.loaded[a.ThreadID]:
				state = "thread " + a.ThreadID + " open"
			default:
				state = "thread " + a.ThreadID + " closed"
			}
		case a.ClaudeSession != "" || a.ClaudePID != 0:
			if ok, err := b.Watching(a.ClaudeSession, a.ClaudePID); err != nil {
				return err
			} else if ok {
				state = "monitor running"
			} else {
				state = "no monitor (invoke agenteur:join)"
			}
		case a.Kind == "codex":
			state = "no thread registered"
		default:
			state = "no Claude session"
		}
		events, err := b.PendingEvents(a.Session, a.ID)
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%d waiting\n", a.ID, a.Kind, state, len(events))
	}
	return tw.Flush()
}
