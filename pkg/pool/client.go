package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/prompt"
)

// Client is an OpenAI-compatible LLM/embedding client.
type Client struct {
	providerName  string
	baseURL       string
	key           string
	modelName     string
	http          *http.Client
	promptManager *prompt.Manager
	// nativeSearch accumulates evidence about the upstream's native
	// web-search support (see native_search.go). Shared by pointer across
	// clients derived from the same provider.
	nativeSearch *nativeSearchState
	// structuredSchemaRejected is set once the upstream refuses
	// response_format json_schema; structured calls then use the prompt form.
	structuredSchemaRejected atomic.Bool
	// reasons is set once a reply from this upstream has carried reasoning,
	// and thinkingOffRejected once it has refused the field that turns
	// reasoning off. Between them they decide whether a structured call that
	// wants no reasoning asks for it; see structuredCompletion.
	reasons             atomic.Bool
	thinkingOffRejected atomic.Bool
	// reasoningBytes and reasoningTime are the streamed-reasoning budget;
	// see SetReasoningBudget.
	reasoningBytes int
	reasoningTime  time.Duration
}

// NewClient creates a new client.
func NewClient(providerName, baseURL, key, modelName string) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	if modelName == "" {
		return nil, fmt.Errorf("model_name is required")
	}

	return &Client{
		providerName: providerName,
		baseURL:      baseURL,
		key:          key,
		modelName:    modelName,
		http: &http.Client{
			Timeout:   600 * time.Second,
			Transport: sharedTransport,
		},
		promptManager: prompt.NewManager(),
		nativeSearch:  newNativeSearchState(providerName, domain.NativeWebSearchUndeclared, nil),
	}, nil
}

// SetNativeWebSearch declares whether this client's upstream has built-in web
// search, and which request field turns it on. It is the programmatic form of
// Provider.NativeWebSearch / NativeWebSearchOptions, for a client built with
// NewClient and handed to an agent with Builder.WithLLM. Call it before the
// client serves requests.
//
// options, when given, are sent inside the declared field: DashScope's
// search_options (e.g. {"forced_search": true}), extra keys of OpenAI's
// web_search_options, or the google_search tool's own config.
func (c *Client) SetNativeWebSearch(format domain.NativeWebSearchFormat, options ...map[string]interface{}) error {
	f, err := domain.ParseNativeWebSearchFormat(string(format))
	if err != nil {
		return err
	}
	var opts map[string]interface{}
	for _, o := range options {
		for k, v := range o {
			if opts == nil {
				opts = map[string]interface{}{}
			}
			opts[k] = v
		}
	}
	if err := domain.ValidateNativeWebSearchOptions(f, opts); err != nil {
		return err
	}
	c.nativeSearch = newNativeSearchState(c.providerName, f, opts)
	return nil
}

// GetProviderName returns the provider name
func (c *Client) GetProviderName() string {
	return c.providerName
}

func (c *Client) SetPromptManager(m *prompt.Manager) {
	c.promptManager = m
}

// GetModelName returns the model name
func (c *Client) GetModelName() string {
	return c.modelName
}

// GetBaseURL returns the base URL
func (c *Client) GetBaseURL() string {
	return c.baseURL
}

// IsFastModel reports whether the selected model is likely optimized for latency.
func (c *Client) IsFastModel() bool {
	return domain.IsFastModelName(c.modelName)
}

// Generate generates text
func (c *Client) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	if opts == nil {
		opts = &domain.GenerationOptions{}
	}

	reqBody := map[string]interface{}{
		"model": c.modelName,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	// Add the optional parameters.
	if opts.Temperature > 0 {
		reqBody["temperature"] = opts.Temperature
	}
	if opts.MaxTokens > 0 {
		reqBody["max_tokens"] = opts.MaxTokens
	}

	resp, err := c.doRequest(ctx, "/chat/completions", reqBody)
	if err != nil {
		return "", err
	}

	// Parse the response.
	var result struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	msg := result.Choices[0].Message
	content, _ := parseThinkContent(msg.Content, msg.ReasoningContent)
	return content, nil
}

// Stream generates text incrementally.
func (c *Client) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	if opts == nil {
		opts = &domain.GenerationOptions{}
	}

	reqBody := map[string]interface{}{
		"model": c.modelName,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream": true,
	}

	if opts.Temperature > 0 {
		reqBody["temperature"] = opts.Temperature
	}
	if opts.MaxTokens > 0 {
		reqBody["max_tokens"] = opts.MaxTokens
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error: %s", string(body))
	}

	// Handle the SSE stream.
	for {
		var line bytes.Buffer
		for {
			b := make([]byte, 1)
			_, err := resp.Body.Read(b)
			if err != nil {
				return nil
			}
			if b[0] == '\n' {
				break
			}
			if b[0] != '\r' {
				line.Write(b)
			}
		}

		lineStr := line.String()
		if lineStr == "" {
			continue
		}
		if lineStr == "data: [DONE]" {
			break
		}
		if len(lineStr) < 6 || lineStr[:6] != "data: " {
			continue
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(lineStr[6:]), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			callback(chunk.Choices[0].Delta.Content)
		}
	}

	return nil
}

func buildPoolGenerateWithToolsRequest(modelName string, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, searchFormat domain.NativeWebSearchFormat, searchOptions ...map[string]interface{}) map[string]interface{} {
	apiMessages := make([]map[string]interface{}, len(messages))
	for i, msg := range messages {
		apiMessages[i] = map[string]interface{}{
			"role": msg.Role,
			// Parts, when there are any, become a content array. Before this
			// the field was always msg.Content and every attached image was
			// dropped without a word — see multimodal.go.
			"content": messageContentForWire(msg),
		}
		if msg.ToolCalls != nil {
			apiToolCalls := make([]map[string]interface{}, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				argsBytes, _ := json.Marshal(tc.Function.Arguments)
				normalizedID := domain.NormalizeToolCallID(tc.ID)
				apiToolCalls[j] = map[string]interface{}{
					"id":      normalizedID,
					"call_id": normalizedID,
					"type":    tc.Type,
					"function": map[string]interface{}{
						"name":      tc.Function.Name,
						"arguments": string(argsBytes),
					},
				}
			}
			apiMessages[i]["tool_calls"] = apiToolCalls
		}
		if msg.ToolCallID != "" {
			normalizedID := domain.NormalizeToolCallID(msg.ToolCallID)
			apiMessages[i]["tool_call_id"] = normalizedID
			apiMessages[i]["call_id"] = normalizedID
		}
		if msg.ReasoningContent != "" {
			apiMessages[i]["reasoning_content"] = msg.ReasoningContent
		}
	}

	apiTools := make([]map[string]interface{}, len(tools))
	for i, tool := range tools {
		apiTools[i] = map[string]interface{}{
			"type":     "function",
			"function": tool.Function,
		}
	}

	// Cache markers go on last, once messages and tools are both built: they
	// rewrite two of them, and only a finished entry can be asked whether it
	// has anything to mark.
	if opts != nil && opts.PromptCache == domain.PromptCacheExplicit {
		markPromptCacheBreakpoints(apiMessages, apiTools)
	}

	reqBody := map[string]interface{}{
		"model":    modelName,
		"messages": apiMessages,
		"tools":    apiTools,
	}

	if opts != nil {
		if opts.Temperature > 0 {
			reqBody["temperature"] = opts.Temperature
		}
		if opts.MaxTokens > 0 {
			reqBody["max_tokens"] = opts.MaxTokens
		}
		if opts.ToolChoice != "" {
			switch opts.ToolChoice {
			case "auto", "required", "none":
				reqBody["tool_choice"] = opts.ToolChoice
			default:
				// Named tool form — OpenAI/DeepSeek both want a struct here.
				reqBody["tool_choice"] = map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name": opts.ToolChoice,
					},
				}
			}
		}
		// DeepSeek v4 (and reasoner-style models more broadly) accept a
		// top-level `thinking` field: `{"type":"disabled"}` skips
		// chain-of-thought, dropping latency a lot for tool-heavy runs.
		// Other providers either ignore the field or 400 — the runtime
		// only sets this when a caller explicitly opts in via
		// WithThinking() / opts.Thinking, so non-DeepSeek paths default
		// to "leave it alone".
		if opts.Thinking != nil && opts.Thinking.Type != "" {
			reqBody["thinking"] = map[string]interface{}{
				"type": opts.Thinking.Type,
			}
		}
		if rf := buildResponseFormatBody(opts.ResponseFormat); rf != nil {
			reqBody["response_format"] = rf
		}
		if domain.UsesNativeWebSearch(opts.WebSearchMode) {
			var extra map[string]interface{}
			if len(searchOptions) > 0 {
				extra = searchOptions[0]
			}
			applyNativeWebSearch(reqBody, searchFormat, opts.WebSearchContextSize, extra)
		}
	}

	return reqBody
}

// applyNativeWebSearch adds the field that turns on the upstream's own web
// search. A declared format sends exactly its field, with the declared
// options inside it; an undeclared provider gets every field we know, and
// applyPoolRetryFallbacks drops them all if the upstream rejects one.
func applyNativeWebSearch(reqBody map[string]interface{}, format domain.NativeWebSearchFormat, contextSize string, extra map[string]interface{}) {
	openAI := func(extra map[string]interface{}) {
		o := map[string]interface{}{
			"search_context_size": domain.NormalizeWebSearchContextSize(contextSize),
		}
		for k, v := range extra {
			o[k] = v
		}
		reqBody["web_search_options"] = o
	}
	switch format {
	case domain.NativeWebSearchNone:
	case domain.NativeWebSearchOpenAI:
		openAI(extra)
	case domain.NativeWebSearchDashScope:
		reqBody["enable_search"] = true
		if len(extra) > 0 {
			reqBody["search_options"] = extra
		}
	case domain.NativeWebSearchGoogle:
		// A built-in tool, listed beside the function tools rather than
		// replacing them.
		cfg := map[string]interface{}{}
		for k, v := range extra {
			cfg[k] = v
		}
		existing, _ := reqBody["tools"].([]map[string]interface{})
		reqBody["tools"] = append(existing, map[string]interface{}{"google_search": cfg})
	default:
		// Undeclared. DashScope ignores web_search_options and triggers
		// retrieval via the non-standard enable_search flag instead, so send
		// both; providers ignore the one they don't know.
		openAI(nil)
		reqBody["enable_search"] = true
	}
}

func shouldRetryPoolWithoutNativeWebSearch(opts *domain.GenerationOptions, err error) bool {
	if opts == nil || domain.NormalizeWebSearchMode(opts.WebSearchMode) != domain.WebSearchModeAuto || err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "web_search_options") ||
		strings.Contains(msg, "enable_search") ||
		strings.Contains(msg, "search_options") ||
		strings.Contains(msg, "google_search") ||
		// Gemini refuses a built-in tool beside function tools unless a
		// tool_config flag is set, which OpenAI-compatible gateways do not
		// all pass through.
		strings.Contains(msg, "built-in tools") ||
		strings.Contains(msg, "server_side_tool_invocations") ||
		strings.Contains(msg, "web search") ||
		strings.Contains(msg, "unsupported parameter") ||
		strings.Contains(msg, "unknown field")
}

// shouldRetryPoolWithoutToolChoice reports whether the upstream rejected
// the tool_choice parameter (DeepSeek's reasoner models reject any
// tool_choice value when chained with reasoning_content; some OpenAI
// o-series variants reject named-tool choice). On hit, retry once with
// tool_choice stripped — the model can still call tools organically.
func shouldRetryPoolWithoutToolChoice(opts *domain.GenerationOptions, err error) bool {
	if opts == nil || strings.TrimSpace(opts.ToolChoice) == "" || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "tool_choice") {
		return false
	}
	return strings.Contains(msg, "does not support") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "invalid")
}

// shouldRetryPoolWithoutResponseFormat reports whether the upstream
// rejected the response_format parameter. Older OpenAI-compat endpoints
// and some local servers either ignore it (fine) or reject it (the case
// we care about here). On hit, strip the field and retry; the
// structured-output lint will still validate the answer post-hoc.
func shouldRetryPoolWithoutResponseFormat(opts *domain.GenerationOptions, err error) bool {
	if opts == nil || opts.ResponseFormat == nil || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "response_format") && !strings.Contains(msg, "json_schema") {
		return false
	}
	return strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "does not support") ||
		strings.Contains(msg, "invalid") ||
		strings.Contains(msg, "unavailable") || // DeepSeek: "response_format type is unavailable now"
		strings.Contains(msg, "unknown")
}

// applyPoolRetryFallbacks composes the compatibility fallbacks (drop
// native web search, drop tool_choice, drop response_format). Returns a
// new options struct when something applies, nil otherwise.
func applyPoolRetryFallbacks(opts *domain.GenerationOptions, err error) *domain.GenerationOptions {
	if opts == nil || err == nil {
		return nil
	}
	cur := *opts
	changed := false
	if shouldRetryPoolWithoutNativeWebSearch(&cur, err) {
		cur.WebSearchMode = domain.WebSearchModeMCP
		changed = true
	}
	if shouldRetryPoolWithoutToolChoice(&cur, err) {
		cur.ToolChoice = ""
		changed = true
	}
	if shouldRetryPoolWithoutResponseFormat(&cur, err) {
		cur.ResponseFormat = nil
		changed = true
	}
	if shouldRetryPoolWithoutPromptCache(&cur, err) {
		cur.PromptCache = domain.PromptCacheOff
		changed = true
	}
	if !changed {
		return nil
	}
	return &cur
}

// buildResponseFormatBody renders a domain.ResponseFormat into the
// shape expected by OpenAI-compat /chat/completions. Returns nil when
// the spec is empty so the field is omitted from the request body.
func buildResponseFormatBody(rf *domain.ResponseFormat) map[string]interface{} {
	if rf == nil || rf.Type == "" {
		return nil
	}
	switch rf.Type {
	case "json_object":
		return map[string]interface{}{"type": "json_object"}
	case "json_schema":
		js := map[string]interface{}{}
		if rf.Name != "" {
			js["name"] = rf.Name
		}
		if rf.Strict {
			js["strict"] = true
		}
		if len(rf.Schema) > 0 {
			// Send the schema as a structured object, not a JSON string.
			var schemaObj interface{}
			if err := json.Unmarshal(rf.Schema, &schemaObj); err == nil {
				js["schema"] = schemaObj
			}
		}
		return map[string]interface{}{
			"type":        "json_schema",
			"json_schema": js,
		}
	}
	return nil
}

// GenerateWithTools generates a response with tools available.
func (c *Client) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	if opts == nil {
		opts = &domain.GenerationOptions{}
	}
	reqBody := buildPoolGenerateWithToolsRequest(c.modelName, messages, tools, opts, c.nativeSearch.sendFormat(), c.nativeSearch.sendOptions())

	resp, err := c.doRequest(ctx, "/chat/completions", reqBody)
	// Iterate the compatibility fallbacks: a provider can reject several
	// params in sequence (e.g. DeepSeek thinking mode rejects tool_choice on
	// the first call, then response_format on the next). Each pass strips
	// whatever the current error complains about and retries, until the call
	// succeeds or nothing more can be dropped.
	curOpts := opts
	for attempts := 0; err != nil && attempts < 3; attempts++ {
		retryOpts := applyPoolRetryFallbacks(curOpts, err)
		if retryOpts == nil {
			break
		}
		curOpts = retryOpts
		resp, err = c.doRequest(ctx, "/chat/completions", buildPoolGenerateWithToolsRequest(c.modelName, messages, tools, curOpts, c.nativeSearch.sendFormat(), c.nativeSearch.sendOptions()))
	}
	if err != nil {
		return nil, fmt.Errorf("request failed (model=%s): %w", c.modelName, err)
	}
	c.recordNativeWebSearch(opts, curOpts, resp)

	var result struct {
		Choices []struct {
			Message struct {
				Content          string            `json:"content"`
				Role             string            `json:"role"`
				ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
				ReasoningContent string            `json:"reasoning_content"`
				// Non-text output. A model asked to draw returns content
				// null and the picture here; without this the loop read an
				// empty answer and the lint layer called it a refusal.
				Images []wireOutputImage `json:"images,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response (model=%s): %w", c.modelName, err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response (model=%s)", c.modelName)
	}

	choice := result.Choices[0]
	cleanContent, reasoning := parseThinkContent(choice.Message.Content, choice.Message.ReasoningContent)
	if reasoning != "" {
		c.reasons.Store(true)
	}
	response := &domain.GenerationResult{
		Content:          cleanContent,
		ReasoningContent: reasoning,
		Parts:            outputPartsFromMessage(choice.Message.Images),
		FinishReason:     choice.FinishReason,
		Usage:            parsePoolUsage(resp),
	}

	// Parse the tool calls.
	if len(choice.Message.ToolCalls) > 0 {
		response.ToolCalls = make([]domain.ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			var toolCall struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if err := json.Unmarshal(tc, &toolCall); err == nil {
				response.ToolCalls[i] = domain.ToolCall{
					ID:   toolCall.ID,
					Type: toolCall.Type,
					Function: domain.FunctionCall{
						Name: toolCall.Function.Name,
					},
				}
				// Parse arguments as map
				if toolCall.Function.Arguments != "" {
					var args map[string]interface{}
					if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err == nil {
						response.ToolCalls[i].Function.Arguments = args
					}
				}
			}
		}
	}

	// Some proxies (e.g. api.132999.xyz routing Claude through AWS Bedrock) wrap
	// the raw Bedrock EventStream binary inside the OpenAI JSON content field
	// instead of placing tool calls in the tool_calls array.  Detect this and
	// extract tool calls from the binary so callers see normal ToolCall objects.
	if len(response.ToolCalls) == 0 && isBedrockEventStream(choice.Message.Content) {
		if extracted := extractBedrockToolCalls(choice.Message.Content); len(extracted) > 0 {
			response.ToolCalls = extracted
			response.Content = "" // clear the binary garbage from content
		}
	}
	// DeepSeek writing its own call markup as text (see dsml.go).
	if len(response.ToolCalls) == 0 {
		if calls, rest := extractDSMLToolCalls(response.Content); len(calls) > 0 {
			response.ToolCalls, response.Content = calls, rest
		}
	}

	return response, nil
}

// GenerateStructured generates structured (JSON) output.
//
// Two things about real providers shape it. Some reject response_format
// json_schema outright (DeepSeek: "This response_format type is unavailable
// now"); that answer is remembered, so later calls go straight to the prompt
// form instead of paying a doomed request each time. And a model that reasons
// before it writes can spend a small max_tokens on reasoning alone —
// deepseek-v4-flash used 356–1437 reasoning tokens on a one-line
// classification capped at 400, and returned finish_reason "length" with no
// content, which read as "the model gave no JSON". See structuredCompletion.
func (c *Client) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if opts == nil {
		opts = &domain.GenerationOptions{}
	}
	if c.structuredSchemaRejected.Load() {
		return c.generateStructuredFallback(ctx, prompt, schema, opts)
	}

	reqBody := map[string]interface{}{
		"model": c.modelName,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"response_format": map[string]interface{}{
			"type": "json_schema",
			"json_schema": map[string]interface{}{
				"name":   "response",
				"schema": schema,
				// strict:true intentionally omitted — many OpenAI-compatible providers
				// do not support it and return an error or empty content.
			},
		},
	}
	if opts.Temperature > 0 {
		reqBody["temperature"] = opts.Temperature
	}
	setThinking(reqBody, opts)

	content, err := c.structuredCompletion(ctx, reqBody, opts.MaxTokens, opts.NoReasoning)
	if err != nil {
		if isResponseFormatRejection(err) {
			c.structuredSchemaRejected.Store(true)
		}
		// Fallback: provider rejected response_format, retry as plain JSON prompt.
		return c.generateStructuredFallback(ctx, prompt, schema, opts)
	}

	raw := extractPoolJSON(content)
	if raw == "" {
		// Provider returned empty content; fall back to plain JSON prompt.
		return c.generateStructuredFallback(ctx, prompt, schema, opts)
	}

	return &domain.StructuredResult{
		Raw:   raw,
		Valid: true,
	}, nil
}

// generateStructuredFallback retries without response_format, asking the model
// to output valid JSON in the prompt text instead.
func (c *Client) generateStructuredFallback(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	schemaBytes, _ := json.Marshal(schema)
	augmented := fmt.Sprintf(
		"%s\n\nRespond with valid JSON only (no markdown, no explanation) matching this schema:\n%s",
		prompt, string(schemaBytes),
	)

	reqBody := map[string]interface{}{
		"model": c.modelName,
		"messages": []map[string]string{
			{"role": "user", "content": augmented},
		},
	}
	maxTokens, noReasoning := 0, false
	if opts != nil {
		noReasoning = opts.NoReasoning
		if opts.Temperature > 0 {
			reqBody["temperature"] = opts.Temperature
		}
		maxTokens = opts.MaxTokens
		setThinking(reqBody, opts)
	}

	content, err := c.structuredCompletion(ctx, reqBody, maxTokens, noReasoning)
	if err != nil {
		return nil, fmt.Errorf("structured fallback request failed: %w", err)
	}

	raw := extractPoolJSON(content)
	if raw == "" {
		return nil, fmt.Errorf("empty JSON content in fallback response")
	}

	return &domain.StructuredResult{
		Raw:   raw,
		Valid: true,
	}, nil
}

// structuredEscalations bounds how often a truncated structured reply is
// re-asked with a larger budget; each step multiplies it by four.
const structuredEscalations = 2

// structuredCompletion sends one chat completion and returns its content.
// A reply cut off by max_tokens with nothing written is re-asked with four
// times the budget, at most structuredEscalations times: the caller's cap
// was sized for the answer, and on a reasoning model the reasoning comes out
// of the same budget first. A truncated reply that did write something is
// returned as is — the budget reached the answer, and the caller judges it.
//
// noReasoning is a caller waiting on the answer (GenerationOptions
// .NoReasoning). The memory navigator's pick of five ids from a nine-line
// index, in front of every turn, took 5–8s on deepseek-flash, 1–2k tokens of
// it reasoning, and 1.6s with reasoning off, choosing the same ids. Once this
// upstream has shown it reasons, such a call asks it not to; an upstream that
// refuses the field is asked again without it and not sent it again. No
// model is named: what turns this on is a reply that reasoned. It is not the
// default because it is not free: memory extraction run without reasoning
// filed the assistant's own offer as the person's wish and got a date wrong,
// and extraction runs after the reply, where nobody waits on it.
func (c *Client) structuredCompletion(ctx context.Context, reqBody map[string]interface{}, maxTokens int, noReasoning bool) (string, error) {
	_, explicit := reqBody["thinking"]
	thinkingOff := noReasoning && !explicit && c.reasons.Load() && !c.thinkingOffRejected.Load()
	if thinkingOff {
		reqBody["thinking"] = map[string]interface{}{"type": "disabled"}
	}
	for step := 0; ; step++ {
		if maxTokens > 0 {
			reqBody["max_tokens"] = maxTokens
		}
		resp, err := c.doRequest(ctx, "/chat/completions", reqBody)
		if err != nil && thinkingOff && isThinkingRejection(err) {
			c.thinkingOffRejected.Store(true)
			thinkingOff = false
			delete(reqBody, "thinking")
			resp, err = c.doRequest(ctx, "/chat/completions", reqBody)
		}
		if err != nil {
			return "", err
		}
		var result struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return "", fmt.Errorf("failed to parse response: %w", err)
		}
		if len(result.Choices) == 0 {
			return "", fmt.Errorf("no choices in response")
		}
		choice := result.Choices[0]
		if choice.Message.ReasoningContent != "" || strings.Contains(choice.Message.Content, "<think>") {
			c.reasons.Store(true)
		}
		truncatedEmpty := choice.FinishReason == "length" && strings.TrimSpace(choice.Message.Content) == ""
		if !truncatedEmpty || maxTokens <= 0 || step >= structuredEscalations {
			return choice.Message.Content, nil
		}
		maxTokens *= 4
	}
}

// isResponseFormatRejection reports whether a provider refused the
// response_format field itself, as opposed to failing for another reason.
// setThinking carries a caller's explicit reasoning choice into a structured
// request; without one, structuredCompletion decides.
func setThinking(reqBody map[string]interface{}, opts *domain.GenerationOptions) {
	if opts != nil && opts.Thinking != nil && opts.Thinking.Type != "" {
		reqBody["thinking"] = map[string]interface{}{"type": opts.Thinking.Type}
	}
}

// isThinkingRejection reports an upstream refusing the thinking field.
func isThinkingRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 400") && strings.Contains(msg, "thinking")
}

func isResponseFormatRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 400") && strings.Contains(msg, "response_format")
}

// extractPoolJSON strips markdown code fences and finds the first JSON object/array.
// parseThinkContent extracts <think>...</think> blocks from content and moves
// them into reasoningContent. Some reasoning models (DeepSeek-R1, QwQ, etc.)
// embed chain-of-thought in content tags instead of using a separate API field.
// Returns (cleanContent, updatedReasoningContent).
func parseThinkContent(content, reasoningContent string) (string, string) {
	if !strings.Contains(content, "<think>") {
		return content, reasoningContent
	}

	var thinkParts []string
	remaining := content
	for {
		start := strings.Index(remaining, "<think>")
		if start == -1 {
			break
		}
		end := strings.Index(remaining, "</think>")
		if end == -1 {
			// Unclosed tag — treat rest as think content and stop.
			thinkParts = append(thinkParts, strings.TrimSpace(remaining[start+len("<think>"):]))
			remaining = remaining[:start]
			break
		}
		thinkParts = append(thinkParts, strings.TrimSpace(remaining[start+len("<think>"):end]))
		remaining = remaining[:start] + remaining[end+len("</think>"):]
	}

	newContent := strings.TrimSpace(remaining)
	if len(thinkParts) == 0 {
		return newContent, reasoningContent
	}
	think := strings.Join(thinkParts, "\n")
	newReasoning := think
	if reasoningContent != "" {
		newReasoning = reasoningContent + "\n" + think
	}
	// Some models place the actual answer as the final line of the think block
	// with nothing outside it (e.g. when MaxTokens cuts the response mid-chain).
	// If the content is empty, use the last non-empty paragraph as a best-effort answer.
	if newContent == "" && think != "" {
		paragraphs := strings.Split(think, "\n\n")
		for i := len(paragraphs) - 1; i >= 0; i-- {
			if p := strings.TrimSpace(paragraphs[i]); p != "" {
				newContent = p
				break
			}
		}
	}
	return newContent, newReasoning
}

func extractPoolJSON(s string) string {
	for _, fence := range []string{"```json", "```"} {
		if idx := strings.Index(s, fence); idx != -1 {
			s = s[idx+len(fence):]
			if end := strings.Index(s, "```"); end != -1 {
				s = s[:end]
			}
		}
	}
	s = strings.TrimSpace(s)
	for i, ch := range s {
		if ch == '{' || ch == '[' {
			return s[i:]
		}
	}
	return s
}

// RecognizeIntent classifies the intent of an utterance.
func (c *Client) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	data := map[string]interface{}{
		"Query":   request,
		"Intents": "question, action, analysis, search, calculation, status, unknown",
	}

	rendered, err := c.promptManager.Render(prompt.RouterIntentAnalysis, data)
	if err != nil {
		rendered = fmt.Sprintf("Analyze intent for: %s", request)
	}

	result, err := c.Generate(ctx, rendered, &domain.GenerationOptions{Temperature: 0.1})
	if err != nil {
		return nil, err
	}

	var intentResult domain.IntentResult
	if err := json.Unmarshal([]byte(result), &intentResult); err != nil {
		// Fall back to the default value on failure.
		return &domain.IntentResult{
			Intent:     domain.IntentUnknown,
			Confidence: 0.5,
			KeyVerbs:   []string{},
			Entities:   []string{},
		}, nil
	}

	return &intentResult, nil
}

// Embed vectorizes one text (returns []float64 to satisfy domain.Embedder).
func (c *Client) Embed(ctx context.Context, texts []string) ([]float64, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("no texts provided")
	}

	// OpenAI embedding API returns the first embedding's vector
	if len(texts) == 1 {
		return c.embedSingle(ctx, texts[0])
	}

	// For multiple texts, return the first one's vector
	// (This is a simplification - in production you might want to handle this differently)
	return c.embedSingle(ctx, texts[0])
}

// EmbedMultiple vectorizes several texts, returning one vector per text.
func (c *Client) EmbedMultiple(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("no texts provided")
	}

	reqBody := map[string]interface{}{
		"model": c.modelName,
		"input": texts,
	}

	resp, err := c.doRequest(ctx, "/embeddings", reqBody)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	embeddings := make([][]float64, len(result.Data))
	for _, item := range result.Data {
		vec := make([]float64, len(item.Embedding))
		for i, v := range item.Embedding {
			vec[i] = float64(v)
		}
		embeddings[item.Index] = vec
	}

	return embeddings, nil
}

// embedSingle vectorizes a single text.
func (c *Client) embedSingle(ctx context.Context, text string) ([]float64, error) {
	reqBody := map[string]interface{}{
		"model": c.modelName,
		"input": []string{text},
	}

	resp, err := c.doRequest(ctx, "/embeddings", reqBody)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	// Protect against empty embedding vectors
	if len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedding vector is empty (length 0)")
	}

	// Convert []float32 to []float64
	vec := make([]float64, len(result.Data[0].Embedding))
	for i, v := range result.Data[0].Embedding {
		vec[i] = float64(v)
	}

	return vec, nil
}

// Health reports whether the client is reachable.
func (c *Client) Health(ctx context.Context) error {
	// Check if this is an embedding model by trying a simple Generate first
	// If Generate fails, try embedding
	_, err := c.Generate(ctx, "hi", &domain.GenerationOptions{MaxTokens: 1})
	if err != nil {
		// If Generate fails, this might be an embedding-only model
		// Try embedding as fallback
		_, embedErr := c.embedSingle(ctx, "health")
		if embedErr != nil {
			return fmt.Errorf("both generate and embed failed: generate=%v, embed=%w", err, embedErr)
		}
	}
	return nil
}

// Close releases the client.
func (c *Client) Close() error {
	c.http.CloseIdleConnections()
	return nil
}

// doRequest performs the HTTP request.
func (c *Client) doRequest(ctx context.Context, path string, body interface{}) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// isBedrockEventStream reports whether s looks like an AWS Bedrock EventStream
// binary blob that was placed inside an OpenAI-compatible JSON content field.
// Bedrock EventStream frames start with a 4-byte big-endian total-length header
// followed by a 4-byte headers-length header, both of which are small positive
// integers.  The frame also always contains the ASCII string ":event-type".
func isBedrockEventStream(s string) bool {
	if len(s) < 12 {
		return false
	}
	// The first byte is \x00 (high byte of total-length), which is never valid
	// UTF-8 text or JSON.
	if s[0] != 0x00 {
		return false
	}
	// Presence of the EventStream header-name marker is a strong signal.
	return bytes.Contains([]byte(s), []byte(":event-type"))
}

// extractBedrockToolCalls extracts tool calls from AWS Bedrock EventStream content
// by scanning for JSON objects containing toolUseId. This approach works regardless
// of JSON escaping or byte offsets.
func extractBedrockToolCalls(content string) []domain.ToolCall {
	data := []byte(content)
	seen := make(map[string]bool)
	var calls []domain.ToolCall

	for i := 0; i < len(data); i++ {
		if data[i] != '{' {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(data[i:]))
		var event struct {
			Name      string `json:"name"`
			ToolUseID string `json:"toolUseId"`
			Stop      bool   `json:"stop"`
			Input     string `json:"input"`
		}
		if err := dec.Decode(&event); err != nil {
			continue
		}
		if event.ToolUseID == "" || event.Stop {
			continue
		}
		if seen[event.ToolUseID] {
			continue
		}
		seen[event.ToolUseID] = true
		tc := domain.ToolCall{
			ID:       event.ToolUseID,
			Type:     "function",
			Function: domain.FunctionCall{Name: event.Name},
		}
		if event.Input != "" {
			var args map[string]interface{}
			if json.Unmarshal([]byte(event.Input), &args) == nil {
				tc.Function.Arguments = args
			}
		}
		calls = append(calls, tc)
	}
	return calls
}
