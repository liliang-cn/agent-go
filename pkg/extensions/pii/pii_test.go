package pii

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func TestRedactMasksEachKind(t *testing.T) {
	e := New()
	in := "mail alice@example.com, call +1 415 555 0142 or 13812345678, card 4111 1111 1111 1111, key sk-abcdefghijklmnopqrstuvwxyz"
	out, kinds := e.Redact(in)
	for _, leak := range []string{"alice@example.com", "415 555 0142", "13812345678", "4111 1111 1111 1111", "sk-abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q survived: %s", leak, out)
		}
	}
	for _, want := range []string{"a***@example.com", "***0142", "***5678", "**** **** **** 1111", "sk-a…[redacted]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected mask %q in: %s", want, out)
		}
	}
	if len(kinds) != 4 {
		t.Fatalf("kinds = %v", kinds)
	}
	st := e.Stats()
	if st[Email] != 1 || st[Phone] != 2 || st[CreditCard] != 1 || st[Secret] != 1 {
		t.Fatalf("stats = %v", st)
	}
}

// A run of digits that is not a card number is left alone: the Luhn check
// is what separates an order id from a PAN.
func TestCardDetectionNeedsLuhn(t *testing.T) {
	e := New()
	out, kinds := e.Redact("order 1234 5678 9012 3456 shipped")
	if len(kinds) != 0 || !strings.Contains(out, "1234 5678 9012 3456") {
		t.Fatalf("non-Luhn digits were masked: %s %v", out, kinds)
	}
}

func TestAfterToolWalksStructuredResults(t *testing.T) {
	e := New()
	type record struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	res, replaced, err := e.AfterTool(context.Background(), agent.ToolResultInfo{
		Name: "lookup",
		Result: map[string]interface{}{
			"customer": record{Name: "Bob", Email: "bob@corp.io"},
			"notes":    []interface{}{"phone +44 20 7946 0958", 42},
		},
	})
	if err != nil || !replaced {
		t.Fatalf("replaced=%v err=%v", replaced, err)
	}
	m := res.(map[string]interface{})
	cust := m["customer"].(map[string]interface{})
	if cust["email"] != "b***@corp.io" || cust["name"] != "Bob" {
		t.Fatalf("customer = %v", cust)
	}
	notes := m["notes"].([]interface{})
	if notes[0] != "phone ***0958" || notes[1] != 42 {
		t.Fatalf("notes = %v", notes)
	}

	// Nothing to mask: the original value comes back untouched, unreplaced.
	_, replaced, _ = e.AfterTool(context.Background(), agent.ToolResultInfo{Result: map[string]interface{}{"ok": true}})
	if replaced {
		t.Fatal("clean result reported as replaced")
	}
}

func TestLintRejectsAnswerThatLeaks(t *testing.T) {
	e := New()
	ok, reason := e.Check("Contact her at alice@example.com", agent.LintContext{})
	if ok || !strings.Contains(reason, "email address") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
	ok, _ = e.Check("Contact her at a***@example.com", agent.LintContext{})
	if !ok {
		t.Fatal("masked answer was rejected")
	}
	ok, _ = New(WithFinalAnswerLint(false)).Check("alice@example.com", agent.LintContext{})
	if !ok {
		t.Fatal("lint ran while disabled")
	}
}

func TestBlockedToolRefusesArgumentsWithPII(t *testing.T) {
	e := New(WithBlockedTools("web_search"))
	v, err := e.BeforeTool(context.Background(), agent.ToolCallInfo{
		Name: "web_search", Args: map[string]interface{}{"query": "who is alice@example.com"},
	})
	if err != nil || v.Block == "" {
		t.Fatalf("block=%q err=%v", v.Block, err)
	}
	v, _ = e.BeforeTool(context.Background(), agent.ToolCallInfo{
		Name: "fs_read", Args: map[string]interface{}{"path": "alice@example.com"},
	})
	if v.Block != "" {
		t.Fatal("a tool not on the list was blocked")
	}
}

func TestKindsCanBeNarrowed(t *testing.T) {
	e := New(WithKinds(Email))
	out, kinds := e.Redact("alice@example.com +1 415 555 0142")
	if len(kinds) != 1 || kinds[0] != Email || !strings.Contains(out, "415 555 0142") {
		t.Fatalf("out=%q kinds=%v", out, kinds)
	}
}
