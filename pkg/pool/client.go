package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/prompt"
)

// Client OpenAI兼容的LLM/Embedding Client
type Client struct {
	providerName  string
	baseURL       string
	key           string
	modelName     string
	http          *http.Client
	promptManager *prompt.Manager
}

// NewClient 创建新client
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
			Timeout: 600 * time.Second,
		},
		promptManager: prompt.NewManager(),
	}, nil
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

	// 添加可选参数
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

	// 解析响应
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

	return result.Choices[0].Message.Content, nil
}

// Stream 流式生成
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

	// 处理SSE流
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

func buildPoolGenerateWithToolsRequest(modelName string, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) map[string]interface{} {
	apiMessages := make([]map[string]interface{}, len(messages))
	for i, msg := range messages {
		apiMessages[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if msg.ToolCalls != nil {
			apiToolCalls := make([]map[string]interface{}, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				argsBytes, _ := json.Marshal(tc.Function.Arguments)
				apiToolCalls[j] = map[string]interface{}{
					"id":   domain.NormalizeToolCallID(tc.ID),
					"type": tc.Type,
					"function": map[string]interface{}{
						"name":      tc.Function.Name,
						"arguments": string(argsBytes),
					},
				}
			}
			apiMessages[i]["tool_calls"] = apiToolCalls
		}
		if msg.ToolCallID != "" {
			apiMessages[i]["tool_call_id"] = domain.NormalizeToolCallID(msg.ToolCallID)
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
			reqBody["tool_choice"] = opts.ToolChoice
		}
		if domain.UsesNativeWebSearch(opts.WebSearchMode) {
			reqBody["web_search_options"] = map[string]interface{}{
				"search_context_size": domain.NormalizeWebSearchContextSize(opts.WebSearchContextSize),
			}
		}
	}

	return reqBody
}

func shouldRetryPoolWithoutNativeWebSearch(opts *domain.GenerationOptions, err error) bool {
	if opts == nil || domain.NormalizeWebSearchMode(opts.WebSearchMode) != domain.WebSearchModeAuto || err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "web_search_options") ||
		strings.Contains(msg, "web search") ||
		strings.Contains(msg, "unsupported parameter") ||
		strings.Contains(msg, "unknown field")
}

// GenerateWithTools 使用工具生成
func (c *Client) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	if opts == nil {
		opts = &domain.GenerationOptions{}
	}
	reqBody := buildPoolGenerateWithToolsRequest(c.modelName, messages, tools, opts)

	resp, err := c.doRequest(ctx, "/chat/completions", reqBody)
	if err != nil {
		if shouldRetryPoolWithoutNativeWebSearch(opts, err) {
			retryOpts := *opts
			retryOpts.WebSearchMode = domain.WebSearchModeMCP
			resp, err = c.doRequest(ctx, "/chat/completions", buildPoolGenerateWithToolsRequest(c.modelName, messages, tools, &retryOpts))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("request failed (model=%s): %w", c.modelName, err)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content          string            `json:"content"`
				Role             string            `json:"role"`
				ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
				ReasoningContent string            `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response (model=%s): %w", c.modelName, err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response (model=%s)", c.modelName)
	}

	choice := result.Choices[0]
	response := &domain.GenerationResult{
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
	}

	// 解析tool calls
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

	return response, nil
}

// StreamWithTools 流式工具调用
func (c *Client) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	// 简化实现：先获取完整结果再流式回调
	result, err := c.GenerateWithTools(ctx, messages, tools, opts)
	if err != nil {
		return err
	}

	return callback(result)
}

// GenerateStructured 结构化生成
func (c *Client) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if opts == nil {
		opts = &domain.GenerationOptions{}
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
	if opts.MaxTokens > 0 {
		reqBody["max_tokens"] = opts.MaxTokens
	}

	resp, err := c.doRequest(ctx, "/chat/completions", reqBody)
	if err != nil {
		// Fallback: provider rejected response_format, retry as plain JSON prompt.
		return c.generateStructuredFallback(ctx, prompt, schema, opts)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	raw := extractPoolJSON(result.Choices[0].Message.Content)
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
	if opts != nil {
		if opts.Temperature > 0 {
			reqBody["temperature"] = opts.Temperature
		}
		if opts.MaxTokens > 0 {
			reqBody["max_tokens"] = opts.MaxTokens
		}
	}

	resp, err := c.doRequest(ctx, "/chat/completions", reqBody)
	if err != nil {
		return nil, fmt.Errorf("structured fallback request failed: %w", err)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse fallback response: %w", err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in fallback response")
	}

	raw := extractPoolJSON(result.Choices[0].Message.Content)
	if raw == "" {
		return nil, fmt.Errorf("empty JSON content in fallback response")
	}

	return &domain.StructuredResult{
		Raw:   raw,
		Valid: true,
	}, nil
}

// extractPoolJSON strips markdown code fences and finds the first JSON object/array.
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

// RecognizeIntent 意图识别
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
		// 失败时返回默认值
		return &domain.IntentResult{
			Intent:     domain.IntentUnknown,
			Confidence: 0.5,
			KeyVerbs:   []string{},
			Entities:   []string{},
		}, nil
	}

	return &intentResult, nil
}

// Embed 向量化 (返回[]float64以兼容domain.Embedder接口)
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

// EmbedMultiple 向量化多个文本，返回多个向量
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

// embedSingle 向量化单个文本
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

// Health 健康检查
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

// Close 关闭client
func (c *Client) Close() error {
	c.http.CloseIdleConnections()
	return nil
}

// doRequest 执行HTTP请求
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
