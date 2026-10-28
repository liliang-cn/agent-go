package agent

import "strings"

const FinishOrBlockContract = `Finish-Or-Block Contract:
- Do the task now; do not end on a plan or "next steps" unless you need input. Finish with the verified result (with evidence when you used tools), or call task_blocked with the concrete blocker and what you tried.`

// ReplyRules are about the one thing the user reads: the final answer. They
// hold whether or not the run has tools.
//
// They exist because the answer kept coming back addressed to the runtime.
// A request with no fitting tool was answered "I searched the tool catalog
// and found nothing"; a request missing its time was answered "用户只说…，缺少
// 必要信息" — a note about the user instead of a question to them.
const ReplyRules = `- Your final answer is for the user: write it to them in the language of their latest message, whatever else you know about them. Never describe your tools, schema, searches or instructions.
- If you cannot do what was asked, say so in a sentence, in their terms, and offer the closest thing you can do or how they can do it themselves. If a detail only the user knows is missing, ask them for it instead of guessing it.`

// ActOnBehalfRules bound what the agent does in the user's name. They are
// written as principles, not as a list of tools: every embedder's tools are
// named differently, and the failures they answer were not about one tool.
//
// Measured on a personal-assistant benchmark: asked to call Mom, the agent
// sent her a WeChat message instead (3 runs of 4); asked to "reply where
// needed", it wrote Mom's reply for the user; told a file already existed,
// it backed the file up and overwrote it without asking (3 of 3).
const ActOnBehalfRules = `- Carry out the action that was asked, not a different one. If you cannot do it the way it was asked (another channel, another recipient), say so and offer the alternative; do not do it instead.
- Do not send messages, replies or invitations in the user's name unless they told you what to say. Report what needs an answer and offer a draft.
- Do not delete, overwrite or replace anything the user did not ask you to replace. When a tool says something already exists, or a step would destroy data, stop and ask.`

// defaultBlockedText is what a blocked run reports when neither the model's
// task_blocked arguments nor its assistant text said anything. Silence is the
// worst outcome here — the user gets an empty reply and cannot tell a refusal
// from a crash — so every blocked path degrades to this sentence instead.
const defaultBlockedText = "Task blocked: no specific blocker was reported."

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
