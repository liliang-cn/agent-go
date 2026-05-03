package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// OpenAILLMProvider implements LLMProvider for OpenAI-compatible services
type OpenAILLMProvider struct {
	client        openai.Client
	config        *domain.OpenAIProviderConfig
	promptManager *prompt.Manager
}

// OpenAIRealtimeSession handles bidirectional WebSocket communication
type OpenAIRealtimeSession struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// ClassifyError wraps an error with a specific type if it matches known patterns.
// This enables error withholding and recovery in the agent.
func ClassifyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context_length") ||
		strings.Contains(msg, "prompt too long") ||
		strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "maximum context"):
		return fmt.Errorf("%w: %v", domain.ErrContextTooLong, err)
	case strings.Contains(msg, "max_tokens") ||
		strings.Contains(msg, "maximum output tokens") ||
		strings.Contains(msg, "output token") ||
		strings.Contains(msg, "response too long"):
		return fmt.Errorf("%w: %v", domain.ErrMaxOutputTokens, err)
	case strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "too many requests"):
		return fmt.Errorf("%w: %v", domain.ErrRateLimited, err)
	}
	return err
}

func (s *OpenAIRealtimeSession) Send(ctx context.Context, msg domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// OpenAI Realtime API uses a specific JSON structure
	// This is a simplified version of response.create event
	event := map[string]interface{}{
		"type": "response.create",
		"response": map[string]interface{}{
			"instructions": msg.Content,
		},
	}

	return s.conn.WriteJSON(event)
}

func (s *OpenAIRealtimeSession) Receive(ctx context.Context) (*domain.GenerationResult, error) {
	for {
		var event map[string]interface{}
		err := s.conn.ReadJSON(&event)
		if err != nil {
			return nil, err
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "response.text.delta":
			delta, _ := event["delta"].(string)
			return &domain.GenerationResult{
				Content:  delta,
				Finished: false,
			}, nil
		case "response.done":
			return &domain.GenerationResult{
				Content:  "",
				Finished: true,
			}, nil
		case "error":
			errorMsg, _ := event["error"].(map[string]interface{})["message"].(string)
			return nil, fmt.Errorf("OpenAI Realtime Error: %s", errorMsg)
		}
	}
}

func (s *OpenAIRealtimeSession) Close() error {
	return s.conn.Close()
}

// NewSession creates a new persistent WebSocket session
func (p *OpenAILLMProvider) NewSession(ctx context.Context, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (domain.RealtimeSession, error) {
	url := "wss://api.openai.com/v1/realtime?model=" + p.config.LLMModel

	headers := make(http.Header)
	headers.Add("Authorization", "Bearer "+p.config.APIKey)
	headers.Add("OpenAI-Beta", "realtime=v1")

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to dial openai realtime websocket: %w", err)
	}

	return &OpenAIRealtimeSession{conn: conn}, nil
}

// NewOpenAILLMProvider creates a new OpenAI LLM provider
func NewOpenAILLMProvider(config *domain.OpenAIProviderConfig) (domain.LLMProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	opts := []option.RequestOption{}

	// For local services like Ollama, API key might not be required
	// Use empty string for API key if not provided, let the provider handle authentication
	if config.APIKey != "" {
		opts = append(opts, option.WithAPIKey(config.APIKey))
	} else {
		// Use a placeholder for local services that don't require API keys
		opts = append(opts, option.WithAPIKey("ollama"))
	}

	if config.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(config.BaseURL))
	}

	return &OpenAILLMProvider{
		client:        openai.NewClient(opts...),
		config:        config,
		promptManager: prompt.NewManager(),
	}, nil
}

// SetPromptManager sets a custom prompt manager
func (p *OpenAILLMProvider) SetPromptManager(m *prompt.Manager) {
	p.promptManager = m
}

// ProviderType returns the provider type
func (p *OpenAILLMProvider) ProviderType() domain.ProviderType {
	return domain.ProviderOpenAI
}

// UsageModel returns the configured chat model for usage accounting.
func (p *OpenAILLMProvider) UsageModel() string {
	return p.config.LLMModel
}

// IsFastModel reports whether the configured chat model is latency-optimized.
func (p *OpenAILLMProvider) IsFastModel() bool {
	if p == nil || p.config == nil {
		return false
	}
	return domain.IsFastModelName(p.config.LLMModel)
}

// toOpenAIMessages converts domain messages to the OpenAI API format
func toOpenAIMessages(messages []domain.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	openAIMessages := make([]openai.ChatCompletionMessageParamUnion, len(messages))
	for i, msg := range messages {
		switch msg.Role {
		case "user":
			if len(msg.Parts) > 0 {
				parts, err := toOpenAIUserContentParts(msg)
				if err != nil {
					return nil, err
				}
				openAIMessages[i] = openai.UserMessage(parts)
			} else {
				openAIMessages[i] = openai.UserMessage(msg.Content)
			}
		case "system":
			if len(msg.Parts) > 0 {
				parts, err := toOpenAITextParts(msg, "system")
				if err != nil {
					return nil, err
				}
				openAIMessages[i] = openai.SystemMessage(parts)
			} else {
				openAIMessages[i] = openai.SystemMessage(msg.Content)
			}
		case "tool":
			if len(msg.Parts) > 0 {
				parts, err := toOpenAITextParts(msg, "tool")
				if err != nil {
					return nil, err
				}
				openAIMessages[i] = openai.ToolMessage(parts, domain.NormalizeToolCallID(msg.ToolCallID))
			} else {
				openAIMessages[i] = openai.ToolMessage(msg.Content, domain.NormalizeToolCallID(msg.ToolCallID))
			}
		case "assistant":
			assistantMsg := openai.ChatCompletionAssistantMessageParam{}
			if len(msg.Parts) > 0 {
				parts, err := toOpenAIAssistantTextParts(msg)
				if err != nil {
					return nil, err
				}
				assistantMsg.Content.OfArrayOfContentParts = parts
			} else {
				assistantMsg.Content.OfString = param.NewOpt(msg.Content)
			}
			if len(msg.ToolCalls) > 0 {
				assistantMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallUnionParam, len(msg.ToolCalls))
				for j, tc := range msg.ToolCalls {
					args, err := json.Marshal(tc.Function.Arguments)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal tool call arguments: %w", err)
					}
					assistantMsg.ToolCalls[j] = openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID: domain.NormalizeToolCallID(tc.ID),
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Function.Name,
								Arguments: string(args),
							},
						},
					}
				}
			}
			openAIMessages[i] = openai.ChatCompletionMessageParamUnion{OfAssistant: &assistantMsg}
		default:
			return nil, fmt.Errorf("unknown message role: %s", msg.Role)
		}
	}
	return openAIMessages, nil
}

func toOpenAIUserContentParts(msg domain.Message) ([]openai.ChatCompletionContentPartUnionParam, error) {
	parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(msg.Parts)+1)
	if strings.TrimSpace(msg.Content) != "" {
		parts = append(parts, openai.TextContentPart(msg.Content))
	}
	for idx, part := range msg.Parts {
		switch normalizeMessagePartType(part) {
		case domain.MessagePartTypeText:
			parts = append(parts, openai.TextContentPart(part.Text))
		case domain.MessagePartTypeImage:
			imageURL, err := toOpenAIImageURLParam(part)
			if err != nil {
				return nil, fmt.Errorf("user message part %d: %w", idx, err)
			}
			parts = append(parts, openai.ImageContentPart(imageURL))
		default:
			return nil, fmt.Errorf("user message part %d: unsupported part type %q", idx, part.Type)
		}
	}
	return parts, nil
}

func toOpenAITextParts(msg domain.Message, role string) ([]openai.ChatCompletionContentPartTextParam, error) {
	parts := make([]openai.ChatCompletionContentPartTextParam, 0, len(msg.Parts)+1)
	if strings.TrimSpace(msg.Content) != "" {
		parts = append(parts, openai.ChatCompletionContentPartTextParam{Text: msg.Content})
	}
	for idx, part := range msg.Parts {
		switch normalizeMessagePartType(part) {
		case domain.MessagePartTypeText:
			parts = append(parts, openai.ChatCompletionContentPartTextParam{Text: part.Text})
		case domain.MessagePartTypeImage:
			return nil, fmt.Errorf("%s message part %d: image parts are only supported for user messages", role, idx)
		default:
			return nil, fmt.Errorf("%s message part %d: unsupported part type %q", role, idx, part.Type)
		}
	}
	return parts, nil
}

func toOpenAIAssistantTextParts(msg domain.Message) ([]openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion, error) {
	textParts, err := toOpenAITextParts(msg, "assistant")
	if err != nil {
		return nil, err
	}
	out := make([]openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion, 0, len(textParts))
	for _, part := range textParts {
		partCopy := part
		out = append(out, openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion{OfText: &partCopy})
	}
	return out, nil
}

func normalizeMessagePartType(part domain.MessagePart) domain.MessagePartType {
	if part.Type != "" {
		return part.Type
	}
	if part.Image != nil {
		return domain.MessagePartTypeImage
	}
	return domain.MessagePartTypeText
}

func toOpenAIImageURLParam(part domain.MessagePart) (openai.ChatCompletionContentPartImageImageURLParam, error) {
	if part.Image == nil {
		return openai.ChatCompletionContentPartImageImageURLParam{}, fmt.Errorf("image part is missing image payload")
	}
	image := part.Image
	if strings.TrimSpace(image.Base64) != "" && strings.TrimSpace(image.LocalPath) != "" {
		return openai.ChatCompletionContentPartImageImageURLParam{}, fmt.Errorf("image part must not set both base64 and local_path")
	}

	var (
		url string
		err error
	)
	switch {
	case strings.TrimSpace(image.Base64) != "":
		url, err = imageDataURLFromBase64(image.Base64, image.MIMEType)
	case strings.TrimSpace(image.LocalPath) != "":
		url, err = imageDataURLFromFile(image.LocalPath, image.MIMEType)
	default:
		err = fmt.Errorf("image part requires either base64 or local_path")
	}
	if err != nil {
		return openai.ChatCompletionContentPartImageImageURLParam{}, err
	}

	return openai.ChatCompletionContentPartImageImageURLParam{
		URL:    url,
		Detail: strings.TrimSpace(image.Detail),
	}, nil
}

func imageDataURLFromBase64(rawBase64, explicitMIME string) (string, error) {
	trimmed := strings.TrimSpace(rawBase64)
	if strings.HasPrefix(trimmed, "data:") {
		return trimmed, nil
	}
	normalized := stripBase64Whitespace(trimmed)
	decoded, err := base64.StdEncoding.DecodeString(normalized)
	if err != nil {
		return "", fmt.Errorf("decode image base64: %w", err)
	}
	mimeType := detectImageMIMEType(decoded, explicitMIME, "")
	return fmt.Sprintf("data:%s;base64,%s", mimeType, normalized), nil
}

func imageDataURLFromFile(path, explicitMIME string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read image file %q: %w", path, err)
	}
	mimeType := detectImageMIMEType(data, explicitMIME, path)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data)), nil
}

func detectImageMIMEType(data []byte, explicitMIME, path string) string {
	if mimeType := strings.TrimSpace(explicitMIME); mimeType != "" {
		return mimeType
	}
	if detected := strings.TrimSpace(http.DetectContentType(data)); detected != "" && detected != "application/octet-stream" {
		return detected
	}
	if ext := strings.TrimSpace(filepath.Ext(path)); ext != "" {
		if guessed := strings.TrimSpace(mime.TypeByExtension(ext)); guessed != "" {
			return guessed
		}
	}
	return "image/png"
}

func stripBase64Whitespace(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, value)
}

func buildOpenAIChatCompletionParams(messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, model string) (openai.ChatCompletionNewParams, error) {
	openAIMessages, err := toOpenAIMessages(messages)
	if err != nil {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("failed to convert messages: %w", err)
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(model),
		Messages: openAIMessages,
	}

	if len(tools) > 0 {
		openaiTools := make([]openai.ChatCompletionToolUnionParam, len(tools))
		for i, tool := range tools {
			openaiTools[i] = openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        tool.Function.Name,
				Description: openai.String(tool.Function.Description),
				Parameters:  tool.Function.Parameters,
			})
		}
		params.Tools = openaiTools

		// Map opts.ToolChoice into the SDK's union param. The OpenAI
		// spec (and DeepSeek's compatible API) accepts:
		//   - "auto" / "required" / "none" as plain strings
		//   - {"type": "function", "function": {"name": <fn>}} to force
		//     a specific tool by name
		// Earlier code wrongly converted "required" into a named-tool
		// choice pinned to tools[0], which DeepSeek's reasoner rejects
		// with `does not support this tool_choice`. Send strings as
		// strings; only fall through to the named form when ToolChoice
		// looks like an actual function name.
		if opts != nil && opts.ToolChoice != "" {
			switch opts.ToolChoice {
			case "auto", "required", "none":
				params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
					OfAuto: openai.String(opts.ToolChoice),
				}
			default:
				params.ToolChoice = openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{
					Name: opts.ToolChoice,
				})
			}
		}
	}

	if opts != nil {
		if opts.Temperature >= 0 {
			params.Temperature = openai.Float(opts.Temperature)
		}
		if opts.MaxTokens > 0 {
			params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
		}
		if domain.UsesNativeWebSearch(opts.WebSearchMode) {
			params.WebSearchOptions = openai.ChatCompletionNewParamsWebSearchOptions{
				SearchContextSize: domain.NormalizeWebSearchContextSize(opts.WebSearchContextSize),
			}
		}
	}

	return params, nil
}

func shouldRetryOpenAIWithoutNativeWebSearch(opts *domain.GenerationOptions, err error) bool {
	if opts == nil || domain.NormalizeWebSearchMode(opts.WebSearchMode) != domain.WebSearchModeAuto || err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "web_search_options") ||
		strings.Contains(msg, "web search") ||
		strings.Contains(msg, "unsupported parameter") ||
		strings.Contains(msg, "unknown field")
}

func cloneOptionsWithoutNativeWebSearch(opts *domain.GenerationOptions) *domain.GenerationOptions {
	if opts == nil {
		return &domain.GenerationOptions{WebSearchMode: domain.WebSearchModeMCP}
	}
	cloned := *opts
	cloned.WebSearchMode = domain.WebSearchModeMCP
	return &cloned
}

// shouldRetryOpenAIWithoutToolChoice reports whether the upstream error
// is "this model does not support tool_choice" (DeepSeek's reasoner
// models reject any tool_choice value — even "required" or "auto" —
// when chained with reasoning_content; some OpenAI o-series variants
// also reject named-tool choice). When the upstream rejects the param
// we retry once with tool_choice stripped — the model can still emit
// tool calls organically; it just can't be forced.
func shouldRetryOpenAIWithoutToolChoice(opts *domain.GenerationOptions, err error) bool {
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

func cloneOptionsWithoutToolChoice(opts *domain.GenerationOptions) *domain.GenerationOptions {
	if opts == nil {
		return &domain.GenerationOptions{}
	}
	cloned := *opts
	cloned.ToolChoice = ""
	return &cloned
}

// applyOpenAIRetryFallbacks composes the compatibility fallbacks (drop
// native web search, drop tool_choice). Returns a re-derived options
// struct if any apply, or nil when the error isn't one of the known
// "feature unsupported" patterns.
func applyOpenAIRetryFallbacks(opts *domain.GenerationOptions, err error) *domain.GenerationOptions {
	current := opts
	changed := false
	if shouldRetryOpenAIWithoutNativeWebSearch(current, err) {
		current = cloneOptionsWithoutNativeWebSearch(current)
		changed = true
	}
	if shouldRetryOpenAIWithoutToolChoice(current, err) {
		current = cloneOptionsWithoutToolChoice(current)
		changed = true
	}
	if !changed {
		return nil
	}
	return current
}

// Generate generates text using OpenAI API
func (p *OpenAILLMProvider) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("%w: empty prompt", domain.ErrInvalidInput)
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(prompt),
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(p.config.LLMModel),
		Messages: messages,
	}

	if opts != nil {
		if opts.Temperature >= 0 {
			params.Temperature = openai.Float(opts.Temperature)
		}
		if opts.MaxTokens > 0 {
			params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
		}
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrGenerationFailed, err)
	}

	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("%w: no choices returned", domain.ErrGenerationFailed)
	}

	return completion.Choices[0].Message.Content, nil
}

// Stream generates text with streaming using OpenAI API
func (p *OpenAILLMProvider) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	if prompt == "" {
		return fmt.Errorf("%w: empty prompt", domain.ErrInvalidInput)
	}
	if callback == nil {
		return fmt.Errorf("%w: nil callback", domain.ErrInvalidInput)
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(prompt),
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(p.config.LLMModel),
		Messages: messages,
	}

	if opts != nil {
		if opts.Temperature >= 0 {
			params.Temperature = openai.Float(opts.Temperature)
		}
		if opts.MaxTokens > 0 {
			params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
		}
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			callback(chunk.Choices[0].Delta.Content)
		}
	}

	if err := stream.Err(); err != nil {
		wrapped := ClassifyError(err)
		if wrapped == err {
			return fmt.Errorf("%w: %v", domain.ErrGenerationFailed, err)
		}
		return wrapped
	}

	return nil
}

// GenerateWithTools generates text with tool calling support
func (p *OpenAILLMProvider) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("%w: empty messages", domain.ErrInvalidInput)
	}

	params, err := buildOpenAIChatCompletionParams(messages, tools, opts, p.config.LLMModel)
	if err != nil {
		return nil, err
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		if retryOpts := applyOpenAIRetryFallbacks(opts, err); retryOpts != nil {
			retryParams, buildErr := buildOpenAIChatCompletionParams(messages, tools, retryOpts, p.config.LLMModel)
			if buildErr != nil {
				return nil, buildErr
			}
			completion, err = p.client.Chat.Completions.New(ctx, retryParams)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrGenerationFailed, err)
	}

	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("%w: no choices returned", domain.ErrGenerationFailed)
	}

	choice := completion.Choices[0]
	result := &domain.GenerationResult{
		Content:  choice.Message.Content,
		Finished: true,
	}

	if len(choice.Message.ToolCalls) > 0 {
		result.ToolCalls = make([]domain.ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil, fmt.Errorf("failed to parse tool call arguments: %w", err)
			}
			result.ToolCalls[i] = domain.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: domain.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: args,
				},
			}
		}
	}

	return result, nil
}

// StreamWithTools generates text with tool calling support in streaming mode
func (p *OpenAILLMProvider) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	if len(messages) == 0 {
		return fmt.Errorf("%w: empty messages", domain.ErrInvalidInput)
	}
	if callback == nil {
		return fmt.Errorf("%w: nil callback", domain.ErrInvalidInput)
	}

	err := p.streamWithToolsOnce(ctx, messages, tools, opts, callback)
	if retryOpts := applyOpenAIRetryFallbacks(opts, err); retryOpts != nil {
		return p.streamWithToolsOnce(ctx, messages, tools, retryOpts, callback)
	}
	return err
}

func (p *OpenAILLMProvider) streamWithToolsOnce(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	params, err := buildOpenAIChatCompletionParams(messages, tools, opts, p.config.LLMModel)
	if err != nil {
		return err
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := &domain.GenerationResult{
			Content: choice.Delta.Content,
		}

		// Support reasoning_content from DeepSeek/Ollama via ExtraFields
		if reasoning, ok := choice.Delta.JSON.ExtraFields["reasoning_content"]; ok {
			// Field value can be extracted using its raw representation or a simple string check
			delta.ReasoningContent = strings.Trim(reasoning.Raw(), "\"")
		}

		if len(choice.Delta.ToolCalls) > 0 {
			delta.ToolCalls = make([]domain.ToolCall, len(choice.Delta.ToolCalls))
			for i, tc := range choice.Delta.ToolCalls {
				var args map[string]interface{}
				// In streaming, arguments arrive in fragments
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)

				delta.ToolCalls[i] = domain.ToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: domain.FunctionCall{
						Name:      tc.Function.Name,
						Arguments: args,
					},
				}
			}
		}

		if err := callback(delta); err != nil {
			return fmt.Errorf("callback error: %w", err)
		}
	}

	if err := stream.Err(); err != nil {
		wrapped := ClassifyError(err)
		if wrapped == err {
			return fmt.Errorf("%w: %v", domain.ErrGenerationFailed, err)
		}
		return wrapped
	}

	return nil
}

// GenerateStructured implements structured JSON output generation for OpenAI using native structured output
func (p *OpenAILLMProvider) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if err := ValidateStructuredRequest(prompt, schema); err != nil {
		return nil, err
	}

	if opts == nil {
		opts = DefaultStructuredOptions()
	}
	if err := ValidateGenerationOptions(opts); err != nil {
		return nil, err
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(prompt),
	}

	// Use OpenAI's native structured output with ResponseFormat.
	// Note: Strict mode is intentionally omitted — many OpenAI-compatible providers
	// (MiniMax, Ollama, etc.) do not support strict:true and return an error.
	// Schema-level validation is handled on the client side after parsing.
	schemaParam := openai.ResponseFormatJSONSchemaJSONSchemaParam{
		Name:        "structured_response",
		Description: openai.String("Structured response based on the provided schema"),
		Schema:      schema,
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(p.config.LLMModel),
		Messages: messages,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{JSONSchema: schemaParam},
		},
	}

	if opts.Temperature >= 0 {
		params.Temperature = openai.Float(opts.Temperature)
	}
	if opts.MaxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
	}

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		// Fallback: some OpenAI-compatible providers reject response_format entirely.
		// Retry as a plain chat completion asking for JSON in the prompt.
		return p.generateStructuredFallback(ctx, prompt, schema, opts)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response choices returned")
	}

	rawJSON := resp.Choices[0].Message.Content

	// Try to parse the JSON into the provided schema
	var isValid bool
	if err := json.Unmarshal([]byte(rawJSON), schema); err == nil {
		isValid = true
	}

	return &domain.StructuredResult{
		Data:  schema,
		Raw:   rawJSON,
		Valid: isValid,
	}, nil
}

// generateStructuredFallback is called when the provider rejects response_format.
// It issues a plain chat completion instructing the model to respond with valid JSON,
// then extracts the JSON from the response text.
func (p *OpenAILLMProvider) generateStructuredFallback(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	schemaBytes, _ := json.Marshal(schema)
	augmented := fmt.Sprintf(
		"%s\n\nRespond with valid JSON only (no markdown, no explanation) matching this schema:\n%s",
		prompt, string(schemaBytes),
	)

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(augmented),
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(p.config.LLMModel),
		Messages: messages,
	}
	if opts != nil {
		if opts.Temperature >= 0 {
			params.Temperature = openai.Float(opts.Temperature)
		}
		if opts.MaxTokens > 0 {
			params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
		}
	}

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, WrapStructuredOutputError(domain.ProviderOpenAI, err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response choices returned")
	}

	rawText := resp.Choices[0].Message.Content
	// Strip possible markdown code fences
	rawJSON := extractJSON(rawText)

	var isValid bool
	if err := json.Unmarshal([]byte(rawJSON), schema); err == nil {
		isValid = true
	}

	return &domain.StructuredResult{
		Data:  schema,
		Raw:   rawJSON,
		Valid: isValid,
	}, nil
}

// extractJSON pulls the first JSON object or array from a string,
// stripping markdown code fences if present.
func extractJSON(s string) string {
	// Strip ```json ... ``` or ``` ... ```
	for _, fence := range []string{"```json", "```"} {
		if idx := strings.Index(s, fence); idx != -1 {
			s = s[idx+len(fence):]
			if end := strings.Index(s, "```"); end != -1 {
				s = s[:end]
			}
		}
	}
	s = strings.TrimSpace(s)
	// Find first { or [
	for i, ch := range s {
		if ch == '{' || ch == '[' {
			return s[i:]
		}
	}
	return s
}

// Health checks the health of the OpenAI service
func (p *OpenAILLMProvider) Health(ctx context.Context) error {
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("Hello"),
	}

	params := openai.ChatCompletionNewParams{
		Model:               shared.ChatModel(p.config.LLMModel),
		Messages:            messages,
		MaxCompletionTokens: openai.Int(1),
	}

	_, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrServiceUnavailable, err)
	}

	return nil
}

// ExtractMetadata extracts metadata from content using OpenAI API
func (p *OpenAILLMProvider) ExtractMetadata(ctx context.Context, content string, model string) (*domain.ExtractedMetadata, error) {
	if content == "" {
		return nil, fmt.Errorf("%w: content cannot be empty", domain.ErrInvalidInput)
	}

	data := map[string]interface{}{
		"Content": content,
	}
	rendered, err := p.promptManager.Render(prompt.MetadataExtraction, data)
	if err != nil {
		rendered = fmt.Sprintf("Extract metadata from: %s", content)
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(rendered),
	}

	llmModel := p.config.LLMModel
	if model != "" {
		llmModel = model
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(llmModel),
		Messages: messages,
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("%w: metadata extraction failed: %v", domain.ErrGenerationFailed, err)
	}

	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("%w: no choices returned for metadata extraction", domain.ErrGenerationFailed)
	}

	var metadata domain.ExtractedMetadata
	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &metadata); err != nil {
		return nil, fmt.Errorf("%w: failed to unmarshal metadata response: %v. Raw response: %s",
			domain.ErrInvalidInput, err, completion.Choices[0].Message.Content)
	}

	return &metadata, nil
}

// RecognizeIntent analyzes user request to determine intent type
func (p *OpenAILLMProvider) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	if request == "" {
		return nil, fmt.Errorf("%w: empty request", domain.ErrInvalidInput)
	}

	data := map[string]interface{}{
		"Query":   request,
		"Intents": "question, action, analysis, search, calculation, status, unknown",
	}
	rendered, err := p.promptManager.Render(prompt.RouterIntentAnalysis, data)
	if err != nil {
		rendered = fmt.Sprintf("Analyze intent for: %s", request)
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(rendered),
	}

	params := openai.ChatCompletionNewParams{
		Model:       openai.ChatModel(p.config.LLMModel),
		Messages:    messages,
		Temperature: openai.Float(0.1),
		MaxTokens:   openai.Int(300),
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		// Fallback to unknown intent
		return &domain.IntentResult{
			Intent:     domain.IntentUnknown,
			Confidence: 0.0,
			NeedsTools: true,
			Reasoning:  "Failed to determine intent",
		}, nil
	}

	if len(completion.Choices) == 0 {
		return &domain.IntentResult{
			Intent:     domain.IntentUnknown,
			Confidence: 0.0,
			NeedsTools: true,
			Reasoning:  "No response from model",
		}, nil
	}

	// Parse the response
	var intentData struct {
		Intent     string   `json:"intent"`
		Confidence float64  `json:"confidence"`
		KeyVerbs   []string `json:"key_verbs"`
		Entities   []string `json:"entities"`
		NeedsTools bool     `json:"needs_tools"`
		Reasoning  string   `json:"reasoning"`
	}

	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &intentData); err != nil {
		return &domain.IntentResult{
			Intent:     domain.IntentUnknown,
			Confidence: 0.0,
			NeedsTools: true,
			Reasoning:  "Failed to parse intent",
		}, nil
	}

	// Map string to IntentType
	var intentType domain.IntentType
	switch intentData.Intent {
	case "question":
		intentType = domain.IntentQuestion
	case "action":
		intentType = domain.IntentAction
	case "analysis":
		intentType = domain.IntentAnalysis
	case "search":
		intentType = domain.IntentSearch
	case "calculation":
		intentType = domain.IntentCalculation
	case "status":
		intentType = domain.IntentStatus
	default:
		intentType = domain.IntentUnknown
	}

	return &domain.IntentResult{
		Intent:     intentType,
		Confidence: intentData.Confidence,
		KeyVerbs:   intentData.KeyVerbs,
		Entities:   intentData.Entities,
		NeedsTools: intentData.NeedsTools,
		Reasoning:  intentData.Reasoning,
	}, nil
}

// OpenAIEmbedderProvider implements EmbedderProvider for OpenAI-compatible services
type OpenAIEmbedderProvider struct {
	client openai.Client
	config *domain.OpenAIProviderConfig
}

// NewOpenAIEmbedderProvider creates a new OpenAI embedder provider
func NewOpenAIEmbedderProvider(config *domain.OpenAIProviderConfig) (domain.EmbedderProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	opts := []option.RequestOption{}

	// For local services like Ollama, API key might not be required
	// Use empty string for API key if not provided, let the provider handle authentication
	if config.APIKey != "" {
		opts = append(opts, option.WithAPIKey(config.APIKey))
	} else {
		// Use a placeholder for local services that don't require API keys
		opts = append(opts, option.WithAPIKey("ollama"))
	}

	if config.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(config.BaseURL))
	}

	return &OpenAIEmbedderProvider{
		client: openai.NewClient(opts...),
		config: config,
	}, nil
}

// ProviderType returns the provider type
func (p *OpenAIEmbedderProvider) ProviderType() domain.ProviderType {
	return domain.ProviderOpenAI
}

// UsageModel returns the configured embedding model for usage accounting.
func (p *OpenAIEmbedderProvider) UsageModel() string {
	return p.config.EmbeddingModel
}

// Embed generates embeddings using OpenAI API
func (p *OpenAIEmbedderProvider) Embed(ctx context.Context, text string) ([]float64, error) {
	if text == "" {
		return nil, fmt.Errorf("%w: empty text", domain.ErrInvalidInput)
	}

	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(p.config.EmbeddingModel),
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String(text),
		},
	}

	embedding, err := p.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrEmbeddingFailed, err)
	}

	if len(embedding.Data) == 0 {
		return nil, fmt.Errorf("%w: no embedding data returned", domain.ErrEmbeddingFailed)
	}

	vec64 := make([]float64, len(embedding.Data[0].Embedding))
	for i, v := range embedding.Data[0].Embedding {
		vec64[i] = float64(v)
	}

	return vec64, nil
}

// EmbedBatch generates embeddings for multiple texts in a single API call.
// Inputs are batched (max 512 per request) to stay within API limits.
func (p *OpenAIEmbedderProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	const maxBatchSize = 512
	all := make([][]float64, 0, len(texts))

	for i := 0; i < len(texts); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		params := openai.EmbeddingNewParams{
			Model: openai.EmbeddingModel(p.config.EmbeddingModel),
			Input: openai.EmbeddingNewParamsInputUnion{
				OfArrayOfStrings: batch,
			},
		}

		res, err := p.client.Embeddings.New(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", domain.ErrEmbeddingFailed, err)
		}

		// Use Index field to ensure correct ordering
		vecs := make([][]float64, len(batch))
		for _, item := range res.Data {
			vec := make([]float64, len(item.Embedding))
			for j, v := range item.Embedding {
				vec[j] = float64(v)
			}
			vecs[item.Index] = vec
		}
		all = append(all, vecs...)
	}

	return all, nil
}

// Health checks the health of the OpenAI embeddings service
func (p *OpenAIEmbedderProvider) Health(ctx context.Context) error {
	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(p.config.EmbeddingModel),
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String("test"),
		},
	}

	_, err := p.client.Embeddings.New(ctx, params)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrServiceUnavailable, err)
	}

	return nil
}
