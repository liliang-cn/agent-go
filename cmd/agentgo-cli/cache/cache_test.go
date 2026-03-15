package cache

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/pkg/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()

	home := t.TempDir()
	cfg := &config.Config{
		Home: home,
		Cache: config.CacheConfig{
			StoreType: "file",
			Path:      filepath.Join(home, "data", "cache"),
			MaxSize:   10,
		},
	}
	cfg.Cache.Path = filepath.Join(home, "data", "cache")
	return cfg
}

func executeCommand(t *testing.T, cfg *config.Config, args ...string) (string, error) {
	t.Helper()

	SetSharedVariables(cfg, false)
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return out.String(), err
}

func TestCacheCommandPutGetStatusAndClear(t *testing.T) {
	cfg := testConfig(t)

	if out, err := executeCommand(t, cfg, "put", "query", "demo-key", "demo-value"); err != nil {
		t.Fatalf("put failed: %v (%s)", err, out)
	}

	getOut, err := executeCommand(t, cfg, "get", "query", "demo-key")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !strings.Contains(getOut, "demo-value") {
		t.Fatalf("expected stored value in output, got %q", getOut)
	}

	statusOut, err := executeCommand(t, cfg, "status")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if !strings.Contains(statusOut, "Store: file") || !strings.Contains(statusOut, "query: size=1") {
		t.Fatalf("unexpected status output: %q", statusOut)
	}

	if out, err := executeCommand(t, cfg, "clear", "query"); err != nil {
		t.Fatalf("clear failed: %v (%s)", err, out)
	}

	if _, err := executeCommand(t, cfg, "get", "query", "demo-key"); err == nil {
		t.Fatal("expected miss after clear")
	}
}

func TestCacheCommandRejectsUnknownNamespace(t *testing.T) {
	cfg := testConfig(t)

	if _, err := executeCommand(t, cfg, "put", "unknown", "k", "v"); err == nil {
		t.Fatal("expected unknown namespace error")
	}
}
