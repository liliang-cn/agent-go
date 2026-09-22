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

// routeCase is one goal and the route it belongs on.
type routeCase struct {
	text string
	want string // ModelRoute.Name
}

// TestDecisionRouterAgainstLiveEngine calibrates routing the way
// TestDecisionGateAgainstLiveEngine calibrates the gate.
//
//	AGENTGO_DECISION_LIVE=1 go test ./pkg/agent -run TestDecisionRouterAgainstLive -v
//
// The descriptions here are the ones in WithModelRoutes' own example, so a
// change to the advice in that doc comment should be run past this.
func TestDecisionRouterAgainstLiveEngine(t *testing.T) {
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

	routes := []ModelRoute{
		{Name: "fast", Model: "cheap-model",
			Description: "A direct lookup or a short factual answer"},
		{Name: "deep", Model: "smart-model",
			Description: "Design, debugging, or a decision with consequences"},
	}
	router := NewDecisionRouter(engine, DefaultDecisionConfidence, routes...).(ConfidentModelRouter)

	corpus := []routeCase{
		{"What is a mutex?", "fast"},
		{"Go 的 channel 是什么", "fast"},
		{"What port does Postgres listen on by default?", "fast"},
		{"列一下这个目录里的文件", "fast"},

		{"Our checkout loses about 2% of orders under load. Find out why.", "deep"},
		{"设计一套跨三个服务的幂等写入方案", "deep"},
		{"Should we move the storage layer from SQLite to Postgres?", "deep"},
		{"这个 goroutine 泄漏排查了两天了，帮我找出来", "deep"},
	}

	var routed, correct, unsure int
	for _, c := range corpus {
		route, confidence, ok := router.RouteWithConfidence(ctx, c.text)
		if !ok {
			unsure++
			t.Logf("  unsure  (%.2f)          %s", confidence, c.text)
			continue
		}
		routed++
		mark := "  ok    "
		if route.Name != c.want {
			mark = "  WRONG "
			correct--
		}
		correct++
		t.Logf("%s %-5s (%.2f) want %-5s %s", mark, route.Name, confidence, c.want, c.text)
	}

	t.Logf("routed %d/%d, %d correct, %d left to the pool (floor %.2f)",
		routed, len(corpus), correct, unsure, DefaultDecisionConfidence)

	// Routing the wrong way costs money or quality; routing nowhere costs
	// neither, so the bar is on the ones it acted on.
	if routed > 0 && correct < routed {
		t.Errorf("%d of %d routes went to the wrong model", routed-correct, routed)
	}
}
