package agent

import "testing"

func TestApplyStopHookControlParsesJSONDirective(t *testing.T) {
	t.Parallel()

	result := &StopHookResult{}
	applyStopHookControl(`{"prevent_continuation":true,"stop_reason":"stop now"}`, result)

	if !result.PreventContinuation {
		t.Fatal("expected PreventContinuation to be true")
	}
	if result.StopReason != "stop now" {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, "stop now")
	}
}
