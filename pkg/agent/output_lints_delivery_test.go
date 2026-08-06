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
	email := []DeliverableRequirement{{Kind: "email", Description: "the tax result to bob@example.com"}}

	// The run owed an email, a matching tool WAS available, and it was never
	// called -> reject. This is the "computed it but never sent it" failure the
	// contract exists to make impossible.
	ok, reason := lint.Check("The tax owed is 1,240.", LintContext{
		Deliverables:   email,
		ToolCalls:      []string{"fs_read"},
		AvailableTools: []string{"fs_read", "send_email"},
	})
	if ok {
		t.Fatal("expected an undelivered email task to be rejected")
	}
	if reason == "" {
		t.Fatal("expected a human-readable reason")
	}

	// Same deliverable, but the delivery tool was actually called -> pass.
	if ok, reason := lint.Check("Sent.", LintContext{
		Deliverables:   email,
		ToolCalls:      []string{"send_email"},
		AvailableTools: []string{"send_email"},
	}); !ok {
		t.Fatalf("expected a delivered task to pass, got %q", reason)
	}

	// No delivery tool exists at all -> not the agent's fault, pass.
	if ok, reason := lint.Check("The tax owed is 1,240.", LintContext{
		Deliverables:   email,
		ToolCalls:      []string{"fs_read"},
		AvailableTools: []string{"fs_read"},
	}); !ok {
		t.Fatalf("expected a run with no delivery capability to pass, got %q", reason)
	}

	// Nothing owed -> pass, whatever the goal text happened to say.
	if ok, reason := lint.Check("42", LintContext{
		Goal:           "send me an email with 6*7",
		AvailableTools: []string{"send_email"},
	}); !ok {
		t.Fatalf("expected a run with no deliverables to pass, got %q", reason)
	}

	// The lint is language-agnostic because it never reads the goal: a Japanese
	// request that the old phrase table had no entry for is enforced identically.
	if ok, _ := lint.Check("計算しました。", LintContext{
		Deliverables:   []DeliverableRequirement{{Kind: "email", Description: "結果をメールで送る"}},
		ToolCalls:      []string{"fs_read"},
		AvailableTools: []string{"send_email"},
	}); ok {
		t.Fatal("expected the contract to hold regardless of the request language")
	}
}
