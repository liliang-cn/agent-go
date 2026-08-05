package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// End-to-end proof that the discovery budget actually reaches inside the PTC
// sandbox. The handler-level test uses ToolRegistry.Call directly, which only
// shows the guard works given the right context; this one runs real JavaScript
// through Goja so the whole context chain is exercised:
//
//	Runtime.Run -> withDiscoveryBudget
//	  -> ExecuteJavascriptTool -> PTCIntegration.ExecuteCode
//	  -> ptc.Service.Execute (context.WithTimeout preserves values)
//	  -> goja Runtime.Execute -> state.ctx -> handler(state.ctx, args)
//
// If any link in that chain dropped context values, the searches below would
// all succeed and this test would fail.
func TestPTCSandboxSearchesRespectDiscoveryBudget(t *testing.T) {
	t.Parallel()

	svc, err := New("ptc-sandbox-budget").
		WithPTC(true).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLintLLM{replies: []string{""}}).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	if svc.ptcIntegration == nil {
		t.Fatal("expected PTC to be enabled")
	}

	ctx := withDiscoveryBudget(context.Background(), newDiscoveryBudget(3))

	// Five distinct rewordings, exactly the pattern seen in agentbench.
	code := `
var out = [];
var queries = ['send email', 'email', 'mail sender', 'smtp client', 'outbound mail'];
for (var i = 0; i < queries.length; i++) {
  out.push(JSON.stringify(callTool('search_available_tools', {query: queries[i]})));
}
return out.join('\n---\n');
`

	res, err := svc.ptcIntegration.ExecuteCode(ctx, code, nil)
	if err != nil {
		t.Fatalf("sandbox execution failed: %v", err)
	}
	if res == nil || !res.Success {
		t.Fatalf("sandbox execution unsuccessful: %+v", res)
	}

	output := fmt.Sprintf("%v\n%v", res.ReturnValue, res.Output)
	refused := strings.Count(output, "Tool discovery budget exhausted")
	if refused != 2 {
		t.Fatalf("expected the last 2 of 5 sandbox searches to be refused, got %d\noutput:\n%s", refused, output)
	}
}
