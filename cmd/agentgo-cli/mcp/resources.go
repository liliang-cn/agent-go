package mcp

import (
	"path/filepath"

	agentmcp "github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/resource"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func saveMCPResource(serverName, configPath string, cfg serverConfig) {
	if Cfg == nil || serverName == "" || configPath == "" {
		return
	}
	db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
	if err != nil {
		return
	}
	defer db.Close()
	_ = db.SaveResource(resource.Resource{
		ID:       "mcp:" + serverName,
		Kind:     resource.KindMCP,
		Name:     serverName,
		Provider: configPath,
		Metadata: map[string]any{
			"config_path":   configPath,
			"base_name":     filepath.Base(configPath),
			"server_config": mcpResourceServerConfig(serverName, cfg),
		},
	})
}

func mcpResourceServerConfig(serverName string, cfg serverConfig) map[string]any {
	serverType := cfg.Type
	if serverType == "" {
		serverType = string(agentmcp.ServerTypeStdio)
		if cfg.URL != "" {
			serverType = string(agentmcp.ServerTypeHTTP)
		}
	}

	command := []string{}
	if cfg.Command != "" {
		command = []string{cfg.Command}
	}

	return map[string]any{
		"name":               serverName,
		"type":               serverType,
		"command":            command,
		"args":               cfg.Args,
		"url":                cfg.URL,
		"headers":            cfg.Headers,
		"working_dir":        cfg.WorkingDir,
		"env":                cfg.Env,
		"auto_start":         true,
		"restart_on_failure": true,
	}
}

func deleteMCPResource(serverName string) {
	if Cfg == nil || serverName == "" {
		return
	}
	db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
	if err != nil {
		return
	}
	defer db.Close()
	_ = db.DeleteResource("mcp:" + serverName)
}
