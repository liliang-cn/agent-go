package agent

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// HookEvent defines the type of hook event
type HookEvent string

const (
	// HookEventPreToolUse fires before a tool is executed
	// Can modify tool arguments or block execution by returning error
	HookEventPreToolUse HookEvent = "pre_tool_use"

	// HookEventPostToolUse fires after a tool completes
	// Receives the result and error from tool execution
	HookEventPostToolUse HookEvent = "post_tool_use"

	// HookEventSubagentStart fires when a sub-agent begins execution
	HookEventSubagentStart HookEvent = "subagent_start"

	// HookEventSubagentStop fires when a sub-agent completes or fails
	HookEventSubagentStop HookEvent = "subagent_stop"

	// HookEventSubagentCancel fires when a sub-agent is cancelled
	HookEventSubagentCancel HookEvent = "subagent_cancel"

	// HookEventSubagentProgress fires when a sub-agent makes progress
	HookEventSubagentProgress HookEvent = "subagent_progress"

	// HookEventPostExecution fires after the entire agent run completes
	HookEventPostExecution HookEvent = "post_execution"

	// HookEventStop fires at the end of each turn before continuation decision
	// Can block continuation by returning HookResult with PreventContinuation=true
	HookEventStop HookEvent = "stop"

	// HookEventTurnComplete fires when a turn completes successfully
	HookEventTurnComplete HookEvent = "turn_complete"
)

// HookData contains data passed to hook handlers
type HookData struct {
	// Tool hooks
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolArgs   map[string]interface{} `json:"tool_args,omitempty"`
	ToolResult interface{}            `json:"tool_result,omitempty"`
	ToolError  error                  `json:"tool_error,omitempty"`

	// SubAgent hooks
	SubagentID   string        `json:"subagent_id,omitempty"`
	SubagentName string        `json:"subagent_name,omitempty"`
	Goal         string        `json:"goal,omitempty"`
	Result       interface{}   `json:"result,omitempty"`
	Error        error         `json:"error,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`

	// Context
	SessionID string                 `json:"session_id,omitempty"`
	AgentID   string                 `json:"agent_id,omitempty"`
	Timestamp time.Time              `json:"timestamp,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`

	// Stop hook fields
	PreventContinuation bool   `json:"prevent_continuation,omitempty"`
	StopReason          string `json:"stop_reason,omitempty"`
	HookOutput          string `json:"hook_output,omitempty"`
	HookError           string `json:"hook_error,omitempty"`
	HookExitCode        int    `json:"hook_exit_code,omitempty"`
	DurationMs          int64  `json:"duration_ms,omitempty"`
}

// StopHookResult is returned by stop hooks to control continuation behavior
type StopHookResult struct {
	// PreventContinuation if true, stops the agent loop from continuing
	PreventContinuation bool `json:"prevent_continuation"`
	// StopReason describes why continuation was prevented
	StopReason string `json:"stop_reason,omitempty"`
	// BlockingError is an error message to display to the user (blocks with message)
	BlockingError string `json:"blocking_error,omitempty"`
	// HookOutput captures stdout from the hook command
	HookOutput string `json:"hook_output,omitempty"`
	// HookError captures stderr from the hook command
	HookError string `json:"hook_error,omitempty"`
	// ExitCode from the hook command
	ExitCode int `json:"exit_code,omitempty"`
	// DurationMs execution time of the hook
	DurationMs int64 `json:"duration_ms,omitempty"`
}

// HookHandler is a function that handles a hook event
// Returns modified HookData to pass to next hook, or error to block execution
type HookHandler func(ctx context.Context, event HookEvent, data HookData) (interface{}, error)

// HookMatcher determines if a hook should run for a given event
type HookMatcher interface {
	Match(event HookEvent, data HookData) bool
}

// Hook represents a registered hook
type Hook struct {
	ID          string      `json:"id"`
	Event       HookEvent   `json:"event"`
	Handler     HookHandler `json:"-"`
	Matcher     HookMatcher `json:"-"`
	Priority    int         `json:"priority"` // Lower = higher priority
	Enabled     bool        `json:"enabled"`
	Description string      `json:"description"`
	CreatedAt   time.Time   `json:"created_at"`
}

// HookOption configures a Hook
type HookOption func(*Hook)

// HookRegistry manages all hooks
type HookRegistry struct {
	mu     sync.RWMutex
	hooks  map[HookEvent][]*Hook
	global []*Hook // Hooks for all events
}

var (
	globalRegistry *HookRegistry
	globalOnce     sync.Once
)

// GlobalHookRegistry returns the global hook registry singleton
func GlobalHookRegistry() *HookRegistry {
	globalOnce.Do(func() {
		globalRegistry = NewHookRegistry()
	})
	return globalRegistry
}

// NewHookRegistry creates a new hook registry
func NewHookRegistry() *HookRegistry {
	return &HookRegistry{
		hooks:  make(map[HookEvent][]*Hook),
		global: []*Hook{},
	}
}

// Register registers a new hook and returns its ID
func (r *HookRegistry) Register(event HookEvent, handler HookHandler, opts ...HookOption) string {
	hook := &Hook{
		ID:        uuid.New().String(),
		Event:     event,
		Handler:   handler,
		Priority:  100, // Default priority
		Enabled:   true,
		CreatedAt: time.Now(),
	}

	for _, opt := range opts {
		opt(hook)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if event == "" {
		// Global hook - runs for all events
		r.global = append(r.global, hook)
		sortHooks(r.global)
	} else {
		r.hooks[event] = append(r.hooks[event], hook)
		sortHooks(r.hooks[event])
	}

	return hook.ID
}

// Emit emits an event to all matching hooks (non-blocking, doesn't wait for results)
func (r *HookRegistry) Emit(event HookEvent, data HookData) []interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	data.Timestamp = time.Now()
	var results []interface{}

	// Run global hooks first
	for _, hook := range r.global {
		if !hook.Enabled {
			continue
		}
		if hook.Matcher != nil && !hook.Matcher.Match(event, data) {
			continue
		}

		ctx := context.Background()
		result, err := hook.Handler(ctx, event, data)
		if err != nil {
			log.Printf("[Hook] Hook %s error: %v", hook.ID, err)
		}
		if result != nil {
			results = append(results, result)
		}
	}

	// Run event-specific hooks
	for _, hook := range r.hooks[event] {
		if !hook.Enabled {
			continue
		}
		if hook.Matcher != nil && !hook.Matcher.Match(event, data) {
			continue
		}

		ctx := context.Background()
		result, err := hook.Handler(ctx, event, data)
		if err != nil {
			log.Printf("[Hook] Hook %s error: %v", hook.ID, err)
		}
		if result != nil {
			results = append(results, result)
		}
	}

	return results
}

// EmitWithResult emits an event and processes results (blocking)
// Returns modified HookData and any blocking error
func (r *HookRegistry) EmitWithResult(ctx context.Context, event HookEvent, data HookData) (HookData, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	data.Timestamp = time.Now()

	// Run global hooks first
	for _, hook := range r.global {
		if !hook.Enabled {
			continue
		}
		if hook.Matcher != nil && !hook.Matcher.Match(event, data) {
			continue
		}

		result, err := hook.Handler(ctx, event, data)
		if err != nil {
			return data, err // Block on error
		}

		// Check for data modifications
		if modified, ok := result.(HookData); ok {
			data = modified
		}
	}

	// Run event-specific hooks
	for _, hook := range r.hooks[event] {
		if !hook.Enabled {
			continue
		}
		if hook.Matcher != nil && !hook.Matcher.Match(event, data) {
			continue
		}

		result, err := hook.Handler(ctx, event, data)
		if err != nil {
			return data, err // Block on error
		}

		// Check for data modifications
		if modified, ok := result.(HookData); ok {
			data = modified
		}
	}

	return data, nil
}

// Unregister removes a hook by ID
func (r *HookRegistry) Unregister(hookID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove from event-specific hooks
	for event, hooks := range r.hooks {
		for i, hook := range hooks {
			if hook.ID == hookID {
				r.hooks[event] = append(hooks[:i], hooks[i+1:]...)
				return true
			}
		}
	}

	// Remove from global hooks
	for i, hook := range r.global {
		if hook.ID == hookID {
			r.global = append(r.global[:i], r.global[i+1:]...)
			return true
		}
	}

	return false
}

// Get returns a hook by ID
func (r *HookRegistry) Get(hookID string) (*Hook, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Check event-specific hooks
	for _, hooks := range r.hooks {
		for _, hook := range hooks {
			if hook.ID == hookID {
				return hook, true
			}
		}
	}

	// Check global hooks
	for _, hook := range r.global {
		if hook.ID == hookID {
			return hook, true
		}
	}

	return nil, false
}

// List returns all hooks for a specific event (or all if event is empty)
func (r *HookRegistry) List(event HookEvent) []*Hook {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if event == "" {
		// Return all hooks
		var all []*Hook
		all = append(all, r.global...)
		for _, hooks := range r.hooks {
			all = append(all, hooks...)
		}
		return all
	}

	result := make([]*Hook, len(r.hooks[event]))
	copy(result, r.hooks[event])
	return result
}

// Enable enables a hook by ID
func (r *HookRegistry) Enable(hookID string) bool {
	if hook, ok := r.Get(hookID); ok {
		hook.Enabled = true
		return true
	}
	return false
}

// Disable disables a hook by ID
func (r *HookRegistry) Disable(hookID string) bool {
	if hook, ok := r.Get(hookID); ok {
		hook.Enabled = false
		return true
	}
	return false
}

// Clear removes all hooks
func (r *HookRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.hooks = make(map[HookEvent][]*Hook)
	r.global = []*Hook{}
}

// sortHooks sorts hooks by priority (lower = higher priority)
func sortHooks(hooks []*Hook) {
	sort.Slice(hooks, func(i, j int) bool {
		return hooks[i].Priority < hooks[j].Priority
	})
}

// --- Hook Options ---

// WithHookPriority sets hook priority (lower = higher priority)
func WithHookPriority(priority int) HookOption {
	return func(h *Hook) {
		h.Priority = priority
	}
}

// WithHookMatcher sets a custom matcher for conditional execution
func WithHookMatcher(matcher HookMatcher) HookOption {
	return func(h *Hook) {
		h.Matcher = matcher
	}
}

// WithHookDescription sets a description for the hook
func WithHookDescription(desc string) HookOption {
	return func(h *Hook) {
		h.Description = desc
	}
}

// WithHookEnabled sets the enabled state
func WithHookEnabled(enabled bool) HookOption {
	return func(h *Hook) {
		h.Enabled = enabled
	}
}

// --- Convenience functions for common hooks (deprecated) ---
// These functions register into the global HookRegistry which is no longer
// used by Service. Use svc.RegisterHook(event, handler) instead.

// Deprecated: use svc.RegisterHook(HookEventPreToolUse, handler).
func OnPreToolUse(handler HookHandler, opts ...HookOption) string {
	return GlobalHookRegistry().Register(HookEventPreToolUse, handler, opts...)
}

// Deprecated: use svc.RegisterHook(HookEventPostToolUse, handler).
func OnPostToolUse(handler HookHandler, opts ...HookOption) string {
	return GlobalHookRegistry().Register(HookEventPostToolUse, handler, opts...)
}

// Deprecated: use svc.RegisterHook(HookEventSubagentStart, handler).
func OnSubagentStart(handler HookHandler, opts ...HookOption) string {
	return GlobalHookRegistry().Register(HookEventSubagentStart, handler, opts...)
}

// Deprecated: use svc.RegisterHook(HookEventSubagentStop, handler).
func OnSubagentStop(handler HookHandler, opts ...HookOption) string {
	return GlobalHookRegistry().Register(HookEventSubagentStop, handler, opts...)
}
