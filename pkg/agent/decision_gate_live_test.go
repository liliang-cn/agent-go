package agent

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/decision"
)

// gateCase is one goal and what the constraint extraction should say about it.
type gateCase struct {
	text        string
	constrained bool
}

// gateCorpus is deliberately half constrained and half not, in two languages.
// The unconstrained half is where the saving comes from; the constrained half
// is where a wrong skip would cost a lint its evidence.
var gateCorpus = []gateCase{
	{"Write the release notes to CHANGELOG.md", true},
	{"给张伟发一封邮件，说会议改到周四", true},
	{"Send a slack message to the team that the deploy is done", true},
	{"create a directory wordfreq with a main.go in it", true},
	{"帮我设个提醒，明天十点开会", true},
	{"把结果保存到 report.md", true},
	{"不要用任何工具，直接告诉我 Go 的 channel 是什么", true},
	{"Don't use any tools. Just explain what a mutex is.", true},
	{"add a calendar entry for the review on Friday", true},
	{"记一条笔记：下周要交季度报告", true},

	{"解释一下 Go 的 channel 是什么", false},
	{"What is a mutex?", false},
	{"把这个 bug 修好，跑一遍测试", false},
	{"Refactor this function to be clearer", false},
	{"为什么我的测试挂了？", false},
	{"Review this code for concurrency issues", false},
	{"这段代码什么意思", false},
	{"How does the prompt cache work in this repo?", false},
	{"帮我想三个给这个功能起的名字", false},
	{"Compare BM25 and vector search", false},
}

type gateRecorder struct {
	BaseObserver
	last DecisionInfo
}

func (g *gateRecorder) OnDecision(_ context.Context, info DecisionInfo) { g.last = info }

// TestDecisionGateAgainstLiveEngine calibrates the gate against a real engine.
//
//	AGENTGO_DECISION_LIVE=1 go test ./pkg/agent -run TestDecisionGateAgainstLive -v
//
// It is skipped by default because it needs a laya-serve. Run it after
// changing constraintGateQuestions or DefaultDecisionConfidence: both were
// chosen from what this prints, and neither can be reasoned about from the
// code. Point it elsewhere with AGENTGO_DECISION_URL.
func TestDecisionGateAgainstLiveEngine(t *testing.T) {
	if os.Getenv("AGENTGO_DECISION_LIVE") != "1" {
		t.Skip("set AGENTGO_DECISION_LIVE=1 and run a decision engine")
	}

	var opts []decision.LayaOption
	if url := os.Getenv("AGENTGO_DECISION_URL"); url != "" {
		opts = append(opts, decision.WithLayaURL(url))
	}
	engine := decision.NewLaya(opts...)

	ctx := context.Background()
	if err := engine.Ready(ctx); err != nil {
		t.Skipf("no decision engine reachable: %v", err)
	}

	rec := &gateRecorder{}
	svc := &Service{
		decisionEngine:     engine,
		decisionConfidence: DefaultDecisionConfidence,
		logger:             slog.Default(),
		observers:          []Observer{rec},
	}

	var latencies []time.Duration
	skipped, wrong := 0, 0
	for _, c := range gateCorpus {
		didSkip := svc.goalNeedsNoConstraints(ctx, c.text)
		latencies = append(latencies, rec.last.Duration)
		if !didSkip {
			continue
		}
		skipped++
		if c.constrained {
			wrong++
			// A wrong skip means a run that asked for a file completes
			// without file_task_must_write ever knowing it should have
			// looked. That is the failure this floor exists to prevent.
			t.Errorf("skipped the extraction for a constrained goal (conf %.2f, weakest %s): %s",
				rec.last.Confidence, rec.last.Label, c.text)
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("floor %.2f: skipped %d/%d extractions, %d wrongly; gate median %v, p90 %v",
		DefaultDecisionConfidence, skipped, len(gateCorpus), wrong,
		latencies[len(latencies)/2].Round(time.Millisecond),
		latencies[len(latencies)*9/10].Round(time.Millisecond))

	// A gate that never fires is pure added latency. This is not a quality
	// bar for the engine, it is a smoke test for the wiring: if none of ten
	// plainly unconstrained goals clears the floor, something is broken.
	if skipped == 0 {
		t.Error("the gate never skipped anything; it is costing latency and saving nothing")
	}
}
