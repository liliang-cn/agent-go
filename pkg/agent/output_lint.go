package agent

import (
	"strings"
	"sync"
)

// OutputLint is a deterministic post-output check applied to an agent's
// final-text response. Lints encode rules that used to live in instruction
// strings ("Do not bounce the task back to Dispatcher", "Never store a
// relative time reference", ...) so the runtime can reject the response and
// give the model a structured retry, instead of relying on the model to
// remember the rule on its own.
type OutputLint interface {
	// Name returns a stable identifier used in events and retry feedback.
	Name() string
	// Check inspects the final text. Returns ok=true to pass; ok=false with a
	// human-readable reason to reject (the reason is shown to the model on
	// retry).
	Check(text string, ctx LintContext) (ok bool, reason string)
}

// LintContext carries runtime metadata that lints may consult. Fields are
// best-effort: a lint should treat zero values as "unknown" rather than
// asserting on them.
type LintContext struct {
	AgentName  string
	TaskID     string
	SessionID  string
	TurnIndex  int
	// Goal is the task input for the run, so goal-aware lints can check the
	// final answer against what was actually asked.
	Goal string
	// ToolCalls is the set of tool names invoked during the run.
	ToolCalls  []string
	IsRetry    bool
	RetryCount int
}

// LintFunc adapts a plain function into an OutputLint. Useful for inline
// definitions and tests.
type LintFunc struct {
	NameValue string
	Fn        func(text string, ctx LintContext) (bool, string)
}

func (f LintFunc) Name() string { return f.NameValue }

func (f LintFunc) Check(text string, ctx LintContext) (bool, string) {
	if f.Fn == nil {
		return true, ""
	}
	return f.Fn(text, ctx)
}

// LintViolation describes the first lint that rejected a response.
type LintViolation struct {
	LintName string
	Reason   string
}

// OutputLintRegistry stores lints keyed by agent name plus a global bucket
// applied to every agent.
type OutputLintRegistry struct {
	mu      sync.RWMutex
	global  []OutputLint
	byAgent map[string][]OutputLint
}

// NewOutputLintRegistry returns an empty registry.
func NewOutputLintRegistry() *OutputLintRegistry {
	return &OutputLintRegistry{
		byAgent: make(map[string][]OutputLint),
	}
}

// RegisterGlobal adds a lint that runs for every agent.
func (r *OutputLintRegistry) RegisterGlobal(lint OutputLint) {
	if r == nil || lint == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Idempotent by name: build() registers the global baseline lints and a
	// caller may also invoke RegisterDefaultOutputLints — don't double-run.
	for _, existing := range r.global {
		if existing.Name() == lint.Name() {
			return
		}
	}
	r.global = append(r.global, lint)
}

// RegisterForAgent adds a lint that runs only when the given agent produced
// the response. Agent name is matched case-insensitively after trimming
// whitespace.
func (r *OutputLintRegistry) RegisterForAgent(agentName string, lint OutputLint) {
	if r == nil || lint == nil {
		return
	}
	key := normalizeLintAgentKey(agentName)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.byAgent[key] {
		if existing.Name() == lint.Name() {
			return
		}
	}
	r.byAgent[key] = append(r.byAgent[key], lint)
}

// Run executes every applicable lint in registration order (global first,
// then agent-specific) and returns the first violation. If no lint rejects
// the text, the returned violation is nil.
func (r *OutputLintRegistry) Run(text string, ctx LintContext) *LintViolation {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	lints := make([]OutputLint, 0, len(r.global)+4)
	lints = append(lints, r.global...)
	if specific, ok := r.byAgent[normalizeLintAgentKey(ctx.AgentName)]; ok {
		lints = append(lints, specific...)
	}
	r.mu.RUnlock()

	for _, lint := range lints {
		if lint == nil {
			continue
		}
		ok, reason := lint.Check(text, ctx)
		if ok {
			continue
		}
		return &LintViolation{LintName: lint.Name(), Reason: strings.TrimSpace(reason)}
	}
	return nil
}

// Names lists the lint names that would apply for the given agent. Returned
// in the same order as Run would evaluate them. Useful for tests and for
// logging "which lints are wired up".
func (r *OutputLintRegistry) Names(agentName string) []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.global)+len(r.byAgent[normalizeLintAgentKey(agentName)]))
	for _, lint := range r.global {
		if lint != nil {
			out = append(out, lint.Name())
		}
	}
	for _, lint := range r.byAgent[normalizeLintAgentKey(agentName)] {
		if lint != nil {
			out = append(out, lint.Name())
		}
	}
	return out
}

func normalizeLintAgentKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// defaultLintRetryBudget is the number of times a single turn may be
// re-prompted because of lint failures before the runtime gives up and
// blocks the task. Picked small on purpose — lints are meant to catch
// recurring mistakes, not to babysit a confused model into compliance.
const defaultLintRetryBudget = 2

// FormatLintFeedback builds the system message appended to history when a
// lint rejects a response and the runtime is about to retry.
func FormatLintFeedback(violation *LintViolation) string {
	if violation == nil {
		return ""
	}
	reason := strings.TrimSpace(violation.Reason)
	if reason == "" {
		reason = "no reason provided"
	}
	return strings.TrimSpace(
		"Your previous response failed an output lint and was not delivered to the user.\n" +
			"Lint: " + violation.LintName + "\n" +
			"Reason: " + reason + "\n" +
			"Revise your response to satisfy the rule above and reply with the corrected final answer. " +
			"Do not acknowledge this message — produce only the revised final answer.",
	)
}
