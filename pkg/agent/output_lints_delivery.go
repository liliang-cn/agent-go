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

// --- task_delivery_contract / requested_action_contract -------------------------
//
// Both contracts below are pure structure. Neither knows what a mail tool or a
// reminder tool is called, because nothing in this package can know that: every
// embedder names their tools differently, and a name table only ever covers the
// ones somebody thought to list. The semantic step — "which of the tools this
// run actually has would carry this out?" — happens once, in the constraint
// extraction, which is shown the run's own tool catalog and answers with a
// tool name (see constraints.go, satisfied_by). What is left here is a
// comparison: extraction named tool X, did the trace call X?

// toolWasCalled reports whether name appears in the run's tool-call trace.
// Exact match on the registered tool name — the extraction copied that name out
// of the catalog, so there is nothing to normalise or guess at.
func toolWasCalled(toolCalls []string, name string) bool {
	if name == "" {
		return false
	}
	for _, call := range toolCalls {
		if strings.TrimSpace(call) == name {
			return true
		}
	}
	return false
}

// toolIsAvailable reports whether name is one of the tools this run could call.
func toolIsAvailable(availableTools []string, name string) bool {
	return toolWasCalled(availableTools, name)
}

// TaskDeliveryContract enforces the v3 delivery contract: when the run owes a
// side effect, it cannot be declared complete until the trace shows the tool
// that performs that side effect was actually called.
//
// "Computed the right number but never sent the mail" was the single most
// common silent failure in v2; this makes it a rejected terminal state instead.
//
// A deliverable no available tool can satisfy is NOT a violation — the agent
// cannot be faulted for a capability it was never given, and burning the retry
// budget on it just turns a clean task_blocked into a loop.
func TaskDeliveryContract() OutputLint {
	return LintFunc{
		NameValue: "task_delivery_contract",
		Fn: func(_ string, ctx LintContext) (bool, string) {
			for _, want := range ctx.Deliverables {
				// A named target file is checkable directly: prefer the
				// artifact over the attempt.
				if strings.EqualFold(strings.TrimSpace(want.Kind), "file") && want.Path != "" {
					if fileArtifactExists(want.Path) {
						continue
					}
					return false, "the task asked you to produce " + want.Path +
						", but no such file exists on disk yet (or it is empty — a write may have " +
						"been truncated). Actually write the file, verify it exists, then finish; " +
						"or call task_blocked with the concrete blocker."
				}
				if reason := unmetToolContract(ctx, want.SatisfiedBy, want.Description,
					"perform the delivery"); reason != "" {
					return false, reason
				}
			}
			return true, ""
		},
	}
}

// RequestedActionContract enforces the other half of the same idea: when the
// user asked the agent to DO something with a tool — set a reminder, add a
// schedule, record a note — the run cannot be declared complete while the tool
// that does it sat unused.
//
// This exists because the deliverable extraction is biased hard toward
// reporting nothing, and that bias explicitly exempts reminders and calendar
// entries: over-extracting a deliverable puts an ordinary question under a
// contract it can never satisfy. The exemption left a hole the benchmark walked
// straight into — four separate tasks where the model wrote "I've set a
// reminder for you" and never called the tool sitting right there. Requested
// actions are their own category so the exemption can stay and the hole can
// close.
func RequestedActionContract() OutputLint {
	return LintFunc{
		NameValue: "requested_action_contract",
		Fn: func(_ string, ctx LintContext) (bool, string) {
			for _, want := range ctx.RequestedActions {
				if reason := unmetToolContract(ctx, want.SatisfiedBy, want.Description,
					"carry out that action"); reason != "" {
					return false, reason
				}
			}
			return true, ""
		},
	}
}

// unmetToolContract returns the rejection reason when the run had the named
// tool and never called it, or "" when the contract is satisfied (or not
// enforceable). Shared by both contracts so their semantics cannot drift.
func unmetToolContract(ctx LintContext, tool, description, verb string) string {
	if tool == "" {
		// No available tool can do this. Rejecting here would burn the retry
		// budget on something the agent cannot do; that case belongs to
		// task_blocked, or to the partial-answer redirect.
		return ""
	}
	if toolWasCalled(ctx.ToolCalls, tool) {
		return ""
	}
	if !toolIsAvailable(ctx.AvailableTools, tool) {
		// The tool the extraction named is not registered for this run after
		// all; treat it as a missing capability rather than a failure.
		return ""
	}
	detail := ""
	if strings.TrimSpace(description) != "" {
		detail = " (" + strings.TrimSpace(description) + ")"
	}
	return "the user explicitly asked you to " + verb + detail +
		", and the tool " + tool + " that does it was available to you, but you never called it. " +
		"Do not tell the user it is done when it is not. Call " + tool + " now and report what it " +
		"returned, or call task_blocked stating plainly that you did not do it and why."
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
