// Handing a task to another agent CLI.
//
// The most capable agent runtime on a developer's machine is often not the one
// you are writing: claude, codex, gemini and cursor-agent are all whole agents
// with their own tools and their own subscriptions. These two tools let an
// agent-go agent give one of them a job and get the answer back.
//
//	go run ./examples/cli-agents
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agentexec"
)

func main() {
	// Discovery on its own needs no service and no model. Note what it does
	// and does not tell you: these binaries exist. Whether the account behind
	// one is still logged in is only knowable by running it, so nothing here
	// pretends to know.
	found := agentexec.Discover(nil)
	if len(found) == 0 {
		fmt.Println("no agent CLIs on PATH — install one of claude, codex, gemini, cursor-agent")
		return
	}
	for _, a := range found {
		fmt.Printf("- %-13s %s %s\n", a.Name, a.Binary, a.Version)
	}

	svc, err := agent.New("assistant").
		WithSystemPrompt("You answer briefly. When a task is better done by another agent CLI, delegate it.").
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	// The directory the delegated agent is allowed to work in. It runs with
	// its approval prompts turned off, so this bound is the only thing
	// standing between "read this" and "and while you are there, edit that".
	workdir, err := os.MkdirTemp("", "cli-agent-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(workdir)

	// Off by default, like the background tools and for the same reason: a
	// delegated call is a whole agent run billed to somebody's subscription,
	// and an agent that can start one without its author deciding so can spend
	// money in a loop.
	if err := agent.RegisterCLIAgentTools(svc, agent.CLIAgentConfig{
		AllowedRoots:   []string{workdir},
		DefaultTimeout: 3 * time.Minute,
	}); err != nil {
		log.Fatal(err)
	}

	// An observer is how the delegated spend gets accounted separately: the
	// sub-agent bracket fires with Kind "cli", and the end carries the other
	// CLI's own token counts, which are not in this run's usage.
	svc.RegisterObserver(&billing{})

	res, err := svc.Run(context.Background(),
		"Use cli_agent_list to see which agent CLIs are installed, then ask one of them "+
			"to reply with exactly the word OK. Report what it said, and say plainly if it failed.")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n" + res.Text())
}

type billing struct{ agent.BaseObserver }

func (billing) OnSubAgentEnd(_ context.Context, info agent.SubAgentInfo, result any, err error) {
	if info.Kind != "cli" {
		return
	}
	out, ok := result.(agent.CLIAgentRunResult)
	if !ok {
		return
	}
	fmt.Printf("\n[%s] %d in / %d out / %d cached, $%.4f, %dms, failed=%v err=%v\n",
		info.Provider, out.Input, out.Output, out.Cache, out.CostUSD, out.Duration, out.Failed, err)
}
