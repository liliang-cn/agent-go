package agent

import (
	"strconv"
	"time"

	"github.com/google/uuid"
)

func (r *Runtime) emitLLMLatency(round int, tokens int, duration time.Duration) {
	r.eventChan <- NewAnalyticsEvent(AnalyticsLLMLatency, map[string]interface{}{
		"round":       round,
		"tokens":      tokens,
		"duration_ms": duration.Milliseconds(),
	})
}

func (r *Runtime) emitRoundCompletedAnalytics(state *queryLoopState) {
	if state == nil {
		return
	}
	r.eventChan <- NewAnalyticsEvent(AnalyticsRoundCompleted, map[string]interface{}{
		"round":        state.CurrentRound,
		"total_tokens": state.Budget.EstimatedTokens,
		"total_tools":  state.TotalToolCalls,
	})
}

func (r *Runtime) emitCheckpointEvent(name string, start, end time.Time, dur time.Duration) {
	r.eventChan <- &Event{
		ID:                 uuid.New().String(),
		Type:               EventTypeCheckpoint,
		AgentName:          r.currentAgent.Name(),
		AgentID:            r.currentAgent.ID(),
		CheckpointName:     name,
		CheckpointStart:    start,
		CheckpointEnd:      end,
		CheckpointDuration: dur,
		Timestamp:          time.Now(),
	}
}

func (r *Runtime) emitAutoContinueNotice(state *queryLoopState) {
	if state == nil {
		return
	}
	r.emit(EventTypeStateUpdate, "Auto-continuing run (budget available: "+
		formatBudgetProgress(state.Budget.CompletedRounds, state.Budget.MaxRounds)+")")
}

func formatBudgetProgress(used, max int) string {
	return itoa(used) + "/" + itoa(max)
}

func itoa(v int) string {
	return strconv.Itoa(v)
}
