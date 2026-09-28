package agent

import (
	"context"
	"time"
)

// Who approved this destructive call?
//
// The permission gate (authorizeTool) always made its decision and then
// forgot it: the handler's response — reason, metadata, who answered — was
// dropped the moment the call was let through or refused, and nothing reached
// an observer, a trace or a checkpoint. A run that deleted a file could say
// that it did, never who said it might.
//
// PermissionObserver is how the decision is kept. It is called once per tool
// call that reaches the gate, including the calls that needed no approval:
// "not gated" is said by a record that says so, never by the absence of one,
// because an absent record cannot be told apart from a lost one. The cost is
// one struct and a type assertion per tool call, and nothing at all when no
// observer implements the interface.

// PermissionDecision is what the gate decided.
type PermissionDecision string

const (
	// PermissionAllowed — approval was required and granted.
	PermissionAllowed PermissionDecision = "allowed"
	// PermissionDenied — approval was required and refused.
	PermissionDenied PermissionDecision = "denied"
	// PermissionNotRequired — the call ran without asking anyone.
	PermissionNotRequired PermissionDecision = "not_required"
	// PermissionDecisionError — the handler failed; the call did not run.
	PermissionDecisionError PermissionDecision = "error"
)

// PermissionDecider is which part of the gate decided.
type PermissionDecider string

const (
	// PermissionDeciderNone — no permission handler is configured, so every
	// call runs ungated. Recorded so a trace from such a service says so.
	PermissionDeciderNone PermissionDecider = "none"
	// PermissionDeciderPolicy — the PermissionPolicy said this call needs no
	// approval.
	PermissionDeciderPolicy PermissionDecider = "policy"
	// PermissionDeciderHandler — the PermissionHandler was asked.
	// PermissionDecisionInfo.DecidedBy names who answered, when the handler
	// said.
	PermissionDeciderHandler PermissionDecider = "handler"
)

// PermissionDecisionInfo is one permission decision.
type PermissionDecisionInfo struct {
	TaskID    string
	RunID     string
	SessionID string
	AgentName string
	AgentID   string
	CallID    string

	Tool string
	// Args are the arguments the call was decided on — the same map
	// OnToolStart carries, after pre_tool_use hooks ran.
	Args        map[string]any
	ReadOnly    bool
	Destructive bool

	// Required is true when the handler was asked.
	Required bool
	Decision PermissionDecision
	Decider  PermissionDecider
	// DecidedBy is PermissionResponse.DecidedBy: the handler's own name for
	// who answered. Empty when the handler did not say, or was not asked.
	DecidedBy string
	Reason    string
	Metadata  map[string]any
	Error     string
	// Duration is how long the handler took — for a human approver, how long
	// the run waited on a person. Zero when the handler was not asked.
	Duration time.Duration
}

// PermissionObserver receives every permission decision.
//
// It is deliberately NOT a method on Observer: hosts outside this repository
// implement Observer in full, and a new method would break every one that
// does not embed BaseObserver. Same rule as ResourceObserver.
//
//	type audit struct{ agent.BaseObserver }
//	func (audit) OnPermissionDecision(_ context.Context, d agent.PermissionDecisionInfo) { … }
type PermissionObserver interface {
	OnPermissionDecision(ctx context.Context, info PermissionDecisionInfo)
}

// emitPermissionDecision fills in the correlation ids and hands the decision
// to every observer that asked for them.
func (s *Service) emitPermissionDecision(ctx context.Context, currentAgent *Agent, session *Session, callID string, info PermissionDecisionInfo) {
	if s == nil {
		return
	}
	s.observersMu.RLock()
	var targets []Observer
	for _, o := range s.observers {
		if _, ok := o.(PermissionObserver); ok {
			targets = append(targets, o)
		}
	}
	s.observersMu.RUnlock()
	if len(targets) == 0 {
		return
	}

	info.CallID = callID
	info.RunID = currentRunID(ctx)
	info.TaskID = currentTaskID(session)
	if info.SessionID == "" && session != nil {
		info.SessionID = session.GetID()
	}
	if currentAgent != nil {
		info.AgentName = currentAgent.Name()
	}
	for _, o := range targets {
		po := o.(PermissionObserver)
		s.invokeObserver(o, func(Observer) { po.OnPermissionDecision(ctx, info) })
	}
}
