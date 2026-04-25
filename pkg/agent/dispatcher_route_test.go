package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestRouteBuiltInRequestDispatchesArchivistWithMemorySavePrefix(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		calls       []string
		finalPrompt string
	)

	dispatch := func(ctx context.Context, agentName, prompt string, opts []RunOption) (string, error) {
		mu.Lock()
		calls = append(calls, agentName)
		mu.Unlock()

		switch agentName {
		case defaultIntentRouterAgentName:
			return "TARGET_AGENT: Archivist\nINTENT_TYPE: schedule_event\nREASON: schedule fact\nNEEDS_OPTIMIZATION: no", nil
		case defaultPromptOptimizerAgentName:
			return optimizedPromptBeginMarker + "\n明天下午17：00去万达吃饭\n" + optimizedPromptEndMarker, nil
		case defaultArchivistAgentName:
			finalPrompt = prompt
			return "已记住。", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "明天下午17：00去万达吃饭", domain.MemoryQueryContext{
		AgentID: "Dispatcher",
		TeamID:  defaultTeamID,
	}, nil, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}

	if result.TargetAgent != defaultArchivistAgentName {
		t.Fatalf("expected Archivist target, got %+v", result)
	}
	if result.IntentType != "schedule_event" {
		t.Fatalf("expected schedule_event intent, got %+v", result)
	}
	if result.Result != "已记住。" {
		t.Fatalf("unexpected final result: %+v", result)
	}
	// Archivist memory_save prompts get "记住：" prefix
	if finalPrompt != "记住：明天下午17：00去万达吃饭" {
		t.Fatalf("expected memory-save dispatch prompt with prefix, got %q", finalPrompt)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("expected 3 dispatch calls (router + optimizer + archivist), got %+v", calls)
	}
}

func TestRouteBuiltInRequestRunsOptimizerWhenNeeded(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		calls []string
	)

	dispatch := func(ctx context.Context, agentName, prompt string, opts []RunOption) (string, error) {
		mu.Lock()
		calls = append(calls, agentName)
		mu.Unlock()

		switch agentName {
		case defaultIntentRouterAgentName:
			return "TARGET_AGENT: Operator\nINTENT_TYPE: web_search\nREASON: current information request\nNEEDS_OPTIMIZATION: yes", nil
		case defaultPromptOptimizerAgentName:
			return optimizedPromptBeginMarker + "\nWhat is the weather forecast for tomorrow?\n" + optimizedPromptEndMarker, nil
		case defaultOperatorAgentName:
			return "It will be sunny.", nil
		case defaultVerifierAgentName:
			return "VERIFIED_COMPLETE: Weather lookup completed.", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "明天天气怎么样", domain.MemoryQueryContext{
		AgentID: "Dispatcher",
		TeamID:  defaultTeamID,
	}, nil, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}

	if result.TargetAgent != defaultOperatorAgentName {
		t.Fatalf("expected Operator target, got %s", result.TargetAgent)
	}

	mu.Lock()
	defer mu.Unlock()
	// 3 calls: IntentRouter + PromptOptimizer + Operator
	if len(calls) != 3 {
		t.Fatalf("expected 3 dispatch calls, got %+v", calls)
	}
}

func TestRouteBuiltInRequestStartsRouterAndOptimizerConcurrently(t *testing.T) {
	t.Parallel()

	routerStarted := make(chan struct{})
	optimizerStarted := make(chan struct{})

	dispatch := func(ctx context.Context, agentName, prompt string, opts []RunOption) (string, error) {
		switch agentName {
		case defaultIntentRouterAgentName:
			close(routerStarted)
			select {
			case <-optimizerStarted:
				return "TARGET_AGENT: Responder\nINTENT_TYPE: general_qa\nREASON: parallel routing\nNEEDS_OPTIMIZATION: no", nil
			case <-time.After(500 * time.Millisecond):
				return "", fmt.Errorf("optimizer did not start before router finished")
			}
		case defaultPromptOptimizerAgentName:
			close(optimizerStarted)
			select {
			case <-routerStarted:
				return optimizedPromptBeginMarker + "\nparallel prompt\n" + optimizedPromptEndMarker, nil
			case <-time.After(500 * time.Millisecond):
				return "", fmt.Errorf("router did not start before optimizer finished")
			}
		case defaultResponderAgentName:
			return "ok", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "帮我总结一下", domain.MemoryQueryContext{
		AgentID: "Dispatcher",
		TeamID:  defaultTeamID,
	}, nil, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}
	if result.TargetAgent != defaultResponderAgentName {
		t.Fatalf("expected Responder target, got %+v", result)
	}
}

func TestParseIntentRouterDecisionNormalizesTargetAgent(t *testing.T) {
	t.Parallel()

	decision := parseIntentRouterDecision("TARGET_AGENT: archivist\nINTENT_TYPE: memory_save\nREASON: durable fact\nNEEDS_OPTIMIZATION: no")
	if decision.TargetAgent != defaultArchivistAgentName {
		t.Fatalf("expected normalized Archivist target, got %+v", decision)
	}
	if decision.IntentType != "memory_save" {
		t.Fatalf("unexpected intent type: %+v", decision)
	}
	if decision.NeedsOptimization {
		t.Fatal("expected NeedsOptimization=false")
	}
}

func TestParseIntentRouterDecisionParsesNeedsOptimization(t *testing.T) {
	t.Parallel()

	decision := parseIntentRouterDecision("TARGET_AGENT: Responder\nINTENT_TYPE: general_qa\nREASON: test\nNEEDS_OPTIMIZATION: yes")
	if !decision.NeedsOptimization {
		t.Fatal("expected NeedsOptimization=true")
	}
}

func TestRouteBuiltInRequestStabilizesExplicitMemorySaveRouting(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		calls       []string
		finalPrompt string
	)

	dispatch := func(ctx context.Context, agentName, prompt string, opts []RunOption) (string, error) {
		mu.Lock()
		calls = append(calls, agentName)
		mu.Unlock()

		switch agentName {
		case defaultIntentRouterAgentName:
			return "TARGET_AGENT: Responder\nINTENT_TYPE: general_qa\nREASON: misread request\nNEEDS_OPTIMIZATION: yes", nil
		case defaultPromptOptimizerAgentName:
			return optimizedPromptBeginMarker + "\n请先解释“夜航计划”具体是什么意思\n" + optimizedPromptEndMarker, nil
		case defaultArchivistAgentName:
			finalPrompt = prompt
			return "已记住。", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "请记住：北极星项目组代号是 Nebula-42。", domain.MemoryQueryContext{
		AgentID: "Dispatcher",
		TeamID:  defaultTeamID,
	}, nil, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}

	if result.TargetAgent != defaultArchivistAgentName {
		t.Fatalf("expected Archivist target, got %+v", result)
	}
	if result.IntentType != "memory_save" {
		t.Fatalf("expected memory_save intent, got %+v", result)
	}
	if result.OptimizedPrompt != "请记住：北极星项目组代号是 Nebula-42。" {
		t.Fatalf("expected original prompt to be preserved, got %q", result.OptimizedPrompt)
	}
	if finalPrompt != "请记住：北极星项目组代号是 Nebula-42。" {
		t.Fatalf("expected Archivist final prompt to preserve memory save request, got %q", finalPrompt)
	}
	if result.Result != "已记住。" {
		t.Fatalf("unexpected final result: %+v", result)
	}
	if len(calls) != 3 {
		t.Fatalf("expected router + optimizer + archivist dispatch calls, got %+v", calls)
	}
}

func TestRouteBuiltInRequestOverridesResponderWithArchivistForImplicitMemoryRecall(t *testing.T) {
	t.Parallel()

	dispatch := func(ctx context.Context, agentName, prompt string, opts []RunOption) (string, error) {
		switch agentName {
		case defaultIntentRouterAgentName:
			return "TARGET_AGENT: Responder\nINTENT_TYPE: general_qa\nREASON: explanatory question\nNEEDS_OPTIMIZATION: no", nil
		case defaultPromptOptimizerAgentName:
			return optimizedPromptBeginMarker + "\n如果有人提到移动端掉帧，应该找谁？夜航计划指的是什么？蓝色标签又代表什么？只用一行回答。\n" + optimizedPromptEndMarker, nil
		case defaultArchivistAgentName:
			return "移动端掉帧找宋屿；夜航计划指灰度演练和回滚预案；蓝色标签表示待验证。", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "如果有人提到移动端掉帧，应该找谁？夜航计划指的是什么？蓝色标签又代表什么？只用一行回答。", domain.MemoryQueryContext{
		AgentID: "Dispatcher",
		TeamID:  defaultTeamID,
	}, nil, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}
	if result.TargetAgent != defaultArchivistAgentName {
		t.Fatalf("expected Archivist target, got %+v", result)
	}
	if result.IntentType != "memory_recall" {
		t.Fatalf("expected memory_recall intent, got %+v", result)
	}
	if result.Result != "移动端掉帧找宋屿；夜航计划指灰度演练和回滚预案；蓝色标签表示待验证。" {
		t.Fatalf("unexpected final result: %+v", result)
	}
}

func TestFallbackBuiltInRouteDecisionDefaultsToResponderForGenericRequests(t *testing.T) {
	t.Parallel()

	decision := fallbackBuiltInRouteDecision("让宠物狗跑起来")
	// Fallback heuristic uses only intent recognition patterns; generic action
	// requests without file/memory/search patterns default to Responder.
	if decision.TargetAgent != defaultResponderAgentName {
		t.Fatalf("expected Responder target for generic request, got %+v", decision)
	}
}

func TestFallbackBuiltInRouteDecisionUsesExecutionHintsForCurrentInfo(t *testing.T) {
	t.Parallel()

	decision := fallbackBuiltInRouteDecision("What's the latest weather in Shanghai today?")
	if decision.TargetAgent != defaultOperatorAgentName {
		t.Fatalf("expected Operator target for current-info request, got %+v", decision)
	}
}

func TestRouteBuiltInRequestVerifiesOperatorCompletion(t *testing.T) {
	t.Parallel()

	var prompts []string
	var promptsMu sync.Mutex
	dispatch := func(ctx context.Context, agentName, prompt string, opts []RunOption) (string, error) {
		promptsMu.Lock()
		prompts = append(prompts, agentName+": "+prompt)
		promptsMu.Unlock()
		switch agentName {
		case defaultIntentRouterAgentName:
			return "TARGET_AGENT: Operator\nINTENT_TYPE: tool_execution\nREASON: execution request\nNEEDS_OPTIMIZATION: no", nil
		case defaultPromptOptimizerAgentName:
			return optimizedPromptBeginMarker + "\n让宠物狗跑起来\n" + optimizedPromptEndMarker, nil
		case defaultOperatorAgentName:
			return "I invoked the walking action.", nil
		case defaultVerifierAgentName:
			return "VERIFIED_COMPLETE: Current state reports the desktop pet is walking.", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "让宠物狗跑起来", domain.MemoryQueryContext{}, nil, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}
	if result.TargetAgent != defaultOperatorAgentName {
		t.Fatalf("expected Operator target, got %+v", result)
	}
	if !strings.Contains(result.Result, "Verifier confirmation: Current state reports the desktop pet is walking.") {
		t.Fatalf("unexpected verified result: %+v", result)
	}
	if !strings.HasPrefix(result.VerificationResult, "VERIFIED_COMPLETE:") {
		t.Fatalf("expected verification result to be recorded, got %+v", result)
	}
}

func TestRouteBuiltInRequestRoutesToOperatorWhenMCPToolsProvided(t *testing.T) {
	t.Parallel()

	// Simulate MCP tool context that the IntentRouter can use to decide routing.
	mcpTools := []string{"mcp_husky-pet_run: Make the pet dog run", "mcp_husky-pet_stop: Stop the pet dog"}

	dispatch := func(ctx context.Context, agentName, prompt string, opts []RunOption) (string, error) {
		switch agentName {
		case defaultIntentRouterAgentName:
			// IntentRouter sees the MCP tool list in the prompt and picks Operator.
			return "TARGET_AGENT: Operator\nINTENT_TYPE: tool_execution\nREASON: MCP tool available\nNEEDS_OPTIMIZATION: no", nil
		case defaultPromptOptimizerAgentName:
			return optimizedPromptBeginMarker + "\n让宠物狗跑起来\n" + optimizedPromptEndMarker, nil
		case defaultOperatorAgentName:
			return "Walking action invoked.", nil
		case defaultVerifierAgentName:
			return "VERIFIED_COMPLETE: Desktop pet walking started successfully.", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "让宠物狗跑起来", domain.MemoryQueryContext{}, mcpTools, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}
	if result.TargetAgent != defaultOperatorAgentName {
		t.Fatalf("expected Operator when MCP tools provided, got %+v", result)
	}
}
