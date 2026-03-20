package security

import (
	"context"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

// Policy defines security policies for PTC execution
type Policy struct {
	// MaxExecutionTime is the maximum execution time
	MaxExecutionTime time.Duration
	// MaxMemoryMB is the maximum memory in MB
	MaxMemoryMB int
	// MaxCodeSize is the maximum code size in bytes
	MaxCodeSize int
	// MaxOutputSize is the maximum output size in bytes
	MaxOutputSize int
	// MaxToolCalls is the maximum number of tool calls
	MaxToolCalls int
	// AllowFileAccess allows file system access
	AllowFileAccess bool
	// AllowNetwork allows network access
	AllowNetwork bool
	// AllowedTools is the whitelist of allowed tools
	AllowedTools map[string]bool
	// BlockedTools is the blacklist of blocked tools
	BlockedTools map[string]bool
}

// DefaultPolicy returns the default security policy
func DefaultPolicy() *Policy {
	return &Policy{
		MaxExecutionTime: 30 * time.Second,
		MaxMemoryMB:      64,
		MaxCodeSize:      64 * 1024,
		MaxOutputSize:    1024 * 1024,
		MaxToolCalls:     20,
		AllowFileAccess:  false,
		AllowNetwork:     false,
		AllowedTools:     make(map[string]bool),
		BlockedTools:     make(map[string]bool),
	}
}

// PolicyFromConfig creates a policy from configuration
func PolicyFromConfig(config *ptc.Config) *Policy {
	policy := DefaultPolicy()

	if config.DefaultTimeout > 0 {
		policy.MaxExecutionTime = config.DefaultTimeout
	}
	if config.MaxMemoryMB > 0 {
		policy.MaxMemoryMB = config.MaxMemoryMB
	}
	if config.MaxCodeSize > 0 {
		policy.MaxCodeSize = config.MaxCodeSize
	}
	if config.MaxOutputSize > 0 {
		policy.MaxOutputSize = config.MaxOutputSize
	}
	if config.MaxToolCalls > 0 {
		policy.MaxToolCalls = config.MaxToolCalls
	}

	policy.AllowFileAccess = config.Security.AllowFileAccess
	policy.AllowNetwork = config.Security.AllowNetwork

	policy.AllowedTools = make(map[string]bool)
	for _, tool := range config.Security.AllowedTools {
		policy.AllowedTools[tool] = true
	}

	policy.BlockedTools = make(map[string]bool)
	for _, tool := range config.Security.BlockedTools {
		policy.BlockedTools[tool] = true
	}

	return policy
}

// IsToolAllowed checks if a tool is allowed by the policy
func (p *Policy) IsToolAllowed(toolName string) bool {
	// Check blocked list first
	if p.BlockedTools["*"] || p.BlockedTools[toolName] {
		return false
	}

	// If allowed list is empty, all non-blocked tools are allowed
	if len(p.AllowedTools) == 0 {
		return true
	}

	// Check allowed list
	return p.AllowedTools["*"] || p.AllowedTools[toolName]
}

// AllowTool adds a tool to the allowed list
func (p *Policy) AllowTool(toolName string) {
	if p.AllowedTools == nil {
		p.AllowedTools = make(map[string]bool)
	}
	p.AllowedTools[toolName] = true
	delete(p.BlockedTools, toolName)
}

// BlockTool adds a tool to the blocked list
func (p *Policy) BlockTool(toolName string) {
	if p.BlockedTools == nil {
		p.BlockedTools = make(map[string]bool)
	}
	p.BlockedTools[toolName] = true
	delete(p.AllowedTools, toolName)
}

// Enforcer enforces security policies during execution
type Enforcer struct {
	policy *Policy
}

// NewEnforcer creates a new policy enforcer
func NewEnforcer(policy *Policy) *Enforcer {
	return &Enforcer{policy: policy}
}

// CheckExecutionRequest checks if an execution request complies with the policy
func (e *Enforcer) CheckExecutionRequest(req *ptc.ExecutionRequest) error {
	if req.Timeout > e.policy.MaxExecutionTime {
		return ptc.ErrExecutionTimeout
	}
	if req.MaxMemoryMB > e.policy.MaxMemoryMB {
		return ptc.ErrMemoryLimitExceeded
	}
	if len(req.Code) > e.policy.MaxCodeSize {
		return ptc.ErrCodeSizeExceeded
	}
	return nil
}

// CheckToolCall checks if a tool call is allowed
func (e *Enforcer) CheckToolCall(toolName string) error {
	if !e.policy.IsToolAllowed(toolName) {
		return ptc.ErrToolNotAllowed
	}
	return nil
}

// CheckOutputSize checks if output size is within limits
func (e *Enforcer) CheckOutputSize(size int) error {
	if size > e.policy.MaxOutputSize {
		return ptc.ErrOutputSizeExceeded
	}
	return nil
}

// CheckToolCallCount checks if tool call count is within limits
func (e *Enforcer) CheckToolCallCount(count int) error {
	if count > e.policy.MaxToolCalls {
		return ptc.ErrMaxToolCallsExceeded
	}
	return nil
}

// ContextKey is used to store policy in context
type ContextKey string

const (
	// PolicyKey is the context key for policy
	PolicyKey ContextKey = "ptc_policy"
	// EnforcerKey is the context key for enforcer
	EnforcerKey ContextKey = "ptc_enforcer"
)

// WithPolicy adds policy to context
func WithPolicy(ctx context.Context, policy *Policy) context.Context {
	return context.WithValue(ctx, PolicyKey, policy)
}

// PolicyFromContext retrieves policy from context
func PolicyFromContext(ctx context.Context) *Policy {
	if policy, ok := ctx.Value(PolicyKey).(*Policy); ok {
		return policy
	}
	return nil
}

// WithEnforcer adds enforcer to context
func WithEnforcer(ctx context.Context, enforcer *Enforcer) context.Context {
	return context.WithValue(ctx, EnforcerKey, enforcer)
}

// EnforcerFromContext retrieves enforcer from context
func EnforcerFromContext(ctx context.Context) *Enforcer {
	if enforcer, ok := ctx.Value(EnforcerKey).(*Enforcer); ok {
		return enforcer
	}
	return nil
}
