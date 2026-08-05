package agent

import "strings"

// This file holds the two v3 acceptance lints described in the v3 plan §4.5:
// they replace prompt sentences with deterministic runtime rejection.
//
//  1. non_empty_final_answer — "empty-handed" runs (search tools → search
//     tools → task_blocked with no text) must not be able to terminate.
//  2. task_delivery_contract — a task whose goal contains an explicit delivery
//     action (send the mail, write the file, post the message) cannot finish
//     unless the trace shows a matching side-effect tool actually succeeded.

// --- non_empty_final_answer ---------------------------------------------------

// NonEmptyFinalAnswer rejects a terminal answer that carries no usable text.
// The runtime pairs this with forceFinalSynthesis: an exhausted loop is forced
// to produce a conclusion, and if it still produces nothing the lint rejects and
// re-prompts rather than emitting an empty completion event.
func NonEmptyFinalAnswer() OutputLint {
	return LintFunc{
		NameValue: "non_empty_final_answer",
		Fn: func(text string, _ LintContext) (bool, string) {
			if len(strings.TrimSpace(text)) >= minimumFinalAnswerChars {
				return true, ""
			}
			return false, "your final answer was empty. Never end a run with no text. " +
				"Summarise what you found, what you did, and what (if anything) you could not do — " +
				"using only the information you already have. Do not search for more tools."
		},
	}
}

// minimumFinalAnswerChars is the shortest string that still counts as an
// answer. One or two characters is a stutter, not a reply.
const minimumFinalAnswerChars = 2

// --- task_delivery_contract ---------------------------------------------------

// deliveryAction pairs the phrases that mark an explicit delivery request in a
// goal with the tool-name fragments that prove the delivery actually happened.
type deliveryAction struct {
	Name         string
	GoalMarkers  []string
	ToolMarkers  []string
	FeedbackVerb string
}

var deliveryActions = []deliveryAction{
	{
		Name:         "email",
		GoalMarkers:  []string{"send an email", "send email", "email it", "email the", "mail it to", "发邮件", "发送邮件", "寄邮件"},
		ToolMarkers:  []string{"send_mail", "send_email", "smtp", "gmail", "outlook", "mailer"},
		FeedbackVerb: "send the email",
	},
	{
		Name:         "message",
		GoalMarkers:  []string{"send a message", "send message", "post to slack", "send a slack", "notify", "发消息", "发送消息", "通知"},
		ToolMarkers:  []string{"slack", "send_sms", "telegram", "wechat", "discord", "chat_post", "push_notification"},
		FeedbackVerb: "send the message",
	},
	{
		Name:         "file",
		GoalMarkers:  []string{"write a file", "save it to", "save the file", "write to disk", "写文件", "保存到"},
		ToolMarkers:  []string{"fs_write", "write_file", "create_file", "sandbox_write"},
		FeedbackVerb: "write the file",
	},
}

// TaskDeliveryContract enforces the v3 delivery contract: if the goal asked for
// a concrete side effect, the run cannot be declared complete until the trace
// shows a tool that performs that side effect was actually called.
//
// "Computed the right number but never sent the mail" was the single most
// common silent failure in v2; this makes it a rejected terminal state instead.
func TaskDeliveryContract() OutputLint {
	return LintFunc{
		NameValue: "task_delivery_contract",
		Fn: func(_ string, ctx LintContext) (bool, string) {
			goal := strings.ToLower(ctx.Goal)
			if strings.TrimSpace(goal) == "" {
				return true, ""
			}
			for _, action := range deliveryActions {
				if !containsAny(goal, action.GoalMarkers) {
					continue
				}
				if toolCallsMatchAny(ctx.ToolCalls, action.ToolMarkers) {
					continue
				}
				// No tool in this run can perform the delivery. Rejecting here
				// would just burn the retry budget on something the agent
				// cannot do; that case belongs to task_blocked, not a lint.
				if !toolCallsMatchAny(ctx.AvailableTools, action.ToolMarkers) {
					continue
				}
				return false, "the task explicitly asked you to " + action.FeedbackVerb +
					", but no tool that performs that action was called in this run. " +
					"Actually perform the delivery now and report the confirmation, " +
					"or call task_blocked with the concrete blocker that prevents it."
			}
			return true, ""
		},
	}
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

func toolCallsMatchAny(toolCalls []string, markers []string) bool {
	for _, call := range toolCalls {
		lower := strings.ToLower(call)
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}
