package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Defaults for a laya-serve on the machine running the agent. They are
// deliberately concrete: a decision engine is only worth configuring if it is
// fast, and the fast case is a local one.
const (
	LayaDefaultURL = "http://127.0.0.1:43711"
	// LayaDefaultModel is the multilingual checkpoint. Measured against this
	// framework's own constraint questions it was both quicker and markedly
	// better outside English, and a gate that only works in English is the one
	// thing this repository refuses to ship.
	LayaDefaultModel = "aac6fef/laya-multilingual-mlx"
	// LayaDefaultTimeout is generous next to a local answer in tens of
	// milliseconds, and still far under the language-model call this is meant
	// to avoid. A caller that hits it should degrade, not wait.
	LayaDefaultTimeout = 2 * time.Second
)

// Laya is an Engine backed by a laya-serve instance.
type Laya struct {
	url    string
	model  string
	client *http.Client
	header http.Header
}

// LayaOption configures a Laya.
type LayaOption func(*Laya)

// WithLayaURL points at a laya-serve other than the local default.
func WithLayaURL(url string) LayaOption {
	return func(l *Laya) {
		if u := strings.TrimRight(strings.TrimSpace(url), "/"); u != "" {
			l.url = u
		}
	}
}

// WithLayaModel selects a checkpoint.
func WithLayaModel(model string) LayaOption {
	return func(l *Laya) {
		if m := strings.TrimSpace(model); m != "" {
			l.model = m
		}
	}
}

// WithLayaTimeout bounds one request.
func WithLayaTimeout(d time.Duration) LayaOption {
	return func(l *Laya) {
		if d > 0 {
			l.client.Timeout = d
		}
	}
}

// WithLayaHTTPClient supplies the client, for a caller that needs its own
// transport, proxy or TLS. Its Timeout is used as given.
func WithLayaHTTPClient(c *http.Client) LayaOption {
	return func(l *Laya) {
		if c != nil {
			l.client = c
		}
	}
}

// WithLayaHeader adds a header to every request, for an instance behind a
// gateway that wants a token.
func WithLayaHeader(key, value string) LayaOption {
	return func(l *Laya) {
		if l.header == nil {
			l.header = http.Header{}
		}
		l.header.Set(key, value)
	}
}

// NewLaya builds a client. With no options it talks to a laya-serve on this
// machine using the multilingual checkpoint.
func NewLaya(opts ...LayaOption) *Laya {
	l := &Laya{
		url:    LayaDefaultURL,
		model:  LayaDefaultModel,
		client: &http.Client{Timeout: LayaDefaultTimeout},
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Name reports the engine and checkpoint, so a log line says which answered.
func (l *Laya) Name() string { return "laya:" + l.model }

// Ready reports whether the instance answers, for a Doctor check or a startup
// probe. It is not called by Decide: an engine that has gone away must surface
// as a failed decision the caller degrades from, not as a second round trip on
// every request.
func (l *Laya) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url+"/healthz", nil)
	if err != nil {
		return err
	}
	l.applyHeader(req)
	res, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("decision: laya health %s", res.Status)
	}
	return nil
}

type layaQuestion struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria,omitempty"`
}

type layaRequest struct {
	Model     string                  `json:"model"`
	Text      string                  `json:"text"`
	Questions map[string]layaQuestion `json:"questions"`
}

type layaAnswer struct {
	Type          string             `json:"type"`
	Confidence    float64            `json:"confidence"`
	Choice        string             `json:"choice"`
	Noul          *float64           `json:"noul"`
	Score         *float64           `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type layaResponse struct {
	Model   string                `json:"model"`
	Answers map[string]layaAnswer `json:"answers"`
}

// Decide sends every question in one request, which is the whole point of the
// engine: it runs a single forward pass whatever the number of questions.
func (l *Laya) Decide(ctx context.Context, text string, questions map[string]Question) (map[string]Answer, error) {
	if err := ValidateQuestions(questions); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("decision: empty text")
	}

	payload := layaRequest{Model: l.model, Text: text, Questions: make(map[string]layaQuestion, len(questions))}
	for name, q := range questions {
		payload.Questions[name] = layaQuestion{
			Type:         string(q.Type),
			Instructions: q.Instructions,
			Criteria:     q.Criteria,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url+"/v1/decisions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	l.applyHeader(req)

	res, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("decision: laya %s", res.Status)
	}

	var parsed layaResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decision: laya reply: %w", err)
	}

	out := make(map[string]Answer, len(questions))
	for name := range questions {
		raw, ok := parsed.Answers[name]
		if !ok {
			return nil, fmt.Errorf("decision: laya did not answer %q", name)
		}
		out[name] = raw.toAnswer()
	}
	return out, nil
}

func (l *Laya) applyHeader(req *http.Request) {
	for k, vs := range l.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
}

// toAnswer normalises one wire answer.
//
// Confidence is taken as the engine reported it rather than recomputed from
// the distribution. The two are not the same number — a choice whose winning
// option sits at 0.58 can carry a confidence of 0.36 — and the engine's is the
// one that accounts for how close the runner-up was.
func (a layaAnswer) toAnswer() Answer {
	out := Answer{
		Type:          Type(a.Type),
		Confidence:    a.Confidence,
		Probabilities: a.Probabilities,
	}
	switch out.Type {
	case TypeNoul:
		if a.Noul != nil {
			out.Yes = *a.Noul >= 0.5
		}
		out.Label = "no"
		if out.Yes {
			out.Label = "yes"
		}
		out.Probabilities = nil
	case TypeScore:
		if a.Score != nil {
			out.Score = *a.Score
		}
		out.Label = a.scoreLabel()
	default:
		out.Label = a.Choice
	}
	return out
}

// scoreLabel resolves the winning level to its name. The wire keys a score's
// probabilities by index and carries the names in a legend, so an unlabelled
// index is reported as the index itself rather than as an empty string.
func (a layaAnswer) scoreLabel() string {
	best, bestP, found := "", 0.0, false
	for idx, p := range a.Probabilities {
		if !found || p > bestP {
			best, bestP, found = idx, p, true
		}
	}
	if !found {
		if a.Score != nil {
			return strconv.FormatFloat(*a.Score, 'f', -1, 64)
		}
		return ""
	}
	if name, ok := a.Legend[best]; ok && strings.TrimSpace(name) != "" {
		return name
	}
	return best
}
