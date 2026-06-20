package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

func skillsServiceWith(t *testing.T, names ...string) *skills.Service {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		p := filepath.Join(dir, n, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		content := "---\nname: " + n + "\ndescription: test skill " + n + ".\n---\n\n# " + n + "\n"
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}
	svc, err := skills.NewService(&skills.Config{Enabled: true, Paths: []string{dir}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	return svc
}

func TestSkillsInstalledDetection(t *testing.T) {
	// No skills service at all.
	empty := &Service{}
	if empty.SkillsInstalled() {
		t.Error("expected SkillsInstalled()=false with no skills service")
	}
	if empty.HasSkill("understand") {
		t.Error("expected HasSkill=false with no skills service")
	}

	svc := &Service{skillsService: skillsServiceWith(t, "understand", "understand-chat")}
	if !svc.SkillsInstalled() {
		t.Fatal("expected SkillsInstalled()=true")
	}
	got := map[string]bool{}
	for _, id := range svc.InstalledSkills() {
		got[id] = true
	}
	if !got["understand"] || !got["understand-chat"] {
		t.Fatalf("InstalledSkills missing entries: %v", svc.InstalledSkills())
	}
	if !svc.HasSkill("understand") {
		t.Error("expected HasSkill(understand)=true")
	}
	if svc.HasSkill("nope") {
		t.Error("expected HasSkill(nope)=false")
	}
}

func TestMissingSkills(t *testing.T) {
	svc := &Service{skillsService: skillsServiceWith(t, "understand")}
	missing := svc.MissingSkills("understand", "understand-chat", "understand-diff")
	want := map[string]bool{"understand-chat": true, "understand-diff": true}
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing, got %v", missing)
	}
	for _, m := range missing {
		if !want[m] {
			t.Errorf("unexpected missing entry %q", m)
		}
	}
	if svc.MissingSkills() != nil {
		t.Error("empty want should return nil")
	}
}
