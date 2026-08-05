package agent

import (
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

type queryLoopBudget struct {
	MaxRounds        int
	CompletedRounds  int
	EstimatedTokens  int
	InputTokens      int
	OutputTokens     int
	EstimatedCostUSD float64
	CompactionCount  int
	RecoveryCount    int
	RemainingRounds  int

	// Diminishing returns detection
	continuationCount int   // rounds without meaningful progress
	tokensPerRound    []int // rolling window of tokens per round
}

type recoveryMeta struct {
	Compacted bool
	Recovered bool
}

// budgetDecision indicates whether the query loop should continue
type budgetDecision int

const (
	budgetContinue budgetDecision = iota
	budgetStop
	budgetCompact
)

// Diminishing returns detection thresholds
const (
	diminishingReturnsWindow  = 3  // Number of rounds to track
	diminishingReturnsPercent = 50 // Percentage threshold (50 = 50%)
	maxContinuations          = 5  // Max rounds without meaningful progress
)

const (
	queryLoopTransitionNextTurn             = "next_turn"
	queryLoopTransitionHandoff              = "handoff"
	queryLoopTransitionDuplicateToolResults = "duplicate_tool_results"
	queryLoopTransitionToolBatch            = "tool_batch_executed"
	queryLoopTransitionToolExecutionError   = "tool_execution_error"
	queryLoopTransitionTextResponse         = "text_response"
	queryLoopTransitionMaxTurnsExceeded     = "max_turns_exceeded"
	queryLoopTransitionLintRetry            = "lint_retry"
)

type queryLoopState struct {
	Goal             string
	TaskID           string
	Messages         []domain.Message
	PrevToolCalls    map[string]int
	Transition       string
	LoopTransition   string
	TransitionReason string
	Stage            string
	CurrentRound     int
	PendingToolCount int
	TotalToolCalls   int
	LastResponseID   string
	Budget           queryLoopBudget
}

func newQueryLoopState(goal string, messages []domain.Message, maxRounds int) *queryLoopState {
	state := &queryLoopState{
		Goal:          goal,
		Messages:      append([]domain.Message(nil), messages...),
		PrevToolCalls: make(map[string]int),
		Budget: queryLoopBudget{
			MaxRounds:       maxRounds,
			RemainingRounds: maxRounds,
		},
	}
	return state
}

func (s *queryLoopState) beginRound() int {
	s.CurrentRound++
	return s.CurrentRound
}

func (s *queryLoopState) setMessages(messages []domain.Message) {
	s.Messages = append([]domain.Message(nil), messages...)
}

func (s *queryLoopState) setStage(stage, reason string, toolCount int) {
	s.Stage = stage
	s.TransitionReason = reason
	if toolCount >= 0 {
		s.PendingToolCount = toolCount
	}
}

func (s *queryLoopState) setLoopTransition(transition, reason string) {
	if transition != "" {
		s.LoopTransition = transition
	}
	if reason != "" {
		s.TransitionReason = reason
	}
}

func (s *queryLoopState) noteResponse(responseID string) {
	s.LastResponseID = responseID
}

func (s *queryLoopState) noteTokens(tokens int) {
	if tokens <= 0 {
		return
	}
	s.Budget.EstimatedTokens += tokens
}

// noteCost adds input/output tokens to the running totals and recomputes
// the estimated cost via pkg/usage's per-model pricing table. Called once
// per LLM round so the runtime can enforce MaxBudgetUSD.
func (s *queryLoopState) noteCost(inputTokens, outputTokens int, costUSD float64) {
	if inputTokens > 0 {
		s.Budget.InputTokens += inputTokens
	}
	if outputTokens > 0 {
		s.Budget.OutputTokens += outputTokens
	}
	if costUSD > 0 {
		s.Budget.EstimatedCostUSD += costUSD
	}
}

func (s *queryLoopState) noteRecovery(meta recoveryMeta) {
	if meta.Compacted {
		s.Budget.CompactionCount++
	}
	if meta.Recovered {
		s.Budget.RecoveryCount++
	}
}

func (s *queryLoopState) noteRoundCompleted() {
	s.Budget.CompletedRounds = s.CurrentRound
	remaining := s.Budget.MaxRounds - s.Budget.CompletedRounds
	if remaining < 0 {
		remaining = 0
	}
	s.Budget.RemainingRounds = remaining
}

func (s *queryLoopState) recordToolResults(results []ToolExecutionResult) {
	s.TotalToolCalls += len(results)
}

// resetContinuation resets the continuation counter (meaningful progress made)
func (s *queryLoopState) resetContinuation() {
	s.Budget.continuationCount = 0
}

// shouldContinue returns whether the query loop should continue, compact, or stop
func (s *queryLoopState) shouldContinue() budgetDecision {
	// If too many continuations without progress, stop
	if s.Budget.continuationCount >= maxContinuations {
		return budgetStop
	}

	// Check diminishing returns on tokens
	if len(s.Budget.tokensPerRound) >= diminishingReturnsWindow {
		window := s.Budget.tokensPerRound[len(s.Budget.tokensPerRound)-diminishingReturnsWindow:]
		if hasDiminishingReturns(window) {
			return budgetCompact
		}
	}

	return budgetContinue
}

// hasDiminishingReturns checks if token usage is decreasing over rounds
func hasDiminishingReturns(tokensPerRound []int) bool {
	if len(tokensPerRound) < diminishingReturnsWindow {
		return false
	}
	// Check if each round is <= diminishingReturnsPercent% of the previous
	for i := 1; i < len(tokensPerRound); i++ {
		prev := tokensPerRound[i-1]
		curr := tokensPerRound[i]
		if prev == 0 {
			continue
		}
		// If current is more than X% of previous, not diminishing
		if curr > prev*diminishingReturnsPercent/100 {
			return false
		}
	}
	return true
}
