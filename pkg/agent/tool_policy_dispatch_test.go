package agent_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

// A run's allow and deny lists hold at dispatch: a tool the model calls
// without having been offered it does not run, and the model is told why.
func TestRunToolListsAreEnforcedAtDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  agent.RunOption
	}{
		{"denylist", agent.WithToolDenylist([]string{"wipe_disk"})},
		{"allowlist", agent.WithToolAllowlist([]string{"read_status"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wiped atomic.Int32
			llm := extensiontest.Script(
				extensiontest.CallTool("wipe_disk", map[string]interface{}{}),
				extensiontest.Answer("ok then"),
			)
			svc := extensiontest.NewService(t, llm)
			svc.AddToolWithMetadata("wipe_disk", "destroys", map[string]interface{}{"type": "object"},
				func(context.Context, map[string]interface{}) (interface{}, error) { wiped.Add(1); return "gone", nil },
				agent.ToolMetadata{Destructive: true, InterruptBehavior: agent.InterruptBehaviorCancel})
			svc.AddToolWithMetadata("read_status", "reads", map[string]interface{}{"type": "object"},
				func(context.Context, map[string]interface{}) (interface{}, error) { return "fine", nil },
				agent.ToolMetadata{ReadOnly: true, InterruptBehavior: agent.InterruptBehaviorCancel})
			out := extensiontest.Run(t, svc, "clean up", tc.opt)
			if out.Final != "ok then" {
				t.Fatalf("run ended %q (blocked %q)", out.Final, out.Blocked)
			}
			if wiped.Load() != 0 {
				t.Fatal("a withheld tool ran")
			}
			if !hasMessage(llm.Rounds()[1], "tool", "not available in this run") {
				t.Fatalf("the refusal did not reach the model:\n%+v", llm.Rounds()[1])
			}
		})
	}
}
