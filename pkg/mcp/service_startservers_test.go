package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One broken server entry — a stale binary path in a config file — must not
// keep the other servers from starting. The in-process websearch builtin has
// no binary to go stale; it must come up even when a stdio sibling cannot.
func TestStartServersContinuesPastABrokenServer(t *testing.T) {
	// Through the same door a real deployment uses: a servers JSON file with
	// one entry whose binary does not exist. (NewService reloads the config
	// from JSON, so servers added to LoadedServers beforehand are wiped.)
	serversFile := filepath.Join(t.TempDir(), "mcpServers.json")
	if err := os.WriteFile(serversFile, []byte(`{"mcpServers":{"broken":{"command":"/nonexistent/path/to/a/binary-9.9.9"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Servers = []string{serversFile}

	svc, err := NewService(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopServer("websearch")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	startErr := svc.StartServers(ctx, nil)

	if startErr == nil {
		t.Fatal("expected an error naming the broken server")
	}
	if !strings.Contains(startErr.Error(), "broken") {
		t.Fatalf("error does not name the broken server: %v", startErr)
	}

	// The builtin websearch must be running regardless.
	found := false
	for _, tool := range svc.GetAvailableTools(ctx) {
		if tool.ServerName == "websearch" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("builtin websearch not started after sibling failure: %v", startErr)
	}
}
