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
	email := []DeliverableRequirement{{
		Kind:        "email",
		Description: "the tax result to bob@example.com",
		// The constraint extraction picked this out of the run's own tool
		// catalog; the lint does nothing but compare it against the trace.
		SatisfiedBy: "send_email",
	}}

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

	// No delivery tool exists at all (the extraction found nothing in the
	// catalog to pick) -> not the agent's fault, pass.
	if ok, reason := lint.Check("The tax owed is 1,240.", LintContext{
		Deliverables:   []DeliverableRequirement{{Kind: "email", Description: "the tax result"}},
		ToolCalls:      []string{"fs_read"},
		AvailableTools: []string{"fs_read"},
	}); !ok {
		t.Fatalf("expected a run with no delivery capability to pass, got %q", reason)
	}

	// The extraction named a tool that is not actually registered -> treat it
	// as a missing capability rather than an unsatisfiable contract.
	if ok, reason := lint.Check("The tax owed is 1,240.", LintContext{
		Deliverables:   email,
		ToolCalls:      []string{"fs_read"},
		AvailableTools: []string{"fs_read"},
	}); !ok {
		t.Fatalf("expected a hallucinated tool pick to pass, got %q", reason)
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
		Deliverables: []DeliverableRequirement{
			{Kind: "email", Description: "結果をメールで送る", SatisfiedBy: "send_email"},
		},
		ToolCalls:      []string{"fs_read"},
		AvailableTools: []string{"send_email"},
	}); ok {
		t.Fatal("expected the contract to hold regardless of the request language")
	}
}

func TestRequestedActionContract(t *testing.T) {
	lint := RequestedActionContract()
	if lint.Name() != "requested_action_contract" {
		t.Fatalf("name = %q", lint.Name())
	}
	reminder := []RequestedAction{{
		Kind:        "reminder",
		Description: "remind me to refill my bottle each morning",
		SatisfiedBy: "set_reminder",
	}}

	// The benchmark failure this exists for: the model answers the computable
	// half, claims the reminder is set, and never calls the tool sitting right
	// there. Reject.
	ok, reason := lint.Check("Your daily total is 2 L. I've set a reminder for you.", LintContext{
		RequestedActions: reminder,
		ToolCalls:        []string{"resolve_datetime"},
		AvailableTools:   []string{"resolve_datetime", "set_reminder"},
	})
	if ok {
		t.Fatal("expected a claimed-but-uncalled reminder to be rejected")
	}
	if reason == "" {
		t.Fatal("expected a human-readable reason")
	}

	// Tool actually called -> pass.
	if ok, reason := lint.Check("Done — reminder set for 08:00 daily.", LintContext{
		RequestedActions: reminder,
		ToolCalls:        []string{"set_reminder"},
		AvailableTools:   []string{"set_reminder"},
	}); !ok {
		t.Fatalf("expected a performed action to pass, got %q", reason)
	}

	// No tool in this run can set a reminder (extraction picked nothing) ->
	// not the agent's fault, pass.
	if ok, reason := lint.Check("Your daily total is 2 L.", LintContext{
		RequestedActions: []RequestedAction{{Kind: "reminder", Description: "refill reminder"}},
		AvailableTools:   []string{"resolve_datetime"},
	}); !ok {
		t.Fatalf("expected a run with no reminder capability to pass, got %q", reason)
	}

	// Nothing requested -> pass regardless of what the goal text said.
	if ok, reason := lint.Check("2 L.", LintContext{
		Goal:           "set a reminder to drink water",
		AvailableTools: []string{"set_reminder"},
	}); !ok {
		t.Fatalf("expected a run with no requested actions to pass, got %q", reason)
	}

	// Language-agnostic: the lint never reads the goal or the answer, so a
	// Japanese request enforces identically.
	if ok, _ := lint.Check("リマインダーを設定しました。", LintContext{
		RequestedActions: []RequestedAction{
			{Kind: "reminder", Description: "毎朝の水の補充", SatisfiedBy: "set_reminder"},
		},
		ToolCalls:      []string{"resolve_datetime"},
		AvailableTools: []string{"set_reminder"},
	}); ok {
		t.Fatal("expected the contract to hold regardless of the request language")
	}
}
