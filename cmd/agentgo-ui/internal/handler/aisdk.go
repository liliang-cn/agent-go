package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/pkg/agent"
)

// setSSEHeaders sets common headers for SSE
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// writeSSEChunk writes a JSON payload as an SSE data chunk
func writeSSEChunk(w http.ResponseWriter, flusher http.Flusher, payload map[string]any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// streamAISDKChat streams a simple text response in AI SDK format
func streamAISDKChat(w http.ResponseWriter, r *http.Request, chatID, text string) {
	setSSEHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		JSONError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	messageID := chatID
	if messageID == "" {
		messageID = uuid.New().String()
	}
	textPartID := "text-0"

	writeSSEChunk(w, flusher, map[string]any{
		"type":      "start",
		"messageId": messageID,
	})
	writeSSEChunk(w, flusher, map[string]any{
		"type": "text-start",
		"id":   textPartID,
	})

	for _, chunk := range chunkText(text, 96) {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		writeSSEChunk(w, flusher, map[string]any{
			"type":  "text-delta",
			"id":    textPartID,
			"delta": chunk,
		})
	}

	writeSSEChunk(w, flusher, map[string]any{
		"type": "text-end",
		"id":   textPartID,
	})
	writeSSEChunk(w, flusher, map[string]any{
		"type":         "finish",
		"finishReason": "stop",
		"usage": map[string]int{
			"inputTokens":  0,
			"outputTokens": 0,
			"totalTokens":  0,
		},
	})
}

// extractLastUserMessage extracts the last user message from AI SDK messages array
func extractLastUserMessage(messages any) string {
	items, ok := messages.([]any)
	if !ok {
		return ""
	}

	for i := len(items) - 1; i >= 0; i-- {
		message, ok := items[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if role != "user" {
			continue
		}
		if content, ok := message["content"].(string); ok && strings.TrimSpace(content) != "" {
			return content
		}
		parts, ok := message["parts"].([]any)
		if !ok {
			continue
		}
		var textParts []string
		for _, part := range parts {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := partMap["type"].(string)
			if partType != "text" {
				continue
			}
			if text, ok := partMap["text"].(string); ok && strings.TrimSpace(text) != "" {
				textParts = append(textParts, text)
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n")
		}
	}

	return ""
}

// chunkText splits text into chunks of given size
func chunkText(text string, size int) []string {
	if text == "" {
		return []string{""}
	}

	runes := []rune(text)
	if len(runes) <= size {
		return []string{text}
	}

	chunks := make([]string, 0, (len(runes)/size)+1)
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

// stringifyJSON marshals any value to JSON string
func stringifyJSON(v any) string {
	if v == nil {
		return ""
	}

	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// streamLegacyChat streams a simple text response in legacy format
func streamLegacyChat(w http.ResponseWriter, r *http.Request, text string) {
	setSSEHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		JSONError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	for _, chunk := range chunkText(text, 96) {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		data, _ := json.Marshal(map[string]string{"content": chunk})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// streamAISDKAgentChat streams agent events in AI SDK format
func streamAISDKAgentChat(w http.ResponseWriter, r *http.Request, chatID, agentName string, events <-chan *agent.Event) {
	setSSEHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		JSONError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	messageID := chatID
	if messageID == "" {
		messageID = uuid.New().String()
	}
	textPartID := "text-0"
	textStarted := false
	partialSeen := false
	toolCalls := make(map[string][]string)
	finishReason := "stop"

	writeSSEChunk(w, flusher, map[string]any{
		"type":      "start",
		"messageId": messageID,
		"messageMetadata": map[string]any{
			"mode":       "agent",
			"agent_name": agentName,
		},
	})

	for evt := range events {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		switch evt.Type {
		case agent.EventTypeThinking, agent.EventTypeDebug, agent.EventTypeHandoff, agent.EventTypeStart:
			writeSSEChunk(w, flusher, map[string]any{
				"type":      "data-agent-event",
				"transient": true,
				"data": map[string]any{
					"event_type":   string(evt.Type),
					"content":      evt.Content,
					"agent_name":   evt.AgentName,
					"tool_name":    evt.ToolName,
					"tool_args":    evt.ToolArgs,
					"debug_type":   evt.DebugType,
					"round":        evt.Round,
					"timestamp":    evt.Timestamp,
					"message_mode": "agent",
				},
			})
		case agent.EventTypePartial:
			if !textStarted {
				writeSSEChunk(w, flusher, map[string]any{
					"type": "text-start",
					"id":   textPartID,
				})
				textStarted = true
			}
			partialSeen = true
			if evt.Content != "" {
				writeSSEChunk(w, flusher, map[string]any{
					"type":  "text-delta",
					"id":    textPartID,
					"delta": evt.Content,
				})
			}
		case agent.EventTypeToolCall:
			callID := uuid.New().String()
			toolCalls[evt.ToolName] = append(toolCalls[evt.ToolName], callID)
			writeSSEChunk(w, flusher, map[string]any{
				"type":       "tool-input-start",
				"toolCallId": callID,
				"toolName":   evt.ToolName,
			})
			inputText := stringifyJSON(evt.ToolArgs)
			if inputText != "" {
				writeSSEChunk(w, flusher, map[string]any{
					"type":           "tool-input-delta",
					"toolCallId":     callID,
					"inputTextDelta": inputText,
				})
			}
			writeSSEChunk(w, flusher, map[string]any{
				"type":       "tool-input-available",
				"toolCallId": callID,
				"toolName":   evt.ToolName,
				"input":      evt.ToolArgs,
			})
		case agent.EventTypeToolResult:
			callID := dequeueToolCallID(toolCalls, evt.ToolName)
			if callID == "" {
				callID = uuid.New().String()
			}
			writeSSEChunk(w, flusher, map[string]any{
				"type":       "tool-output-available",
				"toolCallId": callID,
				"output":     evt.ToolResult,
			})
		case agent.EventTypeComplete:
			if evt.Content != "" && !partialSeen {
				if !textStarted {
					writeSSEChunk(w, flusher, map[string]any{
						"type": "text-start",
						"id":   textPartID,
					})
					textStarted = true
				}
				writeSSEChunk(w, flusher, map[string]any{
					"type":  "text-delta",
					"id":    textPartID,
					"delta": evt.Content,
				})
			}
		case agent.EventTypeError:
			finishReason = "error"
			writeSSEChunk(w, flusher, map[string]any{
				"type":      "error",
				"errorText": evt.Content,
			})
		}
	}

	if textStarted {
		writeSSEChunk(w, flusher, map[string]any{
			"type": "text-end",
			"id":   textPartID,
		})
	}
	writeSSEChunk(w, flusher, map[string]any{
		"type":         "finish",
		"finishReason": finishReason,
		"messageMetadata": map[string]any{
			"mode":       "agent",
			"agent_name": agentName,
		},
	})
}

func dequeueToolCallID(queue map[string][]string, toolName string) string {
	items := queue[toolName]
	if len(items) == 0 {
		return ""
	}

	callID := items[0]
	if len(items) == 1 {
		delete(queue, toolName)
		return callID
	}

	queue[toolName] = items[1:]
	return callID
}
