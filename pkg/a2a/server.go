package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	a2aproto "github.com/a2aproject/a2a-go/a2a"
	a2asrv "github.com/a2aproject/a2a-go/a2asrv"
	"github.com/a2aproject/a2a-go/a2asrv/eventqueue"
	agentpkg "github.com/liliang-cn/agent-go/v2/pkg/agent"
)

type AgentRunner interface {
	Run(context.Context, string, ...agentpkg.RunOption) (*agentpkg.ExecutionResult, error)
	RunStream(context.Context, string) (<-chan *agentpkg.Event, error)
}

type AgentCatalog interface {
	ListAgents() ([]*agentpkg.AgentModel, error)
	GetAgentByName(name string) (*agentpkg.AgentModel, error)
	GetAgentByA2AID(a2aID string) (*agentpkg.AgentModel, error)
	GetAgentService(name string) (AgentRunner, error)
	ListTeams() ([]*agentpkg.Team, error)
	GetTeamByName(name string) (*agentpkg.Team, error)
	GetTeamByA2AID(a2aID string) (*agentpkg.Team, error)
	GetLeadAgentForTeam(teamID string) (*agentpkg.AgentModel, error)
}

type AsyncTaskCatalog interface {
	SubmitTeamTask(ctx context.Context, sessionID, teamID, prompt string, agentNames []string) (*agentpkg.AsyncTask, error)
	GetTask(taskID string) (*agentpkg.AsyncTask, error)
	SubscribeTask(taskID string) (<-chan *agentpkg.TaskEvent, func(), error)
	CancelTask(ctx context.Context, taskID string) (*agentpkg.AsyncTask, error)
	ListTasks(limit int) []*agentpkg.AsyncTask
}

type Config struct {
	Enabled              bool
	PublicBaseURL        string
	PathPrefix           string
	IncludeBuiltInAgents bool
	IncludeCustomAgents  bool
	AgentVersion         string
	ProviderOrganization string
	ProviderURL          string
	DocumentationURL     string
	IncludeDebugEvents   bool
}

func DefaultConfig() Config {
	return Config{
		PathPrefix:           "/a2a",
		IncludeBuiltInAgents: true,
		IncludeCustomAgents:  true,
		AgentVersion:         "dev",
	}
}

type Server struct {
	catalog AgentCatalog
	cfg     Config

	mu        sync.Mutex
	endpoints map[string]*agentEndpoint
}

type agentEndpoint struct {
	agentName string
	handler   http.Handler
}

func NewServer(catalog AgentCatalog, cfg Config) (*Server, error) {
	if catalog == nil {
		return nil, fmt.Errorf("a2a catalog is required")
	}
	cfg = normalizeConfig(cfg)
	return &Server{
		catalog:   catalog,
		cfg:       cfg,
		endpoints: make(map[string]*agentEndpoint),
	}, nil
}

func normalizeConfig(cfg Config) Config {
	def := DefaultConfig()
	if strings.TrimSpace(cfg.PathPrefix) == "" {
		cfg.PathPrefix = def.PathPrefix
	}
	cfg.PathPrefix = normalizePathPrefix(cfg.PathPrefix)
	if cfg.AgentVersion == "" {
		cfg.AgentVersion = def.AgentVersion
	}
	if !cfg.IncludeBuiltInAgents && !cfg.IncludeCustomAgents {
		cfg.IncludeBuiltInAgents = def.IncludeBuiltInAgents
		cfg.IncludeCustomAgents = def.IncludeCustomAgents
	}
	return cfg
}

func normalizePathPrefix(prefix string) string {
	prefix = "/" + strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "/" {
		return "/a2a"
	}
	return prefix
}

func (s *Server) Enabled() bool {
	return s != nil && s.cfg.Enabled
}

func (s *Server) Mount(mux *http.ServeMux) {
	if mux == nil || s == nil {
		return
	}
	mux.Handle(s.cfg.PathPrefix, s)
	mux.Handle(s.cfg.PathPrefix+"/", s)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || !s.cfg.Enabled {
		http.NotFound(w, r)
		return
	}

	rel, ok := trimPathPrefix(r.URL.Path, s.cfg.PathPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch rel {
	case "", "/":
		http.Redirect(w, r, s.cfg.PathPrefix+"/agents", http.StatusTemporaryRedirect)
		return
	case "/agents":
		s.handleAgentsIndex(w, r)
		return
	case "/teams":
		s.handleTeamsIndex(w, r)
		return
	}

	resourceKind, name, endpointKind, ok := parseResourcePath(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch resourceKind {
	case "agents":
		model, err := s.lookupExposedAgent(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch endpointKind {
		case "card":
			s.handleAgentCard(w, r, model)
		case "invoke":
			endpoint, err := s.getAgentEndpoint(model.Name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			endpoint.handler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	case "teams":
		team, err := s.lookupExposedTeam(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch endpointKind {
		case "card":
			s.handleTeamCard(w, r, team)
		case "invoke":
			endpoint, err := s.getTeamEndpoint(team.Name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			endpoint.handler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func trimPathPrefix(requestPath, prefix string) (string, bool) {
	if requestPath == prefix {
		return "/", true
	}
	if !strings.HasPrefix(requestPath, prefix+"/") {
		return "", false
	}
	return strings.TrimPrefix(requestPath, prefix), true
}

func parseResourcePath(rel string) (string, string, string, bool) {
	trimmed := strings.Trim(rel, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 || (parts[0] != "agents" && parts[0] != "teams") {
		return "", "", "", false
	}
	name, err := url.PathUnescape(parts[1])
	if err != nil || strings.TrimSpace(name) == "" {
		return "", "", "", false
	}
	if len(parts) == 3 && parts[2] == "invoke" {
		return parts[0], name, "invoke", true
	}
	if len(parts) == 4 && parts[2] == ".well-known" && parts[3] == "agent-card.json" {
		return parts[0], name, "card", true
	}
	return "", "", "", false
}

func (s *Server) handleAgentsIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agents, err := s.listExposedAgents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type item struct {
		Name      string `json:"name"`
		BuiltIn   bool   `json:"built_in"`
		CardURL   string `json:"card_url"`
		InvokeURL string `json:"invoke_url"`
	}
	out := make([]item, 0, len(agents))
	for _, model := range agents {
		out = append(out, item{
			Name:      model.Name,
			BuiltIn:   isBuiltIn(model),
			CardURL:   s.agentCardURL(r, model),
			InvokeURL: s.invokeURL(r, model),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"agents": out})
}

func (s *Server) handleTeamsIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	teams, err := s.listExposedTeams()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type item struct {
		Name      string `json:"name"`
		CardURL   string `json:"card_url"`
		InvokeURL string `json:"invoke_url"`
	}
	out := make([]item, 0, len(teams))
	for _, team := range teams {
		out = append(out, item{
			Name:      team.Name,
			CardURL:   s.teamCardURL(r, team),
			InvokeURL: s.teamInvokeURL(r, team),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"teams": out})
}

func (s *Server) handleAgentCard(w http.ResponseWriter, r *http.Request, model *agentpkg.AgentModel) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	card := s.buildAgentCard(r, model)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(card)
}

func (s *Server) handleTeamCard(w http.ResponseWriter, r *http.Request, team *agentpkg.Team) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	card := s.buildTeamCard(r, team)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(card)
}

func (s *Server) buildAgentCard(r *http.Request, model *agentpkg.AgentModel) *a2aproto.AgentCard {
	desc := strings.TrimSpace(model.Description)
	if desc == "" {
		desc = model.Name
	}
	card := &a2aproto.AgentCard{
		Name:               model.Name,
		Description:        desc,
		URL:                s.invokeURL(r, model),
		PreferredTransport: a2aproto.TransportProtocolJSONRPC,
		ProtocolVersion:    string(a2aproto.Version),
		Version:            s.cfg.AgentVersion,
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Capabilities: a2aproto.AgentCapabilities{
			Streaming: true,
		},
		Skills: buildSkills(model),
	}
	if strings.TrimSpace(s.cfg.DocumentationURL) != "" {
		card.DocumentationURL = strings.TrimSpace(s.cfg.DocumentationURL)
	}
	if strings.TrimSpace(s.cfg.ProviderOrganization) != "" || strings.TrimSpace(s.cfg.ProviderURL) != "" {
		card.Provider = &a2aproto.AgentProvider{
			Org: strings.TrimSpace(s.cfg.ProviderOrganization),
			URL: strings.TrimSpace(s.cfg.ProviderURL),
		}
	}
	return card
}

func (s *Server) buildTeamCard(r *http.Request, team *agentpkg.Team) *a2aproto.AgentCard {
	desc := strings.TrimSpace(team.Description)
	if desc == "" {
		desc = team.Name
	}
	card := &a2aproto.AgentCard{
		Name:               team.Name,
		Description:        desc,
		URL:                s.teamInvokeURL(r, team),
		PreferredTransport: a2aproto.TransportProtocolJSONRPC,
		ProtocolVersion:    string(a2aproto.Version),
		Version:            s.cfg.AgentVersion,
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Capabilities: a2aproto.AgentCapabilities{
			Streaming: true,
		},
		Skills: []a2aproto.AgentSkill{{
			ID:          normalizeSkillID(team.Name),
			Name:        team.Name,
			Description: desc,
			Tags:        []string{"team", "agentgo", "orchestration"},
			Examples:    []string{team.Name + " coordinates work across its member agents."},
		}},
	}
	if strings.TrimSpace(s.cfg.DocumentationURL) != "" {
		card.DocumentationURL = strings.TrimSpace(s.cfg.DocumentationURL)
	}
	if strings.TrimSpace(s.cfg.ProviderOrganization) != "" || strings.TrimSpace(s.cfg.ProviderURL) != "" {
		card.Provider = &a2aproto.AgentProvider{
			Org: strings.TrimSpace(s.cfg.ProviderOrganization),
			URL: strings.TrimSpace(s.cfg.ProviderURL),
		}
	}
	return card
}

func buildSkills(model *agentpkg.AgentModel) []a2aproto.AgentSkill {
	if model == nil {
		return nil
	}
	tags := []string{string(model.Kind), "agentgo"}
	if isBuiltIn(model) {
		tags = append(tags, "built-in")
	} else {
		tags = append(tags, "custom")
	}
	if model.EnableRAG {
		tags = append(tags, "rag")
	}
	if model.EnableMemory {
		tags = append(tags, "memory")
	}
	if model.EnableMCP {
		tags = append(tags, "mcp")
	}

	skills := []a2aproto.AgentSkill{{
		ID:          normalizeSkillID(model.Name),
		Name:        model.Name,
		Description: strings.TrimSpace(model.Description),
		Tags:        tags,
		Examples: []string{
			model.Name + " can help with tasks in its configured domain.",
		},
	}}
	for _, skillID := range model.Skills {
		skillID = strings.TrimSpace(skillID)
		if skillID == "" {
			continue
		}
		skills = append(skills, a2aproto.AgentSkill{
			ID:          normalizeSkillID(skillID),
			Name:        skillID,
			Description: "Configured AgentGo skill available to this agent.",
			Tags:        []string{"agentgo", "skill"},
		})
	}
	return skills
}

func normalizeSkillID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "agentgo"
	}
	replacer := strings.NewReplacer(" ", "-", "_", "-", "/", "-", ":", "-", ".", "-")
	value = replacer.Replace(value)
	return strings.Trim(value, "-")
}

func (s *Server) listExposedAgents() ([]*agentpkg.AgentModel, error) {
	agents, err := s.catalog.ListAgents()
	if err != nil {
		return nil, err
	}
	out := make([]*agentpkg.AgentModel, 0, len(agents))
	for _, model := range agents {
		if s.isExposedAgent(model) {
			out = append(out, model)
		}
	}
	return out, nil
}

func (s *Server) listExposedTeams() ([]*agentpkg.Team, error) {
	teams, err := s.catalog.ListTeams()
	if err != nil {
		return nil, err
	}
	out := make([]*agentpkg.Team, 0, len(teams))
	for _, team := range teams {
		if s.isExposedTeam(team) {
			out = append(out, team)
		}
	}
	return out, nil
}

func (s *Server) lookupExposedAgent(id string) (*agentpkg.AgentModel, error) {
	id = strings.TrimSpace(id)
	model, err := s.catalog.GetAgentByA2AID(id)
	if err != nil {
		model, err = s.catalog.GetAgentByName(id)
		if err != nil {
			return nil, err
		}
	}
	if !s.isExposedAgent(model) {
		return nil, fmt.Errorf("agent %q is not exposed via a2a", id)
	}
	return model, nil
}

func (s *Server) lookupExposedTeam(id string) (*agentpkg.Team, error) {
	id = strings.TrimSpace(id)
	team, err := s.catalog.GetTeamByA2AID(id)
	if err != nil {
		team, err = s.catalog.GetTeamByName(id)
		if err != nil {
			return nil, err
		}
	}
	if !s.isExposedTeam(team) {
		return nil, fmt.Errorf("team %q is not exposed via a2a", id)
	}
	return team, nil
}

func (s *Server) isExposedAgent(model *agentpkg.AgentModel) bool {
	if model == nil || !model.EnableA2A || len(model.Teams) != 0 {
		return false
	}
	if isBuiltIn(model) {
		return s.cfg.IncludeBuiltInAgents
	}
	return s.cfg.IncludeCustomAgents
}

func (s *Server) isExposedTeam(team *agentpkg.Team) bool {
	return team != nil && team.EnableA2A
}

func isBuiltIn(model *agentpkg.AgentModel) bool {
	if model == nil {
		return false
	}
	switch strings.TrimSpace(model.Name) {
	case "Concierge", "Assistant", "Operator", "Captain", "Stakeholder", "Archivist", "Verifier":
		return true
	default:
		return false
	}
}

func (s *Server) getAgentEndpoint(agentName string) (*agentEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := "agent:" + agentName
	if ep := s.endpoints[key]; ep != nil {
		return ep, nil
	}

	requestHandler := a2asrv.NewHandler(&executor{
		catalog:      s.catalog,
		resourceKind: "agent",
		name:         agentName,
		includeDebug: s.cfg.IncludeDebugEvents,
	})
	ep := &agentEndpoint{
		agentName: agentName,
		handler:   a2asrv.NewJSONRPCHandler(requestHandler),
	}
	s.endpoints[key] = ep
	return ep, nil
}

func (s *Server) getTeamEndpoint(teamName string) (*agentEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := "team:" + teamName
	if ep := s.endpoints[key]; ep != nil {
		return ep, nil
	}

	requestHandler := a2asrv.NewHandler(&executor{
		catalog:      s.catalog,
		resourceKind: "team",
		name:         teamName,
		includeDebug: s.cfg.IncludeDebugEvents,
	})
	ep := &agentEndpoint{
		agentName: teamName,
		handler:   a2asrv.NewJSONRPCHandler(requestHandler),
	}
	s.endpoints[key] = ep
	return ep, nil
}

func (s *Server) invokeURL(r *http.Request, model *agentpkg.AgentModel) string {
	return s.publicURL(r, path.Join(s.cfg.PathPrefix, "agents", url.PathEscape(a2aIDOrName(model.A2AID, model.Name)), "invoke"))
}

func (s *Server) agentCardURL(r *http.Request, model *agentpkg.AgentModel) string {
	return s.publicURL(r, path.Join(s.cfg.PathPrefix, "agents", url.PathEscape(a2aIDOrName(model.A2AID, model.Name)), ".well-known", "agent-card.json"))
}

func (s *Server) teamInvokeURL(r *http.Request, team *agentpkg.Team) string {
	return s.publicURL(r, path.Join(s.cfg.PathPrefix, "teams", url.PathEscape(a2aIDOrName(team.A2AID, team.Name)), "invoke"))
}

func (s *Server) teamCardURL(r *http.Request, team *agentpkg.Team) string {
	return s.publicURL(r, path.Join(s.cfg.PathPrefix, "teams", url.PathEscape(a2aIDOrName(team.A2AID, team.Name)), ".well-known", "agent-card.json"))
}

func a2aIDOrName(a2aID, name string) string {
	if strings.TrimSpace(a2aID) != "" {
		return strings.TrimSpace(a2aID)
	}
	return strings.TrimSpace(name)
}

func (s *Server) publicURL(r *http.Request, suffix string) string {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.PublicBaseURL), "/")
	if base == "" && r != nil {
		scheme := "http"
		if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
			scheme = proto
		} else if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	if base == "" {
		base = "http://127.0.0.1"
	}
	return base + "/" + strings.TrimLeft(suffix, "/")
}

type executor struct {
	catalog      AgentCatalog
	resourceKind string
	name         string
	includeDebug bool
}

func (e *executor) Execute(ctx context.Context, reqCtx *a2asrv.RequestContext, q eventqueue.Queue) error {
	prompt := promptFromMessage(reqCtx.Message)
	if strings.TrimSpace(prompt) == "" {
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateRejected, "A2A request did not contain any supported input content.")
	}

	if e.resourceKind == "team" {
		return e.executeTeamTask(ctx, reqCtx, q, prompt)
	}

	runner, err := e.resolveRunner()
	if err != nil {
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, err.Error())
	}
	if err := q.Write(ctx, a2aproto.NewStatusUpdateEvent(reqCtx, a2aproto.TaskStateWorking, nil)); err != nil {
		return err
	}

	events, err := runner.RunStream(ctx, prompt)
	if err != nil {
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, err.Error())
	}

	var artifactID a2aproto.ArtifactID
	var sawArtifact bool
	var streamed strings.Builder
	for evt := range events {
		switch evt.Type {
		case agentpkg.EventTypeThinking, agentpkg.EventTypeToolCall, agentpkg.EventTypeToolResult:
			artifact := structuredRuntimeArtifact(reqCtx, evt)
			if artifact != nil {
				if artifactID == "" {
					artifactID = artifact.Artifact.ID
				} else {
					artifact.Artifact.ID = artifactID
					artifact.Append = true
				}
				if err := q.Write(ctx, artifact); err != nil {
					return err
				}
			}
		case agentpkg.EventTypePartial:
			text := evt.Content
			if text == "" {
				continue
			}
			streamed.WriteString(text)
			var artifact *a2aproto.TaskArtifactUpdateEvent
			if artifactID == "" {
				artifact = a2aproto.NewArtifactEvent(reqCtx, a2aproto.TextPart{Text: text})
				artifactID = artifact.Artifact.ID
			} else {
				artifact = a2aproto.NewArtifactUpdateEvent(reqCtx, artifactID, a2aproto.TextPart{Text: text})
			}
			if err := q.Write(ctx, artifact); err != nil {
				return err
			}
			sawArtifact = true
		case agentpkg.EventTypeComplete:
			text := strings.TrimSpace(evt.Content)
			if text == "" || text == strings.TrimSpace(streamed.String()) {
				continue
			}
			var artifact *a2aproto.TaskArtifactUpdateEvent
			if artifactID == "" {
				artifact = a2aproto.NewArtifactEvent(reqCtx, a2aproto.TextPart{Text: text})
				artifactID = artifact.Artifact.ID
			} else {
				artifact = a2aproto.NewArtifactUpdateEvent(reqCtx, artifactID, a2aproto.TextPart{Text: text})
			}
			if err := q.Write(ctx, artifact); err != nil {
				return err
			}
			sawArtifact = true
		case agentpkg.EventTypeDebug:
			if !e.includeDebugEvents() {
				continue
			}
			artifact := structuredRuntimeArtifact(reqCtx, evt)
			if artifact != nil {
				if artifactID == "" {
					artifactID = artifact.Artifact.ID
				} else {
					artifact.Artifact.ID = artifactID
					artifact.Append = true
				}
				if err := q.Write(ctx, artifact); err != nil {
					return err
				}
			}
		case agentpkg.EventTypeError:
			return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, strings.TrimSpace(evt.Content))
		}
	}

	if !sawArtifact {
		res, err := runner.Run(ctx, prompt)
		if err != nil {
			return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, err.Error())
		}
		if text := strings.TrimSpace(res.Text()); text != "" {
			if err := q.Write(ctx, a2aproto.NewArtifactEvent(reqCtx, a2aproto.TextPart{Text: text})); err != nil {
				return err
			}
		}
	}

	return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateCompleted, "")
}

func (e *executor) executeTeamTask(ctx context.Context, reqCtx *a2asrv.RequestContext, q eventqueue.Queue, prompt string) error {
	asyncCatalog, ok := e.catalog.(AsyncTaskCatalog)
	if !ok {
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, "team async task support is unavailable")
	}

	team, err := e.catalog.GetTeamByName(e.name)
	if err != nil {
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, err.Error())
	}

	submitted, err := asyncCatalog.SubmitTeamTask(ctx, string(reqCtx.TaskID), team.ID, prompt, nil)
	if err != nil {
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, err.Error())
	}

	if err := q.Write(ctx, newAsyncStatusEvent(reqCtx, submitted, a2aproto.TaskStateSubmitted, false, submitted.AckMessage)); err != nil {
		return err
	}

	events, unsubscribe, err := asyncCatalog.SubscribeTask(submitted.ID)
	if err != nil {
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, err.Error())
	}
	defer unsubscribe()

	var artifactID a2aproto.ArtifactID
	for evt := range events {
		if evt == nil {
			continue
		}
		switch evt.Type {
		case agentpkg.TaskEventTypeStarted:
			if err := q.Write(ctx, newAsyncStatusEvent(reqCtx, submitted, a2aproto.TaskStateWorking, false, evt.Message)); err != nil {
				return err
			}
		case agentpkg.TaskEventTypeRuntime:
			if evt.Runtime != nil {
				switch evt.Runtime.Type {
				case agentpkg.EventTypeThinking, agentpkg.EventTypeToolCall, agentpkg.EventTypeToolResult:
					artifact := structuredRuntimeArtifact(reqCtx, evt.Runtime)
					if artifact != nil {
						if artifactID == "" {
							artifactID = artifact.Artifact.ID
						} else {
							artifact.Artifact.ID = artifactID
							artifact.Append = true
						}
						if err := q.Write(ctx, artifact); err != nil {
							return err
						}
					}
				case agentpkg.EventTypeDebug:
					if e.includeDebugEvents() {
						artifact := structuredRuntimeArtifact(reqCtx, evt.Runtime)
						if artifact != nil {
							if artifactID == "" {
								artifactID = artifact.Artifact.ID
							} else {
								artifact.Artifact.ID = artifactID
								artifact.Append = true
							}
							if err := q.Write(ctx, artifact); err != nil {
								return err
							}
						}
					}
				}
			}
			text := extractAsyncRuntimeText(evt)
			if strings.TrimSpace(text) == "" {
				continue
			}
			var artifact *a2aproto.TaskArtifactUpdateEvent
			if artifactID == "" {
				artifact = a2aproto.NewArtifactEvent(reqCtx, a2aproto.TextPart{Text: text})
				artifactID = artifact.Artifact.ID
			} else {
				artifact = a2aproto.NewArtifactUpdateEvent(reqCtx, artifactID, a2aproto.TextPart{Text: text})
			}
			if err := q.Write(ctx, artifact); err != nil {
				return err
			}
		case agentpkg.TaskEventTypeCompleted:
			if text := strings.TrimSpace(evt.Message); text != "" {
				var artifact *a2aproto.TaskArtifactUpdateEvent
				if artifactID == "" {
					artifact = a2aproto.NewArtifactEvent(reqCtx, a2aproto.TextPart{Text: text})
				} else {
					artifact = a2aproto.NewArtifactUpdateEvent(reqCtx, artifactID, a2aproto.TextPart{Text: text})
				}
				if err := q.Write(ctx, artifact); err != nil {
					return err
				}
			}
			if err := q.Write(ctx, newAsyncStatusEvent(reqCtx, submitted, a2aproto.TaskStateCompleted, true, evt.Message)); err != nil {
				return err
			}
			return nil
		case agentpkg.TaskEventTypeFailed:
			return q.Write(ctx, newAsyncStatusEvent(reqCtx, submitted, a2aproto.TaskStateFailed, true, evt.Message))
		case agentpkg.TaskEventTypeCancelled:
			return q.Write(ctx, newAsyncStatusEvent(reqCtx, submitted, a2aproto.TaskStateCanceled, true, evt.Message))
		}
	}

	latest, err := asyncCatalog.GetTask(submitted.ID)
	if err != nil {
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, "team task ended without terminal event")
	}
	switch latest.Status {
	case agentpkg.AsyncTaskStatusCompleted:
		return q.Write(ctx, newAsyncStatusEvent(reqCtx, latest, a2aproto.TaskStateCompleted, true, latest.ResultText))
	case agentpkg.AsyncTaskStatusFailed:
		return q.Write(ctx, newAsyncStatusEvent(reqCtx, latest, a2aproto.TaskStateFailed, true, firstNonEmptyText(latest.Error, latest.ResultText)))
	case agentpkg.AsyncTaskStatusCancelled:
		return q.Write(ctx, newAsyncStatusEvent(reqCtx, latest, a2aproto.TaskStateCanceled, true, latest.ResultText))
	default:
		return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, "team task stream closed before terminal state")
	}
}

func (e *executor) resolveRunner() (AgentRunner, error) {
	switch e.resourceKind {
	case "agent":
		return e.catalog.GetAgentService(e.name)
	case "team":
		team, err := e.catalog.GetTeamByName(e.name)
		if err != nil {
			return nil, err
		}
		lead, err := e.catalog.GetLeadAgentForTeam(team.ID)
		if err != nil {
			return nil, err
		}
		return e.catalog.GetAgentService(lead.Name)
	default:
		return nil, fmt.Errorf("unsupported a2a resource kind: %s", e.resourceKind)
	}
}

func (e *executor) Cancel(ctx context.Context, reqCtx *a2asrv.RequestContext, q eventqueue.Queue) error {
	if e.resourceKind == "team" {
		asyncCatalog, ok := e.catalog.(AsyncTaskCatalog)
		if !ok {
			return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, "team async task support is unavailable")
		}
		internalTaskID := internalAsyncTaskID(reqCtx)
		if internalTaskID == "" {
			return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, "missing internal team task id")
		}
		task, err := asyncCatalog.CancelTask(ctx, internalTaskID)
		if err != nil {
			return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateFailed, err.Error())
		}
		return q.Write(ctx, newAsyncStatusEvent(reqCtx, task, a2aproto.TaskStateCanceled, true, task.ResultText))
	}
	return writeFinalStatus(ctx, q, reqCtx, a2aproto.TaskStateCanceled, "Task canceled.")
}

func writeFinalStatus(ctx context.Context, q eventqueue.Queue, reqCtx *a2asrv.RequestContext, state a2aproto.TaskState, text string) error {
	var msg *a2aproto.Message
	if strings.TrimSpace(text) != "" {
		msg = a2aproto.NewMessageForTask(a2aproto.MessageRoleAgent, reqCtx, a2aproto.TextPart{Text: strings.TrimSpace(text)})
	}
	event := a2aproto.NewStatusUpdateEvent(reqCtx, state, msg)
	event.Final = true
	return q.Write(ctx, event)
}

func promptFromMessage(message *a2aproto.Message) string {
	if message == nil {
		return ""
	}
	parts := make([]string, 0, len(message.Parts))
	for _, raw := range message.Parts {
		switch part := raw.(type) {
		case a2aproto.TextPart:
			if text := strings.TrimSpace(part.Text); text != "" {
				parts = append(parts, text)
			}
		case a2aproto.DataPart:
			if data, err := json.Marshal(part.Data); err == nil && len(data) > 0 {
				parts = append(parts, string(data))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

const agentGoAsyncTaskIDMetaKey = "agentgo_async_task_id"

func newAsyncStatusEvent(reqCtx *a2asrv.RequestContext, task *agentpkg.AsyncTask, state a2aproto.TaskState, final bool, text string) *a2aproto.TaskStatusUpdateEvent {
	var msg *a2aproto.Message
	if strings.TrimSpace(text) != "" {
		msg = a2aproto.NewMessageForTask(a2aproto.MessageRoleAgent, reqCtx, a2aproto.TextPart{Text: strings.TrimSpace(text)})
	}
	event := a2aproto.NewStatusUpdateEvent(reqCtx, state, msg)
	event.Final = final
	if task != nil {
		event.SetMeta(agentGoAsyncTaskIDMetaKey, task.ID)
		if strings.TrimSpace(task.TeamID) != "" {
			event.SetMeta("agentgo_team_id", task.TeamID)
		}
		if strings.TrimSpace(task.CaptainName) != "" {
			event.SetMeta("agentgo_captain_name", task.CaptainName)
		}
	}
	return event
}

func internalAsyncTaskID(reqCtx *a2asrv.RequestContext) string {
	if reqCtx == nil || reqCtx.StoredTask == nil || reqCtx.StoredTask.Metadata == nil {
		return ""
	}
	if raw, ok := reqCtx.StoredTask.Metadata[agentGoAsyncTaskIDMetaKey]; ok {
		if value, ok := raw.(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extractAsyncRuntimeText(evt *agentpkg.TaskEvent) string {
	if evt == nil || evt.Runtime == nil {
		return ""
	}
	switch evt.Runtime.Type {
	case agentpkg.EventTypePartial, agentpkg.EventTypeComplete:
		return strings.TrimSpace(evt.Runtime.Content)
	default:
		return ""
	}
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (e *executor) includeDebugEvents() bool {
	return e.includeDebug
}

func structuredRuntimeArtifact(reqCtx *a2asrv.RequestContext, evt *agentpkg.Event) *a2aproto.TaskArtifactUpdateEvent {
	if evt == nil {
		return nil
	}
	data := map[string]any{
		"event_type": string(evt.Type),
		"agent_name": evt.AgentName,
		"agent_id":   evt.AgentID,
	}
	switch evt.Type {
	case agentpkg.EventTypeThinking, agentpkg.EventTypeDebug:
		if strings.TrimSpace(evt.Content) != "" {
			data["content"] = strings.TrimSpace(evt.Content)
		}
		if evt.Round > 0 {
			data["round"] = evt.Round
		}
		if strings.TrimSpace(evt.DebugType) != "" {
			data["debug_type"] = evt.DebugType
		}
	case agentpkg.EventTypeToolCall:
		data["tool_name"] = evt.ToolName
		if evt.ToolArgs != nil {
			data["tool_args"] = evt.ToolArgs
		}
	case agentpkg.EventTypeToolResult:
		data["tool_name"] = evt.ToolName
		if evt.ToolResult != nil {
			data["tool_result"] = evt.ToolResult
		}
	}

	artifact := a2aproto.NewArtifactEvent(reqCtx, a2aproto.DataPart{
		Data: data,
		Metadata: map[string]any{
			"agentgo_runtime_event": true,
		},
	})
	artifact.SetMeta("agentgo_runtime_event", true)
	return artifact
}
