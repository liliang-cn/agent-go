package memory

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// ScopePriority returns the priority of a scope type (higher = more important)
func ScopePriority(t domain.MemoryScopeType) int {
	switch t {
	case domain.MemoryScopeSession:
		return 100
	case domain.MemoryScopeAgent:
		return 80
	case domain.MemoryScopeSquad:
		return 60
	case domain.MemoryScopeProject:
		return 60
	case domain.MemoryScopeUser:
		return 40
	case domain.MemoryScopeGlobal:
		return 20
	default:
		return 0
	}
}

// ToBankID converts scope to bank ID format.
// Format: "global" or "agent:main" or "squad:alpha" or "session:uuid".
func ToBankID(s domain.MemoryScope) string {
	s = normalizeScope(s)
	if s.Type == domain.MemoryScopeGlobal {
		return "global"
	}
	if s.ID == "" {
		return string(s.Type)
	}
	return fmt.Sprintf("%s:%s", s.Type, s.ID)
}

// ParseBankID parses a bank ID back to MemoryScope
func ParseBankID(bankID string) domain.MemoryScope {
	if bankID == "" || bankID == "global" || bankID == "default" {
		return domain.MemoryScope{Type: domain.MemoryScopeGlobal}
	}

	parts := strings.SplitN(bankID, ":", 2)
	if len(parts) == 1 {
		scopeType := normalizeScopeType(domain.MemoryScopeType(parts[0]))
		switch scopeType {
		case domain.MemoryScopeGlobal, domain.MemoryScopeAgent, domain.MemoryScopeSquad, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeSession:
			return domain.MemoryScope{Type: scopeType}
		default:
			return domain.MemoryScope{Type: domain.MemoryScopeSession, ID: strings.TrimSpace(bankID)}
		}
	}

	return domain.MemoryScope{
		Type: normalizeScopeType(domain.MemoryScopeType(parts[0])),
		ID:   parts[1],
	}
}

// ScopeString returns string representation
func ScopeString(s domain.MemoryScope) string {
	s = normalizeScope(s)
	if s.ID == "" {
		return string(s.Type)
	}
	return fmt.Sprintf("%s:%s", s.Type, s.ID)
}

// IsGlobal returns true if this is a global scope
func IsGlobal(s domain.MemoryScope) bool {
	return s.Type == domain.MemoryScopeGlobal
}

// GlobalScope returns the global scope
func GlobalScope() domain.MemoryScope {
	return domain.MemoryScope{Type: domain.MemoryScopeGlobal}
}

// AgentScope returns an agent scope
func AgentScope(agentID string) domain.MemoryScope {
	return domain.MemoryScope{Type: domain.MemoryScopeAgent, ID: agentID}
}

// SquadScope returns a squad scope.
func SquadScope(squadID string) domain.MemoryScope {
	return domain.MemoryScope{Type: domain.MemoryScopeSquad, ID: squadID}
}

// ProjectScope returns a project scope
func ProjectScope(projectID string) domain.MemoryScope {
	// Keep the legacy helper but normalize project-scoped memory to squad scope.
	return SquadScope(projectID)
}

// UserScope returns a user scope
func UserScope(userID string) domain.MemoryScope {
	return domain.MemoryScope{Type: domain.MemoryScopeUser, ID: userID}
}

// SessionScope returns a session scope (legacy compatibility)
func SessionScope(sessionID string) domain.MemoryScope {
	return domain.MemoryScope{Type: domain.MemoryScopeSession, ID: sessionID}
}

// ScopeChain defines a chain of scopes to search
// Higher priority scopes are searched first
type ScopeChain []domain.MemoryScope

// DefaultScopeChain returns the default scope chain for searching.
// Order: Session > Agent > Squad/Project > User > Global.
func DefaultScopeChain(sessionID, agentID, squadOrProjectID, userID string) ScopeChain {
	var chain ScopeChain

	if sessionID != "" {
		chain = append(chain, SessionScope(sessionID))
	}
	if agentID != "" {
		chain = append(chain, AgentScope(agentID))
	}
	if squadOrProjectID != "" {
		chain = append(chain, SquadScope(squadOrProjectID))
	}
	if userID != "" {
		chain = append(chain, UserScope(userID))
	}
	chain = append(chain, GlobalScope())

	return chain
}

// SearchOrder returns scopes in search order (highest priority first)
func (c ScopeChain) SearchOrder() []domain.MemoryScope {
	// Already sorted by priority in DefaultScopeChain
	return c
}

// StoreOrder returns scopes in store order (determine where to store new memory)
func (c ScopeChain) StoreOrder() []domain.MemoryScope {
	// Store in the first non-global scope, or global if none
	for _, s := range c {
		if s.Type != domain.MemoryScopeGlobal {
			return []domain.MemoryScope{s}
		}
	}
	return []domain.MemoryScope{GlobalScope()}
}

// ToSlice returns the chain as a slice
func (c ScopeChain) ToSlice() []domain.MemoryScope {
	return c
}

// ScopeWeightConfig defines weights for different scope types
type ScopeWeightConfig struct {
	SessionWeight float64
	AgentWeight   float64
	SquadWeight   float64
	ProjectWeight float64
	UserWeight    float64
	GlobalWeight  float64
}

// DefaultScopeWeightConfig returns default scope weights
func DefaultScopeWeightConfig() *ScopeWeightConfig {
	return &ScopeWeightConfig{
		SessionWeight: 1.0,
		AgentWeight:   0.9,
		SquadWeight:   0.8,
		ProjectWeight: 0.8,
		UserWeight:    0.7,
		GlobalWeight:  0.6,
	}
}

// GetWeight returns the weight for a scope type
func (c *ScopeWeightConfig) GetWeight(scopeType domain.MemoryScopeType) float64 {
	switch scopeType {
	case domain.MemoryScopeSession:
		return c.SessionWeight
	case domain.MemoryScopeAgent:
		return c.AgentWeight
	case domain.MemoryScopeSquad:
		return c.SquadWeight
	case domain.MemoryScopeProject:
		return c.ProjectWeight
	case domain.MemoryScopeUser:
		return c.UserWeight
	case domain.MemoryScopeGlobal:
		return c.GlobalWeight
	default:
		return 0.5
	}
}

func normalizeScope(scope domain.MemoryScope) domain.MemoryScope {
	scope.Type = normalizeScopeType(scope.Type)
	if scope.Type == "" {
		scope.Type = domain.MemoryScopeGlobal
	}
	return scope
}

func normalizeScopeType(scopeType domain.MemoryScopeType) domain.MemoryScopeType {
	switch scopeType {
	case "":
		return domain.MemoryScopeGlobal
	case domain.MemoryScopeProject:
		return domain.MemoryScopeSquad
	default:
		return scopeType
	}
}
