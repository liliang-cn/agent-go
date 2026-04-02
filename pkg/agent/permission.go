package agent

import (
	"context"
	"strings"
)

// PermissionRequest describes a tool execution that may require approval.
type PermissionRequest struct {
	ToolName        string                 `json:"tool_name"`
	ToolArgs        map[string]interface{} `json:"tool_args,omitempty"`
	SessionID       string                 `json:"session_id,omitempty"`
	AgentID         string                 `json:"agent_id,omitempty"`
	ReadOnly        bool                   `json:"read_only,omitempty"`
	Destructive     bool                   `json:"destructive,omitempty"`
	ConcurrencySafe bool                   `json:"concurrency_safe,omitempty"`
}

// PermissionResponse is the decision returned by a PermissionHandler.
type PermissionResponse struct {
	Allowed  bool                   `json:"allowed"`
	Reason   string                 `json:"reason,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// PermissionHandler authorizes a tool execution at runtime.
type PermissionHandler func(ctx context.Context, req PermissionRequest) (*PermissionResponse, error)

// PermissionPolicy decides whether a tool execution needs approval.
type PermissionPolicy func(req PermissionRequest) bool

// DefaultPermissionPolicy marks risky tools as approval-gated.
func DefaultPermissionPolicy(req PermissionRequest) bool {
	if req.ReadOnly {
		return false
	}
	if req.Destructive {
		return true
	}
	lower := strings.ToLower(req.ToolName)
	switch {
	case strings.Contains(lower, "write"),
		strings.Contains(lower, "edit"),
		strings.Contains(lower, "update"),
		strings.Contains(lower, "delete"),
		strings.Contains(lower, "remove"),
		strings.Contains(lower, "create"),
		strings.Contains(lower, "ingest"),
		strings.Contains(lower, "execute"),
		strings.Contains(lower, "shell"),
		strings.Contains(lower, "bash"),
		strings.Contains(lower, "script"),
		strings.Contains(lower, "terminal"):
		return true
	default:
		return false
	}
}

func (s *Service) SetPermissionHandler(handler PermissionHandler) {
	s.permissionMu.Lock()
	defer s.permissionMu.Unlock()
	s.permissionHandler = handler
}

func (s *Service) SetPermissionPolicy(policy PermissionPolicy) {
	s.permissionMu.Lock()
	defer s.permissionMu.Unlock()
	s.permissionPolicy = policy
}

func (s *Service) authorizeTool(ctx context.Context, req PermissionRequest) error {
	s.permissionMu.RLock()
	handler := s.permissionHandler
	policy := s.permissionPolicy
	s.permissionMu.RUnlock()

	if handler == nil {
		return nil
	}
	if policy != nil && !policy(req) {
		return nil
	}

	resp, err := handler(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil || !resp.Allowed {
		if resp != nil && resp.Reason != "" {
			return PermissionDeniedError{Reason: resp.Reason}
		}
		return PermissionDeniedError{Reason: "permission denied"}
	}
	return nil
}

// PermissionDeniedError indicates a user or policy rejection.
type PermissionDeniedError struct {
	Reason string
}

func (e PermissionDeniedError) Error() string {
	if e.Reason == "" {
		return "permission denied"
	}
	return e.Reason
}
