package runner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
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

	// toolsOffered records, per call, how many tool definitions the runtime
	// attached to the request. Scenarios assert on it to pin runtime-level
	// tool policy (e.g. "a task that forbids tools must be offered none").
	toolsOffered []int

	// constraints is the scripted reply to the runtime's constraint-extraction
	// call, so a scenario can exercise the forbid-tools / deliverables gates
	// end to end instead of relying on the runtime guessing from the goal text.
	constraints string
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

// MaxToolsOffered returns the largest number of tools the runtime attached to
// any single model call during the run.
func (m *MockLLM) MaxToolsOffered() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	max := 0
	for _, n := range m.toolsOffered {
		if n > max {
			max = n
		}
	}
	return max
}

func (m *MockLLM) noteTools(tools []domain.ToolDefinition) {
	m.mu.Lock()
	m.toolsOffered = append(m.toolsOffered, len(tools))
	m.mu.Unlock()
}

// toolCallReplyPrefix marks a scripted reply as a tool call rather than text:
//
//	llm_replies:
//	  - 'tool:task_blocked {"blocker":"I have no email tool."}'
//
// Without this a scenario cannot reach any path that keys off a terminal tool
// call, and would silently exercise the free-text path instead.
const toolCallReplyPrefix = "tool:"

// replyResult turns the next scripted reply into either text or a tool call.
func (m *MockLLM) replyResult() *domain.GenerationResult {
	raw := m.nextReply()
	if !strings.HasPrefix(raw, toolCallReplyPrefix) {
		return &domain.GenerationResult{Content: raw}
	}
	spec := strings.TrimSpace(strings.TrimPrefix(raw, toolCallReplyPrefix))
	name, argsJSON, _ := strings.Cut(spec, " ")
	args := map[string]interface{}{}
	if strings.TrimSpace(argsJSON) != "" {
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:       "mock-" + strings.TrimSpace(name),
			Type:     "function",
			Function: domain.FunctionCall{Name: strings.TrimSpace(name), Arguments: args},
		}},
	}
}

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

// SetConstraints scripts the reply to the runtime's constraint-extraction call.
// raw is the JSON body the model would have returned, e.g.
// `{"forbid_tools":true,"deliverables":[]}`. Empty means "no constraints".
func (m *MockLLM) SetConstraints(raw string) {
	m.mu.Lock()
	m.constraints = raw
	m.mu.Unlock()
}

func (m *MockLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return m.nextReply(), nil
}

func (m *MockLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (m *MockLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	m.noteTools(tools)
	return m.replyResult(), nil
}

func (m *MockLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	m.noteTools(tools)
	return callback(m.replyResult())
}

func (m *MockLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if strings.Contains(prompt, "report ONLY the constraints") {
		m.mu.Lock()
		raw := m.constraints
		m.mu.Unlock()
		if raw == "" {
			raw = `{"forbid_tools":false,"deliverables":[]}`
		}
		return &domain.StructuredResult{Raw: raw, Valid: true}, nil
	}
	return &domain.StructuredResult{Data: map[string]interface{}{}, Raw: "{}", Valid: true}, nil
}

func (m *MockLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}
