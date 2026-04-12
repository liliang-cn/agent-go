package agent

import (
	"context"
	"time"
)

const defaultLLMTurnTimeout = 20 * time.Second

func withLLMTurnTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), defaultLLMTurnTimeout)
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining <= defaultLLMTurnTimeout {
			return ctx, func() {}
		}
	}
	return context.WithTimeout(ctx, defaultLLMTurnTimeout)
}
