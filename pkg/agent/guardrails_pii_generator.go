package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// PII redaction at the provider boundary.
//
// The guardrail seam in Runtime covers the conversation turn, but that is not
// the only place text leaves the process: the planner asks the model to
// classify intent, MCP and memory services hold the same Generator, and any
// future helper call would be another hole. Each of those would have to
// remember to redact.
//
// Wrapping the Generator itself makes the boundary the enforcement point, so
// "PII redaction is on" means every prompt and message body is scrubbed on the
// way out, no matter which code path built it.
//
// Redaction is idempotent — a masked value no longer matches the detectors —
// so text that already went through the Runtime seam is unaffected here.

// piiRedactingGenerator scrubs PII from everything handed to the wrapped
// Generator. In RedactBlock mode a detection is refused with an error instead
// of being rewritten.
type piiRedactingGenerator struct {
	inner domain.Generator
	dets  []piiDetector
	mode  RedactMode
}

// newPIIRedactingGenerator wraps gen so no prompt reaches the provider with PII
// in it. Returns gen untouched when there is nothing to enforce.
func newPIIRedactingGenerator(gen domain.Generator, kinds []PIIKind, mode RedactMode) domain.Generator {
	if gen == nil {
		return nil
	}
	if mode == "" {
		mode = RedactPartial
	}
	if len(kinds) == 0 {
		kinds = AllPIIKinds
	}
	dets := detectorsFor(kinds)
	if len(dets) == 0 {
		return gen
	}
	return &piiRedactingGenerator{inner: gen, dets: dets, mode: mode}
}

// scrub redacts one string, or reports why the call must not proceed.
func (g *piiRedactingGenerator) scrub(s string) (string, error) {
	if s == "" {
		return s, nil
	}
	redacted, detected, blocked := redactPII(s, g.dets, g.mode)
	if blocked {
		kinds := make([]string, len(detected))
		for i, k := range detected {
			kinds[i] = string(k)
		}
		return "", fmt.Errorf("pii guardrail blocked the request: content contains %s", strings.Join(kinds, ", "))
	}
	return redacted, nil
}

// scrubMessages redacts a COPY of msgs, leaving the caller's slice (and any
// persisted session) intact. Roles, tool calls and tool_call_ids are preserved
// verbatim so tool-call pairing is never broken.
func (g *piiRedactingGenerator) scrubMessages(msgs []domain.Message) ([]domain.Message, error) {
	out := make([]domain.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		// The system prompt is authored by the app, not the user.
		if out[i].Role == "system" {
			continue
		}
		if out[i].Content != "" {
			scrubbed, err := g.scrub(out[i].Content)
			if err != nil {
				return nil, err
			}
			out[i].Content = scrubbed
		}
		if len(out[i].Parts) > 0 {
			parts := make([]domain.MessagePart, len(out[i].Parts))
			copy(parts, out[i].Parts)
			for j := range parts {
				if parts[j].Type != domain.MessagePartTypeText || parts[j].Text == "" {
					continue
				}
				scrubbed, err := g.scrub(parts[j].Text)
				if err != nil {
					return nil, err
				}
				parts[j].Text = scrubbed
			}
			out[i].Parts = parts
		}
	}
	return out, nil
}

func (g *piiRedactingGenerator) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	scrubbed, err := g.scrub(prompt)
	if err != nil {
		return "", err
	}
	return g.inner.Generate(ctx, scrubbed, opts)
}

func (g *piiRedactingGenerator) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	scrubbed, err := g.scrub(prompt)
	if err != nil {
		return err
	}
	return g.inner.Stream(ctx, scrubbed, opts, callback)
}

func (g *piiRedactingGenerator) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	scrubbed, err := g.scrubMessages(messages)
	if err != nil {
		return nil, err
	}
	return g.inner.GenerateWithTools(ctx, scrubbed, tools, opts)
}

func (g *piiRedactingGenerator) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	scrubbed, err := g.scrubMessages(messages)
	if err != nil {
		return err
	}
	return g.inner.StreamWithTools(ctx, scrubbed, tools, opts, callback)
}

func (g *piiRedactingGenerator) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	scrubbed, err := g.scrub(prompt)
	if err != nil {
		return nil, err
	}
	return g.inner.GenerateStructured(ctx, scrubbed, schema, opts)
}

func (g *piiRedactingGenerator) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	scrubbed, err := g.scrub(request)
	if err != nil {
		return nil, err
	}
	return g.inner.RecognizeIntent(ctx, scrubbed)
}

// NewSession forwards realtime sessions when the wrapped generator supports
// them, so wrapping does not silently drop the capability. Message bodies sent
// through a live session are scrubbed by realtimeSession below.
func (g *piiRedactingGenerator) NewSession(ctx context.Context, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (domain.RealtimeSession, error) {
	rt, ok := g.inner.(domain.RealtimeGenerator)
	if !ok {
		return nil, fmt.Errorf("realtime not supported by the underlying generator")
	}
	sess, err := rt.NewSession(ctx, tools, opts)
	if err != nil {
		return nil, err
	}
	return &piiRedactingSession{inner: sess, gen: g}, nil
}

// piiRedactingSession scrubs each message sent over a realtime session.
type piiRedactingSession struct {
	inner domain.RealtimeSession
	gen   *piiRedactingGenerator
}

func (s *piiRedactingSession) Send(ctx context.Context, message domain.Message) error {
	scrubbed, err := s.gen.scrubMessages([]domain.Message{message})
	if err != nil {
		return err
	}
	return s.inner.Send(ctx, scrubbed[0])
}

func (s *piiRedactingSession) Receive(ctx context.Context) (*domain.GenerationResult, error) {
	return s.inner.Receive(ctx)
}

func (s *piiRedactingSession) Close() error { return s.inner.Close() }
