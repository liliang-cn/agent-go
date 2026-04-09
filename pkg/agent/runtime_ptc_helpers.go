package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

var (
	ptcMarkdownFenceRe    = regexp.MustCompile("(?s)```.*?```")
	ptcTaskCompleteLineRe = regexp.MustCompile(`(?im)^\s*task_complete\s*$`)
	ptcReturnValueLineRe  = regexp.MustCompile(`(?m)^\*\*Return Value:\*\*\s*(.+?)\s*$`)
)

func (r *Runtime) overridePTCToolCallsFromContent(round int, content string, toolCalls []domain.ToolCall) []domain.ToolCall {
	if !r.svc.isPTCEnabled() {
		return toolCalls
	}

	isCode := r.svc.ptcIntegration.IsCodeResponse(content)
	if r.debugEnabled() {
		r.emitDebug(round+1, "ptc_override", fmt.Sprintf("IsCodeResponse=%v contentLen=%d", isCode, len(content)))
	}
	if !isCode {
		return toolCalls
	}

	extracted := r.svc.ptcIntegration.ExtractCode(content)
	if r.debugEnabled() {
		r.emitDebug(round+1, "ptc_override", fmt.Sprintf("Extracted code len=%d", len(extracted)))
	}
	extracted = sanitiseJSCode(extracted)
	if r.debugEnabled() {
		r.emitDebug(round+1, "ptc_override", fmt.Sprintf("Sanitised code len=%d", len(extracted)))
	}
	if extracted == "" {
		return toolCalls
	}

	out := append([]domain.ToolCall(nil), toolCalls...)
	for i, tc := range out {
		if tc.Function.Name == "execute_javascript" {
			if out[i].Function.Arguments == nil {
				out[i].Function.Arguments = make(map[string]interface{})
			}
			out[i].Function.Arguments["code"] = extracted
			if r.debugEnabled() {
				r.emitDebug(round+1, "ptc_override", fmt.Sprintf("Replaced execute_javascript payload for tool call %d", i))
			}
		}
	}
	return out
}

func (r *Runtime) handlePTCTextFallback(ctx context.Context, content string, messages []domain.Message) ([]domain.Message, bool) {
	if !r.svc.isPTCEnabled() || !r.svc.ptcIntegration.IsCodeResponse(content) {
		return messages, false
	}

	code := r.svc.ptcIntegration.ExtractCode(content)
	if code == "" {
		return messages, false
	}

	tc := domain.ToolCall{
		ID:   domain.NormalizeToolCallID("ptc_fallback_" + uuid.New().String()[:8]),
		Type: "function",
		Function: domain.FunctionCall{
			Name:      "execute_javascript",
			Arguments: map[string]interface{}{"code": code},
		},
	}

	behavior, endExecution := r.svc.beginToolExecution("execute_javascript", r.currentAgent)
	r.emitToolCall("execute_javascript", tc.Function.Arguments, behavior)
	execResult, execErr, _ := r.executeToolViaSubAgent(ctx, tc)
	endExecution()
	r.emitToolResult("execute_javascript", execResult, execErr, behavior)

	messages = append(messages, domain.Message{
		Role:      "assistant",
		Content:   content,
		ToolCalls: []domain.ToolCall{tc},
	})
	resultMsg := toolResultToString(execResult)
	if execErr != nil {
		resultMsg = fmt.Sprintf("execute_javascript error: %v", execErr)
	}
	messages = append(messages, domain.Message{
		Role:       "tool",
		ToolCallID: tc.ID,
		Content:    resultMsg,
	})
	return messages, true
}

func (r *Runtime) shouldShortCircuitPTCToolRound(content string, toolCalls []domain.ToolCall) (string, bool) {
	if !r.svc.isPTCEnabled() || r.svc.ptcIntegration == nil || len(toolCalls) == 0 {
		return "", false
	}
	for _, tc := range toolCalls {
		if tc.Function.Name != "execute_javascript" {
			return "", false
		}
	}

	hasPlausibleCode := false
	extracted := sanitiseJSCode(r.svc.ptcIntegration.ExtractCode(content))
	if extracted != "" && r.svc.ptcIntegration.looksLikeCode(extracted) {
		hasPlausibleCode = true
	}
	for _, tc := range toolCalls {
		code, _ := tc.Function.Arguments["code"].(string)
		code = sanitiseJSCode(code)
		if code != "" && r.svc.ptcIntegration.looksLikeCode(code) {
			hasPlausibleCode = true
			break
		}
	}
	if hasPlausibleCode {
		return "", false
	}

	final := extractInlineTaskCompleteResult(content)
	if final == "" {
		return "", false
	}
	return final, true
}

func extractInlineTaskCompleteResult(content string) string {
	cleaned := ptcMarkdownFenceRe.ReplaceAllString(content, " ")
	cleaned = strings.ReplaceAll(cleaned, "<code>", " ")
	cleaned = strings.ReplaceAll(cleaned, "</code>", " ")
	cleaned = ptcTaskCompleteLineRe.ReplaceAllString(cleaned, " ")

	lines := strings.Split(cleaned, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch line {
		case "{}", "json", "```json", "```":
			continue
		}
		parts = append(parts, line)
	}

	candidate := strings.TrimSpace(strings.Join(parts, " "))
	if !isMeaningfulAnswerText(candidate) {
		return ""
	}
	return candidate
}

func extractPTCTerminalAnswer(toolResults []ToolExecutionResult) string {
	for i := len(toolResults) - 1; i >= 0; i-- {
		tr := toolResults[i]
		if tr.ToolName != "execute_javascript" {
			continue
		}

		resultText, _ := tr.Result.(string)
		resultText = strings.TrimSpace(resultText)
		if resultText == "" {
			continue
		}
		if strings.Contains(strings.ToLower(resultText), "failed") {
			continue
		}

		match := ptcReturnValueLineRe.FindStringSubmatch(resultText)
		if len(match) < 2 {
			continue
		}
		candidate := strings.TrimSpace(match[1])
		if candidate == "" || strings.HasPrefix(candidate, "(none") {
			continue
		}
		if isMeaningfulAnswerText(candidate) {
			return candidate
		}
	}
	return ""
}
