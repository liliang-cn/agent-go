package mcp

import (
	"testing"
	"time"
)

// A server that never answers must not hold up the caller, and Close must
// leave nothing connected behind it.
func TestStartServersInBackgroundDoesNotWait(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Servers = nil
	cfg.AddServer(&ServerConfig{
		Name: "silent", Type: ServerTypeStdio,
		Command: []string{"sleep"}, Args: []string{"30"}, AutoStart: true,
	})
	svc := &Service{manager: NewManager(&cfg), mcpConfig: &cfg, conversations: map[string]*Conversation{}}

	start := time.Now()
	svc.StartServersInBackground(nil)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("StartServersInBackground waited %s", d)
	}
	time.Sleep(100 * time.Millisecond)
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(svc.manager.ListClients()); n != 0 {
		t.Fatalf("%d clients left after Close", n)
	}
}
