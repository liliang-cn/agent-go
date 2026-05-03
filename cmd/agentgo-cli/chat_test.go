package main

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func TestDelegatedResultLooksFailed(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		failed bool
	}{
		{name: "ptc error prefix", input: "Code execution failed: boom", failed: true},
		{name: "status failed marker", input: "Code execution completed.\n**Status:** Failed ❌", failed: true},
		{name: "normal success", input: "helloworld.go has been created successfully.", failed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := delegatedResultLooksFailed(tt.input); got != tt.failed {
				t.Fatalf("delegatedResultLooksFailed(%q) = %v, want %v", tt.input, got, tt.failed)
			}
		})
	}
}

func TestParseDelegatedTasks(t *testing.T) {
	isKnown := func(name string) bool {
		switch name {
		case "Responder", "Coder", "Writer":
			return true
		default:
			return false
		}
	}

	t.Run("non delegation message", func(t *testing.T) {
		tasks, err := parseDelegatedTasks("hello world", isKnown)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tasks) != 0 {
			t.Fatalf("expected no tasks, got %d", len(tasks))
		}
	})

	t.Run("single agent", func(t *testing.T) {
		tasks, err := parseDelegatedTasks("@Coder 写一个 hello world", isKnown)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tasks) != 1 {
			t.Fatalf("expected 1 task, got %d", len(tasks))
		}
		if tasks[0].AgentName != "Coder" || tasks[0].Instruction != "写一个 hello world" {
			t.Fatalf("unexpected task: %+v", tasks[0])
		}
	})

	t.Run("multiple agents", func(t *testing.T) {
		tasks, err := parseDelegatedTasks("@Responder 查一下 2024 欧冠冠军 @Coder 把上一步结果写到 result.txt", isKnown)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tasks) != 2 {
			t.Fatalf("expected 2 tasks, got %d", len(tasks))
		}
		if tasks[0].AgentName != "Responder" || tasks[0].Instruction != "查一下 2024 欧冠冠军" {
			t.Fatalf("unexpected first task: %+v", tasks[0])
		}
		if tasks[1].AgentName != "Coder" || tasks[1].Instruction != "把上一步结果写到 result.txt" {
			t.Fatalf("unexpected second task: %+v", tasks[1])
		}
	})

	t.Run("dynamic agent mention", func(t *testing.T) {
		tasks, err := parseDelegatedTasks("@Writer 根据上一步输出整理成摘要", isKnown)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tasks) != 1 || tasks[0].AgentName != "Writer" {
			t.Fatalf("unexpected tasks: %+v", tasks)
		}
	})

	t.Run("unknown first agent", func(t *testing.T) {
		_, err := parseDelegatedTasks("@Unknown 做点什么", isKnown)
		if err == nil {
			t.Fatal("expected error for unknown agent")
		}
	})

	t.Run("unknown later mention treated as text", func(t *testing.T) {
		tasks, err := parseDelegatedTasks("@Responder 调查 @Unknown 这个名字会不会被保留", isKnown)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tasks) != 1 {
			t.Fatalf("expected 1 task, got %d", len(tasks))
		}
		if tasks[0].Instruction != "调查 @Unknown 这个名字会不会被保留" {
			t.Fatalf("unexpected instruction: %q", tasks[0].Instruction)
		}
	})
}

func TestSanitizeChatDisplayText(t *testing.T) {
	got := sanitizeChatDisplayText("<think>internal reasoning</think>\n\nDone")
	if got != "Done" {
		t.Fatalf("expected think blocks to be removed, got %q", got)
	}
}

func TestPrintChatPlanSnapshot(t *testing.T) {
	plan := &agent.TaskPlan{
		ID:   "12345678-plan",
		Goal: "ship task plans",
		Items: []agent.TaskPlanItem{
			{ID: "inspect", Subject: "Inspect code", Status: agent.PlanItemStatusCompleted, OwnerAgent: "Coder"},
			{ID: "verify", Subject: "Verify behavior", Status: agent.PlanItemStatusPending, BlockedBy: []string{"inspect"}},
		},
	}

	output := captureStdout(t, func() {
		printChatPlanSnapshot(plan)
	})

	if !strings.Contains(output, "Plan 12345678: ship task plans") {
		t.Fatalf("missing plan header: %q", output)
	}
	if !strings.Contains(output, "pending:1 in_progress:0 completed:1 blocked:0 failed:0") {
		t.Fatalf("missing plan counts: %q", output)
	}
	if !strings.Contains(output, "- [completed] inspect Inspect code @Coder") {
		t.Fatalf("missing item line: %q", output)
	}
}

func TestHandleChatPlanCommandListsCurrentSessionPlans(t *testing.T) {
	store, err := agent.NewStore(t.TempDir() + "/agentgo.db")
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	manager := agent.NewTeamManager(store)
	_, err = manager.Plans().Create(context.Background(), agent.TaskPlanCreateOptions{
		SessionID: "session-a",
		Goal:      "visible plan",
		Items:     []agent.TaskPlanItem{{ID: "one", Subject: "First step"}},
	})
	if err != nil {
		t.Fatalf("Create visible plan error = %v", err)
	}
	_, err = manager.Plans().Create(context.Background(), agent.TaskPlanCreateOptions{
		SessionID: "session-b",
		Goal:      "hidden plan",
		Items:     []agent.TaskPlanItem{{ID: "two", Subject: "Second step"}},
	})
	if err != nil {
		t.Fatalf("Create hidden plan error = %v", err)
	}

	output := captureStdout(t, func() {
		handled, err := handleChatPlanCommand(context.Background(), manager, "session-a", "/plans", nil)
		if err != nil {
			t.Fatalf("handleChatPlanCommand error = %v", err)
		}
		if !handled {
			t.Fatal("expected /plans to be handled")
		}
	})

	if !strings.Contains(output, "visible plan") {
		t.Fatalf("missing visible plan: %q", output)
	}
	if strings.Contains(output, "hidden plan") {
		t.Fatalf("unexpected hidden session plan: %q", output)
	}
}
