package memory

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// graphRecordingStore is a file store that also records the graph calls a
// store which keeps a graph would receive.
type graphRecordingStore struct {
	*store.FileMemoryStore
	writes map[string]domain.MemoryGraph
	drops  []string
}

func (g *graphRecordingStore) WriteMemoryGraph(_ context.Context, id string, mg domain.MemoryGraph) error {
	g.writes[id] = mg
	return nil
}

func (g *graphRecordingStore) DropMemoryGraph(_ context.Context, id string) error {
	g.drops = append(g.drops, id)
	return nil
}

func newGraphService(t *testing.T, llm domain.Generator) (*Service, *graphRecordingStore) {
	t.Helper()
	fs, err := store.NewFileMemoryStore(t.TempDir())
	require.NoError(t, err)
	gs := &graphRecordingStore{FileMemoryStore: fs, writes: map[string]domain.MemoryGraph{}}
	cfg := DefaultConfig()
	cfg.ReflectThreshold = 0
	svc := NewService(gs, llm, nil, cfg)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, gs
}

// What a memory is about comes back from the same extraction call — the
// schema asks for it as required — and is written to the store's graph under
// the memory it came with. The entity names also become the memory's
// keywords, so recall can seed the graph from them.
func TestExtractionWritesTheMemoryGraph(t *testing.T) {
	ctx := context.Background()
	llm := &promptFuncLLM{reply: func(string) string {
		return `{"should_store": true, "memories": [{"type": "fact", "content": "周明远的女儿可可每周三16:30上钢琴课", "importance": 0.8,
			"op": "add", "kind": "world", "time_text": "每周三16:30", "time_kind": "recurring",
			"graph_entities": [{"name": "周明远", "type": "person"}, {"name": "可可", "type": "person"}, {"name": "钢琴课", "type": "event"}],
			"graph_relations": [{"from": "可可", "type": "daughter_of", "to": "周明远"}, {"from": "可可", "type": "attends", "to": "钢琴课"}]}]}`
	}}
	svc, gs := newGraphService(t, llm)

	require.NoError(t, svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID: "s1", TaskGoal: "我女儿可可每周三下午四点半有钢琴课", TaskResult: "记下了。",
	}))
	require.Len(t, llm.prompts, 1, "the graph must not cost a second model call")
	assert.Contains(t, llm.prompts[0], "graph_entities")

	items := llm.schemas[0].(map[string]interface{})["properties"].(map[string]interface{})["memories"].(map[string]interface{})["items"].(map[string]interface{})
	assert.Contains(t, items["properties"], "graph_entities")
	assert.Contains(t, items["properties"], "graph_relations")
	assert.Subset(t, items["required"], []string{"graph_entities", "graph_relations"})

	require.Len(t, gs.writes, 1)
	var memID string
	var g domain.MemoryGraph
	for id, mg := range gs.writes {
		memID, g = id, mg
	}
	assert.Len(t, g.Entities, 3)
	assert.Equal(t, domain.MemoryGraphRelation{From: "可可", Type: "daughter_of", To: "周明远"}, g.Relations[0])

	mem, err := svc.Get(ctx, memID)
	require.NoError(t, err)
	assert.Subset(t, mem.Keywords, []string{"周明远", "可可", "钢琴课"})
}

// A memory the extraction call replaces takes its graph with it: its edges
// were true of the old fact.
func TestReplacedMemoryDropsItsGraph(t *testing.T) {
	ctx := context.Background()
	var oldID string
	llm := &promptFuncLLM{reply: func(string) string {
		return `{"should_store": true, "memories": [{"type": "fact", "content": "可可的钢琴课改到每周五16:30", "importance": 0.8,
			"op": "update", "target_id": "` + oldID + `", "kind": "world", "time_text": "每周五16:30", "time_kind": "recurring",
			"graph_entities": [{"name": "可可", "type": "person"}], "graph_relations": []}]}`
	}}
	svc, gs := newGraphService(t, llm)
	old := &domain.Memory{
		ID: "77777777-7777-4777-8777-777777777777", Type: domain.MemoryTypeFact,
		SessionID: "s1", ScopeType: domain.MemoryScopeSession, ScopeID: "s1",
		Content: "可可每周三16:30上钢琴课", Importance: 0.8, CreatedAt: time.Now(),
	}
	oldID = old.ID
	require.NoError(t, svc.Add(ctx, old))

	require.NoError(t, svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID: "s1", TaskGoal: "钢琴课从下周改到周五了", TaskResult: "改好了。",
	}))
	assert.Equal(t, []string{old.ID}, gs.drops)
	stale, err := svc.Get(ctx, old.ID)
	require.NoError(t, err)
	assert.True(t, domain.MemoryIsSuperseded(stale))
}

func TestGraphsFromSummaryToleratesAnAnswerWithoutThem(t *testing.T) {
	assert.Nil(t, graphsFromSummary(""))
	gs := graphsFromSummary(`{"should_store": true, "memories": [{"type": "fact", "content": "x"}]}`)
	require.Len(t, gs, 1)
	assert.True(t, gs[0].Empty())
}

// A memory id is never an entity, however the model spells it.
func TestGraphsFromSummaryDropsIDNames(t *testing.T) {
	gs := graphsFromSummary(`{"memories": [{"graph_entities": [{"name": "9f1d9d46", "type": "other"}, {"name": "车机合作", "type": "project"}],
		"graph_relations": [{"from": "9f1d9d46", "type": "part_of", "to": "车机合作"}, {"from": "周明远", "type": "attends", "to": "车机合作"}]}]}`)
	require.Len(t, gs, 1)
	assert.Equal(t, []string{"车机合作", "周明远"}, gs[0].Names())
	assert.Len(t, gs[0].Relations, 1)
}

// An update must carry over what the memory it replaces still says.
func TestUpdateRuleKeepsWhatIsStillTrue(t *testing.T) {
	assert.Contains(t, reconcilePromptRules(nil), "whatever the replacement leaves out is forgotten")
}
