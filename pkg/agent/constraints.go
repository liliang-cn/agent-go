package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
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

	// RequestedActions are tool actions the user explicitly asked the agent to
	// carry out on their behalf — set a reminder, add a calendar entry, record
	// a note. They are deliberately a separate category from Deliverables:
	// the deliverable extraction is biased hard toward reporting nothing
	// (over-extraction puts an ordinary question under a contract it can never
	// satisfy), and that bias exempts exactly these actions. The result was the
	// most expensive failure in the benchmark — the model writing "I've set a
	// reminder for you" without ever calling set_reminder, with no lint able to
	// see it. The requested-action contract closes that hole.
	RequestedActions []RequestedAction
}

// RequestedAction is one tool action the user asked the run to perform.
type RequestedAction struct {
	// Kind is one of "reminder", "calendar", "note", "other". It is
	// descriptive only — nothing in the runtime maps a kind to a tool.
	Kind string `json:"kind"`
	// Description is the user's own words for the action.
	Description string `json:"description"`
	// SatisfiedBy names the available tool that performs this action, chosen
	// by the extraction from the run's own tool catalog. Empty means this run
	// has no tool for it, and the action is not enforced.
	SatisfiedBy string `json:"satisfied_by,omitempty"`
}

// DeliverableRequirement is one side effect the run owes the user.
type DeliverableRequirement struct {
	// Kind is one of "email", "file", "message", "other".
	Kind string `json:"kind"`
	// Description is the user's own words for what has to be delivered.
	Description string `json:"description"`
	// Path is the target file path, when the user named one.
	Path string `json:"path,omitempty"`
	// SatisfiedBy names the available tool that performs this delivery, as
	// chosen by the extraction from the run's own tool catalog. Empty means no
	// available tool can do it — the run is then steered toward answering the
	// computable part rather than being punished for a missing capability.
	//
	// This is deliberately the model's judgement rather than a name table in
	// this package: a table only ever covers the tool names somebody thought to
	// list, and every embedder names their tools differently.
	SatisfiedBy string `json:"satisfied_by,omitempty"`
}

// Empty reports whether the constraints ask for nothing.
func (c RunConstraints) Empty() bool {
	return !c.ForbidTools && len(c.Deliverables) == 0 && len(c.RequestedActions) == 0
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
						"description":  map[string]interface{}{"type": "string"},
						"path":         map[string]interface{}{"type": "string"},
						"satisfied_by": satisfiedBySchema(),
					},
					"required": []string{"kind", "description", "satisfied_by"},
				},
			},
			"requested_actions": map[string]interface{}{
				"type": "array",
				"description": "Actions the user explicitly asked the assistant to carry out " +
					"for them — setting a reminder or alarm, adding a calendar entry or " +
					"schedule, recording a note. Empty when the user asked for none.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"kind": map[string]interface{}{
							"type": "string",
							"enum": []string{"reminder", "calendar", "note", "other"},
						},
						"description":  map[string]interface{}{"type": "string"},
						"satisfied_by": satisfiedBySchema(),
					},
					"required": []string{"kind", "description", "satisfied_by"},
				},
			},
		},
		"required": []string{"forbid_tools", "deliverables", "requested_actions"},
	}
}

// satisfiedBySchema describes the tool-selection field shared by deliverables
// and requested actions. The runtime never guesses which tool performs an
// action; the extraction picks it out of the catalog it was shown, and the
// lints do nothing but compare that name against the trace.
func satisfiedBySchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "string",
		"description": "The exact name of the one available tool that performs this, " +
			"copied from the AVAILABLE TOOLS list. Empty string when no listed tool can do it.",
	}
}

// toolCatalogEntry is one line of the tool catalog shown to the extraction.
type toolCatalogEntry struct {
	Name        string
	Description string
}

// maxCatalogDescriptionChars keeps the catalog to roughly one line per tool.
// The extraction runs once per run, and a full schema dump would make an
// always-on precondition check cost more than the work it guards.
const maxCatalogDescriptionChars = 100

// constraintToolCatalog lists the tools this run could call, as name plus a
// one-line description. It reads the same registry that
// Runtime.availableToolNamesSnapshot feeds the lints, so the tool the
// extraction picks is by construction a tool the lint can look for.
func (s *Service) constraintToolCatalog() []toolCatalogEntry {
	if s == nil || s.toolRegistry == nil {
		return nil
	}
	names := s.toolRegistry.Names()
	sort.Strings(names)
	out := make([]toolCatalogEntry, 0, len(names))
	for _, name := range names {
		def, ok := s.toolRegistry.DefinitionOf(name)
		desc := ""
		if ok {
			desc = strings.TrimSpace(def.Function.Description)
		}
		if idx := strings.IndexAny(desc, ".\n"); idx > 0 {
			desc = desc[:idx]
		}
		if len(desc) > maxCatalogDescriptionChars {
			desc = strings.TrimSpace(desc[:maxCatalogDescriptionChars]) + "…"
		}
		out = append(out, toolCatalogEntry{Name: name, Description: desc})
	}
	return out
}

// renderToolCatalog formats the catalog block appended to the extraction
// prompt. An empty catalog renders an explicit "none", so the model is told
// there is nothing to pick rather than left to imagine a tool.
func renderToolCatalog(entries []toolCatalogEntry) string {
	var b strings.Builder
	b.WriteString("\nAVAILABLE TOOLS (satisfied_by must be one of these names, or empty):\n")
	if len(entries) == 0 {
		b.WriteString("(none)\n")
		return b.String()
	}
	for _, e := range entries {
		b.WriteString("- ")
		b.WriteString(e.Name)
		if e.Description != "" {
			b.WriteString(": ")
			b.WriteString(e.Description)
		}
		b.WriteString("\n")
	}
	return b.String()
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
  - Setting a reminder, adding a calendar entry, or recording a note is NOT a deliverable — report those under requested_actions instead.
  - If the request only asks you to find something out and tell the user, deliverables MUST be empty.
- requested_actions: ONLY actions the user explicitly asked YOU to carry out on their behalf, such as setting a reminder or alarm, adding a calendar entry or schedule, or recording a note for later.
  - kind: "reminder" for a reminder/alarm/timer, "calendar" for a calendar entry/schedule/event, "note" for something recorded for later, "other" for anything else the user asked you to perform.
  - Report the action only if the user asked for it in their own words. A calculation, a translation, a lookup, or an explanation is NOT a requested action.
  - Never invent an action because it would be helpful. When unsure, report nothing.
- satisfied_by (on every deliverable and every requested action): pick the ONE tool from the AVAILABLE TOOLS list below that would carry it out, and copy its name exactly. Use "" when no listed tool can carry it out. Never invent a name that is not on the list.
- Work in whatever language the request is written in.
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
		declared.RequestedActions = cfg.RequiredActions
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

	catalog := s.constraintToolCatalog()
	prompt := constraintExtractionPrompt + renderToolCatalog(catalog) + "\nUser request:\n" + goal

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
			return pruneUnknownTools(parsed, catalog)
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
		ForbidTools      bool                     `json:"forbid_tools"`
		Deliverables     []DeliverableRequirement `json:"deliverables"`
		RequestedActions []RequestedAction        `json:"requested_actions"`
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
			SatisfiedBy: strings.TrimSpace(d.SatisfiedBy),
		})
	}
	for _, a := range payload.RequestedActions {
		kind := strings.ToLower(strings.TrimSpace(a.Kind))
		switch kind {
		case "reminder", "calendar", "note", "other":
		default:
			// Unknown kind: keep the action but treat it generically rather
			// than dropping something the user explicitly asked for.
			kind = "other"
		}
		out.RequestedActions = append(out.RequestedActions, RequestedAction{
			Kind:        kind,
			Description: strings.TrimSpace(a.Description),
			SatisfiedBy: strings.TrimSpace(a.SatisfiedBy),
		})
	}
	return out, nil
}

// pruneUnknownTools drops any satisfied_by the extraction invented. A lint that
// waits for a tool nobody registered can never be satisfied, so a hallucinated
// name would turn into a guaranteed block; clearing it degrades to "no tool can
// do this", which is the safe reading.
func pruneUnknownTools(in RunConstraints, catalog []toolCatalogEntry) RunConstraints {
	known := make(map[string]bool, len(catalog))
	for _, e := range catalog {
		known[e.Name] = true
	}
	for i := range in.Deliverables {
		if !known[in.Deliverables[i].SatisfiedBy] {
			in.Deliverables[i].SatisfiedBy = ""
		}
	}
	for i := range in.RequestedActions {
		if !known[in.RequestedActions[i].SatisfiedBy] {
			in.RequestedActions[i].SatisfiedBy = ""
		}
	}
	return in
}
