package agent

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/mcp"
	"github.com/liliang-cn/agent-go/v3/pkg/ptc"
)

// mcpToolAdapter wraps mcp.Service to implement MCPToolExecutor
type mcpToolAdapter struct {
	service *mcp.Service
}

func (a *mcpToolAdapter) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	result, err := a.service.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return nil, fmt.Errorf("MCP tool error: %s (tool=%s args=%v)", result.Error, toolName, args)
	}
	return result.Data, nil
}

func (a *mcpToolAdapter) ListTools() []domain.ToolDefinition {
	tools := a.service.GetAvailableTools(context.Background())
	result := make([]domain.ToolDefinition, 0, len(tools))

	for _, t := range tools {
		params := t.InputSchema
		if params == nil {
			params = map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"args": map[string]interface{}{
						"description": "arguments",
						"type":        "object",
					},
				},
			}
		}
		result = append(result, domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return result
}

func (a *mcpToolAdapter) AddServer(ctx context.Context, name string, command string, args []string) error {
	return a.service.AddDynamicServer(ctx, name, command, args)
}

func (a *mcpToolAdapter) ToolMetadata(toolName string) (ToolMetadata, bool) {
	return inferDynamicToolMetadata(toolName)
}

// ListToolInfos lets the PTC router read the MCP tool list on every turn.
//
// Without it the router falls back to the snapshot taken by
// buildPTCRouterOptions at Build time (see ptc.AgentGoRouter.getMCPTools), so a
// server installed mid-conversation — via add_mcp_server — stayed invisible to
// the model in PTC mode: installed, persisted, running, and unusable. Reading
// live is what makes a dynamic install actually take effect.
func (a *mcpToolAdapter) ListToolInfos(ctx context.Context) []ptc.ToolInfo {
	if a == nil || a.service == nil {
		return nil
	}
	tools := a.service.GetAvailableTools(ctx)
	infos := make([]ptc.ToolInfo, 0, len(tools))
	for _, t := range tools {
		params := t.InputSchema
		if params == nil {
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		infos = append(infos, ptc.ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
			Category:    CategoryMCP,
		})
	}
	return infos
}
