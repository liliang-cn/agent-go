package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	memorypkg "github.com/liliang-cn/agent-go/v2/pkg/memory"
)

type conciergeDirectRouteResult struct {
	targetAgent  string
	intentType   string
	reason       string
	optimized    string
	blocked      bool
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
	if memoryResult, ok, err := s.executeDirectConciergeMemoryIntent(ctx, session, goal); ok {
		return memoryResult, true, err
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
	if strings.Contains(strings.ToLower(parsed.finalText), "couldn't find that in memory") && s.memoryService != nil {
		if memories, err := s.retrieveConciergeMemories(ctx, goal, s.resolveMemoryQueryContext(session)); err == nil && len(memories) > 0 {
			parsed.finalText = fallbackExplicitRecallSnippet(memories)
		} else {
			parsed.finalText = sanitizeConciergeMemoryFallbackText(parsed.finalText)
		}
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
			"dispatch_blocked":     parsed.blocked,
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

		result, _, err := s.executeDirectConciergeRoute(ctx, session, goal)
		if err != nil {
			errEvt := NewEvent(EventTypeError, s.agent)
			errEvt.Content = err.Error()
			events <- errEvt
			return
		}

		toolName, toolArgs := conciergeShortCircuitToolMetadata(result, goal)
		if toolName != "" {
			toolEvt := NewEvent(EventTypeToolCall, s.agent)
			toolEvt.ToolName = toolName
			toolEvt.ToolArgs = toolArgs
			events <- toolEvt
		}

		targetName := strings.TrimSpace(metadataString(result.Metadata, "dispatch_target"))
		if targetName == "" {
			targetName = s.agent.Name()
		}

		if toolName != "" {
			toolResultEvt := NewEvent(EventTypeToolResult, s.agent)
			toolResultEvt.ToolName = toolName
			toolResultEvt.ToolResult = conciergeShortCircuitToolResult(result)
			events <- toolResultEvt
		}

		completeEvt := NewEvent(EventTypeComplete, s.agent)
		completeEvt.AgentName = targetName
		completeEvt.Content = strings.TrimSpace(result.Text())
		events <- completeEvt
	}()

	return events, true, nil
}

func (s *Service) executeDirectConciergeMemoryIntent(ctx context.Context, session *Session, goal string) (*ExecutionResult, bool, error) {
	if s == nil || s.memoryService == nil {
		return nil, false, nil
	}

	intent := (&Planner{}).fallbackIntentRecognition(goal)
	queryContext := s.resolveMemoryQueryContext(session)
	now := time.Now()

	if isExplicitMemorySaveIntent(goal, intent) {
		content := strings.TrimSpace(extractExplicitMemorySaveContent(goal))
		if content == "" {
			return nil, true, fmt.Errorf("memory save content is required")
		}
		if err := s.memoryService.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
			SessionID:    firstNonEmpty(queryContext.SessionID, sessionIDOrEmpty(session)),
			AgentID:      queryContext.AgentID,
			TeamID:       queryContext.TeamID,
			UserID:       queryContext.UserID,
			TaskGoal:     goal,
			TaskResult:   content,
			ExecutionLog: "explicit concierge memory save",
		}); err != nil {
			return nil, true, err
		}
		return &ExecutionResult{
			PlanID:      fmt.Sprintf("concierge-memory-save-%d", now.UnixNano()),
			SessionID:   firstNonEmpty(s.CurrentSessionID(), sessionIDOrEmpty(session)),
			Success:     true,
			StepsTotal:  1,
			StepsDone:   1,
			StepsFailed: 0,
			StartedAt:   &now,
			CompletedAt: &now,
			ToolCalls:   1,
			ToolsUsed:   []string{"memory_save"},
			FinalResult: "已保存用于后续跨会话。",
			Duration:    "completed",
			Metadata: map[string]interface{}{
				"dispatch_mode":   "direct_concierge_memory_save",
				"dispatch_target": s.agent.Name(),
				"dispatch_intent": "memory_save",
				"memory_saved":    true,
			},
		}, true, nil
	}

	if !isExplicitMemoryRecallIntent(goal, intent) {
		if !looksLikeInformationSeekingQuery(goal) {
			return nil, false, nil
		}
		memories, err := s.retrieveConciergeMemories(ctx, goal, queryContext)
		if err != nil || !shouldDirectConciergeAnswerFromMemory(memories) {
			return nil, false, nil
		}
		return s.buildDirectConciergeMemoryRecallResult(ctx, session, goal, intent, memories)
	}

	memories, err := s.retrieveConciergeMemories(ctx, goal, queryContext)
	if err != nil {
		return nil, true, err
	}
	return s.buildDirectConciergeMemoryRecallResult(ctx, session, goal, intent, memories)
}

func (s *Service) retrieveConciergeMemories(ctx context.Context, goal string, queryContext domain.MemoryQueryContext) ([]*domain.MemoryWithScore, error) {
	if s == nil || s.memoryService == nil {
		return nil, nil
	}

	queries := conciergeMemoryRecallQueries(goal)
	seen := make(map[string]*domain.MemoryWithScore)
	order := make([]string, 0)
	var firstErr error

	for _, query := range queries {
		_, memories, err := s.memoryService.RetrieveAndInjectWithContext(ctx, query, queryContext)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, memory := range memories {
			if memory == nil || memory.Memory == nil {
				continue
			}
			key := strings.TrimSpace(memory.ID)
			if key == "" {
				key = strings.TrimSpace(memory.Content)
			}
			if key == "" {
				continue
			}
			if existing, ok := seen[key]; ok {
				if memory.Score > existing.Score {
					copyMemory := *memory
					seen[key] = &copyMemory
				}
				continue
			}
			copyMemory := *memory
			seen[key] = &copyMemory
			order = append(order, key)
		}
	}

	memories := make([]*domain.MemoryWithScore, 0, len(order))
	for _, key := range order {
		if memory := seen[key]; memory != nil {
			memories = append(memories, memory)
		}
	}
	if listed, _, err := s.memoryService.List(ctx, 128, 0); err == nil && len(listed) > 0 {
		fallback := make([]*domain.MemoryWithScore, 0, len(listed))
		for _, memory := range listed {
			if memory == nil {
				continue
			}
			score := conciergeRecallFallbackScore(queries, memory.Content)
			if score <= 0 {
				continue
			}
			fallback = append(fallback, &domain.MemoryWithScore{
				Memory: memory,
				Score:  score,
			})
		}
		memories = mergeConciergeMemoryResults(memories, memorypkg.FilterMemoriesForQuery(goal, fallback))
	}
	if len(memories) == 0 {
		return nil, firstErr
	}
	sort.SliceStable(memories, func(i, j int) bool {
		if memories[i] == nil || memories[i].Memory == nil {
			return false
		}
		if memories[j] == nil || memories[j].Memory == nil {
			return true
		}
		if memories[i].Score == memories[j].Score {
			return memories[i].CreatedAt.After(memories[j].CreatedAt)
		}
		return memories[i].Score > memories[j].Score
	})
	return memories, nil
}

func conciergeMemoryRecallQueries(goal string) []string {
	goal = normalizeTaskPrompt(goal)
	if goal == "" {
		return nil
	}

	seen := make(map[string]struct{})
	var queries []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || isConciergeRecallDirective(value) {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		queries = append(queries, value)
	}

	add(goal)

	fields := strings.FieldsFunc(goal, func(r rune) bool {
		switch r {
		case '，', ',', '。', '.', '？', '?', '；', ';', '！', '!', '\n', '\r':
			return true
		default:
			return unicode.IsSpace(r)
		}
	})
	for _, field := range fields {
		add(field)
		add(normalizeConciergeRecallFragment(field))
	}

	if len(queries) > 8 {
		queries = queries[:8]
	}
	return queries
}

func normalizeConciergeRecallFragment(fragment string) string {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return ""
	}

	prefixes := []string{
		"如果有人提到",
		"如果提到",
		"我之前让你记住的团队资料里",
		"我之前让你记住的",
		"之前让你记住的",
		"关于",
	}
	suffixes := []string{
		"应该找谁",
		"找谁",
		"指的是什么",
		"是什么",
		"表示什么",
		"代表什么",
		"又代表什么",
		"要做什么",
		"只用一行回答",
		"只回答",
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(fragment, prefix) {
			fragment = strings.TrimSpace(strings.TrimPrefix(fragment, prefix))
			break
		}
	}
	for _, suffix := range suffixes {
		if strings.HasSuffix(fragment, suffix) {
			fragment = strings.TrimSpace(strings.TrimSuffix(fragment, suffix))
			break
		}
	}
	return fragment
}

func conciergeRecallFallbackScore(queries []string, content string) float64 {
	contentLower := strings.ToLower(strings.TrimSpace(content))
	if contentLower == "" {
		return 0
	}
	best := 0.0
	for _, query := range queries {
		query = strings.ToLower(strings.TrimSpace(query))
		if query == "" || isConciergeRecallDirective(query) {
			continue
		}
		switch {
		case strings.Contains(contentLower, query):
			lengthBoost := 0.75
			if len([]rune(query)) >= 4 {
				lengthBoost = 0.9
			}
			if lengthBoost > best {
				best = lengthBoost
			}
		case len([]rune(query)) >= 2:
			if overlap := conciergeContentOverlap(query, contentLower); overlap > best {
				best = overlap
			}
		}
	}
	return best
}

func conciergeContentOverlap(query, content string) float64 {
	matches := 0
	for _, token := range strings.Fields(query) {
		token = strings.TrimSpace(token)
		if token != "" && strings.Contains(content, token) {
			matches++
		}
	}
	if matches == 0 {
		return 0
	}
	if matches >= 2 {
		return 0.7
	}
	return 0.45
}

func shouldDirectConciergeAnswerFromMemory(memories []*domain.MemoryWithScore) bool {
	if len(memories) == 0 {
		return false
	}
	if memories[0] != nil && memories[0].Score >= 0.85 {
		return true
	}
	if len(memories) >= 2 && memories[1] != nil && memories[1].Score >= 0.60 {
		return true
	}
	return false
}

func (s *Service) buildDirectConciergeMemoryRecallResult(ctx context.Context, session *Session, goal string, intent *IntentRecognitionResult, memories []*domain.MemoryWithScore) (*ExecutionResult, bool, error) {
	formatted := formatExplicitRecallMemories(memories)
	cfg := DefaultRunConfig()
	answer, ok, err := s.answerExplicitMemoryRecall(ctx, goal, intent, formatted, memories, cfg)
	if err != nil {
		return nil, true, err
	}
	if !ok || strings.TrimSpace(answer) == "" {
		answer = "未找到任何相关长期记忆。"
	} else if strings.EqualFold(strings.TrimSpace(answer), "I couldn't find that in memory.") && len(memories) > 0 {
		answer = fallbackExplicitRecallSnippet(memories)
	}
	now := time.Now()
	return &ExecutionResult{
		PlanID:      fmt.Sprintf("concierge-memory-recall-%d", now.UnixNano()),
		SessionID:   firstNonEmpty(s.CurrentSessionID(), sessionIDOrEmpty(session)),
		Success:     true,
		StepsTotal:  1,
		StepsDone:   1,
		StepsFailed: 0,
		StartedAt:   &now,
		CompletedAt: &now,
		ToolCalls:   1,
		ToolsUsed:   []string{"memory_recall"},
		FinalResult: strings.TrimSpace(answer),
		Duration:    "completed",
		Metadata: map[string]interface{}{
			"dispatch_mode":   "direct_concierge_memory_recall",
			"dispatch_target": s.agent.Name(),
			"dispatch_intent": "memory_recall",
			"memory_recall":   formatted,
		},
	}, true, nil
}

func fallbackExplicitRecallSnippet(memories []*domain.MemoryWithScore) string {
	if len(memories) == 0 {
		return "未找到任何相关长期记忆。"
	}
	seen := make(map[string]struct{})
	parts := make([]string, 0, 4)
	for _, memory := range memories {
		if memory == nil || memory.Memory == nil {
			continue
		}
		content := strings.TrimSpace(memory.Content)
		if content == "" {
			continue
		}
		if _, ok := seen[content]; ok {
			continue
		}
		seen[content] = struct{}{}
		parts = append(parts, strings.TrimRight(content, "。.;； "))
		if len(parts) == 4 {
			break
		}
	}
	if len(parts) == 0 {
		return "未找到任何相关长期记忆。"
	}
	return strings.Join(parts, "；")
}

func sanitizeConciergeMemoryFallbackText(text string) string {
	lower := strings.ToLower(text)
	needle := "couldn't find that in memory"
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return text
	}
	return strings.TrimSpace(text[:idx] + "信息不足")
}

func mergeConciergeMemoryResults(primary []*domain.MemoryWithScore, extras []*domain.MemoryWithScore) []*domain.MemoryWithScore {
	if len(extras) == 0 {
		return primary
	}
	seen := make(map[string]*domain.MemoryWithScore)
	order := make([]string, 0, len(primary)+len(extras))
	add := func(memory *domain.MemoryWithScore) {
		if memory == nil || memory.Memory == nil {
			return
		}
		key := strings.TrimSpace(memory.ID)
		if key == "" {
			key = strings.TrimSpace(memory.Content)
		}
		if key == "" {
			return
		}
		if existing, ok := seen[key]; ok {
			if memory.Score > existing.Score {
				copyMemory := *memory
				seen[key] = &copyMemory
			}
			return
		}
		copyMemory := *memory
		seen[key] = &copyMemory
		order = append(order, key)
	}
	for _, memory := range primary {
		add(memory)
	}
	for _, memory := range extras {
		add(memory)
	}
	out := make([]*domain.MemoryWithScore, 0, len(order))
	for _, key := range order {
		if memory := seen[key]; memory != nil {
			out = append(out, memory)
		}
	}
	return out
}

func isConciergeRecallDirective(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "只用一行回答", "只回答", "一行回答", "只用一行", "reply in one line", "reply with one line":
		return true
	default:
		return strings.HasPrefix(value, "我之前让你记住的") || strings.HasPrefix(value, "如果有人提到")
	}
}

func conciergeShortCircuitToolMetadata(result *ExecutionResult, goal string) (string, map[string]interface{}) {
	if result == nil {
		return "", nil
	}
	switch strings.TrimSpace(metadataString(result.Metadata, "dispatch_mode")) {
	case "direct_concierge_memory_save":
		return "memory_save", map[string]interface{}{"content": strings.TrimSpace(extractExplicitMemorySaveContent(goal))}
	case "direct_concierge_memory_recall":
		return "memory_recall", map[string]interface{}{"query": normalizeTaskPrompt(goal)}
	default:
		return "route_builtin_request", map[string]interface{}{"prompt": normalizeTaskPrompt(goal)}
	}
}

func conciergeShortCircuitToolResult(result *ExecutionResult) interface{} {
	if result == nil {
		return nil
	}
	switch strings.TrimSpace(metadataString(result.Metadata, "dispatch_mode")) {
	case "direct_concierge_memory_save":
		return map[string]interface{}{"status": "saved", "result": result.Text()}
	case "direct_concierge_memory_recall":
		return map[string]interface{}{"result": result.Text(), "formatted": result.Metadata["memory_recall"]}
	default:
		return result.Metadata["route_builtin_result"]
	}
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
	if blocked, ok := payload["blocked"].(bool); ok {
		result.blocked = blocked
	}
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
