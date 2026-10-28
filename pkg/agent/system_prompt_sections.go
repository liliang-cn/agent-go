package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/prompt"
)

const SystemPromptDynamicBoundary = "<SYSTEM_PROMPT_DYNAMIC_BOUNDARY>"

type systemPromptSection struct {
	name    string
	content string
	dynamic bool
}

type systemPromptOptions struct {
	// forbidTools drops every rule that teaches the model to reach for a tool.
	// The run will be offered none, so mentioning them only invites refusals.
	forbidTools bool
	// noToolSearch says the turn offers no tool search (nothing is deferred),
	// so neither the search rule nor the search-mode catalogue note applies.
	noToolSearch bool
	// projectInstructions is the run's snapshot of its instruction files.
	// When not resolved (a prompt built outside a run) they are read fresh.
	projectInstructions         string
	projectInstructionsResolved bool
}

type systemPromptSectionData struct {
	service *Service
	agent   *Agent
	options systemPromptOptions
	data    map[string]interface{}
	// projectInstructions is the rendered instruction files for this prompt.
	projectInstructions string
}

func (s *Service) renderPromptSection(key string, data map[string]interface{}) string {
	if s == nil || s.promptManager == nil {
		return ""
	}
	rendered, err := s.promptManager.Render(key, data)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(rendered)
}

func renderSystemPromptSections(sections []systemPromptSection) string {
	if len(sections) == 0 {
		return ""
	}

	out := make([]string, 0, len(sections)+1)
	insertedDynamicBoundary := false
	for _, section := range sections {
		content := strings.TrimSpace(section.content)
		if content == "" {
			continue
		}
		if section.dynamic && !insertedDynamicBoundary {
			out = append(out, SystemPromptDynamicBoundary)
			insertedDynamicBoundary = true
		}
		out = append(out, content)
	}
	return strings.Join(out, "\n\n")
}

func formatSystemPromptSectionsForDebug(sections []systemPromptSection) string {
	if len(sections) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("=== SYSTEM PROMPT SECTIONS ===\n")
	for _, section := range sections {
		content := strings.TrimSpace(section.content)
		if content == "" {
			continue
		}
		dynamic := "static"
		if section.dynamic {
			dynamic = "dynamic"
		}
		sb.WriteString("[" + section.name + " | " + dynamic + "]\n")
		sb.WriteString(content)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}

func (s *Service) ensureSystemPromptSectionRegistry() {
	if s == nil || s.promptManager == nil {
		return
	}

	s.promptManager.RegisterSection("identity", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		return prompt.Section{Name: "identity", Content: s.renderPromptSection(prompt.AgentSystemIdentity, data.data)}, nil
	})
	s.promptManager.RegisterSection("operational", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		return prompt.Section{Name: "operational", Content: s.renderPromptSection(prompt.AgentSystemOperational, data.data)}, nil
	})
	s.promptManager.RegisterSection("system_context", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		return prompt.Section{Name: "system_context", Content: s.renderPromptSection(prompt.AgentSystemContext, data.data)}, nil
	})
	s.promptManager.RegisterSection("project_instructions", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		return prompt.Section{Name: "project_instructions", Content: data.projectInstructions}, nil
	})
	s.promptManager.RegisterSection("memory", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		return prompt.Section{Name: "memory", Content: data.service.buildMemoryPromptNote(ctx, data.agent), Dynamic: true}, nil
	})
	s.promptManager.RegisterSection("messaging", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		return prompt.Section{Name: "messaging", Content: data.service.buildAgentMessagingPromptNote(ctx, data.agent), Dynamic: true}, nil
	})
	s.promptManager.RegisterSection("tool_catalog", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		if data.options.noToolSearch {
			return prompt.Section{Name: "tool_catalog", Dynamic: true}, nil
		}
		return prompt.Section{Name: "tool_catalog", Content: data.service.buildToolCatalogSummary(ctx), Dynamic: true}, nil
	})
	s.promptManager.RegisterSection("web_search", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		if alwaysFalse(data.agent) {
			return prompt.Section{Name: "web_search"}, nil
		}
		return prompt.Section{Name: "web_search", Content: data.service.buildWebSearchPromptNote(data.agent), Dynamic: true}, nil
	})
	s.promptManager.RegisterSection("frc", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		return prompt.Section{Name: "frc", Content: "After a tool result, act on it — another call or the answer — without restating it."}, nil
	})
	s.promptManager.RegisterSection("numeric_length_anchors", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		return prompt.Section{Name: "numeric_length_anchors", Content: "Length limits: ≤25 words between tool calls; final answers ≤100 words unless the task needs more."}, nil
	})
}

func (s *Service) buildSystemPromptSections(ctx context.Context, agent *Agent, opts systemPromptOptions) []systemPromptSection {
	s.ensureSystemPromptSectionRegistry()
	systemCtx := s.buildSystemContext()
	rules := []string{
		"- " + strings.ReplaceAll(FinishOrBlockContract, "\n", "\n- "),
		ReplyRules,
	}
	if opts.forbidTools {
		// No tools will be attached to this run, so every tool rule below is
		// noise at best and an invitation to a refused call at worst.
		rules = append(rules,
			"- No tools are available for this task. Answer directly from your own knowledge.",
		)
	} else {
		toolRule := "- Call only tools in your schema; never invent names. If none fits, search once (`search_available_tools`) — one search per task — then answer or block."
		if opts.noToolSearch {
			// Every tool there is is already in the schema.
			toolRule = "- Call only tools in your schema; never invent names. Every tool you have is in it."
		}
		rules = append(rules,
			"- Call task_complete when you have the final answer, task_blocked only for a concrete blocker.",
			toolRule,
			"- If 'Relevant Skills For This Task' lists a matching skill, call its `skill_*` tool first and follow it.",
			"- Never repeat a tool call that cannot return anything new.",
			// Each round is a model call; independent lookups made one per
			// round cost a call each for nothing.
			"- Make lookups that do not depend on each other in the same turn, as parallel calls.",
			ActOnBehalfRules,
		)
	}
	operationalRules := strings.Join(rules, "\n")
	if alwaysFalse(agent) {
		operationalRules = ""
	}

	data := map[string]interface{}{
		"AgentInstructions": agent.Instructions(),
		"OperationalRules":  operationalRules,
		"SystemContext":     systemCtx.FormatForPrompt(),
	}

	projectInstructions := opts.projectInstructions
	if !opts.projectInstructionsResolved {
		projectInstructions = s.projectInstructionsForRun()
	}

	sectionData := systemPromptSectionData{
		service:             s,
		agent:               agent,
		options:             opts,
		data:                data,
		projectInstructions: projectInstructions,
	}
	if s.promptManager == nil {
		return nil
	}
	names := []string{
		"identity",
		"operational",
		"system_context",
		// Static, right after the ambient context: the files change when a
		// person edits them, not per round, so they belong in the cached
		// prefix. Empty (and dropped) unless WithProjectInstructions is on.
		"project_instructions",
		"frc",
	}
	// The length anchors are on by default and only leave the prompt when the
	// caller says so. Dropping the name here rather than emptying the section
	// keeps the section registry itself untouched, so nothing else that resolves
	// sections changes behaviour. See Builder.WithLengthLimits.
	if !s.omitLengthLimits {
		names = append(names, "numeric_length_anchors")
	}
	names = append(names,
		"memory",
		"messaging",
		"tool_catalog",
		"web_search",
	)
	resolved, err := s.promptManager.ResolveSections(ctx, names, sectionData)

	if err == nil && len(resolved) > 0 {
		sections := make([]systemPromptSection, 0, len(resolved))
		for _, section := range resolved {
			sections = append(sections, systemPromptSection{
				name:    section.Name,
				content: section.Content,
				dynamic: section.Dynamic,
			})
		}
		return sections
	}

	identity := s.renderPromptSection(prompt.AgentSystemIdentity, data)
	operational := s.renderPromptSection(prompt.AgentSystemOperational, data)
	contextSection := s.renderPromptSection(prompt.AgentSystemContext, data)
	if identity == "" && operational == "" && contextSection == "" {
		basePrompt, err := s.promptManager.Render(prompt.AgentSystemPrompt, data)
		if err != nil {
			basePrompt = agent.Instructions() + "\n\n" + systemCtx.FormatForPrompt()
		}
		identity = strings.TrimSpace(basePrompt)
	}

	sections := make([]systemPromptSection, 0, 8)
	if identity != "" {
		sections = append(sections, systemPromptSection{name: "identity", content: identity})
	}
	if operational != "" {
		sections = append(sections, systemPromptSection{name: "operational", content: operational})
	}
	if contextSection != "" {
		sections = append(sections, systemPromptSection{name: "system_context", content: contextSection})
	}
	if projectInstructions != "" {
		sections = append(sections, systemPromptSection{name: "project_instructions", content: projectInstructions})
	}
	return sections
}
