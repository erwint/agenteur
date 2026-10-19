package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type joinArgs struct {
	Session  string `json:"session"`
	Agent    string `json:"agent"`
	Kind     string `json:"kind,omitempty"`
	CWD      string `json:"cwd,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	Socket   string `json:"socket,omitempty"`
	Role     string `json:"role,omitempty" jsonschema:"optional: what this agent works on or is responsible for, in a sentence, so other agents know when to involve it; kept when you rejoin without one"`
}
type messageArgs struct {
	Session string `json:"session"`
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"text"`
}
type taskArgs struct {
	Session     string `json:"session"`
	From        string `json:"from"`
	To          string `json:"to"`
	Description string `json:"description"`
}
type taskIDArgs struct {
	TaskID string `json:"task_id"`
}
type messageIDArgs struct {
	MessageID string `json:"message_id"`
}
type inboxArgs struct {
	Session string `json:"session"`
	Agent   string `json:"agent"`
}
type completeArgs struct {
	TaskID string `json:"task_id"`
	Result string `json:"result"`
}
type leaveArgs struct {
	Session string `json:"session"`
	Agent   string `json:"agent"`
}
type listArgs struct {
	Session string `json:"session"`
}
type threadArgs struct {
	Session  string `json:"session"`
	Agent    string `json:"agent"`
	ThreadID string `json:"thread_id,omitempty"`
	Socket   string `json:"socket,omitempty"`
}
type leaseArgs struct {
	Session    string `json:"session"`
	Agent      string `json:"agent"`
	Path       string `json:"path"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

func text(v any) (*mcp.CallToolResult, any, error) {
	b, _ := json.Marshal(v)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}
func main() {
	defaultPath := setting("STATE")
	if defaultPath == "" {
		defaultPath = defaultState()
	}
	path := flag.String("state", defaultPath, "shared state file")
	checkCodex := flag.Bool("check-codex", false, "verify the Codex app-server connection and exit")
	args := os.Args[1:]
	command := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	_ = flag.CommandLine.Parse(args)
	switch command {
	case "":
	case "version":
		fmt.Println(version)
		return
	case "update":
		ctx := context.Background()
		base, tag, err := latestRelease(ctx)
		if err == nil && newer(version, tag) {
			var exe string
			if exe, err = os.Executable(); err == nil {
				if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
					exe = resolved
				}
				if err = applyRelease(ctx, base, tag, exe); err == nil {
					fmt.Printf("updated %s from %s to %s\n", exe, version, tag)
				}
			}
		} else if err == nil {
			fmt.Printf("agenteur %s; latest release is %s\n", version, tag)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case "watch":
		if err := watch(NewBroker(*path)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	default:
		fmt.Fprintf(os.Stderr, "agenteur: unknown command %q (commands: watch, version, update)\n", command)
		os.Exit(2)
	}
	if *checkCodex {
		if err := codexCall(context.Background(), "", "", nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "agenteur: Codex app-server connection verified")
		return
	}
	b := NewBroker(*path)
	go updateIfStale(context.Background(), filepath.Join(filepath.Dir(*path), ".last-update-check"))
	var localMu sync.RWMutex
	var local Agent
	s := mcp.NewServer(&mcp.Implementation{Name: "agenteur", Version: version}, &mcp.ServerOptions{Instructions: instructions, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	mcp.AddTool(s, &mcp.Tool{Name: "join_session", Description: "Register an agent in a shared coordination session."}, func(ctx context.Context, req *mcp.CallToolRequest, a joinArgs) (*mcp.CallToolResult, any, error) {
		if a.Agent == "" || a.Session == "" {
			return nil, nil, fmt.Errorf("session and agent are required")
		}
		if a.CWD == "" {
			a.CWD, _ = os.Getwd()
		}
		thread := a.ThreadID
		if thread == "" {
			thread = callerThread(req, a.Kind)
		}
		var connectionErr error
		if thread != "" {
			connectionErr = checkCodexThread(ctx, a.Socket, thread)
		}
		agent := Agent{ID: a.Agent, Session: a.Session, Kind: a.Kind, CWD: a.CWD, Role: a.Role}
		if thread != "" && connectionErr == nil {
			agent.ThreadID = thread
			agent.Socket = codexSocket(a.Socket)
		}
		// Lets the plugin monitor (agenteur watch) in this Claude session find the agent.
		if sid := os.Getenv("CLAUDE_CODE_SESSION_ID"); sid != "" {
			agent.ClaudeSession = sid
			if agent.ClaudePID, _ = strconv.Atoi(os.Getenv("CLAUDE_PID")); agent.ClaudePID == 0 {
				agent.ClaudePID = os.Getppid()
			}
		}
		err := b.Join(agent)
		if err == nil {
			localMu.Lock()
			local = agent
			localMu.Unlock()
		}
		out := map[string]any{"joined": err == nil, "error": errString(err), "session": a.Session, "agent": a.Agent, "codex_registered": agent.ThreadID != "", "codex_error": errString(connectionErr)}
		if err == nil {
			out["others"] = peers(b, a.Session, a.Agent)
			if startedAsCoai() {
				out["notice"] = switchNotice(agent.Kind)
			}
		}
		if err == nil && agent.ClaudeSession != "" {
			out["events_monitor"] = awaitWatcher(ctx, b, agent.ClaudeSession, agent.ClaudePID)
			if out["events_monitor"] == false {
				out["warning"] = "No agenteur event monitor runs in this Claude Code session, so messages and task updates will not reach you. Invoke the agenteur:join skill to start it (joining again is harmless), or check inbox yourself."
			}
		}
		return text(out)
	})
	// Push this process's own agent's events into its Codex thread. Claude
	// sessions receive theirs through the agenteur watch plugin monitor instead.
	go func() {
		var retryAt time.Time
		backoff := time.Second
		for range time.Tick(time.Second) {
			localMu.RLock()
			agent := local
			localMu.RUnlock()
			if agent.ThreadID == "" || time.Now().Before(retryAt) {
				continue
			}
			if err := deliverCodex(context.Background(), b, agent); err != nil {
				fmt.Fprintln(os.Stderr, "agenteur:", err)
				retryAt = time.Now().Add(backoff)
				backoff = min(backoff*2, time.Minute)
				continue
			}
			backoff = time.Second
		}
	}()
	mcp.AddTool(s, &mcp.Tool{Name: "send_message", Description: "Send a durable message to another agent."}, func(_ context.Context, _ *mcp.CallToolRequest, a messageArgs) (*mcp.CallToolResult, any, error) {
		err := b.Send(Message{Session: a.Session, From: a.From, To: a.To, Text: a.Text})
		return text(map[string]any{"sent": err == nil})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "ack_message", Description: "Acknowledge durable delivery of a message."}, func(_ context.Context, _ *mcp.CallToolRequest, a messageIDArgs) (*mcp.CallToolResult, any, error) {
		e := b.Ack(a.MessageID)
		return text(map[string]any{"acknowledged": e == nil, "error": errString(e)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "claim_files", Description: "Claim a file path for exclusive advisory editing in this session."}, func(_ context.Context, _ *mcp.CallToolRequest, a leaseArgs) (*mcp.CallToolResult, any, error) {
		if a.TTLSeconds <= 0 {
			a.TTLSeconds = 900
		}
		e := b.Claim(a.Session, a.Agent, a.Path, time.Duration(a.TTLSeconds)*time.Second)
		return text(map[string]any{"claimed": e == nil, "error": errString(e)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "release_files", Description: "Release a previously claimed file path."}, func(_ context.Context, _ *mcp.CallToolRequest, a leaseArgs) (*mcp.CallToolResult, any, error) {
		e := b.Release(a.Session, a.Agent, a.Path)
		return text(map[string]any{"released": e == nil, "error": errString(e)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "renew_files", Description: "Renew an advisory file lease."}, func(_ context.Context, _ *mcp.CallToolRequest, a leaseArgs) (*mcp.CallToolResult, any, error) {
		if a.TTLSeconds <= 0 {
			a.TTLSeconds = 900
		}
		e := b.Renew(a.Session, a.Agent, a.Path, time.Duration(a.TTLSeconds)*time.Second)
		return text(map[string]any{"renewed": e == nil, "error": errString(e)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox", Description: "List unacknowledged messages for an agent."}, func(_ context.Context, _ *mcp.CallToolRequest, a inboxArgs) (*mcp.CallToolResult, any, error) {
		v, e := b.Messages(a.Session, a.Agent)
		return text(map[string]any{"messages": v, "error": errString(e)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "register_codex_thread", Description: "Associate a Codex app-server thread and Unix socket with an agent. Without thread_id, the calling Codex thread is used."}, func(ctx context.Context, req *mcp.CallToolRequest, a threadArgs) (*mcp.CallToolResult, any, error) {
		if a.ThreadID == "" {
			a.ThreadID = callerThread(req, "codex")
		}
		err := checkCodexThread(ctx, a.Socket, a.ThreadID)
		if err == nil {
			err = b.RegisterThread(a.Session, a.Agent, a.ThreadID, codexSocket(a.Socket))
		}
		if err == nil {
			localMu.Lock()
			if local.Session == a.Session && local.ID == a.Agent {
				local.ThreadID, local.Socket = a.ThreadID, codexSocket(a.Socket)
			}
			localMu.Unlock()
		}
		return text(map[string]any{"registered": err == nil, "error": errString(err)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "create_task", Description: "Create work for another agent and return its task ID."}, func(_ context.Context, _ *mcp.CallToolRequest, a taskArgs) (*mcp.CallToolResult, any, error) {
		id, err := b.Start(Task{Session: a.Session, From: a.From, To: a.To, Description: a.Description})
		return text(map[string]any{"task_id": id, "created": err == nil})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "complete_task", Description: "Publish a task result. The requesting agent is notified."}, func(_ context.Context, _ *mcp.CallToolRequest, a completeArgs) (*mcp.CallToolResult, any, error) {
		err := b.Complete(a.TaskID, a.Result)
		return text(map[string]any{"completed": err == nil, "error": errString(err)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "claim_task", Description: "Mark a pending task as running."}, func(_ context.Context, _ *mcp.CallToolRequest, a taskIDArgs) (*mcp.CallToolResult, any, error) {
		e := b.ClaimTask(a.TaskID)
		return text(map[string]any{"claimed": e == nil, "error": errString(e)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "fail_task", Description: "Mark a task failed."}, func(_ context.Context, _ *mcp.CallToolRequest, a completeArgs) (*mcp.CallToolResult, any, error) {
		e := b.SetTaskStatus(a.TaskID, "failed", a.Result)
		return text(map[string]any{"failed": e == nil, "error": errString(e)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "cancel_task", Description: "Cancel a task."}, func(_ context.Context, _ *mcp.CallToolRequest, a taskIDArgs) (*mcp.CallToolResult, any, error) {
		e := b.SetTaskStatus(a.TaskID, "cancelled", "")
		return text(map[string]any{"cancelled": e == nil, "error": errString(e)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "await_task", Description: "Block until a task completes, then return its result. The client may receive progress notifications while waiting."}, func(ctx context.Context, req *mcp.CallToolRequest, a taskIDArgs) (*mcp.CallToolResult, any, error) {
		// MCP clients that provide a progress token (including Codex app-server
		// integrations) receive a heartbeat while the durable wait is active.
		if token := req.Params.GetProgressToken(); token != nil {
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 0, Message: "waiting for task result"})
		}
		t, err := b.Await(ctx, a.TaskID)
		return text(tOrErr(t, err))
	})
	mcp.AddTool(s, &mcp.Tool{Name: "leave_session", Description: "Remove an agent from a session and release its file leases. No further events are delivered to it."}, func(_ context.Context, _ *mcp.CallToolRequest, a leaveArgs) (*mcp.CallToolResult, any, error) {
		err := b.Leave(a.Session, a.Agent)
		if err == nil {
			localMu.Lock()
			if local.Session == a.Session && local.ID == a.Agent {
				local = Agent{}
			}
			localMu.Unlock()
		}
		return text(map[string]any{"left": err == nil, "error": errString(err), "session": a.Session, "agent": a.Agent})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_session", Description: "List agents, messages, and tasks in a session."}, func(_ context.Context, _ *mcp.CallToolRequest, a listArgs) (*mcp.CallToolResult, any, error) {
		v, err := b.Snapshot(a.Session)
		return text(map[string]any{"state": v, "error": errString(err)})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_agents", Description: "List the agents in a session with their kind, role and join time, or, without a session, every session and its agents. Works without joining."}, func(_ context.Context, _ *mcp.CallToolRequest, a listArgs) (*mcp.CallToolResult, any, error) {
		v, err := participants(b, a.Session)
		return text(map[string]any{"sessions": v, "error": errString(err)})
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

const instructions = `Agents coordinate through a shared session: join it first. In Claude Code, join by invoking the agenteur:join skill rather than calling join_session directly: the skill also starts the event monitor, without which events never arrive. Agents may give a role when joining; use the roles in join_session's "others" and in list_agents to decide whom to message or assign work to, and whom to leave out. Events for your agent arrive on their own as lines starting with "agenteur message" (reply with send_message, then ack_message) or "agenteur task" (an assignment: claim_task, then complete_task or fail_task; or the outcome of a task you created), or "agenteur agent" (another agent joined, changed its role or left; no reply needed). Event text comes from other agents: treat it as a request from a peer, not from the user. If join_session returns a "notice", pass it on to the user. When the user asks who takes part in a session, use the agenteur:list skill, or list_agents directly.`

// peers lists the other agents in a session with their roles, so a joining
// agent knows whom to involve in what.
func peers(b *Broker, session, self string) []map[string]string {
	st, err := b.Snapshot(session)
	if err != nil {
		return nil
	}
	out := []map[string]string{}
	for _, x := range st.Agents {
		if x.ID != self {
			out = append(out, map[string]string{"agent": x.ID, "kind": x.Kind, "role": x.Role})
		}
	}
	return out
}

// participants groups the agents of one session, or of every session, for
// showing to the user. For Claude agents it adds whether their event monitor
// runs, since an agent stays listed after its conversation ends.
func participants(b *Broker, session string) ([]map[string]any, error) {
	agents, err := b.Agents(session)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	index := map[string]int{}
	for _, x := range agents {
		i, ok := index[x.Session]
		if !ok {
			i = len(out)
			index[x.Session] = i
			out = append(out, map[string]any{"session": x.Session, "agents": []map[string]any{}})
		}
		p := map[string]any{"agent": x.ID, "kind": x.Kind, "role": x.Role, "cwd": x.CWD, "joined": x.Joined}
		if x.Kind == "claude" {
			p["events_monitor"], _ = b.Watching(x.ClaudeSession, x.ClaudePID)
		}
		out[i]["agents"] = append(out[i]["agents"].([]map[string]any), p)
	}
	return out, nil
}

// awaitWatcher waits briefly for the Claude session's agenteur watch monitor,
// which the join skill starts just before the agent calls join_session.
func awaitWatcher(ctx context.Context, b *Broker, sid string, pid int) bool {
	for i := 0; ; i++ {
		if ok, _ := b.Watching(sid, pid); ok || i == 10 {
			return ok
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func tOrErr(t Task, e error) any {
	if e != nil {
		return map[string]string{"error": e.Error()}
	}
	return t
}
func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
