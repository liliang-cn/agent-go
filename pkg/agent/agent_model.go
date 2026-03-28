package agent

import "time"

// AgentKind distinguishes standalone agents, team lead-role agents, and reusable specialists.
type AgentKind string

const (
	AgentKindAgent      AgentKind = "agent"
	AgentKindCaptain    AgentKind = "captain"
	AgentKindLeadAgent  AgentKind = AgentKindCaptain
	AgentKindLeader     AgentKind = AgentKindCaptain
	AgentKindCommander  AgentKind = AgentKindCaptain
	AgentKindSpecialist AgentKind = "specialist"
)

// AgentModel represents the configuration of a dynamic agent in the database.
type AgentModel struct {
	ID                    string           `json:"id"`
	A2AID                 string           `json:"a2a_id,omitempty"`
	TeamID                string           `json:"team_id,omitempty"`
	Name                  string           `json:"name"`
	Kind                  AgentKind        `json:"kind"`
	Teams                 []TeamMembership `json:"teams,omitempty"`
	Description           string           `json:"description"`
	Instructions          string           `json:"instructions"`
	Model                 string           `json:"model"`
	PreferredProvider     string           `json:"preferred_provider,omitempty"`
	PreferredModel        string           `json:"preferred_model,omitempty"`
	RequiredLLMCapability int              `json:"required_llm_capability"`
	MCPTools              []string         `json:"mcp_tools"`
	Skills                []string         `json:"skills"`
	EnableRAG             bool             `json:"enable_rag"`
	EnableMemory          bool             `json:"enable_memory"`
	EnablePTC             bool             `json:"enable_ptc"`
	EnableMCP             bool             `json:"enable_mcp"`
	EnableA2A             bool             `json:"enable_a2a"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

type TeamMembership struct {
	AgentID   string    `json:"agent_id,omitempty"`
	TeamID    string    `json:"team_id"`
	TeamName  string    `json:"team_name,omitempty"`
	Role      AgentKind `json:"role"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Team struct {
	ID          string    `json:"id"`
	A2AID       string    `json:"a2a_id,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	EnableA2A   bool      `json:"enable_a2a"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
