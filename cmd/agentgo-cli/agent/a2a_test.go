package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agenta2a "github.com/liliang-cn/agent-go/v2/pkg/a2a"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func TestAgentAddWithA2AFlagPersistsExposure(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	Cfg = cfg
	defer func() { Cfg = nil }()

	agentDescription = "Writes docs"
	agentInstructions = "Write concise docs"
	agentProvider = ""
	agentModel = ""
	agentA2AEnabled = true
	defer func() {
		agentDescription = ""
		agentInstructions = ""
		agentProvider = ""
		agentModel = ""
		agentA2AEnabled = false
	}()

	if err := agentAddCmd.RunE(agentAddCmd, []string{"Writer"}); err != nil {
		t.Fatalf("agent add failed: %v", err)
	}

	manager, err := getManager()
	if err != nil {
		t.Fatalf("getManager failed: %v", err)
	}
	model, err := manager.GetAgentByName("Writer")
	if err != nil {
		t.Fatalf("GetAgentByName failed: %v", err)
	}
	if !model.EnableA2A {
		t.Fatal("expected Writer.EnableA2A to be true")
	}
}

func TestAgentUpdateWithA2AFlagTogglesExposure(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	Cfg = cfg
	defer func() { Cfg = nil }()

	manager, err := getManager()
	if err != nil {
		t.Fatalf("getManager failed: %v", err)
	}
	created, err := manager.CreateAgent(context.Background(), &agent.AgentModel{
		Name:         "Writer",
		Kind:         agent.AgentKindAgent,
		Description:  "Writes docs",
		Instructions: "Write concise docs",
		EnableA2A:    false,
	})
	if err != nil {
		t.Fatalf("CreateAgent failed: %v", err)
	}

	agentA2AEnabled = true
	defer func() { agentA2AEnabled = false }()
	if err := agentUpdateCmd.Flags().Set("a2a", "true"); err != nil {
		t.Fatalf("set flag failed: %v", err)
	}
	defer agentUpdateCmd.Flags().Set("a2a", "false")

	if err := agentUpdateCmd.RunE(agentUpdateCmd, []string{created.Name}); err != nil {
		t.Fatalf("agent update failed: %v", err)
	}

	updated, err := manager.GetAgentByName("Writer")
	if err != nil {
		t.Fatalf("GetAgentByName failed: %v", err)
	}
	if !updated.EnableA2A {
		t.Fatal("expected Writer.EnableA2A to be true after update")
	}
}

type cliInvokeCatalog struct {
	agents  map[string]*agent.AgentModel
	teams  map[string]*agent.Team
	runners map[string]agenta2a.AgentRunner
}

func (c cliInvokeCatalog) ListAgents() ([]*agent.AgentModel, error) {
	out := make([]*agent.AgentModel, 0, len(c.agents))
	for _, model := range c.agents {
		out = append(out, model)
	}
	return out, nil
}

func (c cliInvokeCatalog) GetAgentByName(name string) (*agent.AgentModel, error) {
	if model, ok := c.agents[name]; ok {
		return model, nil
	}
	return nil, http.ErrMissingFile
}

func (c cliInvokeCatalog) GetAgentByA2AID(a2aID string) (*agent.AgentModel, error) {
	for _, model := range c.agents {
		if model != nil && model.A2AID == a2aID {
			return model, nil
		}
	}
	return nil, http.ErrMissingFile
}

func (c cliInvokeCatalog) GetAgentService(name string) (agenta2a.AgentRunner, error) {
	if runner, ok := c.runners[name]; ok {
		return runner, nil
	}
	return nil, http.ErrMissingFile
}

func (c cliInvokeCatalog) ListTeams() ([]*agent.Team, error) {
	out := make([]*agent.Team, 0, len(c.teams))
	for _, team := range c.teams {
		out = append(out, team)
	}
	return out, nil
}

func (c cliInvokeCatalog) GetTeamByName(name string) (*agent.Team, error) {
	if team, ok := c.teams[name]; ok {
		return team, nil
	}
	return nil, http.ErrMissingFile
}

func (c cliInvokeCatalog) GetTeamByA2AID(a2aID string) (*agent.Team, error) {
	for _, team := range c.teams {
		if team != nil && team.A2AID == a2aID {
			return team, nil
		}
	}
	return nil, http.ErrMissingFile
}

func (c cliInvokeCatalog) GetLeadAgentForTeam(teamID string) (*agent.AgentModel, error) {
	for _, team := range c.teams {
		if team != nil && team.ID == teamID {
			if model, ok := c.agents[team.Name+" Captain"]; ok {
				return model, nil
			}
		}
	}
	return nil, http.ErrMissingFile
}

type cliInvokeRunner struct {
	text string
}

func (r cliInvokeRunner) Run(_ context.Context, _ string, _ ...agent.RunOption) (*agent.ExecutionResult, error) {
	return &agent.ExecutionResult{FinalResult: r.text}, nil
}

func (r cliInvokeRunner) RunStream(_ context.Context, _ string) (<-chan *agent.Event, error) {
	ch := make(chan *agent.Event, 2)
	ch <- &agent.Event{Type: agent.EventTypePartial, Content: r.text}
	ch <- &agent.Event{Type: agent.EventTypeComplete, Content: r.text}
	close(ch)
	return ch, nil
}

func TestA2AServeInvokeEndToEnd(t *testing.T) {
	server, err := agenta2a.NewServer(cliInvokeCatalog{
		agents: map[string]*agent.AgentModel{
			"Writer": {
				Name:      "Writer",
				EnableA2A: true,
			},
		},
		runners: map[string]agenta2a.AgentRunner{
			"Writer": cliInvokeRunner{text: "writer response"},
		},
	}, agenta2a.Config{
		Enabled:              true,
		PublicBaseURL:        "http://127.0.0.1:7332",
		PathPrefix:           "/a2a",
		IncludeBuiltInAgents: true,
		IncludeCustomAgents:  true,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	mux := http.NewServeMux()
	server.Mount(mux)

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"messageId": "m1",
				"role":      "user",
				"parts": []map[string]any{
					{"kind": "text", "text": "write docs"},
				},
			},
		},
	}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/a2a/agents/Writer/invoke", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "writer response") {
		t.Fatalf("expected invoke response to contain agent output, got %s", rec.Body.String())
	}
}

func TestGetManagerUsesTempConfigHome(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	Cfg = cfg
	defer func() { Cfg = nil }()

	manager, err := getManager()
	if err != nil {
		t.Fatalf("getManager failed: %v", err)
	}
	model, err := manager.GetAgentByName("Assistant")
	if err != nil {
		t.Fatalf("GetAgentByName failed: %v", err)
	}
	if model == nil {
		t.Fatal("expected built-in assistant")
	}
	if got := filepath.Clean(cfg.AgentDBPath()); got == "" {
		t.Fatal("expected non-empty agent db path")
	}
}
