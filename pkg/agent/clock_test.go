package agent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

// Everything the runtime tells the model about the time comes from the
// embedder's clock. Before WithClock the system prompt carried the host's
// time and the per-turn line the zone-shifted host time, so an app that knew
// it was 09:00 for its user still sent "01:00" beside its own statement, and
// the model reasoned from whichever it trusted: a reminder for "明早9点" was
// set for eight hours later, today.
func TestWithClockIsTheOnlyNowTheModelSees(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	pinned := time.Date(2026, 10, 29, 9, 0, 0, 0, shanghai)

	llm := extensiontest.Script(extensiontest.Answer("ok"))
	svc, err := extensiontest.NewServiceWithBuilder(agent.New("clock").WithLLM(llm).
		WithClock(func() time.Time { return pinned }).WithTimezone(shanghai), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	extensiontest.Run(t, svc, "what day is it?")

	rounds := llm.Rounds()
	if len(rounds) == 0 {
		t.Fatal("no model request was recorded")
	}
	var sent strings.Builder
	for _, m := range rounds[0] {
		sent.WriteString(m.Content)
		sent.WriteString("\n")
	}
	text := sent.String()
	for _, want := range []string{"2026-10-29 09:00", "Thursday", "UTC+08:00"} {
		if !strings.Contains(text, want) {
			t.Errorf("request does not state the embedder's now (%q missing):\n%s", want, text)
		}
	}
	host := time.Now().In(shanghai)
	if host.Format("2006-01-02 15") != pinned.Format("2006-01-02 15") {
		if strings.Contains(text, host.Format("2006-01-02 15:")) {
			t.Errorf("request still carries the host clock %s:\n%s", host.Format("2006-01-02 15:04"), text)
		}
	}
}
