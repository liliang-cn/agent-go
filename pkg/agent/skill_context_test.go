package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

func TestBuildRelevantSkillReminderInjectsWhenToUseMatch(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "docs-review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `---
name: docs-review
description: Review documentation edits.
when_to_use: Use when editing markdown docs or README files.
paths:
  - docs/*.md
---

# Review docs
`
	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	skillsSvc, err := skills.NewService(&skills.Config{Enabled: true, Paths: []string{dir}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := skillsSvc.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	svc := &Service{skillsService: skillsSvc}
	session := NewSession("agent-1")
	session.AddMessage(domainMessage("user", "please update docs/intro.md"))

	reminder := svc.buildRelevantSkillReminder(context.Background(), "please improve docs/intro.md", session)
	if reminder == nil {
		t.Fatal("expected a skill reminder")
	}
	if !strings.Contains(reminder.Text, "skill_docs-review") {
		t.Fatalf("expected relevant skill id in reminder, got %q", reminder.Text)
	}
	if !strings.Contains(reminder.Text, "Use when editing markdown docs") {
		t.Fatalf("expected when_to_use in reminder, got %q", reminder.Text)
	}

	msg := buildSkillReminderMessage(session, reminder)
	if msg == nil || !strings.Contains(msg.Content, "<system-reminder>") {
		t.Fatalf("expected system reminder message, got %+v", msg)
	}
}

func TestCollectAllAvailableToolsOnlyExposesRelevantSkills(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSkill := func(name, when string) {
		skillPath := filepath.Join(dir, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		content := `---
name: ` + name + `
description: ` + name + ` workflow.
when_to_use: ` + when + `
user-invocable: true
disable-model-invocation: false
---

# ` + name + `
`
		if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}

	writeSkill("docs-review", "Use when editing markdown docs.")
	writeSkill("code-review", "Use when reviewing Go code.")

	skillsSvc, err := skills.NewService(&skills.Config{Enabled: true, Paths: []string{dir}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := skillsSvc.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	svc := &Service{
		skillsService:         skillsSvc,
		toolRegistry:          NewToolRegistry(),
		currentSessionID:      "session-skills-filter",
		sessionRelevantSkills: make(map[string][]string),
	}
	svc.rememberRelevantSkillsForSession("session-skills-filter", []string{"docs-review"})

	tools := svc.collectAllAvailableTools(context.Background(), NewAgent("Assistant"))
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Function.Name)
	}

	if !containsStr(names, "skill_docs-review") {
		t.Fatalf("expected relevant skill to remain visible, got %v", names)
	}
	if containsStr(names, "skill_code-review") {
		t.Fatalf("expected unrelated skill to be hidden, got %v", names)
	}
}

func TestCollectAllAvailableToolsSkillFirstHidesRawToolsUntilSkillSatisfied(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillPath := filepath.Join(dir, "docs-review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `---
name: docs-review
description: Review docs edits.
when_to_use: Use when editing markdown docs.
user-invocable: true
disable-model-invocation: false
---

# Review docs
`
	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	skillsSvc, err := skills.NewService(&skills.Config{Enabled: true, Paths: []string{dir}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := skillsSvc.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	svc := &Service{
		skillsService:         skillsSvc,
		toolRegistry:          NewToolRegistry(),
		currentSessionID:      "session-skill-first",
		sessionRelevantSkills: make(map[string][]string),
		taskSkillSatisfied:    make(map[string]bool),
	}
	svc.toolRegistry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "custom_raw_tool",
			Description: "Raw tool that should stay hidden before skill execution.",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryCustom)

	svc.rememberRelevantSkillsForSession("session-skill-first", []string{"docs-review"})
	session := NewSession("agent-1")
	session.ID = "session-skill-first"
	session.SetContext(sessionContextTaskID, "task-1")
	ctx := withCurrentSession(context.Background(), session)

	before := svc.collectAllAvailableTools(ctx, NewAgent("Assistant"))
	beforeNames := make([]string, 0, len(before))
	for _, tool := range before {
		beforeNames = append(beforeNames, tool.Function.Name)
	}
	if containsStr(beforeNames, "custom_raw_tool") {
		t.Fatalf("expected raw tool hidden before skill execution, got %v", beforeNames)
	}
	if !containsStr(beforeNames, "skill_docs-review") {
		t.Fatalf("expected relevant skill visible before skill execution, got %v", beforeNames)
	}

	svc.markRelevantSkillSatisfied("session-skill-first", "task-1")
	after := svc.collectAllAvailableTools(ctx, NewAgent("Assistant"))
	afterNames := make([]string, 0, len(after))
	for _, tool := range after {
		afterNames = append(afterNames, tool.Function.Name)
	}
	if !containsStr(afterNames, "custom_raw_tool") {
		t.Fatalf("expected raw tool visible after skill execution, got %v", afterNames)
	}
}
