package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

func TestListTasksPaged(t *testing.T) {
	db, err := NewAgentGoDB(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("NewAgentGoDB: %v", err)
	}
	defer db.Close()

	// 5 tasks, ascending created_at: t0 (oldest) .. t4 (newest).
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 5; i++ {
		status := "completed"
		if i%2 == 1 {
			status = "running"
		}
		task := &taskpkg.Task{
			ID:        fmt.Sprintf("task-%d", i),
			Kind:      "agent",
			Status:    taskpkg.Status(status),
			Input:     fmt.Sprintf("analyze AAPL part %d", i),
			AgentName: "Operator",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := db.SaveTask(task); err != nil {
			t.Fatalf("SaveTask %d: %v", i, err)
		}
	}

	// Page 1 (newest-first): task-4, task-3.
	page, total, err := db.ListTasksPaged(TaskListFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("page0: %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(page) != 2 || page[0].ID != "task-4" || page[1].ID != "task-3" {
		t.Fatalf("page0 = %v, want [task-4 task-3] newest-first", ids(page))
	}

	// Page 2 via offset: task-2, task-1.
	page, _, err = db.ListTasksPaged(TaskListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page) != 2 || page[0].ID != "task-2" || page[1].ID != "task-1" {
		t.Fatalf("page1 = %v, want [task-2 task-1]", ids(page))
	}

	// Status filter: 3 completed (0,2,4); total reflects the filter.
	page, total, err = db.ListTasksPaged(TaskListFilter{Limit: 50, Status: "completed"})
	if err != nil {
		t.Fatalf("status filter: %v", err)
	}
	if total != 3 || len(page) != 3 {
		t.Fatalf("completed total=%d len=%d, want 3/3 (%v)", total, len(page), ids(page))
	}
	for _, p := range page {
		if p.Status != "completed" {
			t.Fatalf("status filter leaked %s", p.Status)
		}
	}

	// "all" status is a no-op filter.
	if _, total, _ = db.ListTasksPaged(TaskListFilter{Limit: 50, Status: "all"}); total != 5 {
		t.Fatalf("status=all total = %d, want 5", total)
	}

	// Search matches input substring.
	page, total, err = db.ListTasksPaged(TaskListFilter{Limit: 50, Search: "part 3"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 || len(page) != 1 || page[0].ID != "task-3" {
		t.Fatalf("search 'part 3' = %v total=%d, want [task-3]/1", ids(page), total)
	}
}

func ids(tasks []*taskpkg.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}
