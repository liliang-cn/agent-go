package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/poolsvc"
)

// Manager is the application-level host for AgentGo: it owns the persistent
// Store (agent definitions, sessions, tasks, checkpoints), caches one *Service
// per named agent, and exposes the task surface (submit / list / get / trace /
// cancel / replay).
//
// v3 has no team or role orchestration. A Manager holds a flat registry of
// agent definitions; composition happens inside a single agent via subagent
// tools (see WithSubagents), not via manager-level routing.
type Manager struct {
	store            *Store
	cfg              *config.Config
	injectedLLM      domain.Generator
	injectedEmbedder domain.Embedder
	services         map[string]*Service
	mu               sync.RWMutex

	sessionMu     sync.Mutex
	agentSessions map[string]string
	taskMu        sync.RWMutex
	asyncTasks    map[string]*AsyncTask
	sessionTasks  map[string][]string
	taskSubs      map[string]map[chan *TaskEvent]struct{}
	taskCancels   map[string]context.CancelFunc
	checkpointWr  *checkpointWriter
	agentTools    map[string][]registeredAgentTool

	// streamOverride, when non-nil, replaces the real agent run inside
	// RunStream. It is the single dispatch seam v3 exposes: embedders (and
	// tests) can intercept every manager-driven run without the manager
	// growing routing logic of its own.
	streamOverride func(ctx context.Context, agentName, input string, opts []RunOption) (<-chan *Event, error)
}

// NewManager creates a Manager backed by the given Store.
func NewManager(s *Store) *Manager {
	m := &Manager{
		store:         s,
		services:      make(map[string]*Service),
		agentSessions: make(map[string]string),
		asyncTasks:    make(map[string]*AsyncTask),
		sessionTasks:  make(map[string][]string),
		taskSubs:      make(map[string]map[chan *TaskEvent]struct{}),
		taskCancels:   make(map[string]context.CancelFunc),
		agentTools:    make(map[string][]registeredAgentTool),
	}
	m.checkpointWr = newCheckpointWriter(s)
	return m
}

// SetConfig installs the AgentGo config used when building agent services.
func (m *Manager) SetConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}

// SetLLM injects a custom LLM implementation for services built by this manager.
// Passing nil clears the override and restores global-pool fallback behavior.
func (m *Manager) SetLLM(llm domain.Generator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.injectedLLM = llm
	m.services = make(map[string]*Service)
}

// SetEmbedder injects a custom embedder for services built by this manager.
func (m *Manager) SetEmbedder(embedder domain.Embedder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.injectedEmbedder = embedder
	m.services = make(map[string]*Service)
}

// GetStore returns the underlying Store.
func (m *Manager) GetStore() *Store { return m.store }

func (m *Manager) configuredAgentGoConfig() *config.Config {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	if cfg != nil {
		return cfg
	}
	loaded, err := config.Load()
	if err != nil {
		return nil
	}
	return loaded
}

func (m *Manager) getAgentName() string {
	if cfg := m.configuredAgentGoConfig(); cfg != nil && cfg.Agent.Name != "" {
		return cfg.Agent.Name
	}
	return "AgentGo"
}

// ListAgents returns every persisted agent definition.
func (m *Manager) ListAgents() ([]*AgentModel, error) {
	return m.store.ListAgentModels()
}

// CreateAgent persists a new agent definition.
func (m *Manager) CreateAgent(_ context.Context, model *AgentModel) (*AgentModel, error) {
	if model == nil {
		return nil, fmt.Errorf("agent model is required")
	}
	if strings.TrimSpace(model.Name) == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	if strings.TrimSpace(model.ID) == "" {
		model.ID = uuid.New().String()
	}
	if err := m.store.SaveAgentModel(model); err != nil {
		return nil, err
	}
	return model, nil
}

// DefaultAgentName is the name of the single agent seeded into a fresh store.
const DefaultAgentName = "Assistant"

// SeedDefaultAgent makes sure at least one usable agent definition exists.
// v3 seeds exactly one general-purpose agent — there are no built-in roles.
func (m *Manager) SeedDefaultAgent() error {
	name := strings.TrimSpace(m.getAgentName())
	if name == "" || name == "AgentGo" {
		name = DefaultAgentName
	}
	if existing, err := m.store.GetAgentModelByName(name); err == nil && existing != nil {
		return nil
	}
	if models, err := m.store.ListAgentModels(); err == nil && len(models) > 0 {
		return nil
	}
	return m.store.SaveAgentModel(&AgentModel{
		ID:           uuid.New().String(),
		Name:         name,
		Description:  "General-purpose agent.",
		Instructions: "You are a capable, direct assistant. Use the tools you have to finish the task, then answer.",
		MCPTools:     []string{"*"},
		Skills:       []string{"*"},
		EnableMemory: true,
		EnablePTC:    true,
		EnableMCP:    true,
	})
}

// GetAgentByName looks up a persisted agent definition by name.
func (m *Manager) GetAgentByName(name string) (*AgentModel, error) {
	return m.store.GetAgentModelByName(strings.TrimSpace(name))
}

// Service returns (building and caching if needed) the *Service for an agent.
func (m *Manager) Service(name string) (*Service, error) { return m.getOrBuildService(name) }

func (m *Manager) getOrBuildService(name string) (*Service, error) {
	m.mu.RLock()
	svc, exists := m.services[name]
	m.mu.RUnlock()
	if exists {
		return svc, nil
	}

	model, err := m.store.GetAgentModelByName(name)
	if err != nil {
		return nil, err
	}
	newSvc, err := m.buildServiceForModel(model)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if svc, exists := m.services[name]; exists {
		_ = newSvc.Close()
		return svc, nil
	}
	m.services[name] = newSvc
	return newSvc, nil
}

func (m *Manager) buildServiceForModel(model *AgentModel) (*Service, error) {
	if model == nil {
		return nil, fmt.Errorf("agent model is required")
	}

	builder := New(model.Name)
	systemPrompt := strings.TrimSpace(model.Instructions)

	m.mu.RLock()
	cfg := m.cfg
	injectedLLM := m.injectedLLM
	injectedEmbedder := m.injectedEmbedder
	m.mu.RUnlock()

	if cfg == nil {
		if loaded, err := config.Load(); err == nil {
			cfg = loaded
		}
	}
	if cfg != nil {
		systemPrompt = buildStandaloneAgentPrompt(cfg, model)
		builder.WithConfig(cfg)
	}
	builder.WithSystemPrompt(systemPrompt)

	if injectedLLM != nil {
		builder.WithLLM(injectedLLM)
	}
	if injectedEmbedder != nil {
		builder.WithEmbedder(injectedEmbedder)
	}
	if cfg != nil && (injectedLLM == nil || injectedEmbedder == nil) {
		globalPool := poolsvc.Global()
		if initErr := globalPool.Initialize(context.Background(), cfg); initErr == nil {
			if injectedLLM == nil {
				if llmSvc, llmErr := globalPool.GetLLMServiceWithHint(selectionHintForAgentModel(model)); llmErr == nil {
					builder.WithLLM(llmSvc)
				}
			}
			if injectedEmbedder == nil {
				if embedSvc, embedErr := globalPool.GetEmbeddingService(context.Background()); embedErr == nil {
					builder.WithEmbedder(embedSvc)
				}
			}
		}
	}

	if model.EnableRAG {
		builder.WithRAG()
	}
	if model.EnableMemory {
		storeType := strings.TrimSpace(model.MemoryStoreType)
		if storeType == "" && cfg != nil {
			storeType = cfg.GetMemoryStoreType().String()
		}
		if storeType != "" {
			builder.WithMemory(WithMemoryStoreType(storeType))
		} else {
			builder.WithMemory()
		}
	}
	if model.EnableMCP {
		builder.WithMCP()
	}
	if len(model.Skills) > 0 {
		builder.WithSkills()
	}

	newSvc, err := builder.Build()
	if err != nil {
		return nil, err
	}

	if len(model.MCPTools) > 0 {
		newSvc.agent.SetAllowedMCPTools(model.MCPTools)
	} else {
		newSvc.agent.SetAllowedMCPTools([]string{})
	}
	if len(model.Skills) > 0 {
		newSvc.agent.SetAllowedSkills(model.Skills)
	} else {
		newSvc.agent.SetAllowedSkills([]string{})
	}

	m.applyRegisteredAgentTools(newSvc, model.Name)
	RegisterDefaultOutputLints(newSvc)
	newSvc.SetCheckpointSink(m)

	if label := configuredModelLabel(model); label != "" {
		newSvc.agent.SetModel(label)
	}
	newSvc.SetMemoryScope(model.Name, "", "")

	return newSvc, nil
}

// Run executes one synchronous turn for the named agent.
func (m *Manager) Run(ctx context.Context, agentName, input string, opts ...RunOption) (*ExecutionResult, error) {
	svc, err := m.getOrBuildService(agentName)
	if err != nil {
		return nil, err
	}
	return svc.Run(ctx, input, opts...)
}

// RunStream executes one turn for the named agent and streams its events.
func (m *Manager) RunStream(ctx context.Context, agentName, input string, opts ...RunOption) (<-chan *Event, error) {
	m.mu.RLock()
	override := m.streamOverride
	m.mu.RUnlock()
	if override != nil {
		return override(ctx, agentName, input, opts)
	}
	svc, err := m.getOrBuildService(agentName)
	if err != nil {
		return nil, err
	}
	return svc.RunStreamWithOptions(ctx, input, opts...)
}

// sessionIDFor returns a stable per-(conversation, agent) session UUID.
func (m *Manager) sessionIDFor(conversationKey, agentName string) string {
	key := strings.TrimSpace(conversationKey) + "\x00" + strings.TrimSpace(agentName)
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	if id, ok := m.agentSessions[key]; ok {
		return id
	}
	id := uuid.New().String()
	m.agentSessions[key] = id
	return id
}

// Close releases every cached service.
func (m *Manager) Close() error {
	m.mu.Lock()
	services := m.services
	m.services = make(map[string]*Service)
	m.mu.Unlock()
	var firstErr error
	for _, svc := range services {
		if err := svc.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// alwaysFalse is a placeholder for the removed built-in role predicates. v3
// has no privileged agent names, so every gate that used to ask "is this the
// dispatcher?" now answers no.
func alwaysFalse(_ *Agent) bool { return false }

// sessionIDOrEmpty returns the session's UUID, or "" when session is nil.
func sessionIDOrEmpty(session *Session) string {
	if session == nil {
		return ""
	}
	return session.GetID()
}

// SetStreamOverride installs (or clears, with nil) the dispatch seam used by
// RunStream. Passing nil restores normal service-backed execution.
func (m *Manager) SetStreamOverride(fn func(ctx context.Context, agentName, input string, opts []RunOption) (<-chan *Event, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamOverride = fn
}
