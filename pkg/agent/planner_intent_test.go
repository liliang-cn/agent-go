package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestPlannerFallbackIntentRecognitionDetectsMemoryRecall(t *testing.T) {
	p := &Planner{}

	intent := p.fallbackIntentRecognition("What is the secret code I asked you to remember? Reply with only the code.")
	if intent == nil {
		t.Fatal("expected intent result")
	}
	if intent.IntentType != "memory_recall" {
		t.Fatalf("expected memory_recall intent, got %q", intent.IntentType)
	}
	if intent.Confidence < 0.8 {
		t.Fatalf("expected boosted confidence for memory_recall, got %.2f", intent.Confidence)
	}
	if !intent.RequiresTools || intent.PreferredAgent != defaultArchivistAgentName {
		t.Fatalf("expected execution hints for memory_recall, got %+v", intent)
	}
}

func TestPlannerFallbackIntentRecognitionDetectsMemorySave(t *testing.T) {
	p := &Planner{}

	intent := p.fallbackIntentRecognition("Please remember that my favorite drink is oolong tea.")
	if intent == nil {
		t.Fatal("expected intent result")
	}
	if intent.IntentType != "memory_save" {
		t.Fatalf("expected memory_save intent, got %q", intent.IntentType)
	}
	if intent.Confidence < 0.8 {
		t.Fatalf("expected boosted confidence for memory_save, got %.2f", intent.Confidence)
	}
	if !intent.RequiresTools || intent.PreferredAgent != defaultArchivistAgentName {
		t.Fatalf("expected execution hints for memory_save, got %+v", intent)
	}
}

func TestPlannerFallbackIntentRecognitionDetectsImplicitScheduleMemorySave(t *testing.T) {
	p := &Planner{}

	intent := p.fallbackIntentRecognition("明天下午3：00开启动会。")
	if intent == nil {
		t.Fatal("expected intent result")
	}
	if intent.IntentType != "memory_save" {
		t.Fatalf("expected implicit schedule to map to memory_save, got %q", intent.IntentType)
	}
	if intent.Confidence < 0.7 {
		t.Fatalf("expected confidence for implicit schedule memory_save, got %.2f", intent.Confidence)
	}
	if intent.Transition != "tool_first" {
		t.Fatalf("expected tool_first transition, got %+v", intent)
	}
}

func TestIsExplicitMemoryRecallIntentPrefersIntentSignal(t *testing.T) {
	intent := &IntentRecognitionResult{IntentType: "memory_recall", Confidence: 0.9}
	if !isExplicitMemoryRecallIntent("short query", intent) {
		t.Fatal("expected memory_recall intent to trigger explicit recall path")
	}
}

func TestIsExplicitMemoryRecallIntentDetectsScheduleRecallQuery(t *testing.T) {
	if !isExplicitMemoryRecallIntent("明天有什么安排？", nil) {
		t.Fatal("expected schedule recall query to trigger explicit recall path")
	}
}

func TestIsExplicitMemorySaveIntentPrefersIntentSignal(t *testing.T) {
	intent := &IntentRecognitionResult{IntentType: "memory_save", Confidence: 0.9}
	if !isExplicitMemorySaveIntent("short query", intent) {
		t.Fatal("expected memory_save intent to trigger explicit save path")
	}
}

func TestIsExplicitMemorySaveIntentRejectsQuestionLikeGoal(t *testing.T) {
	intent := &IntentRecognitionResult{IntentType: "memory_save", Confidence: 0.9}
	if isExplicitMemorySaveIntent("What is my favorite snack? Reply with only the snack.", intent) {
		t.Fatal("did not expect question-like goal to trigger explicit memory save path")
	}
}

func TestExplicitMemorySaveHelpersHandleTeamEnvelope(t *testing.T) {
	goal := "Team task context:\n- Target team agent: Archivist\n- Execute only the work described in the Task section below.\n\nTask:\n记住：用户明天17:00去万达广场吃饭。"

	if !isExplicitMemorySaveIntent(goal, nil) {
		t.Fatal("expected team envelope memory-save task to trigger explicit save path")
	}

	got := extractExplicitMemorySaveContent(goal)
	if got != "用户明天17:00去万达广场吃饭。" {
		t.Fatalf("unexpected extracted memory content: %q", got)
	}
}

func TestPlannerRuleBasedIntentRecognitionDetectsFileEdit(t *testing.T) {
	p := &Planner{}
	intent := p.ruleBasedIntentRecognition("Please update ./README.md to add installation steps.")
	if intent == nil {
		t.Fatal("expected intent result")
	}
	if intent.IntentType != "file_edit" {
		t.Fatalf("expected file_edit, got %q", intent.IntentType)
	}
	if intent.TargetFile != "./README.md" {
		t.Fatalf("expected target file, got %q", intent.TargetFile)
	}
	if !intent.RequiresTools || intent.PreferredAgent != defaultOperatorAgentName {
		t.Fatalf("expected operator-oriented hints, got %+v", intent)
	}
}

func TestPlannerRuleBasedIntentRecognitionDetectsCurrentInfoWebSearch(t *testing.T) {
	p := &Planner{}
	intent := p.ruleBasedIntentRecognition("What's the latest weather forecast for Shanghai today?")
	if intent == nil {
		t.Fatal("expected intent result")
	}
	if intent.IntentType != "web_search" {
		t.Fatalf("expected web_search, got %q", intent.IntentType)
	}
	if intent.PreferredAgent != defaultOperatorAgentName {
		t.Fatalf("expected operator preferred agent, got %+v", intent)
	}
}

func TestPlannerRuleBasedIntentRecognitionDetectsRAGQuery(t *testing.T) {
	p := &Planner{
		tools: []domain.ToolDefinition{
			{Function: domain.ToolFunction{Name: "rag_query"}},
		},
	}
	intent := p.ruleBasedIntentRecognition("Search the knowledge base for our deployment checklist.")
	if intent == nil {
		t.Fatal("expected intent result")
	}
	if intent.IntentType != "rag_query" {
		t.Fatalf("expected rag_query, got %q", intent.IntentType)
	}
	if intent.Transition != "prefer_tooling" {
		t.Fatalf("expected prefer_tooling transition, got %+v", intent)
	}
}
