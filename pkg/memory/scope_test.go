package memory

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestScopeHelpersSquadCompatibility(t *testing.T) {
	t.Run("Project helper normalizes to squad", func(t *testing.T) {
		scope := ProjectScope("alpha")
		if scope.Type != domain.MemoryScopeSquad || scope.ID != "alpha" {
			t.Fatalf("expected project helper to normalize to squad scope, got %+v", scope)
		}
	})

	t.Run("ParseBankID maps project bank ids to squad scope", func(t *testing.T) {
		scope := ParseBankID("project:alpha")
		if scope.Type != domain.MemoryScopeSquad || scope.ID != "alpha" {
			t.Fatalf("expected project bank id to map to squad scope, got %+v", scope)
		}
	})

	t.Run("ParseBankID treats raw legacy session ids as session scope ids", func(t *testing.T) {
		scope := ParseBankID("legacy-session-1")
		if scope.Type != domain.MemoryScopeSession || scope.ID != "legacy-session-1" {
			t.Fatalf("expected raw legacy session id to map to session scope, got %+v", scope)
		}
	})

	t.Run("ToBankID writes canonical squad bank ids", func(t *testing.T) {
		if got := ToBankID(domain.MemoryScope{Type: domain.MemoryScopeProject, ID: "alpha"}); got != "squad:alpha" {
			t.Fatalf("expected canonical squad bank id, got %q", got)
		}
	})
}

func TestDefaultScopeChainUsesSquadLayer(t *testing.T) {
	chain := DefaultScopeChain("sess-1", "Assistant", "alpha", "user-1")
	if len(chain) != 5 {
		t.Fatalf("unexpected chain length: %d", len(chain))
	}

	if chain[0].Type != domain.MemoryScopeSession {
		t.Fatalf("expected session scope first, got %+v", chain[0])
	}
	if chain[1].Type != domain.MemoryScopeAgent {
		t.Fatalf("expected agent scope second, got %+v", chain[1])
	}
	if chain[2].Type != domain.MemoryScopeSquad || chain[2].ID != "alpha" {
		t.Fatalf("expected squad scope third, got %+v", chain[2])
	}
	if chain[4].Type != domain.MemoryScopeGlobal {
		t.Fatalf("expected global scope last, got %+v", chain[4])
	}
}
