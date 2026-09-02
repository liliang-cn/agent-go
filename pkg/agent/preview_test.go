package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// previewContextExt is a minimal ContextContributor extension: it exists to
// prove a preview shows what an extension would put in front of the model.
type previewContextExt struct{}

func (previewContextExt) Name() string { return "preview-context-ext" }

func (previewContextExt) ContributeContext(_ context.Context, in ContextInput) ([]domain.Message, error) {
	return []domain.Message{{Role: "system", Content: "PREVIEW EXTENSION CONTEXT for " + in.Goal}}, nil
}

func newPreviewService(t *testing.T, llm domain.Generator, exts ...Extension) *Service {
	t.Helper()
	b := New("preview-agent").
		WithConfig(testAgentConfig(t.TempDir())).
		WithSystemPrompt("You are the preview agent.").
		WithLLM(llm)
	if len(exts) > 0 {
		b = b.WithExtensions(exts...)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func TestPreviewContainsGoalAndSystemPrompt(t *testing.T) {
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "unused"}}}
	svc := newPreviewService(t, llm)

	p, err := svc.Preview(context.Background(), "count the widgets")
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if len(p.Messages) == 0 {
		t.Fatal("preview has no messages")
	}
	if p.Messages[0].Role != "system" {
		t.Fatalf("first message role = %q, want system", p.Messages[0].Role)
	}
	if !strings.Contains(p.SystemPrompt, "You are the preview agent.") {
		t.Errorf("system prompt missing the agent's own prompt: %q", p.SystemPrompt)
	}
	if p.SystemPrompt != p.Messages[0].Content {
		t.Errorf("SystemPrompt does not match Messages[0]")
	}
	var sawGoal bool
	for _, m := range p.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "count the widgets") {
			sawGoal = true
		}
	}
	if !sawGoal {
		t.Errorf("goal missing from previewed messages: %+v", p.Messages)
	}
	if p.EstimatedTokens <= 0 {
		t.Errorf("EstimatedTokens = %d, want > 0", p.EstimatedTokens)
	}
	if p.TaskID == "" || p.SessionID == "" {
		t.Errorf("preview did not report its session/task: %q %q", p.SessionID, p.TaskID)
	}
	// Constraint extraction would have cost a model call, so it was skipped
	// and said so rather than reporting invented constraints.
	if !p.ConstraintExtractionSkipped {
		t.Errorf("ConstraintExtractionSkipped = false, want true")
	}
}

// TestPreviewDoesNotCallTheModel is the whole point of the feature: a dry run
// that talks to the provider is not a dry run.
func TestPreviewDoesNotCallTheModel(t *testing.T) {
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "unused"}}}
	svc := newPreviewService(t, llm)

	if _, err := svc.Preview(context.Background(), "do something expensive"); err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if got := llm.callCount(); got != 0 {
		t.Fatalf("LLM was called %d times during Preview", got)
	}
	if rounds := len(llm.rounds()); rounds != 0 {
		t.Fatalf("LLM saw %d message rounds during Preview", rounds)
	}
}

// TestPreviewPersistsNothing pins the other half: no session row, no history,
// no run in the cancel registry, and the service's own conversation pointer
// left where it was.
func TestPreviewPersistsNothing(t *testing.T) {
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "unused"}}}
	svc := newPreviewService(t, llm)

	before := svc.CurrentSessionID()

	p, err := svc.Preview(context.Background(), "leave no trace", WithSessionID("preview-session-1"))
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if p.SessionID != "preview-session-1" {
		t.Fatalf("SessionID = %q", p.SessionID)
	}
	if got := svc.CurrentSessionID(); got != before {
		t.Errorf("Preview moved the service session pointer: %q -> %q", before, got)
	}
	if _, err := svc.store.GetSession("preview-session-1"); err == nil {
		t.Errorf("Preview created a session row in the store")
	}
	if runs := svc.ActiveRuns(); len(runs) != 0 {
		t.Errorf("Preview registered %d runs", len(runs))
	}
}

func TestPreviewToolsReflectAllowlistAndToolsDisabled(t *testing.T) {
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "unused"}}}
	svc := newPreviewService(t, llm)
	svc.RegisterTool(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "preview_probe",
			Description: "a tool that exists only to be previewed",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}, func(context.Context, map[string]interface{}) (interface{}, error) { return "ok", nil })

	base, err := svc.Preview(context.Background(), "use the probe")
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if !previewHasTool(base.Tools, "preview_probe") {
		t.Fatalf("registered tool missing from preview: %v", previewToolNames(base.Tools))
	}
	if len(base.Tools) < 2 {
		t.Fatalf("expected the built-ins alongside the probe, got %v", previewToolNames(base.Tools))
	}

	allowed, err := svc.Preview(context.Background(), "use the probe", WithToolAllowlist([]string{"preview_probe"}))
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if names := previewToolNames(allowed.Tools); len(names) != 1 || names[0] != "preview_probe" {
		t.Errorf("allowlisted preview offered %v, want [preview_probe]", names)
	}

	none, err := svc.Preview(context.Background(), "use the probe", WithToolsDisabled())
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if len(none.Tools) != 0 {
		t.Errorf("WithToolsDisabled preview offered %v, want none", previewToolNames(none.Tools))
	}
	if !none.Constraints.ForbidTools {
		t.Errorf("WithToolsDisabled preview did not report ForbidTools")
	}
	// Declared constraints need no model call in a real run either, so the
	// preview is complete rather than partial.
	if none.ConstraintExtractionSkipped {
		t.Errorf("declared constraints should not report a skipped extraction")
	}
}

func TestPreviewShowsExtensionContributedContext(t *testing.T) {
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "unused"}}}
	svc := newPreviewService(t, llm, previewContextExt{})

	p, err := svc.Preview(context.Background(), "check the extension")
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	var found bool
	for _, m := range p.Messages {
		if strings.Contains(m.Content, "PREVIEW EXTENSION CONTEXT for check the extension") {
			found = true
		}
	}
	if !found {
		t.Fatalf("extension context missing from preview: %+v", p.Messages)
	}
	if llm.callCount() != 0 {
		t.Fatalf("LLM was called during Preview")
	}
}

// TestPreviewMatchesFirstRoundOfARealRun is the guarantee that makes a preview
// worth reading: what it shows is what the loop sends.
func TestPreviewMatchesFirstRoundOfARealRun(t *testing.T) {
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{{Content: "done"}}}
	svc := newPreviewService(t, llm)

	const goal = "compare the preview against the run"
	p, err := svc.Preview(context.Background(), goal,
		WithSessionID("preview-parity"), WithConstraintExtraction(false))
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}

	if _, err := svc.Run(context.Background(), goal,
		WithSessionID("preview-parity"), WithConstraintExtraction(false)); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	first := llm.firstRound()
	if len(first) == 0 {
		t.Fatal("the run never called the model")
	}
	if len(first) != len(p.Messages) {
		t.Fatalf("preview had %d messages, the run sent %d", len(p.Messages), len(first))
	}
	for i := range first {
		if first[i].Role != p.Messages[i].Role || first[i].Content != p.Messages[i].Content {
			t.Fatalf("message %d differs:\npreview: %s / %q\nrun:     %s / %q",
				i, p.Messages[i].Role, p.Messages[i].Content, first[i].Role, first[i].Content)
		}
	}
}

func previewToolNames(tools []domain.ToolDefinition) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Function.Name)
	}
	return out
}

func previewHasTool(tools []domain.ToolDefinition, name string) bool {
	for _, t := range tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}
