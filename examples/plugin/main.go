// Package main loads the demo plugin next to this file onto an agent.
//
// A plugin is a directory with a plugin.json plus any of skills/, mcp.json,
// agents/*.md, prompt.md and extension.json. The last one starts a
// subprocess, so it is skipped unless the host passes plugin.AllowExec.
//
// Usage:
//
//	go run ./examples/plugin
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/plugin"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b := agent.New("assistant")
	if err := plugin.Install(b, plugin.Dirs("examples/plugin/demo")); err != nil {
		log.Fatal(err)
	}
	svc, err := b.Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	for _, p := range svc.Plugins() {
		fmt.Printf("plugin %s %s: %v skipped=%v\n", p.Name, p.Version, p.Components, p.Skipped)
	}
	reply, err := svc.Ask(ctx, "Who are you? One sentence.")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(reply)
}
