package agent

import (
	"os"
	"strconv"
	"strings"
)

// This file holds the two v3 acceptance lints from the v3 plan §4.5: they
// replace prompt sentences with deterministic runtime rejection.
//
//  1. non_empty_final_answer — "empty-handed" runs (search tools → search
//     tools → task_blocked with no text) must not be able to terminate.
//  2. task_delivery_contract — a run that owes the user a side effect (send
//     the mail, write the file, post the message) cannot complete unless the
//     trace shows a matching tool actually ran.
//
// What the run owes comes from LintContext.Deliverables, resolved once per run
// by constraints.go — either declared outright by the embedder or extracted
// from the user's own words. It is deliberately NOT matched out of the goal
// text here: a phrase table only ever covers the languages and phrasings
// somebody thought to list, and silently enforces nothing for everyone else.

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

// deliveryToolMarkers maps a deliverable kind to the tool-name fragments that
// prove it happened. These match TOOL NAMES, which this framework controls —
// not user text, which it does not.
var deliveryToolMarkers = map[string][]string{
	"email":   {"send_mail", "send_email", "sendmail", "smtp", "gmail", "outlook", "mailer"},
	"message": {"slack", "send_sms", "telegram", "wechat", "discord", "chat_post", "push_notification", "notify_user"},
	"file":    {"fs_write", "fs_edit", "fs_multi_edit", "write_file", "create_file", "sandbox_write"},
}

// deliveryVerb renders a kind as something to say back to the model.
var deliveryVerb = map[string]string{
	"email":   "send the email",
	"message": "send the message",
	"file":    "write the file",
	"other":   "perform the delivery you were asked for",
}

// TaskDeliveryContract enforces the v3 delivery contract: when the run owes a
// side effect, it cannot be declared complete until the trace shows a tool that
// performs that side effect was actually called.
//
// "Computed the right number but never sent the mail" was the single most
// common silent failure in v2; this makes it a rejected terminal state instead.
//
// A deliverable whose tools were never available is NOT a violation — the agent
// cannot be faulted for a capability it was never given, and burning the retry
// budget on it just turns a clean task_blocked into a loop.
func TaskDeliveryContract() OutputLint {
	return LintFunc{
		NameValue: "task_delivery_contract",
		Fn: func(_ string, ctx LintContext) (bool, string) {
			for _, want := range ctx.Deliverables {
				kind := strings.ToLower(strings.TrimSpace(want.Kind))

				// A named target file is checkable directly: prefer the
				// artifact over the attempt.
				if kind == "file" && want.Path != "" {
					if fileArtifactExists(want.Path) {
						continue
					}
					return false, "the task asked you to produce " + want.Path +
						", but no such file exists on disk yet (or it is empty — a write may have " +
						"been truncated). Actually write the file, verify it exists, then finish; " +
						"or call task_blocked with the concrete blocker."
				}

				markers, known := deliveryToolMarkers[kind]
				if !known {
					// "other": nothing specific to check for, so trust the run.
					continue
				}
				if toolCallsMatchAny(ctx.ToolCalls, markers) {
					continue
				}
				if !toolCallsMatchAny(ctx.AvailableTools, markers) {
					// No tool in this run can perform the delivery. Rejecting
					// here would burn the retry budget on something the agent
					// cannot do; that case belongs to task_blocked.
					continue
				}
				verb := deliveryVerb[kind]
				if verb == "" {
					verb = deliveryVerb["other"]
				}
				detail := ""
				if want.Description != "" {
					detail = " (" + want.Description + ")"
				}
				return false, "the task explicitly asked you to " + verb + detail +
					", but no tool that performs that action was called in this run. " +
					"Actually perform the delivery now and report the confirmation, " +
					"or call task_blocked with the concrete blocker that prevents it."
			}
			return true, ""
		},
	}
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

// fileArtifactExists reports whether path names a non-empty regular file.
func fileArtifactExists(path string) bool {
	expanded := strings.TrimSpace(path)
	if expanded == "" {
		return false
	}
	if strings.HasPrefix(expanded, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		expanded = home + expanded[1:]
	}
	info, err := os.Stat(expanded)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Size() > 0
}

// --- no_tool_scaffolding_answer -------------------------------------------------

// NoToolScaffoldingAnswer rejects a final answer that is really tool-search
// plumbing echoed back at the user.
//
// The benchmark's home-order-pizza run finished with the literal text "No tools
// found matching the query." — a string this framework generates to tell the
// MODEL that a search came up empty. It reached the user because a PTC round
// can promote its return value straight to the terminal answer, and that value
// was the search result.
//
// The strings matched here are our own constants, not user text: this is the
// runtime recognising its own scaffolding, which is exactly the kind of thing a
// lint should catch.
func NoToolScaffoldingAnswer() OutputLint {
	return LintFunc{
		NameValue: "no_tool_scaffolding_answer",
		Fn: func(text string, _ LintContext) (bool, string) {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				// non_empty_final_answer owns the empty case.
				return true, ""
			}
			for _, scaffold := range toolScaffoldingStrings {
				if !strings.Contains(trimmed, scaffold) {
					continue
				}
				// Only a violation when the scaffolding IS the answer, not when
				// the agent quoted it inside a real explanation.
				if len(trimmed) <= len(scaffold)+scaffoldingSlackChars {
					return false, "your final answer was tool-search plumbing (" +
						strconv.Quote(scaffold) + "), not an answer to the user. " +
						"Answer the original request from what you already know, " +
						"or call task_blocked naming the capability you are missing."
				}
			}
			return true, ""
		},
	}
}

// toolScaffoldingStrings are framework-emitted messages addressed to the model.
var toolScaffoldingStrings = []string{
	toolSearchNoMatches,
	toolSearchSummaryRequest,
	toolSearchNoMappingPrefix,
}

// scaffoldingSlackChars allows a little surrounding punctuation or whitespace
// before we stop calling the reply "just the scaffolding".
const scaffoldingSlackChars = 40
