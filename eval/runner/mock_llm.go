package runner

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// MockLLM is a deterministic Generator stub used by the eval runner. It
// replays a scripted sequence of free-form text completions in the order
// they were configured. After the last reply is consumed, subsequent
// requests receive the last reply repeatedly so a misconfigured scenario
// stalls predictably rather than panicking.
type MockLLM struct {
	mu      sync.Mutex
	replies []string
	calls   int32
}

// NewMockLLM constructs a MockLLM with the given scripted replies. The
// replies are consumed in order on each Generate / GenerateWithTools /
// StreamWithTools call.
func NewMockLLM(replies []string) *MockLLM {
	clone := make([]string, len(replies))
	copy(clone, replies)
	return &MockLLM{replies: clone}
}

// CallCount returns how many times the model was invoked across the
// scripted surfaces (Generate / GenerateWithTools / StreamWithTools).
func (m *MockLLM) CallCount() int { return int(atomic.LoadInt32(&m.calls)) }

func (m *MockLLM) nextReply() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := int(atomic.AddInt32(&m.calls, 1)) - 1
	if len(m.replies) == 0 {
		return ""
	}
	if idx >= len(m.replies) {
		idx = len(m.replies) - 1
	}
	return m.replies[idx]
}

func (m *MockLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return m.nextReply(), nil
}

func (m *MockLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (m *MockLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: m.nextReply()}, nil
}

func (m *MockLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return callback(&domain.GenerationResult{Content: m.nextReply()})
}

func (m *MockLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Data: map[string]interface{}{}, Raw: "{}", Valid: true}, nil
}

func (m *MockLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}
