package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// promptFuncLLM answers extraction calls with whatever reply(prompt) says,
// and records every prompt and schema it was given.
type promptFuncLLM struct {
	movedScriptLLM
	reply   func(prompt string) string
	prompts []string
	schemas []interface{}
}

func (p *promptFuncLLM) GenerateStructured(_ context.Context, prompt string, schema interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	p.prompts = append(p.prompts, prompt)
	p.schemas = append(p.schemas, schema)
	if strings.Contains(prompt, "memory retrieval assistant") {
		return &domain.StructuredResult{Raw: `{"ids": []}`, Valid: true}, nil
	}
	return &domain.StructuredResult{Raw: p.reply(prompt), Valid: true}, nil
}

func newFileService(t *testing.T, llm domain.Generator) (*Service, *store.FileMemoryStore) {
	t.Helper()
	fs, err := store.NewFileMemoryStore(t.TempDir())
	require.NoError(t, err)
	cfg := DefaultConfig()
	cfg.ReflectThreshold = 0
	svc := NewService(fs, llm, nil, cfg)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, fs
}

// The extraction call is shown the existing memories by id, and the schema
// it answers must carry op and kind as required fields — optional ones are
// the ones a model skips, and a skipped op reads exactly like "add".
func TestReconcileRidesOnTheExtractionCall(t *testing.T) {
	ctx := context.Background()
	llm := &promptFuncLLM{reply: func(string) string { return `{"should_store": false, "memories": []}` }}
	svc, _ := newFileService(t, llm)

	existing := &domain.Memory{
		ID: "11111111-1111-4111-8111-111111111111", Type: domain.MemoryTypeFact,
		SessionID: "s1", ScopeType: domain.MemoryScopeSession, ScopeID: "s1",
		Content: "User lives in Berlin", Importance: 0.9, CreatedAt: time.Now(),
	}
	require.NoError(t, svc.Add(ctx, existing))

	require.NoError(t, svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID: "s1", TaskGoal: "I moved to Vienna.", TaskResult: "Congratulations.",
	}))

	require.Len(t, llm.prompts, 1, "reconciliation must not add a model call")
	assert.Contains(t, llm.prompts[0], "id="+existing.ID)
	assert.Contains(t, llm.prompts[0], "User lives in Berlin")

	items := llm.schemas[0].(map[string]interface{})["properties"].(map[string]interface{})["memories"].(map[string]interface{})["items"].(map[string]interface{})
	props := items["properties"].(map[string]interface{})
	for _, f := range []string{"op", "target_id", "kind"} {
		assert.Contains(t, props, f)
	}
	assert.Subset(t, items["required"], []string{"op", "kind"})
}

// Another session's memory is not in this request's scope chain, so it is
// never shown — and an id the model was not shown cannot be retired, however
// confidently it is named.
func TestReconcileRefusesATargetItWasNotShown(t *testing.T) {
	ctx := context.Background()
	foreign := "33333333-3333-4333-8333-333333333333"
	llm := &promptFuncLLM{reply: func(string) string {
		return `{"should_store": true, "memories": [{"type": "fact", "content": "User lives in Vienna", "importance": 0.9,
			"op": "update", "target_id": "` + foreign + `", "kind": "world", "time_text": "", "time_kind": "none"}]}`
	}}
	svc, fs := newFileService(t, llm)
	require.NoError(t, svc.Add(ctx, &domain.Memory{
		ID: foreign, Type: domain.MemoryTypeFact, SessionID: "other", ScopeType: domain.MemoryScopeSession, ScopeID: "other",
		Content: "Someone else lives in Berlin", Importance: 0.9, CreatedAt: time.Now(),
	}))

	require.NoError(t, svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID: "mine", TaskGoal: "I moved to Vienna.", TaskResult: "Noted.",
	}))

	assert.NotContains(t, llm.prompts[0], foreign)
	got, err := fs.Get(ctx, foreign)
	require.NoError(t, err)
	assert.False(t, domain.MemoryIsSuperseded(got), "a memory the model was never shown was retired")
	all, _, _ := fs.List(ctx, 10, 0)
	assert.Len(t, all, 2, "the item is still stored, as new")
}

// noop means "already known": nothing is written.
func TestReconcileNoopStoresNothing(t *testing.T) {
	ctx := context.Background()
	id := "44444444-4444-4444-8444-444444444444"
	llm := &promptFuncLLM{reply: func(string) string {
		return `{"should_store": true, "memories": [{"type": "fact", "content": "User lives in Vienna", "importance": 0.9,
			"op": "noop", "target_id": "` + id + `", "kind": "world", "time_text": "", "time_kind": "none"}]}`
	}}
	svc, fs := newFileService(t, llm)
	require.NoError(t, svc.Add(ctx, &domain.Memory{
		ID: id, Type: domain.MemoryTypeFact, SessionID: "s1", ScopeType: domain.MemoryScopeSession, ScopeID: "s1",
		Content: "User lives in Vienna", Importance: 0.9, CreatedAt: time.Now(),
	}))

	require.NoError(t, svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID: "s1", TaskGoal: "Where do I live?", TaskResult: "You live in Vienna.",
	}))

	all, _, _ := fs.List(ctx, 10, 0)
	assert.Len(t, all, 1)
}

// A backend with no MarkStale keeps both: the replacement is stored, the old
// memory stays current, and Supersede says unsupported instead of pretending.
func TestReconcileUpdateDegradesWithoutAStaleMarker(t *testing.T) {
	ctx := context.Background()
	old := &domain.Memory{
		ID: "55555555-5555-4555-8555-555555555555", Type: domain.MemoryTypeFact,
		SessionID: "session:s1", ScopeType: domain.MemoryScopeSession, ScopeID: "s1",
		Content: "User lives in Berlin", CreatedAt: time.Now(),
	}
	st := new(MockMemoryStore)
	st.On("List", mock.Anything, reconcileListWindow, 0).Return([]*domain.Memory{old}, 1, nil)
	st.On("Store", mock.Anything, mock.AnythingOfType("*domain.Memory")).Return(nil).Once()

	llm := &promptFuncLLM{reply: func(string) string {
		return `{"should_store": true, "memories": [{"type": "fact", "content": "User lives in Vienna", "importance": 0.9,
			"op": "update", "target_id": "` + old.ID + `", "kind": "world", "time_text": "", "time_kind": "none"}]}`
	}}
	cfg := DefaultConfig()
	cfg.ReflectThreshold = 0
	svc := NewService(st, llm, nil, cfg)

	require.NoError(t, svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID: "s1", TaskGoal: "I moved to Vienna.", TaskResult: "Noted.",
	}))
	st.AssertNumberOfCalls(t, "Store", 1)
	st.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)

	stored := st.Calls[len(st.Calls)-1].Arguments.Get(1).(*domain.Memory)
	assert.Equal(t, old.ID, stored.Metadata["supersedes"])
	assert.Equal(t, "world", stored.Metadata[domain.MemoryKindMetadataKey])

	err := svc.Supersede(ctx, old.ID, stored.ID)
	assert.True(t, errors.Is(err, domain.ErrMemoryStoreUnsupported))
}

// A recorded kind is visible where the agent reads the memory.
func TestMemoryKindIsLabelledWhenInjected(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFileService(t, nil)
	mem := &domain.Memory{
		ID: "66666666-6666-4666-8666-666666666666", Type: domain.MemoryTypePreference,
		ScopeType: domain.MemoryScopeGlobal, Content: "zzkind The user finds tabs better than spaces",
		Importance: 0.9, CreatedAt: time.Now(),
	}
	domain.SetMemoryKind(mem, domain.MemoryKindOpinion)
	require.NoError(t, svc.Add(ctx, mem))

	text, _, err := svc.RetrieveAndInject(ctx, "zzkind tabs", "")
	require.NoError(t, err)
	assert.Contains(t, text, "[preference, opinion]")
}

func TestMemoryIsSupersededReadsFieldsAndMirrors(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	cases := []struct {
		name string
		m    *domain.Memory
		want bool
	}{
		{"current", &domain.Memory{}, false},
		{"superseded_by field", &domain.Memory{SupersededBy: "x"}, true},
		{"valid_to passed", &domain.Memory{ValidTo: &past}, true},
		{"valid_to ahead", &domain.Memory{ValidTo: &future}, false},
		{"metadata mirror", &domain.Memory{Metadata: map[string]interface{}{"superseded_by": "x"}}, true},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, domain.MemoryIsSuperseded(c.m), c.name)
	}
}
