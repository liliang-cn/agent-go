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
		AgentID: "Concierge",
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
		AgentID: "Concierge",
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
	// 4 calls: IntentRouter + PromptOptimizer + Operator + Verifier
	if len(calls) != 4 {
		t.Fatalf("expected 4 dispatch calls, got %+v", calls)
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
				return "TARGET_AGENT: Assistant\nINTENT_TYPE: general_qa\nREASON: parallel routing\nNEEDS_OPTIMIZATION: no", nil
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
		case defaultAssistantAgentName:
			return "ok", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "帮我总结一下", domain.MemoryQueryContext{
		AgentID: "Concierge",
		TeamID:  defaultTeamID,
	}, nil, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}
	if result.TargetAgent != defaultAssistantAgentName {
		t.Fatalf("expected Assistant target, got %+v", result)
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

	decision := parseIntentRouterDecision("TARGET_AGENT: Assistant\nINTENT_TYPE: general_qa\nREASON: test\nNEEDS_OPTIMIZATION: yes")
	if !decision.NeedsOptimization {
		t.Fatal("expected NeedsOptimization=true")
	}
}

func TestFallbackBuiltInRouteDecisionDefaultsToAssistantForGenericRequests(t *testing.T) {
	t.Parallel()

	decision := fallbackBuiltInRouteDecision("让宠物狗跑起来")
	// Fallback heuristic uses only intent recognition patterns; generic action
	// requests without file/memory/search patterns default to Assistant.
	if decision.TargetAgent != defaultAssistantAgentName {
		t.Fatalf("expected Assistant target for generic request, got %+v", decision)
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
	dispatch := func(ctx context.Context, agentName, prompt string, opts []RunOption) (string, error) {
		prompts = append(prompts, agentName+": "+prompt)
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
