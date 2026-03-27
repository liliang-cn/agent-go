package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestRouteBuiltInRequestWithDispatcherUsesParallelPreprocessingAndArchivistMemorySavePrompt(t *testing.T) {
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
			return "TARGET_AGENT: Archivist\nINTENT_TYPE: schedule_event\nREASON: schedule fact", nil
		case defaultPromptOptimizerAgentName:
			return optimizedPromptBeginMarker + "\n用户明天17:00去万达广场吃饭。\n" + optimizedPromptEndMarker, nil
		case defaultArchivistAgentName:
			finalPrompt = prompt
			return "已记住。", nil
		default:
			return "", fmt.Errorf("unexpected agent %s", agentName)
		}
	}

	result, err := routeBuiltInRequestWithDispatcher(context.Background(), "用户请求：\"明天下午17：00去万达吃饭\"。请按最合适的内置专长处理该请求。", domain.MemoryQueryContext{
		AgentID: "Concierge",
		SquadID: defaultSquadID,
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
	if finalPrompt != "记住：用户明天17:00去万达广场吃饭。" {
		t.Fatalf("expected explicit memory-save dispatch prompt, got %q", finalPrompt)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("expected 3 dispatch calls, got %+v", calls)
	}
	if !containsStr(calls, defaultIntentRouterAgentName) || !containsStr(calls, defaultPromptOptimizerAgentName) || !containsStr(calls, defaultArchivistAgentName) {
		t.Fatalf("expected route to use router, optimizer, and archivist, got %+v", calls)
	}
}

func TestParseIntentRouterDecisionNormalizesTargetAgent(t *testing.T) {
	t.Parallel()

	decision := parseIntentRouterDecision("TARGET_AGENT: archivist\nINTENT_TYPE: memory_save\nREASON: durable fact")
	if decision.TargetAgent != defaultArchivistAgentName {
		t.Fatalf("expected normalized Archivist target, got %+v", decision)
	}
	if decision.IntentType != "memory_save" {
		t.Fatalf("unexpected intent type: %+v", decision)
	}
}
