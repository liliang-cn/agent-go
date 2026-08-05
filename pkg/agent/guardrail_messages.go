package agent

import (
	"context"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// redactTextFn rewrites one text value, reporting (text, blocked, reason).
// Both Service and Runtime expose one; see redactMessagesWith.
type redactTextFn func(ctx context.Context, text string, kind GuardrailKind) (string, bool, string)

// redactMessagesWith redacts the non-system messages in a COPY of msgs, leaving
// the caller's slice (and the persisted session) untouched — only what is handed
// to the provider is scrubbed. Roles, tool_calls and tool_call_ids are preserved
// verbatim so tool-call pairing is never broken. Returns (redactedCopy, reason,
// blocked); on block the copy is nil.
//
// Service and Runtime each hold their own guardrail chain and used to carry a
// near-identical copy of this walk. They drifted: a fix applied to one did not
// reach the other. One implementation, two thin wrappers.
func redactMessagesWith(ctx context.Context, msgs []domain.Message, redact redactTextFn) ([]domain.Message, string, bool) {
	out := make([]domain.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		// The system prompt is authored by the app, not the user.
		if out[i].Role == "system" {
			continue
		}
		if c := out[i].Content; c != "" {
			nc, blocked, reason := redact(ctx, c, GuardrailKindInput)
			if blocked {
				return nil, reason, true
			}
			out[i].Content = nc
		}
		if len(out[i].Parts) > 0 {
			newParts := make([]domain.MessagePart, len(out[i].Parts))
			copy(newParts, out[i].Parts)
			for j := range newParts {
				if newParts[j].Type != domain.MessagePartTypeText || newParts[j].Text == "" {
					continue
				}
				nt, blocked, reason := redact(ctx, newParts[j].Text, GuardrailKindInput)
				if blocked {
					return nil, reason, true
				}
				newParts[j].Text = nt
			}
			out[i].Parts = newParts
		}
	}
	return out, "", false
}
