// Package main shows the uniform cap on tool output.
//
// Every tool result — built-in, MCP, skill or sub-agent — is capped on its
// way into the conversation (agent.DefaultToolOutputLimit bytes unless
// WithToolOutputLimit says otherwise). The model receives the head and the
// tail plus a line saying how much was cut and how to get the rest; fs_read
// pages by default and names the next offset. Every cut is reported to
// observers implementing agent.ToolOutputObserver — ActivityLog prints a
// "truncate" line for it.
//
// Usage (uses the provider configured in AGENTGO_HOME / agentgo.db):
//
//	go run ./examples/tool-output-limit
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// A workspace holding a 10,000-line log with one error in the middle.
	ws, err := os.MkdirTemp("", "tool-output-limit-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(ws)
	var b strings.Builder
	for i := 1; i <= 10000; i++ {
		if i == 6123 {
			fmt.Fprintf(&b, "line %d ERROR db: connection pool exhausted (max=4)\n", i)
			continue
		}
		fmt.Fprintf(&b, "line %d INFO request handled status=200\n", i)
	}
	if err := os.WriteFile(filepath.Join(ws, "app.log"), []byte(b.String()), 0o644); err != nil {
		log.Fatal(err)
	}

	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(ws))
	if err != nil {
		log.Fatal(err)
	}
	defer sb.Close()

	svc, err := agent.New("log-reader").
		WithSandbox(sb).
		// 16KB per tool result instead of the default 32KB; -1 turns the cap off.
		WithToolOutputLimit(16000).
		WithObserver(agent.NewActivityLog(os.Stderr)).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	res, err := svc.Run(ctx, "app.log in the workspace contains one ERROR line. Find it and quote it.")
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	fmt.Println(res.Text())
}
