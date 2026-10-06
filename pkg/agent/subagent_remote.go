package agent

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A host's own sub-agents.
//
// The framework knows two kinds of sub-agent: another Runtime on this Service
// (WithSubagents) and an agent CLI on this machine (RegisterCLIAgentTools).
// Both announce themselves to every Observer — OnSubAgentStart, OnSubAgentEnd
// with the tokens it used — so ActivityLog, TraceWriter, OTel and a usage
// extension see the work. A host that reaches an agent some other way, a
// worker over HTTP say, had no way to say so: its tool was one opaque call
// that ran for minutes, and ten workers, who failed, what they used, were
// invisible to everything that watches a run.
//
// SubAgentBracket is that announcement, for a sub-agent the host runs itself.

// SubAgentKindRemote is SubAgentInfo.Kind for work handed to an agent reached
// over a network: another process, accounted on its own, whose tokens an
// observer must not fold into this run's.
const SubAgentKindRemote = "remote"

// RemoteAgentRunResult is what a host reports when a remote sub-agent ends:
// the shape CLIAgentRunResult has for CLIs, for an agent the host reached
// itself. Usage is what the remote side said, when it said anything.
type RemoteAgentRunResult struct {
	Agent    string `json:"agent"`
	Provider string `json:"provider,omitempty"`
	// Endpoint is where the work went, for a log; never a credential.
	Endpoint string `json:"endpoint,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Failed   bool   `json:"failed"`
	Reason   string `json:"reason,omitempty"`
	Duration int64  `json:"duration_ms"`
	Model    string `json:"model,omitempty"`
	// Usage is nil when the remote side reported none, which is an honest
	// unknown rather than zero tokens.
	Usage *domain.TokenUsage `json:"usage,omitempty"`
}

// SubAgentBracket announces a sub-agent the host is about to run itself, and
// returns the function that announces its end. Call it inside a tool handler,
// with the handler's ctx, so the run and task the work belongs to are filled
// in from the context; Kind defaults to SubAgentKindRemote and SubAgentID is
// minted when empty. The end function reports once; later calls are ignored,
// so a deferred end and an explicit one do not double-count.
//
//	end := svc.SubAgentBracket(ctx, agent.SubAgentInfo{Name: worker, Goal: prompt, Provider: "superai-hive"})
//	res := askWorker(ctx, worker, prompt)
//	end(agent.RemoteAgentRunResult{Agent: worker, Usage: res.Usage, Failed: res.Failed}, err)
//
// A nil Service is a no-op, so a tool that is also used without a service
// need not check.
func (s *Service) SubAgentBracket(ctx context.Context, info SubAgentInfo) func(result any, err error) {
	if s == nil {
		return func(any, error) {}
	}
	if info.Kind == "" {
		info.Kind = SubAgentKindRemote
	}
	if info.SubAgentID == "" {
		info.SubAgentID = uuid.NewString()
	}
	if info.RunID == "" {
		info.RunID = currentRunID(ctx)
	}
	if info.SessionID == "" {
		info.SessionID = currentRunSessionID(ctx)
	}
	if info.ParentTaskID == "" {
		info.ParentTaskID = currentTaskID(getCurrentSession(ctx))
	}
	s.emitObserver(func(o Observer) { o.OnSubAgentStart(ctx, info) })
	var once sync.Once
	return func(result any, err error) {
		once.Do(func() {
			s.emitObserver(func(o Observer) { o.OnSubAgentEnd(ctx, info, result, err) })
		})
	}
}
