package skills

import (
	"path/filepath"

	"github.com/liliang-cn/agent-go/v2/cmd/agentgo-cli/rag"
	"github.com/liliang-cn/agent-go/v2/pkg/resource"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func saveSkillResource(skillName, skillDir string) {
	if skillName == "" || skillDir == "" {
		return
	}
	if ragCfg := rag.GetConfig(); ragCfg != nil {
		db, err := store.NewAgentGoDB(ragCfg.AgentDBPath())
		if err != nil {
			return
		}
		defer db.Close()
		parent := filepath.Dir(skillDir)
		_ = db.SaveResource(resource.Resource{
			ID:       "skill:" + skillName,
			Kind:     resource.KindSkill,
			Name:     skillName,
			Provider: parent,
			Metadata: map[string]any{
				"path":      parent,
				"skill_dir": skillDir,
			},
		})
	}
}

func deleteSkillResource(skillName string) {
	if skillName == "" {
		return
	}
	if ragCfg := rag.GetConfig(); ragCfg != nil {
		db, err := store.NewAgentGoDB(ragCfg.AgentDBPath())
		if err != nil {
			return
		}
		defer db.Close()
		_ = db.DeleteResource("skill:" + skillName)
	}
}
