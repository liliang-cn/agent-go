package acpserver

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type fakeRuntime struct {
	getSessionFunc func(sessionID string) (*agent.Session, error)
	runStreamFunc  func(ctx context.Context, goal string) (<-chan *agent.Event, error)
	closeFunc      func() error
	sessionID      string
}

func (f *fakeRuntime) SetSessionID(sessionID string) { f.sessionID = sessionID }

func (f *fakeRuntime) GetSession(sessionID string) (*agent.Session, error) {
	return f.getSessionFunc(sessionID)
}

func (f *fakeRuntime) RunStream(ctx context.Context, goal string) (<-chan *agent.Event, error) {
	return f.runStreamFunc(ctx, goal)
}

func (f *fakeRuntime) Close() error {
	if f.closeFunc != nil {
		return f.closeFunc()
	}
	return nil
}

type testClient struct {
	mu                sync.Mutex
	updates           []acp.SessionNotification
	permissionCalls   int
	permissionOutcome string
}

func (c *testClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, nil
}

func (c *testClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, nil
}

func (c *testClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.permissionCalls++
	if c.permissionOutcome == "reject" {
		return acp.RequestPermissionResponse{
			Outcome: acp.NewRequestPermissionOutcomeSelected("reject"),
		}, nil
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeSelected("allow"),
	}, nil
}

func (c *testClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, params)
	return nil
}

func (c *testClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, nil
}

func (c *testClient) KillTerminalCommand(ctx context.Context, params acp.KillTerminalCommandRequest) (acp.KillTerminalCommandResponse, error) {
	return acp.KillTerminalCommandResponse{}, nil
}

func (c *testClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}

func (c *testClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}

func (c *testClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}

func (c *testClient) snapshot() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]acp.SessionNotification, len(c.updates))
	copy(out, c.updates)
	return out
}

func (c *testClient) permissionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.permissionCalls
}

func (c *testClient) waitForUpdates(t *testing.T, min int) []acp.SessionNotification {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := c.snapshot()
		if len(snapshot) >= min {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	return c.snapshot()
}

type fakeHookRuntime struct {
	*fakeRuntime
	mu    sync.Mutex
	hooks map[string]hookRegistration
}

type hookRegistration struct {
	event   agent.HookEvent
	handler agent.HookHandler
}

func newFakeHookRuntime() *fakeHookRuntime {
	return &fakeHookRuntime{
		fakeRuntime: &fakeRuntime{},
		hooks:       make(map[string]hookRegistration),
	}
}

func (f *fakeHookRuntime) RegisterHook(event agent.HookEvent, handler agent.HookHandler, opts ...agent.HookOption) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := string(event) + time.Now().String()
	f.hooks[id] = hookRegistration{event: event, handler: handler}
	return id
}

func (f *fakeHookRuntime) UnregisterHook(hookID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.hooks[hookID]
	delete(f.hooks, hookID)
	return ok
}

func (f *fakeHookRuntime) emitHook(ctx context.Context, event agent.HookEvent, data agent.HookData) error {
	f.mu.Lock()
	hooks := make([]hookRegistration, 0, len(f.hooks))
	for _, h := range f.hooks {
		if h.event == event {
			hooks = append(hooks, h)
		}
	}
	f.mu.Unlock()
	for _, h := range hooks {
		if _, err := h.handler(ctx, event, data); err != nil {
			return err
		}
	}
	return nil
}

func TestServerInitializeNewSessionAndPrompt(t *testing.T) {
	t.Parallel()

	rt := &fakeRuntime{
		getSessionFunc: func(sessionID string) (*agent.Session, error) {
			return agent.NewSessionWithID(sessionID, "agent"), nil
		},
		runStreamFunc: func(ctx context.Context, goal string) (<-chan *agent.Event, error) {
			ch := make(chan *agent.Event, 5)
			ch <- &agent.Event{Type: agent.EventTypeThinking, Content: "thinking"}
			ch <- &agent.Event{Type: agent.EventTypePartial, Content: "Hello "}
			ch <- &agent.Event{Type: agent.EventTypeToolCall, ToolName: "rag_query", ToolArgs: map[string]interface{}{"query": goal}}
			ch <- &agent.Event{Type: agent.EventTypeToolResult, ToolName: "rag_query", ToolResult: "doc context"}
			ch <- &agent.Event{Type: agent.EventTypeComplete, Content: "Hello world"}
			close(ch)
			return ch, nil
		},
	}

	server, clientConn, client := newTestACPBridge(t, func(ctx context.Context, cfg SessionConfig) (SessionRuntime, error) {
		return rt, nil
	})
	defer server.Close()

	ctx := context.Background()
	initResp, err := clientConn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapability{},
		},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if !initResp.AgentCapabilities.LoadSession {
		t.Fatalf("expected loadSession capability")
	}
	if !initResp.AgentCapabilities.PromptCapabilities.EmbeddedContext {
		t.Fatalf("expected embeddedContext capability")
	}

	newResp, err := clientConn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        "/tmp/project",
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if newResp.SessionId == "" {
		t.Fatalf("expected session id")
	}

	promptResp, err := clientConn.Prompt(ctx, acp.PromptRequest{
		SessionId: newResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Summarize this"),
			acp.ResourceBlock(acp.EmbeddedResourceResource{
				TextResourceContents: &acp.TextResourceContents{
					Text: "embedded context",
					Uri:  "file:///tmp/context.txt",
				},
			}),
		},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if promptResp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("unexpected stop reason: %s", promptResp.StopReason)
	}

	updates := client.waitForUpdates(t, 4)
	if len(updates) < 4 {
		t.Fatalf("expected streamed updates, got %d", len(updates))
	}
	thoughtIdx, messageIdx, toolCallIdx, toolUpdateIdx := -1, -1, -1, -1
	for i, update := range updates {
		switch {
		case thoughtIdx == -1 && update.Update.AgentThoughtChunk != nil:
			thoughtIdx = i
		case messageIdx == -1 && update.Update.AgentMessageChunk != nil:
			messageIdx = i
		case toolCallIdx == -1 && update.Update.ToolCall != nil:
			toolCallIdx = i
		case toolUpdateIdx == -1 && update.Update.ToolCallUpdate != nil:
			toolUpdateIdx = i
		}
	}
	if thoughtIdx == -1 {
		t.Fatalf("expected a thought chunk update, got %#v", updates)
	}
	if messageIdx == -1 {
		t.Fatalf("expected an agent message chunk update, got %#v", updates)
	}
	if toolCallIdx == -1 {
		t.Fatalf("expected a tool call update, got %#v", updates)
	}
	if toolUpdateIdx == -1 {
		t.Fatalf("expected a tool call result update, got %#v", updates)
	}
	if !(thoughtIdx < toolCallIdx && thoughtIdx < toolUpdateIdx) {
		t.Fatalf("expected thought chunk before tool lifecycle updates, got thought=%d toolCall=%d toolUpdate=%d", thoughtIdx, toolCallIdx, toolUpdateIdx)
	}
}

func TestServerLoadSessionReplaysHistory(t *testing.T) {
	t.Parallel()

	rt := &fakeRuntime{
		getSessionFunc: func(sessionID string) (*agent.Session, error) {
			s := agent.NewSessionWithID(sessionID, "agent")
			s.AddMessage(domain.Message{Role: "user", Content: "remember this"})
			s.AddMessage(domain.Message{Role: "assistant", Content: "I will remember this"})
			return s, nil
		},
		runStreamFunc: func(ctx context.Context, goal string) (<-chan *agent.Event, error) {
			ch := make(chan *agent.Event)
			close(ch)
			return ch, nil
		},
	}

	server, clientConn, client := newTestACPBridge(t, func(ctx context.Context, cfg SessionConfig) (SessionRuntime, error) {
		return rt, nil
	})
	defer server.Close()

	_, err := clientConn.LoadSession(context.Background(), acp.LoadSessionRequest{
		Cwd:        "/tmp/project",
		McpServers: []acp.McpServer{},
		SessionId:  acp.SessionId("sess-load"),
	})
	if err != nil {
		t.Fatalf("load session: %v", err)
	}

	updates := client.waitForUpdates(t, 2)
	if len(updates) != 2 {
		t.Fatalf("expected 2 replayed updates, got %d", len(updates))
	}

	var sawUser, sawAssistant bool
	for _, update := range updates {
		switch {
		case update.Update.UserMessageChunk != nil && update.Update.UserMessageChunk.Content.Text.Text == "remember this":
			sawUser = true
		case update.Update.AgentMessageChunk != nil && update.Update.AgentMessageChunk.Content.Text.Text == "I will remember this":
			sawAssistant = true
		}
	}
	if !sawUser {
		t.Fatalf("expected replayed user message, got %#v", updates)
	}
	if !sawAssistant {
		t.Fatalf("expected replayed assistant message, got %#v", updates)
	}
}

func TestServerPromptCancellation(t *testing.T) {
	t.Parallel()

	rt := &fakeRuntime{
		getSessionFunc: func(sessionID string) (*agent.Session, error) {
			return agent.NewSessionWithID(sessionID, "agent"), nil
		},
		runStreamFunc: func(ctx context.Context, goal string) (<-chan *agent.Event, error) {
			ch := make(chan *agent.Event)
			go func() {
				defer close(ch)
				<-ctx.Done()
			}()
			return ch, nil
		},
	}

	server, clientConn, _ := newTestACPBridge(t, func(ctx context.Context, cfg SessionConfig) (SessionRuntime, error) {
		return rt, nil
	})
	defer server.Close()

	ctx := context.Background()
	newResp, err := clientConn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        "/tmp/project",
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	promptDone := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := clientConn.Prompt(ctx, acp.PromptRequest{
			SessionId: newResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("wait")},
		})
		promptDone <- resp
		promptErr <- err
	}()

	time.Sleep(50 * time.Millisecond)
	if err := clientConn.Cancel(ctx, acp.CancelNotification{SessionId: newResp.SessionId}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	select {
	case resp := <-promptDone:
		if err := <-promptErr; err != nil {
			t.Fatalf("prompt returned error: %v", err)
		}
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("expected cancelled stop reason, got %s", resp.StopReason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not return after cancellation")
	}
}

func TestServerPromptRequestsPermissionViaHooks(t *testing.T) {
	t.Parallel()

	rt := newFakeHookRuntime()
	rt.getSessionFunc = func(sessionID string) (*agent.Session, error) {
		return agent.NewSessionWithID(sessionID, "agent"), nil
	}
	rt.runStreamFunc = func(ctx context.Context, goal string) (<-chan *agent.Event, error) {
		ch := make(chan *agent.Event, 2)
		go func() {
			defer close(ch)
			ch <- &agent.Event{Type: agent.EventTypePartial, Content: "Starting "}
			data := agent.HookData{
				SessionID: rt.sessionID,
				ToolName:  "execute_javascript",
				ToolArgs:  map[string]interface{}{"code": "return 1"},
			}
			if err := rt.emitHook(ctx, agent.HookEventPreToolUse, data); err != nil {
				ch <- &agent.Event{Type: agent.EventTypeError, Content: err.Error()}
				return
			}
			data.ToolResult = "ok"
			if err := rt.emitHook(ctx, agent.HookEventPostToolUse, data); err != nil {
				ch <- &agent.Event{Type: agent.EventTypeError, Content: err.Error()}
				return
			}
			ch <- &agent.Event{Type: agent.EventTypeComplete, Content: "Finished"}
		}()
		return ch, nil
	}

	server, clientConn, client := newTestACPBridge(t, func(ctx context.Context, cfg SessionConfig) (SessionRuntime, error) {
		return rt, nil
	})
	defer server.Close()

	newResp, err := clientConn.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd:        "/tmp/project",
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	resp, err := clientConn.Prompt(context.Background(), acp.PromptRequest{
		SessionId: newResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("run the code")},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("unexpected stop reason: %s", resp.StopReason)
	}
	if client.permissionCount() != 1 {
		t.Fatalf("expected 1 permission request, got %d", client.permissionCount())
	}

	updates := client.waitForUpdates(t, 4)
	var hasPending, hasProgress, hasCompleted bool
	for _, update := range updates {
		if update.Update.ToolCall != nil && update.Update.ToolCall.Status == acp.ToolCallStatusPending {
			hasPending = true
		}
		if update.Update.ToolCallUpdate != nil && update.Update.ToolCallUpdate.Status != nil && *update.Update.ToolCallUpdate.Status == acp.ToolCallStatusInProgress {
			hasProgress = true
		}
		if update.Update.ToolCallUpdate != nil && update.Update.ToolCallUpdate.Status != nil && *update.Update.ToolCallUpdate.Status == acp.ToolCallStatusCompleted {
			hasCompleted = true
		}
	}
	if !hasPending || !hasProgress || !hasCompleted {
		t.Fatalf("expected pending/in_progress/completed tool lifecycle, got %#v", updates)
	}
}

func TestServerPromptRejectsPermissionViaHooks(t *testing.T) {
	t.Parallel()

	rt := newFakeHookRuntime()
	rt.getSessionFunc = func(sessionID string) (*agent.Session, error) {
		return agent.NewSessionWithID(sessionID, "agent"), nil
	}
	rt.runStreamFunc = func(ctx context.Context, goal string) (<-chan *agent.Event, error) {
		ch := make(chan *agent.Event, 2)
		go func() {
			defer close(ch)
			data := agent.HookData{
				SessionID: rt.sessionID,
				ToolName:  "execute_javascript",
				ToolArgs:  map[string]interface{}{"code": "return 1"},
			}
			if err := rt.emitHook(ctx, agent.HookEventPreToolUse, data); err != nil {
				ch <- &agent.Event{Type: agent.EventTypeError, Content: err.Error()}
				return
			}
			ch <- &agent.Event{Type: agent.EventTypeComplete, Content: "unexpected"}
		}()
		return ch, nil
	}

	server, clientConn, client := newTestACPBridge(t, func(ctx context.Context, cfg SessionConfig) (SessionRuntime, error) {
		return rt, nil
	})
	client.permissionOutcome = "reject"
	defer server.Close()

	newResp, err := clientConn.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd:        "/tmp/project",
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	_, err = clientConn.Prompt(context.Background(), acp.PromptRequest{
		SessionId: newResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("run the code")},
	})
	if err == nil {
		t.Fatal("expected prompt error when permission is rejected")
	}
	if client.permissionCount() != 1 {
		t.Fatalf("expected 1 permission request, got %d", client.permissionCount())
	}

	updates := client.waitForUpdates(t, 2)
	var hasFailed bool
	for _, update := range updates {
		if update.Update.ToolCallUpdate != nil && update.Update.ToolCallUpdate.Status != nil && *update.Update.ToolCallUpdate.Status == acp.ToolCallStatusFailed {
			hasFailed = true
		}
	}
	if !hasFailed {
		t.Fatalf("expected failed tool update after rejection, got %#v", updates)
	}
}

func newTestACPBridge(t *testing.T, factory SessionFactory) (*Server, *acp.ClientSideConnection, *testClient) {
	t.Helper()

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	server := New(factory, nil)
	agentConn := acp.NewAgentSideConnection(server, a2cW, c2aR)
	server.SetAgentConnection(agentConn)

	client := &testClient{}
	clientConn := acp.NewClientSideConnection(client, c2aW, a2cR)
	return server, clientConn, client
}
