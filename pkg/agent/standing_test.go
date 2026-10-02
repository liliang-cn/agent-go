package agent_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
	_ "modernc.org/sqlite"
)

type notes struct {
	mu   sync.Mutex
	seen []agent.Notification
}

func (n *notes) Notify(_ context.Context, x agent.Notification) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = append(n.seen, x)
}

func (n *notes) ofKind(kind string) []agent.Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []agent.Notification
	for _, x := range n.seen {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func wakeDone(st *agent.Standing, id string) func() bool {
	return func() bool {
		s, ok := st.Get(id)
		return ok && s.Running == nil && s.LastWake != nil
	}
}

func args(kv ...interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// One wake: the brief reaches the model, the three tools reach the
// responsibility — a notification goes out, the notes are kept, the next
// wake is set — and it is all persisted.
func TestStandingWakeCarriesTheBriefAndTheTools(t *testing.T) {
	llm := extensiontest.Script(
		extensiontest.CallTool("standing_notify", args("message", "main is red since 10:40")),
		extensiontest.CallTool("standing_note", args("notes", "checked CI at 10:41; main red, build #512")),
		extensiontest.CallTool("standing_wake_me", args("after_seconds", float64(3600))),
		extensiontest.Answer("told them, noted, sleeping an hour"),
	)
	svc := extensiontest.NewService(t, llm)
	n := &notes{}
	st, err := agent.NewStanding(svc, agent.WithNotifier(n))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	r, err := st.Add(context.Background(), agent.Responsibility{
		Name: "ci", Goal: "main stays green", Watch: []string{"the CI dashboard"},
		Attention: "main has been red for more than ten minutes", Never: []string{"retry a job"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WakeNow(context.Background(), r.ID, "the host asked"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the wake to finish", wakeDone(st, r.ID))

	first := llm.Rounds()[0]
	brief := first[len(first)-1].Content
	for _, want := range []string{"WHAT SHOULD STAY TRUE", "main stays green", "the CI dashboard", "NEVER", "retry a job", "WHY YOU ARE AWAKE NOW (host)", "the host asked"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief lacks %q:\n%s", want, brief)
		}
	}
	if got := n.ofKind("message"); len(got) != 1 || got[0].Message != "main is red since 10:40" || got[0].ResponsibilityID != r.ID {
		t.Fatalf("notifications = %+v", got)
	}
	s, _ := st.Get(r.ID)
	if s.Responsibility.Notes != "checked CI at 10:41; main red, build #512" {
		t.Fatalf("notes = %q", s.Responsibility.Notes)
	}
	if in := time.Until(s.Responsibility.NextWake); in < 59*time.Minute || in > 61*time.Minute {
		t.Fatalf("next wake in %s, want about an hour", in)
	}
	if s.NextDueKind != agent.WakeSelf || s.LastWake.Error != "" || s.WakesToday != 1 {
		t.Fatalf("status = %+v", s)
	}

	// A second Standing over the same store finds it, notes and all.
	st2, err := agent.NewStanding(svc, agent.WithNotifier(n))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st2.Close)
	s2, ok := st2.Get(r.ID)
	if !ok || s2.Responsibility.Notes != s.Responsibility.Notes || !s2.Responsibility.NextWake.Equal(s.Responsibility.NextWake) {
		t.Fatalf("reloaded = %+v", s2)
	}
}

// An event delivered during a wake is steered into it; one delivered while
// idle starts a wake that opens with it.
func TestStandingDeliverSteersOrWakes(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	llm := extensiontest.Script(
		extensiontest.CallTool("slow", args()),
		extensiontest.Answer("first wake done"),
		extensiontest.Answer("second wake done"),
	)
	svc := extensiontest.NewService(t, llm)
	svc.AddToolWithMetadata("slow", "waits", map[string]interface{}{"type": "object"},
		func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return "ok", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, agent.ToolMetadata{InterruptBehavior: agent.InterruptBehaviorCancel})
	st, err := agent.NewStanding(svc, agent.WithNotifier(&notes{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	r, _ := st.Add(context.Background(), agent.Responsibility{Goal: "answer the hive"})
	if err := st.WakeNow(context.Background(), r.ID, "start"); err != nil {
		t.Fatal(err)
	}
	<-started
	steered, err := st.Deliver(context.Background(), r.ID, agent.StandingEvent{Source: "hive", Kind: "message", Payload: "worker-1: the ledger moved to port 5433"})
	if err != nil || !steered {
		t.Fatalf("Deliver during a wake: steered=%v err=%v", steered, err)
	}
	close(release)
	waitFor(t, "the first wake to finish", wakeDone(st, r.ID))
	second := llm.Rounds()[1]
	if !hasMessage(second, "user", "[event from hive · message") || !hasMessage(second, "user", "port 5433") {
		t.Fatalf("the steered event did not reach the model:\n%+v", second)
	}

	steered, err = st.Deliver(context.Background(), r.ID, agent.StandingEvent{Source: "hive", Payload: "worker-2: done with the backups"})
	if err != nil || steered {
		t.Fatalf("Deliver while idle: steered=%v err=%v", steered, err)
	}
	waitFor(t, "the event wake to finish", func() bool {
		s, _ := st.Get(r.ID)
		return s.Running == nil && s.LastWake != nil && s.LastWake.Kind == agent.WakeEvent
	})
	rounds := llm.Rounds()
	brief := rounds[len(rounds)-1][len(rounds[len(rounds)-1])-1].Content
	if !strings.Contains(brief, "WHY YOU ARE AWAKE NOW (event)") || !strings.Contains(brief, "done with the backups") {
		t.Fatalf("the event wake did not open with the event:\n%s", brief)
	}
}

// Past the day's wake ceiling the responsibility pauses itself and says so.
func TestStandingDailyLimitPausesAndNotifies(t *testing.T) {
	llm := extensiontest.Script(extensiontest.Answer("one"), extensiontest.Answer("two"))
	svc := extensiontest.NewService(t, llm)
	n := &notes{}
	st, err := agent.NewStanding(svc, agent.WithNotifier(n))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	r, _ := st.Add(context.Background(), agent.Responsibility{Goal: "x", MaxWakesPerDay: 1})
	_ = st.WakeNow(context.Background(), r.ID, "first")
	waitFor(t, "the first wake", wakeDone(st, r.ID))
	_ = st.WakeNow(context.Background(), r.ID, "second")
	waitFor(t, "the pause notification", func() bool { return len(n.ofKind("paused")) == 1 })
	s, _ := st.Get(r.ID)
	if !s.Responsibility.Paused || !strings.Contains(s.Responsibility.PausedReason, "daily limit") || s.WakesToday != 1 {
		t.Fatalf("status = %+v", s)
	}
	if llm.Calls() != 1 {
		t.Fatalf("model calls = %d, want 1: the second wake must not have run", llm.Calls())
	}
}

// A scan offers only tools that declared ReadOnly: a destructive tool the
// model calls anyway never runs.
func TestStandingScanOffersOnlyReadingTools(t *testing.T) {
	var deleted, read atomic.Int32
	llm := extensiontest.Script(
		extensiontest.CallTool("wipe_disk", args()),
		extensiontest.CallTool("read_status", args()),
		extensiontest.Answer("scanned"),
	)
	svc := extensiontest.NewService(t, llm)
	svc.AddToolWithMetadata("wipe_disk", "destroys", map[string]interface{}{"type": "object"},
		func(context.Context, map[string]interface{}) (interface{}, error) { deleted.Add(1); return "gone", nil },
		agent.ToolMetadata{Destructive: true, InterruptBehavior: agent.InterruptBehaviorCancel})
	svc.AddToolWithMetadata("read_status", "reads", map[string]interface{}{"type": "object"},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			read.Add(1)
			return "all good", nil
		},
		agent.ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: agent.InterruptBehaviorCancel})
	st, err := agent.NewStanding(svc, agent.WithNotifier(&notes{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	r, _ := st.Add(context.Background(), agent.Responsibility{Goal: "keep an eye on the host", Scan: &agent.ScanPolicy{Every: 30 * time.Millisecond}})
	waitFor(t, "the scan to finish", func() bool {
		s, _ := st.Get(r.ID)
		return s.Running == nil && s.LastWake != nil && s.LastWake.Kind == agent.WakeScan
	})
	if deleted.Load() != 0 {
		t.Fatal("a destructive tool ran during a scan")
	}
	if read.Load() != 1 {
		t.Fatalf("the reading tool ran %d times, want 1", read.Load())
	}
	brief := llm.Rounds()[0]
	if !strings.Contains(brief[len(brief)-1].Content, "This is a scan: look, do not act") {
		t.Fatal("the scan brief does not say it is a scan")
	}
}

// The standing tools refuse a run that is not a wake, and the run goes on.
func TestStandingToolsRefuseAnOrdinaryRun(t *testing.T) {
	llm := extensiontest.Script(
		extensiontest.CallTool("standing_notify", args("message", "hello")),
		extensiontest.Answer("carried on"),
	)
	svc := extensiontest.NewService(t, llm)
	n := &notes{}
	st, err := agent.NewStanding(svc, agent.WithNotifier(n))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	out := extensiontest.Run(t, svc, "just chat")
	if out.Final != "carried on" {
		t.Fatalf("run ended %q", out.Final)
	}
	if len(n.seen) != 0 {
		t.Fatalf("an ordinary run notified: %+v", n.seen)
	}
	if !hasMessage(llm.Rounds()[1], "tool", "not one") {
		t.Fatalf("the refusal did not reach the model:\n%+v", llm.Rounds()[1])
	}
}

func TestSQLiteStandingStoreRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "standing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := agent.NewSQLiteStandingStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := agent.Responsibility{ID: "r1", Goal: "g", Watch: []string{"a", "b"}, Every: time.Hour, Notes: "n", NextWake: time.Now().Add(time.Hour).Round(0)}
	if err := s.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Notes = "n2"
	if err := s.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil || len(got) != 1 || got[0].Notes != "n2" || got[0].Every != time.Hour || len(got[0].Watch) != 2 || !got[0].NextWake.Equal(r.NextWake) {
		t.Fatalf("loaded %+v (err %v)", got, err)
	}
	if err := s.Delete(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(ctx); len(got) != 0 {
		t.Fatalf("still %d after delete", len(got))
	}
}
