package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

const SystemPromptDynamicBoundary = "<SYSTEM_PROMPT_DYNAMIC_BOUNDARY>"

type systemPromptSection struct {
	name    string
	content string
	dynamic bool
}

type systemPromptOptions struct {
	includePTC bool
}

type systemPromptSectionData struct {
	service *Service
	agent   *Agent
	options systemPromptOptions
	data    map[string]interface{}
}

func dynamicPromptSection(name, content string) *systemPromptSection {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	return &systemPromptSection{name: name, content: content, dynamic: true}
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
	s.promptManager.RegisterSection("ptc", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		if !data.options.includePTC || data.service.ptcIntegration == nil {
			return prompt.Section{Name: "ptc"}, nil
		}
		availableCallTools := data.service.ptcAvailableCallTools(ctx)
		return prompt.Section{Name: "ptc", Content: data.service.ptcIntegration.GetPTCSystemPrompt(availableCallTools), Dynamic: true}, nil
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
		return prompt.Section{Name: "tool_catalog", Content: data.service.buildToolCatalogSummary(ctx), Dynamic: true}, nil
	})
	s.promptManager.RegisterSection("web_search", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(systemPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected section data type %T", raw)
		}
		if isDispatchOnlyAgent(data.agent) {
			return prompt.Section{Name: "web_search"}, nil
		}
		return prompt.Section{Name: "web_search", Content: data.service.buildWebSearchPromptNote(data.agent), Dynamic: true}, nil
	})
}

func (s *Service) buildSystemPromptSections(ctx context.Context, agent *Agent, opts systemPromptOptions) []systemPromptSection {
	s.ensureSystemPromptSectionRegistry()
	systemCtx := s.buildSystemContext()
	operationalRules := strings.Join([]string{
		"- Call task_complete as soon as you have the final answer. Never keep running after the task is done.",
		"- For file operations use mcp_filesystem_* tools; for web search use mcp_websearch_* tools.",
		"- Treat the visible callable tool list as the authoritative source of what can actually be executed in this runtime.",
		"- Do not invent hidden tool or API names such as generic run/status/start methods when concrete callable tool names are already exposed.",
		"- If you are unsure which exact tool fits a request, call `search_available_tools` before claiming the capability is unavailable.",
		"- Skills: calling a skill tool returns step-by-step instructions — follow them, then call task_complete.",
		"- Never repeat the same tool call with identical arguments.",
	}, "\n")
	if isDispatchOnlyAgent(agent) {
		operationalRules = ""
	}

	data := map[string]interface{}{
		"AgentInstructions": agent.Instructions(),
		"OperationalRules":  operationalRules,
		"SystemContext":     systemCtx.FormatForPrompt(),
	}

	sectionData := systemPromptSectionData{
		service: s,
		agent:   agent,
		options: opts,
		data:    data,
	}
	resolved, err := s.promptManager.ResolveSections(ctx, []string{
		"identity",
		"operational",
		"system_context",
		"ptc",
		"memory",
		"messaging",
		"tool_catalog",
		"web_search",
	}, sectionData)

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
	return sections
}

func (s *Service) buildDynamicSystemPromptSections(ctx context.Context, agent *Agent, opts systemPromptOptions) []systemPromptSection {
	sections := make([]systemPromptSection, 0, 5)

	if opts.includePTC && s.ptcIntegration != nil {
		availableCallTools := s.ptcAvailableCallTools(ctx)
		if section := dynamicPromptSection("ptc", s.ptcIntegration.GetPTCSystemPrompt(availableCallTools)); section != nil {
			sections = append(sections, *section)
		}
	}
	if section := dynamicPromptSection("memory", s.buildMemoryPromptNote(ctx, agent)); section != nil {
		sections = append(sections, *section)
	}
	if section := dynamicPromptSection("messaging", s.buildAgentMessagingPromptNote(ctx, agent)); section != nil {
		sections = append(sections, *section)
	}
	if section := dynamicPromptSection("tool_catalog", s.buildToolCatalogSummary(ctx)); section != nil {
		sections = append(sections, *section)
	}
	if !isDispatchOnlyAgent(agent) {
		if section := dynamicPromptSection("web_search", s.buildWebSearchPromptNote(agent)); section != nil {
			sections = append(sections, *section)
		}
	}

	return sections
}
