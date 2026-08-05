package agent

import (
	"time"
)

// SubAgentOption functions for configuring SubAgent

// WithSubAgentMode sets the execution mode (foreground or background)
func WithSubAgentMode(mode SubAgentMode) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Mode = mode
	}
}

// WithSubAgentMaxTurns sets the maximum number of turns
func WithSubAgentMaxTurns(maxTurns int) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.MaxTurns = maxTurns
	}
}

// WithSubAgentIsolated sets whether to isolate context from parent
func WithSubAgentIsolated(isolated bool) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Isolated = isolated
	}
}

// WithSubAgentToolAllowlist sets which tools are allowed (empty = all)
func WithSubAgentToolAllowlist(tools []string) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.ToolAllowlist = tools
	}
}

// WithSubAgentToolDenylist sets which tools are denied
func WithSubAgentToolDenylist(tools []string) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.ToolDenylist = tools
	}
}

// WithSubAgentContext sets additional context for the sub-agent
func WithSubAgentContext(ctx map[string]interface{}) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Context = ctx
	}
}

// WithSubAgentService sets the parent service
func WithSubAgentService(svc *Service) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Service = svc
	}
}

// WithSubAgentTimeout sets the execution timeout
func WithSubAgentTimeout(timeout time.Duration) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Timeout = timeout
	}
}

// WithSubAgentProgressCallback sets the progress callback
func WithSubAgentProgressCallback(cb SubAgentProgressCallback) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.ProgressCb = cb
	}
}

// WithSubAgentRetry sets the number of retries on failure
func WithSubAgentRetry(retries int) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.RetryOnFailure = retries
	}
}

// --- Convenience Presets ---

// SubAgentReadOnly creates options for a read-only sub-agent
func SubAgentReadOnly() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.ToolAllowlist = []string{
			"rag_query",
			"memory_recall",
		}
		cfg.ToolDenylist = []string{
			"memory_save",
			"rag_ingest",
		}
	}
}

// SubAgentQuick creates options for a quick sub-agent with limited turns
func SubAgentQuick() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.MaxTurns = 3
		cfg.Mode = SubAgentModeForeground
	}
}

// SubAgentBackground creates options for background execution
func SubAgentBackground() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Mode = SubAgentModeBackground
		cfg.Isolated = true
	}
}

// SubAgentForCodeReview creates options for a code review sub-agent
func SubAgentForCodeReview() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.MaxTurns = 5
		cfg.ToolAllowlist = []string{
			"rag_query",
			"memory_recall",
		}
		cfg.ToolDenylist = []string{
			"memory_save",
			"rag_ingest",
		}
	}
}

// SubAgentForDebugging creates options for a debugging sub-agent
func SubAgentForDebugging() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.MaxTurns = 10
		cfg.Isolated = false // Inherit context for debugging
	}
}

// SubAgentForAnalysis creates options for an analysis sub-agent
func SubAgentForAnalysis() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.MaxTurns = 5
		cfg.ToolAllowlist = []string{
			"rag_query",
			"memory_recall",
		}
	}
}

// SubAgentForExecution creates options for an execution sub-agent
func SubAgentForExecution() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.MaxTurns = 15
		cfg.Isolated = true
	}
}

// SubAgentWithTimeout creates options with a specific timeout
func SubAgentWithTimeout(timeout time.Duration) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Timeout = timeout
		cfg.CancelOnTimeout = true
	}
}

// SubAgentWithRetry creates options with retry support
func SubAgentWithRetry(retries int) SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.RetryOnFailure = retries
	}
}

// SubAgentShortTimeout creates options with a 30-second timeout
func SubAgentShortTimeout() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Timeout = 30 * time.Second
		cfg.CancelOnTimeout = true
	}
}

// SubAgentMediumTimeout creates options with a 2-minute timeout
func SubAgentMediumTimeout() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Timeout = 2 * time.Minute
		cfg.CancelOnTimeout = true
	}
}

// SubAgentLongTimeout creates options with a 10-minute timeout
func SubAgentLongTimeout() SubAgentOption {
	return func(cfg *SubAgentConfig) {
		cfg.Timeout = 10 * time.Minute
		cfg.CancelOnTimeout = true
	}
}
