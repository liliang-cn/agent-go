package agent

import (
	"strings"
	"time"

	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

func (s *Service) observeRunStream(session *Session, taskID, goal string, startedAt time.Time, upstream <-chan *Event) <-chan *Event {
	if upstream == nil {
		return upstream
	}
	out := make(chan *Event, 64)
	go func() {
		defer close(out)

		var metrics executionMetrics
		toolSet := map[string]struct{}{}
		maxRound := 0

		for evt := range upstream {
			if evt != nil {
				s.persistRunTaskEvent(session, taskID, evt)

				// Accumulate metrics from events.
				if evt.Round > maxRound {
					maxRound = evt.Round
				}
				if evt.TokensUsed > 0 {
					metrics.estimatedTokens += evt.TokensUsed
				}
				if evt.Type == EventTypeToolCall && evt.ToolName != "" {
					metrics.toolCalls++
					if _, seen := toolSet[evt.ToolName]; !seen {
						toolSet[evt.ToolName] = struct{}{}
						metrics.toolsUsed = append(metrics.toolsUsed, evt.ToolName)
					}
				}
				if evt.DurationMs > 0 {
					metrics.totalDurationMs += evt.DurationMs
				}
				// Cost rolls forward as the runtime accrues spend across
				// LLM rounds; the terminal event carries the final value.
				if evt.EstimatedCostUSD > metrics.estimatedCostUSD {
					metrics.estimatedCostUSD = evt.EstimatedCostUSD
				}
				// Extract round/token data from analytics events.
				if evt.Type == EventTypeAnalytics && evt.AnalyticsEvent != nil {
					ad := evt.AnalyticsEvent
					if ad.Name == AnalyticsRoundCompleted {
						if r, ok := ad.Data["round"].(int); ok && r > maxRound {
							maxRound = r
						}
						if t, ok := ad.Data["total_tokens"].(int); ok && t > metrics.estimatedTokens {
							metrics.estimatedTokens = t
						}
					}
					if ad.Name == AnalyticsLLMLatency {
						if t, ok := ad.Data["tokens"].(int); ok {
							metrics.estimatedTokens += t
						}
					}
				}

				switch evt.Type {
				case EventTypeComplete:
					s.persistRunTaskState(session, taskID, taskRunStateOptions{
						status:     taskpkg.StatusCompleted,
						input:      goal,
						output:     strings.TrimSpace(evt.Content),
						createdAt:  startedAt,
						finishedAt: evt.Timestamp,
					})
				case EventTypeBlocked:
					s.persistRunTaskState(session, taskID, taskRunStateOptions{
						status:      taskpkg.StatusBlocked,
						input:       goal,
						output:      strings.TrimSpace(evt.Content),
						errorText:   strings.TrimSpace(evt.Content),
						createdAt:   startedAt,
						finishedAt:  evt.Timestamp,
						appendError: true,
					})
				case EventTypeError:
					s.persistRunTaskState(session, taskID, taskRunStateOptions{
						status:      taskpkg.StatusFailed,
						input:       goal,
						errorText:   strings.TrimSpace(evt.Content),
						createdAt:   startedAt,
						finishedAt:  evt.Timestamp,
						appendError: true,
					})
				}
			}
			out <- evt
		}

		// Persist accumulated stats after stream ends.
		metrics.rounds = maxRound
		if metrics.totalDurationMs == 0 {
			metrics.totalDurationMs = time.Since(startedAt).Milliseconds()
		}
		s.persistRunTaskStats(session, taskID, &metrics)
	}()
	return out
}
