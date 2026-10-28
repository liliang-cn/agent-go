package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

type slowLLM struct{ prefixLLM }

func (s *slowLLM) StreamWithTools(ctx context.Context, m []domain.Message, d []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	time.Sleep(40 * time.Millisecond)
	return s.prefixLLM.StreamWithTools(ctx, m, d, o, cb)
}

type turnEnds struct {
	BaseObserver
	mu   sync.Mutex
	ttft []int64
}

func (t *turnEnds) OnModelEnd(_ context.Context, i ModelInfo, r *ModelResult, _ error) {
	if i.Purpose == "turn" && r != nil {
		t.mu.Lock()
		t.ttft = append(t.ttft, r.TTFTMs)
		t.mu.Unlock()
	}
}

// Every turn's ModelResult said its first output came after 0ms: the runtime
// builds the turn's result itself and never carried the time across, though
// the streaming call measured it.
func TestATurnReportsItsTimeToFirstOutput(t *testing.T) {
	t.Parallel()
	obs := &turnEnds{}
	svc, err := New("ttft").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&slowLLM{}).WithObserver(obs).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.ttft) == 0 || obs.ttft[0] < 35 {
		t.Fatalf("turn ttft = %v, want the 40ms wait", obs.ttft)
	}
}
