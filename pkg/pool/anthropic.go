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
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	body, stream, err := chatToMessages(raw)
	if err != nil {
		return nil, fmt.Errorf("anthropic: translate request: %w", err)
	}
	out.URL.Path = strings.TrimSuffix(req.URL.Path, "/chat/completions") + "/messages"
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	out.Header.Set("Content-Type", "application/json")

	resp, err := t.base.RoundTrip(out)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	if stream && strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		pr, pw := io.Pipe()
		src := resp.Body
		go func() {
			err := messagesStreamToChat(src, pw)
			_ = src.Close()
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
	conv, err := messagesToChat(data)
	if err != nil {
		return nil, fmt.Errorf("anthropic: translate response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(conv))
	resp.ContentLength = int64(len(conv))
	resp.Header.Set("Content-Length", strconv.Itoa(len(conv)))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
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
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false, err
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

	// Tools: the last one carries the prefix breakpoint.
	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
		CacheControl json.RawMessage `json:"cache_control,omitempty"`
	}
	_ = json.Unmarshal(in["tools"], &tools)
	var outTools []block
	for _, t := range tools {
		if t.Function.Name == "" {
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
		outTools = append(outTools, b)
	}
	if len(outTools) > 0 {
		outTools[len(outTools)-1]["cache_control"] = block{"type": "ephemeral"}
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
		return nil, false, fmt.Errorf("messages: %w", err)
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
	return body, stream, err
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

func messagesToChat(raw []byte) ([]byte, error) {
	var in struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		Usage messagesUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var text, thinking strings.Builder
	var calls []map[string]interface{}
	for _, b := range in.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
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
		}
	}
	msg := map[string]interface{}{"role": "assistant", "content": text.String()}
	if thinking.Len() > 0 {
		msg["reasoning_content"] = thinking.String()
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	return json.Marshal(map[string]interface{}{
		"id": in.ID, "object": "chat.completion", "model": in.Model,
		"choices": []map[string]interface{}{{"index": 0, "message": msg, "finish_reason": finishReason(in.StopReason)}},
		"usage":   chatUsage(in.Usage),
	})
}

// messagesStreamToChat reads a Messages event stream and writes it out as
// chat-completions chunks, ending with a usage chunk and [DONE].
func messagesStreamToChat(src io.Reader, dst io.Writer) error {
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var (
		id, model string
		usage     messagesUsage
		finish    string
		toolIndex = map[int]int{} // content block index -> tool call index
		nextTool  int
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
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage *messagesUsage `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		var err error
		switch ev.Type {
		case "message_start":
			id, model, usage = ev.Message.ID, ev.Message.Model, ev.Message.Usage
			err = emit(chunk(map[string]interface{}{"role": "assistant", "content": ""}, nil))
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				toolIndex[ev.Index] = nextTool
				err = emit(chunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
					"index": nextTool, "id": ev.ContentBlock.ID, "type": "function",
					"function": map[string]string{"name": ev.ContentBlock.Name, "arguments": ""},
				}}}, nil))
				nextTool++
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				err = emit(chunk(map[string]interface{}{"content": ev.Delta.Text}, nil))
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					err = emit(chunk(map[string]interface{}{"reasoning_content": ev.Delta.Thinking}, nil))
				}
			case "input_json_delta":
				if i, ok := toolIndex[ev.Index]; ok && ev.Delta.PartialJSON != "" {
					err = emit(chunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
						"index": i, "function": map[string]string{"arguments": ev.Delta.PartialJSON},
					}}}, nil))
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				finish = finishReason(ev.Delta.StopReason)
			}
			if ev.Usage != nil {
				// The final counts; input figures arrive here too on
				// some versions, so a non-zero one replaces the start's.
				usage.OutputTokens = ev.Usage.OutputTokens
				if ev.Usage.InputTokens > 0 {
					usage.InputTokens = ev.Usage.InputTokens
				}
				if ev.Usage.CacheReadInputTokens > 0 {
					usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
				}
				if ev.Usage.CacheCreationInputTokens > 0 {
					usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				}
			}
		case "message_stop":
			if finish == "" {
				finish = "stop"
			}
			if err = emit(chunk(map[string]interface{}{}, finish)); err == nil {
				err = emit(map[string]interface{}{
					"id": id, "object": "chat.completion.chunk", "model": model,
					"choices": []interface{}{}, "usage": chatUsage(usage),
				})
			}
			if err == nil {
				_, err = io.WriteString(dst, "data: [DONE]\n\n")
			}
			if err != nil {
				return err
			}
			return nil
		case "error":
			msg := "stream error"
			if ev.Error != nil {
				msg = ev.Error.Type + ": " + ev.Error.Message
			}
			return emit(map[string]interface{}{"error": map[string]string{"message": msg}})
		}
		if err != nil {
			return err
		}
	}
	return sc.Err()
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
