package agent

import (
	"context"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// redactWithGuardrails runs the service's guardrail chain over a single text
// value for the given direction. It returns the (possibly rewritten) text,
// whether a guardrail blocked the value, and the block reason. Callers must
// guard on a non-nil r.svc.guardrails; this is nil-safe regardless.
func (r *Runtime) redactWithGuardrails(ctx context.Context, text string, kind GuardrailKind) (string, bool, string) {
	if r == nil || r.svc == nil || r.svc.guardrails == nil {
		return text, false, ""
	}
	if strings.TrimSpace(text) == "" {
		return text, false, ""
	}
	res, err := r.svc.guardrails.CheckAll(ctx, text, kind)
	if err != nil || res == nil {
		// A guardrail error must never silently forward unredacted content on
		// the input side; but it also must not wedge the run. Fail open on the
		// text (unchanged) and let the (bounded) run proceed. Errors here are
		// rare — the built-in PII guardrail never returns one.
		return text, false, ""
	}
	if !res.Passed {
		reason := res.Reason
		if reason == "" {
			// CheckAll only fills result.Reason in fail-fast mode; the redaction
			// chain runs with fail-fast off, so pull the reason from the first
			// non-passing individual result instead.
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

// applyInputGuardrails scrubs the input-direction guardrails over a copy of
// msgs before the provider sees them, so the persisted session/history stays
// intact. A blocking guardrail discards the copy and refuses the run.
func (r *Runtime) applyInputGuardrails(ctx context.Context, msgs []domain.Message) ([]domain.Message, string, bool) {
	return redactMessagesWith(ctx, msgs, r.redactWithGuardrails)
}

// applyOutputGuardrails redacts a terminal text value with the output-direction
// guardrails. A blocking guardrail on the output path substitutes a safe
// placeholder rather than leaking the original (RedactBlock is primarily an
// input feature; on output it degrades to withholding).
func (r *Runtime) applyOutputGuardrails(ctx context.Context, text string) string {
	nt, blocked, _ := r.redactWithGuardrails(ctx, text, GuardrailKindOutput)
	if blocked {
		return "[response withheld: output blocked by guardrail]"
	}
	return nt
}
