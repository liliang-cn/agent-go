package decision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A reply recorded from Ollama 0.35.1 running tev1:4b. noul answers carry no
// confidence at all, unlike laya's, which is the difference that matters.
const systemOneReply = `{"model":"tev1:4b","answers":{
  "c":{"type":"choice","choice":"exchange","confidence":0.96,
       "probabilities":{"exchange":0.992,"refund":0.004,"repair":0.004}},
  "n":{"type":"noul","noul":0.0213},
  "s":{"type":"score","score":1.9641,"confidence":0.86,
       "legend":{"0":"routine","1":"soon","2":"urgent"},
       "probabilities":{"0":0.002,"1":0.032,"2":0.966}}},
  "usage":{"input_tokens":1203,"output_tokens":5}}`

func systemOneServer(t *testing.T, reply string, seen *[]*http.Request, bodies *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if seen != nil {
			*seen = append(*seen, r)
		}
		if bodies != nil {
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			*bodies = append(*bodies, b)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSystemOneParsesEachAnswerShape(t *testing.T) {
	srv := systemOneServer(t, systemOneReply, nil, nil)
	ans, err := NewSystemOne(WithSystemOneURL(srv.URL)).Decide(context.Background(), "text", map[string]Question{
		"c": Choice("what?", "exchange", "refund", "repair"),
		"n": Noul("yes or no?"),
		"s": Score("how urgent?", "routine", "soon", "urgent"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := ans["c"]; c.Label != "exchange" || c.Confidence != 0.96 {
		t.Errorf("choice = %q conf %v, want exchange / 0.96", c.Label, c.Confidence)
	}
	if s := ans["s"]; s.Label != "urgent" || s.Score != 1.9641 || s.Confidence != 0.86 {
		t.Errorf("score = %q %v conf %v, want urgent 1.9641 / 0.86", s.Label, s.Score, s.Confidence)
	}

	// With no confidence on the wire, a noul's is how far its probability sits
	// from a coin toss. Left at zero, every noul would fall under any floor and
	// a gate built on one would never fire.
	n := ans["n"]
	if n.Yes || n.Label != "no" {
		t.Errorf("noul = %v/%q, want no", n.Yes, n.Label)
	}
	if want := 1 - 0.0213; n.Confidence < want-1e-9 || n.Confidence > want+1e-9 {
		t.Errorf("noul confidence = %v, want %v", n.Confidence, want)
	}
}

func TestSystemOneSendsOllamasRequestShape(t *testing.T) {
	var seen []*http.Request
	var bodies []map[string]any
	srv := systemOneServer(t, systemOneReply, &seen, &bodies)

	engine := NewSystemOne(
		WithSystemOneURL(srv.URL+"/v1/"), // a base that already ends in /v1 must not become /v1/v1
		WithSystemOneModel("clef-flash:9b"),
		WithSystemOneAPIKey("t2m-key"),
		WithSystemOneKeepAlive(30*time.Minute),
	)
	_, err := engine.Decide(context.Background(), "the ticket", map[string]Question{
		"c": Choice("what?", "exchange", "refund", "repair").Describe(map[string]string{
			"exchange": "Wants a replacement", "refund": "Wants money back",
		}),
		"n": Noul("is it a defect?").Describe(map[string]string{"yes": "Broken on arrival"}),
		"s": Score("how urgent?", "routine", "soon", "urgent"),
	})
	if err != nil {
		t.Fatal(err)
	}

	r := seen[0]
	if r.URL.Path != "/v1/systemone" {
		t.Errorf("path = %s, want /v1/systemone", r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer t2m-key" {
		t.Errorf("Authorization = %q", got)
	}

	b := bodies[0]
	if b["model"] != "clef-flash:9b" || b["state"] != "the ticket" || b["keep_alive"] != "30m0s" {
		t.Errorf("model/state/keep_alive = %v / %v / %v", b["model"], b["state"], b["keep_alive"])
	}
	qs := b["questions"].(map[string]any)

	// choice criteria are an object: a described option keeps its words, an
	// undescribed one goes as null, which Ollama reads as "the label says it".
	crit := qs["c"].(map[string]any)["criteria"].(map[string]any)
	if crit["exchange"] != "Wants a replacement" || crit["refund"] != "Wants money back" {
		t.Errorf("choice criteria = %v", crit)
	}
	if v, ok := crit["repair"]; !ok || v != nil {
		t.Errorf("undescribed option = %v (present %v), want null", v, ok)
	}

	// noul descriptions use Ollama's own keys.
	ncrit := qs["n"].(map[string]any)["criteria"].(map[string]any)
	if ncrit["true"] != "Broken on arrival" {
		t.Errorf("noul criteria = %v, want true: Broken on arrival", ncrit)
	}
	if _, ok := ncrit["false"]; ok {
		t.Errorf("an undescribed side is left to the model's default, got %v", ncrit)
	}

	// score criteria stay an ordered list.
	if levels := qs["s"].(map[string]any)["criteria"].([]any); len(levels) != 3 || levels[0] != "routine" {
		t.Errorf("score criteria = %v", levels)
	}
}

func TestSystemOneOmitsCriteriaForABareNoul(t *testing.T) {
	var bodies []map[string]any
	srv := systemOneServer(t, `{"answers":{"n":{"type":"noul","noul":0.9}}}`, nil, &bodies)
	if _, err := NewSystemOne(WithSystemOneURL(srv.URL)).Decide(context.Background(), "x",
		map[string]Question{"n": Noul("yes?")}); err != nil {
		t.Fatal(err)
	}
	q := bodies[0]["questions"].(map[string]any)["n"].(map[string]any)
	if _, ok := q["criteria"]; ok {
		t.Errorf("bare noul sent criteria %v", q["criteria"])
	}
	if _, ok := bodies[0]["keep_alive"]; ok {
		t.Error("keep_alive sent without being asked for")
	}
}

func TestSystemOneReportsTheServersError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"question \"q\": criteria must contain 2–26 candidates"}`))
	}))
	defer srv.Close()
	_, err := NewSystemOne(WithSystemOneURL(srv.URL)).Decide(context.Background(), "x",
		map[string]Question{"q": Choice("?", "a", "b")})
	// The status alone says nothing a caller can fix; Ollama's message does.
	if err == nil || !strings.Contains(err.Error(), "2–26 candidates") {
		t.Fatalf("err = %v, want the server's message", err)
	}
}

func TestSystemOneRejectsAMissingAnswer(t *testing.T) {
	srv := systemOneServer(t, `{"answers":{}}`, nil, nil)
	_, err := NewSystemOne(WithSystemOneURL(srv.URL)).Decide(context.Background(), "x",
		map[string]Question{"q": Noul("?")})
	if err == nil || !strings.Contains(err.Error(), `"q"`) {
		t.Fatalf("err = %v, want it to name the unanswered question", err)
	}
}

func TestSystemOneReadyChecksTheModelList(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"tev1:4b"}]}`))
	}))
	defer srv.Close()

	if err := NewSystemOne(WithSystemOneURL(srv.URL)).Ready(context.Background()); err != nil {
		t.Fatalf("ready with the model listed: %v", err)
	}
	if paths[0] != "/v1/models" {
		t.Errorf("Ready asked %s", paths[0])
	}
	// A reachable server without the model is not ready: every Decide would fail.
	err := NewSystemOne(WithSystemOneURL(srv.URL), WithSystemOneModel("clef:27b")).Ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "clef:27b") {
		t.Fatalf("err = %v, want it to name the missing model", err)
	}
}

func TestSystemOneDefaults(t *testing.T) {
	e := NewSystemOne()
	if e.Name() != "systemone:"+SystemOneDefaultModel || e.url != SystemOneDefaultURL {
		t.Errorf("defaults = %s at %s", e.Name(), e.url)
	}
}

func TestDescribeRejectsAnUnknownCriterion(t *testing.T) {
	q := Choice("?", "a", "b").Describe(map[string]string{"c": "not an option"})
	if err := q.Validate(); err == nil {
		t.Error("a description for an option that does not exist should not validate")
	}
	n := Noul("?").Describe(map[string]string{"maybe": "x"})
	if err := n.Validate(); err == nil {
		t.Error("a noul describes only yes and no")
	}
	if err := Score("?", "lo", "hi").Describe(map[string]string{"lo": "x"}).Validate(); err == nil {
		t.Error("score levels are already descriptions; describing them again should not validate")
	}
}

// laya takes labels only, so descriptions travel inside the instructions
// rather than being dropped: they are what makes the question answerable.
func TestLayaFoldsDescriptionsIntoInstructions(t *testing.T) {
	var bodies []map[string]any
	srv := systemOneServer(t, `{"answers":{"c":{"type":"choice","choice":"a","confidence":0.9}}}`, nil, &bodies)
	_, err := NewLaya(WithLayaURL(srv.URL)).Decide(context.Background(), "x", map[string]Question{
		"c": Choice("Which?", "a", "b").Describe(map[string]string{"a": "first one"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	q := bodies[0]["questions"].(map[string]any)["c"].(map[string]any)
	if got := q["instructions"].(string); !strings.Contains(got, "Which?") || !strings.Contains(got, "a: first one") {
		t.Errorf("instructions = %q", got)
	}
	if crit := q["criteria"].([]any); len(crit) != 2 {
		t.Errorf("criteria = %v, want the two labels", crit)
	}
}
