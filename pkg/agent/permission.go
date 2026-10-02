package agent

import (
	"context"
	"strings"
	"time"
)

// PermissionRequest describes a tool execution that may require approval.
type PermissionRequest struct {
	ToolName  string                 `json:"tool_name"`
	ToolArgs  map[string]interface{} `json:"tool_args,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	// TaskID is the run's task. For a standing responsibility's wake it is
	// the responsibility's id, so a host mapping an approval request back to
	// the responsibility that made it reads this rather than scanning
	// sessions.
	TaskID          string `json:"task_id,omitempty"`
	AgentID         string `json:"agent_id,omitempty"`
	ReadOnly        bool   `json:"read_only,omitempty"`
	Destructive     bool   `json:"destructive,omitempty"`
	ConcurrencySafe bool   `json:"concurrency_safe,omitempty"`
}

// PermissionResponse is the decision returned by a PermissionHandler.
type PermissionResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	// DecidedBy names who or what made the decision — a user id, an
	// approver's name, "auto-approve-reads". Opaque to the runtime, which
	// only carries it into the permission record so "who approved this
	// destructive call?" has an answer after the fact.
	DecidedBy string                 `json:"decided_by,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	// ContinueRun makes a denial the one tool call's result instead of the
	// end of the run: the model reads the reason and can try another way.
	// Without it a denial blocks the run, which is right when a person said
	// "stop" and wrong when nobody was there to answer — an unattended run
	// then throws away everything it did before the call it could not make.
	ContinueRun bool `json:"continue_run,omitempty"`
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

// authorizeTool is the permission gate. decideTool is the same decision with
// its provenance attached.
func (s *Service) authorizeTool(ctx context.Context, req PermissionRequest) error {
	_, err := s.decideTool(ctx, req)
	return err
}

// decideTool makes the permission decision and describes how it was made. The
// error is exactly what the gate has always returned; the description is a
// record of the decision, never an input to it.
func (s *Service) decideTool(ctx context.Context, req PermissionRequest) (PermissionDecisionInfo, error) {
	s.permissionMu.RLock()
	handler := s.permissionHandler
	policy := s.permissionPolicy
	s.permissionMu.RUnlock()

	info := PermissionDecisionInfo{
		Tool:        req.ToolName,
		Args:        req.ToolArgs,
		SessionID:   req.SessionID,
		AgentID:     req.AgentID,
		ReadOnly:    req.ReadOnly,
		Destructive: req.Destructive,
	}

	if handler == nil {
		info.Decision = PermissionNotRequired
		info.Decider = PermissionDeciderNone
		return info, nil
	}
	if policy != nil && !policy(req) {
		info.Decision = PermissionNotRequired
		info.Decider = PermissionDeciderPolicy
		return info, nil
	}

	info.Required = true
	info.Decider = PermissionDeciderHandler
	started := time.Now()
	resp, err := handler(ctx, req)
	info.Duration = time.Since(started)
	if resp != nil {
		info.DecidedBy = resp.DecidedBy
		info.Reason = resp.Reason
		info.Metadata = resp.Metadata
	}
	if err != nil {
		info.Decision = PermissionDecisionError
		info.Error = err.Error()
		return info, err
	}
	if resp == nil || !resp.Allowed {
		info.Decision = PermissionDenied
		if resp != nil && resp.ContinueRun {
			return info, PermissionRefusedError{Reason: resp.Reason}
		}
		if resp != nil && resp.Reason != "" {
			return info, PermissionDeniedError{Reason: resp.Reason}
		}
		info.Reason = "permission denied"
		return info, PermissionDeniedError{Reason: "permission denied"}
	}
	info.Decision = PermissionAllowed
	return info, nil
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

func (e PermissionDeniedError) BlockedReason() string {
	return e.Error()
}

// PermissionRefusedError is a denial that leaves the run going: see
// PermissionResponse.ContinueRun. It is deliberately not a blocker.
type PermissionRefusedError struct {
	Reason string
}

func (e PermissionRefusedError) Error() string {
	if e.Reason == "" {
		return "permission denied"
	}
	return "permission denied: " + e.Reason
}
