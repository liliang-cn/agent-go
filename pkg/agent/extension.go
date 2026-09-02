package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Extensions.
//
// The loop has a fixed shape — assemble context, call the model, execute
// tools, lint the answer, terminate — and a small number of seams where
// something outside the loop may take part. Every seam already existed as
// its own interface (Observer, OutputLint, Module, the hook registry); what
// did not exist was a way to ship one concern that touches several of them
// as one thing, in one registration, in one order. PII handling is the
// canonical case: it redacts tool results, rejects a final answer that leaks,
// and wants to be listed in the run's telemetry — three interfaces, three
// registrations, and nothing that says they belong together.
//
// An Extension is that bundle. It implements Name and whichever of the
// optional capabilities below it needs; Build() detects each one and wires it
// into the right seam. Extensions run in registration order at every seam.
//
// What an extension can do is exactly what the seam allows: contribute to the
// context (additively), rewrite or refuse a tool call, replace a tool result,
// reject a final answer, add tools, observe. There is deliberately no
// "next()" — an extension cannot wrap the loop, skip a stage, or call the
// model itself. That is the difference between this and a middleware chain,
// and it is what keeps the loop one loop.

// Extension is anything that plugs into one or more seams of the loop.
//
// The optional capabilities, detected by type assertion at Build():
//
//   - Observer            — see model turns, tool calls, retries, checkpoints
//   - OutputLint          — reject a final answer and force a retry
//   - Module              — add tools
//   - HookProvider        — register raw hooks on any HookEvent
//   - ContextContributor  — append system messages before the first turn
//   - ToolCallFilter      — rewrite the arguments of a tool call, or refuse it
//   - ToolResultFilter    — replace what the model sees as the tool's result
//   - RunLifecycle        — veto a run before its first turn; see how it ended
//   - Lifecycle           — start with Build(), stop with Close()
type Extension interface {
	// Name identifies the extension in logs, events and errors. Must be
	// non-empty and unique within a service.
	Name() string
}

// HookSpec is one hook an extension wants registered.
type HookSpec struct {
	Event   HookEvent
	Handler HookHandler
	// Priority orders this hook among all hooks for the event; lower runs
	// first. Zero means "after user-registered hooks, in extension order".
	Priority    int
	Description string
}

// HookProvider is an extension that registers raw hooks. It is the escape
// hatch for events the typed capabilities do not cover (stop, pre_compact,
// sub-agent lifecycle).
type HookProvider interface {
	Hooks() []HookSpec
}

// ContextInput is what a ContextContributor sees.
type ContextInput struct {
	Goal      string
	SessionID string
	AgentID   string
}

// ContextContributor appends system messages to a run before its first turn.
//
// It is additive on purpose. An extension that could rewrite the goal would
// be one hardcoded phrase table away from deciding behaviour from the user's
// wording, which the framework forbids everywhere else. Anything reaching the
// prompt prefix must be byte-stable across rounds, or it defeats the
// provider's prompt cache.
type ContextContributor interface {
	ContributeContext(ctx context.Context, in ContextInput) ([]domain.Message, error)
}

// ToolCallInfo describes a tool call about to run.
type ToolCallInfo struct {
	Name      string
	Args      map[string]interface{}
	SessionID string
	AgentID   string
}

// ToolVerdict is a ToolCallFilter's decision.
type ToolVerdict struct {
	// Args, when non-nil, replaces the call's arguments.
	Args map[string]interface{}
	// Block, when non-empty, refuses the call. The model sees the reason as
	// the tool's error and can choose another way; it does not see the
	// original result because there is none.
	Block string
}

// ToolCallFilter runs before a tool executes.
type ToolCallFilter interface {
	BeforeTool(ctx context.Context, call ToolCallInfo) (ToolVerdict, error)
}

// ToolResultInfo describes a tool call that has run.
type ToolResultInfo struct {
	Name      string
	Args      map[string]interface{}
	Result    interface{}
	Err       error
	SessionID string
	AgentID   string
}

// ToolResultFilter runs after a tool executes and before the model sees the
// result. Return replaced=true to substitute result; an error replaces the
// result with that error, so a filter that fails closed cannot leak what it
// was meant to hide.
type ToolResultFilter interface {
	AfterTool(ctx context.Context, res ToolResultInfo) (result interface{}, replaced bool, err error)
}

// Lifecycle is an extension that holds a resource — a connection, a file, a
// goroutine. Start runs at the end of a successful Build(), in extension
// order; Stop runs in Close(), in reverse order, after in-flight runs have
// been cancelled and before the store is released. A Start that fails fails
// the Build, and the extensions already started are stopped.
type Lifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// RunInfo identifies one run.
type RunInfo struct {
	Goal      string
	SessionID string
	AgentID   string
	TaskID    string
}

// RunOutcome is how a run ended.
type RunOutcome struct {
	StopReason StopReason
	// Text is the final answer, the blocker, or the cancellation notice.
	Text      string
	Blocked   bool
	Cancelled bool
	Duration  time.Duration
}

// RunLifecycle brackets each run. OnRunStart runs before the first turn and
// an error blocks the run with that reason — a budget that is already spent,
// a caller that is not allowed. OnRunEnd runs on every terminal path
// (completed, blocked, cancelled) and cannot change the outcome.
type RunLifecycle interface {
	OnRunStart(ctx context.Context, run RunInfo) error
	OnRunEnd(ctx context.Context, run RunInfo, outcome RunOutcome)
}

// extensionHookPriority places extension hooks after user-registered ones
// (default priority 100) and keeps them in registration order among
// themselves.
func extensionHookPriority(index int) int { return 1000 + index }

// installExtensions wires each extension into every seam it implements.
func installExtensions(svc *Service, exts []Extension) error {
	seen := map[string]bool{}
	for i, ext := range exts {
		if ext == nil {
			return fmt.Errorf("extensions[%d] is nil", i)
		}
		name := ext.Name()
		if name == "" {
			return fmt.Errorf("extensions[%d] (%T) has an empty name", i, ext)
		}
		if seen[name] {
			return fmt.Errorf("extension %q registered twice", name)
		}
		seen[name] = true
		prio := extensionHookPriority(i)

		if o, ok := ext.(Observer); ok {
			svc.RegisterObserver(o)
		}
		if l, ok := ext.(OutputLint); ok {
			svc.RegisterOutputLint(l)
		}
		if m, ok := ext.(Module); ok {
			if err := m.RegisterTools(svc.toolRegistry); err != nil {
				return fmt.Errorf("extension %q: register tools: %w", name, err)
			}
		}
		if hp, ok := ext.(HookProvider); ok {
			for _, spec := range hp.Hooks() {
				if spec.Handler == nil || spec.Event == "" {
					return fmt.Errorf("extension %q: hook spec needs an event and a handler", name)
				}
				p := spec.Priority
				if p == 0 {
					p = prio
				}
				svc.hooks.Register(spec.Event, spec.Handler,
					WithHookPriority(p), WithHookDescription(name+": "+spec.Description))
			}
		}
		if cc, ok := ext.(ContextContributor); ok {
			svc.hooks.Register(HookEventUserPromptSubmit, contextContributorHook(name, cc),
				WithHookPriority(prio), WithHookDescription(name+": context"))
		}
		if f, ok := ext.(ToolCallFilter); ok {
			svc.hooks.Register(HookEventPreToolUse, toolCallFilterHook(name, f),
				WithHookPriority(prio), WithHookDescription(name+": before tool"))
		}
		if f, ok := ext.(ToolResultFilter); ok {
			svc.hooks.Register(HookEventPostToolUse, toolResultFilterHook(name, f),
				WithHookPriority(prio), WithHookDescription(name+": after tool"))
		}
		if rl, ok := ext.(RunLifecycle); ok {
			svc.hooks.Register(HookEventUserPromptSubmit, runStartHook(name, rl),
				WithHookPriority(prio), WithHookDescription(name+": run start"))
			svc.hooks.Register(HookEventPostExecution, runEndHook(rl),
				WithHookPriority(prio), WithHookDescription(name+": run end"))
		}
		svc.extensions = append(svc.extensions, ext)
	}
	return nil
}

// startExtensions runs Start on every Lifecycle extension, in order. On
// failure the ones already started are stopped, in reverse.
func (s *Service) startExtensions(ctx context.Context) error {
	var started []Lifecycle
	for _, ext := range s.extensions {
		lc, ok := ext.(Lifecycle)
		if !ok {
			continue
		}
		if err := lc.Start(ctx); err != nil {
			for i := len(started) - 1; i >= 0; i-- {
				_ = started[i].Stop(ctx)
			}
			return fmt.Errorf("extension %q: start: %w", ext.Name(), err)
		}
		started = append(started, lc)
	}
	return nil
}

// stopExtensions runs Stop on every Lifecycle extension, in reverse order,
// and reports every failure rather than the first.
func (s *Service) stopExtensions(ctx context.Context) error {
	var errs []error
	for i := len(s.extensions) - 1; i >= 0; i-- {
		if lc, ok := s.extensions[i].(Lifecycle); ok {
			if err := lc.Stop(ctx); err != nil {
				errs = append(errs, fmt.Errorf("extension %q: stop: %w", s.extensions[i].Name(), err))
			}
		}
	}
	return errors.Join(errs...)
}

func runInfoFrom(data HookData) RunInfo {
	return RunInfo{Goal: data.Goal, SessionID: data.SessionID, AgentID: data.AgentID, TaskID: data.TaskID}
}

func runStartHook(name string, rl RunLifecycle) HookHandler {
	return func(ctx context.Context, _ HookEvent, data HookData) (interface{}, error) {
		if err := rl.OnRunStart(ctx, runInfoFrom(data)); err != nil {
			return nil, fmt.Errorf("extension %q: %w", name, err)
		}
		return nil, nil
	}
}

func runEndHook(rl RunLifecycle) HookHandler {
	return func(ctx context.Context, _ HookEvent, data HookData) (interface{}, error) {
		text, _ := data.Result.(string)
		blocked, _ := data.Metadata["blocked"].(bool)
		cancelled, _ := data.Metadata["cancelled"].(bool)
		rl.OnRunEnd(ctx, runInfoFrom(data), RunOutcome{
			StopReason: StopReason(data.StopReason), Text: text,
			Blocked: blocked, Cancelled: cancelled, Duration: data.Duration,
		})
		return nil, nil
	}
}

func contextContributorHook(name string, cc ContextContributor) HookHandler {
	return func(ctx context.Context, _ HookEvent, data HookData) (interface{}, error) {
		msgs, err := cc.ContributeContext(ctx, ContextInput{
			Goal: data.Goal, SessionID: data.SessionID, AgentID: data.AgentID,
		})
		if err != nil {
			return nil, fmt.Errorf("extension %q: %w", name, err)
		}
		for _, m := range msgs {
			if m.Role == "" {
				m.Role = "system"
			}
			data.AdditionalSystemMessages = append(data.AdditionalSystemMessages, m)
		}
		return data, nil
	}
}

func toolCallFilterHook(name string, f ToolCallFilter) HookHandler {
	return func(ctx context.Context, _ HookEvent, data HookData) (interface{}, error) {
		verdict, err := f.BeforeTool(ctx, ToolCallInfo{
			Name: data.ToolName, Args: data.ToolArgs, SessionID: data.SessionID, AgentID: data.AgentID,
		})
		if err != nil {
			return nil, fmt.Errorf("extension %q: %w", name, err)
		}
		if verdict.Block != "" {
			slog.Info("extension refused tool call", "module", "agent.extensions",
				"extension", name, "tool", data.ToolName, "reason", verdict.Block)
			return nil, fmt.Errorf("tool %q refused by extension %q: %s", data.ToolName, name, verdict.Block)
		}
		if verdict.Args != nil {
			slog.Debug("extension rewrote tool arguments", "module", "agent.extensions",
				"extension", name, "tool", data.ToolName)
			data.ToolArgs = verdict.Args
		}
		return data, nil
	}
}

func toolResultFilterHook(name string, f ToolResultFilter) HookHandler {
	return func(ctx context.Context, _ HookEvent, data HookData) (interface{}, error) {
		res, replaced, err := f.AfterTool(ctx, ToolResultInfo{
			Name: data.ToolName, Args: data.ToolArgs, Result: data.ToolResult, Err: data.ToolError,
			SessionID: data.SessionID, AgentID: data.AgentID,
		})
		if err != nil {
			return nil, fmt.Errorf("extension %q: %w", name, err)
		}
		if replaced {
			slog.Debug("extension replaced tool result", "module", "agent.extensions",
				"extension", name, "tool", data.ToolName)
			data.ToolResult = res
		}
		return data, nil
	}
}

// Extensions lists the extensions installed on this service, in the order
// they run.
func (s *Service) Extensions() []Extension {
	out := make([]Extension, len(s.extensions))
	copy(out, s.extensions)
	return out
}
