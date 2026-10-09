package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// noteLLM is a notebook assistant asked to record something. The extraction
// picks save_note as the one tool that does it; the model instead appends to
// the note that already holds the fact (update_note), or — with write unset —
// writes nothing and claims it did.
type noteLLM struct {
	mu          sync.Mutex
	write       string // tool the model calls on its first turn, "" for none
	extraction  string // raw extraction reply
	turns       int
	extractions int
	sawFeedback bool
}

func (n *noteLLM) next(messages []domain.Message) *domain.GenerationResult {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, m := range messages {
		if strings.Contains(m.Content, "you never called it") {
			n.sawFeedback = true
		}
	}
	n.turns++
	if n.turns == 1 && n.write != "" {
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID:       "call-write",
			Type:     "function",
			Function: domain.FunctionCall{Name: n.write, Arguments: map[string]interface{}{"text": "CS-3 飞边偏厚"}},
		}}}
	}
	return &domain.GenerationResult{Content: "已记下。"}
}

func (n *noteLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (n *noteLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (n *noteLLM) GenerateWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return n.next(m), nil
}
func (n *noteLLM) StreamWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(n.next(m))
}
func (n *noteLLM) GenerateStructured(_ context.Context, p string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if strings.Contains(p, "report ONLY the constraints") {
		n.mu.Lock()
		n.extractions++
		n.mu.Unlock()
		return &domain.StructuredResult{Raw: n.extraction, Valid: true}, nil
	}
	return structuredJSON(map[string]interface{}{}), nil
}
func (n *noteLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

const saveNoteOnly = `{"forbid_tools":false,"deliverables":[],"requested_actions":[` +
	`{"kind":"note","description":"记一下 CS-3 飞边偏厚","satisfied_by":"save_note","unconditional":true}]}`

// buildNotebook registers the two writes a notebook has, counting each call.
func buildNotebook(t *testing.T, llm *noteLLM, opts ...func(*Builder)) (*Service, map[string]int) {
	t.Helper()
	b := New("notebook").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm)
	for _, o := range opts {
		o(b)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	var mu sync.Mutex
	calls := map[string]int{}
	params := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}}}
	for _, name := range []string{"save_note", "update_note"} {
		svc.AddToolWithMetadata(name, name+" writes the notebook", params,
			func(context.Context, map[string]interface{}) (interface{}, error) {
				mu.Lock()
				calls[name]++
				mu.Unlock()
				return "ok", nil
			}, ToolMetadata{})
	}
	return svc, calls
}

// Measured on a notebook assistant: asked to record something, the model
// appended to the note that already held it. The contract wanted save_note,
// rejected the answer, and on the retry the model created the duplicate the
// append had avoided. Once a write has gone through, the run is not sent back.
func TestContractDoesNotSendAWriteBack(t *testing.T) {
	t.Parallel()
	llm := &noteLLM{write: "update_note", extraction: saveNoteOnly}
	svc, calls := buildNotebook(t, llm)

	res, err := svc.Run(context.Background(), "记一下：曲轴 CS-3 批次飞边偏厚")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Blocked {
		t.Fatalf("run blocked: %q", res.Text())
	}
	if llm.sawFeedback {
		t.Error("the answer was rejected after update_note had already written")
	}
	if calls["update_note"] != 1 || calls["save_note"] != 0 {
		t.Errorf("writes = %v, want one update_note and no save_note", calls)
	}
}

// The case the contract exists for is still caught: nothing written, "done"
// claimed.
func TestContractStillCatchesAClaimWithNoWrite(t *testing.T) {
	t.Parallel()
	llm := &noteLLM{extraction: saveNoteOnly}
	svc, _ := buildNotebook(t, llm)

	if _, err := svc.Run(context.Background(), "记一下：曲轴 CS-3 批次飞边偏厚"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !llm.sawFeedback {
		t.Error("a run that wrote nothing and said it had was not rejected")
	}
}

// A failed write changed nothing, so it does not count as one.
func TestFailedWriteIsNotAStateChange(t *testing.T) {
	t.Parallel()
	r := &Runtime{svc: &Service{toolRegistry: NewToolRegistry()}}
	r.noteToolOutcome("update_note", context.Canceled)
	r.noteToolOutcome("task_complete", nil)
	if got := r.stateChangesSnapshot(); len(got) != 0 {
		t.Errorf("state changes = %v, want none", got)
	}
	r.noteToolOutcome("update_note", nil)
	if got := r.stateChangesSnapshot(); len(got) != 1 || got[0] != "update_note" {
		t.Errorf("state changes = %v, want [update_note]", got)
	}
}

// Either tool that does the action meets the contract.
func TestContractAcceptsAnAlternativeTool(t *testing.T) {
	t.Parallel()
	lint := RequestedActionContract()
	action := []RequestedAction{{
		Kind:            "note",
		Description:     "记一下",
		SatisfiedBy:     "save_note",
		AlsoSatisfiedBy: []string{"update_note"},
		Unconditional:   true,
	}}
	avail := []string{"save_note", "update_note", "search_notes"}
	if ok, reason := lint.Check("已追加。", LintContext{RequestedActions: action, ToolCalls: []string{"search_notes", "update_note"}, AvailableTools: avail}); !ok {
		t.Fatalf("an alternative tool was rejected: %s", reason)
	}
	ok, reason := lint.Check("已记下。", LintContext{RequestedActions: action, ToolCalls: []string{"search_notes"}, AvailableTools: avail})
	if ok {
		t.Fatal("neither tool was called and nothing was written")
	}
	if !strings.Contains(reason, "save_note or update_note") {
		t.Errorf("the reason should name both tools: %s", reason)
	}
}

// The extraction reports alternatives; an invented one is dropped, and a real
// alternative stands in for an invented choice.
func TestAlternativesAreParsedAndPruned(t *testing.T) {
	t.Parallel()
	got, err := parseRunConstraints(`{"forbid_tools":false,"deliverables":[],"requested_actions":[` +
		`{"kind":"note","description":"x","satisfied_by":"save_note","also_satisfied_by":[" update_note ","",  "notes_v9"],"unconditional":true},` +
		`{"kind":"note","description":"y","satisfied_by":"make_note","also_satisfied_by":["update_note"],"unconditional":true}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got = pruneUnknownTools(got, []toolCatalogEntry{{Name: "save_note"}, {Name: "update_note"}})
	if a := got.RequestedActions[0]; strings.Join(a.Tools(), ",") != "save_note,update_note" {
		t.Errorf("first action tools = %v", a.Tools())
	}
	if a := got.RequestedActions[1]; a.SatisfiedBy != "update_note" || len(a.AlsoSatisfiedBy) != 0 {
		t.Errorf("second action = %+v, want update_note promoted", a)
	}
	if names := requiredToolNames(got); strings.Join(names, ",") != "save_note,update_note,update_note" {
		t.Errorf("required tools = %v", names)
	}
}

// Switched off for the service, the extraction is never made — not even for
// a request that plainly asks for an action.
func TestServiceCanSwitchConstraintExtractionOff(t *testing.T) {
	t.Parallel()
	llm := &noteLLM{extraction: saveNoteOnly}
	svc, _ := buildNotebook(t, llm, func(b *Builder) { b.WithConstraintExtraction(false) })

	if _, err := svc.Run(context.Background(), "记一下：曲轴 CS-3 批次飞边偏厚"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if llm.extractions != 0 {
		t.Errorf("extraction ran %d times on a service that switched it off", llm.extractions)
	}
	if llm.sawFeedback {
		t.Error("a contract was enforced with the extraction off")
	}
}
