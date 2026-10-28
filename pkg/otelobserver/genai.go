package otelobserver

import (
	"encoding/json"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// OpenTelemetry GenAI semantic conventions, beside the OpenInference set.
//
// The spans carried OpenInference attributes only — Arize's open spec, which
// Phoenix and Langfuse read natively. The OpenTelemetry project's own GenAI
// conventions (gen_ai.*) are what Grafana's LLM dashboards, Datadog LLM
// Observability, Honeycomb and the OTel Collector's GenAI processors key on:
// a backend built on them received our spans and did not recognise a single
// one as a model call. Both sets are written now; a span carries the same
// facts twice under two names, which is cheap, and reads in both worlds.
//
// Keys are spelled out as strings. The conventions are still marked
// "development" in the spec and the otel-go semconv packages ship only the
// stable attributes, so there is nothing to import. The strings are the
// contract; a renamed key in a future spec version is a deliberate change
// here, not a dependency bump.
//
// Spec: https://opentelemetry.io/docs/specs/semconv/gen-ai/
const (
	genAIOperationName   = "gen_ai.operation.name"
	genAIProviderName    = "gen_ai.provider.name"
	genAIRequestModel    = "gen_ai.request.model"
	genAIResponseModel   = "gen_ai.response.model"
	genAIUsageInput      = "gen_ai.usage.input_tokens"
	genAIUsageOutput     = "gen_ai.usage.output_tokens"
	genAIUsageCacheRead  = "gen_ai.usage.cache_read.input_tokens"
	genAIUsageCacheWrite = "gen_ai.usage.cache_creation.input_tokens"
	genAIConversationID  = "gen_ai.conversation.id"
	genAIAgentName       = "gen_ai.agent.name"
	genAIToolName        = "gen_ai.tool.name"
	genAIToolType        = "gen_ai.tool.type"
	genAIToolCallID      = "gen_ai.tool.call.id"
	genAIToolCallArgs    = "gen_ai.tool.call.arguments"
	genAIToolCallResult  = "gen_ai.tool.call.result"
	genAIInputMessages   = "gen_ai.input.messages"
	genAIOutputMessages  = "gen_ai.output.messages"
	genAITokenType       = "gen_ai.token.type"
	genAIErrorType       = "error.type"
	genAIToolDefinitions = "gen_ai.tool.definitions"

	genAIMetricTokenUsage        = "gen_ai.client.token.usage"
	genAIMetricOperationDuration = "gen_ai.client.operation.duration"
)

// genAIOperation is the spec's operation name for a model call. Every call
// the runtime makes is one request and one completion over a message list,
// which the spec calls "chat"; the answering turn and the structured calls
// made on the run's behalf differ in purpose, not in operation, and the
// purpose stays in agentgo.llm.purpose beside it.
func genAIOperation(string) string { return "chat" }

func genAIModelStartAttrs(info agent.ModelInfo) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(genAIOperationName, genAIOperation(info.Purpose)),
		attribute.String(genAIRequestModel, nonEmpty(info.Model, "unknown")),
	}
	if info.SessionID != "" {
		attrs = append(attrs, attribute.String(genAIConversationID, info.SessionID))
	}
	if info.AgentName != "" {
		attrs = append(attrs, attribute.String(genAIAgentName, info.AgentName))
	}
	return attrs
}

func genAIModelEndAttrs(info agent.ModelInfo, res *agent.ModelResult, err error) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if req := info.Request; req != nil {
		attrs = append(attrs, attribute.String(genAIInputMessages, genAIMessagesJSON(req)))
		if len(req.Tools) > 0 {
			attrs = append(attrs, attribute.String(genAIToolDefinitions, toJSON(req.Tools)))
		}
	}
	if res != nil {
		attrs = append(attrs,
			attribute.Int(genAIUsageInput, res.PromptTokens),
			attribute.Int(genAIUsageOutput, res.CompletionTokens),
			attribute.String(genAIResponseModel, nonEmpty(res.Model, nonEmpty(info.Model, "unknown"))),
		)
		if res.CachedTokens > 0 {
			attrs = append(attrs, attribute.Int(genAIUsageCacheRead, res.CachedTokens))
		}
		if res.CacheWriteTokens > 0 {
			attrs = append(attrs, attribute.Int(genAIUsageCacheWrite, res.CacheWriteTokens))
		}
		if res.Provider != "" {
			attrs = append(attrs, attribute.String(genAIProviderName, strings.ToLower(res.Provider)))
		}
		if info.Request != nil || res.Response != nil {
			if msg := genAIOutput(res); len(msg.Parts) > 0 {
				attrs = append(attrs, attribute.String(genAIOutputMessages, toJSON([]genAIMessage{msg})))
			}
		}
	}
	if err != nil {
		attrs = append(attrs, attribute.String(genAIErrorType, errorType(err)))
	}
	return attrs
}

func genAIToolAttrs(info agent.ToolInfo, argsJSON string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(genAIOperationName, "execute_tool"),
		attribute.String(genAIToolName, info.Tool),
		attribute.String(genAIToolType, "function"),
		attribute.String(genAIToolCallArgs, argsJSON),
	}
	if info.CallID != "" {
		attrs = append(attrs, attribute.String(genAIToolCallID, info.CallID))
	}
	if info.SessionID != "" {
		attrs = append(attrs, attribute.String(genAIConversationID, info.SessionID))
	}
	if info.AgentName != "" {
		attrs = append(attrs, attribute.String(genAIAgentName, info.AgentName))
	}
	return attrs
}

func genAIAgentAttrs(info agent.SubAgentInfo) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(genAIOperationName, "invoke_agent"),
		attribute.String(genAIAgentName, info.Name),
	}
	if info.SessionID != "" {
		attrs = append(attrs, attribute.String(genAIConversationID, info.SessionID))
	}
	return attrs
}

// genAIMetricAttrs is the attribute set the two semconv metrics share:
// operation, requested model, the answering model and provider when known,
// and error.type on failure.
func genAIMetricAttrs(info agent.ModelInfo, res *agent.ModelResult, err error) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(genAIOperationName, genAIOperation(info.Purpose)),
		attribute.String(genAIRequestModel, nonEmpty(info.Model, "unknown")),
	}
	if res != nil {
		attrs = append(attrs, attribute.String(genAIResponseModel, nonEmpty(res.Model, nonEmpty(info.Model, "unknown"))))
		if res.Provider != "" {
			attrs = append(attrs, attribute.String(genAIProviderName, strings.ToLower(res.Provider)))
		}
	}
	if err != nil {
		attrs = append(attrs, attribute.String(genAIErrorType, errorType(err)))
	}
	return attrs
}

// genAIMessage is the spec's message shape for gen_ai.input.messages and
// gen_ai.output.messages: a role and a list of typed parts.
type genAIMessage struct {
	Role  string      `json:"role"`
	Parts []genAIPart `json:"parts"`
	// FinishReason is set on an output message only.
	FinishReason string `json:"finish_reason,omitempty"`
}

type genAIPart struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments any    `json:"arguments,omitempty"`
	Response  string `json:"response,omitempty"`
}

// genAIOutput is the model's answer as the spec's assistant message: its
// reasoning, its text and the tool calls it asked for, with why it stopped.
func genAIOutput(res *agent.ModelResult) genAIMessage {
	msg := genAIMessage{Role: "assistant", Parts: []genAIPart{}}
	if r := res.Response; r != nil && r.Reasoning != "" {
		msg.Parts = append(msg.Parts, genAIPart{Type: "reasoning", Content: r.Reasoning})
	}
	if res.Content != "" {
		msg.Parts = append(msg.Parts, genAIPart{Type: "text", Content: res.Content})
	}
	if r := res.Response; r != nil {
		for _, tc := range r.ToolCalls {
			msg.Parts = append(msg.Parts, genAIPart{Type: "tool_call", ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
		msg.FinishReason = r.FinishReason
	}
	return msg
}

// genAIMessagesJSON renders a ModelRequest in the spec's message format: text
// parts for content, tool_call parts for an assistant's calls, and a
// tool_call_response part for a tool message.
func genAIMessagesJSON(req *agent.ModelRequest) string {
	if req.Prompt != "" && len(req.Messages) == 0 {
		return toJSON([]genAIMessage{{Role: "user", Parts: []genAIPart{{Type: "text", Content: req.Prompt}}}})
	}
	out := make([]genAIMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := genAIMessage{Role: m.Role, Parts: []genAIPart{}}
		if m.Role == "tool" {
			msg.Parts = append(msg.Parts, genAIPart{Type: "tool_call_response", ID: m.ToolCallID, Response: m.Content})
		} else if m.Content != "" || len(m.ToolCalls) == 0 {
			msg.Parts = append(msg.Parts, genAIPart{Type: "text", Content: m.Content})
		}
		for _, tc := range m.ToolCalls {
			msg.Parts = append(msg.Parts, genAIPart{Type: "tool_call", ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
		out = append(out, msg)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// errorType is the spec's error.type: a low-cardinality class, not the
// message. The Go type name of the error is the closest honest answer that
// does not put a user's words into a metric attribute.
func errorType(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "context canceled"):
		return "cancelled"
	case strings.Contains(s, "deadline exceeded"), strings.Contains(s, "timeout"):
		return "timeout"
	case strings.Contains(s, "429"), strings.Contains(s, "rate limit"):
		return "rate_limited"
	case strings.Contains(s, "401"), strings.Contains(s, "403"), strings.Contains(s, "authenticat"):
		return "auth"
	case strings.Contains(s, "500"), strings.Contains(s, "502"), strings.Contains(s, "503"),
		strings.Contains(s, "504"), strings.Contains(s, "unavailable"):
		return "server_error"
	default:
		return "_OTHER"
	}
}
