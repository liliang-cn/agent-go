package agent

import "github.com/liliang-cn/agent-go/v2/pkg/domain"

type queryLoopBudget struct {
	MaxRounds       int
	CompletedRounds int
	EstimatedTokens int
	CompactionCount int
	RecoveryCount   int
	RemainingRounds int
}

type queryLoopState struct {
	Goal             string
	Messages         []domain.Message
	PrevToolCalls    map[string]int
	Intent           *IntentRecognitionResult
	Transition       string
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

func (s *queryLoopState) setStage(stage, reason string, toolCount int) {
	s.Stage = stage
	s.TransitionReason = reason
	if toolCount >= 0 {
		s.PendingToolCount = toolCount
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
