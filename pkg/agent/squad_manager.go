package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
)

// SquadManager handles the lifecycle, discovery, and execution routing for squad agents.
type SquadManager struct {
	store          *Store
	cfg            *config.Config
	runningAgents  map[string]context.CancelFunc // Tracks running agents if they are background loopers
	services       map[string]*Service           // Cached instantiated agent services
	mu             sync.RWMutex
	sessionMu      sync.Mutex
	memberSessions map[string]string
	queueMu        sync.Mutex
	taskQueues     map[string][]string
	sharedTasks    map[string]*SharedTask
	queueRunning   map[string]bool
	taskMu         sync.RWMutex
	asyncTasks     map[string]*AsyncTask
	sessionTasks   map[string][]string
	taskSubs       map[string]map[chan *TaskEvent]struct{}
	taskCancels    map[string]context.CancelFunc
}

// TeamManager is kept as a compatibility alias for older call sites.
type TeamManager = SquadManager

type SharedTaskStatus string

const (
	SharedTaskStatusQueued    SharedTaskStatus = "queued"
	SharedTaskStatusRunning   SharedTaskStatus = "running"
	SharedTaskStatusCompleted SharedTaskStatus = "completed"
	SharedTaskStatusFailed    SharedTaskStatus = "failed"
	defaultSquadID                             = "squad-default-001"
	defaultSquadName                           = "AgentGo Squad"
	legacyDefaultSquadName                     = "Default Squad"
	defaultSquadDescription                    = "Default AgentGo squad."
)

// SharedTaskResult captures the outcome of one delegated squad agent call.
type SharedTaskResult struct {
	AgentName string `json:"agent_name"`
	Text      string `json:"text,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SharedTask is a queued squad task owned by one squad lead agent.
type SharedTask struct {
	ID          string             `json:"id"`
	SessionID   string             `json:"session_id,omitempty"`
	SquadID     string             `json:"squad_id"`
	SquadName   string             `json:"squad_name,omitempty"`
	CaptainName string             `json:"captain_name"`
	AgentNames  []string           `json:"agent_names"`
	Prompt      string             `json:"prompt"`
	AckMessage  string             `json:"ack_message"`
	Status      SharedTaskStatus   `json:"status"`
	QueuedAhead int                `json:"queued_ahead"`
	ResultText  string             `json:"result_text,omitempty"`
	Results     []SharedTaskResult `json:"results,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	StartedAt   *time.Time         `json:"started_at,omitempty"`
	FinishedAt  *time.Time         `json:"finished_at,omitempty"`
}

// SeedDefaultMembers seeds the built-in default squad and standalone agents.
func (m *SquadManager) SeedDefaultMembers() error {
	agentName := m.getAgentName()
	squadName := m.getSquadName()

	if _, err := m.ensureDefaultSquad(); err != nil {
		return err
	}

	ctx := context.Background()
	agents, err := m.store.ListAgentModels()
	if err != nil {
		return err
	}
	for _, member := range agents {
		if strings.EqualFold(member.Name, "Researcher") || strings.EqualFold(member.Name, "FileSystemAgent") || strings.EqualFold(member.Name, "Coder") {
			if err := m.store.DeleteAgentModel(member.ID); err != nil {
				return err
			}
		}
	}

	squads, err := m.store.ListTeams()
	if err != nil {
		return err
	}
	for _, squad := range squads {
		if strings.EqualFold(strings.TrimSpace(squad.Name), "Default Team") {
			if err := m.store.DeleteTeam(squad.ID); err != nil {
				return err
			}
		}
	}

	for _, builtin := range defaultBuiltInStandaloneAgents(agentName) {
		if err := m.ensureBuiltInStandaloneAgent(ctx, builtin); err != nil {
			return err
		}
	}
	if err := m.ensureDefaultSquadCaptain(ctx, agentName, squadName); err != nil {
		return err
	}
	if err := m.ensureDefaultSquadConcierge(ctx, agentName, squadName); err != nil {
		return err
	}
	if err := m.ensureDefaultSquadSpecialists(ctx, agentName); err != nil {
		return err
	}
	return nil
}

func (m *SquadManager) ensureDefaultSquad() (*Squad, error) {
	squadName := m.getSquadName()
	squads, err := m.store.ListTeams()
	if err != nil {
		return nil, err
	}
	for _, squad := range squads {
		if squad.ID == defaultSquadID || strings.EqualFold(squad.Name, squadName) || strings.EqualFold(squad.Name, legacyDefaultSquadName) {
			updated := false
			if squad.ID != defaultSquadID {
				squad.ID = defaultSquadID
				updated = true
			}
			if !strings.EqualFold(squad.Name, squadName) {
				squad.Name = squadName
				updated = true
			}
			if strings.TrimSpace(squad.Description) == "" || strings.EqualFold(strings.TrimSpace(squad.Description), "Default workspace squad.") {
				squad.Description = fmt.Sprintf("Default %s squad.", m.getAgentName())
				updated = true
			}
			if updated {
				squad.UpdatedAt = time.Now()
				if err := m.store.SaveTeam(squad); err != nil {
					return nil, err
				}
			}
			return squad, nil
		}
	}

	squad := &Squad{
		ID:          defaultSquadID,
		Name:        squadName,
		Description: fmt.Sprintf("Default %s squad.", m.getAgentName()),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := m.store.SaveTeam(squad); err != nil {
		return nil, err
	}
	return squad, nil
}

// NewSquadManager creates a new squad manager based on a store.
func NewSquadManager(s *Store) *SquadManager {
	manager := &SquadManager{
		store:          s,
		runningAgents:  make(map[string]context.CancelFunc),
		services:       make(map[string]*Service),
		memberSessions: make(map[string]string),
		taskQueues:     make(map[string][]string),
		sharedTasks:    make(map[string]*SharedTask),
		queueRunning:   make(map[string]bool),
		asyncTasks:     make(map[string]*AsyncTask),
		sessionTasks:   make(map[string][]string),
		taskSubs:       make(map[string]map[chan *TaskEvent]struct{}),
		taskCancels:    make(map[string]context.CancelFunc),
	}
	manager.restoreSharedTasks()
	return manager
}

// NewTeamManager is kept as a compatibility alias for older call sites.
func NewTeamManager(s *Store) *TeamManager {
	return NewSquadManager(s)
}

func (m *SquadManager) SetConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}

func (m *SquadManager) GetStore() *Store {
	return m.store
}

// SetAgentName sets the global agent name used in built-in prompts and squad names.
// This overrides the agent.name field from config file or environment.
// Call before SeedDefaultMembers for the names to take effect during initialization.
func (m *SquadManager) SetAgentName(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		m.cfg = &config.Config{}
	}
	m.cfg.Agent.Name = name
}

func (m *SquadManager) configuredAgentGoConfig() *config.Config {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	if cfg != nil {
		return cfg
	}
	loaded, err := config.Load("")
	if err != nil {
		return nil
	}
	return loaded
}

func (m *SquadManager) getAgentName() string {
	if cfg := m.configuredAgentGoConfig(); cfg != nil && cfg.Agent.Name != "" {
		return cfg.Agent.Name
	}
	return "AgentGo"
}

func (m *SquadManager) getSquadName() string {
	if cfg := m.configuredAgentGoConfig(); cfg != nil {
		if cfg.Squad.Name != "" {
			return cfg.Squad.Name
		}
		if cfg.Agent.Name != "" {
			return cfg.Agent.Name + " Squad"
		}
	}
	return "AgentGo Squad"
}

// EnqueueSharedTask queues a squad task under one squad lead agent and returns an immediate acknowledgement.
func (m *SquadManager) EnqueueSharedTask(ctx context.Context, captainName string, agentNames []string, prompt string) (*SharedTask, error) {
	return m.EnqueueSharedTaskForSquad(ctx, "", captainName, agentNames, prompt)
}

// EnqueueSharedTaskForSquad queues a squad task for a specific squad and lead agent.
func (m *SquadManager) EnqueueSharedTaskForSquad(ctx context.Context, squadID, captainName string, agentNames []string, prompt string) (*SharedTask, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("message required")
	}
	squad, captain, err := m.resolveSharedTaskContext(strings.TrimSpace(squadID), strings.TrimSpace(captainName))
	if err != nil {
		return nil, err
	}

	if len(agentNames) == 0 {
		agentNames = []string{captain.Name}
	}

	for _, name := range agentNames {
		member, memberErr := m.GetMemberByNameInSquad(name, squad.ID)
		if memberErr != nil {
			return nil, fmt.Errorf("cannot load squad agent %s: %w", name, memberErr)
		}
		if member.Kind == AgentKindCaptain && !strings.EqualFold(name, captain.Name) {
			return nil, fmt.Errorf("%s is also a squad lead agent and cannot be delegated from %s", name, captain.Name)
		}
	}

	now := time.Now()
	task := &SharedTask{
		ID:          uuid.New().String(),
		SquadID:     squad.ID,
		SquadName:   squad.Name,
		CaptainName: captain.Name,
		AgentNames:  append([]string(nil), agentNames...),
		Prompt:      strings.TrimSpace(prompt),
		Status:      SharedTaskStatusQueued,
		CreatedAt:   now,
	}

	m.queueMu.Lock()
	queuedAhead := len(m.taskQueues[squad.ID])
	if m.queueRunning[squad.ID] {
		queuedAhead++
	}
	task.QueuedAhead = queuedAhead
	task.AckMessage = buildSharedTaskAck(captain.Name, queuedAhead)
	m.sharedTasks[task.ID] = task
	m.taskQueues[squad.ID] = append(m.taskQueues[squad.ID], task.ID)
	shouldStartWorker := !m.queueRunning[squad.ID]
	if shouldStartWorker {
		m.queueRunning[squad.ID] = true
	}
	m.queueMu.Unlock()

	if err := m.store.SaveSharedTask(task); err != nil {
		m.queueMu.Lock()
		delete(m.sharedTasks, task.ID)
		queue := m.taskQueues[squad.ID]
		filtered := make([]string, 0, len(queue))
		for _, id := range queue {
			if id != task.ID {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			delete(m.taskQueues, squad.ID)
			if shouldStartWorker {
				delete(m.queueRunning, squad.ID)
			}
		} else {
			m.taskQueues[squad.ID] = filtered
		}
		m.queueMu.Unlock()
		return nil, err
	}

	if shouldStartWorker {
		go m.runSharedTaskQueue(context.WithoutCancel(ctx), squad.ID)
	}

	return cloneSharedTask(task), nil
}

// ListSharedTasks returns recent queued or completed squad tasks for one captain.
func (m *SquadManager) ListSharedTasks(captainName string, since time.Time, limit int) []*SharedTask {
	return m.listSharedTasks("", captainName, since, limit)
}

// ListSharedTasksForSquad returns recent queued or completed squad tasks for one squad.
func (m *SquadManager) ListSharedTasksForSquad(squadID string, since time.Time, limit int) []*SharedTask {
	return m.listSharedTasks(squadID, "", since, limit)
}

func (m *SquadManager) listSharedTasks(squadID, captainName string, since time.Time, limit int) []*SharedTask {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()

	tasks := make([]*SharedTask, 0, len(m.sharedTasks))
	for _, task := range m.sharedTasks {
		if squadID != "" && !strings.EqualFold(task.SquadID, squadID) {
			continue
		}
		if captainName != "" && !strings.EqualFold(task.CaptainName, captainName) {
			continue
		}
		if !since.IsZero() && task.CreatedAt.Before(since) && (task.FinishedAt == nil || task.FinishedAt.Before(since)) {
			continue
		}
		tasks = append(tasks, cloneSharedTask(task))
	}

	slices.SortFunc(tasks, func(a, b *SharedTask) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	if limit > 0 && len(tasks) > limit {
		tasks = tasks[len(tasks)-limit:]
	}
	return tasks
}

func (m *SquadManager) runSharedTaskQueue(ctx context.Context, squadID string) {
	for {
		task := m.nextQueuedTask(squadID)
		if task == nil {
			return
		}
		m.executeSharedTask(ctx, task)
	}
}

func (m *SquadManager) nextQueuedTask(squadID string) *SharedTask {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()

	queue := m.taskQueues[squadID]
	if len(queue) == 0 {
		delete(m.queueRunning, squadID)
		return nil
	}

	taskID := queue[0]
	if len(queue) == 1 {
		delete(m.taskQueues, squadID)
	} else {
		m.taskQueues[squadID] = queue[1:]
	}

	task := m.sharedTasks[taskID]
	if task == nil {
		return nil
	}
	now := time.Now()
	task.Status = SharedTaskStatusRunning
	task.StartedAt = &now
	task.QueuedAhead = 0
	_ = m.store.SaveSharedTask(task)
	return cloneSharedTask(task)
}

func (m *SquadManager) executeSharedTask(ctx context.Context, task *SharedTask) {
	m.executeSharedTaskStream(ctx, task)
}

func buildSharedTaskAck(captainName string, queuedAhead int) string {
	if queuedAhead > 0 {
		return fmt.Sprintf("%s received that. It is queued behind %d task(s).", captainName, queuedAhead)
	}
	return fmt.Sprintf("%s received that. Starting it now.", captainName)
}

func (m *SquadManager) restoreSharedTasks() {
	if m == nil || m.store == nil {
		return
	}

	tasks, err := m.store.ListSharedTasksPersisted()
	if err != nil {
		return
	}

	queuedBySquad := make(map[string][]*SharedTask)
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if task.Status == SharedTaskStatusRunning {
			task.Status = SharedTaskStatusQueued
			task.StartedAt = nil
			task.QueuedAhead = 0
			_ = m.store.SaveSharedTask(task)
		}
		m.sharedTasks[task.ID] = cloneSharedTask(task)
		if task.Status == SharedTaskStatusQueued {
			queuedBySquad[task.SquadID] = append(queuedBySquad[task.SquadID], cloneSharedTask(task))
		}
		m.ensureAsyncTaskForSharedTask(task, task.SessionID, task.SquadName)
	}

	for squadID, squadTasks := range queuedBySquad {
		slices.SortFunc(squadTasks, func(a, b *SharedTask) int {
			return a.CreatedAt.Compare(b.CreatedAt)
		})
		queue := make([]string, 0, len(squadTasks))
		for idx, task := range squadTasks {
			task.QueuedAhead = idx
			if stored := m.sharedTasks[task.ID]; stored != nil {
				stored.QueuedAhead = idx
				_ = m.store.SaveSharedTask(stored)
			}
			queue = append(queue, task.ID)
		}
		if len(queue) == 0 {
			continue
		}
		m.taskQueues[squadID] = queue
		m.queueRunning[squadID] = true
		go m.runSharedTaskQueue(context.Background(), squadID)
	}
}

func cloneSharedTask(task *SharedTask) *SharedTask {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.AgentNames = append([]string(nil), task.AgentNames...)
	cloned.Results = append([]SharedTaskResult(nil), task.Results...)
	if task.StartedAt != nil {
		startedAt := *task.StartedAt
		cloned.StartedAt = &startedAt
	}
	if task.FinishedAt != nil {
		finishedAt := *task.FinishedAt
		cloned.FinishedAt = &finishedAt
	}
	return &cloned
}

// ListMembers returns all registered captains and specialists that belong to squads.
func (m *SquadManager) ListMembers() ([]*AgentModel, error) {
	all, err := m.store.ListAgentModels()
	if err != nil {
		return nil, err
	}
	members := make([]*AgentModel, 0, len(all))
	for _, model := range all {
		for _, membership := range model.Squads {
			members = append(members, cloneAgentForMembership(model, membership))
		}
	}
	return members, nil
}

// CreateMember persists a new squad agent configuration.
func (m *SquadManager) CreateMember(ctx context.Context, model *AgentModel) (*AgentModel, error) {
	if model == nil {
		return nil, fmt.Errorf("agent model is required")
	}
	squadID := strings.TrimSpace(model.TeamID)
	if squadID == "" {
		defaultSquad, err := m.ensureDefaultSquad()
		if err != nil {
			return nil, err
		}
		squadID = defaultSquad.ID
	}
	role := model.Kind
	if role == "" || role == AgentKindAgent {
		role = AgentKindSpecialist
	}
	model.TeamID = squadID
	model.Kind = role
	return m.CreateAgent(ctx, model)
}

func (m *SquadManager) CreateSquad(_ context.Context, squad *Squad) (*Squad, error) {
	if squad == nil {
		return nil, fmt.Errorf("squad is required")
	}
	if strings.TrimSpace(squad.Name) == "" {
		return nil, fmt.Errorf("squad name is required")
	}
	if strings.TrimSpace(squad.ID) == "" {
		squad.ID = uuid.New().String()
	}
	if strings.TrimSpace(squad.A2AID) == "" {
		squad.A2AID = uuid.NewString()
	}
	if strings.TrimSpace(squad.Description) == "" {
		squad.Description = squad.Name
	}
	now := time.Now()
	if squad.CreatedAt.IsZero() {
		squad.CreatedAt = now
	}
	squad.UpdatedAt = now

	if err := m.store.SaveTeam(squad); err != nil {
		return nil, err
	}

	leadAgentName := defaultSquadLeadName(squad.Name)
	if existing, err := m.store.GetAgentModelByName(leadAgentName); err == nil {
		if _, err := m.JoinSquad(context.Background(), existing.Name, squad.ID, AgentKindCaptain); err != nil {
			return nil, err
		}
	} else {
		_, err := m.CreateMember(context.Background(), &AgentModel{
			ID:           uuid.New().String(),
			TeamID:       squad.ID,
			Name:         leadAgentName,
			Kind:         AgentKindCaptain,
			Description:  fmt.Sprintf("Default captain agent for %s.", squad.Name),
			Instructions: fmt.Sprintf("You are the captain agent for squad %s. Help directly when possible and coordinate specialists when useful.", squad.Name),
			MCPTools:     defaultMemberMCPTools("Captain"),
			EnableRAG:    true,
			EnableMemory: true,
			EnableMCP:    true,
		})
		if err != nil {
			return nil, err
		}
	}

	return m.store.GetTeam(squad.ID)
}

func (m *SquadManager) ListSquads() ([]*Squad, error) {
	return m.store.ListTeams()
}

func (m *SquadManager) GetSquadByName(name string) (*Squad, error) {
	return m.store.GetTeamByName(strings.TrimSpace(name))
}

func (m *SquadManager) GetSquadByA2AID(a2aID string) (*Squad, error) {
	return m.store.GetTeamByA2AID(strings.TrimSpace(a2aID))
}

func (m *SquadManager) AddCaptain(ctx context.Context, squadID, name, description, instructions string) (*AgentModel, error) {
	return m.CreateMember(ctx, &AgentModel{
		TeamID:       strings.TrimSpace(squadID),
		Name:         strings.TrimSpace(name),
		Kind:         AgentKindCaptain,
		Description:  strings.TrimSpace(description),
		Instructions: strings.TrimSpace(instructions),
	})
}

func (m *SquadManager) AddSpecialist(ctx context.Context, squadID, name, description, instructions string) (*AgentModel, error) {
	return m.CreateMember(ctx, &AgentModel{
		TeamID:       strings.TrimSpace(squadID),
		Name:         strings.TrimSpace(name),
		Kind:         AgentKindSpecialist,
		Description:  strings.TrimSpace(description),
		Instructions: strings.TrimSpace(instructions),
	})
}

func (m *SquadManager) ListCaptains() ([]*AgentModel, error) {
	members, err := m.ListMembers()
	if err != nil {
		return nil, err
	}
	captains := make([]*AgentModel, 0, len(members))
	for _, member := range members {
		if member.Kind == AgentKindCaptain {
			captains = append(captains, member)
		}
	}
	return captains, nil
}

func (m *SquadManager) ListSpecialists() ([]*AgentModel, error) {
	members, err := m.ListMembers()
	if err != nil {
		return nil, err
	}
	specialists := make([]*AgentModel, 0, len(members))
	for _, member := range members {
		if member.Kind == AgentKindSpecialist {
			specialists = append(specialists, member)
		}
	}
	return specialists, nil
}

func (m *SquadManager) CreateTeam(ctx context.Context, team *Team) (*Team, error) {
	return m.CreateSquad(ctx, team)
}

func (m *SquadManager) ListTeams() ([]*Team, error) {
	return m.ListSquads()
}

// GetMemberByName retrieves a persisted squad agent model by name.
func (m *SquadManager) GetMemberByName(name string) (*AgentModel, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	if len(model.Squads) == 0 {
		return nil, fmt.Errorf("agent '%s' is not in a squad", model.Name)
	}
	if len(model.Squads) == 1 {
		return cloneAgentForMembership(model, model.Squads[0]), nil
	}
	for _, membership := range model.Squads {
		if membership.SquadID == defaultSquadID {
			return cloneAgentForMembership(model, membership), nil
		}
	}
	return cloneAgentForMembership(model, model.Squads[0]), nil
}

func (m *SquadManager) GetMemberByNameInSquad(name, squadID string) (*AgentModel, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	squadID = strings.TrimSpace(squadID)
	for _, membership := range model.Squads {
		if membership.SquadID == squadID {
			return cloneAgentForMembership(model, membership), nil
		}
	}
	return nil, fmt.Errorf("agent '%s' is not in squad %s", model.Name, squadID)
}

// getOrBuildService returns a cached service or builds a new one for the agent model.
func (m *SquadManager) getOrBuildService(name string) (*Service, error) {
	m.mu.RLock()
	svc, exists := m.services[name]
	m.mu.RUnlock()

	if exists {
		return svc, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if svc, exists := m.services[name]; exists {
		return svc, nil
	}

	model, err := m.store.GetAgentModelByName(name)
	if err != nil {
		return nil, err
	}

	var agentgoCfg *config.Config
	builder := New(model.Name)
	systemPrompt := strings.TrimSpace(model.Instructions)

	cfg := m.cfg
	if cfg == nil {
		var loadErr error
		cfg, loadErr = config.Load("")
		if loadErr != nil {
			cfg = nil
		}
	}
	if cfg != nil {
		agentgoCfg = cfg
		if len(model.Squads) > 0 {
			systemPrompt = m.buildTeamSystemPromptForModel(cfg, model) + "\n\n" + buildTeamMemberPrompt(model)
		} else {
			systemPrompt = buildStandaloneAgentPrompt(cfg, model)
		}
	} else {
		if len(model.Squads) > 0 {
			systemPrompt = m.buildTeamSystemPromptForModel(nil, model) + "\n\n" + buildTeamMemberPrompt(model)
		}
	}
	systemPrompt = m.decorateDelegableBuiltInAgentPrompt(systemPrompt, model)
	builder.WithSystemPrompt(systemPrompt)

	if agentgoCfg != nil {
		builder.WithConfig(agentgoCfg)

		globalPool := services.GetGlobalPoolService()
		if initErr := globalPool.Initialize(context.Background(), agentgoCfg); initErr == nil {
			if llmSvc, llmErr := globalPool.GetLLMServiceWithHint(selectionHintForAgentModel(model)); llmErr == nil {
				builder.WithLLM(llmSvc)
			}
			if embedSvc, embedErr := globalPool.GetEmbeddingService(context.Background()); embedErr == nil {
				builder.WithEmbedder(embedSvc)
			}
		}
	}

	if model.EnableRAG {
		builder.WithRAG()
	}
	if model.EnableMemory {
		storeType := ""
		if agentgoCfg != nil {
			storeType = agentgoCfg.GetMemoryStoreType().String()
		}
		if storeType != "" {
			builder.WithMemory(WithMemoryStoreType(storeType))
		} else {
			builder.WithMemory()
		}
	}
	if model.EnablePTC {
		builder.WithPTC()
	}
	if model.EnableMCP {
		builder.WithMCP()
	}

	// If the model specifies an LLM model string, this logic would require pool support to select specifically.
	// For now, relies on the default or global pool inside Build().

	if len(model.Skills) > 0 {
		builder.WithSkills()
	}

	newSvc, err := builder.Build()
	if err != nil {
		return nil, err
	}

	// Apply tool filters to the agent
	allowedMCPTools := model.MCPTools
	if len(allowedMCPTools) == 0 {
		allowedMCPTools = defaultMemberMCPTools(model.Name)
	}
	if len(allowedMCPTools) > 0 {
		newSvc.agent.SetAllowedMCPTools(allowedMCPTools)
	} else {
		newSvc.agent.SetAllowedMCPTools([]string{}) // none allowed if empty
	}

	if len(model.Skills) > 0 {
		newSvc.agent.SetAllowedSkills(model.Skills)
	} else {
		newSvc.agent.SetAllowedSkills([]string{}) // none allowed if empty
	}

	if hasMembershipRole(model.Squads, AgentKindCaptain) {
		m.RegisterCaptainTools(newSvc)
		configureCaptainService(newSvc)
	}
	m.registerBuiltInAgentDelegationTools(newSvc, model)
	if strings.EqualFold(strings.TrimSpace(model.Name), defaultOperatorAgentName) {
		registerOperatorTools(newSvc)
	}

	if label := configuredModelLabel(model); label != "" {
		newSvc.agent.SetModel(label)
	}
	newSvc.SetMemoryScope(model.Name, primaryMemorySquadID(model), "")

	m.services[name] = newSvc

	return newSvc, nil
}

func primaryMemorySquadID(model *AgentModel) string {
	if model == nil {
		return ""
	}
	if teamID := strings.TrimSpace(model.TeamID); teamID != "" {
		return teamID
	}
	for _, membership := range model.Squads {
		if squadID := strings.TrimSpace(membership.SquadID); squadID != "" {
			return squadID
		}
	}
	return ""
}

func (m *SquadManager) buildTeamSystemPromptForModel(cfg *config.Config, model *AgentModel) string {
	base := buildTeamSystemPrompt(cfg, model)
	if model == nil || !hasMembershipRole(model.Squads, AgentKindCaptain) {
		return base
	}

	roster := strings.TrimSpace(m.buildCaptainRosterContext(model))
	if roster == "" {
		return base
	}
	return strings.TrimSpace(base + "\n\n" + roster)
}

func (m *SquadManager) decorateDelegableBuiltInAgentPrompt(base string, model *AgentModel) string {
	base = strings.TrimSpace(base)
	if model == nil || isBuiltInAgentModel(model) {
		return base
	}
	context := strings.TrimSpace(m.buildDelegableBuiltInAgentsContext(model))
	if context == "" {
		return base
	}
	if base == "" {
		return context
	}
	return strings.TrimSpace(base + "\n\n" + context)
}

func buildTeamSystemPrompt(cfg *config.Config, model *AgentModel) string {
	agentName := "AgentGo"
	squadName := "AgentGo Squad"
	if cfg != nil && cfg.Agent.Name != "" {
		agentName = cfg.Agent.Name
		squadName = agentName + " Squad"
	}

	workspace := ""
	if cfg != nil {
		workspace = strings.TrimSpace(cfg.WorkspaceDir())
	}
	projectRoot := ""
	if cwd, err := os.Getwd(); err == nil {
		projectRoot = strings.TrimSpace(cwd)
	}

	lines := []string{
		fmt.Sprintf("You are working as part of a %s.", squadName),
		"The squad shares one workspace and one project context.",
	}
	lines = append(lines, buildRuntimeContextLines()...)
	if workspace != "" {
		lines = append(lines, "- Shared writable workspace: "+workspace)
	}
	if projectRoot != "" {
		lines = append(lines, "- Active project root: "+projectRoot)
		lines = append(lines, "- Stay inside the active project root unless the user explicitly asks for another location.")
	}
	if model != nil && hasMembershipRole(model.Squads, AgentKindCaptain) {
		lines = append(lines,
			"- You are the captain for this squad.",
			"- Handle direct user requests when possible and delegate specialist work only when that improves the result.",
			"- Prefer assigning multi-step or implementation-heavy work to named squad members via async team tasks instead of doing it yourself.",
			"- Use async team task submission first for coding, file-writing, research, and verification work. Only use synchronous delegation when you truly need an immediate inline sub-result.",
			"- Do not use generic sub-agent delegation; coordinate through the squad members listed below.",
		)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (m *SquadManager) buildCaptainRosterContext(model *AgentModel) string {
	if model == nil || !hasMembershipRole(model.Squads, AgentKindCaptain) {
		return ""
	}

	lines := []string{
		"Captain responsibilities and squad roster:",
		"- Your role: captain / team lead.",
	}
	if desc := strings.TrimSpace(model.Description); desc != "" {
		lines = append(lines, "- Your responsibility summary: "+desc)
	}
	if instr := strings.TrimSpace(model.Instructions); instr != "" {
		lines = append(lines, "- Your operating responsibilities: "+singleLinePromptText(instr))
	}

	for _, membership := range model.Squads {
		if membership.Role != AgentKindCaptain {
			continue
		}
		squadLabel := strings.TrimSpace(membership.SquadName)
		if squadLabel == "" {
			squadLabel = strings.TrimSpace(membership.SquadID)
		}
		if squadLabel == "" {
			squadLabel = "current squad"
		}

		lines = append(lines, "- Squad: "+squadLabel)
		members, err := m.ListSquadAgentsForSquad(membership.SquadID)
		if err != nil {
			lines = append(lines, "  - Unable to load squad members: "+err.Error())
			continue
		}
		slices.SortFunc(members, compareAgentModelsForRoster)
		for _, member := range members {
			if member == nil || strings.TrimSpace(member.Name) == "" || strings.EqualFold(member.Name, model.Name) {
				continue
			}
			line := fmt.Sprintf("  - %s [%s]", member.Name, strings.ToLower(string(member.Kind)))
			if desc := strings.TrimSpace(member.Description); desc != "" {
				line += ": " + desc
			}
			if instr := strings.TrimSpace(member.Instructions); instr != "" {
				line += " Responsibilities: " + singleLinePromptText(instr)
			}
			if len(member.Skills) > 0 {
				line += " Skills: " + strings.Join(member.Skills, ", ")
			}
			lines = append(lines, line)
		}
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func compareAgentModelsForRoster(a, b *AgentModel) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return 1
		default:
			return -1
		}
	}
	if a.Kind != b.Kind {
		if a.Kind == AgentKindCaptain {
			return -1
		}
		if b.Kind == AgentKindCaptain {
			return 1
		}
	}
	switch {
	case a.Name < b.Name:
		return -1
	case a.Name > b.Name:
		return 1
	default:
		return 0
	}
}

func singleLinePromptText(text string) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if len(text) > 240 {
		return text[:237] + "..."
	}
	return text
}

func buildTeamMemberPrompt(model *AgentModel) string {
	if model == nil {
		return ""
	}

	base := strings.TrimSpace(model.Instructions)
	extras := make([]string, 0, 8)

	switch strings.ToLower(strings.TrimSpace(model.Name)) {
	case "coder":
		extras = append(extras,
			"- Use only the tools and capabilities that are actually exposed in this runtime. Do not invent helper tools that are not present in the visible tool list.",
			"- If the task says create/write/save/update a file, you MUST call a filesystem write or modify tool and actually change the file.",
			"- Do not stop after listing files when the task clearly asks you to write or edit a file.",
			"- Directory listing is only for confirmation or discovery. It is not a valid final action for a file-writing task.",
			"- If a multi-file read result is incomplete, fall back to individual read_file calls before continuing.",
			"- After writing a file, briefly state which file was changed and what was written.",
			"- Return the exact file path(s) you changed.",
		)
	default:
		extras = append(extras,
			"- Use only the tools and capabilities that are actually exposed in the current runtime. Do not invent helper tools that are not present in the visible tool list.",
			"- For repository or filesystem questions, prefer targeted file reads over broad directory traversal.",
			"- If the task already names specific files such as Makefile, package.json, App.tsx, or main.go, read those files first before calling list_directory.",
			"- If a multi-file read result is incomplete, fall back to individual read_file calls before continuing.",
			"- Never inspect blacklisted repository paths unless the user explicitly asks for them. Blacklist: "+FormatRepositoryIgnoreList(),
			"- Avoid full repository tree scans. Use directory listing only when you need quick structure confirmation, and do it on one narrow path at a time.",
			"- Delegate specialized implementation work to specialists instead of carrying their detailed operating rules yourself.",
		)
	}

	if len(extras) == 0 {
		return base
	}
	return strings.TrimSpace(base + "\n\n" + strings.Join(extras, "\n"))
}

func buildTeamTaskEnvelope(cfg *config.Config, agentName, instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return ""
	}

	lines := []string{
		"Squad task context:",
		"- Target squad agent: " + strings.TrimSpace(agentName),
	}
	lines = append(lines, buildRuntimeContextLines()...)
	if cfg != nil {
		if workspace := strings.TrimSpace(cfg.WorkspaceDir()); workspace != "" {
			lines = append(lines, "- Shared writable workspace: "+workspace)
		}
	}
	if projectRoot, err := os.Getwd(); err == nil && strings.TrimSpace(projectRoot) != "" {
		lines = append(lines, "- Active project root: "+strings.TrimSpace(projectRoot))
		lines = append(lines, "- Stay inside the active project root unless the user explicitly asks for another location.")
	}
	lines = append(lines,
		"- The bullets above are context, not the requested action.",
		"- Execute only the work described in the Task section below.",
		"- Keep your response focused on your own role.",
		"- Ignore blacklisted repository paths unless the user explicitly asks for them: "+FormatRepositoryIgnoreList(),
		"",
		"Task:",
		instruction,
	)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func buildRuntimeContextLines() []string {
	lines := []string{
		"- Runtime OS/arch: " + runtime.GOOS + "/" + runtime.GOARCH,
	}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		lines = append(lines, "- Interactive shell: "+shell)
		lines = append(lines, "- If you provide shell commands or scripts, prefer compatibility with this shell when practical.")
	}
	return lines
}

func defaultMemberMCPTools(name string) []string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "assistant", "captain", "stakeholder":
		return []string{
			"mcp_filesystem_list_allowed_directories",
			"mcp_filesystem_list_directory",
			"mcp_filesystem_read_file",
			"mcp_filesystem_read_multiple_files",
			"mcp_filesystem_search_within_files",
			"mcp_filesystem_get_file_info",
			"mcp_filesystem_create_directory",
			"mcp_filesystem_write_file",
			"mcp_filesystem_modify_file",
			"mcp_filesystem_move_file",
			"mcp_filesystem_copy_file",
			"mcp_filesystem_delete_file",
			"mcp_websearch_websearch_ai_summary",
			"mcp_websearch_fetch_page_content",
			"mcp_websearch_deep_read_page",
		}
	case "coder":
		return []string{
			"mcp_filesystem_list_allowed_directories",
			"mcp_filesystem_list_directory",
			"mcp_filesystem_read_file",
			"mcp_filesystem_read_multiple_files",
			"mcp_filesystem_search_within_files",
			"mcp_filesystem_get_file_info",
			"mcp_filesystem_create_directory",
			"mcp_filesystem_write_file",
			"mcp_filesystem_modify_file",
			"mcp_filesystem_move_file",
			"mcp_filesystem_copy_file",
			"mcp_filesystem_delete_file",
		}
	default:
		return []string{
			"mcp_filesystem_list_allowed_directories",
			"mcp_filesystem_list_directory",
			"mcp_filesystem_read_file",
			"mcp_filesystem_read_multiple_files",
			"mcp_filesystem_search_within_files",
			"mcp_filesystem_get_file_info",
			"mcp_filesystem_create_directory",
			"mcp_filesystem_write_file",
			"mcp_filesystem_modify_file",
			"mcp_filesystem_move_file",
			"mcp_filesystem_copy_file",
			"mcp_filesystem_delete_file",
			"mcp_websearch_websearch_ai_summary",
			"mcp_websearch_fetch_page_content",
			"mcp_websearch_deep_read_page",
		}
	}
}

func (m *SquadManager) ensureAgentRunning(ctx context.Context, name string) error {
	_, err := m.store.GetAgentModelByName(name)
	return err
}

func extractDispatchText(res *ExecutionResult) string {
	if res == nil {
		return ""
	}

	if res.PTCResult != nil && res.PTCResult.Type != PTCResultTypeText {
		text := strings.TrimSpace(res.PTCResult.FormatForLLM())
		if isMeaningfulDispatchText(text) {
			return text
		}
	}

	textCandidates := []string{
		res.Text(),
	}

	if s, ok := res.Metadata["dispatch_result"].(string); ok {
		textCandidates = append(textCandidates, s)
	}
	if s, ok := res.Metadata["final_text"].(string); ok {
		textCandidates = append(textCandidates, s)
	}

	for _, candidate := range textCandidates {
		candidate = sanitizeDispatchText(candidate)
		if isMeaningfulDispatchText(candidate) {
			return candidate
		}
	}

	if res.FinalResult != nil {
		if bz, err := json.Marshal(res.FinalResult); err == nil {
			candidate := strings.TrimSpace(string(bz))
			if candidate != "" && candidate != "null" {
				return candidate
			}
		}
	}

	for _, candidate := range textCandidates {
		candidate = sanitizeDispatchText(candidate)
		if candidate != "" {
			return candidate
		}
	}

	return ""
}

var thinkBlockRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

func sanitizeDispatchText(text string) string {
	text = thinkBlockRe.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func isMeaningfulDispatchText(text string) bool {
	if text == "" {
		return false
	}

	normalized := strings.ToLower(strings.TrimSpace(text))
	generic := map[string]struct{}{
		"task complete":                {},
		"the task has been completed.": {},
		"the task has been completed":  {},
		"the task has been completed. the information has been saved to memory.": {},
		"the information has been saved to memory.":                              {},
		"done": {},
	}

	_, isGeneric := generic[normalized]
	return !isGeneric
}

// DispatchTask runs the task on the target squad agent service directly.
func (m *SquadManager) DispatchTask(ctx context.Context, agentName string, instruction string) (string, error) {
	return m.dispatchTask(ctx, agentName, instruction, "")
}

// ChatWithMember runs a squad chat turn with persistent history scoped to one conversation key and squad agent.
func (m *SquadManager) ChatWithMember(ctx context.Context, conversationKey, agentName string, instruction string) (string, error) {
	conversationKey = strings.TrimSpace(conversationKey)
	if conversationKey == "" {
		return m.DispatchTask(ctx, agentName, instruction)
	}
	return m.dispatchTask(ctx, agentName, instruction, m.conversationSessionID(conversationKey, agentName))
}

func (m *SquadManager) dispatchTask(ctx context.Context, agentName string, instruction string, sessionID string) (string, error) {
	if err := m.ensureAgentRunning(ctx, agentName); err != nil {
		return "", fmt.Errorf("cannot start agent %s: %w", agentName, err)
	}

	svc, err := m.getOrBuildService(agentName)
	if err != nil {
		return "", fmt.Errorf("cannot dispatch to agent %s: %w", agentName, err)
	}

	wrappedInstruction := instruction
	if cfg := m.configuredAgentGoConfig(); cfg != nil {
		wrappedInstruction = buildTeamTaskEnvelope(cfg, agentName, instruction)
	}

	runOptions := dispatchRunOptions(agentName)
	if strings.TrimSpace(sessionID) != "" {
		runOptions = append(runOptions, WithSessionID(sessionID))
	}

	res, err := svc.Run(ctx, wrappedInstruction, runOptions...)
	if err != nil {
		return "", err
	}

	if text := extractDispatchText(res); text != "" {
		return text, nil
	}

	bz, _ := json.Marshal(res.FinalResult)
	return string(bz), nil
}

func (m *SquadManager) conversationSessionID(conversationKey, agentName string) string {
	key := strings.ToLower(strings.TrimSpace(conversationKey)) + "::" + strings.ToLower(strings.TrimSpace(agentName))

	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()

	if sessionID, ok := m.memberSessions[key]; ok && strings.TrimSpace(sessionID) != "" {
		return sessionID
	}

	sessionID := uuid.NewString()
	m.memberSessions[key] = sessionID
	return sessionID
}

// DispatchTaskStream runs the task on the target agent and returns the raw event stream.
func (m *SquadManager) DispatchTaskStream(ctx context.Context, agentName string, instruction string) (<-chan *Event, error) {
	return m.dispatchTaskStream(ctx, agentName, instruction, "", nil)
}

// ChatWithMemberStream runs a squad chat turn with persistent history and returns the raw event stream.
func (m *SquadManager) ChatWithMemberStream(ctx context.Context, conversationKey, agentName, instruction string) (<-chan *Event, error) {
	conversationKey = strings.TrimSpace(conversationKey)
	if conversationKey == "" {
		return m.DispatchTaskStream(ctx, agentName, instruction)
	}
	return m.dispatchTaskStream(ctx, agentName, instruction, m.conversationSessionID(conversationKey, agentName), nil)
}

// DispatchTaskStreamWithOptions runs the task and returns the raw event stream with explicit run options.
func (m *SquadManager) DispatchTaskStreamWithOptions(ctx context.Context, agentName string, instruction string, opts ...RunOption) (<-chan *Event, error) {
	return m.dispatchTaskStream(ctx, agentName, instruction, "", opts)
}

// ChatWithMemberStreamWithOptions runs a squad chat turn with persistent history and explicit run options.
func (m *SquadManager) ChatWithMemberStreamWithOptions(ctx context.Context, conversationKey, agentName, instruction string, opts ...RunOption) (<-chan *Event, error) {
	conversationKey = strings.TrimSpace(conversationKey)
	if conversationKey == "" {
		return m.DispatchTaskStreamWithOptions(ctx, agentName, instruction, opts...)
	}
	return m.dispatchTaskStream(ctx, agentName, instruction, m.conversationSessionID(conversationKey, agentName), opts)
}

func (m *SquadManager) dispatchTaskStream(ctx context.Context, agentName string, instruction string, sessionID string, extraOpts []RunOption) (<-chan *Event, error) {
	if err := m.ensureAgentRunning(ctx, agentName); err != nil {
		return nil, fmt.Errorf("cannot start agent %s: %w", agentName, err)
	}

	svc, err := m.getOrBuildService(agentName)
	if err != nil {
		return nil, fmt.Errorf("cannot dispatch to agent %s: %w", agentName, err)
	}

	wrappedInstruction := instruction
	if cfg := m.configuredAgentGoConfig(); cfg != nil {
		wrappedInstruction = buildTeamTaskEnvelope(cfg, agentName, instruction)
	}

	runOptions := dispatchRunOptions(agentName)
	if strings.TrimSpace(sessionID) != "" {
		runOptions = append(runOptions, WithSessionID(sessionID))
	}
	runOptions = append(runOptions, extraOpts...)

	return svc.RunStreamWithOptions(ctx, wrappedInstruction, runOptions...)
}

func dispatchRunOptions(agentName string) []RunOption {
	name := strings.ToLower(strings.TrimSpace(agentName))
	switch name {
	case "coder":
		return []RunOption{WithMaxTurns(10), WithTemperature(0.1)}
	default:
		return []RunOption{WithMaxTurns(14), WithTemperature(0.1)}
	}
}

// RegisterCaptainTools adds the squad management tools to the frontdesk lead agent.
func (m *SquadManager) RegisterCaptainTools(captain *Service) {
	if captain == nil {
		return
	}

	register := func(name, description string, parameters map[string]interface{}, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
		if captain.toolRegistry != nil && captain.toolRegistry.Has(name) {
			return
		}
		captain.AddTool(name, description, parameters, handler)
	}

	// 1. discover_agents
	discoverDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "discover_agents",
			Description: "Discover all available specialized agents in the system and their descriptions.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
	register(discoverDef.Function.Name, discoverDef.Function.Description, discoverDef.Function.Parameters, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agents, err := m.ListMembers()
		if err != nil {
			return nil, err
		}
		var result []map[string]interface{}
		for _, a := range agents {
			result = append(result, map[string]interface{}{
				"name":        a.Name,
				"description": a.Description,
			})
		}
		return result, nil
	})

	// 2. submit_team_async
	submitAsyncDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "submit_team_async",
			Description: "Queue async work for one or more named squad members and return immediately with a task id. Prefer this for implementation, research, and verification work.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_names": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "The squad member names that should handle the async work.",
					},
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "The task prompt to queue.",
					},
				},
				"required": []string{"agent_names", "prompt"},
			},
		},
	}
	register(submitAsyncDef.Function.Name, submitAsyncDef.Function.Description, submitAsyncDef.Function.Parameters, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		lead, squad, err := m.resolveCaptainServiceContext(captain)
		if err != nil {
			return nil, err
		}
		prompt := getStringArg(args, "prompt")
		if prompt == "" {
			return nil, fmt.Errorf("prompt is required")
		}
		agentNames := getStringSliceArg(args, "agent_names")
		if len(agentNames) == 0 {
			return nil, fmt.Errorf("agent_names is required")
		}

		task, err := m.SubmitSquadTask(ctx, captain.CurrentSessionID(), squad.ID, prompt, agentNames)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"task_id":      task.ID,
			"squad_id":     task.SquadID,
			"squad_name":   squad.Name,
			"captain_name": lead.Name,
			"agent_names":  append([]string(nil), task.AgentNames...),
			"ack_message":  task.AckMessage,
			"status":       task.Status,
		}, nil
	})

	// 3. get_task_status
	getTaskDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "get_task_status",
			Description: "Get the status of one async team task by id.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "The async team task id.",
					},
				},
				"required": []string{"task_id"},
			},
		},
	}
	register(getTaskDef.Function.Name, getTaskDef.Function.Description, getTaskDef.Function.Parameters, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		taskID := getStringArg(args, "task_id")
		if taskID == "" {
			return nil, fmt.Errorf("task_id is required")
		}
		return m.GetTask(taskID)
	})

	// 4. list_team_tasks
	listTasksDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "list_team_tasks",
			Description: "List recent async tasks for the captain's current squad.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"limit": map[string]interface{}{
						"type":        "number",
						"description": "Optional maximum number of tasks to return.",
					},
				},
			},
		},
	}
	register(listTasksDef.Function.Name, listTasksDef.Function.Description, listTasksDef.Function.Parameters, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		_, squad, err := m.resolveCaptainServiceContext(captain)
		if err != nil {
			return nil, err
		}
		limit := getIntArg(args, "limit", 10)
		tasks := m.ListSharedTasksForSquad(squad.ID, time.Time{}, limit)
		out := make([]map[string]interface{}, 0, len(tasks))
		for _, task := range tasks {
			out = append(out, map[string]interface{}{
				"task_id":      task.ID,
				"captain_name": task.CaptainName,
				"agent_names":  append([]string(nil), task.AgentNames...),
				"prompt":       task.Prompt,
				"status":       task.Status,
				"ack_message":  task.AckMessage,
				"result_text":  task.ResultText,
				"created_at":   task.CreatedAt,
				"started_at":   task.StartedAt,
				"finished_at":  task.FinishedAt,
			})
		}
		return out, nil
	})

	// 5. delegate_task
	delegateDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "delegate_task",
			Description: "Synchronously delegate a short task to one squad agent and wait for the inline result. Prefer submit_team_async for longer implementation work.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_name": map[string]interface{}{
						"type":        "string",
						"description": "The name of the target squad agent.",
					},
					"instruction": map[string]interface{}{
						"type":        "string",
						"description": "The full prompt/instruction for the task.",
					},
				},
				"required": []string{"agent_name", "instruction"},
			},
		},
	}
	register(delegateDef.Function.Name, delegateDef.Function.Description, delegateDef.Function.Parameters, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName, _ := args["agent_name"].(string)
		instruction, _ := args["instruction"].(string)
		return m.DispatchTask(ctx, agentName, instruction)
	})
}

// RegisterCommanderTools is kept as a compatibility alias for older call sites.
func (m *SquadManager) RegisterCommanderTools(commander *Service) {
	m.RegisterCaptainTools(commander)
}

func (m *SquadManager) resolveCaptainServiceContext(captain *Service) (*AgentModel, *Squad, error) {
	if captain == nil || captain.agent == nil {
		return nil, nil, fmt.Errorf("captain service is not initialized")
	}
	member, err := m.GetMemberByName(captain.agent.Name())
	if err != nil {
		return nil, nil, err
	}
	for _, membership := range member.Squads {
		if membership.Role == AgentKindCaptain {
			squad, squadErr := m.store.GetTeam(membership.SquadID)
			if squadErr != nil {
				return nil, nil, squadErr
			}
			return member, squad, nil
		}
	}
	return nil, nil, fmt.Errorf("%s is not a squad captain", captain.agent.Name())
}
