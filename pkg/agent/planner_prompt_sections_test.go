package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

func TestPlannerBuildSystemPromptSections_UsesNamedSections(t *testing.T) {
	p := &Planner{
		tools:         []domain.ToolDefinition{{Function: domain.ToolFunction{Name: "read_file", Parameters: map[string]interface{}{"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}}}}},
		promptManager: prompt.NewManager(),
	}

	sections := p.buildPlannerSystemPromptSections()
	if len(sections) == 0 {
		t.Fatal("expected planner system sections")
	}

	names := make([]string, 0, len(sections))
	for _, section := range sections {
		names = append(names, section.Name)
	}
	if !containsStr(names, "planner.system.role") {
		t.Fatalf("expected planner.system.role section, got %v", names)
	}
	if !containsStr(names, "planner.system.tools") {
		t.Fatalf("expected planner.system.tools section, got %v", names)
	}
	if !containsStr(names, "planner.system.guidance") {
		t.Fatalf("expected planner.system.guidance section, got %v", names)
	}
}

func TestPlannerBuildUserPromptSections_UsesNamedSections(t *testing.T) {
	p := &Planner{
		promptManager: prompt.NewManager(),
	}
	session := NewSession("agent-1")
	session.SetSummary("Earlier we discussed the repo layout.")

	sections := p.buildPlannerUserPromptSections("read the config", session, &IntentRecognitionResult{IntentType: "file_read", Confidence: 0.9})
	if len(sections) == 0 {
		t.Fatal("expected planner user sections")
	}

	joined := renderPlannerPromptSections(sections)
	if !strings.Contains(joined, "Goal: read the config") {
		t.Fatalf("expected goal section in planner user prompt, got %q", joined)
	}
	if !strings.Contains(joined, "Intent Analysis:") {
		t.Fatalf("expected intent section in planner user prompt, got %q", joined)
	}
	if !strings.Contains(joined, "Recent conversation context:") && !strings.Contains(joined, "Conversation Summary:") {
		t.Fatalf("expected session context section in planner user prompt, got %q", joined)
	}
}

func TestPlannerPromptSectionRegistry_ResolveSections(t *testing.T) {
	p := &Planner{
		promptManager: prompt.NewManager(),
	}
	p.ensurePlannerPromptSectionRegistry()

	sections, err := p.promptManager.ResolveSections(context.Background(), []string{
		"planner.system.role",
		"planner.system.tools",
	}, plannerPromptSectionData{
		planner: p,
		data: map[string]interface{}{
			"ToolDescriptions": "Available tools:\n- read_file(path)",
			"HasRAG":           false,
		},
	})
	if err != nil {
		t.Fatalf("ResolveSections() error = %v", err)
	}
	if len(sections) != 2 {
		t.Fatalf("expected 2 planner sections, got %+v", sections)
	}
	if !strings.Contains(sections[0].Content, "planning agent") {
		t.Fatalf("unexpected planner role section: %+v", sections[0])
	}
	if !strings.Contains(sections[1].Content, "Available tools:") {
		t.Fatalf("unexpected planner tools section: %+v", sections[1])
	}
}
