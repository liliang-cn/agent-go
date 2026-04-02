package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

// ChatWithPTC sends a message with PTC (Parallel Tool Calling) support.
//
// PTC is a transport mode, not a separate execution path. This method is a
// thin backward-compatibility wrapper around Chat(). When PTC is enabled,
// Chat() automatically uses the PTC execution path and populates
// ExecutionResult.PTCResult with rich JS execution details.
func (s *Service) ChatWithPTC(ctx context.Context, message string) (*PTCChatResult, error) {
	result, err := s.Chat(ctx, message)
	if err != nil {
		return nil, err
	}

	ptcUsed := result.PTCResult != nil &&
		(result.PTCResult.Code != "" ||
			result.PTCResult.Type == PTCResultTypeExecuted ||
			result.PTCResult.Type == PTCResultTypeCode)

	llmResp := ""
	if result.PTCResult != nil {
		llmResp = result.PTCResult.OriginalContent
	} else {
		llmResp = fmt.Sprintf("%v", result.FinalResult)
	}

	return &PTCChatResult{
		ExecutionResult: result,
		PTCResult:       result.PTCResult,
		PTCUsed:         ptcUsed,
		LLMResponse:     llmResp,
		SessionID:       result.SessionID,
	}, nil
}

// runPTCExecution is the PTC transport path called from runWithConfig when
// isPTCEnabled() is true. It streams a response from the LLM, extracts any
// JavaScript code (from <code> tags or execute_javascript tool calls), runs
// it in the goja sandbox, and returns the raw content plus the rich PTCResult.
// Session management (loading/saving messages) remains in runWithConfig.
func (s *Service) runPTCExecution(ctx context.Context, goal string, session *Session, cfg *RunConfig) (interface{}, *PTCResult, error) {
	// Embed PTC usage instructions in the user message so the LLM knows to
	// respond with <code> tags (fallback for models that don't support function
	// calling, or when the system prompt alone is not enough).
	ptcPrompt := goal + `

IMPORTANT: Respond with JavaScript code in <code> tags.
Your code will be executed in a secure sandbox.
Use console.log() for output and return the final result with a top-level return statement.
Use callTool(name, args) to invoke tools by exact name.
Do not search for tools inside PTC. Tool discovery belongs to the agent/tool-calling layer, not the JavaScript sandbox.
MCP tool results are usually shaped like { success, data, error }. Use toolOk(result) and toolData(result) to inspect them safely.
DO NOT wrap code in function main(){...}main().
Example format:
` + "<code>\nconst data = callTool('some_tool', { arg: 'value' });\nconsole.log(\"Processing:\", data);\nreturn { result: data };\n</code>"

	currentAgent := s.resolveCurrentAgent(session)

	// Prepend system prompt so the LLM has full context (memory, env, PTC instructions, etc.)
	systemMsg := s.buildSystemPrompt(ctx, currentAgent)

	// Build message history for LLM (system first, then history, then this user message).
	userMsg := domain.Message{Role: "user", Content: ptcPrompt}
	messages := append([]domain.Message{{Role: "system", Content: systemMsg}}, session.GetMessages()...)
	messages = append(messages, userMsg)

	// Build PTC tools list for the LLM.
	availableCallTools := s.ptcAvailableCallTools(ctx)
	ptcTools := s.ptcIntegration.GetPTCTools(availableCallTools)

	if s.debug || cfg.Debug {
		s.logDebugPrompt(messages, 0)
	}

	temperature := cfg.Temperature
	if temperature == 0 {
		temperature = 0.3
	}
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2000
	}

	var fullContent strings.Builder
	var toolCalls []domain.ToolCall

	err := s.llmService.StreamWithTools(ctx, messages, ptcTools, s.toolGenerationOptions(temperature, maxTokens, ""), func(delta *domain.GenerationResult) error {
		if delta.Content != "" {
			fullContent.WriteString(delta.Content)
			s.emitProgress("partial", delta.Content, 0, "")
		}
		for _, tc := range delta.ToolCalls {
			found := false
			for j, existing := range toolCalls {
				if (existing.ID != "" && existing.ID == tc.ID) ||
					(existing.ID == "" && existing.Function.Name == tc.Function.Name) {
					if existing.Function.Arguments == nil {
						existing.Function.Arguments = make(map[string]interface{})
					}
					for k, v := range tc.Function.Arguments {
						existing.Function.Arguments[k] = v
					}
					toolCalls[j] = existing
					found = true
					break
				}
			}
			if !found {
				toolCalls = append(toolCalls, tc)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("LLM streaming failed: %w", err)
	}

	content := fullContent.String()

	if s.debug || cfg.Debug {
		s.logDebugResponse(&domain.GenerationResult{
			Content:   content,
			ToolCalls: toolCalls,
		}, 0)
	}

	if os.Getenv("DEBUG") != "" {
		fmt.Printf("\nDEBUG [runPTCExecution] raw content: %q\n", content)
		if len(toolCalls) > 0 {
			fmt.Printf("DEBUG [runPTCExecution] tool calls: %v\n", toolCalls)
		}
	}

	hasExecuteJavaScript := false
	// Prefer code from execute_javascript tool call (structured) over content
	// extraction (text-based), since tool calls are more reliable.
	for _, tc := range toolCalls {
		if tc.Function.Name == "execute_javascript" {
			hasExecuteJavaScript = true
			if code, ok := tc.Function.Arguments["code"].(string); ok {
				content = "<code>" + code + "</code>"
				break
			}
		}
	}

	// Some models ignore the PTC transport instruction and emit normal tool calls.
	// Execute them directly instead of returning the model's internal reasoning text.
	if len(toolCalls) > 0 && !hasExecuteJavaScript {
		result := &domain.GenerationResult{
			Content:   content,
			ToolCalls: toolCalls,
		}
		nextAgent, filteredToolCalls, duplicateToolResults, fallback, handoff := s.prepareToolRound(ctx, &messages, currentAgent, session, result, make(map[string]int), 0)
		if handoff {
			currentAgent = nextAgent
		}
		if fallback != "" {
			return fallback, nil, nil
		}
		if len(filteredToolCalls) == 0 {
			if len(duplicateToolResults) == 1 {
				return toolResultToString(duplicateToolResults[0].Result), nil, nil
			}
			if len(duplicateToolResults) > 1 {
				return strings.TrimSpace(s.formatToolResults(duplicateToolResults)), nil, nil
			}
			return "", nil, nil
		}

		toolResults, err := s.executeToolCalls(ctx, currentAgent, session, filteredToolCalls)
		if err != nil {
			return nil, nil, fmt.Errorf("PTC direct tool-call fallback failed: %w", err)
		}
		toolResults = append(duplicateToolResults, toolResults...)

		var final strings.Builder
		final.WriteString("Direct tool-call fallback executed successfully.\n")
		if len(toolResults) == 1 {
			final.WriteString(toolResultToString(toolResults[0].Result))
		} else {
			final.WriteString(s.formatToolResults(toolResults))
		}

		return strings.TrimSpace(final.String()), nil, nil
	}

	ptcResult, err := s.ptcIntegration.ProcessLLMResponse(ctx, content, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("PTC processing failed: %w", err)
	}

	return content, ptcResult, nil
}

// PTCChatResult contains the result of a PTC-aware chat
type PTCChatResult struct {
	ExecutionResult *ExecutionResult `json:"execution_result,omitempty"`
	PTCResult       *PTCResult       `json:"ptc_result,omitempty"`
	PTCUsed         bool             `json:"ptc_used"`
	LLMResponse     string           `json:"llm_response"`
	SessionID       string           `json:"session_id"`
}

// buildPTCRouterOptions constructs ptc.RouterOption list for dynamic providers only.
// Static tools (RAG, Memory, custom) are registered via ToolRegistry.SyncToPTCRouter.
func buildPTCRouterOptions(mcpSvc MCPToolExecutor, skillsSvc *skills.Service) []ptc.RouterOption {
	var opts []ptc.RouterOption

	if mcpSvc != nil {
		opts = append(opts, ptc.WithMCPService(mcpSvc))
		mcpInfos := domainToolsToPTCInfos(mcpSvc.ListTools(), CategoryMCP)
		if len(mcpInfos) > 0 {
			opts = append(opts, ptc.WithMCPToolInfos(mcpInfos))
		}
	}

	if skillsSvc != nil {
		opts = append(opts, ptc.WithSkillsService(skillsSvc))
		skillList, _ := skillsSvc.ListSkills(context.Background(), skills.SkillFilter{})
		skillInfos := make([]ptc.ToolInfo, 0, len(skillList))
		for _, sk := range skillList {
			skillInfos = append(skillInfos, ptc.ToolInfo{
				Name:        sk.ID,
				Description: sk.Description,
				Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
				Category:    CategorySkill,
			})
		}
		if len(skillInfos) > 0 {
			opts = append(opts, ptc.WithSkillToolInfos(skillInfos))
		}
	}

	return opts
}

// domainToolsToPTCInfos converts domain.ToolDefinition slice to ptc.ToolInfo slice.
func domainToolsToPTCInfos(defs []domain.ToolDefinition, category string) []ptc.ToolInfo {
	infos := make([]ptc.ToolInfo, 0, len(defs))
	for _, d := range defs {
		params := d.Function.Parameters
		if params == nil {
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		infos = append(infos, ptc.ToolInfo{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			Parameters:  params,
			Category:    category,
		})
	}
	return infos
}
