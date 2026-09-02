package memory

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/timeaware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// The whole feature, end to end: a memory written yesterday saying "明天"
// must be recalled today as being about today.
func TestYesterdaysTomorrowIsRecalledAsToday(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	yesterday := time.Date(2026, 9, 1, 20, 0, 0, 0, shanghai)
	today := time.Date(2026, 9, 2, 9, 0, 0, 0, shanghai)

	// What the write path stored yesterday: the words, plus the day they
	// meant, resolved by the model against yesterday.
	mem := &domain.Memory{
		ID:        "m1",
		Type:      domain.MemoryTypeFact,
		Content:   "用户说明天要去医院复查",
		CreatedAt: yesterday,
	}
	mem.Metadata = setMemoryTimeReference(mem.Metadata, timeaware.Reference{
		Text: "明天", Kind: timeaware.KindPoint, Date: "2026-09-02",
		AllDay: true, AnchoredAt: yesterday,
	})

	note := timeNoteFor(mem, today)
	if !strings.Contains(note, "written 2026-09-01, yesterday") {
		t.Errorf("the recalled memory does not say when it was written: %s", note)
	}
	if !strings.Contains(note, `"明天" = 2026-09-02, today`) {
		t.Errorf("the recalled memory does not resolve yesterday's tomorrow to today: %s", note)
	}

	// And the header states the day, next to the memories it applies to.
	header := timeHeader(today)
	if !strings.Contains(header, "2026-09-02 (Wednesday)") || !strings.Contains(header, "+08:00") {
		t.Errorf("header = %q, want today's date and offset", header)
	}
}

// A memory nobody could resolve still carries its write time, which is the
// honest degraded answer — and is enough for a model to reckon from.
func TestUnresolvedMemoryStillCarriesItsWriteTime(t *testing.T) {
	written := time.Date(2026, 9, 1, 20, 0, 0, 0, time.FixedZone("CST", 8*3600))
	mem := &domain.Memory{ID: "m2", Content: "something with no date in it", CreatedAt: written}
	note := timeNoteFor(mem, written.AddDate(0, 0, 3))
	if !strings.Contains(note, "written 2026-09-01, 3 days ago") {
		t.Errorf("note = %q", note)
	}
	if strings.Contains(note, "=") {
		t.Errorf("nothing was resolved, so the note must claim nothing: %q", note)
	}
}

// The read path is on the agent's turn. It must not call a model.
func TestRecallNeverCallsTheModelForTime(t *testing.T) {
	ctx := context.Background()
	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	svc := NewService(store, llm, nil, DefaultConfig())

	written := time.Now().Add(-24 * time.Hour)
	mem := &domain.Memory{ID: "m3", Type: domain.MemoryTypeFact, Content: "明天要开会", CreatedAt: written}
	mem.Metadata = setMemoryTimeReference(mem.Metadata, timeaware.Reference{
		Text: "明天", Kind: timeaware.KindPoint,
		Date: time.Now().Format("2006-01-02"), AllDay: true, AnchoredAt: written,
	})

	out := svc.formatMemoriesForQuery(ctx, "什么时候开会", domain.MemoryQueryContext{SessionID: "s"},
		[]*domain.MemoryWithScore{{Memory: mem, Score: 1}})

	if !strings.Contains(out, "which is today") && !strings.Contains(out, ", today") {
		t.Errorf("the injected text does not re-anchor the memory:\n%s", out)
	}
	// Not one model call on the read path.
	llm.AssertNotCalled(t, "GenerateStructured", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	llm.AssertNotCalled(t, "Generate", mock.Anything, mock.Anything, mock.Anything)
}

// The write path resolves time without costing a single extra model call:
// the fields ride on the extraction request that was already being made.
func TestWritePathResolvesTimeWithoutASecondCall(t *testing.T) {
	ctx := context.Background()
	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	svc := NewService(store, llm, nil, DefaultConfig())

	// One answer, carrying both what to remember and what its words meant.
	extraction := `{"should_store": true, "memories": [
		{"type": "fact", "content": "用户明天要去医院", "importance": 0.8,
		 "time_text": "明天", "time_kind": "point", "occurs_on": "2099-01-02"},
		{"type": "fact", "content": "用户喜欢用 Neovim", "importance": 0.6, "time_kind": "none"}
	]}`

	var prompts []string
	var schemas []interface{}
	llm.On("GenerateStructured", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			prompts = append(prompts, args.String(1))
			schemas = append(schemas, args.Get(2))
		}).
		Return(&domain.StructuredResult{Raw: extraction, Valid: true}, nil)

	var stored []*domain.Memory
	store.On("Store", mock.Anything, mock.AnythingOfType("*domain.Memory")).
		Run(func(args mock.Arguments) { stored = append(stored, args.Get(1).(*domain.Memory)) }).
		Return(nil)

	assert.NoError(t, svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID: "s", TaskGoal: "用户说明天要去医院", TaskResult: "记住了",
	}))

	// The whole point: one call, not two.
	if len(llm.Calls) != 1 {
		t.Fatalf("made %d model calls, want 1 — time rides on the extraction", len(llm.Calls))
	}
	// And the model was actually told when "now" is, or it could not answer.
	if !strings.Contains(prompts[0], "Every relative expression in it is relative to that moment") {
		t.Errorf("the extraction prompt carries no anchor:\n%s", prompts[0])
	}
	// The schema asked for the fields, or the answer would have nowhere to go.
	props := schemas[0].(map[string]interface{})["properties"].(map[string]interface{})["memories"].(map[string]interface{})["items"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, field := range []string{"time_text", "time_kind", "occurs_on", "occurs_at"} {
		if _, ok := props[field]; !ok {
			t.Errorf("the extraction schema does not ask for %q", field)
		}
	}

	if len(stored) != 2 {
		t.Fatalf("stored %d memories, want 2", len(stored))
	}
	ref, ok := getMemoryTimeReference(stored[0].Metadata)
	if !ok || !ref.Resolved() {
		t.Fatalf("the dated memory kept no resolved reference: %+v", stored[0].Metadata)
	}
	if got, _ := ref.Start(); got.Format("2006-01-02") != "2099-01-02" {
		t.Errorf("resolved to %v, want 2099-01-02", got)
	}
	if ref.Text != "明天" {
		t.Errorf("the reference lost the words it resolved: %q", ref.Text)
	}
	if _, ok := getMemoryTimeReference(stored[1].Metadata); ok {
		t.Error("a memory with no time in it should carry no reference")
	}

	// And the structured event blob got the date too, so the schedule
	// machinery downstream sees it without any pattern having run.
	if event, ok := domain.GetMemoryEventMetadata(stored[0].Metadata); ok && event != nil {
		if event.OccursOn != "2099-01-02" {
			t.Errorf("event metadata occurs_on = %q, want the resolved date", event.OccursOn)
		}
	}
}

// No model, no resolver: memories still get written, and still carry their
// timestamps. Time anchoring is an enrichment, never a gate.
func TestNoResolverStillStoresAndStillDatesMemories(t *testing.T) {
	svc := NewService(new(MockMemoryStore), nil, nil, DefaultConfig())
	if svc.timeResolver != nil {
		t.Fatal("a service with no model should have no resolver")
	}
	if svc.resolveItemTimes(context.Background(), []domain.MemoryItem{{Content: "明天"}}, time.Now()) != nil {
		t.Error("resolving without a model must return nothing rather than guess")
	}
	written := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	note := timeNoteFor(&domain.Memory{Content: "明天", CreatedAt: written}, written.AddDate(0, 0, 1))
	if !strings.Contains(note, "written 2026-09-01, yesterday") {
		t.Errorf("note = %q, want the write time even with no resolver", note)
	}
}

// A failed extraction used to be indistinguishable from "the model decided
// there was nothing worth remembering": both returned nil from the writer,
// and a provider outage therefore stored nothing, silently, for as long as
// it lasted. Found by a live check where the memory simply was not there.
func TestFailedExtractionIsNotSilent(t *testing.T) {
	ctx := context.Background()
	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	svc := NewService(store, llm, nil, DefaultConfig())

	llm.On("GenerateStructured", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, context.DeadlineExceeded)

	var logged []string
	restore := captureWarnings(&logged)
	defer restore()

	// Still not an error to the caller: auto-store is best-effort and must
	// not fail the turn it rode in on.
	assert.NoError(t, svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID: "s", TaskGoal: "记住这个", TaskResult: "好的",
	}))
	store.AssertNotCalled(t, "Store", mock.Anything, mock.Anything)

	found := false
	for _, line := range logged {
		if strings.Contains(line, "extraction call failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a failed extraction said nothing; logged: %v", logged)
	}
}

// captureWarnings routes the framework's logging into a slice for the
// duration of a test.
func captureWarnings(into *[]string) func() {
	var mu sync.Mutex
	handler := slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		*into = append(*into, string(p))
		mu.Unlock()
		return len(p), nil
	}), &slog.HandlerOptions{Level: slog.LevelDebug})
	agentgolog.SetLogger(slog.New(handler))
	return func() { agentgolog.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) }
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
