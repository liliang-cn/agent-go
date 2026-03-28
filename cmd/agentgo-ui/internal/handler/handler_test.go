package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		Home:  home,
		Debug: true,
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
		},
		RAG: config.RAGConfig{
			Enabled: true,
		},
		Memory: config.MemoryConfig{
			StoreType: "file",
		},
	}
	cfg.ApplyHomeLayout()
	return cfg
}

func newTestManager(t *testing.T) *agent.TeamManager {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	store, err := agent.NewStore(dbPath)
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := agent.NewTeamManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed agents failed: %v", err)
	}
	return manager
}

func TestConfigHandlerGetAndPut(t *testing.T) {
	cfg := testConfig(t)
	configPath := filepath.Join(t.TempDir(), "config", "agentgo.toml")
	handler := NewConfigHandler(cfg, configPath)

	// Test GET
	getReq := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	getRec := httptest.NewRecorder()
	handler.HandleConfig(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("unexpected get status: %d", getRec.Code)
	}

	var getResp ConfigSnapshot
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode get response failed: %v", err)
	}
	if getResp.Home != cfg.Home || getResp.ServerPort != cfg.Server.Port {
		t.Fatalf("unexpected get snapshot: %+v", getResp)
	}

	// Test PUT
	enabled := true
	port := 9000
	reqBody := UpdateConfigRequest{
		Debug:      boolPtr(false),
		ServerPort: &port,
		RAGEnabled: &enabled,
	}
	body, _ := json.Marshal(reqBody)
	putReq := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	putRec := httptest.NewRecorder()
	handler.HandleConfig(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("unexpected put status: %d body=%s", putRec.Code, putRec.Body.String())
	}
	if cfg.Server.Port != 9000 {
		t.Fatalf("expected config mutation, got %+v", cfg)
	}
}

func TestHandleAgentsAndOperations(t *testing.T) {
	cfg := testConfig(t)
	manager := newTestManager(t)
	h := &Handler{cfg: cfg, teamManager: manager, aiChatSessions: map[string]string{}, opsLogs: []OpsLogEntry{}}

	t.Run("list agents", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
		h.HandleAgents(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
	})

	t.Run("create agent", func(t *testing.T) {
		body := []byte(`{"name":"Writer","description":"Writes","instructions":"Write clearly","enable_mcp":true,"required_llm_capability":4}`)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
		h.HandleAgents(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
	})
}

func TestJSONHelpersAndServeHTTP(t *testing.T) {
	rec := httptest.NewRecorder()
	JSONResponse(rec, map[string]string{"ok": "yes"})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected json response metadata")
	}
}

func boolPtr(v bool) *bool       { return &v }
func intPtr(v int) *int          { return &v }
func stringPtr(v string) *string { return &v }

func TestDequeueToolCallID(t *testing.T) {
	queue := map[string][]string{
		"read_file": {"call-1", "call-2"},
	}

	if got := dequeueToolCallID(queue, "read_file"); got != "call-1" {
		t.Fatalf("unexpected first call id: %s", got)
	}
}
