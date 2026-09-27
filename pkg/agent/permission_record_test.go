package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// permissionScriptLLM asks for a mix of destructive and harmless tools over
// two turns, then answers. The last call of the second turn is the one the
// test's approver refuses.
type permissionScriptLLM struct{ calls int32 }

func (l *permissionScriptLLM) turn() *domain.GenerationResult {
	switch atomic.AddInt32(&l.calls, 1) {
	case 1:
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{
			{ID: "call_rm_a", Function: domain.FunctionCall{Name: "rm_file", Arguments: map[string]interface{}{"path": "a.txt"}}},
			{ID: "call_ping", Function: domain.FunctionCall{Name: "ping", Arguments: map[string]interface{}{"msg": "hi"}}},
		}}
	case 2:
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{
			{ID: "call_drop", Function: domain.FunctionCall{Name: "drop_table", Arguments: map[string]interface{}{"table": "orders"}}},
			{ID: "call_rm_b", Function: domain.FunctionCall{Name: "rm_file", Arguments: map[string]interface{}{"path": "secret.txt"}}},
		}}
	default:
		return &domain.GenerationResult{Content: "all done"}
	}
}

func (l *permissionScriptLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *permissionScriptLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *permissionScriptLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.turn(), nil
}
func (l *permissionScriptLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.turn())
}
func (l *permissionScriptLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *permissionScriptLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

type rmParams struct {
	Path string `json:"path" desc:"file"`
}

type dropParams struct {
	Table string `json:"table" desc:"table"`
}

// recordingPermissionObserver collects decisions through the optional
// interface.
type recordingPermissionObserver struct {
	BaseObserver
	mu   sync.Mutex
	recs []PermissionDecisionInfo
}

func (o *recordingPermissionObserver) OnPermissionDecision(_ context.Context, info PermissionDecisionInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recs = append(o.recs, info)
}

// TestEveryDestructiveCallHasOnePermissionRecord is the measurement for "who
// approved this destructive call?". A scripted run makes three destructive
// calls and one read-only one through an approver that allows two and refuses
// one; the trace must hold exactly one permission record per destructive
// call, naming who decided. On the code before PermissionObserver existed
// this reported 0/3.
func TestEveryDestructiveCallHasOnePermissionRecord(t *testing.T) {
	buf := &syncBuffer{}
	logBuf := &syncBuffer{}
	rec := &recordingPermissionObserver{}
	var executed sync.Map
	svc, err := New("permission-record-agent").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&permissionScriptLLM{}).
		WithTool(NewTool("ping", "Ping back", func(_ context.Context, p *pingParams) (any, error) {
			return map[string]any{"ok": true}, nil
		}).WithReadOnly(true)).
		WithTool(NewTool("rm_file", "Delete a file", func(_ context.Context, p *rmParams) (any, error) {
			executed.Store("rm:"+p.Path, true)
			return map[string]any{"deleted": p.Path}, nil
		}).WithDestructive(true)).
		WithTool(NewTool("drop_table", "Drop a table", func(_ context.Context, p *dropParams) (any, error) {
			executed.Store("drop:"+p.Table, true)
			return map[string]any{"dropped": p.Table}, nil
		}).WithDestructive(true)).
		WithObserver(NewTraceWriter(buf)).
		WithObserver(NewActivityLog(logBuf)).
		WithObserver(rec).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	svc.SetPermissionPolicy(DefaultPermissionPolicy)
	svc.SetPermissionHandler(func(_ context.Context, req PermissionRequest) (*PermissionResponse, error) {
		if req.ToolArgs["path"] == "secret.txt" {
			return &PermissionResponse{Allowed: false, Reason: "not that one", DecidedBy: "alice"}, nil
		}
		return &PermissionResponse{Allowed: true, Reason: "looks fine", DecidedBy: "alice",
			Metadata: map[string]interface{}{"ticket": "OPS-7"}}, nil
	})

	events, err := svc.RunStreamWithOptions(context.Background(), "clean up", WithRunID("perm-run"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	_ = Concat(events)

	destructive := map[string]bool{"rm_file": true, "drop_table": true}
	destructiveCalls := map[string]string{}
	records := map[string][]map[string]any{}
	for _, l := range parseTrace(t, buf.String()) {
		switch l["event"] {
		case "tool_start":
			tool, _ := l["tool"].(string)
			if destructive[tool] {
				destructiveCalls[l["call_id"].(string)] = tool
			}
		case "permission":
			id, _ := l["call_id"].(string)
			records[id] = append(records[id], l)
		}
	}
	covered := 0
	for id := range destructiveCalls {
		if len(records[id]) == 1 {
			covered++
		}
	}
	t.Logf("permission coverage: %d/%d destructive calls have exactly one record (%v)", covered, len(destructiveCalls), destructiveCalls)
	if len(destructiveCalls) != 3 {
		t.Fatalf("expected 3 destructive calls dispatched, got %d (%v)", len(destructiveCalls), destructiveCalls)
	}
	if covered != len(destructiveCalls) {
		t.Fatalf("coverage %d/%d; records=%v", covered, len(destructiveCalls), records)
	}

	want := map[string]string{"fc_call_rm_a": "allowed", "fc_call_drop": "allowed", "fc_call_rm_b": "denied"}
	for id, decision := range want {
		r := records[id][0]
		if r["decision"] != decision {
			t.Errorf("%s decision = %v, want %s", id, r["decision"], decision)
		}
		if r["decider"] != "handler" || r["decided_by"] != "alice" {
			t.Errorf("%s decider = %v/%v, want handler/alice", id, r["decider"], r["decided_by"])
		}
		if r["run_id"] != "perm-run" || r["session_id"] == nil || r["task_id"] == nil {
			t.Errorf("%s record missing correlation ids: %v", id, r)
		}
		if r["destructive"] != true || r["required"] != true || r["args"] == nil {
			t.Errorf("%s record flags/args: %v", id, r)
		}
	}
	if r := records["fc_call_rm_b"][0]; r["reason"] != "not that one" {
		t.Errorf("denial reason = %v", r["reason"])
	}
	if md, _ := records["fc_call_drop"][0]["metadata"].(map[string]any); md["ticket"] != "OPS-7" {
		t.Errorf("handler metadata dropped: %v", records["fc_call_drop"][0])
	}
	// The read-only call is recorded too, as needing no approval.
	if got := records["fc_call_ping"]; len(got) != 1 || got[0]["decision"] != "not_required" || got[0]["decider"] != "policy" {
		t.Errorf("ping record = %v", got)
	}

	// Recording changed no decision: the approved calls ran, the refused one
	// did not.
	for _, k := range []string{"rm:a.txt", "drop:orders"} {
		if _, ok := executed.Load(k); !ok {
			t.Errorf("%s should have run", k)
		}
	}
	if _, ok := executed.Load("rm:secret.txt"); ok {
		t.Error("the refused call ran")
	}

	// The optional interface saw the same four decisions.
	rec.mu.Lock()
	if len(rec.recs) != 4 {
		t.Errorf("PermissionObserver got %d decisions, want 4", len(rec.recs))
	}
	rec.mu.Unlock()

	// ActivityLog: denials and destructive approvals only.
	log := logBuf.String()
	if n := strings.Count(log, " perm "); n != 3 {
		t.Errorf("activity log has %d perm lines, want 3:\n%s", n, log)
	}
	if !strings.Contains(log, "perm    rm_file denied by=handler:alice") {
		t.Errorf("activity log misses the denial:\n%s", log)
	}
	if strings.Contains(log, "perm    ping") {
		t.Errorf("activity log should not narrate ungated reads:\n%s", log)
	}
}

func TestDecideToolRecordsEveryPath(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	d, err := svc.decideTool(context.Background(), PermissionRequest{ToolName: "rm", Destructive: true})
	if err != nil || d.Decision != PermissionNotRequired || d.Decider != PermissionDeciderNone || d.Required {
		t.Fatalf("no handler: %+v %v", d, err)
	}

	boom := errors.New("approver unreachable")
	svc.SetPermissionHandler(func(context.Context, PermissionRequest) (*PermissionResponse, error) {
		return nil, boom
	})
	d, err = svc.decideTool(context.Background(), PermissionRequest{ToolName: "rm", Destructive: true})
	if !errors.Is(err, boom) || d.Decision != PermissionDecisionError || d.Error != boom.Error() || !d.Required {
		t.Fatalf("handler error: %+v %v", d, err)
	}

	svc.SetPermissionHandler(func(context.Context, PermissionRequest) (*PermissionResponse, error) {
		return nil, nil
	})
	d, err = svc.decideTool(context.Background(), PermissionRequest{ToolName: "rm", Destructive: true})
	var denied PermissionDeniedError
	if !errors.As(err, &denied) || d.Decision != PermissionDenied || d.Reason != "permission denied" {
		t.Fatalf("nil response: %+v %v", d, err)
	}
}
