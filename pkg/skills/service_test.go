package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAllLoadsProjectRootSkill(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	name := filepath.Base(dir)
	writeSkillFile(t, filepath.Join(dir, "SKILL.md"), name, "Loads from project root", "# Root Skill\n")

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	svc, err := NewService(&Config{Enabled: true, Paths: []string{".skills"}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := svc.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	skills, err := svc.ListSkills(context.Background(), SkillFilter{})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].ID != name {
		t.Fatalf("expected %s, got %q", name, skills[0].ID)
	}
}

func TestExecuteLoadsSkillContentOnDemand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	skillDir := filepath.Join(dir, ".skills", "echo-skill")
	writeSkillFile(t, filepath.Join(skillDir, "SKILL.md"), "echo-skill", "Echoes a variable", "# Echo {{name}}\n")

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	svc, err := NewService(&Config{Enabled: true, Paths: []string{".skills"}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := svc.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	result, err := svc.Execute(context.Background(), &ExecutionRequest{
		SkillID:   "echo-skill",
		Variables: map[string]interface{}{"name": "world"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "# Echo world" {
		t.Fatalf("expected rendered content, got %q", result.Output)
	}
}

func TestListCollectionsGroupsNestedSkills(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeSkillFile(t, filepath.Join(dir, ".skills", "impeccable", "frontend-design", "SKILL.md"), "frontend-design", "Frontend design", "# Frontend\n")
	writeSkillFile(t, filepath.Join(dir, ".skills", "impeccable", "audit", "SKILL.md"), "audit", "Audit", "# Audit\n")
	writeSkillFile(t, filepath.Join(dir, ".skills", "pptx", "SKILL.md"), "pptx", "PPTX", "# PPTX\n")

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	svc, err := NewService(&Config{Enabled: true, Paths: []string{".skills"}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	collections, err := svc.ListCollections(context.Background())
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(collections) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(collections))
	}
	if collections[0].Name != "impeccable" {
		t.Fatalf("expected impeccable, got %q", collections[0].Name)
	}
	if len(collections[0].Skills) != 2 {
		t.Fatalf("expected 2 collection skills, got %d", len(collections[0].Skills))
	}
}

func writeSkillFile(t *testing.T, path, name, description, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}
