package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedResidence(t *testing.T, svc *Service) *domain.Memory {
	t.Helper()
	m := &domain.Memory{
		ID: "22222222-2222-4222-8222-222222222222", Type: domain.MemoryTypeFact,
		SessionID: "agent:assistant", ScopeType: domain.MemoryScopeAgent, ScopeID: "assistant",
		Content: "User lives in Chengdu", Importance: 0.9, CreatedAt: time.Now(),
	}
	require.NoError(t, svc.Add(context.Background(), m))
	return m
}

func explicitMemory(content string) *domain.Memory {
	return &domain.Memory{
		Type: domain.MemoryTypeFact, SessionID: "agent:assistant",
		ScopeType: domain.MemoryScopeAgent, ScopeID: "assistant",
		Content: content, Importance: 0.8,
	}
}

// The superai case: an explicit "moved to Beijing" used to be added beside
// "lives in Chengdu", both current. Reconciled, the old one is retired.
func TestExplicitSaveRetiresTheMemoryItRevises(t *testing.T) {
	ctx := context.Background()
	var seed *domain.Memory
	llm := &promptFuncLLM{reply: func(p string) string {
		if strings.Contains(p, "A memory is about to be saved") {
			return `{"op":"update","target_id":"` + seed.ID + `"}`
		}
		return `{"should_store": false, "memories": []}`
	}}
	svc, fs := newFileService(t, llm)
	seed = seedResidence(t, svc)

	out, err := svc.AddReconciled(ctx, explicitMemory("User moved to Beijing"))
	require.NoError(t, err)
	assert.Equal(t, domain.MemoryOpUpdate, out.Op)
	assert.Equal(t, seed.ID, out.TargetID)
	assert.NotEmpty(t, out.ID)

	old, err := fs.Get(ctx, seed.ID)
	require.NoError(t, err)
	assert.True(t, domain.MemoryIsSuperseded(old), "the revised memory must be retired")
	require.Len(t, llm.prompts, 1)
	assert.Contains(t, llm.prompts[0], seed.ID, "the candidate is shown by id")
}

// A fact already remembered is not stored twice.
func TestExplicitSaveOfAKnownFactStoresNothing(t *testing.T) {
	ctx := context.Background()
	var seed *domain.Memory
	llm := &promptFuncLLM{reply: func(p string) string {
		return `{"op":"noop","target_id":"` + seed.ID + `"}`
	}}
	svc, fs := newFileService(t, llm)
	seed = seedResidence(t, svc)

	out, err := svc.AddReconciled(ctx, explicitMemory("The user lives in Chengdu"))
	require.NoError(t, err)
	assert.Equal(t, domain.MemoryOpNoop, out.Op)
	assert.Empty(t, out.ID)
	all, _, err := fs.List(ctx, 100, 0)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

// A verdict naming a memory the model was not shown, or no model at all,
// adds — a reconciliation that cannot run must not lose the memory.
func TestExplicitSaveFallsBackToAdd(t *testing.T) {
	ctx := context.Background()
	llm := &promptFuncLLM{reply: func(string) string { return `{"op":"update","target_id":"not-shown"}` }}
	svc, fs := newFileService(t, llm)
	seed := seedResidence(t, svc)

	out, err := svc.AddReconciled(ctx, explicitMemory("User likes tea"))
	require.NoError(t, err)
	assert.Equal(t, domain.MemoryOpAdd, out.Op)
	old, err := fs.Get(ctx, seed.ID)
	require.NoError(t, err)
	assert.False(t, domain.MemoryIsSuperseded(old))

	bare, _ := newFileService(t, nil)
	out, err = bare.AddReconciled(ctx, explicitMemory("User likes tea"))
	require.NoError(t, err)
	assert.Equal(t, domain.MemoryOpAdd, out.Op)
}

// A memory the agent saves itself gets its graph from the same reconciliation
// call — also the first one, when nothing similar is stored yet.
func TestExplicitSaveWritesTheMemoryGraph(t *testing.T) {
	ctx := context.Background()
	llm := &promptFuncLLM{reply: func(string) string {
		return `{"op":"add","target_id":"",
			"graph_entities":[{"name":"周明远","type":"person"},{"name":"花生","type":"concept"}],
			"graph_relations":[{"from":"周明远","type":"allergic_to","to":"花生"}]}`
	}}
	svc, gs := newGraphService(t, llm)

	out, err := svc.AddReconciled(ctx, explicitMemory("周明远对花生过敏"))
	require.NoError(t, err)
	require.Len(t, llm.prompts, 1, "the graph rides on the reconciliation call, even with nothing to reconcile against")
	assert.Contains(t, llm.prompts[0], "(none")
	require.Contains(t, gs.writes, out.ID)
	assert.Equal(t, []domain.MemoryGraphRelation{{From: "周明远", Type: "allergic_to", To: "花生"}}, gs.writes[out.ID].Relations)
	assert.Subset(t, llm.schemas[0].(map[string]interface{})["required"], []string{"graph_entities", "graph_relations"})
}
