package agent

import (
	"context"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

const defaultLLMTurnTimeout = 180 * time.Second

func resolveLLMTurnTimeout(cfg *config.Config) time.Duration {
	if cfg != nil {
		if timeout := cfg.AgentLLMTurnTimeout(); timeout > 0 {
			return timeout
		}
	}
	if seconds := config.GetEnvOrDefaultInt("AGENTGO_LLM_TURN_TIMEOUT_SECONDS", int(defaultLLMTurnTimeout/time.Second)); seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return defaultLLMTurnTimeout
}

func withLLMTurnTimeout(ctx context.Context, cfg *config.Config) (context.Context, context.CancelFunc) {
	timeout := resolveLLMTurnTimeout(cfg)
	if ctx == nil {
		return context.WithTimeout(context.Background(), timeout)
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining <= timeout {
			return ctx, func() {}
		}
	}
	return context.WithTimeout(ctx, timeout)
}
