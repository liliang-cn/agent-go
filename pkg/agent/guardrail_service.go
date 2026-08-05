package agent

import (
	"context"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
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

// redactInputMessages scrubs the input-direction guardrails over a copy of
// msgs. Sub-agents only hold a *Service, so this is their route to the same
// protection the main loop gets.
func (s *Service) redactInputMessages(ctx context.Context, msgs []domain.Message) ([]domain.Message, string, bool) {
	if s == nil || s.guardrails == nil {
		return msgs, "", false
	}
	return redactMessagesWith(ctx, msgs, s.redactWithGuardrails)
}
