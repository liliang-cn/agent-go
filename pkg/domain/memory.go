package domain

import (
	"context"
	"encoding/json"
	"time"
)

// FlexibleStringArray handles JSON unmarshaling for fields that might be
// arrays, objects, or null. Used for robust LLM response parsing.
type FlexibleStringArray []string

// UnmarshalJSON implements custom JSON unmarshaling for FlexibleStringArray
// It handles: ["a","b"], {"key":"value"}, null, or even a single string
func (f *FlexibleStringArray) UnmarshalJSON(data []byte) error {
	// Handle null
	if len(data) == 0 || string(data) == "null" {
		*f = []string{}
		return nil
	}

	// Try to unmarshal as string array first
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*f = arr
		return nil
	}

	// Try to unmarshal as object - extract keys
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err == nil {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		*f = keys
		return nil
	}

	// Try to unmarshal as single string
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		if str != "" {
			*f = []string{str}
		} else {
			*f = []string{}
		}
		return nil
	}

	// If all else fails, return empty array
	*f = []string{}
	return nil
}

// Strings returns the string slice
func (f FlexibleStringArray) Strings() []string {
	if f == nil {
		return []string{}
	}
	return []string(f)
}

// MemoryScopeType defines the type of memory scope
type MemoryScopeType string

const (
	MemoryScopeGlobal  MemoryScopeType = "global"
	MemoryScopeAgent   MemoryScopeType = "agent"
	MemoryScopeTeam    MemoryScopeType = "team"
	MemoryScopeProject MemoryScopeType = "project"
	MemoryScopeUser    MemoryScopeType = "user"
	MemoryScopeSession MemoryScopeType = "session"
)

// MemoryScope represents a memory isolation scope
type MemoryScope struct {
	Type MemoryScopeType `json:"type"`
	ID   string          `json:"id,omitempty"`
}

// MemoryBankConfig represents the configuration for a Hindsight memory bank
type MemoryBankConfig struct {
	Mission    string   `json:"mission"`    // The identity/purpose of this memory bank
	Directives []string `json:"directives"` // Hard rules the agent must follow
	Skepticism int      `json:"skepticism"` // 1-5, how much to doubt new information
	Literalism int      `json:"literalism"` // 1-5, how strictly to interpret language
	Empathy    int      `json:"empathy"`    // 1-5, level of emotional resonance
}

// MentalModel represents a user-curated summary or rule
type MentalModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags,omitempty"`
}

// Entity represents a named entity extracted from text
type Entity struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases,omitempty"`
}

// MemoryType represents different types of long-term memories
type MemoryType string

const (
	MemoryTypeFact        MemoryType = "fact"
	MemoryTypeSkill       MemoryType = "skill"
	MemoryTypePattern     MemoryType = "pattern"
	MemoryTypeContext     MemoryType = "context"
	MemoryTypePreference  MemoryType = "preference"
	MemoryTypeObservation MemoryType = "observation" // LLM-consolidated from multiple facts
)

// MemorySourceType indicates how a memory was created
type MemorySourceType string

const (
	MemorySourceUserInput    MemorySourceType = "user_input"   // user explicitly stated this
	MemorySourceInferred     MemorySourceType = "inferred"     // agent inferred from behavior
	MemorySourceConsolidated MemorySourceType = "consolidated" // merged from multiple facts by Reflect
)

// MemoryRevision records a single modification to a memory.
type MemoryRevision struct {
	At      time.Time `json:"at"`                // when the change occurred
	By      string    `json:"by,omitempty"`      // actor: "user", "agent", "reflect", etc.
	Summary string    `json:"summary,omitempty"` // brief description of what changed
}

// Memory represents a single long-term memory
type Memory struct {
	ID           string                 `json:"id"`
	SessionID    string                 `json:"session_id,omitempty"` // Associated session, empty means global memory
	ScopeType    MemoryScopeType        `json:"scope_type,omitempty"` // Explicit scope type; supersedes raw SessionID parsing when set
	ScopeID      string                 `json:"scope_id,omitempty"`   // Scope identifier for non-global memories
	Type         MemoryType             `json:"type"`
	Content      string                 `json:"content"`
	Vector       []float64              `json:"vector,omitempty"`
	Keywords     []string               `json:"keywords,omitempty"`
	Tags         []string               `json:"tags,omitempty"`
	Importance   float64                `json:"importance"` // 0-1, used for sorting/priority
	AccessCount  int                    `json:"access_count"`
	LastAccessed time.Time              `json:"last_accessed"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`

	// Hindsight: temporal and evidence fields
	EvidenceIDs     []string         `json:"evidence_ids,omitempty"`     // fact IDs that support this observation
	Confidence      float64          `json:"confidence,omitempty"`       // 0-1, confidence of this observation
	ValidFrom       time.Time        `json:"valid_from,omitempty"`       // when this fact became valid
	ValidTo         *time.Time       `json:"valid_to,omitempty"`         // nil means currently valid
	SupersededBy    string           `json:"superseded_by,omitempty"`    // ID of the memory that replaced this one
	SourceType      MemorySourceType `json:"source_type,omitempty"`      // how this memory was created
	Conflicting     bool             `json:"conflicting,omitempty"`      // true if this observation has conflicting evidence
	RevisionHistory []MemoryRevision `json:"revision_history,omitempty"` // ordered list of changes to this memory
	Archived        bool             `json:"archived,omitempty"`         // true if the memory has been archived from active retrieval
	ArchivedAt      *time.Time       `json:"archived_at,omitempty"`      // when the memory was archived
	ArchiveReason   string           `json:"archive_reason,omitempty"`   // human-readable archive reason
}

// MemoryWithScore represents a memory with its similarity score
type MemoryWithScore struct {
	*Memory
	Score float64 `json:"score"`
}

// MemoryRetrieveResult represents the result of a memory retrieval
type MemoryRetrieveResult struct {
	Memories    []*MemoryWithScore `json:"memories"`
	Query       string             `json:"query"`
	Threshold   float64            `json:"threshold"`    // Minimum relevance score
	HasRelevant bool               `json:"has_relevant"` // Whether relevant memories were found
}

// MemoryQueryContext describes the runtime scope chain available to one memory lookup.
// Session is the most local scope, followed by agent/thread, team/process, user, and global.
type MemoryQueryContext struct {
	SessionID string `json:"session_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// MemoryStoreRequest is a request to store memories after task completion
type MemoryStoreRequest struct {
	SessionID    string                 `json:"session_id"`
	AgentID      string                 `json:"agent_id,omitempty"`
	TeamID       string                 `json:"team_id,omitempty"`
	UserID       string                 `json:"user_id,omitempty"`
	TaskGoal     string                 `json:"task_goal"`
	TaskResult   string                 `json:"task_result"`
	ExecutionLog string                 `json:"execution_log,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// MemorySummaryResult is the LLM-extracted memory summary
type MemorySummaryResult struct {
	ShouldStore bool         `json:"should_store"`
	Memories    []MemoryItem `json:"memories"`
	Reasoning   string       `json:"reasoning"`
}

// MemoryItem represents a single memory item extracted by LLM
type MemoryItem struct {
	Type            MemoryType          `json:"type"`
	Scope           MemoryScopeType     `json:"scope,omitempty"`
	PromoteTo       MemoryScopeType     `json:"promote_to,omitempty"`
	Content         string              `json:"content"`
	Importance      float64             `json:"importance"`
	ScopeReason     string              `json:"scope_reason,omitempty"`
	PromotionReason string              `json:"promotion_reason,omitempty"`
	Tags            FlexibleStringArray `json:"tags,omitempty"`
	Entities        FlexibleStringArray `json:"entities,omitempty"`
}

// MemoryStore defines the interface for memory persistence
type MemoryStore interface {
	// Store saves a new memory
	Store(ctx context.Context, memory *Memory) error

	// Search performs vector search for related memories
	Search(ctx context.Context, vector []float64, topK int, minScore float64) ([]*MemoryWithScore, error)

	// SearchBySession searches memories within a specific session
	SearchBySession(ctx context.Context, sessionID string, vector []float64, topK int) ([]*MemoryWithScore, error)

	// SearchByScope searches memories within specific scopes
	// Scopes are searched in priority order (higher priority first)
	SearchByScope(ctx context.Context, vector []float64, scopes []MemoryScope, topK int) ([]*MemoryWithScore, error)

	// StoreWithScope stores a memory with a specific scope
	StoreWithScope(ctx context.Context, memory *Memory, scope MemoryScope) error

	// SearchByText performs full-text search (BM25)
	// Returns memories matching the query text
	SearchByText(ctx context.Context, query string, topK int) ([]*MemoryWithScore, error)

	// Get retrieves a memory by ID
	Get(ctx context.Context, id string) (*Memory, error)

	// Update updates an existing memory
	Update(ctx context.Context, memory *Memory) error

	// IncrementAccess increments the access count and updates last_accessed
	IncrementAccess(ctx context.Context, id string) error

	// GetByType retrieves memories by type
	GetByType(ctx context.Context, memoryType MemoryType, limit int) ([]*Memory, error)

	// List lists all memories with optional pagination
	List(ctx context.Context, limit, offset int) ([]*Memory, int, error)

	// Delete removes a memory
	Delete(ctx context.Context, id string) error

	// DeleteBySession removes all memories for a session
	DeleteBySession(ctx context.Context, sessionID string) error

	// ConfigureBank sets mission and disposition for a memory bank (session)
	ConfigureBank(ctx context.Context, sessionID string, config *MemoryBankConfig) error

	// Reflect triggers knowledge consolidation for a bank
	Reflect(ctx context.Context, sessionID string) (string, error)

	// AddMentalModel adds a curated mental model to the memory system
	AddMentalModel(ctx context.Context, model *MentalModel) error

	// InitSchema initializes the memory tables
	InitSchema(ctx context.Context) error
}

// MemoryService defines the interface for memory management
type MemoryService interface {
	// RetrieveAndInject searches relevant memories and formats them for LLM context
	RetrieveAndInject(ctx context.Context, query string, sessionID string) (string, []*MemoryWithScore, error)

	// RetrieveAndInjectWithLogic is like RetrieveAndInject but also returns the
	// IndexNavigator's reasoning string (MemoryLogic) explaining which memories
	// were selected and why. Returns: formatted context, scored memories, reasoning, error.
	RetrieveAndInjectWithLogic(ctx context.Context, query string, sessionID string) (string, []*MemoryWithScore, string, error)

	// RetrieveAndInjectWithContext searches relevant memories using an explicit scope chain.
	RetrieveAndInjectWithContext(ctx context.Context, query string, queryContext MemoryQueryContext) (string, []*MemoryWithScore, error)

	// RetrieveAndInjectWithContextAndLogic is the context-aware form of RetrieveAndInjectWithLogic.
	RetrieveAndInjectWithContextAndLogic(ctx context.Context, query string, queryContext MemoryQueryContext) (string, []*MemoryWithScore, string, error)

	// StoreIfWorthwhile analyzes task completion and decides what to store
	StoreIfWorthwhile(ctx context.Context, req *MemoryStoreRequest) error

	// Add directly adds a memory
	Add(ctx context.Context, memory *Memory) error

	// Update updates a memory's content (LLM-driven)
	Update(ctx context.Context, id string, content string) error

	// Search searches memories by query
	Search(ctx context.Context, query string, topK int) ([]*MemoryWithScore, error)

	// Get retrieves a memory by ID
	Get(ctx context.Context, id string) (*Memory, error)

	// List lists memories
	List(ctx context.Context, limit, offset int) ([]*Memory, int, error)

	// Delete removes a memory
	Delete(ctx context.Context, id string) error

	// ConfigureBank sets mission and disposition for a memory bank (session)
	ConfigureBank(ctx context.Context, sessionID string, config *MemoryBankConfig) error

	// Reflect triggers knowledge consolidation for a bank
	Reflect(ctx context.Context, sessionID string) (string, error)

	// AddMentalModel adds a curated mental model to the memory system
	AddMentalModel(ctx context.Context, model *MentalModel) error
}
