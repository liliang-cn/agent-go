package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

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
	}, dispatch)
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
	// Only 2 calls: IntentRouter + Archivist (no optimizer when NEEDS_OPTIMIZATION=no)
	if len(calls) != 2 {
		t.Fatalf("expected 2 dispatch calls (router + archivist), got %+v", calls)
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
			return "TARGET_AGENT: Assistant\nINTENT_TYPE: general_qa\nREASON: vague question\nNEEDS_OPTIMIZATION: yes", nil
		case defaultPromptOptimizerAgentName:
			return optimizedPromptBeginMarker + "\nWhat is the weather forecast for tomorrow?\n" + optimizedPromptEndMarker, nil
		case defaultAssistantAgentName:
			return "It will be sunny.", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "明天天气怎么样", domain.MemoryQueryContext{
		AgentID: "Concierge",
		TeamID:  defaultTeamID,
	}, dispatch)
	if err != nil {
		t.Fatalf("routeBuiltInRequestWithDispatcher failed: %v", err)
	}

	if result.TargetAgent != defaultAssistantAgentName {
		t.Fatalf("expected Assistant target, got %s", result.TargetAgent)
	}

	mu.Lock()
	defer mu.Unlock()
	// 3 calls: IntentRouter + PromptOptimizer + Assistant
	if len(calls) != 3 {
		t.Fatalf("expected 3 dispatch calls, got %+v", calls)
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

