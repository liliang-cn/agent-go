// Per-run and per-round metric accumulators, filled by the run-stream observer
// and persisted onto the task record.
package agent

type roundMetrics struct {
	round      int
	tokens     int
	toolCalls  int
	llmMs      int64
	toolMs     int64
	durationMs int64
}

type executionMetrics struct {
	toolCalls        int
	toolsUsed        []string
	estimatedTokens  int
	rounds           int
	roundStats       []roundMetrics
	totalDurationMs  int64
	estimatedCostUSD float64
}
