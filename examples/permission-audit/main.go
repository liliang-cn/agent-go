// Who approved this destructive call?
//
// A permission handler decides whether a tool call may run. Before
// PermissionObserver the decision was made and forgotten: nothing reached an
// observer or a trace. This example gates a destructive tool behind an
// approver that names itself, refuses one path, and writes every decision
// three ways: a PermissionObserver of its own (an audit log), TraceWriter's
// "event":"permission" lines, and ActivityLog's "perm" lines.
//
//	go run ./examples/permission-audit
//	go run ./examples/permission-audit -trace run.jsonl
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// auditLog is the host's own record: one line per decision that needed one.
type auditLog struct{ agent.BaseObserver }

func (auditLog) OnPermissionDecision(_ context.Context, d agent.PermissionDecisionInfo) {
	if !d.Required {
		return
	}
	fmt.Printf("AUDIT run=%s call=%s tool=%s args=%v decision=%s by=%s reason=%q waited=%s\n",
		d.RunID, d.CallID, d.Tool, d.Args, d.Decision, d.DecidedBy, d.Reason, d.Duration)
}

type deleteParams struct {
	Path string `json:"path" desc:"the file to delete"`
}

func main() {
	tracePath := flag.String("trace", "", "also write a JSONL trace here")
	flag.Parse()

	builder := agent.New("permission-audit").
		WithSystemPrompt("You tidy a workspace. Use delete_file for each file the user names.").
		WithTool(agent.NewTool("delete_file", "Delete a file from the workspace",
			func(_ context.Context, p *deleteParams) (any, error) {
				// A demo: nothing is really deleted.
				return map[string]any{"deleted": p.Path}, nil
			}).WithDestructive(true)).
		WithObserver(auditLog{}).
		WithObserver(agent.NewActivityLog(os.Stderr))
	if *tracePath != "" {
		f, err := os.Create(*tracePath)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		builder = builder.WithObserver(agent.NewTraceWriter(f))
	}
	svc, err := builder.Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	svc.SetPermissionPolicy(agent.DefaultPermissionPolicy)
	svc.SetPermissionHandler(func(_ context.Context, req agent.PermissionRequest) (*agent.PermissionResponse, error) {
		// A real host asks a person here. DecidedBy is what makes the record
		// answer "who", not only "what".
		path, _ := req.ToolArgs["path"].(string)
		if strings.Contains(path, "keep") {
			return &agent.PermissionResponse{Allowed: false, Reason: "files marked keep stay", DecidedBy: "policy-bot"}, nil
		}
		return &agent.PermissionResponse{Allowed: true, Reason: "scratch file", DecidedBy: "policy-bot"}, nil
	})

	res, err := svc.Run(context.Background(), "Delete tmp1.txt, then delete keep-notes.md.")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("final:", res.Text())
}
