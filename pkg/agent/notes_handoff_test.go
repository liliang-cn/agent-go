package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

func notesService(t *testing.T, name string, sb sandbox.Sandbox, llm *promptCapturingLLM) *Service {
	t.Helper()
	svc, err := New(name).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithSandbox(sb).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return svc
}

func runOnce(t *testing.T, svc *Service, goal string) {
	t.Helper()
	events, err := svc.RunStream(context.Background(), goal)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}
}

// The knowledge a segmented run kept losing was not per-step progress — the
// plan holds that — but the shape of things: exact signatures, route paths,
// table columns. A segment writing an end-to-end test had to call functions
// earlier segments defined and nothing carried their signatures, so it guessed,
// failed to compile, read, fixed, guessed the next.
func TestNotesFileContentsAreCarriedIntoTheRun(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()
	notes := "store.CreateUser(ctx, email, hash, role) (*User, error)\nPOST /cart/items takes {product_id, quantity}"
	if err := sb.WriteFile(context.Background(), DefaultNotesFile, []byte(notes), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	llm := &promptCapturingLLM{}
	svc := notesService(t, "notes-carried", sb, llm)
	defer svc.Close()
	runOnce(t, svc, "Carry on.")

	prompts := llm.systemPrompts()
	if len(prompts) == 0 {
		t.Fatal("the model was never called")
	}
	for _, want := range []string{"Notes carried across this task", "store.CreateUser(ctx, email, hash, role)", "POST /cart/items"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("the prompt never carried %q", want)
		}
	}
}

// An agent never told where to put durable knowledge does not write any, which
// is how the signatures went missing in the first place. So the section
// appears even when the file does not exist yet.
func TestMissingNotesFileStillInvitesOne(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()

	llm := &promptCapturingLLM{}
	svc := notesService(t, "notes-absent", sb, llm)
	defer svc.Close()
	runOnce(t, svc, "Start.")

	p := llm.systemPrompts()[0]
	if !strings.Contains(p, "There is no "+DefaultNotesFile) {
		t.Errorf("the prompt should say the notes file is there to be written:\n%s", p)
	}
	if !strings.Contains(p, "exact function signatures") {
		t.Error("it should say what belongs in it")
	}
}

// No sandbox, no notes: nothing to read and nowhere to write.
func TestNoSandboxMeansNoNotesSection(t *testing.T) {
	llm := &promptCapturingLLM{}
	svc, err := New("notes-no-sandbox").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	runOnce(t, svc, "Start.")

	for i, p := range llm.systemPrompts() {
		if strings.Contains(p, "Notes carried across this task") {
			t.Errorf("turn %d carried a notes section with no workspace to hold one", i)
		}
	}
}

// The notes ride in a prompt that must stay byte-stable for a whole segment,
// so an agent that pastes its source tree in there cannot drown the run.
func TestNotesAreBounded(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()
	huge := strings.Repeat("x", notesHandoffMaxBytes*3)
	if err := sb.WriteFile(context.Background(), DefaultNotesFile, []byte(huge), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc := notesService(t, "notes-bounded", sb, &promptCapturingLLM{})
	defer svc.Close()

	out := svc.notesForRun(context.Background())
	if len(out) > notesHandoffMaxBytes+400 {
		t.Errorf("notes section is %d bytes; it must stay bounded", len(out))
	}
	if !strings.Contains(out, "read the file for the rest") {
		t.Error("a truncated notes section should say so")
	}
}

func TestWithNotesFileOverridesTheName(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()
	if err := sb.WriteFile(context.Background(), "HANDOVER.txt", []byte("the port is 47821"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc, err := New("notes-custom").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&promptCapturingLLM{}).
		WithSandbox(sb).
		WithNotesFile("HANDOVER.txt").
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	out := svc.notesForRun(context.Background())
	if !strings.Contains(out, "the port is 47821") {
		t.Errorf("the custom notes file was not read: %q", out)
	}
}
