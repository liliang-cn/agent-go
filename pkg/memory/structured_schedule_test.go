package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func TestFilterMemoriesForPersonalScheduleQuery(t *testing.T) {
	memories := []*domain.MemoryWithScore{
		{
			Memory: &domain.Memory{
				ID:         "direct-dashboard",
				Type:       domain.MemoryTypeContext,
				Content:    "明天早上要处理一下Dashboard的事情。",
				Importance: 0.9,
				CreatedAt:  time.Now(),
			},
			Score: 0.92,
		},
		{
			Memory: &domain.Memory{
				ID:         "indirect-sanbao",
				Type:       domain.MemoryTypeFact,
				Content:    "周二三宝要去春游，然后就放假了。",
				Importance: 0.9,
				CreatedAt:  time.Now(),
			},
			Score: 0.91,
		},
	}

	filtered := FilterMemoriesForQuery("我这周有什么安排？", memories)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered memory, got %d", len(filtered))
	}
	if filtered[0].ID != "direct-dashboard" {
		t.Fatalf("expected direct dashboard memory, got %q", filtered[0].ID)
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
		SessionID:  "agent:Assistant",
		ScopeType:  domain.MemoryScopeAgent,
		ScopeID:    "Assistant",
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
		SessionID:  "agent:Assistant",
		ScopeType:  domain.MemoryScopeAgent,
		ScopeID:    "Assistant",
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

func TestFilterMemoriesForTargetProfileQuery(t *testing.T) {
	memories := []*domain.MemoryWithScore{
		{
			Memory: &domain.Memory{
				ID:      "trip",
				Type:    domain.MemoryTypeFact,
				Content: "周二三宝要去春游，然后就放假了。",
			},
			Score: 0.9,
		},
		{
			Memory: &domain.Memory{
				ID:      "meeting",
				Type:    domain.MemoryTypeContext,
				Content: "周四下午19：30开周例会。",
			},
			Score: 0.8,
		},
	}

	filtered := FilterMemoriesForQuery("三宝这周有什么安排？", memories)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 memory for target profile query, got %d", len(filtered))
	}
	if filtered[0].ID != "trip" {
		t.Fatalf("expected trip memory for 三宝 query, got %q", filtered[0].ID)
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
