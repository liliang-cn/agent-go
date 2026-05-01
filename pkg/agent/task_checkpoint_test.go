package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func newCheckpointTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestStoreSaveAndListTaskCheckpoints(t *testing.T) {
	store := newCheckpointTestStore(t)
	taskID := "task-cp-1"
	for i := 1; i <= 3; i++ {
		cp := &TaskCheckpoint{
			TaskID:   taskID,
			Seq:      i,
			Round:    i,
			Messages: []domain.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ok"}},
		}
		if err := store.SaveTaskCheckpoint(cp); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		if cp.ID == "" {
			t.Fatalf("checkpoint ID should be assigned by SaveTaskCheckpoint")
		}
	}
	got, err := store.ListTaskCheckpoints(taskID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 got %d", len(got))
	}
	// Newest first.
	if got[0].Seq != 3 || got[1].Seq != 2 || got[2].Seq != 1 {
		t.Fatalf("unexpected seq order: %d %d %d", got[0].Seq, got[1].Seq, got[2].Seq)
	}
}

func TestStorePruneTaskCheckpointsKeepsNewest(t *testing.T) {
	store := newCheckpointTestStore(t)
	taskID := "task-prune"
	for i := 1; i <= 5; i++ {
		_ = store.SaveTaskCheckpoint(&TaskCheckpoint{
			TaskID:    taskID,
			Seq:       i,
			Round:     i,
			Messages:  []domain.Message{{Role: "user", Content: "x"}},
			CreatedAt: time.Unix(int64(i), 0),
		})
	}
	deleted, err := store.PruneTaskCheckpoints(taskID, 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("want 3 deleted got %d", deleted)
	}
	remaining, _ := store.ListTaskCheckpoints(taskID, 0)
	if len(remaining) != 2 {
		t.Fatalf("want 2 remaining got %d", len(remaining))
	}
	if remaining[0].Seq != 5 || remaining[1].Seq != 4 {
		t.Fatalf("expected newest two retained, got %d %d", remaining[0].Seq, remaining[1].Seq)
	}
}

func TestStoreLatestTaskCheckpointEmpty(t *testing.T) {
	store := newCheckpointTestStore(t)
	if _, err := store.LatestTaskCheckpoint("nope"); err == nil {
		t.Fatal("expected errCheckpointMissing for empty task")
	}
}

func TestCheckpointWriterAssignsMonotonicSeq(t *testing.T) {
	store := newCheckpointTestStore(t)
	w := newCheckpointWriter(store)
	taskID := "task-mono"
	for i := 0; i < 4; i++ {
		_ = w.Write(&TaskCheckpoint{
			TaskID:   taskID,
			Round:    i,
			Messages: []domain.Message{{Role: "user", Content: "m"}},
		})
	}
	cps, _ := store.ListTaskCheckpoints(taskID, 0)
	if len(cps) != 4 {
		t.Fatalf("want 4 got %d", len(cps))
	}
	for i, cp := range cps {
		// list is descending; expect 4,3,2,1
		want := 4 - i
		if cp.Seq != want {
			t.Fatalf("at %d: want seq %d got %d", i, want, cp.Seq)
		}
	}
}

func TestTeamManagerWriteCheckpointPrunesAtCap(t *testing.T) {
	store := newCheckpointTestStore(t)
	manager := NewTeamManager(store)
	taskID := "task-cap"
	for i := 0; i < MaxCheckpointsPerTask+5; i++ {
		if err := manager.WriteCheckpoint(taskID, CheckpointReasonRoundEnd, i, "session", "Operator", "", "", []domain.Message{
			{Role: "user", Content: "ping"},
		}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	cps, err := manager.ListCheckpoints(taskID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cps) != MaxCheckpointsPerTask {
		t.Fatalf("want %d kept, got %d", MaxCheckpointsPerTask, len(cps))
	}
}

func TestTaskServiceResumeFromCheckpointReplaysMessages(t *testing.T) {
	t.Parallel()
	llm := &scriptedLintLLM{
		// First run: produces a partial response, then is interrupted by
		// the test (we only consume to the first checkpoint).
		// Resume run: produces a clean final answer.
		replies: []string{
			"i'm thinking about it...",
			"final: 42",
		},
	}
	svc, err := New("Operator").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// Build a manager to provide the checkpoint sink and resume API.
	// Reuse svc's underlying store so checkpoint writes/reads see the
	// same DB.
	store := svc.store
	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members: %v", err)
	}
	svc.SetCheckpointSink(manager)

	taskID := "task-resume-test"
	// Seed an AsyncTask record so ResumeFromCheckpoint's GetTask
	// lookup succeeds.
	manager.upsertAsyncTask(&AsyncTask{
		ID:        taskID,
		TaskID:    taskID,
		SessionID: svc.CurrentSessionID(),
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusRunning,
		AgentName: "Operator",
		Prompt:    "what is the answer?",
		CreatedAt: time.Now(),
	})

	// Hand-write a checkpoint as if the previous run was interrupted.
	priorMessages := []domain.Message{
		{Role: "user", Content: "what is the answer?", TaskID: taskID},
		{Role: "assistant", Content: "i'm thinking about it...", TaskID: taskID},
	}
	if err := manager.WriteCheckpoint(taskID, CheckpointReasonRoundEnd, 1, svc.CurrentSessionID(), "Operator", "", "", priorMessages); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	cps, _ := manager.ListCheckpoints(taskID, 0)
	if len(cps) == 0 {
		t.Fatal("checkpoint not persisted")
	}

	// Plug the manager's runtime so ResumeFromCheckpoint can dispatch
	// the resumed run through a real Service. For this test the
	// dispatch path uses the manager's stream override — simplest is
	// to plug in a stream override that just returns a Complete event.
	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		// Verify the resume option was threaded through.
		cfg := DefaultRunConfig()
		for _, opt := range runOptions {
			opt(cfg)
		}
		if len(cfg.ResumeMessages) != len(priorMessages) {
			t.Errorf("expected resume messages of length %d, got %d", len(priorMessages), len(cfg.ResumeMessages))
		}
		ch := make(chan *Event, 1)
		ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "final: 42", Timestamp: time.Now()}
		close(ch)
		return ch, nil
	}

	resumed, err := manager.Tasks().ResumeFromCheckpoint(context.Background(), taskID, CheckpointResumeOptions{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed == nil || resumed.ID != taskID {
		t.Fatalf("resumed unified task mismatch: %#v", resumed)
	}

	// Wait briefly for the goroutine to wrap the run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := manager.Tasks().Get(context.Background(), taskID)
		if got != nil && strings.Contains(got.Output, "final: 42") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := manager.Tasks().Get(context.Background(), taskID)
	t.Fatalf("expected resumed task to complete with 'final: 42', got status=%s output=%q", got.Status, got.Output)
}
