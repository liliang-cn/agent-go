package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

// bigReadLLM asks for one fs_read of a large file, then records what it was
// handed on the next turn and stops.
type bigReadLLM struct {
	mu    sync.Mutex
	turns int
	args  map[string]interface{}
	seen  []domain.Message
}

func (l *bigReadLLM) reply(msgs []domain.Message) *domain.GenerationResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turns++
	if l.turns == 1 {
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID: "read-1", Type: "function",
				Function: domain.FunctionCall{Name: "fs_read", Arguments: l.args},
			}},
			FinishReason: "tool_calls",
		}
	}
	l.seen = append([]domain.Message(nil), msgs...)
	return &domain.GenerationResult{Content: "The log has been read.", FinishReason: "stop"}
}

func (l *bigReadLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *bigReadLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *bigReadLLM) GenerateWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(m), nil
}
func (l *bigReadLLM) StreamWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply(m))
}
func (l *bigReadLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{"should_store":false}`}, nil
}
func (l *bigReadLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// writeBigLog writes n log lines of roughly the width a real service log has.
func writeBigLog(t *testing.T, dir string, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "2026-09-27T10:%02d:%02d.%03dZ INFO worker-%d request id=%06d handled path=/api/v1/items status=200 dur=%dms\n",
			(i/60)%60, i%60, i%1000, i%8, i, i%97)
	}
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type historyMeasure struct {
	FileBytes     int
	ToolMsgBytes  int
	HistoryBytes  int
	HistoryTokens int
	ToolMsg       string
}

// measureBigRead runs the real loop: the model calls fs_read on a
// 10,000-line file through the sandbox tool, and we measure what reached the
// next turn's history.
func measureBigRead(t *testing.T, args map[string]interface{}, opts ...func(*Builder)) historyMeasure {
	t.Helper()
	ws := t.TempDir()
	logPath := writeBigLog(t, ws, 10000)
	fi, _ := os.Stat(logPath)

	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(ws))
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	llm := &bigReadLLM{args: args}
	b := New("big-read").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithSandbox(sb)
	for _, o := range opts {
		o(b)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if _, err := svc.Run(context.Background(), "Read app.log and tell me what is in it.",
		WithConstraintExtraction(false)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(llm.seen) == 0 {
		t.Fatal("the model never got a second turn")
	}
	m := historyMeasure{FileBytes: int(fi.Size())}
	for _, msg := range llm.seen {
		m.HistoryBytes += len(msg.Content)
		if msg.Role == "tool" && len(msg.Content) > m.ToolMsgBytes {
			m.ToolMsgBytes = len(msg.Content)
			m.ToolMsg = msg.Content
		}
	}
	m.HistoryTokens = svc.estimateConversationTokens(llm.seen)
	return m
}

// Before this change (fs_read with no limit returned the whole file, and no
// cap existed) this measured: file=1038961B tool_msg=1129011B
// history=1133632B history_tokens~440085.
func TestMeasureTenThousandLineRead(t *testing.T) {
	m := measureBigRead(t, map[string]interface{}{"path": "app.log"})
	t.Logf("MEASURE fs_read(app.log, no limit): file=%dB tool_msg=%dB history=%dB history_tokens~%d",
		m.FileBytes, m.ToolMsgBytes, m.HistoryBytes, m.HistoryTokens)
	if m.ToolMsgBytes > DefaultToolOutputLimit {
		t.Fatalf("a default fs_read put %d bytes into history; the page should fit under %d",
			m.ToolMsgBytes, DefaultToolOutputLimit)
	}
	if !strings.Contains(m.ToolMsg, `"total_lines":10000`) || !strings.Contains(m.ToolMsg, "offset=") {
		t.Fatalf("a paged read must say how long the file is and where to continue:\n...%s",
			m.ToolMsg[max(0, len(m.ToolMsg)-400):])
	}
}

// An explicit limit is honoured by fs_read, so the uniform cap is what stands
// between a 10,000-line request and the context.
func TestMeasureTenThousandLineReadExplicitLimit(t *testing.T) {
	obs := &truncationRecorder{}
	var trace strings.Builder
	m := measureBigRead(t, map[string]interface{}{"path": "app.log", "limit": 10000}, func(b *Builder) {
		b.WithObserver(obs).WithObserver(NewTraceWriter(&trace))
	})
	t.Logf("MEASURE fs_read(app.log, limit=10000): file=%dB tool_msg=%dB history=%dB history_tokens~%d",
		m.FileBytes, m.ToolMsgBytes, m.HistoryBytes, m.HistoryTokens)
	if m.ToolMsgBytes > DefaultToolOutputLimit {
		t.Fatalf("tool message %d bytes, over the %d cap", m.ToolMsgBytes, DefaultToolOutputLimit)
	}
	// The structured result stays valid JSON with its small fields intact.
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(m.ToolMsg), &decoded); err != nil {
		t.Fatalf("a capped structured result must stay valid JSON: %v", err)
	}
	data, _ := decoded["data"].(map[string]interface{})
	content, _ := data["content"].(string)
	if data["total_lines"] == nil || !strings.Contains(content, "     1\t") || !strings.Contains(content, " 10000\t") {
		t.Fatalf("head, tail and total_lines must survive the cut; got data keys %v", mapKeys(data))
	}
	if !strings.Contains(content, "omitted from the middle") {
		t.Fatal("the cut must say what was omitted")
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.cuts) != 1 || obs.cuts[0].ToolName != "fs_read" || !obs.cuts[0].StructureKept || obs.cuts[0].RunID == "" {
		t.Fatalf("expected one observed fs_read cut with run ids, got %+v", obs.cuts)
	}
	if !strings.Contains(trace.String(), `"event":"tool_output_truncated"`) {
		t.Fatal("TraceWriter did not record the truncation")
	}
}

// Turning the cap off restores sending the whole result.
func TestToolOutputLimitDisabled(t *testing.T) {
	m := measureBigRead(t, map[string]interface{}{"path": "app.log", "limit": 10000}, func(b *Builder) {
		b.WithToolOutputLimit(-1)
	})
	if m.ToolMsgBytes < m.FileBytes {
		t.Fatalf("with the cap off the whole file should arrive: %d < %d", m.ToolMsgBytes, m.FileBytes)
	}
}

type truncationRecorder struct {
	BaseObserver
	mu   sync.Mutex
	cuts []ToolOutputTruncation
}

func (o *truncationRecorder) OnToolOutputTruncated(_ context.Context, info ToolOutputTruncation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cuts = append(o.cuts, info)
}

func mapKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
