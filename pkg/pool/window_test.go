package pool

import "testing"

func TestLookupModelWindowLongestPatternWins(t *testing.T) {
	RegisterModelWindow("gpt-4", ModelWindow{ContextTokens: 8192})
	RegisterModelWindow("gpt-4o", ModelWindow{ContextTokens: 128000})
	defer UnregisterModelWindow("gpt-4")
	defer UnregisterModelWindow("gpt-4o")
	w, ok := LookupModelWindow("gpt-4o-mini-2024-07-18")
	if !ok || w.ContextTokens != 128000 {
		t.Fatalf("gpt-4o-mini = %+v %v, want the gpt-4o window, not gpt-4's", w, ok)
	}
	if w, ok := LookupModelWindow("gpt-4-0613"); !ok || w.ContextTokens != 8192 {
		t.Fatalf("gpt-4 = %+v %v", w, ok)
	}
}

func TestLookupModelWindowUnknownIsReported(t *testing.T) {
	for _, m := range []string{"gemini-3.8-flash-high", "gpt-4o", "claude-3-opus", "deepseek-chat", ""} {
		if _, ok := LookupModelWindow(m); ok {
			t.Errorf("%q has a window with nothing registered", m)
		}
	}
}

func TestARegisteredWindowIsFoundAndNonsenseIsNot(t *testing.T) {
	RegisterModelWindow("gemini-3.8", ModelWindow{ContextTokens: 1048576, MaxOutputTokens: 65536})
	defer UnregisterModelWindow("gemini-3.8")
	if w, ok := LookupModelWindow("gemini-3.8-flash-high"); !ok || w.ContextTokens != 1048576 {
		t.Fatalf("registered alias = %+v %v", w, ok)
	}

	// Nonsense is ignored rather than stored.
	RegisterModelWindow("", ModelWindow{ContextTokens: 1})
	RegisterModelWindow("zero-window", ModelWindow{})
	if _, ok := LookupModelWindow("zero-window-model"); ok {
		t.Fatal("a non-positive window must not register")
	}
}
