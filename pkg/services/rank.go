package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// RankTestResult holds the outcome of one capability test.
type RankTestResult struct {
	Name        string        `json:"name"`
	Passed      bool          `json:"passed"`
	Details     string        `json:"details"`
	Latency     time.Duration `json:"latency"`
	Prompt      string        `json:"prompt,omitempty"`
	RawResponse string        `json:"raw_response,omitempty"`
}

// ProviderRankResult holds the full ranking outcome for a provider.
type ProviderRankResult struct {
	Provider string           `json:"provider"`
	Tests    []RankTestResult `json:"tests"`
	Score    int              `json:"score"` // number of tests passed (0-5)
	CAP      int              `json:"cap"`   // computed capability level (1-5)
}

// rankOutput is the internal return type for each test function.
type rankOutput struct {
	passed      bool
	details     string
	prompt      string // what was sent to the model
	rawResponse string // what the model returned (before evaluation)
}

// RankProvider runs 5 progressive capability tests against a named provider
// (or the default pool selection when providerName is empty).
// Tests are ordered from easiest to hardest:
//
//	1. basic_echo         — exact instruction following
//	2. json_output        — structured JSON compliance
//	3. math_reasoning     — arithmetic reasoning
//	4. tool_calling       — function/tool call support
//	5. system_instruction — system-prompt adherence + JSON extraction
func (s *GlobalPoolService) RankProvider(ctx context.Context, providerName string) (*ProviderRankResult, error) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, fmt.Errorf("pool service not initialized")
	}
	llmPool := s.llmPool
	s.mu.RUnlock()

	var gen domain.Generator
	if providerName != "" {
		client, err := llmPool.GetByProvider(providerName)
		if err != nil {
			return nil, fmt.Errorf("provider %q not found: %w", providerName, err)
		}
		defer llmPool.Release(client)
		gen = client
	} else {
		client, err := llmPool.Get()
		if err != nil {
			return nil, fmt.Errorf("no provider available: %w", err)
		}
		defer llmPool.Release(client)
		gen = client
	}

	type namedTest struct {
		name string
		fn   func(context.Context, domain.Generator) rankOutput
	}

	tests := []namedTest{
		{"basic_echo", rankTestBasicEcho},
		{"json_output", rankTestJSONOutput},
		{"math_reasoning", rankTestMathReasoning},
		{"tool_calling", rankTestToolCalling},
		{"system_instruction", rankTestSystemInstruction},
		{"ptc_roleplay", rankTestPTCRoleplay},
	}

	result := &ProviderRankResult{Provider: providerName}
	if result.Provider == "" {
		result.Provider = "default"
	}

	for _, t := range tests {
		start := time.Now()
		out := t.fn(ctx, gen)
		result.Tests = append(result.Tests, RankTestResult{
			Name:        t.name,
			Passed:      out.passed,
			Details:     out.details,
			Latency:     time.Since(start),
			Prompt:      out.prompt,
			RawResponse: out.rawResponse,
		})
		if out.passed {
			result.Score++
		}
	}

	result.CAP = max(result.Score, 1)

	return result, nil
}

// rankTestBasicEcho verifies the model follows a strict single-word instruction.
// Expected: model replies with exactly "PONG".
func rankTestBasicEcho(ctx context.Context, gen domain.Generator) rankOutput {
	prompt := "Reply with the single word PONG and absolutely nothing else."
	resp, err := gen.Generate(ctx, prompt, &domain.GenerationOptions{Temperature: 0.0, MaxTokens: 512})
	if err != nil {
		return rankOutput{prompt: prompt, rawResponse: "", details: fmt.Sprintf("error: %v", err)}
	}
	clean := strings.TrimSpace(strings.ToUpper(resp))
	if clean == "PONG" {
		return rankOutput{passed: true, prompt: prompt, rawResponse: resp, details: fmt.Sprintf("got %q", resp)}
	}
	return rankOutput{prompt: prompt, rawResponse: resp, details: fmt.Sprintf("expected %q, got %q", "PONG", resp)}
}

// rankTestJSONOutput verifies the model returns valid JSON with exact field values.
func rankTestJSONOutput(ctx context.Context, gen domain.Generator) rankOutput {
	prompt := `Output ONLY this exact JSON with no other text: {"status":"ok","code":200}`
	resp, err := gen.Generate(ctx, prompt, &domain.GenerationOptions{Temperature: 0.0, MaxTokens: 512})
	if err != nil {
		return rankOutput{prompt: prompt, rawResponse: "", details: fmt.Sprintf("error: %v", err)}
	}
	obj, err := extractFirstJSON(resp)
	if err != nil {
		return rankOutput{prompt: prompt, rawResponse: resp, details: fmt.Sprintf("no valid JSON: %v", err)}
	}
	if obj["status"] == "ok" && obj["code"] == float64(200) {
		return rankOutput{passed: true, prompt: prompt, rawResponse: resp, details: "correct JSON fields"}
	}
	return rankOutput{prompt: prompt, rawResponse: resp, details: fmt.Sprintf("wrong values: %v", obj)}
}

// rankTestMathReasoning verifies the model can compute 17×23=391.
func rankTestMathReasoning(ctx context.Context, gen domain.Generator) rankOutput {
	prompt := "What is 17 multiplied by 23? Reply with only the number, no explanation."
	resp, err := gen.Generate(ctx, prompt, &domain.GenerationOptions{Temperature: 0.0, MaxTokens: 512})
	if err != nil {
		return rankOutput{prompt: prompt, rawResponse: "", details: fmt.Sprintf("error: %v", err)}
	}
	if strings.Contains(resp, "391") {
		return rankOutput{passed: true, prompt: prompt, rawResponse: resp, details: fmt.Sprintf("got %q", resp)}
	}
	return rankOutput{prompt: prompt, rawResponse: resp, details: fmt.Sprintf("expected 391, got %q", resp)}
}

// rankTestToolCalling verifies the model issues a function call.
// Defines an echo tool and expects the model to call it with text="hello-world".
func rankTestToolCalling(ctx context.Context, gen domain.Generator) rankOutput {
	tools := []domain.ToolDefinition{
		{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        "echo",
				Description: "Echoes back the provided text exactly.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{
							"type":        "string",
							"description": "The text to echo back.",
						},
					},
					"required": []string{"text"},
				},
			},
		},
	}
	messages := []domain.Message{
		{Role: "user", Content: "Call the echo tool with text set to 'hello-world'."},
	}

	prompt := formatMessagesForDebug(messages)
	result, err := gen.GenerateWithTools(ctx, messages, tools,
		&domain.GenerationOptions{Temperature: 0.0, MaxTokens: 200},
	)
	if err != nil {
		return rankOutput{prompt: prompt, rawResponse: "", details: fmt.Sprintf("error: %v", err)}
	}

	rawResp := formatGenerationResultForDebug(result)
	for _, call := range result.ToolCalls {
		if call.Function.Name == "echo" {
			if text, ok := call.Function.Arguments["text"].(string); ok {
				if strings.Contains(text, "hello-world") {
					return rankOutput{passed: true, prompt: prompt, rawResponse: rawResp,
						details: fmt.Sprintf("echo called with text=%q", text)}
				}
				return rankOutput{prompt: prompt, rawResponse: rawResp,
					details: fmt.Sprintf("echo called but text=%q", text)}
			}
		}
	}
	return rankOutput{prompt: prompt, rawResponse: rawResp,
		details: fmt.Sprintf("echo not called (got %d tool calls)", len(result.ToolCalls))}
}

// rankTestSystemInstruction verifies the model respects a system prompt and
// extracts the year 2025 from a sentence, returning valid JSON.
func rankTestSystemInstruction(ctx context.Context, gen domain.Generator) rankOutput {
	messages := []domain.Message{
		{
			Role:    "system",
			Content: "You are a JSON extraction API. Output ONLY valid JSON, no explanation, no markdown.",
		},
		{
			Role:    "user",
			Content: `Extract the year from: "The summit took place in 2025." Return: {"year": <number>}`,
		},
	}

	prompt := formatMessagesForDebug(messages)
	result, err := gen.GenerateWithTools(ctx, messages, nil,
		&domain.GenerationOptions{Temperature: 0.0, MaxTokens: 50},
	)
	if err != nil {
		return rankOutput{prompt: prompt, rawResponse: "", details: fmt.Sprintf("error: %v", err)}
	}

	rawResp := result.Content
	obj, err := extractFirstJSON(rawResp)
	if err != nil {
		return rankOutput{prompt: prompt, rawResponse: rawResp,
			details: fmt.Sprintf("no valid JSON: %v", err)}
	}
	if obj["year"] == float64(2025) {
		return rankOutput{passed: true, prompt: prompt, rawResponse: rawResp,
			details: "year=2025 correctly extracted"}
	}
	return rankOutput{prompt: prompt, rawResponse: rawResp,
		details: fmt.Sprintf("wrong year: %v", obj)}
}

// rankTestPTCRoleplay tests Programmatic Tool Calling (PTC):
// the model must respond with synchronous ES5 JavaScript wrapped in <code>...</code>,
// using callTool(name, args) to invoke tools and ending with an explicit return.
//
// This matches the actual PTC runtime contract used by PTCIntegration.
func rankTestPTCRoleplay(ctx context.Context, gen domain.Generator) rankOutput {
	messages := []domain.Message{
		{
			Role: "system",
			Content: `## PTC Mode (JavaScript Sandbox)
Respond ONLY with ` + "`<code>...</code>`" + ` containing synchronous ES5 JavaScript.
- Use ` + "`callTool(name, args)`" + ` to invoke any tool. No direct function calls.
- No async/await, no promises, no require/import.
- End with a top-level ` + "`return`" + ` statement.
Example: ` + "`<code>const r = callTool('add', {a: 1, b: 2}); return r;</code>`",
		},
		{
			Role:    "user",
			Content: `Use the multiply tool to compute 6 × 7 and return the result.`,
		},
	}

	prompt := formatMessagesForDebug(messages)
	result, err := gen.GenerateWithTools(ctx, messages, nil,
		&domain.GenerationOptions{Temperature: 0.0, MaxTokens: 512},
	)
	if err != nil {
		return rankOutput{prompt: prompt, rawResponse: "", details: fmt.Sprintf("error: %v", err)}
	}

	raw := result.Content

	// Extract code from <code>...</code> tags (primary format)
	code := extractBetweenTags(raw, "<code>", "</code>")
	if code == "" {
		// Fallback: markdown fences
		code = stripCodeFences(raw)
	}

	if code == "" {
		return rankOutput{prompt: prompt, rawResponse: raw,
			details: "no <code>...</code> block found in response"}
	}
	if !strings.Contains(raw, "<code>") {
		return rankOutput{prompt: prompt, rawResponse: raw,
			details: fmt.Sprintf("missing <code> wrapper, got: %q", truncate(raw, 80))}
	}
	if !strings.Contains(code, "callTool") {
		return rankOutput{prompt: prompt, rawResponse: raw,
			details: fmt.Sprintf("callTool not used: %q", truncate(code, 80))}
	}
	if !strings.Contains(code, "multiply") {
		return rankOutput{prompt: prompt, rawResponse: raw,
			details: fmt.Sprintf("multiply not passed to callTool: %q", truncate(code, 80))}
	}
	if !strings.Contains(code, "6") || !strings.Contains(code, "7") {
		return rankOutput{prompt: prompt, rawResponse: raw,
			details: fmt.Sprintf("arguments 6/7 missing: %q", truncate(code, 80))}
	}
	if !strings.Contains(code, "return") {
		return rankOutput{prompt: prompt, rawResponse: raw,
			details: fmt.Sprintf("no explicit return: %q", truncate(code, 80))}
	}
	return rankOutput{passed: true, prompt: prompt, rawResponse: raw,
		details: fmt.Sprintf("valid PTC: %q", truncate(code, 70))}
}

// extractBetweenTags extracts the content between the first occurrence of open and close tags.
func extractBetweenTags(s, open, close string) string {
	start := strings.Index(s, open)
	if start == -1 {
		return ""
	}
	start += len(open)
	end := strings.Index(s[start:], close)
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(s[start : start+end])
}

// stripCodeFences removes markdown code fences (```js / ```javascript / ```) if present.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "```"); idx != -1 {
		s = s[idx:]
		for _, prefix := range []string{"```javascript", "```js", "```"} {
			s = strings.TrimPrefix(s, prefix)
		}
		if end := strings.Index(s, "```"); end != -1 {
			s = s[:end]
		}
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// extractFirstJSON finds the first {...} block in s and parses it.
func extractFirstJSON(s string) (map[string]any, error) {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || start >= end {
		return nil, fmt.Errorf("no JSON object found")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s[start:end+1]), &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// formatMessagesForDebug renders a message slice as a readable string.
func formatMessagesForDebug(msgs []domain.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&sb, "[%s] %s\n", strings.ToUpper(m.Role), m.Content)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatGenerationResultForDebug renders a GenerationResult as a readable string.
func formatGenerationResultForDebug(r *domain.GenerationResult) string {
	if len(r.ToolCalls) == 0 {
		return r.Content
	}
	var sb strings.Builder
	if r.Content != "" {
		sb.WriteString(r.Content)
		sb.WriteString("\n")
	}
	for _, tc := range r.ToolCalls {
		args, _ := json.Marshal(tc.Function.Arguments)
		fmt.Fprintf(&sb, "[tool_call] %s(%s)", tc.Function.Name, string(args))
	}
	return sb.String()
}
