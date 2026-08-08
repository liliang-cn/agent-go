package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/skills"
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

	tools := svc.collectAllAvailableTools(context.Background(), NewAgent("Responder"))
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

// Skill-first recommends; it does not confiscate.
//
// The matcher behind it is lexical — skills-go scores a skill by asking whether
// each four-letter-or-longer input word appears as a substring of its name,
// when_to_use or description, and anything above zero counts. So a question
// about the weather matched a frontend design skill on the word "check", which
// appears inside "pre-flight check" in that skill's description. When the match
// removed every other tool, that one word took web search away and the model
// told the user no web search tool existed.
//
// This is that exact case, with that exact skill text. The relevant skill must
// lead the list; the unrelated capability must still be there.
func TestSkillFirstRecommendsWithoutRemovingCapabilities(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillPath := filepath.Join(dir, "design-taste-frontend", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `---
name: design-taste-frontend
description: Anti-slop frontend skill for landing pages and redesigns, with a strict pre-flight check.
when_to_use: Use when building a landing page.
user-invocable: true
disable-model-invocation: false
---

# Design taste
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
	for _, name := range []string{"web_search", "set_reminder"} {
		svc.toolRegistry.Register(domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        name,
				Description: name,
				Parameters:  map[string]interface{}{"type": "object"},
			},
		}, nil, CategoryCustom)
	}

	// Pretend the matcher returned it anyway, so this pins the schema half of
	// the behaviour independently of the scoring half.
	svc.rememberRelevantSkillsForSession("session-skill-first", []string{"design-taste-frontend"})
	session := NewSession("agent-1")
	session.ID = "session-skill-first"
	session.SetContext(sessionContextTaskID, "task-1")
	ctx := withCurrentSession(context.Background(), session)

	tools := svc.collectAllAvailableTools(ctx, NewAgent("Responder"))
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Function.Name)
	}

	for _, want := range []string{"web_search", "set_reminder"} {
		if !containsStr(names, want) {
			t.Fatalf("an unrelated skill match removed %s from the schema: %v", want, names)
		}
	}
	if !containsStr(names, "skill_design-taste-frontend") {
		t.Fatalf("expected the matched skill to be offered, got %v", names)
	}
	if names[0] != "skill_design-taste-frontend" {
		t.Errorf("expected the matched skill to lead the list, got %v", names)
	}

	// Once the skill has been used the promotion stops; nothing else changes.
	svc.markRelevantSkillSatisfied("session-skill-first", "task-1")
	after := svc.collectAllAvailableTools(ctx, NewAgent("Responder"))
	afterNames := make([]string, 0, len(after))
	for _, tool := range after {
		afterNames = append(afterNames, tool.Function.Name)
	}
	for _, want := range []string{"web_search", "set_reminder"} {
		if !containsStr(afterNames, want) {
			t.Fatalf("expected %s after the skill was used, got %v", want, afterNames)
		}
	}
}

// The scoring half, end to end: a weather question must not surface a frontend
// design skill at all — no <skill-discovery> reminder, no activated tool, no
// promotion. skills-go now scores that overlap at 0.03 and the runtime declines
// to act below its floor.
//
// The same registry must still find a skill that genuinely fits, or the floor
// has simply been set too high.
func TestWeakSkillMatchIsNotSurfacedAtAll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSkill := func(name, desc string) {
		p := filepath.Join(dir, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		body := "---\nname: " + name + "\ndescription: " + desc +
			"\nuser-invocable: true\ndisable-model-invocation: false\n---\n\n# " + name + "\n"
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}
	writeSkill("design-taste-frontend",
		"Anti-slop frontend skill for landing pages, portfolios, and redesigns. Real design systems when applicable, audit-first on redesigns, strict pre-flight check.")
	writeSkill("cortexdb", "Use CortexDB for vector search, hybrid search, knowledge graphs, and RAG.")
	writeSkill("golang-pro", "Implements concurrent Go patterns using goroutines and channels, designs microservices with gRPC or REST.")
	writeSkill("brandkit", "Premium brand-kit image generation for brand-guidelines boards, logo systems and identity decks.")

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
		sessionRelevantSkills: make(map[string][]string),
		taskSkillSatisfied:    make(map[string]bool),
	}

	weather := "Check the weather in Chicago. If it's sunny, remind me to hang the laundry outside; otherwise remind me to use the dryer."
	if reminder := svc.buildRelevantSkillReminder(context.Background(), weather, NewSession("agent-1")); reminder != nil {
		t.Errorf("a weather question surfaced skills %v: %q", reminder.Names, reminder.Text)
	}

	// The floor is not simply excluding everything.
	design := "I need a landing page for my startup, make the design look premium"
	reminder := svc.buildRelevantSkillReminder(context.Background(), design, NewSession("agent-2"))
	if reminder == nil {
		t.Fatal("a genuine design request surfaced no skill; the floor is too high")
	}
	if !strings.Contains(reminder.Text, "skill_design-taste-frontend") {
		t.Errorf("expected the design skill, got %q", reminder.Text)
	}
}
