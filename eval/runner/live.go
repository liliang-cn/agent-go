package runner

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/poolsvc"
)

// BuildPoolLiveBuilder returns a LiveBuilder that constructs each scenario's
// Service from the caller's own agentgo configuration (agentgo.toml plus the
// SQLite store) via the process-global provider pool.
//
// This is what makes `mode: live` scenarios run against a real model. It fails
// fast when no provider is configured, so a live run never silently degrades
// into "the model answered nothing".
func BuildPoolLiveBuilder() (LiveBuilder, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("no agentgo config found; configure an LLM provider before running live scenarios")
	}
	if err := poolsvc.Global().Initialize(context.Background(), cfg); err != nil {
		return nil, fmt.Errorf("initialize provider pool: %w", err)
	}
	llm, err := poolsvc.Global().GetLLMService()
	if err != nil {
		return nil, fmt.Errorf("get LLM service: %w", err)
	}

	return func(scenarioName, agentName string, lints []string, home string) (*agent.Service, error) {
		evalCfg := &config.Config{
			Home: home,
			LLM:  cfg.LLM,
			RAG:  cfg.RAG,
			Memory: config.MemoryConfig{
				StoreType:  "file",
				MemoryPath: filepath.Join(home, "data", "memories"),
			},
		}
		// RAG stays off in eval: it injects external context that makes
		// behavioral runs noisy and provider-dependent.
		evalCfg.RAG.Enabled = false
		evalCfg.ApplyHomeLayout()

		// Build through a Manager so a live scenario exercises the same wiring
		// a real embedder gets — persisted agent definition, MCP + skills
		// registration, checkpoint sink — instead of only the LLM glue layer.
		store, err := agent.NewStore(filepath.Join(home, "data", "agentgo.db"))
		if err != nil {
			return nil, fmt.Errorf("build eval store: %w", err)
		}
		mgr := agent.NewManager(store)
		mgr.SetConfig(evalCfg)
		mgr.SetLLM(llm)
		if err := mgr.SeedDefaultAgent(); err != nil {
			return nil, fmt.Errorf("seed default agent: %w", err)
		}
		svc, err := mgr.Service(agentName)
		if err != nil {
			return nil, fmt.Errorf("get agent service %q: %w", agentName, err)
		}
		// Live runs always wire the default lint set so the agent plays by the
		// same harness rules production code does. Scenario authors who need a
		// different set register on top after the build.
		agent.RegisterDefaultOutputLints(svc)
		return svc, nil
	}, nil
}
