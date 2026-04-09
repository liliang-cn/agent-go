package agent

import "github.com/liliang-cn/agent-go/v2/pkg/domain"

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

func decideTextRound(loopState *queryLoopState, toolsAvailable bool, toolUsed bool, nudged bool, content string, allowToolUseNudge bool) textRoundDecision {
	if loopState == nil {
		return textRoundDecision{
			Kind:       textRoundDecisionComplete,
			Reason:     "text response completed",
			Transition: queryLoopTransitionTextResponse,
		}
	}

	if shouldAutoContinueAfterTextResponse(content) {
		loopState.incrementContinuation()
		if prompt, ok := buildAutoContinuePrompt(loopState); ok {
			return textRoundDecision{
				Kind:       textRoundDecisionContinue,
				Reason:     "empty text response; auto-continuing next turn",
				Prompt:     prompt,
				Transition: queryLoopTransitionNextTurn,
			}
		}
	} else {
		loopState.resetContinuation()
	}

	if allowToolUseNudge && shouldNudgeForMissingToolUse(toolsAvailable, toolUsed, nudged, content) {
		return textRoundDecision{
			Kind:       textRoundDecisionContinue,
			Reason:     "nudged model to use available tools",
			Prompt:     toolUseNudgePrompt,
			Transition: queryLoopTransitionNextTurn,
		}
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
