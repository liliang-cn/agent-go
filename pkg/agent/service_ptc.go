package agent

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/ptc"
	"github.com/liliang-cn/agent-go/v3/pkg/skills"
)

// ChatWithPTC sends a message with PTC (Parallel Tool Calling) support.
//
// PTC is a transport mode, not a separate execution path. This method is a
// thin backward-compatibility wrapper around Chat(). When PTC is enabled,
// Chat() automatically uses the PTC execution path and populates
// ExecutionResult.PTCResult with rich JS execution details.
func (s *Service) ChatWithPTC(ctx context.Context, message string) (*PTCChatResult, error) {
	result, err := s.Chat(ctx, message)
	if err != nil {
		return nil, err
	}

	ptcUsed := result.PTCResult != nil &&
		(result.PTCResult.Code != "" ||
			result.PTCResult.Type == PTCResultTypeExecuted ||
			result.PTCResult.Type == PTCResultTypeCode)

	llmResp := ""
	if result.PTCResult != nil {
		llmResp = result.PTCResult.OriginalContent
	} else {
		llmResp = fmt.Sprintf("%v", result.FinalResult)
	}

	return &PTCChatResult{
		ExecutionResult: result,
		PTCResult:       result.PTCResult,
		PTCUsed:         ptcUsed,
		LLMResponse:     llmResp,
		SessionID:       result.SessionID,
	}, nil
}

// PTCChatResult contains the result of a PTC-aware chat
type PTCChatResult struct {
	ExecutionResult *ExecutionResult `json:"execution_result,omitempty"`
	PTCResult       *PTCResult       `json:"ptc_result,omitempty"`
	PTCUsed         bool             `json:"ptc_used"`
	LLMResponse     string           `json:"llm_response"`
	SessionID       string           `json:"session_id"`
}

// buildPTCRouterOptions constructs ptc.RouterOption list for dynamic providers only.
// Static tools (RAG, Memory, custom) are registered via ToolRegistry.SyncToPTCRouter.
func buildPTCRouterOptions(mcpSvc MCPToolExecutor, skillsSvc *skills.Service) []ptc.RouterOption {
	var opts []ptc.RouterOption

	if mcpSvc != nil {
		opts = append(opts, ptc.WithMCPService(mcpSvc))
		mcpInfos := domainToolsToPTCInfos(mcpSvc.ListTools(), CategoryMCP)
		if len(mcpInfos) > 0 {
			opts = append(opts, ptc.WithMCPToolInfos(mcpInfos))
		}
	}

	if skillsSvc != nil {
		// Wrapped, not passed raw: the wrapper adds ListSkillInfos so the router
		// reads the skill list on every turn instead of using the snapshot below,
		// which is what lets a skill installed mid-conversation be callable.
		opts = append(opts, ptc.WithSkillsService(&skillsToolLister{svc: skillsSvc}))
		skillList, _ := skillsSvc.ListSkills(context.Background(), skills.SkillFilter{})
		skillInfos := make([]ptc.ToolInfo, 0, len(skillList))
		for _, sk := range skillList {
			skillInfos = append(skillInfos, ptc.ToolInfo{
				Name:        sk.ID,
				Description: sk.Description,
				Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
				Category:    CategorySkill,
			})
		}
		if len(skillInfos) > 0 {
			opts = append(opts, ptc.WithSkillToolInfos(skillInfos))
		}
	}

	return opts
}

// domainToolsToPTCInfos converts domain.ToolDefinition slice to ptc.ToolInfo slice.
func domainToolsToPTCInfos(defs []domain.ToolDefinition, category string) []ptc.ToolInfo {
	infos := make([]ptc.ToolInfo, 0, len(defs))
	for _, d := range defs {
		params := d.Function.Parameters
		if params == nil {
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		infos = append(infos, ptc.ToolInfo{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			Parameters:  params,
			Category:    category,
		})
	}
	return infos
}

// skillsToolLister adapts skills.Service for the PTC router.
//
// The router discovers capabilities by duck-typing: ListSkillInfos for the tool
// list, RunSkill to execute. skills.Service has the latter but not the former,
// so the router fell back to the infos captured at Build time — a skill
// installed later was invisible to the model. Adding the method here keeps
// skills/ from having to know about ptc/, and RunSkill is forwarded so the
// router can still execute.
type skillsToolLister struct {
	svc *skills.Service
}

// ListSkillInfos reports the currently loaded skills, read live.
func (l *skillsToolLister) ListSkillInfos(ctx context.Context) []ptc.ToolInfo {
	if l == nil || l.svc == nil {
		return nil
	}
	list, err := l.svc.ListSkills(ctx, skills.SkillFilter{})
	if err != nil {
		return nil
	}
	infos := make([]ptc.ToolInfo, 0, len(list))
	for _, sk := range list {
		if sk == nil {
			continue
		}
		infos = append(infos, ptc.ToolInfo{
			Name:        sk.ID,
			Description: sk.Description,
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			Category:    CategorySkill,
		})
	}
	return infos
}

// RunSkill forwards execution to the wrapped service.
func (l *skillsToolLister) RunSkill(ctx context.Context, id string, vars map[string]interface{}) (string, error) {
	if l == nil || l.svc == nil {
		return "", fmt.Errorf("skills service unavailable")
	}
	return l.svc.RunSkill(ctx, id, vars)
}
