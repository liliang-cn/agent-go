package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

type subAgentRecorder struct {
	agent.BaseObserver
	mu     sync.Mutex
	starts []agent.SubAgentInfo
	ends   []agent.SubAgentInfo
	result any
	err    error
}

func (r *subAgentRecorder) Name() string { return "recorder" }
func (r *subAgentRecorder) OnSubAgentStart(_ context.Context, info agent.SubAgentInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, info)
}
func (r *subAgentRecorder) OnSubAgentEnd(_ context.Context, info agent.SubAgentInfo, result any, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ends = append(r.ends, info)
	r.result, r.err = result, err
}

// A host's own remote sub-agent, announced from inside a tool, reaches
// observers with the run it belongs to filled in, and ends exactly once.
func TestSubAgentBracketAnnouncesAHostsRemoteWorker(t *testing.T) {
	rec := &subAgentRecorder{}
	var svc *agent.Service
	fanOut := extensiontest.ToolModule("hive_command", "fan out", func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
		end := svc.SubAgentBracket(ctx, agent.SubAgentInfo{Name: "worker-0", Goal: "count", Provider: "superai-hive"})
		defer end(nil, errors.New("should not be seen: the explicit end came first"))
		end(agent.RemoteAgentRunResult{Agent: "worker-0", CostUSD: 0.02, Duration: 1200}, nil)
		return "done", nil
	})
	llm := extensiontest.Script(
		extensiontest.CallTool("hive_command", map[string]interface{}{}),
		extensiontest.Answer("all done"),
	)
	svc = extensiontest.NewService(t, llm, fanOut, rec)
	out := extensiontest.Run(t, svc, "count to ten on every worker")
	if out.Final != "all done" {
		t.Fatalf("run ended %q", out.Final)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.starts) != 1 || len(rec.ends) != 1 {
		t.Fatalf("starts=%d ends=%d, want one each", len(rec.starts), len(rec.ends))
	}
	info := rec.ends[0]
	if info.Kind != agent.SubAgentKindRemote || info.Provider != "superai-hive" || info.Name != "worker-0" {
		t.Fatalf("info = %+v", info)
	}
	if info.RunID == "" || info.SubAgentID == "" {
		t.Fatalf("run and sub-agent ids not filled: %+v", info)
	}
	if info.SessionID == "" {
		t.Fatalf("session id not filled from the tool's context: %+v", info)
	}
	res, ok := rec.result.(agent.RemoteAgentRunResult)
	if !ok || res.CostUSD != 0.02 || rec.err != nil {
		t.Fatalf("end carried result=%#v err=%v", rec.result, rec.err)
	}
}

func TestSubAgentBracketOnNilServiceIsANoOp(t *testing.T) {
	var svc *agent.Service
	end := svc.SubAgentBracket(context.Background(), agent.SubAgentInfo{Name: "w"})
	end(nil, nil)
}
