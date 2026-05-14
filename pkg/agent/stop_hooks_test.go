package agent

import "testing"

func TestApplyStopHookControlParsesJSONDirective(t *testing.T) {
	t.Parallel()

	data := &HookData{}
	applyStopHookControl(`{"prevent_continuation":true,"stop_reason":"stop now"}`, data)

	if !data.PreventContinuation {
		t.Fatal("expected PreventContinuation to be true")
	}
	if data.StopReason != "stop now" {
		t.Fatalf("StopReason = %q, want %q", data.StopReason, "stop now")
	}
}
