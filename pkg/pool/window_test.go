package pool

import "testing"

func TestLookupModelWindowLongestPatternWins(t *testing.T) {
	w, ok := LookupModelWindow("gpt-4o-mini-2024-07-18")
	if !ok || w.ContextTokens != 128000 {
		t.Fatalf("gpt-4o-mini = %+v %v, want the gpt-4o window, not gpt-4's", w, ok)
	}
	if w, ok := LookupModelWindow("gpt-4-0613"); !ok || w.ContextTokens != 8192 {
		t.Fatalf("gpt-4 = %+v %v", w, ok)
	}
}

func TestLookupModelWindowUnknownIsReported(t *testing.T) {
	if _, ok := LookupModelWindow("gemini-3.8-flash-high"); ok {
		t.Fatal("a gateway alias nobody registered must be unknown, not guessed")
	}
	if _, ok := LookupModelWindow(""); ok {
		t.Fatal("an empty model name must be unknown")
	}
}

func TestRegisterModelWindowWinsOverBundledTable(t *testing.T) {
	RegisterModelWindow("gpt-4o", ModelWindow{ContextTokens: 32000})
	defer UnregisterModelWindow("gpt-4o")
	if w, _ := LookupModelWindow("gpt-4o"); w.ContextTokens != 32000 {
		t.Fatalf("registration did not win: %+v", w)
	}

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
