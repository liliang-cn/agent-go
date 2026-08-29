package agent

import (
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Round snapshots carry the conversation and no files, so an interrupted
// task's newest checkpoint has nothing to restore. The archive has to be found
// one query further back, or a resumed run comes up with no workspace at all.
func TestLatestTaskWorkspaceLooksPastFileLessCheckpoints(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	taskID := "task-with-files"
	msgs := []domain.Message{{Role: "user", Content: "go"}}
	archive := []byte("gzip-tar-pretend")

	// A terminal-style snapshot with files, then two round snapshots without.
	if err := store.SaveTaskCheckpoint(&TaskCheckpoint{
		TaskID: taskID, Seq: 1, Round: 1, Messages: msgs, Workspace: archive,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	for seq := 2; seq <= 3; seq++ {
		if err := store.SaveTaskCheckpoint(&TaskCheckpoint{
			TaskID: taskID, Seq: seq, Round: seq, Messages: msgs,
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	latest, err := store.LatestTaskCheckpoint(taskID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if len(latest.Workspace) != 0 {
		t.Fatal("precondition: the newest checkpoint should carry no workspace")
	}

	got, err := store.LatestTaskWorkspace(taskID)
	if err != nil {
		t.Fatalf("LatestTaskWorkspace: %v", err)
	}
	if string(got) != string(archive) {
		t.Errorf("got %q, want the archive from the older checkpoint", got)
	}
}

// A task that never produced files reports none, rather than an error a
// resume path would have to special-case.
func TestLatestTaskWorkspaceIsNilWhenNoneWasEverStored(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	if err := store.SaveTaskCheckpoint(&TaskCheckpoint{
		TaskID: "no-files", Seq: 1, Round: 1,
		Messages: []domain.Message{{Role: "user", Content: "go"}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.LatestTaskWorkspace("no-files")
	if err != nil {
		t.Fatalf("LatestTaskWorkspace: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want nil", got)
	}
}
