package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

type plannerPromptSectionData struct {
	planner *Planner
	data    map[string]interface{}
}

func renderPlannerPromptSections(sections []prompt.Section) string {
	out := make([]string, 0, len(sections))
	for _, section := range sections {
		content := strings.TrimSpace(section.Content)
		if content == "" {
			continue
		}
		out = append(out, content)
	}
	return strings.Join(out, "\n\n")
}

func (p *Planner) ensurePlannerPromptSectionRegistry() {
	if p == nil || p.promptManager == nil {
		return
	}

	p.promptManager.RegisterSection("planner.system.role", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(plannerPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected planner section data type %T", raw)
		}
		rendered, err := data.planner.promptManager.Render(prompt.PlannerSystemRole, data.data)
		if err != nil {
			return prompt.Section{}, err
		}
		return prompt.Section{Name: "planner.system.role", Content: rendered}, nil
	})
	p.promptManager.RegisterSection("planner.system.tools", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(plannerPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected planner section data type %T", raw)
		}
		rendered, err := data.planner.promptManager.Render(prompt.PlannerSystemTools, data.data)
		if err != nil {
			return prompt.Section{}, err
		}
		return prompt.Section{Name: "planner.system.tools", Content: rendered}, nil
	})
	p.promptManager.RegisterSection("planner.system.guidance", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(plannerPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected planner section data type %T", raw)
		}
		rendered, err := data.planner.promptManager.Render(prompt.PlannerSystemGuidance, data.data)
		if err != nil {
			return prompt.Section{}, err
		}
		return prompt.Section{Name: "planner.system.guidance", Content: rendered}, nil
	})
	p.promptManager.RegisterSection("planner.user.goal", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(plannerPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected planner section data type %T", raw)
		}
		rendered, err := data.planner.promptManager.Render(prompt.PlannerUserGoal, data.data)
		if err != nil {
			return prompt.Section{}, err
		}
		return prompt.Section{Name: "planner.user.goal", Content: rendered}, nil
	})
	p.promptManager.RegisterSection("planner.user.intent", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(plannerPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected planner section data type %T", raw)
		}
		rendered, err := data.planner.promptManager.Render(prompt.PlannerUserIntent, data.data)
		if err != nil {
			return prompt.Section{}, err
		}
		return prompt.Section{Name: "planner.user.intent", Content: rendered}, nil
	})
	p.promptManager.RegisterSection("planner.user.session_context", func(ctx context.Context, raw interface{}) (prompt.Section, error) {
		data, ok := raw.(plannerPromptSectionData)
		if !ok {
			return prompt.Section{}, fmt.Errorf("unexpected planner section data type %T", raw)
		}
		rendered, err := data.planner.promptManager.Render(prompt.PlannerUserSessionContext, data.data)
		if err != nil {
			return prompt.Section{}, err
		}
		return prompt.Section{Name: "planner.user.session_context", Content: rendered}, nil
	})
}

func (p *Planner) buildPlannerSystemPromptSections() []prompt.Section {
	p.ensurePlannerPromptSectionRegistry()

	data := map[string]interface{}{
		"ToolDescriptions": p.describeAvailableTools(),
		"HasRAG":           p.hasRAGTools(),
	}
	sections, err := p.promptManager.ResolveSections(context.Background(), []string{
		"planner.system.role",
		"planner.system.tools",
		"planner.system.guidance",
	}, plannerPromptSectionData{planner: p, data: data})
	if err == nil && len(sections) > 0 {
		return sections
	}

	rendered, err := p.promptManager.Render(prompt.PlannerSystemPrompt, data)
	if err != nil {
		rendered = "You are an AI planning agent. Help user achieve their goal using available tools."
	}
	return []prompt.Section{{Name: "planner.system", Content: rendered}}
}

func (p *Planner) buildPlannerUserPromptSections(goal string, session *Session, intent *IntentRecognitionResult) []prompt.Section {
	p.ensurePlannerPromptSectionRegistry()

	data := map[string]interface{}{
		"Goal":   goal,
		"Intent": intent,
	}
	if session != nil {
		var sb strings.Builder
		if session.GetSummary() != "" {
			sb.WriteString("Conversation Summary:\n")
			sb.WriteString(session.GetSummary())
			sb.WriteString("\n\n")
		}
		messages := session.GetLastNMessages(5)
		if len(messages) > 0 {
			sb.WriteString("Recent conversation context:\n")
			for _, msg := range messages {
				sb.WriteString(fmt.Sprintf("- [%s]: %s\n", msg.Role, msg.Content))
			}
		}
		data["SessionContext"] = sb.String()
	}

	sections, err := p.promptManager.ResolveSections(context.Background(), []string{
		"planner.user.goal",
		"planner.user.intent",
		"planner.user.session_context",
	}, plannerPromptSectionData{planner: p, data: data})
	if err == nil && len(sections) > 0 {
		return sections
	}

	rendered, err := p.promptManager.Render(prompt.PlannerUserPrompt, data)
	if err != nil {
		rendered = fmt.Sprintf("Goal: %s. Create a plan.", goal)
	}
	return []prompt.Section{{Name: "planner.user", Content: rendered}}
}
