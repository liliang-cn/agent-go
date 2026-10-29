package pool

// Anthropic's own API, spoken in place of its OpenAI-compatible one.
//
// api.anthropic.com answers /v1/chat/completions, and everything in this
// package speaks that shape. But the compatibility layer does not cache:
// "Prompt caching is not supported" is in its list of limitations. An agent
// loop resends its whole conversation every round, so on that endpoint a run
// paid full prefill for its entire history on every round — one week of a
// queen on Opus came to 1.6M input tokens and a 0% cache read ratio.
//
// So a client whose base URL is Anthropic's sends what it built to
// /v1/messages instead. The translation lives at the transport: the request
// body is rewritten from the chat-completions shape to the Messages one on
// the way out, and the answer — whole or streamed — is rewritten back on the
// way in. Nothing above it changes, and every retry, fallback and trace keeps
// working on the shape it already knows.
//
// Caching is the point, so it is on whatever the caller asked for: the last
// tool carries the prefix (tools, then system, are rendered before messages),
// and the request's top-level cache_control puts the second breakpoint on the
// last block, so each round reads everything up to the previous one.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// anthropicVersion is the API version header every request carries.
const anthropicVersion = "2023-06-01"

// anthropicDefaultMaxTokens is used when the caller named none: the Messages
// API requires it, and a low value cuts an answer off mid-thought.
const anthropicDefaultMaxTokens = 32000

// isAnthropicNative reports whether baseURL is Anthropic's own API, the one
// whose chat-completions endpoint does not cache.
func isAnthropicNative(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.anthropic.com")
}

// anthropicTransport rewrites chat-completions requests to /v1/messages and
// their answers back. Any other request to the host only has its auth header
// translated.
type anthropicTransport struct{ base http.RoundTripper }

func (t anthropicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	if auth := out.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		out.Header.Del("Authorization")
		out.Header.Set("x-api-key", strings.TrimPrefix(auth, "Bearer "))
	}
	out.Header.Set("anthropic-version", anthropicVersion)
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/chat/completions") || req.Body == nil {
		return t.base.RoundTrip(out)
	}

	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	ctx := req.Context()
	body, stream, dropped, err := translateChatRequest(raw)
	if err != nil {
		return nil, fmt.Errorf("anthropic: translate request: %w", err)
	}
	for _, d := range dropped {
		trace.Decide(ctx, "drop_server_tool",
			"a function tool already has the server tool's name; the function tool is sent and the server tool is not",
			map[string]any{"tool": d.name, "type": d.typ})
	}
	out.URL.Path = strings.TrimSuffix(req.URL.Path, "/chat/completions") + "/messages"
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	out.Header.Set("Content-Type", "application/json")

	firstStart := time.Now()
	resp, err := t.base.RoundTrip(out)
	if err == nil && resp.StatusCode == http.StatusBadRequest && stream {
		// A model that does not take eager_input_streaming says so before
		// anything streams: the request goes again without it.
		resp, err = retryWithoutEagerInput(out, resp, t.base)
	}
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	// A turn that used a server tool can stop at pause_turn: the server's
	// own loop ran out of iterations and the answer is not finished. It is
	// resumed here, by sending the turn so far back as the assistant's, and
	// the caller sees one answer.
	resume := func(content []json.RawMessage) (*http.Response, error) {
		next, err := withAssistantContent(body, content)
		if err != nil {
			return nil, err
		}
		r := out.Clone(out.Context())
		r.Body = io.NopCloser(bytes.NewReader(next))
		r.ContentLength = int64(len(next))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(next)), nil }
		agentgolog.GetLogger().DebugContext(ctx, "anthropic: resuming a paused turn",
			"module", "pool", "blocks", len(content))
		resp, err := t.base.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("anthropic: resume after pause_turn: status %d", resp.StatusCode)
		}
		return resp, nil
	}
	if stream && strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		pr, pw := io.Pipe()
		src := resp.Body
		go func() {
			err := streamMessagesToChatFrom(firstStart, src, pw, func(content []json.RawMessage) (io.ReadCloser, error) {
				r, err := resume(content)
				if err != nil {
					return nil, err
				}
				if !strings.Contains(r.Header.Get("Content-Type"), "event-stream") {
					_ = r.Body.Close()
					return nil, fmt.Errorf("anthropic: resume after pause_turn: not a stream")
				}
				return r.Body, nil
			})
			_ = pw.CloseWithError(err)
		}()
		resp.Body = pr
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return resp, nil
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	requestMs := []int64{time.Since(firstStart).Milliseconds()}
	msg, err := decodeMessagesResponse(data)
	if err != nil {
		return nil, fmt.Errorf("anthropic: translate response: %w", err)
	}
	resumes := 0
	for i := 0; msg.StopReason == "pause_turn" && i < anthropicMaxResumes; i++ {
		began := time.Now()
		r, err := resume(msg.Content)
		if err != nil {
			break // what came so far is the answer
		}
		more, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			break
		}
		next, err := decodeMessagesResponse(more)
		if err != nil {
			break
		}
		resumes++
		requestMs = append(requestMs, time.Since(began).Milliseconds())
		msg.Content = append(msg.Content, next.Content...)
		msg.StopReason = next.StopReason
		msg.Usage = msg.Usage.plus(next.Usage)
	}
	chat := chatFromMessages(msg)
	if rep := serverToolReport(msg.Content, resumes, requestMs); rep != nil {
		chat[serverToolsKey] = rep
	}
	conv, err := json.Marshal(chat)
	if err != nil {
		return nil, fmt.Errorf("anthropic: translate response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(conv))
	resp.ContentLength = int64(len(conv))
	resp.Header.Set("Content-Length", strconv.Itoa(len(conv)))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

// anthropicMaxResumes bounds how many times one turn is resumed after
// pause_turn, so a server loop that never finishes cannot hold a request
// forever.
const anthropicMaxResumes = 5

// withAssistantContent returns a Messages body with content as the
// assistant's last turn: appended to a trailing assistant message, or as a
// new one after the user's. The API resumes a paused turn from exactly
// this, its own blocks sent back unchanged.
func withAssistantContent(body []byte, content []json.RawMessage) ([]byte, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	var msgs []struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(req["messages"], &msgs); err != nil {
		return nil, err
	}
	if n := len(msgs); n > 0 && msgs[n-1].Role == "assistant" {
		msgs[n-1].Content = append(msgs[n-1].Content, content...)
	} else {
		msgs = append(msgs, struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		}{"assistant", content})
	}
	raw, err := json.Marshal(msgs)
	if err != nil {
		return nil, err
	}
	req["messages"] = raw
	return json.Marshal(req)
}

// ---- request ----

type chatToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type chatPart struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
	ImageURL     *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

// chatParts reads a chat message's content, a string or a list of parts.
func chatParts(raw json.RawMessage) []chatPart {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []chatPart{{Type: "text", Text: s}}
	}
	var parts []chatPart
	_ = json.Unmarshal(raw, &parts)
	return parts
}

// block is one Messages content block, kept as a map so only the fields the
// block has are sent.
type block = map[string]interface{}

func partBlocks(parts []chatPart) []block {
	var out []block
	for _, p := range parts {
		var b block
		switch p.Type {
		case "text", "":
			if p.Text == "" {
				continue
			}
			b = block{"type": "text", "text": p.Text}
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			b = imageBlock(p.ImageURL.URL)
			if b == nil {
				continue
			}
		default:
			continue
		}
		if len(p.CacheControl) > 0 {
			b["cache_control"] = json.RawMessage(p.CacheControl)
		}
		out = append(out, b)
	}
	return out
}

// imageBlock turns an image_url (a data: URL or a web address) into an image
// block.
func imageBlock(u string) block {
	if rest, ok := strings.CutPrefix(u, "data:"); ok {
		meta, data, found := strings.Cut(rest, ",")
		if !found || !strings.HasSuffix(meta, ";base64") {
			return nil
		}
		return block{"type": "image", "source": block{
			"type": "base64", "media_type": strings.TrimSuffix(meta, ";base64"), "data": data,
		}}
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return block{"type": "image", "source": block{"type": "url", "url": u}}
	}
	return nil
}

// chatToMessages rewrites a chat-completions body as a Messages one, and
// reports whether it asked for a stream.
func chatToMessages(raw []byte) ([]byte, bool, error) {
	body, stream, _, err := translateChatRequest(raw)
	return body, stream, err
}

// droppedServerTool is a server tool left out of a request because a function
// tool already had its name.
type droppedServerTool struct{ name, typ string }

// translateChatRequest is chatToMessages that also says which server tools it
// left out, so the caller can record the decision.
func translateChatRequest(raw []byte) ([]byte, bool, []droppedServerTool, error) {
	var dropped []droppedServerTool
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false, nil, err
	}
	out := map[string]interface{}{}
	var model string
	_ = json.Unmarshal(in["model"], &model)
	out["model"] = model

	maxTokens := 0
	for _, k := range []string{"max_completion_tokens", "max_tokens"} {
		if v, ok := in[k]; ok {
			_ = json.Unmarshal(v, &maxTokens)
			if maxTokens > 0 {
				break
			}
		}
	}
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	out["max_tokens"] = maxTokens

	stream := false
	_ = json.Unmarshal(in["stream"], &stream)
	if stream {
		out["stream"] = true
	}

	if v, ok := in["stop"]; ok {
		var one string
		var many []string
		if json.Unmarshal(v, &one) == nil && strings.TrimSpace(one) != "" {
			out["stop_sequences"] = []string{one}
		} else if json.Unmarshal(v, &many) == nil {
			var keep []string
			for _, s := range many {
				if strings.TrimSpace(s) != "" {
					keep = append(keep, s)
				}
			}
			if len(keep) > 0 {
				out["stop_sequences"] = keep
			}
		}
	}

	// Effort, and a JSON schema the answer must follow, go in output_config.
	// temperature and top_p are not sent: current models refuse them.
	outputConfig := map[string]interface{}{}
	var effort string
	if json.Unmarshal(in["reasoning_effort"], &effort) == nil && effort != "" {
		switch effort {
		case "minimal":
			effort = "low"
		case "none":
			effort = ""
		}
		if effort != "" {
			outputConfig["effort"] = effort
		}
	}
	if v, ok := in["response_format"]; ok {
		var rf struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		}
		if json.Unmarshal(v, &rf) == nil && rf.Type == "json_schema" && len(rf.JSONSchema.Schema) > 0 {
			outputConfig["format"] = map[string]interface{}{"type": "json_schema", "schema": closedSchema(rf.JSONSchema.Schema)}
		}
	}
	if len(outputConfig) > 0 {
		out["output_config"] = outputConfig
	}

	// Tools: the last function tool carries the prefix breakpoint. A server
	// tool (native_web_search "anthropic") arrives already in the Messages
	// shape, a type and a name, and is sent as it came, after them.
	var rawTools []json.RawMessage
	_ = json.Unmarshal(in["tools"], &rawTools)
	var outTools, serverTools []block
	names := map[string]bool{}
	for _, raw := range rawTools {
		var t struct {
			Type     string `json:"type"`
			Name     string `json:"name"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &t) != nil {
			continue
		}
		if t.Function.Name == "" {
			if t.Type != "" && t.Type != "function" && t.Name != "" {
				var st block
				if json.Unmarshal(raw, &st) == nil {
					serverTools = append(serverTools, st)
				}
			}
			continue
		}
		schema := t.Function.Parameters
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		b := block{"name": t.Function.Name, "input_schema": schema}
		if t.Function.Description != "" {
			b["description"] = t.Function.Description
		}
		if stream {
			// Without it the API holds a call's input back and sends it in
			// one burst when the call is done, so an answer written into a
			// call (task_complete's result) reached the person all at once.
			b["eager_input_streaming"] = true
		}
		outTools = append(outTools, b)
		names[t.Function.Name] = true
	}
	if len(outTools) > 0 {
		outTools[len(outTools)-1]["cache_control"] = block{"type": "ephemeral"}
	}
	for _, st := range serverTools {
		// Tool names must be unique. A function tool already called
		// web_search (agent.RegisterWebSearchTool) is one somebody
		// registered on purpose; the server tool steps aside for it.
		if name, _ := st["name"].(string); names[name] {
			typ, _ := st["type"].(string)
			slog.Warn("a function tool has the server tool's name; sending the function tool only",
				"module", "pool", "tool", name, "type", typ)
			dropped = append(dropped, droppedServerTool{name: name, typ: typ})
			continue
		}
		names[st["name"].(string)] = true
		outTools = append(outTools, st)
	}
	if len(outTools) > 0 {
		out["tools"] = outTools
		choice := block{"type": "auto"}
		var tc string
		if json.Unmarshal(in["tool_choice"], &tc) == nil && tc == "none" {
			choice = block{"type": "none"}
		}
		// "required" and a named tool are forced tool use, which current
		// models refuse; auto is what they take.
		var parallel *bool
		if v, ok := in["parallel_tool_calls"]; ok {
			var p bool
			if json.Unmarshal(v, &p) == nil {
				parallel = &p
			}
		}
		if parallel != nil && !*parallel && choice["type"] == "auto" {
			choice["disable_parallel_tool_use"] = true
		}
		out["tool_choice"] = choice
	}

	var msgs []chatMessage
	if err := json.Unmarshal(in["messages"], &msgs); err != nil {
		return nil, false, nil, fmt.Errorf("messages: %w", err)
	}
	system, conv := convertMessages(msgs)
	if len(system) > 0 {
		out["system"] = system
	}
	out["messages"] = conv
	// The second breakpoint: the last cacheable block of the request —
	// unless the caller marked the history itself (WithPromptCache), whose
	// marks plus this one could pass the four a request may carry.
	if !hasCacheMark(conv) {
		out["cache_control"] = block{"type": "ephemeral"}
	}

	body, err := json.Marshal(out)
	return body, stream, dropped, err
}

type anthropicMessage struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

// convertMessages splits out the system prompt and turns the rest into
// alternating user and assistant turns: tool results become tool_result
// blocks in a user turn, and consecutive turns of one role are merged, as
// the Messages API requires.
func convertMessages(msgs []chatMessage) ([]block, []anthropicMessage) {
	var system []block
	var conv []anthropicMessage
	add := func(role string, blocks []block) {
		if len(blocks) == 0 {
			return
		}
		if n := len(conv); n > 0 && conv[n-1].Role == role {
			conv[n-1].Content = append(conv[n-1].Content, blocks...)
			return
		}
		conv = append(conv, anthropicMessage{Role: role, Content: blocks})
	}
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			system = append(system, partBlocks(chatParts(m.Content))...)
		case "user":
			add("user", partBlocks(chatParts(m.Content)))
		case "assistant":
			blocks := partBlocks(chatParts(m.Content))
			for _, tc := range m.ToolCalls {
				// The API takes an object and nothing else. A call made with
				// no arguments comes back from history as "null".
				input := json.RawMessage(strings.TrimSpace(tc.Function.Arguments))
				if !json.Valid(input) || !strings.HasPrefix(string(input), "{") {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, block{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input})
			}
			add("assistant", blocks)
		case "tool":
			var content interface{}
			parts := partBlocks(chatParts(m.Content))
			if len(parts) == 0 {
				content = "(no output)"
			} else {
				content = parts
			}
			add("user", []block{{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": content}})
		}
	}
	// A conversation starts with the user. One that would not (a history
	// whose first kept turn is an answer) gets a placeholder before it.
	if len(conv) > 0 && conv[0].Role != "user" {
		conv = append([]anthropicMessage{{Role: "user", Content: []block{{"type": "text", "text": "(continued)"}}}}, conv...)
	}
	return system, conv
}

// hasCacheMark reports whether any block of the conversation, or a block
// inside a tool result, carries cache_control.
func hasCacheMark(conv []anthropicMessage) bool {
	for _, m := range conv {
		for _, b := range m.Content {
			if _, ok := b["cache_control"]; ok {
				return true
			}
			if inner, ok := b["content"].([]block); ok {
				for _, ib := range inner {
					if _, ok := ib["cache_control"]; ok {
						return true
					}
				}
			}
		}
	}
	return false
}

// ---- response ----

type messagesUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// chatUsage is usage in the chat-completions shape, with Anthropic's cache
// counts alongside (parsePoolUsage reads both). prompt_tokens counts every
// input token, cached or not, as it does on OpenAI.
func chatUsage(u messagesUsage) map[string]interface{} {
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	return map[string]interface{}{
		"prompt_tokens":               prompt,
		"completion_tokens":           u.OutputTokens,
		"total_tokens":                prompt + u.OutputTokens,
		"prompt_tokens_details":       map[string]int{"cached_tokens": u.CacheReadInputTokens},
		"cache_read_input_tokens":     u.CacheReadInputTokens,
		"cache_creation_input_tokens": u.CacheCreationInputTokens,
	}
}

func finishReason(stop string) string {
	switch stop {
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "refusal":
		return "content_filter"
	default: // end_turn, stop_sequence, pause_turn
		return "stop"
	}
}

// messagesResponse is a Messages answer with its content blocks kept as
// sent, so a paused turn can be sent back unchanged.
type messagesResponse struct {
	ID         string            `json:"id"`
	Model      string            `json:"model"`
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
	Usage      messagesUsage     `json:"usage"`
}

func decodeMessagesResponse(raw []byte) (messagesResponse, error) {
	var m messagesResponse
	err := json.Unmarshal(raw, &m)
	return m, err
}

func (u messagesUsage) plus(o messagesUsage) messagesUsage {
	return messagesUsage{
		InputTokens:              u.InputTokens + o.InputTokens,
		OutputTokens:             u.OutputTokens + o.OutputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens + o.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens + o.CacheCreationInputTokens,
	}
}

// contentBlock is the part of a content block the translation reads.
//
// Server tools add three kinds: server_tool_use (the search the model ran),
// web_search_tool_result (what it found, or an error object), and citations
// on the text that uses it. None has a chat-completions form. The text is
// the answer and is kept; the search and its results are not carried into
// history, so a later turn sends neither — the API takes an assistant turn
// without them, where one without the other would be refused. The pages
// cited, or found, become url_citation annotations: that is what proves to
// nativeSearchState that the model searched.
type contentBlock struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Citations []citation      `json:"citations"`
	Content   json.RawMessage `json:"content"`
}

type citation struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// urlAnnotations gathers url_citation annotations, each URL once.
type urlAnnotations struct {
	seen map[string]bool
	list []map[string]interface{}
}

func (a *urlAnnotations) add(url, title string) {
	if url == "" || a.seen[url] {
		return
	}
	if a.seen == nil {
		a.seen = map[string]bool{}
	}
	a.seen[url] = true
	a.list = append(a.list, map[string]interface{}{
		"type": "url_citation", "url_citation": map[string]string{"url": url, "title": title},
	})
}

// serverToolsKey is where the translated answer carries the server tool
// report: an extension field, unknown to the chat-completions shape, read
// back by parseServerTools into GenerationResult.ServerTools.
const serverToolsKey = "x_server_tools"

// serverToolReport reads what the server did from a turn's content blocks:
// each server_tool_use with its input, what its result block said, and the
// citations in the text. nil when there was nothing server-side to report.
func serverToolReport(content []json.RawMessage, resumes int, requestMs []int64) *domain.ServerToolReport {
	rep := &domain.ServerToolReport{Resumes: resumes}
	byID := map[string]int{}
	for _, raw := range content {
		var b contentBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		switch {
		case b.Type == "server_tool_use":
			use := domain.ServerToolUse{ID: b.ID, Name: b.Name}
			_ = json.Unmarshal(b.Input, &use.Input)
			byID[b.ID] = len(rep.Uses)
			rep.Uses = append(rep.Uses, use)
		case strings.HasSuffix(b.Type, "_tool_result") && b.Type != "tool_result":
			i, ok := byID[b.ToolUseID]
			if !ok {
				rep.Uses = append(rep.Uses, domain.ServerToolUse{ID: b.ToolUseID, Name: strings.TrimSuffix(b.Type, "_tool_result")})
				i = len(rep.Uses) - 1
			}
			var failed struct {
				ErrorCode string `json:"error_code"`
			}
			if json.Unmarshal(b.Content, &failed) == nil && failed.ErrorCode != "" {
				rep.Uses[i].ErrorCode = failed.ErrorCode
				continue
			}
			results := searchResults(b.Content)
			rep.Uses[i].Results = len(results)
			for _, r := range results {
				if r.URL != "" {
					rep.Uses[i].URLs = append(rep.Uses[i].URLs, r.URL)
				}
			}
		case b.Type == "text":
			rep.Citations += len(b.Citations)
		}
	}
	if len(rep.Uses) == 0 && resumes == 0 && rep.Citations == 0 {
		return nil
	}
	rep.RequestMs = requestMs
	return rep
}

// searchResults reads a web_search_tool_result's content: a list of results
// when the search ran, an error object when it did not.
func searchResults(raw json.RawMessage) []citation {
	var results []citation
	if json.Unmarshal(raw, &results) != nil {
		return nil
	}
	return results
}

func messagesToChat(raw []byte) ([]byte, error) {
	m, err := decodeMessagesResponse(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(chatFromMessages(m))
}

func chatFromMessages(in messagesResponse) map[string]interface{} {
	var text, thinking strings.Builder
	var calls []map[string]interface{}
	var cited, found urlAnnotations
	for _, raw := range in.Content {
		var b contentBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
			for _, c := range b.Citations {
				cited.add(c.URL, c.Title)
			}
		case "thinking":
			thinking.WriteString(b.Thinking)
		case "tool_use":
			args := string(b.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			calls = append(calls, map[string]interface{}{
				"id": b.ID, "type": "function",
				"function": map[string]string{"name": b.Name, "arguments": args},
			})
		case "web_search_tool_result":
			for _, r := range searchResults(b.Content) {
				found.add(r.URL, r.Title)
			}
		}
	}
	msg := map[string]interface{}{"role": "assistant", "content": text.String()}
	if thinking.Len() > 0 {
		msg["reasoning_content"] = thinking.String()
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	if len(cited.list) > 0 {
		msg["annotations"] = cited.list
	} else if len(found.list) > 0 {
		msg["annotations"] = found.list
	}
	return map[string]interface{}{
		"id": in.ID, "object": "chat.completion", "model": in.Model,
		"choices": []map[string]interface{}{{"index": 0, "message": msg, "finish_reason": finishReason(in.StopReason)}},
		"usage":   chatUsage(in.Usage),
	}
}

// messagesStreamToChat reads a Messages event stream and writes it out as
// chat-completions chunks, ending with a usage chunk and [DONE].
func messagesStreamToChat(src io.Reader, dst io.Writer) error {
	return streamMessagesToChat(io.NopCloser(src), dst, nil)
}

// streamMessagesToChat is streamMessagesToChatFrom timed from now.
func streamMessagesToChat(src io.ReadCloser, dst io.Writer, resume func(content []json.RawMessage) (io.ReadCloser, error)) error {
	return streamMessagesToChatFrom(time.Now(), src, dst, resume)
}

// streamMessagesToChatFrom is messagesStreamToChat that resumes a turn paused
// by a server tool: the blocks streamed so far are rebuilt and handed to
// resume, and its stream carries on into the same chat stream. src is
// closed, and so is every stream resume opens. sentAt is when the first
// request was sent, for the per-request durations in the server tool report.
func streamMessagesToChatFrom(sentAt time.Time, src io.ReadCloser, dst io.Writer, resume func(content []json.RawMessage) (io.ReadCloser, error)) error {
	var (
		id, model string
		total     messagesUsage
		started   bool
		nextTool  int
		content   []json.RawMessage
		requestMs []int64
	)
	emit := func(v interface{}) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(dst, "data: %s\n\n", b)
		return err
	}
	chunk := func(delta map[string]interface{}, finish interface{}) map[string]interface{} {
		return map[string]interface{}{
			"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []map[string]interface{}{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}
	for resumes := 0; ; resumes++ {
		turn := &streamTurn{toolIndex: map[int]int{}, blocks: map[int]block{}, partial: map[int]*strings.Builder{}}
		// A resumed stream is the same answer going on: its message_start
		// opens nothing new.
		err := turn.read(src, &nextTool, func(t *streamTurn) error {
			if started {
				return nil
			}
			started = true
			id, model = t.id, t.model
			return emit(chunk(map[string]interface{}{"role": "assistant", "content": ""}, nil))
		}, emit, chunk)
		_ = src.Close()
		if err != nil {
			return err
		}
		if turn.streamErr != "" {
			return emit(map[string]interface{}{"error": map[string]string{"message": turn.streamErr}})
		}
		if !turn.stopped {
			return nil // the stream ended without message_stop
		}
		total = total.plus(turn.usage)
		content = append(content, turn.content()...)
		requestMs = append(requestMs, time.Since(sentAt).Milliseconds())
		if turn.stop == "pause_turn" && resume != nil && resumes < anthropicMaxResumes {
			sentAt = time.Now()
			next, rerr := resume(content)
			if rerr == nil {
				src = next
				continue
			}
		}
		if err := emit(chunk(map[string]interface{}{}, finishReason(turn.stop))); err != nil {
			return err
		}
		if rep := serverToolReport(content, len(requestMs)-1, requestMs); rep != nil {
			if err := emit(map[string]interface{}{
				"id": id, "object": "chat.completion.chunk", "model": model,
				"choices": []interface{}{}, serverToolsKey: rep,
			}); err != nil {
				return err
			}
		}
		if err := emit(map[string]interface{}{
			"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []interface{}{}, "usage": chatUsage(total),
		}); err != nil {
			return err
		}
		_, err = io.WriteString(dst, "data: [DONE]\n\n")
		return err
	}
}

// streamTurn is one Messages stream as it is read: the chunks it becomes
// are written as they arrive, and its content blocks are rebuilt beside
// them, verbatim, for a resume to send back.
type streamTurn struct {
	id, model string
	usage     messagesUsage
	stop      string
	stopped   bool
	streamErr string
	toolIndex map[int]int // content block index -> tool call index
	blocks    map[int]block
	partial   map[int]*strings.Builder // input_json_delta, by block index
	order     []int
}

// content is the turn's blocks in order, with streamed inputs filled in.
func (t *streamTurn) content() []json.RawMessage {
	var out []json.RawMessage
	for _, i := range t.order {
		b := t.blocks[i]
		if p, ok := t.partial[i]; ok && p.Len() > 0 {
			input := json.RawMessage(strings.TrimSpace(p.String()))
			if !json.Valid(input) || !strings.HasPrefix(string(input), "{") {
				input = json.RawMessage(`{}`)
			}
			b["input"] = input
		}
		if c, ok := b["citations"]; ok && c == nil {
			delete(b, "citations") // a start block's null, not a value to send
		}
		raw, err := json.Marshal(b)
		if err == nil {
			out = append(out, raw)
		}
	}
	return out
}

func (t *streamTurn) read(src io.Reader, nextTool *int, onStart func(*streamTurn) error,
	emit func(interface{}) error, chunk func(map[string]interface{}, interface{}) map[string]interface{}) error {
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string        `json:"id"`
				Model string        `json:"model"`
				Usage messagesUsage `json:"usage"`
			} `json:"message"`
			ContentBlock json.RawMessage `json:"content_block"`
			Delta        struct {
				Type        string   `json:"type"`
				Text        string   `json:"text"`
				Thinking    string   `json:"thinking"`
				Signature   string   `json:"signature"`
				PartialJSON string   `json:"partial_json"`
				StopReason  string   `json:"stop_reason"`
				Citation    citation `json:"citation"`
			} `json:"delta"`
			Usage *messagesUsage `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		trimmed := strings.TrimSpace(data)
		if json.Unmarshal([]byte(trimmed), &ev) != nil {
			continue
		}
		// The raw delta, for citations: kept whole in the rebuilt block.
		var rawDelta struct {
			Delta struct {
				Citation json.RawMessage `json:"citation"`
			} `json:"delta"`
		}
		var err error
		switch ev.Type {
		case "message_start":
			t.id, t.model, t.usage = ev.Message.ID, ev.Message.Model, ev.Message.Usage
			err = onStart(t)
		case "content_block_start":
			var b block
			if json.Unmarshal(ev.ContentBlock, &b) != nil {
				continue
			}
			if _, dup := t.blocks[ev.Index]; !dup {
				t.order = append(t.order, ev.Index)
			}
			t.blocks[ev.Index] = b
			var cb contentBlock
			_ = json.Unmarshal(ev.ContentBlock, &cb)
			switch cb.Type {
			case "tool_use":
				t.partial[ev.Index] = &strings.Builder{}
				t.toolIndex[ev.Index] = *nextTool
				err = emit(chunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
					"index": *nextTool, "id": cb.ID, "type": "function",
					"function": map[string]string{"name": cb.Name, "arguments": ""},
				}}}, nil))
				*nextTool++
			case "server_tool_use":
				t.partial[ev.Index] = &strings.Builder{}
			case "web_search_tool_result":
				var found urlAnnotations
				for _, r := range searchResults(cb.Content) {
					found.add(r.URL, r.Title)
				}
				if len(found.list) > 0 {
					err = emit(chunk(map[string]interface{}{"annotations": found.list}, nil))
				}
			}
		case "content_block_delta":
			b := t.blocks[ev.Index]
			switch ev.Delta.Type {
			case "text_delta":
				if b != nil {
					prev, _ := b["text"].(string)
					b["text"] = prev + ev.Delta.Text
				}
				err = emit(chunk(map[string]interface{}{"content": ev.Delta.Text}, nil))
			case "thinking_delta":
				if b != nil {
					prev, _ := b["thinking"].(string)
					b["thinking"] = prev + ev.Delta.Thinking
				}
				if ev.Delta.Thinking != "" {
					err = emit(chunk(map[string]interface{}{"reasoning_content": ev.Delta.Thinking}, nil))
				}
			case "signature_delta":
				if b != nil {
					prev, _ := b["signature"].(string)
					b["signature"] = prev + ev.Delta.Signature
				}
			case "citations_delta":
				if b != nil && json.Unmarshal([]byte(trimmed), &rawDelta) == nil && len(rawDelta.Delta.Citation) > 0 {
					list, _ := b["citations"].([]interface{})
					b["citations"] = append(list, rawDelta.Delta.Citation)
				}
				var cited urlAnnotations
				cited.add(ev.Delta.Citation.URL, ev.Delta.Citation.Title)
				if len(cited.list) > 0 {
					err = emit(chunk(map[string]interface{}{"annotations": cited.list}, nil))
				}
			case "input_json_delta":
				if p, ok := t.partial[ev.Index]; ok {
					p.WriteString(ev.Delta.PartialJSON)
				}
				if i, ok := t.toolIndex[ev.Index]; ok && ev.Delta.PartialJSON != "" {
					err = emit(chunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
						"index": i, "function": map[string]string{"arguments": ev.Delta.PartialJSON},
					}}}, nil))
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				t.stop = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				// The final counts; input figures arrive here too on
				// some versions, so a non-zero one replaces the start's.
				t.usage.OutputTokens = ev.Usage.OutputTokens
				if ev.Usage.InputTokens > 0 {
					t.usage.InputTokens = ev.Usage.InputTokens
				}
				if ev.Usage.CacheReadInputTokens > 0 {
					t.usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
				}
				if ev.Usage.CacheCreationInputTokens > 0 {
					t.usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				}
			}
		case "message_stop":
			t.stopped = true
			return nil
		case "error":
			t.streamErr = "stream error"
			if ev.Error != nil {
				t.streamErr = ev.Error.Type + ": " + ev.Error.Message
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
	return sc.Err()
}

// retryWithoutEagerInput sends req again with eager_input_streaming taken off
// every tool, when the 400 in resp is about that field. Any other 400 is
// handed back as it came, its body still readable.
func retryWithoutEagerInput(req *http.Request, resp *http.Response, base http.RoundTripper) (*http.Response, error) {
	msg, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(msg))
	if !bytes.Contains(msg, []byte("eager_input_streaming")) {
		return resp, nil
	}
	sent, err := io.ReadAll(mustBody(req))
	if err != nil {
		return resp, nil
	}
	var b map[string]json.RawMessage
	var tools []map[string]json.RawMessage
	if json.Unmarshal(sent, &b) != nil || json.Unmarshal(b["tools"], &tools) != nil {
		return resp, nil
	}
	for _, t := range tools {
		delete(t, "eager_input_streaming")
	}
	b["tools"], _ = json.Marshal(tools)
	next, err := json.Marshal(b)
	if err != nil {
		return resp, nil
	}
	r := req.Clone(req.Context())
	r.Body = io.NopCloser(bytes.NewReader(next))
	r.ContentLength = int64(len(next))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(next)), nil }
	return base.RoundTrip(r)
}

func mustBody(r *http.Request) io.Reader {
	if r.GetBody == nil {
		return bytes.NewReader(nil)
	}
	b, err := r.GetBody()
	if err != nil {
		return bytes.NewReader(nil)
	}
	return b
}

// closedSchema returns schema with additionalProperties false on every object
// that does not say otherwise. The Messages API refuses a schema with an
// object left open; an explicit additionalProperties is kept as written. A
// schema that does not parse is returned as is, for the API to judge.
func closedSchema(schema json.RawMessage) json.RawMessage {
	var node interface{}
	if json.Unmarshal(schema, &node) != nil {
		return schema
	}
	closeSchemaNode(node)
	out, err := json.Marshal(node)
	if err != nil {
		return schema
	}
	return out
}

// closeSchemaNode walks one schema. It follows the keywords that hold
// schemas, and only those: "properties" maps names to schemas, so a property
// called "type" or "properties" is never taken for a keyword.
func closeSchemaNode(node interface{}) {
	s, ok := node.(map[string]interface{})
	if !ok {
		return
	}
	_, hasProps := s["properties"]
	if s["type"] == "object" || hasProps {
		if _, set := s["additionalProperties"]; !set {
			s["additionalProperties"] = false
		}
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions"} {
		if m, ok := s[key].(map[string]interface{}); ok {
			for _, sub := range m {
				closeSchemaNode(sub)
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not", "if", "then", "else", "contains"} {
		switch v := s[key].(type) {
		case map[string]interface{}:
			closeSchemaNode(v)
		case []interface{}:
			for _, sub := range v {
				closeSchemaNode(sub)
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if list, ok := s[key].([]interface{}); ok {
			for _, sub := range list {
				closeSchemaNode(sub)
			}
		}
	}
}
