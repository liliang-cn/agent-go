// Task memory, from the outside.
//
// A Service built with Build() wires a SQLiteTaskStore over its own database
// automatically, and RunSegments writes to it at every segment boundary — so
// most programs never touch this API directly. This example uses the store
// standalone (no LLM, no Service) to show what is actually in it: the task
// projection, the per-run summaries, the idempotent journal, the lessons,
// and the rendered resume context a restarted process injects.
//
// Run it twice to see the point: the second process reads what the first left.
//
//	go run ./examples/task-store
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	_ "modernc.org/sqlite"
)

func main() {
	dir, err := os.MkdirTemp("", "task-store-example")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := sql.Open("sqlite", filepath.Join(dir, "tasks.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	store, err := agent.NewSQLiteTaskStore(db)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	// --- Process one: a task runs, gets somewhere, and stops. -------------

	const taskID = "ship-the-api"
	if err := store.SaveTask(ctx, agent.TaskState{
		ID:     taskID,
		Goal:   "Ship the orders API: auth, routes, rate limiter, load test.",
		Status: agent.TaskStatusRunning,
	}); err != nil {
		log.Fatal(err)
	}

	runID, err := store.BeginRun(ctx, agent.TaskRun{TaskID: taskID})
	if err != nil {
		log.Fatal(err)
	}

	// The journal is append-only. An entry with an IdemKey is the
	// crash-retry guard: record a side-effecting step under a stable key
	// before doing it, and a retried run finds out it already happened.
	seq, dup, err := store.AppendJournal(ctx, agent.TaskJournalEntry{
		TaskID: taskID, RunID: runID, Kind: "tool_call",
		Payload: `{"tool":"create_dns_record","zone":"api.example.com"}`,
		IdemKey: taskID + "/create-dns-record",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("journal seq %d (duplicate=%v) — the DNS record is being created\n", seq, dup)

	// The same append after a crash-and-retry: nothing is written twice.
	seq2, dup2, err := store.AppendJournal(ctx, agent.TaskJournalEntry{
		TaskID: taskID, RunID: runID, Kind: "tool_call",
		Payload: `{"tool":"create_dns_record","zone":"api.example.com"}`,
		IdemKey: taskID + "/create-dns-record",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("journal seq %d (duplicate=%v) — the retry learns it already happened\n\n", seq2, dup2)

	// The run ends with a write-time summary — a few sentences from the run
	// that knows, for the run that will not.
	if err := store.EndRun(ctx, runID, agent.TaskRunOutcomeBlocked,
		"auth and routes done; blocked choosing a rate-limiter algorithm", 0.42); err != nil {
		log.Fatal(err)
	}
	if err := store.AddLearning(ctx, taskID,
		"the vendor SDK retries internally; wrapping it in our retry doubles every call"); err != nil {
		log.Fatal(err)
	}
	if err := store.SaveResumeBrief(ctx, taskID,
		"auth + routes done and tested; next: pick token-bucket or sliding-window, then load test"); err != nil {
		log.Fatal(err)
	}

	// --- Process two: a fresh process picks the task up. ------------------
	//
	// This is the read path — the whole reason the store exists. Inside the
	// framework this string is injected automatically as the "## Task
	// memory" system-prompt section of the next run (see taskResumeForRun).

	fmt.Println("what the next run is told:")
	fmt.Println("--------------------------------------------------")
	fmt.Println(agent.TaskResumeContext(ctx, store, taskID))
	fmt.Println("--------------------------------------------------")
}
