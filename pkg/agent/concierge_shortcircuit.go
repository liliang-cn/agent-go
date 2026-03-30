package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type conciergeDirectRouteResult struct {
	targetAgent  string
	intentType   string
	reason       string
	optimized    string
	verification string
	finalText    string
	rawResult    map[string]interface{}
}

func (s *Service) shouldShortCircuitConciergeRoute(goal string) bool {
	if s == nil || !isConciergeAgent(s.agent) || s.toolRegistry == nil || !s.toolRegistry.Has("route_builtin_request") {
		return false
	}

	goal = normalizeTaskPrompt(goal)
	if goal == "" || looksLikeDelegatedAgentRequest(goal) {
		return false
	}

	lower := strings.ToLower(goal)
	statusHints := []string{
		"status", "progress", "session", "sessions", "task status", "task id",
		"team status", "agent status", "list teams", "list agents", "background task",
		"状态", "进度", "会话", "任务状态", "任务id", "团队状态", "agent 状态", "队伍状态",
	}
	asyncHints := []string{
		"background", "asynchronous", "async", "queue", "queued", "later", "in the background",
		"后台", "异步", "排队", "稍后", "先挂着",
	}
	if containsAny(lower, statusHints) || containsAny(lower, asyncHints) {
		return false
	}

	return true
}

func (s *Service) executeDirectConciergeRoute(ctx context.Context, session *Session, goal string) (*ExecutionResult, bool, error) {
	if !s.shouldShortCircuitConciergeRoute(goal) {
		return nil, false, nil
	}

	toolCtx := withCurrentAgent(ctx, s.agent)
	if session != nil {
		toolCtx = withCurrentSession(toolCtx, session)
	}

	raw, err := s.toolRegistry.Call(toolCtx, "route_builtin_request", map[string]interface{}{
		"prompt": normalizeTaskPrompt(goal),
	})
	if err != nil {
		return nil, true, err
	}

	parsed := parseConciergeDirectRouteResult(raw)
	if strings.TrimSpace(parsed.finalText) == "" {
		return nil, true, fmt.Errorf("route_builtin_request returned an empty result")
	}

	now := time.Now()
	result := &ExecutionResult{
		PlanID:      fmt.Sprintf("concierge-route-%d", now.UnixNano()),
		SessionID:   firstNonEmpty(s.CurrentSessionID(), sessionIDOrEmpty(session)),
		Success:     true,
		StepsTotal:  1,
		StepsDone:   1,
		StepsFailed: 0,
		StartedAt:   &now,
		CompletedAt: &now,
		ToolCalls:   1,
		ToolsUsed:   []string{"route_builtin_request"},
		FinalResult: parsed.finalText,
		Duration:    "completed",
		Metadata: map[string]interface{}{
			"dispatch_mode":        "direct_concierge_route",
			"dispatch_target":      parsed.targetAgent,
			"dispatch_intent":      parsed.intentType,
			"dispatch_reason":      parsed.reason,
			"dispatch_result":      parsed.finalText,
			"optimized_prompt":     parsed.optimized,
			"verification_result":  parsed.verification,
			"route_builtin_result": parsed.rawResult,
		},
	}
	return result, true, nil
}

func (s *Service) streamDirectConciergeRoute(ctx context.Context, session *Session, goal string) (<-chan *Event, bool, error) {
	if !s.shouldShortCircuitConciergeRoute(goal) {
		return nil, false, nil
	}

	events := make(chan *Event, 8)
	go func() {
		defer close(events)

		startEvt := NewEvent(EventTypeStart, s.agent)
		startEvt.Content = fmt.Sprintf("Starting task: %s", strings.TrimSpace(goal))
		events <- startEvt

		toolEvt := NewEvent(EventTypeToolCall, s.agent)
		toolEvt.ToolName = "route_builtin_request"
		toolEvt.ToolArgs = map[string]interface{}{"prompt": normalizeTaskPrompt(goal)}
		events <- toolEvt

		result, _, err := s.executeDirectConciergeRoute(ctx, session, goal)
		if err != nil {
			errEvt := NewEvent(EventTypeError, s.agent)
			errEvt.Content = err.Error()
			events <- errEvt
			return
		}

		targetName := strings.TrimSpace(metadataString(result.Metadata, "dispatch_target"))
		if targetName == "" {
			targetName = s.agent.Name()
		}

		toolResultEvt := NewEvent(EventTypeToolResult, s.agent)
		toolResultEvt.ToolName = "route_builtin_request"
		toolResultEvt.ToolResult = result.Metadata["route_builtin_result"]
		events <- toolResultEvt

		completeEvt := NewEvent(EventTypeComplete, s.agent)
		completeEvt.AgentName = targetName
		completeEvt.Content = strings.TrimSpace(result.Text())
		events <- completeEvt
	}()

	return events, true, nil
}

func parseConciergeDirectRouteResult(raw interface{}) conciergeDirectRouteResult {
	result := conciergeDirectRouteResult{}
	payload, ok := raw.(map[string]interface{})
	if !ok {
		result.finalText = strings.TrimSpace(formatResultForContent(raw))
		return result
	}

	result.rawResult = payload
	result.targetAgent = strings.TrimSpace(metadataString(payload, "target_agent"))
	result.intentType = strings.TrimSpace(metadataString(payload, "intent_type"))
	result.reason = strings.TrimSpace(metadataString(payload, "routing_reason"))
	result.optimized = strings.TrimSpace(metadataString(payload, "optimized_prompt"))
	result.verification = strings.TrimSpace(metadataString(payload, "verification_result"))
	result.finalText = strings.TrimSpace(metadataString(payload, "result"))
	if result.finalText == "" {
		result.finalText = strings.TrimSpace(formatResultForContent(payload))
	}
	return result
}

func sessionIDOrEmpty(session *Session) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(session.GetID())
}
