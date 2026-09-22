package decision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLayaParsesEachAnswerShape(t *testing.T) {
	// Recorded from a live laya-serve. The three types report differently
	// enough that guessing the shape is how this breaks silently.
	const body = `{"object":"decision","model":"m","answers":{
      "c":{"type":"choice","confidence":0.3575,"choice":"nothing",
           "probabilities":{"email":0.0049,"file":0.4119,"nothing":0.5832}},
      "n":{"type":"noul","confidence":0.9916,"noul":0.0084},
      "s":{"type":"score","confidence":0.2018,"score":1.3385,
           "legend":{"0":"low","1":"medium","2":"high"},
           "probabilities":{"0":0.0638,"1":0.534,"2":0.4023}}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	ans, err := NewLaya(WithLayaURL(srv.URL)).Decide(context.Background(), "text", map[string]Question{
		"c": Choice("what?", "email", "file", "nothing"),
		"n": Noul("yes or no?"),
		"s": Score("how much?", "low", "medium", "high"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Confidence is the engine's own number, not the winning probability.
	// Here they differ by a lot, which is the whole reason it is not
	// recomputed: 0.58 would overstate how sure this answer is.
	if c := ans["c"]; c.Label != "nothing" || c.Confidence != 0.3575 {
		t.Errorf("choice = %q conf %v, want nothing / 0.3575", c.Label, c.Confidence)
	}
	if p := ans["c"].Probabilities["file"]; p != 0.4119 {
		t.Errorf("choice runner-up probability = %v, want 0.4119", p)
	}

	// noul carries the probability of yes; 0.0084 is a confident no.
	n := ans["n"]
	if n.Yes || n.Label != "no" || n.Confidence != 0.9916 {
		t.Errorf("noul = %v/%q conf %v, want no / 0.9916", n.Yes, n.Label, n.Confidence)
	}
	if n.Probabilities != nil {
		t.Error("noul carries no distribution")
	}

	// A score's probabilities are keyed by index; the label comes from the
	// legend, and the continuous score survives.
	s := ans["s"]
	if s.Label != "medium" || s.Score != 1.3385 {
		t.Errorf("score = %q %v, want medium / 1.3385", s.Label, s.Score)
	}
}

func TestLayaSurfacesAMissingAnswer(t *testing.T) {
	// A reply that answers some questions is not a partial success: a caller
	// reading a zero-valued Answer would act on a decision nobody made.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"a":{"type":"noul","confidence":1,"noul":1}}}`))
	}))
	defer srv.Close()

	_, err := NewLaya(WithLayaURL(srv.URL)).Decide(context.Background(), "text", map[string]Question{
		"a": Noul("one?"),
		"b": Noul("two?"),
	})
	if err == nil {
		t.Fatal("a missing answer was accepted")
	}
}

func TestLayaSendsOneRequestForEveryQuestion(t *testing.T) {
	// The engine runs a single forward pass however many questions it gets,
	// so asking them one at a time gives up the speed it exists for.
	var requests int
	var sent layaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"answers":{
          "a":{"type":"noul","confidence":1,"noul":1},
          "b":{"type":"noul","confidence":1,"noul":0}}}`))
	}))
	defer srv.Close()

	if _, err := NewLaya(WithLayaURL(srv.URL)).Decide(context.Background(), "text", map[string]Question{
		"a": Noul("one?"), "b": Noul("two?"),
	}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Errorf("made %d requests, want 1", requests)
	}
	if len(sent.Questions) != 2 {
		t.Errorf("sent %d questions, want 2", len(sent.Questions))
	}
	if sent.Model == "" {
		t.Error("model is required by the endpoint and was not sent")
	}
}

func TestLayaReportsTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := NewLaya(WithLayaURL(srv.URL)).Decide(context.Background(), "text",
		map[string]Question{"a": Noul("one?")})
	if err == nil {
		t.Fatal("a 503 was read as an answer")
	}
}

func TestLayaHonoursTheDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte(`{"answers":{"a":{"type":"noul","confidence":1,"noul":1}}}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := NewLaya(WithLayaURL(srv.URL)).Decide(ctx, "text",
		map[string]Question{"a": Noul("one?")}); err == nil {
		t.Fatal("a slow engine outlived its caller's deadline")
	}
}

func TestQuestionsAreValidatedBeforeTheyAreSent(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))
	defer srv.Close()
	engine := NewLaya(WithLayaURL(srv.URL))

	for name, qs := range map[string]map[string]Question{
		"no questions":           {},
		"choice with no options": {"a": {Type: TypeChoice, Instructions: "pick"}},
		"unknown type":           {"a": {Type: "vibes", Instructions: "hmm"}},
		"no instructions":        {"a": {Type: TypeNoul}},
	} {
		if _, err := engine.Decide(context.Background(), "text", qs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if reached {
		t.Error("an invalid question was sent to the engine")
	}
}
