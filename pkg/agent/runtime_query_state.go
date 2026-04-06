package agent

import "github.com/liliang-cn/agent-go/v2/pkg/domain"

type queryLoopBudget struct {
	MaxRounds       int
	CompletedRounds int
	EstimatedTokens int
	CompactionCount int
	RecoveryCount   int
	RemainingRounds int

	// Diminishing returns detection
	continuationCount int     // rounds without meaningful progress
	lastDeltaTokens  int     // tokens from previous round
	tokensPerRound   []int   // rolling window of tokens per round
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
	diminishingReturnsPercent = 50  // Percentage threshold (50 = 50%)
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
)

type queryLoopState struct {
	Goal             string
	Messages         []domain.Message
	PrevToolCalls    map[string]int
	Intent           *IntentRecognitionResult
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

func newQueryLoopState(goal string, messages []domain.Message, intent *IntentRecognitionResult, maxRounds int) *queryLoopState {
	state := &queryLoopState{
		Goal:          goal,
		Messages:      append([]domain.Message(nil), messages...),
		PrevToolCalls: make(map[string]int),
		Intent:        intent,
		Budget: queryLoopBudget{
			MaxRounds:       maxRounds,
			RemainingRounds: maxRounds,
		},
	}
	if intent != nil {
		state.Transition = intent.Transition
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

// recordRoundTokens records tokens used in a round for diminishing returns detection
func (s *queryLoopState) recordRoundTokens(tokens int) {
	s.Budget.tokensPerRound = append(s.Budget.tokensPerRound, tokens)
	// Keep window bounded
	if len(s.Budget.tokensPerRound) > diminishingReturnsWindow*2 {
		s.Budget.tokensPerRound = s.Budget.tokensPerRound[len(s.Budget.tokensPerRound)-diminishingReturnsWindow:]
	}
}

// incrementContinuation increments the continuation counter (no meaningful progress)
func (s *queryLoopState) incrementContinuation() {
	s.Budget.continuationCount++
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

type serviceExecutionLoopState struct {
	*queryLoopState
	CurrentAgent *Agent
	Metrics      executionMetrics
	ToolUsed     bool
	Nudged       bool
}

func newServiceExecutionLoopState(goal string, messages []domain.Message, intent *IntentRecognitionResult, maxRounds int, currentAgent *Agent) *serviceExecutionLoopState {
	return &serviceExecutionLoopState{
		queryLoopState: newQueryLoopState(goal, messages, intent, maxRounds),
		CurrentAgent:   currentAgent,
	}
}

func (s *serviceExecutionLoopState) noteTurnTokens(tokens int) {
	s.queryLoopState.noteTokens(tokens)
	if tokens > 0 {
		s.Metrics.estimatedTokens += tokens
	}
}

func (s *serviceExecutionLoopState) noteToolResults(results []ToolExecutionResult) {
	if len(results) == 0 {
		return
	}
	s.ToolUsed = true
	s.queryLoopState.recordToolResults(results)
	s.Metrics.toolCalls += len(results)
	s.Metrics.toolsUsed = appendToolNames(s.Metrics.toolsUsed, results)
}

func (s *serviceExecutionLoopState) continueWith(transition, reason string, messages []domain.Message) {
	s.setLoopTransition(transition, reason)
	if messages != nil {
		s.setMessages(messages)
	}
	s.noteRoundCompleted()
}

func (s *serviceExecutionLoopState) setCurrentAgent(agent *Agent) {
	if agent != nil {
		s.CurrentAgent = agent
	}
}

func (s *serviceExecutionLoopState) metricsSnapshot() *executionMetrics {
	if s == nil {
		return &executionMetrics{}
	}
	return &executionMetrics{
		toolCalls:       s.Metrics.toolCalls,
		toolsUsed:       append([]string(nil), s.Metrics.toolsUsed...),
		estimatedTokens: s.Metrics.estimatedTokens,
	}
}
