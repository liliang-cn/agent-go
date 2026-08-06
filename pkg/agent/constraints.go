package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

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

// constraintExtractionSchema is the JSON schema handed to the model. It is
// deliberately small: two fields, closed enum, no free-form reasoning. A bigger
// schema costs tokens on every run and gives the model room to editorialise.
var constraintExtractionSchema = map[string]interface{}{
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

// constraintExtractionPrompt is language-agnostic on purpose: it names no
// phrases and no keywords, so it works for whatever the user actually wrote.
const constraintExtractionPrompt = `Read the user request below and report ONLY the constraints the user stated explicitly.

Rules:
- Report a constraint only if the user actually said it. Never infer, never guess at intent.
- forbid_tools: true only when the user explicitly refused the use of tools. A request that merely does not need tools is NOT a refusal.
- deliverables: list only side effects the user explicitly asked to be performed (send an email, write a file, post a message, ...). Asking a question is not a deliverable. Producing an answer in the reply is not a deliverable.
- Work in whatever language the request is written in.

User request:
`

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

// extractRunConstraints makes the single structured call. Any failure degrades
// to "no constraints" — a provider that cannot do structured output must not be
// able to block an otherwise ordinary run.
func (s *Service) extractRunConstraints(ctx context.Context, goal string) RunConstraints {
	res, err := s.llmService.GenerateStructured(
		ctx,
		constraintExtractionPrompt+goal,
		constraintExtractionSchema,
		&domain.GenerationOptions{Temperature: 0, MaxTokens: 400},
	)
	if err != nil {
		s.logger.Warn("constraint extraction failed; running without constraints",
			slog.String("error", err.Error()))
		return RunConstraints{}
	}
	if res == nil {
		return RunConstraints{}
	}

	var payload struct {
		ForbidTools  bool                     `json:"forbid_tools"`
		Deliverables []DeliverableRequirement `json:"deliverables"`
	}
	raw := strings.TrimSpace(res.Raw)
	if raw == "" && res.Data != nil {
		if encoded, encErr := json.Marshal(res.Data); encErr == nil {
			raw = string(encoded)
		}
	}
	if raw == "" {
		return RunConstraints{}
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		s.logger.Warn("constraint extraction returned unparsable JSON; running without constraints",
			slog.String("error", err.Error()))
		return RunConstraints{}
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
	return out
}
