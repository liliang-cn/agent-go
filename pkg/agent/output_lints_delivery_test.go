package agent

import "testing"

func TestNonEmptyFinalAnswer(t *testing.T) {
	lint := NonEmptyFinalAnswer()
	if lint.Name() != "non_empty_final_answer" {
		t.Fatalf("name = %q", lint.Name())
	}
	for _, empty := range []string{"", "   ", "\n\t", "x"} {
		if ok, reason := lint.Check(empty, LintContext{}); ok {
			t.Fatalf("expected %q to be rejected (reason=%q)", empty, reason)
		}
	}
	if ok, reason := lint.Check("Here is the answer: 42.", LintContext{}); !ok {
		t.Fatalf("expected a real answer to pass, got %q", reason)
	}
}

func TestTaskDeliveryContract(t *testing.T) {
	lint := TaskDeliveryContract()

	// The goal names a delivery action, a matching tool WAS available, and it
	// was never called -> reject. This is the "computed it but never sent it"
	// failure the contract exists to make impossible.
	ok, reason := lint.Check("The tax owed is 1,240.", LintContext{
		Goal:           "Compute my tax and send an email with the result to bob@example.com",
		ToolCalls:      []string{"fs_read"},
		AvailableTools: []string{"fs_read", "send_email"},
	})
	if ok {
		t.Fatal("expected an undelivered email task to be rejected")
	}
	if reason == "" {
		t.Fatal("expected a human-readable reason")
	}

	// Same goal, but the delivery tool was actually called -> pass.
	if ok, reason := lint.Check("Sent.", LintContext{
		Goal:           "Compute my tax and send an email with the result to bob@example.com",
		ToolCalls:      []string{"send_email"},
		AvailableTools: []string{"send_email"},
	}); !ok {
		t.Fatalf("expected a delivered task to pass, got %q", reason)
	}

	// No delivery tool exists at all -> not the agent's fault, pass.
	if ok, reason := lint.Check("The tax owed is 1,240.", LintContext{
		Goal:           "Compute my tax and send an email with the result",
		ToolCalls:      []string{"fs_read"},
		AvailableTools: []string{"fs_read"},
	}); !ok {
		t.Fatalf("expected a run with no delivery capability to pass, got %q", reason)
	}

	// Goal with no delivery action at all -> pass.
	if ok, reason := lint.Check("42", LintContext{
		Goal:           "what is 6*7?",
		AvailableTools: []string{"send_email"},
	}); !ok {
		t.Fatalf("expected a non-delivery goal to pass, got %q", reason)
	}
}

func TestNoToolInstructionWithholdsEveryTool(t *testing.T) {
	tools := []struct{ name string }{
		{"fs_write"}, {"execute_javascript"}, {"search_available_tools"},
	}
	for _, tool := range tools {
		if isTaskTerminalToolName(tool.name) {
			t.Fatalf("%s should not be classified as a terminal signal", tool.name)
		}
	}
	if !looksLikeNoToolInstruction("Without using any tools, name the largest planet.") {
		t.Fatal("expected the English phrasing to be recognised")
	}
	if !looksLikeNoToolInstruction("不要使用任何工具，直接回答。") {
		t.Fatal("expected the Chinese phrasing to be recognised")
	}
	if looksLikeNoToolInstruction("use a tool to compute 5*4") {
		t.Fatal("an ordinary request that mentions tools must not be treated as a ban")
	}
}
