package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func TestOffCostsNothingAndRecordsNothing(t *testing.T) {
	SetEnabled(false)
	ctx := WithRun(context.Background(), "r1")
	c := Model(ctx, "turn")
	c.Output()
	c.End(nil, nil)
	Step(ctx, "tool", "bash")(nil)
	if _, _, ok := Timeline(ctx); ok {
		t.Fatal("a run was recorded while tracing was off")
	}
}

func TestARunsTimelineLinesAndFile(t *testing.T) {
	var lineBuf, fileBuf bytes.Buffer
	SetEnabled(true)
	SetOutput(&lineBuf)
	SetFile(&fileBuf)
	SetBodies(true)
	t.Cleanup(func() { SetEnabled(false); SetLines(false); setFile(nil); SetBodies(false) })

	ctx := WithRun(context.Background(), "run-abcdef123")
	pick := Model(WithPurpose(ctx, "memory-pick"), "")
	time.Sleep(5 * time.Millisecond)
	pick.End(nil, nil)

	turn := Model(ctx, "turn", "tools=3")
	turn.Request([]string{"hello"})
	turn.Streamed()
	time.Sleep(5 * time.Millisecond)
	turn.Output()
	turn.Reply("hi")
	turn.End(&domain.TokenUsage{PromptTokens: 100, CompletionTokens: 7}, nil)

	Step(ctx, "tool", "web_search")(errors.New("timed out"))
	End(ctx, "end")

	recs, sum, ok := Timeline(ctx)
	if !ok || len(recs) != 4 {
		t.Fatalf("timeline: ok=%v %d records", ok, len(recs))
	}
	if recs[0].Name != "memory-pick" || recs[1].Name != "turn" || recs[2].Kind != "tool" || recs[3].Kind != "run" {
		t.Fatalf("order: %+v", recs)
	}
	if recs[1].Request != nil {
		t.Fatal("bodies kept in the in-memory timeline")
	}
	if sum.ModelCalls != 2 || sum.ToolCalls != 1 || sum.FirstAnswerTokenMs <= 0 || sum.FirstAnswerTokenMs < sum.FirstModelCallMs {
		t.Fatalf("summary: %+v", sum)
	}

	out := lineBuf.String()
	for _, want := range []string{"run=run-abcd model purpose=memory-pick", "model purpose=turn tools=3", "in=100 out=7", `tool web_search`, `err="timed out"`, "run end total="} {
		if !strings.Contains(out, want) {
			t.Fatalf("lines lack %q:\n%s", want, out)
		}
	}

	var n int
	var withBody bool
	for _, l := range strings.Split(strings.TrimSpace(fileBuf.String()), "\n") {
		var r Record
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("bad JSON line %q: %v", l, err)
		}
		if r.Name == "turn" && r.Request != nil && r.Reply == "hi" {
			withBody = true
		}
		n++
	}
	if n != 4 || !withBody {
		t.Fatalf("file: %d records, bodies=%v\n%s", n, withBody, fileBuf.String())
	}
}
