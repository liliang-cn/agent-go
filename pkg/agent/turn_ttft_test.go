package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// slowLLM waits, then answers as a streaming client does: its result says it
// came off a stream. whole says the opposite — one JSON document.
type slowLLM struct {
	prefixLLM
	whole bool
}

func (s *slowLLM) StreamWithTools(ctx context.Context, m []domain.Message, d []domain.ToolDefinition, o *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	time.Sleep(40 * time.Millisecond)
	return s.prefixLLM.StreamWithTools(ctx, m, d, o, func(r *domain.GenerationResult) error {
		r.Streamed = !s.whole
		return cb(r)
	})
}

type turnEnds struct {
	BaseObserver
	mu       sync.Mutex
	ttft     []int64
	streamed []bool
}

func (t *turnEnds) OnModelEnd(_ context.Context, i ModelInfo, r *ModelResult, _ error) {
	if i.Purpose == "turn" && r != nil {
		t.mu.Lock()
		t.ttft = append(t.ttft, r.TTFTMs)
		t.streamed = append(t.streamed, r.Streamed)
		t.mu.Unlock()
	}
}

// A turn whose answer came whole has no time to first output: its first byte
// was its last, and calling the whole wait "ttft" said a turn took that long
// to start.
func TestAWholeAnswerHasNoTimeToFirstOutput(t *testing.T) {
	t.Parallel()
	obs := &turnEnds{}
	svc, err := New("ttft-whole").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&slowLLM{whole: true}).WithObserver(obs).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.ttft) == 0 || obs.ttft[0] != 0 || obs.streamed[0] {
		t.Fatalf("whole answer: ttft=%v streamed=%v, want 0 and false", obs.ttft, obs.streamed)
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
	if len(obs.ttft) == 0 || obs.ttft[0] < 35 || !obs.streamed[0] {
		t.Fatalf("turn ttft = %v streamed = %v, want the 40ms wait on a stream", obs.ttft, obs.streamed)
	}
}
