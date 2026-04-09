package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	skillgo "github.com/liliang-cn/skills-go/skill"
	"golang.org/x/sync/errgroup"
)

// Service orchestrates all skill functionality using skills-go as the backend
type Service struct {
	config   *Config
	store    SkillStore
	registry *skillgo.Registry
	loader   *skillgo.Loader

	// Integration services (optional)
	ragService interface{} // domain.RAGProcessor
	mcpService interface{} // mcp.Service
	agentSvc   interface{} // agent.Service

	mu     sync.RWMutex
	loaded bool
}

// NewService creates a new skills service
func NewService(cfg *Config) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Create skills-go loader and registry
	loader := skillgo.NewLoader(skillgo.WithPaths(cfg.Paths...))
	registry := skillgo.NewRegistry(loader)

	svc := &Service{
		config:   cfg,
		loader:   loader,
		registry: registry,
	}

	return svc, nil
}

// RegisterFunction registers a Go function as a skill
func (s *Service) RegisterFunction(id, name, description string, fn func(ctx context.Context, vars map[string]interface{}) (string, error)) {
	s.registry.RegisterFunction(id, description, skillgo.HandlerFunc(fn))
}

// SetStore sets the skill store
func (s *Service) SetStore(store SkillStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

// SetRAGService sets the RAG service for integration
func (s *Service) SetRAGService(ragService interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ragService = ragService
}

// SetMCPService sets the MCP service for integration
func (s *Service) SetMCPService(mcpService interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mcpService = mcpService
}

// SetAgentService sets the agent service for integration
func (s *Service) SetAgentService(agentSvc interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentSvc = agentSvc
}

// LoadAll loads skills from all configured paths
func (s *Service) LoadAll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.registry.Load(ctx); err != nil {
		return fmt.Errorf("failed to load skills: %w", err)
	}

	if err := s.loadDirectSkills(ctx); err != nil {
		return fmt.Errorf("failed to load direct skills: %w", err)
	}

	s.loaded = true
	return nil
}

// IsLoaded returns whether skills have been loaded
func (s *Service) IsLoaded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loaded
}

// ListSkills lists available skills
func (s *Service) ListSkills(ctx context.Context, filter SkillFilter) ([]*Skill, error) {
	// Always return from registry first (it's the source of truth for loaded skills)
	s.mu.RLock()
	defer s.mu.RUnlock()

	skills := s.registry.ListSkills()
	var result []*Skill

	for _, sk := range skills {
		skill := convertFromSkillGo(sk)
		if s.matchesFilter(skill, filter) {
			result = append(result, skill)
		}
	}

	return result, nil
}

// ListCollections lists grouped skill collections/bundles.
func (s *Service) ListCollections(ctx context.Context) ([]*Collection, error) {
	skills, err := s.ListSkills(ctx, SkillFilter{})
	if err != nil {
		return nil, err
	}

	byName := make(map[string]*Collection)
	for _, skill := range skills {
		if skill.Collection == "" {
			continue
		}
		col, ok := byName[skill.Collection]
		if !ok {
			col = &Collection{
				Name: skill.Collection,
				Path: skill.CollectionPath,
			}
			byName[skill.Collection] = col
		}
		col.Skills = append(col.Skills, skill)
	}

	collections := make([]*Collection, 0, len(byName))
	for _, col := range byName {
		sort.Slice(col.Skills, func(i, j int) bool {
			return col.Skills[i].Name < col.Skills[j].Name
		})
		collections = append(collections, col)
	}
	sort.Slice(collections, func(i, j int) bool {
		return collections[i].Name < collections[j].Name
	})
	return collections, nil
}

// matchesFilter checks if a skill matches the filter
func (s *Service) matchesFilter(skill *Skill, filter SkillFilter) bool {
	if filter.Category != "" && skill.Category != filter.Category {
		return false
	}
	if filter.Tag != "" {
		found := false
		for _, tag := range skill.Tags {
			if tag == filter.Tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Enabled != nil && *filter.Enabled != skill.Enabled {
		return false
	}
	if filter.SearchTerm != "" {
		searchTerm := strings.ToLower(filter.SearchTerm)
		combined := strings.ToLower(skill.Name + " " + skill.Description + " " + skill.WhenToUse)
		if !strings.Contains(combined, searchTerm) {
			return false
		}
	}
	return true
}

// GetSkill retrieves a skill by ID
func (s *Service) GetSkill(ctx context.Context, id string) (*Skill, error) {
	// Always get from registry first (it's the source of truth for loaded skills)
	s.mu.RLock()
	defer s.mu.RUnlock()

	sk, err := s.registry.Get(id)
	if err != nil {
		return nil, fmt.Errorf("skill not found: %s", id)
	}

	return convertFromSkillGo(sk), nil
}

// Resolve resolves skills for a given query
func (s *Service) Resolve(ctx context.Context, query string) ([]*Skill, error) {
	skills, err := s.ListSkills(ctx, SkillFilter{})
	if err != nil {
		return nil, err
	}

	// Simple keyword matching for now
	queryLower := strings.ToLower(query)
	var matched []*Skill

	for _, skill := range skills {
		if !skill.Enabled || !skill.UserInvocable {
			continue
		}

		// Check name and description
		if strings.Contains(strings.ToLower(skill.Name), queryLower) ||
			strings.Contains(strings.ToLower(skill.Description), queryLower) {
			matched = append(matched, skill)
			continue
		}

		// Check tags
		for _, tag := range skill.Tags {
			if strings.Contains(strings.ToLower(tag), queryLower) {
				matched = append(matched, skill)
				break
			}
		}
	}

	return matched, nil
}

// ResolveForModel finds model-invocable skills relevant to the current task.
func (s *Service) ResolveForModel(ctx context.Context, query string, touchedPaths []string) ([]*Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resolved, err := s.registry.ResolveForModel(ctx, query, touchedPaths)
	if err != nil {
		return nil, err
	}
	out := make([]*Skill, 0, len(resolved))
	for _, sk := range resolved {
		out = append(out, convertFromSkillGo(sk))
	}
	return out, nil
}

// Execute executes a skill
func (s *Service) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	start := time.Now()

	// Check if it's a handler skill first
	if handler := s.registry.GetHandler(req.SkillID); handler != nil {
		return s.executeHandler(ctx, req, handler, start)
	}

	// Get skill from registry
	sk, err := s.registry.GetWithLevel(ctx, req.SkillID, skillgo.LoadLevelContent)
	if err != nil {
		return &ExecutionResult{
			Success:    false,
			SkillID:    req.SkillID,
			Error:      err.Error(),
			ExecutedAt: start,
			Duration:   time.Since(start),
		}, err
	}

	// Convert to agentgo skill for consistency
	skill := convertFromSkillGo(sk)
	if !skill.Enabled {
		return &ExecutionResult{
			Success:    false,
			SkillID:    req.SkillID,
			Error:      fmt.Sprintf("skill %s is disabled", skill.ID),
			ExecutedAt: start,
			Duration:   time.Since(start),
		}, fmt.Errorf("skill %s is disabled", skill.ID)
	}

	var output string
	var execErr error

	// Handle built-in skills
	switch skill.ID {
	case "rag-query", "rag":
		output, execErr = s.executeRAGQuery(ctx, req)
	default:
		// For file-based skills, render the content with variable substitution
		output = renderSkillContent(sk.Content, req.Variables)
	}

	result := &ExecutionResult{
		Success:    execErr == nil,
		SkillID:    req.SkillID,
		Output:     output,
		Variables:  req.Variables,
		ExecutedAt: start,
		Duration:   time.Since(start),
	}

	if execErr != nil {
		result.Error = execErr.Error()
	}

	// Save execution result if store is available
	if s.store != nil {
		_ = s.store.SaveExecution(ctx, result)
	}

	return result, nil
}

func (s *Service) loadDirectSkills(ctx context.Context) error {
	candidates := make([]string, 0, len(s.config.Paths)+1)
	candidates = append(candidates, s.config.Paths...)
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}

		skillPath, ok := resolveDirectSkillPath(candidate)
		if !ok {
			continue
		}

		absPath, err := filepath.Abs(skillPath)
		if err != nil {
			absPath = skillPath
		}
		if _, exists := seen[absPath]; exists {
			continue
		}
		seen[absPath] = struct{}{}

		if _, err := s.registry.GetByPath(absPath); err == nil {
			continue
		}

		sk, err := s.loader.LoadMetadata(ctx, absPath)
		if err != nil {
			return err
		}
		s.registry.Add(sk)
	}

	return nil
}

func resolveDirectSkillPath(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}

	if info.IsDir() {
		skillFile := filepath.Join(path, "SKILL.md")
		if fileInfo, err := os.Stat(skillFile); err == nil && !fileInfo.IsDir() {
			return path, true
		}
		return "", false
	}

	if filepath.Base(path) == "SKILL.md" {
		return path, true
	}

	return "", false
}

// executeHandler executes a Go function handler
func (s *Service) executeHandler(ctx context.Context, req *ExecutionRequest, handler skillgo.HandlerFunc, start time.Time) (*ExecutionResult, error) {
	output, err := handler(ctx, req.Variables)
	if err != nil {
		return &ExecutionResult{
			Success:    false,
			SkillID:    req.SkillID,
			Error:      err.Error(),
			ExecutedAt: start,
			Duration:   time.Since(start),
		}, err
	}

	return &ExecutionResult{
		Success:    true,
		SkillID:    req.SkillID,
		Output:     output,
		Variables:  req.Variables,
		ExecutedAt: start,
		Duration:   time.Since(start),
	}, nil
}

// RunSkill is a simplified entry point for executing a skill by ID
func (s *Service) RunSkill(ctx context.Context, id string, vars map[string]interface{}) (string, error) {
	req := &ExecutionRequest{
		SkillID:   id,
		Variables: vars,
	}
	res, err := s.Execute(ctx, req)
	if err != nil {
		return "", err
	}
	return res.Output, nil
}

// ExecuteMultiple executes multiple skills in parallel using goroutines
func (s *Service) ExecuteMultiple(ctx context.Context, requests []*ExecutionRequest) ([]*ExecutionResult, error) {
	if len(requests) == 0 {
		return nil, nil
	}

	results := make([]*ExecutionResult, len(requests))

	// Use errgroup for parallel execution
	g, groupCtx := errgroup.WithContext(ctx)

	for i, req := range requests {
		// Capture index and request for the goroutine
		idx, request := i, req

		g.Go(func() error {
			result, err := s.Execute(groupCtx, request)
			if err != nil {
				return err
			}
			results[idx] = result
			return nil
		})
	}

	// Wait for all skills to finish
	if err := g.Wait(); err != nil {
		return results, err
	}

	return results, nil
}

// executeRAGQuery executes a RAG query
func (s *Service) executeRAGQuery(ctx context.Context, req *ExecutionRequest) (string, error) {
	if s.ragService == nil {
		return "", fmt.Errorf("RAG service not configured")
	}

	// Get query from variables
	query, ok := req.Variables["query"].(string)
	if !ok {
		return "", fmt.Errorf("query variable is required")
	}

	// Get top_k from variables (default 5)
	topK := 5
	if tk, ok := req.Variables["top_k"].(float64); ok {
		topK = int(tk)
	} else if tk, ok := req.Variables["top_k"].(int); ok {
		topK = tk
	}

	// Get temperature from variables (default 0.7)
	temperature := 0.7
	if temp, ok := req.Variables["temperature"].(float64); ok {
		temperature = temp
	}

	// Call RAG service
	if ragProc, ok := s.ragService.(interface {
		Query(ctx context.Context, req domain.QueryRequest) (domain.QueryResponse, error)
	}); ok {
		ragReq := domain.QueryRequest{
			Query:        query,
			TopK:         topK,
			Temperature:  temperature,
			ShowSources:  true,
			ShowThinking: false,
		}

		resp, err := ragProc.Query(ctx, ragReq)
		if err != nil {
			return "", fmt.Errorf("RAG query failed: %w", err)
		}

		// Format output
		output := fmt.Sprintf("Question: %s\n\nAnswer:\n%s\n", query, resp.Answer)
		if len(resp.Sources) > 0 {
			output += fmt.Sprintf("\nSources: %d documents found", len(resp.Sources))
		}
		return output, nil
	}

	return "", fmt.Errorf("RAG service does not support Query interface")
}

// BuildSystemPrompt builds a system prompt for LLM with skills context
func (s *Service) BuildSystemPrompt(ctx context.Context, skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var prompt string
	prompt += "# Available Skills\n\n"

	for _, skill := range skills {
		prompt += fmt.Sprintf("## /%s", skill.Name)
		if skill.Description != "" {
			prompt += fmt.Sprintf(": %s", skill.Description)
		}
		prompt += "\n"

		if skill.Command != "" {
			prompt += fmt.Sprintf("Command: `%s`\n", skill.Command)
		}

		if len(skill.Variables) > 0 {
			prompt += "Variables:\n"
			for _, v := range skill.Variables {
				required := ""
				if v.Required {
					required = " (required)"
				}
				prompt += fmt.Sprintf("  - %s%s: %s\n", v.Name, required, v.Description)
			}
		}

		prompt += "\n"
	}

	return prompt
}

// RegisterAsMCPTools registers skills as MCP tools
func (s *Service) RegisterAsMCPTools() ([]domain.ToolDefinition, error) {
	skills, err := s.ListSkills(context.Background(), SkillFilter{})
	if err != nil {
		return nil, err
	}

	var tools []domain.ToolDefinition

	for _, skill := range skills {
		// Only include skills that are enabled and allowed for model invocation
		if !skill.Enabled || skill.DisableModelInvocation {
			continue
		}

		// Build parameter schema from skill variables
		properties := make(map[string]interface{})
		required := make([]string, 0)

		for _, v := range skill.Variables {
			prop := map[string]interface{}{
				"type":        getTypeString(v.Type),
				"description": v.Description,
			}
			if v.Default != nil {
				prop["default"] = v.Default
			}
			properties[v.Name] = prop

			if v.Required {
				required = append(required, v.Name)
			}
		}

		desc := skill.Description
		if desc == "" {
			desc = skill.Name
		}
		// Clarify that calling this skill returns its workflow instructions.
		desc = "Skill workflow: " + desc + ". Call this tool to receive step-by-step instructions for this task; you MUST then follow those instructions to complete the work."

		tool := domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        fmt.Sprintf("skill_%s", skill.ID),
				Description: desc,
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": properties,
					"required":   required,
				},
			},
		}

		tools = append(tools, tool)
	}

	return tools, nil
}

// GetExecutionHistory retrieves execution history
func (s *Service) GetExecutionHistory(ctx context.Context, skillID string, limit int) ([]*ExecutionResult, error) {
	if s.store != nil {
		return s.store.GetExecutions(ctx, skillID, limit)
	}
	return nil, fmt.Errorf("no store configured")
}

// Close closes the skills service
func (s *Service) Close() error {
	if s.store != nil {
		if closer, ok := s.store.(interface{ Close() error }); ok {
			return closer.Close()
		}
	}
	return nil
}

// Helper functions

// getTypeString returns the JSON schema type string for a variable type
func getTypeString(typ string) string {
	switch typ {
	case "string", "file":
		return "string"
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		return "array"
	case "object":
		return "object"
	default:
		return "string"
	}
}

// renderSkillContent substitutes variables into skill template content.
// Supports two substitution formats:
// 1. {{varname}} - simple placeholder
// 2. ```input:varname\n``` - code block placeholder (replaced with variable value)
func renderSkillContent(content string, variables map[string]interface{}) string {
	if len(variables) == 0 {
		return content
	}
	result := content
	for k, v := range variables {
		val := fmt.Sprintf("%v", v)
		// Replace ```input:varname\n``` blocks
		result = strings.ReplaceAll(result, "```input:"+k+"\n```", val)
		// Replace {{varname}} placeholders
		result = strings.ReplaceAll(result, "{{"+k+"}}", val)
		// Replace {varname} placeholders
		result = strings.ReplaceAll(result, "{"+k+"}", val)
	}
	return result
}

func convertFromSkillGo(sk *skillgo.Skill) *Skill {
	skill := &Skill{
		ID:                     sk.Name,
		Name:                   sk.Name,
		Description:            sk.Meta.Description,
		WhenToUse:              sk.Meta.WhenToUse,
		Version:                sk.Version,
		Collection:             sk.Collection,
		CollectionPath:         sk.CollectionPath,
		Paths:                  append([]string(nil), sk.Meta.Paths...),
		Path:                   sk.Path,
		Enabled:                true,
		UserInvocable:          sk.IsUserInvocable(),
		DisableModelInvocation: !sk.IsModelInvocable(),
		ForkMode:               sk.ShouldFork(),
		CreatedAt:              sk.LoadedAt,
		UpdatedAt:              sk.LoadedAt,
		Variables:              make(map[string]VariableDef),
	}

	// Extract category/tags from metadata
	if sk.Meta.Metadata != nil {
		if cat, ok := sk.Meta.Metadata["category"].(string); ok {
			skill.Category = cat
		}
		if tags, ok := sk.Meta.Metadata["tags"].([]interface{}); ok {
			for _, t := range tags {
				if ts, ok := t.(string); ok {
					skill.Tags = append(skill.Tags, ts)
				}
			}
		}
	}

	return skill
}
