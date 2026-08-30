package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// chattyLLM keeps calling a tool that returns a large result, so history
// grows past the compaction threshold within a few rounds.
type chattyLLM struct {
	mu    sync.Mutex
	turns int
}

func (l *chattyLLM) reply() *domain.GenerationResult {
	l.mu.Lock()
	l.turns++
	n := l.turns
	l.mu.Unlock()
	if n > 8 {
		return &domain.GenerationResult{Content: "Done reading.", FinishReason: "stop"}
	}
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:       fmt.Sprintf("call-%d", n),
			Type:     "function",
			Function: domain.FunctionCall{Name: "bulk", Arguments: map[string]interface{}{"n": float64(n)}},
		}},
		FinishReason: "tool_calls",
	}
}

func (l *chattyLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	// summarizeForCompaction goes through Generate; an empty answer makes the
	// compactor fail rather than decline, which is a different path.
	return "Earlier: the agent read bulk text several times.", nil
}
func (l *chattyLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *chattyLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(), nil
}
func (l *chattyLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply())
}
func (l *chattyLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{"summary":"folded"}`}, nil
}
func (l *chattyLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

type recordingCompactionObserver struct {
	BaseObserver
	mu   sync.Mutex
	seen []CompactionInfo
}

func (o *recordingCompactionObserver) OnCompaction(_ context.Context, info CompactionInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, info)
}

// Compaction deletes the model's working memory mid-run. It was reported on
// the event stream only, which goes to whoever called RunStream — and the
// thing you attach to a run you cannot watch is an Observer. Without this,
// the only way to notice a run compacting was to plot token counts and infer
// backwards.
func TestCompactionIsObservable(t *testing.T) {
	obs := &recordingCompactionObserver{}
	var log strings.Builder

	svc, err := New("compaction-observed").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&chattyLLM{}).
		WithObserver(obs).
		WithObserver(NewActivityLog(&log)).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// A tool whose result is big enough to cross the default threshold fast.
	svc.AddTool("bulk", "Returns a lot of text.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"text": strings.Repeat("lorem ipsum dolor sit amet ", 500)}, nil
		})

	// Name the threshold rather than lean on the default: the production
	// default is sized for real runs (60k), and a test that depends on it
	// silently changes meaning whenever that number is retuned.
	if _, err := svc.Run(context.Background(), "Read everything.",
		WithConstraintExtraction(false), WithAutoCompaction(8000, 6)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.seen) == 0 {
		t.Fatal("history was compacted but no observer heard about it")
	}
	got := obs.seen[0]
	if got.MessagesAfter >= got.MessagesBefore {
		t.Fatalf("reported no shrink: %d -> %d", got.MessagesBefore, got.MessagesAfter)
	}
	if got.Trigger == "" {
		t.Error("compaction reported without a trigger")
	}
	if !strings.Contains(log.String(), "compact") {
		t.Errorf("ActivityLog does not mention compaction:\n%s", log.String())
	}
}
