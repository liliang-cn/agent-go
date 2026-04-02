package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
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
