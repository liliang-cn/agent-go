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
	squads  map[string]*agentpkg.Squad
	runners map[string]AgentRunner
	tasks   map[string]*agentpkg.AsyncTask
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

func (f *fakeCatalog) ListSquads() ([]*agentpkg.Squad, error) {
	out := make([]*agentpkg.Squad, 0, len(f.squads))
	for _, squad := range f.squads {
		out = append(out, squad)
	}
	return out, nil
}

func (f *fakeCatalog) GetSquadByName(name string) (*agentpkg.Squad, error) {
	if squad, ok := f.squads[name]; ok {
		return squad, nil
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) GetSquadByA2AID(a2aID string) (*agentpkg.Squad, error) {
	for _, squad := range f.squads {
		if squad != nil && squad.A2AID == a2aID {
			return squad, nil
		}
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) GetLeadAgentForSquad(squadID string) (*agentpkg.AgentModel, error) {
	for _, squad := range f.squads {
		if squad != nil && squad.ID == squadID {
			if agent, ok := f.agents[squad.Name+" Captain"]; ok {
				return agent, nil
			}
		}
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) SubmitSquadTask(ctx context.Context, sessionID, squadID, prompt string, agentNames []string) (*agentpkg.AsyncTask, error) {
	if f.tasks == nil {
		f.tasks = make(map[string]*agentpkg.AsyncTask)
	}
	var squad *agentpkg.Squad
	for _, candidate := range f.squads {
		if candidate != nil && (candidate.ID == squadID || candidate.A2AID == squadID || candidate.Name == squadID) {
			squad = candidate
			break
		}
	}
	if squad == nil {
		return nil, http.ErrMissingFile
	}
	lead, err := f.GetLeadAgentForSquad(squad.ID)
	if err != nil {
		return nil, err
	}
	task := &agentpkg.AsyncTask{
		ID:          "task-" + squad.ID,
		SessionID:   sessionID,
		Kind:        agentpkg.AsyncTaskKindSquad,
		Status:      agentpkg.AsyncTaskStatusCompleted,
		SquadID:     squad.ID,
		SquadName:   squad.Name,
		CaptainName: lead.Name,
		Prompt:      prompt,
		AckMessage:  "accepted",
		ResultText:  "squad response",
	}
	f.tasks[task.ID] = task
	return task, nil
}

func (f *fakeCatalog) GetTask(taskID string) (*agentpkg.AsyncTask, error) {
	if task, ok := f.tasks[taskID]; ok {
		return task, nil
	}
	return nil, http.ErrMissingFile
}

func (f *fakeCatalog) SubscribeTask(taskID string) (<-chan *agentpkg.TaskEvent, func(), error) {
	task, err := f.GetTask(taskID)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan *agentpkg.TaskEvent, 4)
	ch <- &agentpkg.TaskEvent{TaskID: task.ID, Type: agentpkg.TaskEventTypeStarted, Message: "started"}
	ch <- &agentpkg.TaskEvent{TaskID: task.ID, Type: agentpkg.TaskEventTypeRuntime, Runtime: &agentpkg.Event{Type: agentpkg.EventTypePartial, Content: "partial squad output"}}
	ch <- &agentpkg.TaskEvent{TaskID: task.ID, Type: agentpkg.TaskEventTypeCompleted, Message: task.ResultText}
	close(ch)
	return ch, func() {}, nil
}

func (f *fakeCatalog) CancelTask(ctx context.Context, taskID string) (*agentpkg.AsyncTask, error) {
	task, err := f.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	task.Status = agentpkg.AsyncTaskStatusCancelled
	task.ResultText = "Task canceled."
	return task, nil
}

func (f *fakeCatalog) ListTasks(limit int) []*agentpkg.AsyncTask {
	out := make([]*agentpkg.AsyncTask, 0, len(f.tasks))
	for _, task := range f.tasks {
		out = append(out, task)
	}
	return out
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
			"Captain":   {Name: "Captain", EnableA2A: true, Squads: []agentpkg.SquadMembership{{SquadID: "s1"}}},
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

func TestServerBuildsSquadCard(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		squads: map[string]*agentpkg.Squad{
			"Docs Squad": {ID: "s1", Name: "Docs Squad", Description: "Coordinates docs work", EnableA2A: true},
		},
	}, Config{Enabled: true, PublicBaseURL: "https://agentgo.example", PathPrefix: "/a2a", IncludeBuiltInAgents: true, IncludeCustomAgents: true, AgentVersion: "vtest"})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/a2a/squads/Docs%20Squad/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Docs Squad") {
		t.Fatalf("expected squad card body, got %s", rec.Body.String())
	}
}

func TestServerInvokesA2ASquadViaCaptain(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		agents: map[string]*agentpkg.AgentModel{
			"Docs Squad Captain": {Name: "Docs Squad Captain"},
		},
		squads: map[string]*agentpkg.Squad{
			"Docs Squad": {ID: "s1", Name: "Docs Squad", Description: "Coordinates docs work", EnableA2A: true},
		},
		runners: map[string]AgentRunner{
			"Docs Squad Captain": &fakeRunner{text: "squad response"},
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
	req := httptest.NewRequest(http.MethodPost, "/a2a/squads/Docs%20Squad/invoke", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("squad response")) {
		t.Fatalf("expected invoke response to contain squad output, got %s", rec.Body.String())
	}
}
