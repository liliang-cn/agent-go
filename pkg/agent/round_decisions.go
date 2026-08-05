package agent

import "github.com/liliang-cn/agent-go/v3/pkg/domain"

const (
	textRoundDecisionComplete = "complete"
	textRoundDecisionContinue = "continue"
)

type textRoundDecision struct {
	Kind       string
	Reason     string
	Prompt     string
	Transition string
}

// decideTextRound resolves what to do after a text-only model turn.
//
// v3 deliberately has no auto-continue nudge and no tool-avoidance heuristic:
// a text turn ends the run, and any behavioural requirement on that final text
// (non-empty, delivered, no planning-only) is enforced by the output lint
// registry, not by a prompt nudge. See lint.go / output_lints_builtin.go.
func decideTextRound(loopState *queryLoopState, _ bool, _ bool, _ bool, _ string, _ bool) textRoundDecision {
	if loopState != nil {
		loopState.resetContinuation()
	}
	return textRoundDecision{
		Kind:       textRoundDecisionComplete,
		Reason:     "text response completed",
		Transition: queryLoopTransitionTextResponse,
	}
}

type postToolRoundDecision struct {
	Messages    []domain.Message
	ToolResults []ToolExecutionResult
	Terminal    string
	Blocked     bool
	AwaitAnswer bool
	Reason      string
	Transition  string
}

type handoffDecision struct {
	NextAgent  *Agent
	Reason     interface{}
	Transition string
	Message    string
}

func decideHandoff(nextAgent *Agent, reason interface{}) *handoffDecision {
	if nextAgent == nil {
		return nil
	}
	return &handoffDecision{
		NextAgent:  nextAgent,
		Reason:     reason,
		Transition: queryLoopTransitionHandoff,
		Message:    "agent handoff requested",
	}
}

func (s *Service) decidePostToolRound(messages []domain.Message, taskID string, result *domain.GenerationResult, duplicateToolResults, toolResults []ToolExecutionResult, ptcEnabled bool, filteredToolCalls []domain.ToolCall) postToolRoundDecision {
	outcome := s.buildToolRoundOutcome(messages, taskID, result, duplicateToolResults, toolResults, ptcEnabled, filteredToolCalls)
	decision := postToolRoundDecision{
		Messages:    outcome.Messages,
		ToolResults: outcome.ToolResults,
		Terminal:    outcome.Terminal,
		Blocked:     outcome.Blocked,
		AwaitAnswer: outcome.AwaitAnswer,
		Reason:      "tool batch completed; continue to next turn",
		Transition:  queryLoopTransitionToolBatch,
	}
	if outcome.Terminal != "" {
		decision.Reason = "tool round produced terminal answer"
		decision.Transition = queryLoopTransitionTextResponse
	}
	return decision
}
