package agent

import (
	"strconv"
	"time"

	"github.com/google/uuid"
)

// emitLLMLatency reports one model turn. totalTokens is the run's running
// total (what the analytics payload has always carried); turnTokens is this
// turn's own, and rides on the event field so a collector can sum it.
//
// That field existed and was never set by anything, which made the collector's
// `result.EstimatedTokens += evt.TokensUsed` dead code — every run fell
// through to the tokenizer's guess and no caller could tell.
func (r *Runtime) emitLLMLatency(round int, totalTokens, turnTokens int, duration time.Duration) {
	evt := NewAnalyticsEvent(AnalyticsLLMLatency, map[string]interface{}{
		"round":       round,
		"tokens":      totalTokens,
		"turn_tokens": turnTokens,
		"duration_ms": duration.Milliseconds(),
	})
	evt.Round = round
	evt.TokensUsed = turnTokens
	evt.DurationMs = duration.Milliseconds()
	r.eventChan <- evt
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
