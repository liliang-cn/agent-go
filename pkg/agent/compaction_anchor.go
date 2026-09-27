package agent

import (
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// Sizing the context the way the provider does.
//
// The compaction trigger used to size the history with a tokenizer estimate
// alone, while the provider's own count of the same prompt sat in the round's
// usage, recorded and never consulted. The estimate is wrong in two ways that
// do not cancel: it uses tiktoken's vocabulary whatever the model is, and it
// counts only the conversation — not the system prompt, not the tool schemas,
// which on a tool-heavy agent are thousands of tokens the provider bills on
// every turn.
//
// So the count anchors on the last reported prompt: that number is exact for
// the messages it covered, and only what has been appended since is
// estimated. A rewrite of the history (compaction) moves the anchor rather
// than trusting it blindly: clipping keeps the message count and shifts the
// anchor by what it removed, a fold drops it until the next report.

// usageAnchor is the provider's prompt count for a known prefix of the loop's
// message slice.
type usageAnchor struct {
	promptTokens int
	msgCount     int
	// lastSig fingerprints msgs[msgCount-1], so a rewrite of the prefix that
	// kept its length is not mistaken for the history the provider counted.
	lastSig string
	valid   bool
}

func messageSignature(m domain.Message) string {
	return fmt.Sprintf("%s|%d|%d|%s|%d", m.Role, len(m.Content), len(m.ToolCalls), m.ToolCallID, len(m.Parts))
}

// newUsageAnchor records a provider report for the messages that produced it.
func newUsageAnchor(promptTokens int, msgs []domain.Message) usageAnchor {
	if promptTokens <= 0 || len(msgs) == 0 {
		return usageAnchor{}
	}
	return usageAnchor{
		promptTokens: promptTokens,
		msgCount:     len(msgs),
		lastSig:      messageSignature(msgs[len(msgs)-1]),
		valid:        true,
	}
}

// covers reports whether the anchor still describes a prefix of msgs.
func (a usageAnchor) covers(msgs []domain.Message) bool {
	return a.valid && a.msgCount > 0 && len(msgs) >= a.msgCount &&
		messageSignature(msgs[a.msgCount-1]) == a.lastSig
}

// anchoredTokens sizes msgs: the anchored prompt count plus an estimate of
// what was appended after it, or a pure estimate when no anchor covers msgs.
// The second return says which.
func anchoredTokens(tc *pool.TokenCounter, model string, a usageAnchor, msgs []domain.Message) (int, bool) {
	if tc == nil {
		return 0, false
	}
	if a.covers(msgs) {
		return a.promptTokens + tc.EstimateConversationTokens(msgs[a.msgCount:], model), true
	}
	return tc.EstimateConversationTokens(msgs, model), false
}

// contextTokens is the loop's size of what the next request will carry:
// anchored on the last provider report where one covers the history, and
// otherwise the history's estimate plus the request overhead — the system
// prompt and tool schemas the last turn was sent with, which the provider
// counts and the message slice does not contain.
func (r *Runtime) contextTokens(msgs []domain.Message) int {
	if r == nil || r.svc == nil || r.svc.tokenCounter == nil {
		return 0
	}
	n, anchored := anchoredTokens(r.svc.tokenCounter, r.svc.Info().Model, r.anchor, msgs)
	if !anchored {
		n += r.requestOverhead
	}
	return n
}

// requestOverheadTokens sizes what a request carries beyond the loop's
// message slice: the system messages the turn assembly put in front of it,
// and the tool schemas.
func requestOverheadTokens(tc *pool.TokenCounter, model string, history, sent []domain.Message, tools []domain.ToolDefinition) int {
	if tc == nil {
		return 0
	}
	sys := tc.EstimateConversationTokens(sent[:leadingSystemCount(sent)], model) -
		tc.EstimateConversationTokens(history[:leadingSystemCount(history)], model)
	if sys < 0 {
		sys = 0
	}
	return sys + tc.EstimateToolsTokens(tools, model)
}

// noteRequestOverhead records the overhead of the turn about to be sent.
func (r *Runtime) noteRequestOverhead(history, sent []domain.Message, tools []domain.ToolDefinition) {
	if r == nil || r.svc == nil || r.svc.tokenCounter == nil {
		return
	}
	r.requestOverhead = requestOverheadTokens(r.svc.tokenCounter, r.svc.Info().Model, history, sent, tools)
}

// noteReportedUsage records the provider's prompt count for the messages the
// turn was built from.
func (r *Runtime) noteReportedUsage(usage *domain.TokenUsage, msgs []domain.Message) {
	if r == nil || usage == nil || usage.PromptTokens <= 0 {
		return
	}
	r.anchor = newUsageAnchor(usage.PromptTokens, msgs)
}

// shiftAnchorForClip keeps the anchor valid across a clip. Clipping preserves
// the message count and replaces content in place, so the provider's count of
// the old prefix minus the estimated size of what was removed is still the
// best number available for the new one.
func (r *Runtime) shiftAnchorForClip(before, after []domain.Message) {
	if r == nil || !r.anchor.valid || r.svc == nil || r.svc.tokenCounter == nil || len(before) != len(after) {
		r.dropAnchor()
		return
	}
	if !r.anchor.covers(before) {
		r.dropAnchor()
		return
	}
	model := r.svc.Info().Model
	tc := r.svc.tokenCounter
	removed := 0
	for i := 0; i < r.anchor.msgCount; i++ {
		if before[i].Content == after[i].Content {
			continue
		}
		removed += tc.EstimateTokens(before[i].Content, model) - tc.EstimateTokens(after[i].Content, model)
	}
	a := r.anchor
	a.promptTokens -= removed
	if a.promptTokens <= 0 {
		r.dropAnchor()
		return
	}
	a.lastSig = messageSignature(after[a.msgCount-1])
	r.anchor = a
}

func (r *Runtime) dropAnchor() {
	if r != nil {
		r.anchor = usageAnchor{}
	}
}

// compactionThreshold resolves the run's threshold once and remembers it. A
// run that falls back to the fixed default because nothing knows the model's
// window says so, once, the way an unpriced model does.
func (r *Runtime) compactionThreshold() (int, string) {
	if r == nil {
		return CompactionDefaultThresholdTokens, CompactionThresholdDefault
	}
	if r.thresholdSource != "" {
		return r.threshold, r.thresholdSource
	}
	configured := 0
	if r.cfg != nil {
		configured = r.cfg.CompactionThresholdTokens
	}
	model := ""
	if r.svc != nil {
		model = r.svc.Info().Model
	}
	if r.cfg != nil && r.cfg.Model != "" {
		model = r.cfg.Model
	}
	r.threshold, r.thresholdSource = resolveCompactionThreshold(configured, model, r.maxTokens())
	if r.thresholdSource == CompactionThresholdDefault {
		log.Warn("no context window known for model; compaction falls back to a fixed threshold",
			"module", "agent.runtime", "model", model, "threshold", r.threshold,
			"fix", "pool.RegisterModelWindow(\""+model+"\", pool.ModelWindow{ContextTokens: …})")
	}
	return r.threshold, r.thresholdSource
}
