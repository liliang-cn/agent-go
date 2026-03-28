package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

var agentMentionRe = regexp.MustCompile(`(^|[\s,;])@([A-Za-z0-9_-]+)`)

type multiAgentResult struct {
	AgentName string
	Text      string
	Err       error
}

func (h *Handler) HandleMultiAgentChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.teamManager == nil {
		JSONError(w, "Team manager unavailable", http.StatusServiceUnavailable)
		return
	}

	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		JSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	prompt := strings.TrimSpace(extractLastUserMessage(raw["messages"]))
	if prompt == "" {
		prompt = strings.TrimSpace(stringValue(raw["message"]))
	}
	agentNames := extractAgentNames(raw["agent_names"])
	mentionedAgents, cleanedPrompt := parseMultiAgentPrompt(prompt)
	if len(agentNames) == 0 {
		agentNames = mentionedAgents
	}
	if len(agentNames) == 0 {
		JSONError(w, "At least one @Agent mention is required", http.StatusBadRequest)
		return
	}
	if cleanedPrompt == "" {
		JSONError(w, "Message required after agent mentions", http.StatusBadRequest)
		return
	}

	chatID := strings.TrimSpace(stringValue(raw["id"]))
	leadAgentName := strings.TrimSpace(stringValue(raw["lead_agent_name"]))
	if leadAgentName == "" {
		leadAgentName = strings.TrimSpace(stringValue(raw["captain_name"]))
	}
	streamAISDKMultiAgentChat(w, r, chatID, leadAgentName, agentNames, cleanedPrompt, h.teamManager)
}

func extractAgentNames(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}

	seen := make(map[string]struct{}, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(stringValue(item))
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	return names
}

func parseMultiAgentPrompt(prompt string) ([]string, string) {
	matches := agentMentionRe.FindAllStringSubmatch(prompt, -1)
	names := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		name := strings.TrimSpace(match[2])
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}

	cleaned := agentMentionRe.ReplaceAllString(prompt, "$1")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return names, strings.TrimSpace(cleaned)
}

func streamAISDKMultiAgentChat(w http.ResponseWriter, r *http.Request, chatID, leadAgentName string, agentNames []string, prompt string, manager *agent.TeamManager) {
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
		"messageMetadata": map[string]any{
			"mode":            "multi-agent",
			"agent_names":     agentNames,
			"captain_name":    leadAgentName,
			"lead_agent_name": leadAgentName,
		},
	})
	writeSSEChunk(w, flusher, map[string]any{
		"type": "text-start",
		"id":   textPartID,
	})

	// Emit dispatch-start events in prompt order before work begins.
	for _, agentName := range agentNames {
		writeSSEChunk(w, flusher, map[string]any{
			"type":      "data-agent-event",
			"transient": true,
			"data": map[string]any{
				"event_type":   "dispatch_started",
				"agent_name":   agentName,
				"content":      fmt.Sprintf("Dispatching shared task to %s", agentName),
				"message_mode": "multi-agent",
			},
		})
	}

	resultsCh := make(chan multiAgentResult, len(agentNames))
	var wg sync.WaitGroup
	for _, agentName := range agentNames {
		agentName := agentName
		wg.Add(1)
		go func() {
			defer wg.Done()
			text, err := manager.DispatchTask(r.Context(), agentName, prompt)
			resultsCh <- multiAgentResult{
				AgentName: agentName,
				Text:      strings.TrimSpace(text),
				Err:       err,
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	finishReason := "stop"
	completed := make(map[string]multiAgentResult, len(agentNames))
	for result := range resultsCh {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		completed[result.AgentName] = result
		eventType := "dispatch_completed"
		content := "Completed"
		if result.Err != nil {
			eventType = "dispatch_failed"
			content = result.Err.Error()
			finishReason = "error"
		}
		writeSSEChunk(w, flusher, map[string]any{
			"type":      "data-agent-event",
			"transient": true,
			"data": map[string]any{
				"event_type":   eventType,
				"agent_name":   result.AgentName,
				"content":      content,
				"message_mode": "multi-agent",
			},
		})
	}

	for _, agentName := range agentNames {
		result, ok := completed[agentName]
		if !ok {
			continue
		}
		section := formatMultiAgentSection(result)
		for _, chunk := range chunkText(section, 96) {
			writeSSEChunk(w, flusher, map[string]any{
				"type":  "text-delta",
				"id":    textPartID,
				"delta": chunk,
			})
		}
	}

	writeSSEChunk(w, flusher, map[string]any{
		"type": "text-end",
		"id":   textPartID,
	})
	writeSSEChunk(w, flusher, map[string]any{
		"type":         "finish",
		"finishReason": finishReason,
		"messageMetadata": map[string]any{
			"mode":            "multi-agent",
			"agent_names":     append([]string(nil), agentNames...),
			"captain_name":    leadAgentName,
			"lead_agent_name": leadAgentName,
		},
	})
}

func formatMultiAgentSection(result multiAgentResult) string {
	if result.Err != nil {
		return fmt.Sprintf("## %s\nError: %s\n\n", result.AgentName, result.Err)
	}
	text := result.Text
	if text == "" {
		text = "No response returned."
	}
	return fmt.Sprintf("## %s\n%s\n\n", result.AgentName, text)
}
