package agent

import (
	"context"

	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

// Skill-installation detection. WithSkills() loads skills from the configured
// paths (default ~/.agentgo/skills) but succeeds silently even when that
// directory is empty or missing — so callers that depend on specific skills
// (e.g. the understand-* codebase-comprehension skills) need a way to verify
// they were actually loaded. These helpers provide that.

// SkillsInstalled reports whether the agent has a skills service with at least
// one loaded skill. Call it right after Build() to confirm WithSkills() found
// skills:
//
//	svc, _ := agent.New("x").WithSkills().Build()
//	if !svc.SkillsInstalled() {
//	    log.Println("no skills found under ~/.agentgo/skills")
//	}
func (s *Service) SkillsInstalled() bool {
	return len(s.InstalledSkills()) > 0
}

// InstalledSkills returns the IDs of every loaded skill, or nil when skills are
// not enabled / none were found.
func (s *Service) InstalledSkills() []string {
	if s == nil || s.skillsService == nil {
		return nil
	}
	list, err := s.skillsService.ListSkills(context.Background(), skills.SkillFilter{})
	if err != nil || len(list) == 0 {
		return nil
	}
	ids := make([]string, 0, len(list))
	for _, sk := range list {
		if sk != nil {
			ids = append(ids, sk.ID)
		}
	}
	return ids
}

// HasSkill reports whether a skill with the given ID or name is installed.
func (s *Service) HasSkill(idOrName string) bool {
	if s == nil || s.skillsService == nil || idOrName == "" {
		return false
	}
	list, err := s.skillsService.ListSkills(context.Background(), skills.SkillFilter{})
	if err != nil {
		return false
	}
	for _, sk := range list {
		if sk != nil && (sk.ID == idOrName || sk.Name == idOrName) {
			return true
		}
	}
	return false
}

// MissingSkills returns the subset of want (IDs or names) that are NOT installed.
// Empty result means all requested skills are present.
func (s *Service) MissingSkills(want ...string) []string {
	if len(want) == 0 {
		return nil
	}
	have := map[string]bool{}
	if s != nil && s.skillsService != nil {
		if list, err := s.skillsService.ListSkills(context.Background(), skills.SkillFilter{}); err == nil {
			for _, sk := range list {
				if sk != nil {
					have[sk.ID] = true
					have[sk.Name] = true
				}
			}
		}
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	return missing
}
