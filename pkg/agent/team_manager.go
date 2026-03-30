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

// TeamManager handles the lifecycle, discovery, and execution routing for team agents.
type TeamManager struct {
	store                         *Store
	cfg                           *config.Config
	runningAgents                 map[string]context.CancelFunc // Tracks running agents if they are background loopers
	services                      map[string]*Service           // Cached instantiated agent services
	mu                            sync.RWMutex
	runtimeMu                     sync.RWMutex
	sessionMu                     sync.Mutex
	memberSessions                map[string]string
	queueMu                       sync.Mutex
	taskQueues                    map[string][]string
	sharedTasks                   map[string]*SharedTask
	queueRunning                  map[string]bool
	taskMu                        sync.RWMutex
	asyncTasks                    map[string]*AsyncTask
	sessionTasks                  map[string][]string
	taskSubs                      map[string]map[chan *TaskEvent]struct{}
	taskCancels                   map[string]context.CancelFunc
	teamGatewayMu                 sync.RWMutex
	teamRequests                  map[string]*TeamRequest
	mailboxMu                     sync.RWMutex
	agentMailboxes                map[string]*agentMailbox
	builtInRuntimes               map[string]*builtInAgentRuntime
	builtInDispatchOverride       builtInRuntimeDispatchFunc
	builtInStreamDispatchOverride builtInRuntimeStreamDispatchFunc
}

type SharedTaskStatus string

const (
	SharedTaskStatusQueued    SharedTaskStatus = "queued"
	SharedTaskStatusRunning   SharedTaskStatus = "running"
	SharedTaskStatusCompleted SharedTaskStatus = "completed"
	SharedTaskStatusFailed    SharedTaskStatus = "failed"
	defaultTeamID                              = "team-default-001"
	defaultTeamName                            = "AgentGo Team"
	legacyDefaultTeamName                      = "Default Team"
	defaultTeamDescription                     = "Default AgentGo team."
)

// SharedTaskResult captures the outcome of one delegated team agent call.
type SharedTaskResult struct {
	AgentName string `json:"agent_name"`
	Text      string `json:"text,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SharedTask is a queued team task owned by one team lead agent.
type SharedTask struct {
	ID          string             `json:"id"`
	SessionID   string             `json:"session_id,omitempty"`
	TeamID      string             `json:"team_id"`
	TeamName    string             `json:"team_name,omitempty"`
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

// SeedDefaultMembers seeds the built-in default team and standalone agents.
func (m *TeamManager) SeedDefaultMembers() error {
	agentName := m.getAgentName()
	teamName := m.getTeamName()

	if _, err := m.ensureDefaultTeam(); err != nil {
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

	teams, err := m.store.ListTeams()
	if err != nil {
		return err
	}
	for _, team := range teams {
		if strings.EqualFold(strings.TrimSpace(team.Name), "Default Team") {
			if err := m.store.DeleteTeam(team.ID); err != nil {
				return err
			}
		}
	}

	for _, builtin := range defaultBuiltInStandaloneAgents(agentName) {
		if err := m.ensureBuiltInStandaloneAgent(ctx, builtin); err != nil {
			return err
		}
	}
	if err := m.ensureDefaultTeamCaptain(ctx, agentName, teamName); err != nil {
		return err
	}
	if err := m.ensureDefaultTeamConcierge(ctx, agentName, teamName); err != nil {
		return err
	}
	if err := m.ensureDefaultTeamSpecialists(ctx, agentName); err != nil {
		return err
	}
	return m.startBuiltInRuntimes(ctx)
}

func (m *TeamManager) ensureDefaultTeam() (*Team, error) {
	teamName := m.getTeamName()
	teams, err := m.store.ListTeams()
	if err != nil {
		return nil, err
	}
	for _, team := range teams {
		if team.ID == defaultTeamID || strings.EqualFold(team.Name, teamName) || strings.EqualFold(team.Name, legacyDefaultTeamName) {
			updated := false
			if team.ID != defaultTeamID {
				team.ID = defaultTeamID
				updated = true
			}
			if !strings.EqualFold(team.Name, teamName) {
				team.Name = teamName
				updated = true
			}
			if strings.TrimSpace(team.Description) == "" || strings.EqualFold(strings.TrimSpace(team.Description), "Default workspace team.") {
				team.Description = fmt.Sprintf("Default %s team.", m.getAgentName())
				updated = true
			}
			if updated {
				team.UpdatedAt = time.Now()
				if err := m.store.SaveTeam(team); err != nil {
					return nil, err
				}
			}
			return team, nil
		}
	}

	team := &Team{
		ID:          defaultTeamID,
		Name:        teamName,
		Description: fmt.Sprintf("Default %s team.", m.getAgentName()),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := m.store.SaveTeam(team); err != nil {
		return nil, err
	}
	return team, nil
}

// NewTeamManager creates a new team manager based on a store.
func NewTeamManager(s *Store) *TeamManager {
	manager := &TeamManager{
		store:           s,
		runningAgents:   make(map[string]context.CancelFunc),
		services:        make(map[string]*Service),
		memberSessions:  make(map[string]string),
		taskQueues:      make(map[string][]string),
		sharedTasks:     make(map[string]*SharedTask),
		queueRunning:    make(map[string]bool),
		asyncTasks:      make(map[string]*AsyncTask),
		sessionTasks:    make(map[string][]string),
		taskSubs:        make(map[string]map[chan *TaskEvent]struct{}),
		taskCancels:     make(map[string]context.CancelFunc),
		teamRequests:    make(map[string]*TeamRequest),
		agentMailboxes:  make(map[string]*agentMailbox),
		builtInRuntimes: make(map[string]*builtInAgentRuntime),
	}
	manager.restoreSharedTasks()
	return manager
}

func (m *TeamManager) SetConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}

func (m *TeamManager) GetStore() *Store {
	return m.store
}

// SetAgentName sets the global agent name used in built-in prompts and team names.
// This overrides the agent.name field from runtime config or environment.
// Call before SeedDefaultMembers for the names to take effect during initialization.
func (m *TeamManager) SetAgentName(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		m.cfg = &config.Config{}
	}
	m.cfg.Agent.Name = name
}

func (m *TeamManager) configuredAgentGoConfig() *config.Config {
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

func (m *TeamManager) getAgentName() string {
	if cfg := m.configuredAgentGoConfig(); cfg != nil && cfg.Agent.Name != "" {
		return cfg.Agent.Name
	}
	return "AgentGo"
}

func (m *TeamManager) getTeamName() string {
	if cfg := m.configuredAgentGoConfig(); cfg != nil {
		if cfg.Team.Name != "" {
			return cfg.Team.Name
		}
		if cfg.Agent.Name != "" {
			return cfg.Agent.Name + " Team"
		}
	}
	return "AgentGo Team"
}

// EnqueueSharedTask queues a team task under one team lead agent and returns an immediate acknowledgement.
func (m *TeamManager) EnqueueSharedTask(ctx context.Context, captainName string, agentNames []string, prompt string) (*SharedTask, error) {
	return m.EnqueueSharedTaskForTeam(ctx, "", captainName, agentNames, prompt)
}

// EnqueueSharedTaskForTeam queues a team task for a specific team and lead agent.
func (m *TeamManager) EnqueueSharedTaskForTeam(ctx context.Context, teamID, captainName string, agentNames []string, prompt string) (*SharedTask, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("message required")
	}
	team, captain, err := m.resolveSharedTaskContext(strings.TrimSpace(teamID), strings.TrimSpace(captainName))
	if err != nil {
		return nil, err
	}

	if len(agentNames) == 0 {
		agentNames = []string{captain.Name}
	}

	for _, name := range agentNames {
		member, memberErr := m.GetMemberByNameInTeam(name, team.ID)
		if memberErr != nil {
			return nil, fmt.Errorf("cannot load team agent %s: %w", name, memberErr)
		}
		if member.Kind == AgentKindCaptain && !strings.EqualFold(name, captain.Name) {
			return nil, fmt.Errorf("%s is also a team lead agent and cannot be delegated from %s", name, captain.Name)
		}
	}

	now := time.Now()
	task := &SharedTask{
		ID:          uuid.New().String(),
		TeamID:      team.ID,
		TeamName:    team.Name,
		CaptainName: captain.Name,
		AgentNames:  append([]string(nil), agentNames...),
		Prompt:      strings.TrimSpace(prompt),
		Status:      SharedTaskStatusQueued,
		CreatedAt:   now,
	}

	m.queueMu.Lock()
	queuedAhead := len(m.taskQueues[team.ID])
	if m.queueRunning[team.ID] {
		queuedAhead++
	}
	task.QueuedAhead = queuedAhead
	task.AckMessage = buildSharedTaskAck(captain.Name, queuedAhead)
	m.sharedTasks[task.ID] = task
	m.taskQueues[team.ID] = append(m.taskQueues[team.ID], task.ID)
	shouldStartWorker := !m.queueRunning[team.ID]
	if shouldStartWorker {
		m.queueRunning[team.ID] = true
	}
	m.queueMu.Unlock()

	if err := m.store.SaveSharedTask(task); err != nil {
		m.queueMu.Lock()
		delete(m.sharedTasks, task.ID)
		queue := m.taskQueues[team.ID]
		filtered := make([]string, 0, len(queue))
		for _, id := range queue {
			if id != task.ID {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			delete(m.taskQueues, team.ID)
			if shouldStartWorker {
				delete(m.queueRunning, team.ID)
			}
		} else {
			m.taskQueues[team.ID] = filtered
		}
		m.queueMu.Unlock()
		return nil, err
	}

	if shouldStartWorker {
		go m.runSharedTaskQueue(context.WithoutCancel(ctx), team.ID)
	}

	return cloneSharedTask(task), nil
}

// ListSharedTasks returns recent queued or completed team tasks for one captain.
func (m *TeamManager) ListSharedTasks(captainName string, since time.Time, limit int) []*SharedTask {
	return m.listSharedTasks("", captainName, since, limit)
}

// ListSharedTasksForTeam returns recent queued or completed team tasks for one team.
func (m *TeamManager) ListSharedTasksForTeam(teamID string, since time.Time, limit int) []*SharedTask {
	return m.listSharedTasks(teamID, "", since, limit)
}

func (m *TeamManager) listSharedTasks(teamID, captainName string, since time.Time, limit int) []*SharedTask {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()

	tasks := make([]*SharedTask, 0, len(m.sharedTasks))
	for _, task := range m.sharedTasks {
		if teamID != "" && !strings.EqualFold(task.TeamID, teamID) {
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

func (m *TeamManager) runSharedTaskQueue(ctx context.Context, teamID string) {
	for {
		task := m.nextQueuedTask(teamID)
		if task == nil {
			return
		}
		m.executeSharedTask(ctx, task)
	}
}

func (m *TeamManager) nextQueuedTask(teamID string) *SharedTask {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()

	queue := m.taskQueues[teamID]
	if len(queue) == 0 {
		delete(m.queueRunning, teamID)
		return nil
	}

	taskID := queue[0]
	if len(queue) == 1 {
		delete(m.taskQueues, teamID)
	} else {
		m.taskQueues[teamID] = queue[1:]
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

func (m *TeamManager) executeSharedTask(ctx context.Context, task *SharedTask) {
	m.executeSharedTaskStream(ctx, task)
}

func buildSharedTaskAck(captainName string, queuedAhead int) string {
	if queuedAhead > 0 {
		return fmt.Sprintf("%s received that. It is queued behind %d task(s).", captainName, queuedAhead)
	}
	return fmt.Sprintf("%s received that. Starting it now.", captainName)
}

func (m *TeamManager) restoreSharedTasks() {
	if m == nil || m.store == nil {
		return
	}

	tasks, err := m.store.ListSharedTasksPersisted()
	if err != nil {
		return
	}

	queuedByTeam := make(map[string][]*SharedTask)
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
			queuedByTeam[task.TeamID] = append(queuedByTeam[task.TeamID], cloneSharedTask(task))
		}
		m.ensureAsyncTaskForSharedTask(task, task.SessionID, task.TeamName)
	}

	for teamID, teamTasks := range queuedByTeam {
		slices.SortFunc(teamTasks, func(a, b *SharedTask) int {
			return a.CreatedAt.Compare(b.CreatedAt)
		})
		queue := make([]string, 0, len(teamTasks))
		for idx, task := range teamTasks {
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
		m.taskQueues[teamID] = queue
		m.queueRunning[teamID] = true
		go m.runSharedTaskQueue(context.Background(), teamID)
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

// ListMembers returns all registered captains and specialists that belong to teams.
func (m *TeamManager) ListMembers() ([]*AgentModel, error) {
	all, err := m.store.ListAgentModels()
	if err != nil {
		return nil, err
	}
	members := make([]*AgentModel, 0, len(all))
	for _, model := range all {
		for _, membership := range model.Teams {
			members = append(members, cloneAgentForMembership(model, membership))
		}
	}
	return members, nil
}

// CreateMember persists a new team agent configuration.
func (m *TeamManager) CreateMember(ctx context.Context, model *AgentModel) (*AgentModel, error) {
	if model == nil {
		return nil, fmt.Errorf("agent model is required")
	}
	teamID := strings.TrimSpace(model.TeamID)
	if teamID == "" {
		defaultTeam, err := m.ensureDefaultTeam()
		if err != nil {
			return nil, err
		}
		teamID = defaultTeam.ID
	}
	role := model.Kind
	if role == "" || role == AgentKindAgent {
		role = AgentKindSpecialist
	}
	model.TeamID = teamID
	model.Kind = role
	return m.CreateAgent(ctx, model)
}

func (m *TeamManager) CreateTeam(_ context.Context, team *Team) (*Team, error) {
	if team == nil {
		return nil, fmt.Errorf("team is required")
	}
	if strings.TrimSpace(team.Name) == "" {
		return nil, fmt.Errorf("team name is required")
	}
	if strings.TrimSpace(team.ID) == "" {
		team.ID = uuid.New().String()
	}
	if strings.TrimSpace(team.A2AID) == "" {
		team.A2AID = uuid.NewString()
	}
	if strings.TrimSpace(team.Description) == "" {
		team.Description = team.Name
	}
	now := time.Now()
	if team.CreatedAt.IsZero() {
		team.CreatedAt = now
	}
	team.UpdatedAt = now

	if err := m.store.SaveTeam(team); err != nil {
		return nil, err
	}

	leadAgentName := defaultTeamLeadName(team.Name)
	if existing, err := m.store.GetAgentModelByName(leadAgentName); err == nil {
		if _, err := m.JoinTeam(context.Background(), existing.Name, team.ID, AgentKindCaptain); err != nil {
			return nil, err
		}
	} else {
		_, err := m.CreateMember(context.Background(), &AgentModel{
			ID:           uuid.New().String(),
			TeamID:       team.ID,
			Name:         leadAgentName,
			Kind:         AgentKindCaptain,
			Description:  fmt.Sprintf("Default captain agent for %s.", team.Name),
			Instructions: fmt.Sprintf("You are the captain agent for team %s. Help directly when possible and coordinate specialists when useful.", team.Name),
			MCPTools:     defaultMemberMCPTools("Captain"),
			EnableRAG:    true,
			EnableMemory: true,
			EnableMCP:    true,
		})
		if err != nil {
			return nil, err
		}
	}

	return m.store.GetTeam(team.ID)
}

func (m *TeamManager) ListTeams() ([]*Team, error) {
	return m.store.ListTeams()
}

func (m *TeamManager) GetTeamByName(name string) (*Team, error) {
	return m.store.GetTeamByName(strings.TrimSpace(name))
}

func (m *TeamManager) GetTeamByA2AID(a2aID string) (*Team, error) {
	return m.store.GetTeamByA2AID(strings.TrimSpace(a2aID))
}

func (m *TeamManager) AddCaptain(ctx context.Context, teamID, name, description, instructions string) (*AgentModel, error) {
	return m.CreateMember(ctx, &AgentModel{
		TeamID:       strings.TrimSpace(teamID),
		Name:         strings.TrimSpace(name),
		Kind:         AgentKindCaptain,
		Description:  strings.TrimSpace(description),
		Instructions: strings.TrimSpace(instructions),
	})
}

func (m *TeamManager) AddSpecialist(ctx context.Context, teamID, name, description, instructions string) (*AgentModel, error) {
	return m.CreateMember(ctx, &AgentModel{
		TeamID:       strings.TrimSpace(teamID),
		Name:         strings.TrimSpace(name),
		Kind:         AgentKindSpecialist,
		Description:  strings.TrimSpace(description),
		Instructions: strings.TrimSpace(instructions),
	})
}

func (m *TeamManager) ListCaptains() ([]*AgentModel, error) {
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

func (m *TeamManager) ListSpecialists() ([]*AgentModel, error) {
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

// GetMemberByName retrieves a persisted team agent model by name.
func (m *TeamManager) GetMemberByName(name string) (*AgentModel, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	if len(model.Teams) == 0 {
		return nil, fmt.Errorf("agent '%s' is not in a team", model.Name)
	}
	if len(model.Teams) == 1 {
		return cloneAgentForMembership(model, model.Teams[0]), nil
	}
	for _, membership := range model.Teams {
		if membership.TeamID == defaultTeamID {
			return cloneAgentForMembership(model, membership), nil
		}
	}
	return cloneAgentForMembership(model, model.Teams[0]), nil
}

func (m *TeamManager) GetMemberByNameInTeam(name, teamID string) (*AgentModel, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	teamID = strings.TrimSpace(teamID)
	for _, membership := range model.Teams {
		if membership.TeamID == teamID {
			return cloneAgentForMembership(model, membership), nil
		}
	}
	return nil, fmt.Errorf("agent '%s' is not in team %s", model.Name, teamID)
}

// getOrBuildService returns a cached service or builds a new one for the agent model.
func (m *TeamManager) getOrBuildService(name string) (*Service, error) {
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

	newSvc, err := m.buildServiceForModel(model)
	if err != nil {
		return nil, err
	}

	m.services[name] = newSvc

	return newSvc, nil
}

func (m *TeamManager) buildEphemeralService(name string) (*Service, error) {
	model, err := m.store.GetAgentModelByName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	return m.buildServiceForModel(model)
}

func (m *TeamManager) buildServiceForModel(model *AgentModel) (*Service, error) {
	if model == nil {
		return nil, fmt.Errorf("agent model is required")
	}

	var agentgoCfg *config.Config
	builder := New(model.Name)
	systemPrompt := strings.TrimSpace(model.Instructions)

	cfg := m.cfg
	if cfg == nil {
		var loadErr error
		cfg, loadErr = config.Load()
		if loadErr != nil {
			cfg = nil
		}
	}
	if cfg != nil {
		agentgoCfg = cfg
		if len(model.Teams) > 0 {
			systemPrompt = m.buildTeamSystemPromptForModel(cfg, model) + "\n\n" + buildTeamMemberPrompt(model)
		} else {
			systemPrompt = buildStandaloneAgentPrompt(cfg, model)
		}
	} else {
		if len(model.Teams) > 0 {
			systemPrompt = m.buildTeamSystemPromptForModel(nil, model) + "\n\n" + buildTeamMemberPrompt(model)
		}
	}
	systemPrompt = m.decorateDelegableBuiltInAgentPrompt(systemPrompt, model)
	systemPrompt = m.decorateKnownAgentsPrompt(systemPrompt, model)
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

	if hasMembershipRole(model.Teams, AgentKindCaptain) {
		m.RegisterCaptainTools(newSvc)
		configureCaptainService(newSvc)
	}
	if strings.EqualFold(strings.TrimSpace(model.Name), defaultConciergeAgentName) {
		m.RegisterConciergeTools(newSvc)
	}
	m.registerBuiltInAgentDelegationTools(newSvc, model)
	m.registerAgentMessagingTools(newSvc, model)
	if strings.EqualFold(strings.TrimSpace(model.Name), defaultOperatorAgentName) {
		registerOperatorTools(newSvc)
	}

	if label := configuredModelLabel(model); label != "" {
		newSvc.agent.SetModel(label)
	}
	newSvc.SetMemoryScope(model.Name, primaryMemoryTeamID(model), "")

	return newSvc, nil
}

func primaryMemoryTeamID(model *AgentModel) string {
	if model == nil {
		return ""
	}
	if teamID := strings.TrimSpace(model.TeamID); teamID != "" {
		return teamID
	}
	for _, membership := range model.Teams {
		if teamID := strings.TrimSpace(membership.TeamID); teamID != "" {
			return teamID
		}
	}
	return ""
}

func (m *TeamManager) buildTeamSystemPromptForModel(cfg *config.Config, model *AgentModel) string {
	base := buildTeamSystemPrompt(cfg, model)
	if model == nil || !hasMembershipRole(model.Teams, AgentKindCaptain) {
		return base
	}

	roster := strings.TrimSpace(m.buildCaptainRosterContext(model))
	if roster == "" {
		return base
	}
	return strings.TrimSpace(base + "\n\n" + roster)
}

func (m *TeamManager) decorateDelegableBuiltInAgentPrompt(base string, model *AgentModel) string {
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

func (m *TeamManager) decorateKnownAgentsPrompt(base string, model *AgentModel) string {
	base = strings.TrimSpace(base)
	context := strings.TrimSpace(m.buildKnownAgentsContext(model))
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
	teamName := "AgentGo Team"
	if cfg != nil && cfg.Agent.Name != "" {
		agentName = cfg.Agent.Name
		teamName = agentName + " Team"
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
		fmt.Sprintf("You are working as part of a %s.", teamName),
		"The team shares one workspace and one project context.",
	}
	lines = append(lines, buildRuntimeContextLines()...)
	if workspace != "" {
		lines = append(lines, "- Shared writable workspace: "+workspace)
	}
	if projectRoot != "" {
		lines = append(lines, "- Active project root: "+projectRoot)
		lines = append(lines, "- Stay inside the active project root unless the user explicitly asks for another location.")
	}
	if model != nil && hasMembershipRole(model.Teams, AgentKindCaptain) {
		lines = append(lines,
			"- You are the captain for this team.",
			"- Handle direct user requests when possible and delegate specialist work only when that improves the result.",
			"- Prefer assigning multi-step or implementation-heavy work to named team members via async team tasks instead of doing it yourself.",
			"- Use async team task submission first for coding, file-writing, research, and verification work. Only use synchronous delegation when you truly need an immediate inline sub-result.",
			"- Do not use generic sub-agent delegation; coordinate through the team members listed below.",
		)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (m *TeamManager) buildCaptainRosterContext(model *AgentModel) string {
	if model == nil || !hasMembershipRole(model.Teams, AgentKindCaptain) {
		return ""
	}

	lines := []string{
		"Captain responsibilities and team roster:",
		"- Your role: captain / team lead.",
	}
	if desc := strings.TrimSpace(model.Description); desc != "" {
		lines = append(lines, "- Your responsibility summary: "+desc)
	}
	if instr := strings.TrimSpace(model.Instructions); instr != "" {
		lines = append(lines, "- Your operating responsibilities: "+singleLinePromptText(instr))
	}

	for _, membership := range model.Teams {
		if membership.Role != AgentKindCaptain {
			continue
		}
		teamLabel := strings.TrimSpace(membership.TeamName)
		if teamLabel == "" {
			teamLabel = strings.TrimSpace(membership.TeamID)
		}
		if teamLabel == "" {
			teamLabel = "current team"
		}

		lines = append(lines, "- Team: "+teamLabel)
		members, err := m.ListTeamAgentsByTeam(membership.TeamID)
		if err != nil {
			lines = append(lines, "  - Unable to load team members: "+err.Error())
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
		"Team task context:",
		"- Target team agent: " + strings.TrimSpace(agentName),
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
	case "operator", "verifier":
		return []string{"*"}
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

func (m *TeamManager) ensureAgentRunning(ctx context.Context, name string) error {
	model, err := m.store.GetAgentModelByName(name)
	if err != nil {
		return err
	}
	if isBuiltInAgentModel(model) {
		return m.ensureBuiltInAgentRuntime(ctx, model.Name)
	}
	return nil
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

// DispatchTask runs the task on the target team agent service directly.
func (m *TeamManager) DispatchTask(ctx context.Context, agentName string, instruction string) (string, error) {
	return m.dispatchTaskWithOptions(ctx, agentName, instruction, "", nil)
}

// ChatWithMember runs a team chat turn with persistent history scoped to one conversation key and team agent.
func (m *TeamManager) ChatWithMember(ctx context.Context, conversationKey, agentName string, instruction string) (string, error) {
	conversationKey = strings.TrimSpace(conversationKey)
	if conversationKey == "" {
		return m.DispatchTask(ctx, agentName, instruction)
	}
	return m.dispatchTaskWithOptions(ctx, agentName, instruction, m.conversationSessionID(conversationKey, agentName), nil)
}

func (m *TeamManager) dispatchTask(ctx context.Context, agentName string, instruction string, sessionID string) (string, error) {
	return m.dispatchTaskWithOptions(ctx, agentName, instruction, sessionID, nil)
}

func (m *TeamManager) dispatchTaskWithOptions(ctx context.Context, agentName string, instruction string, sessionID string, extraOpts []RunOption) (string, error) {
	if err := m.ensureAgentRunning(ctx, agentName); err != nil {
		return "", fmt.Errorf("cannot start agent %s: %w", agentName, err)
	}

	wrappedInstruction, runOptions := m.prepareDispatchRequest(agentName, instruction, sessionID, extraOpts)

	var (
		res *ExecutionResult
		err error
	)
	if isBuiltInRuntimeAgentName(agentName) {
		res, err = m.dispatchViaBuiltInRuntime(ctx, agentName, wrappedInstruction, runOptions)
	} else {
		res, err = m.executeDispatchSync(ctx, agentName, wrappedInstruction, runOptions)
	}
	if err != nil {
		return "", err
	}

	if text := extractDispatchText(res); text != "" {
		return text, nil
	}

	bz, _ := json.Marshal(res.FinalResult)
	return string(bz), nil
}

func (m *TeamManager) conversationSessionID(conversationKey, agentName string) string {
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
func (m *TeamManager) DispatchTaskStream(ctx context.Context, agentName string, instruction string) (<-chan *Event, error) {
	return m.dispatchTaskStream(ctx, agentName, instruction, "", nil)
}

// ChatWithMemberStream runs a team chat turn with persistent history and returns the raw event stream.
func (m *TeamManager) ChatWithMemberStream(ctx context.Context, conversationKey, agentName, instruction string) (<-chan *Event, error) {
	conversationKey = strings.TrimSpace(conversationKey)
	if conversationKey == "" {
		return m.DispatchTaskStream(ctx, agentName, instruction)
	}
	return m.dispatchTaskStream(ctx, agentName, instruction, m.conversationSessionID(conversationKey, agentName), nil)
}

// DispatchTaskStreamWithOptions runs the task and returns the raw event stream with explicit run options.
func (m *TeamManager) DispatchTaskStreamWithOptions(ctx context.Context, agentName string, instruction string, opts ...RunOption) (<-chan *Event, error) {
	return m.dispatchTaskStream(ctx, agentName, instruction, "", opts)
}

// ChatWithMemberStreamWithOptions runs a team chat turn with persistent history and explicit run options.
func (m *TeamManager) ChatWithMemberStreamWithOptions(ctx context.Context, conversationKey, agentName, instruction string, opts ...RunOption) (<-chan *Event, error) {
	conversationKey = strings.TrimSpace(conversationKey)
	if conversationKey == "" {
		return m.DispatchTaskStreamWithOptions(ctx, agentName, instruction, opts...)
	}
	return m.dispatchTaskStream(ctx, agentName, instruction, m.conversationSessionID(conversationKey, agentName), opts)
}

func (m *TeamManager) dispatchTaskStream(ctx context.Context, agentName string, instruction string, sessionID string, extraOpts []RunOption) (<-chan *Event, error) {
	if err := m.ensureAgentRunning(ctx, agentName); err != nil {
		return nil, fmt.Errorf("cannot start agent %s: %w", agentName, err)
	}

	wrappedInstruction, runOptions := m.prepareDispatchRequest(agentName, instruction, sessionID, extraOpts)
	if isBuiltInRuntimeAgentName(agentName) {
		return m.dispatchStreamViaBuiltInRuntime(ctx, agentName, wrappedInstruction, runOptions)
	}
	return m.executeDispatchStream(ctx, agentName, wrappedInstruction, runOptions)
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

// RegisterCaptainTools adds the team management tools to the frontdesk lead agent.
func (m *TeamManager) RegisterCaptainTools(captain *Service) {
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
			Description: "Queue async work for one or more named team members and return immediately with a task id. Prefer this for implementation, research, and verification work.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_names": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "The team member names that should handle the async work.",
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
		lead, team, err := m.resolveCaptainServiceContext(captain)
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

		task, err := m.SubmitTeamTask(ctx, captain.CurrentSessionID(), team.ID, prompt, agentNames)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"task_id":      task.ID,
			"team_id":      task.TeamID,
			"team_name":    team.Name,
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
			Description: "List recent async tasks for the captain's current team.",
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
		_, team, err := m.resolveCaptainServiceContext(captain)
		if err != nil {
			return nil, err
		}
		limit := getIntArg(args, "limit", 10)
		tasks := m.ListSharedTasksForTeam(team.ID, time.Time{}, limit)
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
			Description: "Synchronously delegate a short task to one team agent and wait for the inline result. Prefer submit_team_async for longer implementation work.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_name": map[string]interface{}{
						"type":        "string",
						"description": "The name of the target team agent.",
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
func (m *TeamManager) RegisterCommanderTools(commander *Service) {
	m.RegisterCaptainTools(commander)
}

func (m *TeamManager) resolveCaptainServiceContext(captain *Service) (*AgentModel, *Team, error) {
	if captain == nil || captain.agent == nil {
		return nil, nil, fmt.Errorf("captain service is not initialized")
	}
	member, err := m.GetMemberByName(captain.agent.Name())
	if err != nil {
		return nil, nil, err
	}
	for _, membership := range member.Teams {
		if membership.Role == AgentKindCaptain {
			team, teamErr := m.store.GetTeam(membership.TeamID)
			if teamErr != nil {
				return nil, nil, teamErr
			}
			return member, team, nil
		}
	}
	return nil, nil, fmt.Errorf("%s is not a team captain", captain.agent.Name())
}
