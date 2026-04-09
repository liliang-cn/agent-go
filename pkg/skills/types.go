package skills

import (
	"context"
	"time"
)

// Skill represents a loaded skill from SKILL.md
type Skill struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	WhenToUse      string   `json:"when_to_use,omitempty"`
	Version        string   `json:"version"`
	Author         string   `json:"author,omitempty"`
	Collection     string   `json:"collection,omitempty"`
	CollectionPath string   `json:"collection_path,omitempty"`
	Paths          []string `json:"paths,omitempty"`

	// Progressive disclosure
	Steps []SkillStep `json:"steps"`

	// Metadata
	Category  string                 `json:"category,omitempty"`
	Tags      []string               `json:"tags,omitempty"`
	Variables map[string]VariableDef `json:"variables,omitempty"`

	// Execution
	Command                string                                                                 `json:"command,omitempty"`
	Handler                func(ctx context.Context, vars map[string]interface{}) (string, error) `json:"-"` // Direct Go function handler
	ForkMode               bool                                                                   `json:"fork_mode,omitempty"`
	UserInvocable          bool                                                                   `json:"user_invocable,omitempty"`
	DisableModelInvocation bool                                                                   `json:"disable_model_invocation,omitempty"`

	// System
	Path      string    `json:"path"` // Path to SKILL.md
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Collection struct {
	Name   string   `json:"name"`
	Path   string   `json:"path"`
	Scope  string   `json:"scope,omitempty"`
	Skills []*Skill `json:"skills"`
}

// SkillStep represents a single step in progressive disclosure
type SkillStep struct {
	ID          string                 `json:"id"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Content     string                 `json:"content"`
	Interactive bool                   `json:"interactive"`
	Confirm     bool                   `json:"confirm"` // Requires confirmation
	Variables   map[string]interface{} `json:"variables,omitempty"`
}

// VariableDef defines a variable that can be substituted
type VariableDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Type        string      `json:"type"` // string, number, boolean, file
	Required    bool        `json:"required"`
	Default     interface{} `json:"default,omitempty"`
	Pattern     string      `json:"pattern,omitempty"` // Regex validation
}

// ExecutionRequest represents a skill execution request
type ExecutionRequest struct {
	SkillID        string                 `json:"skill_id"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	Variables      map[string]interface{} `json:"variables,omitempty"`
	ForkMode       bool                   `json:"fork_mode,omitempty"`
	Interactive    bool                   `json:"interactive,omitempty"`
	Context        map[string]interface{} `json:"context,omitempty"` // RAG context, MCP tools, etc.
}

// ExecutionResult represents the result of skill execution
type ExecutionResult struct {
	Success    bool                   `json:"success"`
	SkillID    string                 `json:"skill_id"`
	Output     string                 `json:"output"`
	Data       interface{}            `json:"data,omitempty"`
	Variables  map[string]interface{} `json:"variables,omitempty"` // Output variables
	Steps      []StepResult           `json:"steps,omitempty"`
	Error      string                 `json:"error,omitempty"`
	ExecutedAt time.Time              `json:"executed_at"`
	Duration   time.Duration          `json:"duration"`
}

// StepResult represents the result of a single step execution
type StepResult struct {
	StepID    string      `json:"step_id"`
	Success   bool        `json:"success"`
	Output    string      `json:"output"`
	Data      interface{} `json:"data,omitempty"`
	Confirmed bool        `json:"confirmed,omitempty"`
	Error     string      `json:"error,omitempty"`
}

// SkillFilter filters skills when listing
type SkillFilter struct {
	Category   string
	Tag        string
	Enabled    *bool
	SearchTerm string
}

// SkillStore defines the persistence interface for skills
type SkillStore interface {
	SaveSkill(ctx context.Context, skill *Skill) error
	GetSkill(ctx context.Context, id string) (*Skill, error)
	ListSkills(ctx context.Context, filter SkillFilter) ([]*Skill, error)
	DeleteSkill(ctx context.Context, id string) error
	SaveExecution(ctx context.Context, result *ExecutionResult) error
	GetExecutions(ctx context.Context, skillID string, limit int) ([]*ExecutionResult, error)
}
