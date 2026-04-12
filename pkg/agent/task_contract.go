package agent

import "strings"

const FinishOrBlockContract = `Finish-Or-Block Contract:
- Do not stop at planning when the task can be executed now.
- Do not end with "next steps", "would do", "should do", or "I can do this next" unless external input is genuinely required.
- Continue until the task is completed, blocked, failed, or yielded.
- If completed, provide the verified result and concrete evidence when tools or filesystem/device actions were involved.
- If blocked, call task_blocked with the concrete blocker and evidence of what was attempted.
- Prefer verification over explanation.`

func isTaskTerminalToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "task_complete", "task_blocked":
		return true
	default:
		return false
	}
}

func taskTerminalToolResult(toolName string, args map[string]interface{}, fallback string) string {
	if args == nil {
		return strings.TrimSpace(fallback)
	}
	switch strings.TrimSpace(toolName) {
	case "task_blocked":
		for _, key := range []string{"blocker", "reason", "result"} {
			if text, ok := args[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	case "task_complete":
		if text, ok := args["result"].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return strings.TrimSpace(fallback)
}
