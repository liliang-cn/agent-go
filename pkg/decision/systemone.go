package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Defaults for Ollama on the machine running the agent.
const (
	SystemOneDefaultURL = "http://127.0.0.1:11434"
	// SystemOneDefaultModel is Together AI's 4B decision model. Measured on
	// decisions taken from real agent code — tool-call gating, routing, RAG
	// relevance, "is this worth remembering" — it was right 84–88% of the time
	// at about a second through a relay and 0.4s locally. clef-flash:9b was a
	// few points better and two to three times slower; laya, at 61%, was not
	// usable for gating.
	SystemOneDefaultModel = "tev1:4b"
	// SystemOneDefaultTimeout leaves room for a relay hop, which a local
	// laya-serve never had to. The gates in pkg/agent bound their own calls
	// more tightly than this regardless.
	SystemOneDefaultTimeout = 5 * time.Second
)

// SystemOne is an Engine backed by Ollama's System One API (POST
// /v1/systemone), which serves the decision models Ollama ships from 0.35 on:
// tev1, nimble, clef and clef-flash. Anything that forwards that path — a
// token2money relay, for one — works the same way.
type SystemOne struct {
	url       string
	model     string
	keepAlive *time.Duration
	client    *http.Client
	header    http.Header
}

// SystemOneOption configures a SystemOne.
type SystemOneOption func(*SystemOne)

// WithSystemOneURL points at an Ollama, or a relay in front of one, other
// than the local default. A trailing /v1 is accepted and ignored, so the base
// URL an OpenAI-compatible client already uses can be passed as is.
func WithSystemOneURL(url string) SystemOneOption {
	return func(s *SystemOne) {
		u := strings.TrimRight(strings.TrimSpace(url), "/")
		u = strings.TrimSuffix(u, "/v1")
		if u != "" {
			s.url = u
		}
	}
}

// WithSystemOneModel selects the decision model.
func WithSystemOneModel(model string) SystemOneOption {
	return func(s *SystemOne) {
		if m := strings.TrimSpace(model); m != "" {
			s.model = m
		}
	}
}

// WithSystemOneTimeout bounds one request.
func WithSystemOneTimeout(d time.Duration) SystemOneOption {
	return func(s *SystemOne) {
		if d > 0 {
			s.client.Timeout = d
		}
	}
}

// WithSystemOneHTTPClient supplies the client, for a caller that needs its own
// transport, proxy or TLS. Its Timeout is used as given.
func WithSystemOneHTTPClient(c *http.Client) SystemOneOption {
	return func(s *SystemOne) {
		if c != nil {
			s.client = c
		}
	}
}

// WithSystemOneHeader adds a header to every request.
func WithSystemOneHeader(key, value string) SystemOneOption {
	return func(s *SystemOne) {
		if s.header == nil {
			s.header = http.Header{}
		}
		s.header.Set(key, value)
	}
}

// WithSystemOneAPIKey sends a bearer token, for a relay that wants one. A
// local Ollama ignores it.
func WithSystemOneAPIKey(key string) SystemOneOption {
	if k := strings.TrimSpace(key); k != "" {
		return WithSystemOneHeader("Authorization", "Bearer "+k)
	}
	return func(*SystemOne) {}
}

// WithSystemOneKeepAlive asks Ollama to keep the model loaded this long after
// each request; negative keeps it loaded. Unset, the server's default applies,
// which is five minutes — and a gate whose model was unloaded pays the load on
// its next call, which is far past any gate's timeout, so it is skipped then.
func WithSystemOneKeepAlive(d time.Duration) SystemOneOption {
	return func(s *SystemOne) { s.keepAlive = &d }
}

// NewSystemOne builds a client. With no options it asks tev1:4b on the local
// Ollama.
func NewSystemOne(opts ...SystemOneOption) *SystemOne {
	s := &SystemOne{
		url:    SystemOneDefaultURL,
		model:  SystemOneDefaultModel,
		client: &http.Client{Timeout: SystemOneDefaultTimeout},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Name reports the engine and model, so a log line says which answered.
func (s *SystemOne) Name() string { return "systemone:" + s.model }

// Ready reports whether the server lists the model. A reachable server
// without it is not ready: every Decide would fail. Like Laya.Ready, it is for
// a startup probe and is never called by Decide.
func (s *SystemOne) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/v1/models", nil)
	if err != nil {
		return err
	}
	s.applyHeader(req)
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("decision: systemone models %s: %s", res.Status, errorText(res.Body))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		return fmt.Errorf("decision: systemone models: %w", err)
	}
	for _, m := range list.Data {
		if m.ID == s.model {
			return nil
		}
	}
	return fmt.Errorf("decision: %s does not serve %s", s.url, s.model)
}

type systemOneQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	// Criteria is an ordered object for choice, a list for score, an optional
	// {"true","false"} object for noul.
	Criteria json.RawMessage `json:"criteria,omitempty"`
}

type systemOneRequest struct {
	Model     string                       `json:"model"`
	State     string                       `json:"state"`
	Questions map[string]systemOneQuestion `json:"questions"`
	KeepAlive string                       `json:"keep_alive,omitempty"`
}

type systemOneResponse struct {
	Answers map[string]wireAnswer `json:"answers"`
}

// Decide sends every question in one request. The text goes as the state,
// which System One reads as one string.
func (s *SystemOne) Decide(ctx context.Context, text string, questions map[string]Question) (map[string]Answer, error) {
	if err := ValidateQuestions(questions); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("decision: empty text")
	}

	payload := systemOneRequest{Model: s.model, State: text, Questions: make(map[string]systemOneQuestion, len(questions))}
	if s.keepAlive != nil {
		payload.KeepAlive = s.keepAlive.String()
	}
	for name, q := range questions {
		crit, err := systemOneCriteria(q)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		payload.Questions[name] = systemOneQuestion{Type: string(q.Type), Instructions: q.Instructions, Criteria: crit}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.applyHeader(req)

	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// Ollama validates hard (2–26 options, 64 KiB, a context it will not
		// truncate) and says exactly what it refused; a bare status would
		// leave the caller guessing which.
		return nil, fmt.Errorf("decision: systemone %s: %s", res.Status, errorText(res.Body))
	}

	var parsed systemOneResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decision: systemone reply: %w", err)
	}
	out := make(map[string]Answer, len(questions))
	for name := range questions {
		raw, ok := parsed.Answers[name]
		if !ok {
			return nil, fmt.Errorf("decision: systemone did not answer %q", name)
		}
		out[name] = raw.toAnswer()
	}
	return out, nil
}

// systemOneCriteria renders a question's criteria in System One's shape.
func systemOneCriteria(q Question) (json.RawMessage, error) {
	switch q.Type {
	case TypeScore:
		return json.Marshal(q.Criteria)
	case TypeNoul:
		if len(q.Descriptions) == 0 {
			return nil, nil
		}
		// Ollama's keys are "true" and "false"; an undescribed side is left
		// out and takes the model's own default.
		side := map[string]string{}
		if d := strings.TrimSpace(q.Descriptions["yes"]); d != "" {
			side["true"] = d
		}
		if d := strings.TrimSpace(q.Descriptions["no"]); d != "" {
			side["false"] = d
		}
		if len(side) == 0 {
			return nil, nil
		}
		return json.Marshal(side)
	default:
		// An object, written by hand because the order is the option order
		// and a Go map would sort it; Ollama breaks ties by that order. An
		// undescribed option is null, which Ollama reads as "the label
		// describes itself".
		var buf bytes.Buffer
		buf.WriteByte('{')
		for i, opt := range q.Criteria {
			if i > 0 {
				buf.WriteByte(',')
			}
			k, err := json.Marshal(opt)
			if err != nil {
				return nil, err
			}
			buf.Write(k)
			buf.WriteByte(':')
			if d := strings.TrimSpace(q.Descriptions[opt]); d != "" {
				v, err := json.Marshal(d)
				if err != nil {
					return nil, err
				}
				buf.Write(v)
			} else {
				buf.WriteString("null")
			}
		}
		buf.WriteByte('}')
		return buf.Bytes(), nil
	}
}

func (s *SystemOne) applyHeader(req *http.Request) {
	for k, vs := range s.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
}

// errorText pulls the message out of an error body. Ollama sends
// {"error":"..."}, an OpenAI-style relay {"error":{"message":"..."}}; anything
// else is returned as text, cut short.
func errorText(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 4096))
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && len(e.Error) > 0 {
		var msg string
		if json.Unmarshal(e.Error, &msg) == nil {
			return msg
		}
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
