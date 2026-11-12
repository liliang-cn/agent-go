package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// Giving up before trying anything.
//
// Asked for a fifteen-slide industry report saved as a .pptx, a live run made
// one model call, wrote 88 tokens and ended in task_blocked: "no presentation
// tool". 85 of its 255 tools were in the schema and the rest sat behind a tool
// search it never ran. It had a shell with python3 on the machine,
// and web search for the figures. Tool time for the whole run: 0.00s, 0 calls.
//
// The model read "no tool is named for this" as "this cannot be done", and
// the runtime took its word for it: a task_blocked that carries text ends the
// run whether or not anything was attempted. But a blocker is a finding, and a
// run that has not called one tool has not found anything — it has guessed.
//
// So a block from a run that has done no work is sent back, with what to do
// about it: look in the catalogue, go through the tools there are, take the
// first step. It is the runtime that says so, not the prompt, because the
// prompt already said to check the tools first and the model stopped anyway.
//
// The test is what the run did — its tool calls against the catalogue it was
// given — never what the request says. Nothing here knows what a presentation
// is; the same run asking for a spreadsheet, a deployment or a translation is
// caught by the same line.
//
// It is bounded. Blocking with nothing tried is sometimes right — the request
// needs an answer only the person can give, or must not be carried out at all —
// and the push says so and leaves that door open. One push for every such run;
// a second, firmer one only for a run that answered the first by searching the
// catalogue and stopping there. After that the block stands.

// maxUntriedBlockPushbacks is how many times one run can have a block sent
// back for having tried nothing.
const maxUntriedBlockPushbacks = 2

// countsAsAttempt reports whether calling this tool is work on the task.
//
// The terminal tools end a run and the tool search looks for a way to start
// one; neither is an attempt. A search that came back empty is the usual last
// step before giving up, so counting it would let through exactly the run this
// file is about. Everything else counts, read-only or not: a run that read the
// file it was asked to change and then stopped has at least met the problem.
func countsAsAttempt(toolName string) bool {
	name := strings.TrimSpace(toolName)
	return name != "" && !isTaskTerminalToolName(name) && !isSearchToolName(name)
}

// attemptedAnything reports whether any call in the trace is work on the task.
func attemptedAnything(toolCalls []string) bool {
	for _, name := range toolCalls {
		if countsAsAttempt(name) {
			return true
		}
	}
	return false
}

// untriedBlockPrompt is the first push: the run is about to stop having called
// nothing.
//
// It is built from what the run did and what it has, and from nothing else:
// how many tools there are, how many of them it called, whether it looked in
// the catalogue. It does not read the request and it does not sort tools by
// what their names suggest — which tool could do the job is the model's
// judgement to make, with the catalogue in front of it. An earlier draft
// picked "the shell" and "the web search" out of the catalogue by name and
// named them in the push; that is a word list, it only knows the names
// somebody thought of, and it would have steered a run toward a tool because
// of how it is spelled.
func untriedBlockPrompt(available int, searched bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[system] You are about to stop, and this run has not tried anything: it has %d tools and has not made one call that works on the task. ", available)
	b.WriteString("That no tool is named for the job is not a blocker. Make a real attempt first:\n")
	if !searched {
		b.WriteString("- Your schema may show only part of the tool catalogue. Search it once for what you need.\n")
	}
	b.WriteString("- Go through the tools you do have and ask what each could contribute. A tool made for the job is rarely needed: " +
		"general tools — one that runs commands or code, one that reads and writes files, one that looks things up — " +
		"reach most results in a few steps, and what is missing on the machine can usually be installed.\n")
	b.WriteString("- Do the part that can be done, and find out by doing where it actually stops.\n")
	b.WriteString("Call task_blocked again only when an attempt has failed, and then say what you tried and what happened — " +
		"or when nothing you could run would help: the request needs an answer only the user can give, or must not be carried out.")
	return b.String()
}

// untriedBlockSecondPrompt is the second and last push, for a run that
// answered the first by looking in the catalogue and stopping there.
func untriedBlockSecondPrompt() string {
	return "[system] You are stopping again without having run anything. Looking for a ready-made tool and not finding one is not an attempt. " +
		"Pick the most general tool you have and take the first concrete step with it now, then look at what it returned. " +
		"If that fails, call task_blocked with what you ran and the error it gave. This is the last time the block is sent back."
}

// redirectUntriedBlock intercepts a model-initiated block on a run that has
// not worked on the task at all, and asks the loop for another turn with the
// push above. See the top of this file for why, and for why it is bounded.
//
// Returns true when the loop should continue instead of terminating.
func (r *Runtime) redirectUntriedBlock(ctx context.Context, messages *[]domain.Message, state *queryLoopState) bool {
	if r == nil || r.untriedBlockPushbacks >= maxUntriedBlockPushbacks {
		return false
	}
	// The missing-delivery redirect has already had this run: it told the
	// model the delivery is impossible here and to answer the rest. A second
	// voice saying "try harder" on top of that would contradict it.
	if r.deliverableFallbackUsed {
		return false
	}
	// A run that may not use tools has nothing to try with. Both ways of
	// saying so: the caller's (WithToolsDisabled) and the user's own words,
	// which the constraint check resolves. Bounded like the lints — a block is
	// not held for the whole check.
	if r.cfg != nil && r.cfg.ToolsDisabled {
		return false
	}
	if r.runConstraintsWithin(constraintLintWait).ForbidTools {
		return false
	}
	used := r.toolNamesUsedForTask()
	if attemptedAnything(used) {
		return false
	}
	available := r.availableToolNamesSnapshot()
	if !attemptedAnything(available) {
		// Nothing but the terminal tools and the search: there is nothing to
		// push the model toward.
		return false
	}
	real := 0
	for _, name := range available {
		if countsAsAttempt(name) {
			real++
		}
	}
	searched := false
	for _, name := range used {
		if isSearchToolName(name) {
			searched = true
			break
		}
	}

	var prompt string
	switch r.untriedBlockPushbacks {
	case 0:
		prompt = untriedBlockPrompt(real, searched)
		r.untriedBlockSearchedAtPush = searched
	default:
		// A second push only for a run that moved: it searched the catalogue
		// because it was told to, and stopped at the search. A run that
		// answered the first push with the same block has heard everything
		// there is to say, and gets to stop.
		if !searched || r.untriedBlockSearchedAtPush {
			return false
		}
		prompt = untriedBlockSecondPrompt()
	}
	r.untriedBlockPushbacks++

	trace.Decide(ctx, "untried_block",
		"the run called task_blocked without one tool call that works on the task; the block is sent back",
		map[string]any{"pushback": r.untriedBlockPushbacks, "searched": searched, "tools": real})

	*messages = append(*messages, domain.Message{Role: "user", Content: prompt})
	if state != nil {
		state.Messages = *messages
		state.setLoopTransition(queryLoopTransitionNextTurn,
			"blocked redirected: nothing was tried, attempt the task first")
		state.noteRoundCompleted()
	}
	return true
}
