package executors

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/stretchr/testify/assert"
)

// Test that all executors can be created
func TestCreateAllExecutors(t *testing.T) {
	cfg := &config.Config{}

	// Create all executors
	queryExec := NewQueryExecutor(cfg)
	assert.NotNil(t, queryExec)

	ingestExec := NewIngestExecutor(cfg)
	assert.NotNil(t, ingestExec)

	mcpExec := NewMCPExecutor(cfg)
	assert.NotNil(t, mcpExec)

	scriptExec := NewScriptExecutor(cfg)
	assert.NotNil(t, scriptExec)
}

// Test executor types
func TestExecutorTypes(t *testing.T) {
	cfg := &config.Config{}

	// Test Query executor
	queryExec := NewQueryExecutor(cfg)
	assert.Equal(t, "query", string(queryExec.Type()))

	// Test Ingest executor
	ingestExec := NewIngestExecutor(cfg)
	assert.Equal(t, "ingest", string(ingestExec.Type()))

	// Test MCP executor
	mcpExec := NewMCPExecutor(cfg)
	assert.Equal(t, "mcp", string(mcpExec.Type()))

	// Test Script executor
	scriptExec := NewScriptExecutor(cfg)
	assert.Equal(t, "script", string(scriptExec.Type()))
}
