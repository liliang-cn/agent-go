package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Retrieval must return what the store ranked, whatever the request happens to
// say. The deleted FilterMemoriesForQuery re-read the query after ranking and
// dropped results: "我这周有什么安排？" kept nothing at all once every ranked
// memory had a third-party subject, "帮我做一个学习计划" wiped the whole set, and
// "…plan for the api service" took the personal-schedule branch because "api "
// contains "i ". These cases pin that a schedule-shaped query keeps the
// non-schedule and third-party memories the ranker returned.
func TestScheduleShapedQueriesKeepEveryRankedMemory(t *testing.T) {
	ctx := context.Background()

	stored := []*domain.MemoryWithScore{
		{
			Memory: &domain.Memory{
				ID:         "third-party-trip",
				SessionID:  "session-1",
				ScopeType:  domain.MemoryScopeSession,
				ScopeID:    "session-1",
				Type:       domain.MemoryTypeFact,
				Content:    "周二三宝要去春游，然后就放假了。",
				Importance: 0.9,
				CreatedAt:  time.Now(),
			},
			Score: 0.93,
		},
		{
			Memory: &domain.Memory{
				ID:         "third-party-meeting",
				SessionID:  "session-1",
				ScopeType:  domain.MemoryScopeSession,
				ScopeID:    "session-1",
				Type:       domain.MemoryTypeFact,
				Content:    "老板明天要开会讨论我的项目",
				Importance: 0.9,
				CreatedAt:  time.Now(),
			},
			Score: 0.92,
		},
		{
			Memory: &domain.Memory{
				ID:         "owner-task",
				SessionID:  "session-1",
				ScopeType:  domain.MemoryScopeSession,
				ScopeID:    "session-1",
				Type:       domain.MemoryTypeContext,
				Content:    "明天早上要处理一下Dashboard的事情。",
				Importance: 0.9,
				CreatedAt:  time.Now(),
			},
			Score: 0.91,
		},
		{
			Memory: &domain.Memory{
				ID:         "plain-note",
				SessionID:  "session-1",
				ScopeType:  domain.MemoryScopeSession,
				ScopeID:    "session-1",
				Type:       domain.MemoryTypeFact,
				Content:    "部署脚本在 scripts/deploy.sh，走 staging 再上生产。",
				Importance: 0.8,
				CreatedAt:  time.Now(),
			},
			Score: 0.90,
		},
	}

	queries := []struct {
		name  string
		query string
	}{
		{"personal schedule", "我这周有什么安排？"},
		{"named third party", "三宝这周有什么安排？"},
		{"third party plus self", "三宝和我这周有什么安排？"},
		{"household schedule", "家里这周的安排"},
		{"planning request", "帮我做一个学习计划"},
		{"english plan with api", "what is the deployment plan for the api service"},
		{"english agenda", "what is on my agenda this week"},
	}

	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			memStore := new(MockMemoryStore)
			memStore.On("SearchByText", ctx, tc.query, mock.AnythingOfType("int")).Return(stored, nil)
			memStore.On("IncrementAccess", ctx, mock.AnythingOfType("string")).Return(nil)

			svc := NewService(memStore, nil, nil, DefaultConfig())

			_, memories, _, err := svc.RetrieveAndInjectWithContextAndLogic(
				ctx, tc.query, domain.MemoryQueryContext{SessionID: "session-1"})

			assert.NoError(t, err)

			ids := make([]string, 0, len(memories))
			for _, m := range memories {
				ids = append(ids, m.ID)
			}
			assert.ElementsMatch(t,
				[]string{"third-party-trip", "third-party-meeting", "owner-task", "plain-note"},
				ids,
				"retrieval dropped ranked memories based on the wording of the query")
		})
	}
}

func TestServiceAddAppliesStructuredCorrectionToPriorEvent(t *testing.T) {
	ctx := context.Background()
	memStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	svc := NewService(memStore, nil, nil, DefaultConfig())
	initial := &domain.Memory{
		ID:         "trip-initial",
		SessionID:  "agent:Responder",
		ScopeType:  domain.MemoryScopeAgent,
		ScopeID:    "Responder",
		Type:       domain.MemoryTypeFact,
		Content:    "周二三宝要去春游，然后就放假了。",
		Importance: 0.8,
		CreatedAt:  time.Now(),
	}
	if err := svc.Add(ctx, initial); err != nil {
		t.Fatalf("add initial memory failed: %v", err)
	}

	correction := &domain.Memory{
		ID:         "trip-correction",
		SessionID:  "agent:Responder",
		ScopeType:  domain.MemoryScopeAgent,
		ScopeID:    "Responder",
		Type:       domain.MemoryTypeFact,
		Content:    "三宝是跟着学校去春游，不用我。",
		Importance: 0.85,
		CreatedAt:  time.Now(),
	}
	if err := svc.Add(ctx, correction); err != nil {
		t.Fatalf("add correction memory failed: %v", err)
	}

	updated, err := svc.Get(ctx, initial.ID)
	if err != nil {
		t.Fatalf("get updated memory failed: %v", err)
	}

	event, ok := domain.GetMemoryEventMetadata(updated.Metadata)
	if !ok {
		t.Fatalf("expected structured event metadata on updated memory, got %+v", updated.Metadata)
	}
	if event.RequiresUser {
		t.Fatalf("expected corrected memory to not require user, got %+v", event)
	}
	if event.UserRole != domain.MemoryUserRoleNotInvolved {
		t.Fatalf("expected not_involved user role, got %+v", event)
	}
	if event.UpdatedByMemoryID != correction.ID {
		t.Fatalf("expected UpdatedByMemoryID %q, got %+v", correction.ID, event)
	}
	if !containsString(event.OrganizerProfiles, "学校") {
		t.Fatalf("expected organizer profile 学校, got %+v", event.OrganizerProfiles)
	}
}

// The structured extraction that survives is content-side: it reads what was
// stored, never the request. This pins the enrichment that Add() applies.
func TestEnrichStructuredMemoryReadsOnlyMemoryContent(t *testing.T) {
	trip := &domain.Memory{
		ID:      "trip",
		Type:    domain.MemoryTypeFact,
		Content: "周二三宝要去春游，然后就放假了。",
	}
	enrichStructuredMemory(trip)

	event, ok := domain.GetMemoryEventMetadata(trip.Metadata)
	if !ok {
		t.Fatalf("expected event metadata on the trip memory, got %+v", trip.Metadata)
	}
	if !containsString(event.SubjectProfiles, "三宝") {
		t.Fatalf("expected subject profile 三宝, got %+v", event.SubjectProfiles)
	}
	if event.EventType != "trip" {
		t.Fatalf("expected trip event type, got %q", event.EventType)
	}
	// The time expression is deliberately NOT derived here any more. It used
	// to come from a regular expression listing 今天/明天/周二…, which served
	// Chinese and silently nothing else. It is now resolved by the model on
	// the write path, together with the absolute date it means, and this
	// enrichment leaves it alone.
	if event.TimeExpression != "" {
		t.Fatalf("time is resolved by the model, not by a pattern here; got %q", event.TimeExpression)
	}
	if !containsString(trip.Keywords, "三宝") {
		t.Fatalf("expected 三宝 in keywords, got %+v", trip.Keywords)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
