package mcp

import "testing"

// A host gating on "may this agent call it" must be able to tell "the server
// said read-only" from "the server said nothing". Collapsing the two is how an
// unannotated tool gets silently misclassified.
func TestIsReadOnlyDistinguishesUndeclaredFromFalse(t *testing.T) {
	yes, no := true, false

	if ro, declared := (&ToolAnnotations{ReadOnlyHint: &yes}).IsReadOnly(); !ro || !declared {
		t.Errorf("declared read-only: got (%v,%v), want (true,true)", ro, declared)
	}
	if ro, declared := (&ToolAnnotations{ReadOnlyHint: &no}).IsReadOnly(); ro || !declared {
		t.Errorf("declared not-read-only: got (%v,%v), want (false,true)", ro, declared)
	}
	if ro, declared := (&ToolAnnotations{}).IsReadOnly(); ro || declared {
		t.Errorf("no hint: got (%v,%v), want (false,false)", ro, declared)
	}
	if ro, declared := (*ToolAnnotations)(nil).IsReadOnly(); ro || declared {
		t.Errorf("nil annotations: got (%v,%v), want (false,false)", ro, declared)
	}
}

// The built-in memory tools must classify themselves, so a read-only host gets
// search/get/list and none of the mutations.
func TestBuiltInMemoryToolsDeclareTheirBehaviour(t *testing.T) {
	reads := []MCPTool{&MemorySearchTool{}, &MemoryGetTool{}, &MemoryListTool{}}
	for _, tool := range reads {
		ro, declared := tool.Annotations().IsReadOnly()
		if !ro || !declared {
			t.Errorf("%T: got (%v,%v), want read-only declared", tool, ro, declared)
		}
	}

	writes := []MCPTool{&MemoryAddTool{}, &MemoryUpdateTool{}, &MemoryDeleteTool{}}
	for _, tool := range writes {
		ro, declared := tool.Annotations().IsReadOnly()
		if ro || !declared {
			t.Errorf("%T: got (%v,%v), want not-read-only declared", tool, ro, declared)
		}
	}
}

// trueOrNil collapses false to "undeclared" because the wire format omits a
// false hint, making it indistinguishable from an absent one.
func TestTrueOrNil(t *testing.T) {
	if got := trueOrNil(true); got == nil || !*got {
		t.Errorf("trueOrNil(true) = %v, want pointer to true", got)
	}
	if got := trueOrNil(false); got != nil {
		t.Errorf("trueOrNil(false) = %v, want nil", got)
	}
}
