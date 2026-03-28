package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentpkg "github.com/liliang-cn/agent-go/v2/pkg/agent"
)

type fakeCatalog struct {
	agents  map[string]*agentpkg.AgentModel
	teams   map[string]*agentpkg.Team
	runners map[string]AgentRunner
	tasks   map[string]*agentpkg.TeamResponse
}

func (f *fakeCatalog) ListAgents() ([]*agentpkg.AgentModel, error) {
	out := make([]*agentpkg.AgentModel, 0, len(f.agents))
	for _, agent := range f.agents {
		out = append(out, agent)
	}
	return out, nil
}

func (f *fakeCatalog) GetAgentByName(name string) (*agentpkg.AgentModel, error) {
	if agent, ok := f.agents[name]; ok {
		return agent, nil
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) GetAgentByA2AID(a2aID string) (*agentpkg.AgentModel, error) {
	for _, agent := range f.agents {
		if agent != nil && agent.A2AID == a2aID {
			return agent, nil
		}
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) GetAgentService(name string) (AgentRunner, error) {
	if runner, ok := f.runners[name]; ok {
		return runner, nil
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) ListTeams() ([]*agentpkg.Team, error) {
	out := make([]*agentpkg.Team, 0, len(f.teams))
	for _, team := range f.teams {
		out = append(out, team)
	}
	return out, nil
}

func (f *fakeCatalog) GetTeamByName(name string) (*agentpkg.Team, error) {
	if team, ok := f.teams[name]; ok {
		return team, nil
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) GetTeamByA2AID(a2aID string) (*agentpkg.Team, error) {
	for _, team := range f.teams {
		if team != nil && team.A2AID == a2aID {
			return team, nil
		}
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) GetLeadAgentForTeam(teamID string) (*agentpkg.AgentModel, error) {
	for _, team := range f.teams {
		if team != nil && team.ID == teamID {
			if agent, ok := f.agents[team.Name+" Captain"]; ok {
				return agent, nil
			}
		}
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) SubmitTeamRequest(ctx context.Context, req *agentpkg.TeamRequest) (*agentpkg.TeamResponse, error) {
	if f.tasks == nil {
		f.tasks = make(map[string]*agentpkg.TeamResponse)
	}
	var team *agentpkg.Team
	for _, candidate := range f.teams {
		if candidate != nil && (candidate.ID == req.TeamID || candidate.A2AID == req.TeamID || candidate.Name == req.TeamID || candidate.Name == req.TeamName) {
			team = candidate
			break
		}
	}
	if team == nil {
		return nil, http.ErrMissingFile
	}
	lead, err := f.GetLeadAgentForTeam(team.ID)
	if err != nil {
		return nil, err
	}
	task := &agentpkg.TeamResponse{
		ProtocolVersion: agentpkg.TeamGatewayProtocolVersion,
		ID:              "team-response-" + team.ID,
		RequestID:       req.ID,
		SessionID:       req.SessionID,
		TeamID:          team.ID,
		TeamName:        team.Name,
		CaptainName:     lead.Name,
		AgentNames:      append([]string(nil), req.AgentNames...),
		Prompt:          req.Prompt,
		Status:          agentpkg.TeamResponseStatusCompleted,
		AckMessage:      "accepted",
		ResultText:      "team response",
		CreatedAt:       req.RequestedAt,
	}
	f.tasks[task.ID] = task
	return task, nil
}

func (f *fakeCatalog) GetTeamResponse(taskID string) (*agentpkg.TeamResponse, error) {
	if task, ok := f.tasks[taskID]; ok {
		return task, nil
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) SubscribeTeamResponse(taskID string) (<-chan *agentpkg.TeamResponseEvent, func(), error) {
	task, err := f.GetTeamResponse(taskID)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan *agentpkg.TeamResponseEvent, 4)
	ch <- &agentpkg.TeamResponseEvent{
		ID:              "evt-started",
		ProtocolVersion: agentpkg.TeamGatewayProtocolVersion,
		ResponseID:      task.ID,
		RequestID:       task.RequestID,
		TeamID:          task.TeamID,
		TeamName:        task.TeamName,
		CaptainName:     task.CaptainName,
		Type:            agentpkg.TeamResponseEventTypeStarted,
		Status:          agentpkg.TeamResponseStatusRunning,
		Message:         "started",
	}
	ch <- &agentpkg.TeamResponseEvent{
		ID:              "evt-progress",
		ProtocolVersion: agentpkg.TeamGatewayProtocolVersion,
		ResponseID:      task.ID,
		RequestID:       task.RequestID,
		TeamID:          task.TeamID,
		TeamName:        task.TeamName,
		CaptainName:     task.CaptainName,
		Type:            agentpkg.TeamResponseEventTypeProgress,
		Status:          agentpkg.TeamResponseStatusRunning,
		Runtime:         &agentpkg.Event{Type: agentpkg.EventTypePartial, Content: "partial team output"},
	}
	ch <- &agentpkg.TeamResponseEvent{
		ID:              "evt-completed",
		ProtocolVersion: agentpkg.TeamGatewayProtocolVersion,
		ResponseID:      task.ID,
		RequestID:       task.RequestID,
		TeamID:          task.TeamID,
		TeamName:        task.TeamName,
		CaptainName:     task.CaptainName,
		Type:            agentpkg.TeamResponseEventTypeCompleted,
		Status:          agentpkg.TeamResponseStatusCompleted,
		Message:         task.ResultText,
	}
	close(ch)
	return ch, func() {}, nil
}

func (f *fakeCatalog) CancelTeamResponse(ctx context.Context, taskID string) (*agentpkg.TeamResponse, error) {
	task, err := f.GetTeamResponse(taskID)
	if err != nil {
		return nil, err
	}
	task.Status = agentpkg.TeamResponseStatusCancelled
	task.ResultText = "Task canceled."
	return task, nil
}

type fakeRunner struct {
	text string
}

func (f *fakeRunner) Run(_ context.Context, _ string, _ ...agentpkg.RunOption) (*agentpkg.ExecutionResult, error) {
	return &agentpkg.ExecutionResult{FinalResult: f.text}, nil
}

func (f *fakeRunner) RunStream(_ context.Context, _ string) (<-chan *agentpkg.Event, error) {
	ch := make(chan *agentpkg.Event, 2)
	ch <- &agentpkg.Event{Type: agentpkg.EventTypePartial, Content: f.text}
	ch <- &agentpkg.Event{Type: agentpkg.EventTypeComplete, Content: f.text}
	close(ch)
	return ch, nil
}

type richFakeRunner struct{}

func (richFakeRunner) Run(_ context.Context, _ string, _ ...agentpkg.RunOption) (*agentpkg.ExecutionResult, error) {
	return &agentpkg.ExecutionResult{FinalResult: "done"}, nil
}

func (richFakeRunner) RunStream(_ context.Context, _ string) (<-chan *agentpkg.Event, error) {
	ch := make(chan *agentpkg.Event, 4)
	ch <- &agentpkg.Event{Type: agentpkg.EventTypeThinking, Content: "thinking"}
	ch <- &agentpkg.Event{Type: agentpkg.EventTypeToolCall, ToolName: "search", ToolArgs: map[string]interface{}{"q": "a2a"}}
	ch <- &agentpkg.Event{Type: agentpkg.EventTypePartial, Content: "partial output"}
	ch <- &agentpkg.Event{Type: agentpkg.EventTypeComplete, Content: "final output"}
	close(ch)
	return ch, nil
}

func TestServerListsOnlyStandaloneOptedInAgents(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		agents: map[string]*agentpkg.AgentModel{
			"Assistant": {Name: "Assistant", EnableA2A: true},
			"Writer":    {Name: "Writer", EnableA2A: true},
			"Captain":   {Name: "Captain", EnableA2A: true, Teams: []agentpkg.TeamMembership{{TeamID: "s1"}}},
			"Hidden":    {Name: "Hidden", EnableA2A: false},
		},
	}, Config{Enabled: true, PublicBaseURL: "https://agentgo.example", PathPrefix: "/a2a", IncludeBuiltInAgents: true, IncludeCustomAgents: true})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/a2a/agents", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if len(body.Agents) != 2 {
		t.Fatalf("expected 2 exposed agents, got %+v", body.Agents)
	}
}

func TestServerBuildsAgentCard(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		agents: map[string]*agentpkg.AgentModel{
			"Writer": {Name: "Writer", Description: "Writes concise docs", EnableA2A: true},
		},
	}, Config{Enabled: true, PublicBaseURL: "https://agentgo.example", PathPrefix: "/a2a", IncludeBuiltInAgents: true, IncludeCustomAgents: true, AgentVersion: "vtest"})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/a2a/agents/Writer/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var card map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("unmarshal card failed: %v", err)
	}
	if card["url"] != "https://agentgo.example/a2a/agents/Writer/invoke" {
		t.Fatalf("card url = %v", card["url"])
	}
}

func TestServerInvokesA2AAgent(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		agents: map[string]*agentpkg.AgentModel{
			"Writer": {Name: "Writer", EnableA2A: true},
		},
		runners: map[string]AgentRunner{
			"Writer": &fakeRunner{text: "hello from agent"},
		},
	}, Config{Enabled: true, PublicBaseURL: "https://agentgo.example", PathPrefix: "/a2a", IncludeBuiltInAgents: true, IncludeCustomAgents: true})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"messageId": "m1",
				"role":      "user",
				"parts": []map[string]any{
					{"kind": "text", "text": "hello"},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/a2a/agents/Writer/invoke", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("hello from agent")) {
		t.Fatalf("expected invoke response to contain agent output, got %s", rec.Body.String())
	}
}

func TestServerMapsAgentRuntimeEventsIntoArtifacts(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		agents: map[string]*agentpkg.AgentModel{
			"Writer": {Name: "Writer", A2AID: "agent-uuid-2", EnableA2A: true},
		},
		runners: map[string]AgentRunner{
			"Writer": richFakeRunner{},
		},
	}, Config{Enabled: true, PublicBaseURL: "https://agentgo.example", PathPrefix: "/a2a", IncludeBuiltInAgents: true, IncludeCustomAgents: true})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"messageId": "m1",
				"role":      "user",
				"parts": []map[string]any{
					{"kind": "text", "text": "hello"},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/a2a/agents/agent-uuid-2/invoke", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "partial output") {
		t.Fatalf("expected partial output in task result, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"event_type\":\"tool_call\"") {
		t.Fatalf("expected tool_call event to be serialized, got %s", rec.Body.String())
	}
}

func TestServerBuildsTeamCard(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		teams: map[string]*agentpkg.Team{
			"Docs Team": {ID: "s1", Name: "Docs Team", Description: "Coordinates docs work", EnableA2A: true},
		},
	}, Config{Enabled: true, PublicBaseURL: "https://agentgo.example", PathPrefix: "/a2a", IncludeBuiltInAgents: true, IncludeCustomAgents: true, AgentVersion: "vtest"})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/a2a/teams/Docs%20Team/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Docs Team") {
		t.Fatalf("expected team card body, got %s", rec.Body.String())
	}
}

func TestServerInvokesA2ATeamViaCaptain(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		agents: map[string]*agentpkg.AgentModel{
			"Docs Team Captain": {Name: "Docs Team Captain"},
		},
		teams: map[string]*agentpkg.Team{
			"Docs Team": {ID: "s1", Name: "Docs Team", Description: "Coordinates docs work", EnableA2A: true},
		},
		runners: map[string]AgentRunner{
			"Docs Team Captain": &fakeRunner{text: "team response"},
		},
	}, Config{Enabled: true, PublicBaseURL: "https://agentgo.example", PathPrefix: "/a2a", IncludeBuiltInAgents: true, IncludeCustomAgents: true})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"messageId": "m1",
				"role":      "user",
				"parts": []map[string]any{
					{"kind": "text", "text": "summarize docs"},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/a2a/teams/Docs%20Team/invoke", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("team response")) {
		t.Fatalf("expected invoke response to contain team output, got %s", rec.Body.String())
	}
}
