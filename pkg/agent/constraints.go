package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// RunConstraints are the machine-checkable things a user asked for that the
// runtime enforces itself rather than trusting the model to remember.
//
// They are derived once per run, from two sources in priority order:
//
//  1. what the embedder declared outright (WithToolsDisabled,
//     WithRequiredDeliverables) — always authoritative, no model call;
//  2. otherwise, a single structured extraction pass over the goal.
//
// The extraction exists because the alternative is a hardcoded phrase table
// ("without using any tools", "不要使用任何工具", …), which only ever works for
// the languages and phrasings someone thought to list. A user writing Japanese,
// or English the table didn't anticipate, silently got no enforcement at all.
type RunConstraints struct {
	// ForbidTools is set when the user explicitly refused tool use. The
	// runtime honours it by attaching no tools at all, so the instruction is
	// satisfied by construction rather than by asking the model to comply.
	ForbidTools bool

	// Deliverables are the concrete side effects the user asked for. The
	// delivery-contract lint refuses to let the run complete until each one
	// has a matching successful tool call.
	Deliverables []DeliverableRequirement
}

// DeliverableRequirement is one side effect the run owes the user.
type DeliverableRequirement struct {
	// Kind is one of "email", "file", "message", "other".
	Kind string `json:"kind"`
	// Description is the user's own words for what has to be delivered.
	Description string `json:"description"`
	// Path is the target file path, when the user named one.
	Path string `json:"path,omitempty"`
}

// Empty reports whether the constraints ask for nothing.
func (c RunConstraints) Empty() bool {
	return !c.ForbidTools && len(c.Deliverables) == 0
}

// newConstraintExtractionSchema builds the JSON schema handed to the model. It
// is deliberately small: two fields, a closed enum, no free-form reasoning. A
// bigger schema costs tokens on every run and gives the model room to
// editorialise.
//
// It is built per call rather than kept in a package-level var because
// providers receive it as an `interface{}` and are free to write through it —
// pkg/providers/openai.go hands it straight to json.Unmarshal. That particular
// call happens to be a no-op for a map (Unmarshal rejects a non-pointer map),
// but sharing one mutable map across concurrent runs is a race waiting for the
// next provider that does something slightly different.
func newConstraintExtractionSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"forbid_tools": map[string]interface{}{
				"type": "boolean",
				"description": "true only if the user explicitly forbade using tools " +
					"(in any language). false otherwise.",
			},
			"deliverables": map[string]interface{}{
				"type": "array",
				"description": "Concrete side effects the user explicitly asked for. " +
					"Empty when the user only asked a question.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"kind": map[string]interface{}{
							"type": "string",
							"enum": []string{"email", "file", "message", "other"},
						},
						"description": map[string]interface{}{"type": "string"},
						"path":        map[string]interface{}{"type": "string"},
					},
					"required": []string{"kind", "description"},
				},
			},
		},
		"required": []string{"forbid_tools", "deliverables"},
	}
}

// constraintExtractionPrompt is language-agnostic on purpose: it names no
// phrases and no keywords, so it works for whatever the user actually wrote.
//
// The bias is deliberately toward reporting nothing. A missed constraint costs
// one unenforced instruction; a hallucinated one puts an ordinary request under
// a contract it can never satisfy, and burns the lint retry budget doing it.
const constraintExtractionPrompt = `Read the user request below and report ONLY the constraints the user stated explicitly.

Output a single JSON object and nothing else. No prose, no markdown, no code fence.

Rules:
- Report a constraint only if the user actually said it. Never infer, never guess at intent. When unsure, report nothing.
- forbid_tools: true ONLY when the user explicitly refused the use of tools. A request that simply does not need tools is NOT a refusal.
- deliverables: ONLY side effects the user explicitly asked to be performed on something outside the conversation — sending a message somewhere, writing a file, posting to a service.
  - Asking a question is NOT a deliverable.
  - Asking for a calculation, a translation, a lookup, or an explanation is NOT a deliverable — the answer belongs in the reply.
  - Setting a reminder, adding a calendar entry, or recording a note is NOT a deliverable for this purpose.
  - If the request only asks you to find something out and tell the user, deliverables MUST be empty.
- Work in whatever language the request is written in.

User request:
`

// constraintExtractionTimeout bounds the whole extraction (both attempts).
// It is short on purpose: the extraction guards a minority of runs, so it must
// never be the reason an ordinary task runs out of time.
const constraintExtractionTimeout = 20 * time.Second

// constraintRetryInstruction is appended when the first reply was not parsable.
const constraintRetryInstruction = "\n\nYour previous reply was not valid JSON. Output ONLY the JSON object, with no other text."

// resolveRunConstraints returns the constraints for one run. It is called once
// per run from the loop, so every entry point (Run, RunStream, Ask, Chat, the
// prompt scheduler, and sub-agents) gets identical treatment — this is the
// single place the decision is made.
func (s *Service) resolveRunConstraints(ctx context.Context, goal string, cfg *RunConfig) RunConstraints {
	declared := RunConstraints{}
	if cfg != nil {
		declared.ForbidTools = cfg.ToolsDisabled
		declared.Deliverables = cfg.RequiredDeliverables
	}
	// An explicit declaration is authoritative and costs nothing: skip the
	// model call entirely.
	if !declared.Empty() {
		return declared
	}
	if cfg != nil && cfg.DisableConstraintExtraction {
		return declared
	}
	if strings.TrimSpace(goal) == "" || s == nil || s.llmService == nil {
		return declared
	}
	return s.extractRunConstraints(ctx, goal)
}

// extractRunConstraints makes the structured call, leniently parses whatever
// comes back, and retries once with explicit feedback before giving up. Any
// final failure degrades to "no constraints" — a provider that cannot do
// structured output must not be able to block an otherwise ordinary run.
func (s *Service) extractRunConstraints(ctx context.Context, goal string) RunConstraints {
	// The extraction is a precondition check, not the work. Bound it so a slow
	// or rate-limited gateway cannot spend the run's deadline before the first
	// real turn — the caller's budget belongs to the task, not to this.
	ctx, cancel := context.WithTimeout(ctx, constraintExtractionTimeout)
	defer cancel()

	prompt := constraintExtractionPrompt + goal

	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			s.logger.Warn("constraint extraction timed out; running without constraints")
			return RunConstraints{}
		}
		if attempt > 0 {
			prompt += constraintRetryInstruction
		}
		raw, err := s.callConstraintExtraction(ctx, prompt)
		if err != nil {
			s.logger.Warn("constraint extraction call failed",
				slog.Int("attempt", attempt+1),
				slog.String("error", err.Error()))
			continue
		}
		parsed, perr := parseRunConstraints(raw)
		if perr == nil {
			return parsed
		}
		s.logger.Warn("constraint extraction reply was not parsable JSON",
			slog.Int("attempt", attempt+1),
			slog.String("error", perr.Error()))
	}

	s.logger.Warn("constraint extraction gave up; running without constraints")
	return RunConstraints{}
}

// callConstraintExtraction issues one extraction request and returns the raw
// reply text.
func (s *Service) callConstraintExtraction(ctx context.Context, prompt string) (string, error) {
	res, err := s.llmService.GenerateStructured(
		ctx,
		prompt,
		newConstraintExtractionSchema(),
		&domain.GenerationOptions{Temperature: 0, MaxTokens: 400},
	)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", errNoJSONObject
	}
	raw := strings.TrimSpace(res.Raw)
	if raw == "" && res.Data != nil {
		if encoded, encErr := json.Marshal(res.Data); encErr == nil {
			raw = string(encoded)
		}
	}
	return raw, nil
}

// parseRunConstraints turns a model reply into constraints, tolerating code
// fences and prose around the object.
func parseRunConstraints(raw string) (RunConstraints, error) {
	if strings.TrimSpace(raw) == "" {
		return RunConstraints{}, errNoJSONObject
	}
	body, err := extractJSONObject(raw)
	if err != nil {
		return RunConstraints{}, err
	}

	var payload struct {
		ForbidTools  bool                     `json:"forbid_tools"`
		Deliverables []DeliverableRequirement `json:"deliverables"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return RunConstraints{}, err
	}

	out := RunConstraints{ForbidTools: payload.ForbidTools}
	for _, d := range payload.Deliverables {
		kind := strings.ToLower(strings.TrimSpace(d.Kind))
		switch kind {
		case "email", "file", "message", "other":
		default:
			// Unknown kind: keep the requirement but treat it generically
			// rather than dropping a side effect the user asked for.
			kind = "other"
		}
		out.Deliverables = append(out.Deliverables, DeliverableRequirement{
			Kind:        kind,
			Description: strings.TrimSpace(d.Description),
			Path:        strings.TrimSpace(d.Path),
		})
	}
	return out, nil
}
