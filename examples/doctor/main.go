// Package main checks an AgentGo home before the first Ask can fail on it.
//
// v3 has no CLI, so a config-driven install — AGENTGO_HOME with providers in
// data/agentgo.db — had no way to ask "is this going to work?" short of
// building an agent and watching the first call. agent.Doctor inspects the
// home, the database, every provider, the memory store type, the MCP config
// and the skills directory without calling a model, and says what is wrong
// and how to fix it.
//
// Usage:
//
//	go run ./examples/doctor                 # the default home (~/.agentgo or AGENTGO_HOME)
//	go run ./examples/doctor -home /path     # another home
//	go run ./examples/doctor -mcp 5s         # also probe each MCP server, bounded
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	home := flag.String("home", "", "AgentGo home to check (default: AGENTGO_HOME or ~/.agentgo)")
	mcp := flag.Duration("mcp", 0, "probe each MCP server with this timeout (0 = do not connect)")
	flag.Parse()

	var opts []agent.DoctorOption
	if *home != "" {
		opts = append(opts, agent.WithDoctorHome(*home))
	}
	if *mcp > 0 {
		opts = append(opts, agent.WithMCPProbe(*mcp))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	report, err := agent.Doctor(ctx, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "doctor:", err)
		os.Exit(2)
	}
	fmt.Println(report.Summary())
	if !report.Healthy() {
		os.Exit(1)
	}
}
