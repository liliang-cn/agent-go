package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/liliang-cn/agent-go/pkg/acpserver"
	"github.com/liliang-cn/agent-go/pkg/agent"
	"github.com/liliang-cn/agent-go/pkg/config"
	agentgomcp "github.com/liliang-cn/agent-go/pkg/mcp"
	"github.com/spf13/cobra"
)

var (
	Cfg     *config.Config
	Verbose bool

	acpAgentName string
	acpWithPTC   bool
	acpNoMemory  bool
)

// SetSharedVariables sets shared variables from root command.
func SetSharedVariables(cfg *config.Config, verbose bool) {
	Cfg = cfg
	Verbose = verbose
}

// Cmd is the parent command for ACP operations.
var Cmd = &cobra.Command{
	Use:   "acp",
	Short: "ACP (Agent Client Protocol) integration",
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve AgentGo over ACP stdio",
	Long: `Expose AgentGo as an ACP-compatible agent over stdin/stdout.

This command is intended to be launched by ACP-capable editors and clients.`,
	RunE: runServe,
}

func init() {
	Cmd.AddCommand(serveCmd)

	serveCmd.Flags().StringVar(&acpAgentName, "name", "AgentGo ACP", "agent display name")
	serveCmd.Flags().BoolVar(&acpWithPTC, "with-ptc", false, "enable Programmatic Tool Calling (JS sandbox)")
	serveCmd.Flags().BoolVar(&acpNoMemory, "no-memory", false, "disable long-term memory for ACP sessions")
}

func runServe(cmd *cobra.Command, args []string) error {
	if Cfg == nil {
		return fmt.Errorf("ACP serve requires loaded configuration")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{}))

	server := acpserver.New(func(ctx context.Context, sessionCfg acpserver.SessionConfig) (acpserver.SessionRuntime, error) {
		sessionConfig, cleanup, err := prepareSessionConfig(Cfg, sessionCfg)
		if err != nil {
			return nil, err
		}

		builder := agent.New(acpAgentName).
			WithConfig(sessionConfig).
			WithSystemPrompt("You are AgentGo speaking ACP. Use available tools to complete tasks and stream clear progress back to the client.").
			WithDBPath(sessionConfig.AgentDBPath()).
			WithMCP().
			WithSkills().
			WithRouter()

		if !acpNoMemory {
			builder.WithMemory()
		}
		if acpWithPTC {
			builder.WithPTC()
		}

		svc, err := builder.Build()
		if err != nil {
			_ = cleanup()
			return nil, err
		}
		svc.SetSessionID(sessionCfg.SessionID)
		return &sessionRuntime{
			SessionRuntime: svc,
			cleanup:        cleanup,
		}, nil
	}, logger)
	defer server.Close()

	conn := acpsdk.NewAgentSideConnection(server, os.Stdout, os.Stdin)
	conn.SetLogger(logger)
	server.SetAgentConnection(conn)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	select {
	case <-sigCtx.Done():
		return nil
	case <-conn.Done():
		return nil
	}
}

type sessionRuntime struct {
	acpserver.SessionRuntime
	cleanup func() error
}

func (s *sessionRuntime) Close() error {
	var firstErr error
	if err := s.SessionRuntime.Close(); err != nil {
		firstErr = err
	}
	if s.cleanup != nil {
		if err := s.cleanup(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func prepareSessionConfig(base *config.Config, sessionCfg acpserver.SessionConfig) (*config.Config, func() error, error) {
	// Deep copy via JSON to avoid copying mutex from embedded mcp.Config
	data, err := json.Marshal(base)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal config: %w", err)
	}
	var cfgCopy config.Config
	if err := json.Unmarshal(data, &cfgCopy); err != nil {
		return nil, nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Reset MCP servers to avoid sharing mutable state
	cfgCopy.MCP.Servers = append([]string{}, base.MCP.Servers...)
	cfgCopy.MCP.LoadedServers = append([]agentgomcp.ServerConfig{}, base.MCP.LoadedServers...)

	if len(sessionCfg.MCPServers) == 0 {
		return &cfgCopy, func() error { return nil }, nil
	}

	serverDefs, err := convertACPMCPServers(sessionCfg.MCPServers)
	if err != nil {
		return nil, nil, err
	}

	if err := os.MkdirAll(filepath.Join(cfgCopy.DataDir(), "acp"), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create ACP data dir: %w", err)
	}

	tmpFile := filepath.Join(cfgCopy.DataDir(), "acp", fmt.Sprintf("mcp-%s.json", sessionCfg.SessionID))
	payload := agentgomcp.JSONServersConfig{MCPServers: serverDefs}
	data, err = json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal ACP MCP server config: %w", err)
	}
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return nil, nil, fmt.Errorf("write ACP MCP server config: %w", err)
	}

	cfgCopy.MCP.Enabled = true
	cfgCopy.MCP.Servers = append(cfgCopy.MCP.Servers, tmpFile)

	return &cfgCopy, func() error { return os.Remove(tmpFile) }, nil
}

func convertACPMCPServers(servers []acpsdk.McpServer) (map[string]agentgomcp.SimpleServerConfig, error) {
	out := make(map[string]agentgomcp.SimpleServerConfig, len(servers))
	for _, server := range servers {
		switch {
		case server.Stdio != nil:
			if server.Stdio.Name == "" {
				return nil, fmt.Errorf("ACP stdio MCP server missing name")
			}
			env := make(map[string]string, len(server.Stdio.Env))
			for _, item := range server.Stdio.Env {
				env[item.Name] = item.Value
			}
			out[server.Stdio.Name] = agentgomcp.SimpleServerConfig{
				Type:    "stdio",
				Command: server.Stdio.Command,
				Args:    append([]string{}, server.Stdio.Args...),
				Env:     env,
			}
		case server.Http != nil:
			if server.Http.Name == "" {
				return nil, fmt.Errorf("ACP HTTP MCP server missing name")
			}
			headers := make(map[string]string, len(server.Http.Headers))
			for _, item := range server.Http.Headers {
				headers[item.Name] = item.Value
			}
			out[server.Http.Name] = agentgomcp.SimpleServerConfig{
				Type:    "http",
				URL:     server.Http.Url,
				Headers: headers,
			}
		case server.Sse != nil:
			if server.Sse.Name == "" {
				return nil, fmt.Errorf("ACP SSE MCP server missing name")
			}
			headers := make(map[string]string, len(server.Sse.Headers))
			for _, item := range server.Sse.Headers {
				headers[item.Name] = item.Value
			}
			out[server.Sse.Name] = agentgomcp.SimpleServerConfig{
				Type:    "sse",
				URL:     server.Sse.Url,
				Headers: headers,
			}
		default:
			return nil, fmt.Errorf("unsupported ACP MCP server definition")
		}
	}
	return out, nil
}
