package agent

import (
	"context"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// redactWithGuardrails runs the service's guardrail chain over one text value
// for the given direction. Nil-safe: no chain (default) → text unchanged.
// Returns (possibly-rewritten text, blocked, reason). This is the Service-level
// twin of the Runtime helper, so paths that only hold a *Service (sub-agents)
// get the same PII protection as the main loop.
func (s *Service) redactWithGuardrails(ctx context.Context, text string, kind GuardrailKind) (string, bool, string) {
	if s == nil || s.guardrails == nil || strings.TrimSpace(text) == "" {
		return text, false, ""
	}
	res, err := s.guardrails.CheckAll(ctx, text, kind)
	if err != nil || res == nil {
		// Fail open on the text (unchanged) but never wedge the run.
		return text, false, ""
	}
	if !res.Passed {
		reason := res.Reason
		if reason == "" {
			for _, rr := range res.Results {
				if rr != nil && !rr.Passed && rr.Reason != "" {
					reason = rr.Reason
					break
				}
			}
		}
		return text, true, reason
	}
	if res.Modified && res.FinalContent != "" {
		return res.FinalContent, false, ""
	}
	return text, false, ""
}

// redactInputMessages redacts the non-system messages in a COPY of msgs with
// the input-direction guardrails, leaving the caller's slice (and the persisted
// session) untouched — only what is handed to the provider is scrubbed. Roles,
// tool_calls and tool_call_ids are preserved verbatim so tool-call pairing is
// never broken. Returns (redactedCopy, reason, blocked); on block the copy is nil.
func (s *Service) redactInputMessages(ctx context.Context, msgs []domain.Message) ([]domain.Message, string, bool) {
	if s == nil || s.guardrails == nil {
		return msgs, "", false
	}
	out := make([]domain.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		if out[i].Role == "system" {
			continue
		}
		if c := out[i].Content; c != "" {
			nc, blocked, reason := s.redactWithGuardrails(ctx, c, GuardrailKindInput)
			if blocked {
				return nil, reason, true
			}
			out[i].Content = nc
		}
		if len(out[i].Parts) > 0 {
			newParts := make([]domain.MessagePart, len(out[i].Parts))
			copy(newParts, out[i].Parts)
			for j := range newParts {
				if newParts[j].Type == domain.MessagePartTypeText && newParts[j].Text != "" {
					nt, blocked, reason := s.redactWithGuardrails(ctx, newParts[j].Text, GuardrailKindInput)
					if blocked {
						return nil, reason, true
					}
					newParts[j].Text = nt
				}
			}
			out[i].Parts = newParts
		}
	}
	return out, "", false
}
