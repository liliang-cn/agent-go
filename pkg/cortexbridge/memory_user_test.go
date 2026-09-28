package cortexbridge

import (
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// Through the real loop: a run started WithMemoryUser hands that user to a
// CortexDB tool the model called without one, and a run for someone else
// hands theirs — one service, two people, no shared user_id.
func TestRunMemoryUserReachesCortexTools(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"user_id": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}
	tb := &inputToolbox{inputs: map[string]map[string]interface{}{}, defs: []cortexdb.ToolDefinition{
		{Name: "knowledge_memory_remember", InputSchema: schema},
	}}
	llm := extensiontest.Script(
		extensiontest.CallTool("knowledge_memory_remember", map[string]interface{}{"content": "likes tea"}),
		extensiontest.Answer("noted"),
		extensiontest.CallTool("knowledge_memory_remember", map[string]interface{}{"content": "likes coffee"}),
		extensiontest.Answer("noted"),
	)
	svc := extensiontest.NewService(t, llm)
	if _, err := RegisterToolbox(svc, tb); err != nil {
		t.Fatal(err)
	}

	for _, user := range []string{"alice", "bob"} {
		out := extensiontest.Run(t, svc, "remember what I like", agent.WithMemoryUser(user))
		if out.Final != "noted" {
			t.Fatalf("run for %s ended %q", user, out.Final)
		}
		if got := tb.inputs["knowledge_memory_remember"]["user_id"]; got != user {
			t.Fatalf("run for %s: tool got user_id %v", user, got)
		}
	}
}
