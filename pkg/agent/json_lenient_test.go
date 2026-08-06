package agent

import "testing"

// Providers that advertise structured output do not reliably deliver it: the
// same gateway returns bare JSON on one call and a fenced block or a prose
// preamble on the next. A strict parse turns that into a silently unenforced
// constraint, so the parser has to cope with all of it.
func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"bare", `{"forbid_tools":true,"deliverables":[]}`, `{"forbid_tools":true,"deliverables":[]}`},
		{
			"fenced_json",
			"```json\n{\"forbid_tools\":true,\"deliverables\":[]}\n```",
			`{"forbid_tools":true,"deliverables":[]}`,
		},
		{
			"fenced_bare",
			"```\n{\"forbid_tools\":false,\"deliverables\":[]}\n```",
			`{"forbid_tools":false,"deliverables":[]}`,
		},
		{
			// The exact shape the benchmark hit: "Based on the user request..."
			// is why the strict parse failed with `invalid character 'B'`.
			"prose_preamble",
			"Based on the user request, here are the constraints:\n{\"forbid_tools\":true,\"deliverables\":[]}\nLet me know if you need more.",
			`{"forbid_tools":true,"deliverables":[]}`,
		},
		{
			// Braces inside a string must not end the scan early — a
			// LastIndex("}") scan would have returned the wrong slice here.
			"braces_in_string",
			`{"deliverables":[{"kind":"file","description":"write {a} and {b}","path":"/tmp/x.txt"}],"forbid_tools":false}`,
			`{"deliverables":[{"kind":"file","description":"write {a} and {b}","path":"/tmp/x.txt"}],"forbid_tools":false}`,
		},
		{
			"escaped_quote_in_string",
			`{"deliverables":[{"kind":"other","description":"say \"hi\""}],"forbid_tools":false}`,
			`{"deliverables":[{"kind":"other","description":"say \"hi\""}],"forbid_tools":false}`,
		},
		{
			"trailing_prose_after_object",
			`{"forbid_tools":false,"deliverables":[]} — that is all I could find.`,
			`{"forbid_tools":false,"deliverables":[]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractJSONObject(tc.raw)
			if err != nil {
				t.Fatalf("extractJSONObject(%q) error: %v", tc.raw, err)
			}
			if string(got) != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestExtractJSONObjectRejectsNonJSON(t *testing.T) {
	for _, raw := range []string{
		"",
		"I cannot determine any constraints from this request.",
		"{not valid json at all",
	} {
		if _, err := extractJSONObject(raw); err == nil {
			t.Errorf("expected %q to be rejected", raw)
		}
	}
}

func TestParseRunConstraints(t *testing.T) {
	got, err := parseRunConstraints(
		"```json\n{\"forbid_tools\":true,\"deliverables\":[{\"kind\":\"EMAIL\",\"description\":\"send the report\"}]}\n```")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.ForbidTools {
		t.Error("forbid_tools lost in parsing")
	}
	if len(got.Deliverables) != 1 || got.Deliverables[0].Kind != "email" {
		t.Fatalf("deliverable kind not normalised: %+v", got.Deliverables)
	}

	// An unknown kind is kept rather than dropped — losing a side effect the
	// user asked for is worse than checking it generically.
	got, err = parseRunConstraints(`{"forbid_tools":false,"deliverables":[{"kind":"carrier_pigeon","description":"x"}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Deliverables) != 1 || got.Deliverables[0].Kind != "other" {
		t.Fatalf("unknown kind should degrade to other: %+v", got.Deliverables)
	}

	// An ordinary request must come back with nothing to enforce.
	got, err = parseRunConstraints(`{"forbid_tools":false,"deliverables":[]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.Empty() {
		t.Fatalf("expected no constraints, got %+v", got)
	}
}

func TestNoToolScaffoldingAnswer(t *testing.T) {
	lint := NoToolScaffoldingAnswer()

	// home-order-pizza finished with exactly this, and the user saw it.
	if ok, reason := lint.Check(toolSearchNoMatches, LintContext{}); ok {
		t.Fatal("expected the tool-search miss message to be rejected as a final answer")
	} else if reason == "" {
		t.Error("expected a reason explaining what to do instead")
	}

	if ok, _ := lint.Check(toolSearchSummaryRequest, LintContext{}); ok {
		t.Error("expected the summary-request scaffolding to be rejected")
	}

	// Quoting the scaffolding inside a real answer is fine.
	real := "I could not order a pizza: my tool search came up empty (\"" + toolSearchNoMatches +
		"\"), so there is no delivery integration available. You will need to order directly."
	if ok, reason := lint.Check(real, LintContext{}); !ok {
		t.Errorf("a real explanation that quotes the scaffolding should pass, got %q", reason)
	}

	// Empty is non_empty_final_answer's job, not this lint's.
	if ok, _ := lint.Check("", LintContext{}); !ok {
		t.Error("empty text belongs to non_empty_final_answer")
	}
}
