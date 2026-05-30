package agent

import (
	"context"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/resource"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

func (s *Service) Resources(ctx context.Context) []resource.Resource {
	if s == nil {
		return nil
	}
	var out []resource.Resource
	info := s.Info()
	if info.Model != "" || s.llmService != nil {
		out = append(out, resource.Resource{
			ID:       "llm:" + firstNonEmptyTaskString(info.Model, "default"),
			Kind:     resource.KindLLM,
			Name:     firstNonEmptyTaskString(info.Model, "default"),
			Provider: info.BaseURL,
		})
	}
	if s.memoryService != nil {
		out = append(out, resource.Resource{
			ID:       "memory:" + firstNonEmptyTaskString(s.memoryStoreType, "default"),
			Kind:     resource.KindMemory,
			Name:     firstNonEmptyTaskString(s.memoryStoreType, "default"),
			Provider: "agentgo",
		})
	}
	if s.ragProcessor != nil {
		out = append(out, resource.Resource{ID: "rag:default", Kind: resource.KindRAG, Name: "default", Provider: "agentgo"})
	}
	if s.ptcIntegration != nil && s.ptcIntegration.config != nil && s.ptcIntegration.config.Enabled {
		out = append(out, resource.Resource{ID: "ptc:goja", Kind: resource.KindPTC, Name: "goja", Provider: "agentgo"})
	}
	if s.mcpService != nil {
		for _, tool := range s.mcpService.ListTools() {
			out = append(out, resource.Resource{
				ID:          "mcp:" + tool.Function.Name,
				Kind:        resource.KindMCP,
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Execution:   resource.ExecutionCodeOnly,
			})
		}
	}
	if s.skillsService != nil {
		skillsList, _ := s.skillsService.ListSkills(ctx, skills.SkillFilter{})
		for _, skill := range skillsList {
			if skill == nil {
				continue
			}
			out = append(out, resource.Resource{
				ID:          "skill:" + skill.ID,
				Kind:        resource.KindSkill,
				Name:        skill.ID,
				Description: skill.Description,
				Execution:   resource.ExecutionCodeOnly,
			})
		}
	}
	if s.toolRegistry != nil {
		for _, res := range s.toolRegistry.Resources() {
			out = append(out, res)
		}
	}
	return dedupeResources(out)
}

func dedupeResources(values []resource.Resource) []resource.Resource {
	seen := make(map[string]struct{}, len(values))
	out := make([]resource.Resource, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, value)
	}
	return out
}

func ToolExecutionPolicyFromResources(resources []resource.Resource) ToolExecutionPolicy {
	policy := ToolExecutionPolicy{Rules: make(map[string]ToolExposureMode)}
	for _, res := range resources {
		if res.Kind != resource.KindTool && res.Kind != resource.KindMCP && res.Kind != resource.KindSkill {
			continue
		}
		if res.Execution == "" {
			continue
		}
		name := strings.TrimSpace(res.Name)
		if name == "" {
			continue
		}
		policy.Rules[name] = ToolExposureMode(res.Execution)
	}
	return policy
}
