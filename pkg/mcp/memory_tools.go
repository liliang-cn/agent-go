package mcp

import (
	"context"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// MemoryTools creates a set of MCP tools for memory management
type MemoryTools struct {
	memorySvc domain.MemoryService
}

// NewMemoryTools creates a new MemoryTools instance
func NewMemoryTools(memorySvc domain.MemoryService) *MemoryTools {
	return &MemoryTools{
		memorySvc: memorySvc,
	}
}

// GetTools returns all memory tools as MCPTool implementations
func (mt *MemoryTools) GetTools() []MCPTool {
	return []MCPTool{
		&MemorySearchTool{memorySvc: mt.memorySvc},
		&MemoryAddTool{memorySvc: mt.memorySvc},
		&MemoryGetTool{memorySvc: mt.memorySvc},
		&MemoryUpdateTool{memorySvc: mt.memorySvc},
		&MemoryListTool{memorySvc: mt.memorySvc},
		&MemoryDeleteTool{memorySvc: mt.memorySvc},
	}
}

// RegisterTools registers all memory tools with the MCP tool manager
func RegisterMemoryTools(manager *MCPToolManager, memorySvc domain.MemoryService) {
	if manager == nil || memorySvc == nil {
		return
	}

	mt := NewMemoryTools(memorySvc)
	for _, tool := range mt.GetTools() {
		manager.RegisterCustomTool(tool)
	}
}

// RegisterCustomTool registers a custom tool with the manager
func (tm *MCPToolManager) RegisterCustomTool(tool MCPTool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	name := tool.Name()
	tm.tools[name] = &MCPToolWrapper{
		customTool: tool,
	}
}

// MemorySearchTool implements the memory_search tool
type MemorySearchTool struct {
	memorySvc domain.MemoryService
}

func (t *MemorySearchTool) Name() string {
	return "memory_search"
}

func (t *MemorySearchTool) Description() string {
	return "Search long-term memory for relevant information. Use this to recall facts, skills, patterns, preferences, or context from previous interactions."
}

func (t *MemorySearchTool) ServerName() string {
	return "memory"
}

func (t *MemorySearchTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query for finding relevant memories",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of results to return",
				"default":     5,
			},
		},
		"required": []string{"query"},
	}
}

func (t *MemorySearchTool) Call(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return &MCPToolResult{
			Success: false,
			Error:   "missing required parameter: query",
		}, nil
	}

	limit := 5
	if limitVal, ok := args["limit"].(float64); ok {
		limit = int(limitVal)
	}

	memories, err := t.memorySvc.Search(ctx, query, limit)
	if err != nil {
		return &MCPToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	// Format results
	results := make([]map[string]interface{}, len(memories))
	for i, mem := range memories {
		results[i] = map[string]interface{}{
			"id":         mem.ID,
			"type":       mem.Type,
			"content":    mem.Content,
			"score":      mem.Score,
			"importance": mem.Importance,
		}
	}

	return &MCPToolResult{
		Success: true,
		Data: map[string]interface{}{
			"count":    len(results),
			"memories": results,
		},
	}, nil
}

// MemoryAddTool implements the memory_add tool
type MemoryAddTool struct {
	memorySvc domain.MemoryService
}

func (t *MemoryAddTool) Name() string {
	return "memory_add"
}

func (t *MemoryAddTool) Description() string {
	return "Add a new memory to long-term storage. Use this to store important facts, skills, patterns, or preferences that should be remembered for future interactions."
}

func (t *MemoryAddTool) ServerName() string {
	return "memory"
}

func (t *MemoryAddTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Memory type: fact, skill, pattern, context, or preference",
				"enum":        []string{"fact", "skill", "pattern", "context", "preference"},
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "The memory content to store",
			},
			"importance": map[string]interface{}{
				"type":        "number",
				"description": "Importance score (0-1). >0.8 for critical, >0.5 for useful, <0.5 for trivial",
				"default":     0.5,
			},
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional session ID to associate this memory with",
			},
		},
		"required": []string{"type", "content"},
	}
}

func (t *MemoryAddTool) Call(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	memType, ok := args["type"].(string)
	if !ok || memType == "" {
		return &MCPToolResult{
			Success: false,
			Error:   "missing required parameter: type",
		}, nil
	}

	content, ok := args["content"].(string)
	if !ok || content == "" {
		return &MCPToolResult{
			Success: false,
			Error:   "missing required parameter: content",
		}, nil
	}

	importance := 0.5
	if impVal, ok := args["importance"].(float64); ok {
		importance = impVal
	}

	sessionID, _ := args["session_id"].(string)

	memory := &domain.Memory{
		Type:       domain.MemoryType(memType),
		Content:    content,
		Importance: importance,
		SessionID:  sessionID,
	}

	if err := t.memorySvc.Add(ctx, memory); err != nil {
		return &MCPToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &MCPToolResult{
		Success: true,
		Data: map[string]interface{}{
			"success": true,
			"id":      memory.ID,
			"message": "Memory stored successfully",
		},
	}, nil
}

// MemoryGetTool implements the memory_get tool
type MemoryGetTool struct {
	memorySvc domain.MemoryService
}

func (t *MemoryGetTool) Name() string {
	return "memory_get"
}

func (t *MemoryGetTool) Description() string {
	return "Retrieve a specific memory by its ID"
}

func (t *MemoryGetTool) ServerName() string {
	return "memory"
}

func (t *MemoryGetTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type":        "string",
				"description": "The memory ID to retrieve",
			},
		},
		"required": []string{"id"},
	}
}

func (t *MemoryGetTool) Call(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	id, ok := args["id"].(string)
	if !ok || id == "" {
		return &MCPToolResult{
			Success: false,
			Error:   "missing required parameter: id",
		}, nil
	}

	memory, err := t.memorySvc.Get(ctx, id)
	if err != nil {
		return &MCPToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &MCPToolResult{
		Success: true,
		Data: map[string]interface{}{
			"id":           memory.ID,
			"type":         memory.Type,
			"content":      memory.Content,
			"importance":   memory.Importance,
			"access_count": memory.AccessCount,
			"session_id":   memory.SessionID,
			"created_at":   memory.CreatedAt,
			"updated_at":   memory.UpdatedAt,
		},
	}, nil
}

// MemoryUpdateTool implements the memory_update tool
type MemoryUpdateTool struct {
	memorySvc domain.MemoryService
}

func (t *MemoryUpdateTool) Name() string {
	return "memory_update"
}

func (t *MemoryUpdateTool) Description() string {
	return "Update an existing memory with new information. The LLM will intelligently merge the new information with the existing memory."
}

func (t *MemoryUpdateTool) ServerName() string {
	return "memory"
}

func (t *MemoryUpdateTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type":        "string",
				"description": "The memory ID to update",
			},
			"instruction": map[string]interface{}{
				"type":        "string",
				"description": "How to update the memory content",
			},
		},
		"required": []string{"id", "instruction"},
	}
}

func (t *MemoryUpdateTool) Call(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	id, ok := args["id"].(string)
	if !ok || id == "" {
		return &MCPToolResult{
			Success: false,
			Error:   "missing required parameter: id",
		}, nil
	}

	instruction, ok := args["instruction"].(string)
	if !ok || instruction == "" {
		return &MCPToolResult{
			Success: false,
			Error:   "missing required parameter: instruction",
		}, nil
	}

	if err := t.memorySvc.Update(ctx, id, instruction); err != nil {
		return &MCPToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &MCPToolResult{
		Success: true,
		Data: map[string]interface{}{
			"success": true,
			"message": "Memory updated successfully",
		},
	}, nil
}

// MemoryListTool implements the memory_list tool
type MemoryListTool struct {
	memorySvc domain.MemoryService
}

func (t *MemoryListTool) Name() string {
	return "memory_list"
}

func (t *MemoryListTool) Description() string {
	return "List all stored memories with pagination"
}

func (t *MemoryListTool) ServerName() string {
	return "memory"
}

func (t *MemoryListTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of memories to return",
				"default":     10,
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Number of memories to skip",
				"default":     0,
			},
		},
	}
}

func (t *MemoryListTool) Call(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	limit := 10
	if limitVal, ok := args["limit"].(float64); ok {
		limit = int(limitVal)
	}

	offset := 0
	if offsetVal, ok := args["offset"].(float64); ok {
		offset = int(offsetVal)
	}

	memories, total, err := t.memorySvc.List(ctx, limit, offset)
	if err != nil {
		return &MCPToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	// Format results
	results := make([]map[string]interface{}, len(memories))
	for i, mem := range memories {
		results[i] = map[string]interface{}{
			"id":           mem.ID,
			"type":         mem.Type,
			"content":      mem.Content,
			"importance":   mem.Importance,
			"access_count": mem.AccessCount,
			"session_id":   mem.SessionID,
		}
	}

	return &MCPToolResult{
		Success: true,
		Data: map[string]interface{}{
			"total":    total,
			"limit":    limit,
			"offset":   offset,
			"memories": results,
		},
	}, nil
}

// MemoryDeleteTool implements the memory_delete tool
type MemoryDeleteTool struct {
	memorySvc domain.MemoryService
}

func (t *MemoryDeleteTool) Name() string {
	return "memory_delete"
}

func (t *MemoryDeleteTool) Description() string {
	return "Delete a memory by ID"
}

func (t *MemoryDeleteTool) ServerName() string {
	return "memory"
}

func (t *MemoryDeleteTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type":        "string",
				"description": "The memory ID to delete",
			},
		},
		"required": []string{"id"},
	}
}

func (t *MemoryDeleteTool) Call(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	id, ok := args["id"].(string)
	if !ok || id == "" {
		return &MCPToolResult{
			Success: false,
			Error:   "missing required parameter: id",
		}, nil
	}

	if err := t.memorySvc.Delete(ctx, id); err != nil {
		return &MCPToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &MCPToolResult{
		Success: true,
		Data: map[string]interface{}{
			"success": true,
			"message": "Memory deleted successfully",
		},
	}, nil
}
