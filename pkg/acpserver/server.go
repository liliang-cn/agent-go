package acpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

// SessionRuntime is the subset of agent.Service needed by the ACP bridge.
type SessionRuntime interface {
	SetSessionID(sessionID string)
	GetSession(sessionID string) (*agent.Session, error)
	RunStream(ctx context.Context, goal string) (<-chan *agent.Event, error)
	Close() error
}

// HookRuntime exposes tool lifecycle hooks for runtimes that support pre/post execution interception.
type HookRuntime interface {
	SessionRuntime
	RegisterHook(event agent.HookEvent, handler agent.HookHandler, opts ...agent.HookOption) string
	UnregisterHook(hookID string) bool
}

// PermissionRuntime exposes runtime permission controls when supported by the session implementation.
type PermissionRuntime interface {
	SessionRuntime
	SetPermissionHandler(handler agent.PermissionHandler)
	SetPermissionPolicy(policy agent.PermissionPolicy)
}

// SessionConfig contains the data needed to create a new ACP session runtime.
type SessionConfig struct {
	CWD        string
	MCPServers []acp.McpServer
	SessionID  string
}

// SessionFactory creates a fresh runtime for a single ACP session.
type SessionFactory func(ctx context.Context, cfg SessionConfig) (SessionRuntime, error)

type sessionState struct {
	cwd     string
	runtime SessionRuntime
	prompt  sync.Mutex
}

// Server adapts agent-go's runtime loop to the ACP Go SDK interfaces.
type Server struct {
	conn    *acp.AgentSideConnection
	factory SessionFactory
	logger  *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*sessionState
}

var _ acp.Agent = (*Server)(nil)
var _ acp.AgentLoader = (*Server)(nil)

// New creates a new ACP server bridge.
func New(factory SessionFactory, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		factory:  factory,
		logger:   logger,
		sessions: make(map[string]*sessionState),
	}
}

// SetAgentConnection attaches the active ACP transport connection.
func (s *Server) SetAgentConnection(conn *acp.AgentSideConnection) {
	s.conn = conn
}

// Close releases all per-session runtimes.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for id, session := range s.sessions {
		if err := session.runtime.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close session %s: %w", id, err)
		}
		delete(s.sessions, id)
	}
	return firstErr
}

// Authenticate implements acp.Agent.
func (s *Server) Authenticate(ctx context.Context, params acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

// Initialize implements acp.Agent.
func (s *Server) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			McpCapabilities: acp.McpCapabilities{
				Http: true,
				Sse:  true,
			},
			PromptCapabilities: acp.PromptCapabilities{
				EmbeddedContext: true,
			},
		},
	}, nil
}

// NewSession implements acp.Agent.
func (s *Server) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	_ = params.McpServers // AgentGo currently uses its own MCP config, not ACP-passed server descriptors.

	sessionID := uuid.NewString()
	rt, err := s.factory(ctx, SessionConfig{
		CWD:        params.Cwd,
		MCPServers: params.McpServers,
		SessionID:  sessionID,
	})
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	rt.SetSessionID(sessionID)

	s.mu.Lock()
	s.sessions[sessionID] = &sessionState{
		cwd:     params.Cwd,
		runtime: rt,
	}
	s.mu.Unlock()

	return acp.NewSessionResponse{SessionId: acp.SessionId(sessionID)}, nil
}

// LoadSession implements acp.AgentLoader.
func (s *Server) LoadSession(ctx context.Context, params acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	_ = params.McpServers

	sessionID := string(params.SessionId)
	rt, err := s.factory(ctx, SessionConfig{
		CWD:        params.Cwd,
		MCPServers: params.McpServers,
		SessionID:  sessionID,
	})
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	rt.SetSessionID(sessionID)

	history, err := rt.GetSession(sessionID)
	if err != nil {
		_ = rt.Close()
		return acp.LoadSessionResponse{}, fmt.Errorf("load session %s: %w", sessionID, err)
	}

	s.mu.Lock()
	if existing := s.sessions[sessionID]; existing != nil {
		_ = existing.runtime.Close()
	}
	s.sessions[sessionID] = &sessionState{
		cwd:     params.Cwd,
		runtime: rt,
	}
	s.mu.Unlock()

	if err := s.replayHistory(ctx, params.SessionId, history); err != nil {
		return acp.LoadSessionResponse{}, err
	}

	return acp.LoadSessionResponse{}, nil
}

// Prompt implements acp.Agent.
func (s *Server) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	session, ok := s.getSession(string(params.SessionId))
	if !ok {
		return acp.PromptResponse{}, fmt.Errorf("session %s not found", params.SessionId)
	}

	session.prompt.Lock()
	defer session.prompt.Unlock()

	var toolState *permissionState
	useHookToolLifecycle := false
	if hookRuntime, ok := session.runtime.(HookRuntime); ok {
		useHookToolLifecycle = true
		toolState = newPermissionState()
		_, hasPermissionRuntime := session.runtime.(PermissionRuntime)
		cleanup := s.installToolHooks(hookRuntime, params.SessionId, toolState, !hasPermissionRuntime)
		defer cleanup()
	}
	if permissionRuntime, ok := session.runtime.(PermissionRuntime); ok {
		permissionRuntime.SetPermissionPolicy(agent.DefaultPermissionPolicy)
		permissionRuntime.SetPermissionHandler(func(ctx context.Context, req agent.PermissionRequest) (*agent.PermissionResponse, error) {
			callID := acp.ToolCallId(toolQueueKey(req.ToolName, req.ToolArgs))
			if toolState != nil {
				if queuedID := toolState.current(req.ToolName, req.ToolArgs); queuedID != "" {
					callID = queuedID
				}
			}
			resp, err := s.requestToolPermission(ctx, params.SessionId, callID, req)
			if err != nil {
				return nil, err
			}
			if !resp.Allowed {
				if callID != "" {
					if updateErr := s.sendUpdate(ctx, params.SessionId, acp.UpdateToolCall(
						callID,
						acp.WithUpdateStatus(acp.ToolCallStatusFailed),
						acp.WithUpdateContent([]acp.ToolCallContent{
							acp.ToolContent(acp.TextBlock(resp.Reason)),
						}),
					)); updateErr != nil {
						return nil, updateErr
					}
				}
				return resp, nil
			}
			if callID != "" {
				if err := s.sendUpdate(ctx, params.SessionId, acp.UpdateToolCall(
					callID,
					acp.WithUpdateStatus(acp.ToolCallStatusInProgress),
				)); err != nil {
					return nil, err
				}
			}
			return resp, nil
		})
	}

	promptText := renderPrompt(params.Prompt)
	if strings.TrimSpace(promptText) == "" {
		return acp.PromptResponse{}, fmt.Errorf("prompt did not contain supported text content")
	}

	events, err := session.runtime.RunStream(ctx, promptText)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	fallbackToolCalls := make(map[string][]acp.ToolCallId)
	var fallbackToolSeq int
	var streamedAssistant bool

	for evt := range events {
		switch evt.Type {
		case agent.EventTypeThinking:
			if evt.Content == "" {
				continue
			}
			if err := s.sendUpdate(ctx, params.SessionId, acp.UpdateAgentThoughtText(evt.Content)); err != nil {
				return acp.PromptResponse{}, err
			}
		case agent.EventTypePartial:
			if evt.Content == "" {
				continue
			}
			streamedAssistant = true
			if err := s.sendUpdate(ctx, params.SessionId, acp.UpdateAgentMessageText(evt.Content)); err != nil {
				return acp.PromptResponse{}, err
			}
		case agent.EventTypeToolCall:
			if useHookToolLifecycle {
				continue
			}
			fallbackToolSeq++
			callID := acp.ToolCallId(fmt.Sprintf("tool_%03d", fallbackToolSeq))
			fallbackToolCalls[evt.ToolName] = append(fallbackToolCalls[evt.ToolName], callID)
			if err := s.sendUpdate(ctx, params.SessionId, acp.StartToolCall(
				callID,
				toolTitle(evt.ToolName, evt.ToolArgs),
				acp.WithStartKind(toolKind(evt.ToolName)),
				acp.WithStartStatus(acp.ToolCallStatusPending),
				acp.WithStartRawInput(evt.ToolArgs),
			)); err != nil {
				return acp.PromptResponse{}, err
			}
		case agent.EventTypeToolResult:
			if useHookToolLifecycle {
				continue
			}
			callID := dequeueToolCallID(fallbackToolCalls, evt.ToolName)
			if callID == "" {
				fallbackToolSeq++
				callID = acp.ToolCallId(fmt.Sprintf("tool_%03d", fallbackToolSeq))
			}
			opts := []acp.ToolCallUpdateOpt{
				acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
				acp.WithUpdateRawOutput(evt.ToolResult),
			}
			if evt.Content != "" {
				opts[0] = acp.WithUpdateStatus(acp.ToolCallStatusFailed)
				opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{
					acp.ToolContent(acp.TextBlock(evt.Content)),
				}))
			} else if text := stringifyToolResult(evt.ToolResult); text != "" {
				opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{
					acp.ToolContent(acp.TextBlock(text)),
				}))
			}
			if err := s.sendUpdate(ctx, params.SessionId, acp.UpdateToolCall(callID, opts...)); err != nil {
				return acp.PromptResponse{}, err
			}
		case agent.EventTypeComplete:
			if !streamedAssistant && evt.Content != "" {
				if err := s.sendUpdate(ctx, params.SessionId, acp.UpdateAgentMessageText(evt.Content)); err != nil {
					return acp.PromptResponse{}, err
				}
			}
		case agent.EventTypeError:
			if context.Cause(ctx) != nil {
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}
			return acp.PromptResponse{}, fmt.Errorf("agent run failed: %s", evt.Content)
		}
	}

	if context.Cause(ctx) != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}

	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

// Cancel implements acp.Agent.
func (s *Server) Cancel(ctx context.Context, params acp.CancelNotification) error {
	return nil
}

// SetSessionMode implements acp.Agent.
func (s *Server) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (s *Server) getSession(id string) (*sessionState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	return session, ok
}

func (s *Server) replayHistory(ctx context.Context, sessionID acp.SessionId, history *agent.Session) error {
	for _, msg := range history.GetMessages() {
		switch msg.Role {
		case "user":
			if err := s.sendUpdate(ctx, sessionID, acp.UpdateUserMessageText(msg.Content)); err != nil {
				return err
			}
		case "assistant":
			if err := s.sendUpdate(ctx, sessionID, acp.UpdateAgentMessageText(msg.Content)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) sendUpdate(ctx context.Context, sessionID acp.SessionId, update acp.SessionUpdate) error {
	if s.conn == nil {
		return fmt.Errorf("agent connection not configured")
	}
	return s.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: sessionID,
		Update:    update,
	})
}

func renderPrompt(blocks []acp.ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			if text := strings.TrimSpace(block.Text.Text); text != "" {
				parts = append(parts, text)
			}
		case block.Resource != nil && block.Resource.Resource.TextResourceContents != nil:
			res := block.Resource.Resource.TextResourceContents
			parts = append(parts, fmt.Sprintf("Context from %s:\n%s", res.Uri, res.Text))
		case block.ResourceLink != nil:
			parts = append(parts, fmt.Sprintf("Linked resource %s: %s", block.ResourceLink.Name, block.ResourceLink.Uri))
		}
	}
	return strings.Join(parts, "\n\n")
}

func toolTitle(name string, args map[string]interface{}) string {
	if len(args) == 0 {
		return name
	}
	return fmt.Sprintf("%s %s", name, compactJSON(args))
}

func toolKind(name string) acp.ToolKind {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "read"), strings.Contains(lower, "search"), strings.Contains(lower, "query"), strings.Contains(lower, "list"):
		return acp.ToolKindRead
	case strings.Contains(lower, "write"), strings.Contains(lower, "edit"), strings.Contains(lower, "update"), strings.Contains(lower, "delete"), strings.Contains(lower, "ingest"):
		return acp.ToolKindEdit
	default:
		return acp.ToolKindExecute
	}
}

func dequeueToolCallID(byName map[string][]acp.ToolCallId, name string) acp.ToolCallId {
	queue := byName[name]
	if len(queue) == 0 {
		return ""
	}
	id := queue[0]
	if len(queue) == 1 {
		delete(byName, name)
	} else {
		byName[name] = queue[1:]
	}
	return id
}

func stringifyToolResult(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return compactJSON(v)
	}
}

func compactJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func (s *Server) installToolHooks(runtime HookRuntime, sessionID acp.SessionId, state *permissionState, requestPermissionInHook bool) func() {
	preHookID := runtime.RegisterHook(agent.HookEventPreToolUse, func(ctx context.Context, event agent.HookEvent, data agent.HookData) (interface{}, error) {
		if data.SessionID != string(sessionID) {
			return data, nil
		}

		callID := state.start(data.ToolName, data.ToolArgs)
		if err := s.sendUpdate(ctx, sessionID, acp.StartToolCall(
			callID,
			toolTitle(data.ToolName, data.ToolArgs),
			acp.WithStartKind(toolKind(data.ToolName)),
			acp.WithStartStatus(acp.ToolCallStatusPending),
			acp.WithStartRawInput(data.ToolArgs),
		)); err != nil {
			return nil, err
		}

		req := agent.PermissionRequest{
			ToolName:  data.ToolName,
			ToolArgs:  data.ToolArgs,
			SessionID: data.SessionID,
			AgentID:   data.AgentID,
		}
		if requestPermissionInHook && agent.DefaultPermissionPolicy(req) {
			resp, err := s.requestToolPermission(ctx, sessionID, callID, req)
			if err != nil {
				return nil, err
			}
			if !resp.Allowed {
				if err := s.sendUpdate(ctx, sessionID, acp.UpdateToolCall(
					callID,
					acp.WithUpdateStatus(acp.ToolCallStatusFailed),
					acp.WithUpdateContent([]acp.ToolCallContent{
						acp.ToolContent(acp.TextBlock(resp.Reason)),
					}),
				)); err != nil {
					return nil, err
				}
				return nil, agent.PermissionDeniedError{Reason: resp.Reason}
			}
		}

		if err := s.sendUpdate(ctx, sessionID, acp.UpdateToolCall(
			callID,
			acp.WithUpdateStatus(acp.ToolCallStatusInProgress),
		)); err != nil {
			return nil, err
		}
		return data, nil
	})

	postHookID := runtime.RegisterHook(agent.HookEventPostToolUse, func(ctx context.Context, event agent.HookEvent, data agent.HookData) (interface{}, error) {
		if data.SessionID != string(sessionID) {
			return data, nil
		}

		callID := state.finish(data.ToolName, data.ToolArgs)
		if callID == "" {
			return data, nil
		}

		opts := []acp.ToolCallUpdateOpt{
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateRawOutput(data.ToolResult),
		}
		if data.ToolError != nil {
			opts[0] = acp.WithUpdateStatus(acp.ToolCallStatusFailed)
			opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{
				acp.ToolContent(acp.TextBlock(data.ToolError.Error())),
			}))
		} else if text := stringifyToolResult(data.ToolResult); text != "" {
			opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{
				acp.ToolContent(acp.TextBlock(text)),
			}))
		}

		if err := s.sendUpdate(ctx, sessionID, acp.UpdateToolCall(callID, opts...)); err != nil {
			return data, err
		}
		return data, nil
	})

	return func() {
		runtime.UnregisterHook(preHookID)
		runtime.UnregisterHook(postHookID)
	}
}

type permissionState struct {
	mu      sync.Mutex
	nextSeq int
	queues  map[string][]acp.ToolCallId
}

func newPermissionState() *permissionState {
	return &permissionState{queues: make(map[string][]acp.ToolCallId)}
}

func (p *permissionState) start(name string, args map[string]interface{}) acp.ToolCallId {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextSeq++
	callID := acp.ToolCallId(fmt.Sprintf("tool_%03d", p.nextSeq))
	key := toolQueueKey(name, args)
	p.queues[key] = append(p.queues[key], callID)
	return callID
}

func (p *permissionState) finish(name string, args map[string]interface{}) acp.ToolCallId {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := toolQueueKey(name, args)
	queue := p.queues[key]
	if len(queue) == 0 {
		return ""
	}
	callID := queue[0]
	if len(queue) == 1 {
		delete(p.queues, key)
	} else {
		p.queues[key] = queue[1:]
	}
	return callID
}

func (p *permissionState) current(name string, args map[string]interface{}) acp.ToolCallId {
	p.mu.Lock()
	defer p.mu.Unlock()
	queue := p.queues[toolQueueKey(name, args)]
	if len(queue) == 0 {
		return ""
	}
	return queue[0]
}

func toolQueueKey(name string, args map[string]interface{}) string {
	return name + ":" + compactJSON(args)
}

func (s *Server) requestToolPermission(ctx context.Context, sessionID acp.SessionId, callID acp.ToolCallId, req agent.PermissionRequest) (*agent.PermissionResponse, error) {
	if s.conn == nil {
		return &agent.PermissionResponse{Allowed: true}, nil
	}

	resp, err := s.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: sessionID,
		ToolCall: acp.RequestPermissionToolCall{
			ToolCallId: callID,
			Title:      acp.Ptr(toolTitle(req.ToolName, req.ToolArgs)),
			Kind:       acp.Ptr(toolKind(req.ToolName)),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
			RawInput:   req.ToolArgs,
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.Outcome.Cancelled != nil {
		return &agent.PermissionResponse{Allowed: false, Reason: "permission request cancelled"}, nil
	}
	if resp.Outcome.Selected != nil && string(resp.Outcome.Selected.OptionId) == "allow" {
		return &agent.PermissionResponse{Allowed: true}, nil
	}
	return &agent.PermissionResponse{Allowed: false, Reason: "permission denied by user"}, nil
}
