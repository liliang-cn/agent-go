package a2a

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	a2aproto "github.com/a2aproject/a2a-go/a2a"
	agentpkg "github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func TestExtractTextFromSendResultMessage(t *testing.T) {
	msg := a2aproto.NewMessage(a2aproto.MessageRoleAgent, a2aproto.TextPart{Text: "hello"})
	got := ExtractTextFromSendResult(msg)
	if got != "hello" {
		t.Fatalf("ExtractTextFromSendResult(message) = %q", got)
	}
}

func TestExtractTextFromTaskArtifacts(t *testing.T) {
	task := &a2aproto.Task{
		Artifacts: []*a2aproto.Artifact{
			{Parts: a2aproto.ContentParts{a2aproto.TextPart{Text: "from artifact"}}},
		},
	}
	got := ExtractTextFromTask(task)
	if got != "from artifact" {
		t.Fatalf("ExtractTextFromTask() = %q", got)
	}
}

func TestSplitCardURL(t *testing.T) {
	base, path, err := splitCardURL("http://127.0.0.1:7332/a2a/agents/123/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("splitCardURL() error = %v", err)
	}
	if base != "http://127.0.0.1:7332" {
		t.Fatalf("base = %q", base)
	}
	if !strings.Contains(path, "/a2a/agents/123/.well-known/agent-card.json") {
		t.Fatalf("path = %q", path)
	}
}

func TestConnectAndSendText(t *testing.T) {
	server, err := NewServer(&fakeCatalog{
		agents: map[string]*agentpkg.AgentModel{
			"Writer": {Name: "Writer", A2AID: "agent-uuid-1", EnableA2A: true},
		},
		runners: map[string]AgentRunner{
			"Writer": &fakeRunner{text: "writer response"},
		},
	}, Config{Enabled: true, PathPrefix: "/a2a", IncludeBuiltInAgents: true, IncludeCustomAgents: true})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	ts := httptest.NewServer(server)
	defer ts.Close()

	client, err := Connect(context.Background(), ts.URL+AgentCardPath("/a2a", "agent-uuid-1"), ClientConfig{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	text, _, err := client.SendText(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
	if text != "writer response" {
		t.Fatalf("SendText() = %q", text)
	}
}
