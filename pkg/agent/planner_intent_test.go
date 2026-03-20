package agent

import "testing"

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
}

func TestIsExplicitMemoryRecallIntentPrefersIntentSignal(t *testing.T) {
	intent := &IntentRecognitionResult{IntentType: "memory_recall", Confidence: 0.9}
	if !isExplicitMemoryRecallIntent("short query", intent) {
		t.Fatal("expected memory_recall intent to trigger explicit recall path")
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
