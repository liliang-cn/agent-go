package budgetgate_test

import (
	"strings"
	"testing"

	"example.com/budgetgate/budgetgate"

	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

// The whole point of extensiontest: this is a real run through the real
// loop, with no model behind it, and the gate is exercised at the seam the
// framework actually calls.
func TestGateRefusesOnceTheBudgetIsSpent(t *testing.T) {
	gate := budgetgate.New(1.00)
	llm := extensiontest.Script(extensiontest.Answer("ok"))
	svc := extensiontest.NewService(t, llm, gate)

	if out := extensiontest.Run(t, svc, "first"); out.Final != "ok" {
		t.Fatalf("first run: %+v", out)
	}

	gate.Add(1.00) // the ledger says we are at the ceiling
	out := extensiontest.Run(t, svc, "second")
	if out.Final != "" || !strings.Contains(out.Blocked, "budget of $1.00 is spent") {
		t.Fatalf("second run should be refused: final=%q blocked=%q", out.Final, out.Blocked)
	}
	if llm.Calls() != 1 {
		t.Fatalf("model was called %d times; the refused run must not reach it", llm.Calls())
	}
	if gate.Refused() != 1 {
		t.Fatalf("refused = %d", gate.Refused())
	}
}
