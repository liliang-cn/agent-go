package memory

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// movedScriptLLM is a deterministic stand-in for a model that does what the
// extraction prompt asks and nothing cleverer: it reconciles against the
// existing memories it is shown, and it cannot reconcile against ones it is
// not shown. Every call is counted, by purpose.
type movedScriptLLM struct {
	mu          sync.Mutex
	extractions int
	navigations int
	other       int
}

var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// candidateID returns the id on the first prompt line that names a uuid and
// mentions word — i.e. an existing memory the prompt showed the model.
func candidateID(prompt, word string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if !strings.Contains(line, word) || strings.Contains(line, "session_id") {
			continue
		}
		if id := uuidPattern.FindString(line); id != "" {
			return id
		}
	}
	return ""
}

func (m *movedScriptLLM) GenerateStructured(_ context.Context, prompt string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case strings.Contains(prompt, "memory retrieval assistant"):
		m.navigations++
		// Select every memory in the index — the worst case for staleness,
		// and what a model does when two entries both look relevant.
		ids := uuidPattern.FindAllString(prompt, -1)
		raw, _ := json.Marshal(map[string]interface{}{"ids": ids, "reasoning": "all residence facts"})
		return &domain.StructuredResult{Raw: string(raw), Valid: true}, nil
	case strings.Contains(prompt, "background memory process"):
		m.extractions++
		item := map[string]interface{}{
			"type": "fact", "importance": 0.9, "kind": "world",
			"time_text": "", "time_kind": "none",
		}
		goal := prompt[strings.Index(prompt, "Goal:"):]
		switch {
		case strings.Contains(goal, "I live in Berlin"):
			item["content"] = "User lives in Berlin"
			item["op"] = "add"
		case strings.Contains(goal, "moved to Vienna"):
			item["content"] = "User lives in Vienna"
			item["op"] = "add"
			if id := candidateID(prompt, "Berlin"); id != "" {
				item["op"] = "update"
				item["target_id"] = id
			}
		case strings.Contains(goal, "Where do I live"):
			item["content"] = "User lives in Vienna"
			item["op"] = "add"
			if id := candidateID(prompt, "Vienna"); id != "" {
				item["op"] = "noop"
				item["target_id"] = id
			}
		default:
			return &domain.StructuredResult{Raw: `{"should_store": false, "memories": []}`, Valid: true}, nil
		}
		raw, _ := json.Marshal(map[string]interface{}{"should_store": true, "memories": []interface{}{item}})
		return &domain.StructuredResult{Raw: string(raw), Valid: true}, nil
	}
	m.other++
	return &domain.StructuredResult{Raw: `{}`, Valid: true}, nil
}

func (m *movedScriptLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	m.mu.Lock()
	m.other++
	m.mu.Unlock()
	return "", nil
}
func (m *movedScriptLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (m *movedScriptLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{}, nil
}
func (m *movedScriptLLM) StreamWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions, domain.ToolCallCallback) error {
	return nil
}
func (m *movedScriptLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{}, nil
}

type movedScenarioResult struct {
	staleInjected   int  // injected lines presenting Berlin as a current fact
	staleAnswer     bool // first residence line the answering model reads is Berlin
	activeResidence int  // residence memories still valid after all three turns
	totalRows       int
	modelCalls      int
	extractions     int
	navigations     int
}

// runMovedScenario plays three turns through the default (file) backend the
// way the runtime does — retrieve before the turn, extract after it:
//
//  1. "I live in Berlin."
//  2. "I moved to Vienna last week."
//  3. "Where do I live?"
func runMovedScenario(t *testing.T) movedScenarioResult {
	t.Helper()
	ctx := context.Background()
	fileStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llm := &movedScriptLLM{}
	cfg := DefaultConfig()
	cfg.ReflectThreshold = 0
	svc := NewService(fileStore, llm, nil, cfg)
	defer svc.Close()

	const session = "3f1e9c1a-0000-4000-8000-00000000c0de"
	qc := domain.MemoryQueryContext{SessionID: session}
	turns := []struct{ goal, answer string }{
		{"I live in Berlin.", "Noted."},
		{"I moved to Vienna last week.", "Congratulations on the move."},
		{"Where do I live?", "You live in Vienna."},
	}

	var res movedScenarioResult
	for i, turn := range turns {
		injected, _, err := svc.RetrieveAndInjectWithContext(ctx, turn.goal, qc)
		if err != nil {
			t.Fatalf("turn %d retrieve: %v", i+1, err)
		}
		if i == len(turns)-1 {
			for _, line := range strings.Split(injected, "\n") {
				if !strings.Contains(line, "lives in") {
					continue
				}
				current := !strings.Contains(strings.ToLower(line), "superseded")
				if strings.Contains(line, "Berlin") && current {
					res.staleInjected++
				}
			}
			for _, line := range strings.Split(injected, "\n") {
				if strings.Contains(line, "lives in") && !strings.Contains(strings.ToLower(line), "superseded") {
					res.staleAnswer = strings.Contains(line, "Berlin")
					break
				}
			}
			t.Logf("injected before turn 3:\n%s", injected)
		}
		if err := svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
			SessionID: session, TaskGoal: turn.goal, TaskResult: turn.answer,
		}); err != nil {
			t.Fatalf("turn %d store: %v", i+1, err)
		}
	}

	all, _, err := fileStore.List(ctx, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	res.totalRows = len(all)
	for _, m := range all {
		if strings.Contains(m.Content, "lives in") && !store.IsStale(m) {
			res.activeResidence++
		}
	}
	res.extractions, res.navigations = llm.extractions, llm.navigations
	res.modelCalls = llm.extractions + llm.navigations + llm.other
	return res
}

// Before reconciliation (measured on the parent commit with this same
// script): stale_injected=3 stale_answer=true active_residence=3
// duplicates=2 total_rows=3 model_calls=5. The model-call count must not
// move: reconciliation rides on the extraction call.
func TestMovedScenarioMeasurement(t *testing.T) {
	r := runMovedScenario(t)
	defer t.Logf("MEASURE stale_injected=%d stale_answer=%v active_residence=%d duplicates=%d total_rows=%d model_calls=%d (extraction=%d navigator=%d)",
		r.staleInjected, r.staleAnswer, r.activeResidence, r.activeResidence-1, r.totalRows, r.modelCalls, r.extractions, r.navigations)
	if r.staleInjected != 0 || r.staleAnswer {
		t.Errorf("the superseded residence reached the prompt as current: stale_injected=%d stale_answer=%v", r.staleInjected, r.staleAnswer)
	}
	if r.activeResidence != 1 {
		t.Errorf("want exactly one current residence memory, got %d", r.activeResidence)
	}
	if r.totalRows != 2 {
		t.Errorf("want 2 rows (Berlin kept as history, Vienna current), got %d", r.totalRows)
	}
	if r.modelCalls != 5 {
		t.Errorf("reconciliation must cost no extra model call: got %d calls, want 5", r.modelCalls)
	}
}
