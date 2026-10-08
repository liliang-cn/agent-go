package pool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// StreamWithTools streams a tool-calling generation: text and reasoning reach
// the callback as they arrive, the tool calls once they are whole.
//
// It used to fetch the whole answer and replay it in one callback, so every
// host reading partials — a chat window, a phone — sat on a spinner for the
// length of the answer and then got all of it at once. The model was
// streaming; this client was not.
//
// The compatibility fallbacks are the same as GenerateWithTools': an upstream
// that refuses a parameter (enable_search on a model without it, tool_choice in
// a thinking mode) says so before it streams anything, so the request is
// retried without it and the stream starts from the retry.
func (c *Client) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	if opts == nil {
		opts = &domain.GenerationOptions{}
	}
	curOpts := opts
	body := func(o *domain.GenerationOptions) map[string]interface{} {
		b := buildPoolGenerateWithToolsRequest(c.modelName, messages, tools, o, c.nativeSearch.sendFormat(), c.nativeSearch.sendOptions())
		b["stream"] = true
		// The usage arrives in a last chunk with no choices. Servers that do
		// not know the option ignore it and usage stays nil.
		b["stream_options"] = map[string]interface{}{"include_usage": true}
		return b
	}
	resp, err := c.openStream(ctx, "/chat/completions", body(curOpts))
	for attempts := 0; err != nil && attempts < 3; attempts++ {
		retryOpts := applyPoolRetryFallbacks(curOpts, err)
		if retryOpts == nil {
			break
		}
		curOpts = retryOpts
		resp, err = c.openStream(ctx, "/chat/completions", body(curOpts))
	}
	if err != nil {
		return fmt.Errorf("request failed (model=%s): %w", c.modelName, err)
	}
	// An upstream that ignores "stream" answers with one JSON document. Read
	// it as the non-streaming reply it is rather than as an empty stream.
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		raw, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			return rerr
		}
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '{' {
			c.recordNativeWebSearch(opts, curOpts, raw)
			result, perr := parsePoolToolResponse(raw, c.modelName)
			if perr != nil {
				return perr
			}
			return callback(result)
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
	}

	st := &poolStreamState{}
	grounded := false
	// thought counts reasoning bytes before the answer starts. A model that
	// is still reasoning past the budget is cut off once and asked again
	// with thinking off: a hive worker on glm-5.3 reasoned for seventeen
	// minutes about the timing of "sleep 25 && hostname".
	thought, rethought := 0, false
	var thinkingSince time.Time
	body2 := resp.Body
	defer func() { drainAndClose(body2) }()
	sc := bufio.NewScanner(body2)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
scan:
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk poolStreamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return fmt.Errorf("request failed (model=%s): stream error: %s", c.modelName, chunk.Error.Message)
		}
		if chunk.chunkGrounded() {
			grounded = true
		}
		delta, done := st.absorb(chunk)
		if delta != nil {
			if delta.ReasoningContent != "" && thinkingSince.IsZero() {
				thinkingSince = time.Now()
				c.reasons.Store(true)
			}
			thought += len(delta.ReasoningContent)
			if !rethought && c.overThinking(thought, thinkingSince) && st.content.Len() == 0 && len(st.calls) == 0 {
				rethought = true
				b := body(curOpts)
				b["enable_thinking"] = false
				next, err := c.openStream(ctx, "/chat/completions", b)
				if err == nil && strings.Contains(next.Header.Get("Content-Type"), "event-stream") {
					body2.Close()
					body2 = next.Body
					st = &poolStreamState{}
					sc = bufio.NewScanner(body2)
					sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
					continue scan
				}
				if err == nil {
					next.Body.Close()
				}
				// The upstream would not answer without thinking; let the
				// first stream run on.
			}
			if err := callback(delta); err != nil {
				return err
			}
		}
		if done {
			break
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("stream read (model=%s): %w", c.modelName, err)
	}

	var groundingProof []byte
	if grounded {
		groundingProof = []byte(`{"choices":[{"message":{"annotations":[{"type":"url_citation"}]}}]}`)
	}
	c.recordNativeWebSearch(opts, curOpts, groundingProof)
	return callback(st.finish())
}

// Defaults for SetReasoningBudget: about ten thousand tokens of reasoning, or
// four minutes of it, whichever comes first.
const (
	defaultReasoningBytes = 32 << 10
	defaultReasoningTime  = 4 * time.Minute
)

// SetReasoningBudget caps how long a streamed reply may reason before it has
// said anything. Past either limit the request is sent again with
// enable_thinking off, once; an upstream that refuses that keeps the first
// stream. Zero restores the default for that limit; a negative value turns it
// off.
func (c *Client) SetReasoningBudget(bytes int, d time.Duration) {
	c.reasoningBytes, c.reasoningTime = bytes, d
}

func (c *Client) overThinking(bytes int, since time.Time) bool {
	limit, d := c.reasoningBytes, c.reasoningTime
	if limit == 0 {
		limit = defaultReasoningBytes
	}
	if d == 0 {
		d = defaultReasoningTime
	}
	if limit > 0 && bytes > limit {
		return true
	}
	return d > 0 && !since.IsZero() && time.Since(since) > d
}

// openStream posts a request and returns the response once the upstream has
// accepted it. A refusal comes back as an error carrying the body, the same
// shape doRequest gives, so the retry fallbacks read it the same way.
func (c *Client) openStream(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	sent, added := c.withEffort(path, body)
	resp, err := c.open(ctx, path, sent)
	if added && c.refusedEffort(err) {
		return c.open(ctx, path, body)
	}
	return resp, err
}

func (c *Client) open(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		// Some upstreams send the refusal as an SSE event; the fallbacks
		// match on the message either way.
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, strings.TrimPrefix(strings.TrimSpace(string(raw)), "data: "))
	}
	return resp, nil
}

type poolStreamChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			Images      []wireOutputImage `json:"images"`
			Annotations []struct {
				Type string `json:"type"`
			} `json:"annotations"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (ch poolStreamChunk) chunkGrounded() bool {
	for _, c := range ch.Choices {
		for _, a := range c.Delta.Annotations {
			if a.Type == "url_citation" {
				return true
			}
		}
	}
	return false
}

type poolStreamCall struct {
	id, typ, name string
	args          strings.Builder
}

// poolStreamState gathers what a stream says over many chunks.
type poolStreamState struct {
	id      string
	reason  string
	usage   *domain.TokenUsage
	calls   map[int]*poolStreamCall
	content strings.Builder // everything said as text, for the Bedrock check
	think   thinkSplitter
	dsml    dsmlHold
}

// absorb takes one chunk and returns what of it is for the reader now — the
// text and the reasoning — and whether the stream is over.
func (s *poolStreamState) absorb(ch poolStreamChunk) (*domain.GenerationResult, bool) {
	if ch.ID != "" {
		s.id = ch.ID
	}
	if len(ch.Usage) > 0 && string(ch.Usage) != "null" {
		if u := parsePoolUsage([]byte(`{"usage":` + string(ch.Usage) + `}`)); u != nil {
			s.usage = u
		}
	}
	if len(ch.Choices) == 0 {
		return nil, false
	}
	c := ch.Choices[0]
	if c.FinishReason != "" {
		s.reason = c.FinishReason
	}
	for _, tc := range c.Delta.ToolCalls {
		if s.calls == nil {
			s.calls = map[int]*poolStreamCall{}
		}
		call := s.calls[tc.Index]
		if call == nil {
			call = &poolStreamCall{}
			s.calls[tc.Index] = call
		}
		if tc.ID != "" {
			call.id = tc.ID
		}
		if tc.Type != "" {
			call.typ = tc.Type
		}
		if tc.Function.Name != "" {
			call.name += tc.Function.Name
		}
		call.args.WriteString(tc.Function.Arguments)
	}

	s.content.WriteString(c.Delta.Content)
	text, reasoning := s.think.feed(c.Delta.Content)
	text = s.dsml.feed(text)
	reasoning = c.Delta.ReasoningContent + c.Delta.Reasoning + reasoning
	parts := outputPartsFromMessage(c.Delta.Images)
	if text == "" && reasoning == "" && len(parts) == 0 {
		return nil, false
	}
	return &domain.GenerationResult{ID: s.id, Content: text, ReasoningContent: reasoning, Parts: parts}, false
}

// finish is the last word: the tool calls, now whole, the finish reason and
// the usage.
func (s *poolStreamState) finish() *domain.GenerationResult {
	text, reasoning := s.think.flush()
	text = s.dsml.feed(text) + s.dsml.release()
	out := &domain.GenerationResult{
		ID:               s.id,
		Content:          text,
		ReasoningContent: reasoning,
		FinishReason:     s.reason,
		Usage:            s.usage,
	}
	idx := make([]int, 0, len(s.calls))
	for i := range s.calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		call := s.calls[i]
		tc := domain.ToolCall{ID: call.id, Type: call.typ, Function: domain.FunctionCall{Name: call.name}}
		if tc.Type == "" {
			tc.Type = "function"
		}
		if raw := strings.TrimSpace(call.args.String()); raw != "" {
			var args map[string]interface{}
			if json.Unmarshal([]byte(raw), &args) == nil {
				tc.Function.Arguments = args
			}
		}
		out.ToolCalls = append(out.ToolCalls, tc)
	}
	if len(out.ToolCalls) == 0 && isBedrockEventStream(s.content.String()) {
		out.ToolCalls = extractBedrockToolCalls(s.content.String())
	}
	// Held DSML markup: tool calls written as text. What was not markup is
	// still said.
	if len(out.ToolCalls) == 0 {
		if calls, rest := extractDSMLToolCalls(out.Content); len(calls) > 0 {
			out.ToolCalls, out.Content = calls, rest
		}
	}
	return out
}

// thinkSplitter separates <think>…</think> from the answer as text streams
// in. A tag can be cut between two chunks, so whatever might be the start of
// one is held back until the next chunk says what it was.
type thinkSplitter struct {
	inThink bool
	held    string
}

func (t *thinkSplitter) feed(s string) (text, reasoning string) {
	s = t.held + s
	t.held = ""
	var out, think strings.Builder
	for s != "" {
		tag := "<think>"
		if t.inThink {
			tag = "</think>"
		}
		if i := strings.Index(s, tag); i >= 0 {
			if t.inThink {
				think.WriteString(s[:i])
			} else {
				out.WriteString(s[:i])
			}
			s = s[i+len(tag):]
			t.inThink = !t.inThink
			continue
		}
		// No whole tag: keep back a tail that could be its beginning.
		keep := 0
		for n := len(tag) - 1; n > 0; n-- {
			if strings.HasSuffix(s, tag[:n]) {
				keep = n
				break
			}
		}
		body := s[:len(s)-keep]
		t.held = s[len(s)-keep:]
		if t.inThink {
			think.WriteString(body)
		} else {
			out.WriteString(body)
		}
		break
	}
	return out.String(), think.String()
}

func (t *thinkSplitter) flush() (text, reasoning string) {
	h := t.held
	t.held = ""
	if t.inThink {
		return "", h
	}
	return h, ""
}

// parsePoolToolResponse reads a non-streaming chat completion into a result:
// what an upstream that ignored "stream" sent instead.
func parsePoolToolResponse(raw []byte, model string) (*domain.GenerationResult, error) {
	var result struct {
		Choices []struct {
			Message struct {
				Content          string            `json:"content"`
				ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
				ReasoningContent string            `json:"reasoning_content"`
				Images           []wireOutputImage `json:"images,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response (model=%s): %w", model, err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response (model=%s)", model)
	}
	choice := result.Choices[0]
	content, reasoning := parseThinkContent(choice.Message.Content, choice.Message.ReasoningContent)
	out := &domain.GenerationResult{
		Content:          content,
		ReasoningContent: reasoning,
		Parts:            outputPartsFromMessage(choice.Message.Images),
		FinishReason:     choice.FinishReason,
		Usage:            parsePoolUsage(raw),
	}
	for _, tc := range choice.Message.ToolCalls {
		var call struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		}
		if json.Unmarshal(tc, &call) != nil {
			continue
		}
		t := domain.ToolCall{ID: call.ID, Type: call.Type, Function: domain.FunctionCall{Name: call.Function.Name}}
		if call.Function.Arguments != "" {
			var args map[string]interface{}
			if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil {
				t.Function.Arguments = args
			}
		}
		out.ToolCalls = append(out.ToolCalls, t)
	}
	if len(out.ToolCalls) == 0 {
		if calls, rest := extractDSMLToolCalls(out.Content); len(calls) > 0 {
			out.ToolCalls, out.Content = calls, rest
		}
	}
	return out, nil
}
