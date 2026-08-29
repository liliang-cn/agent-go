package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// memPlanStore is a PlanStore that can be told to fail.
type memPlanStore struct {
	mu       sync.Mutex
	saved    map[string][]PlanItem
	loads    int
	saves    int
	loadErr  error
	saveErr  error
	loadWait chan struct{}
}

func newMemPlanStore() *memPlanStore {
	return &memPlanStore{saved: map[string][]PlanItem{}}
}

func (m *memPlanStore) LoadPlan(_ context.Context, key string) ([]PlanItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return append([]PlanItem(nil), m.saved[key]...), nil
}

func (m *memPlanStore) SavePlan(_ context.Context, key string, items []PlanItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved[key] = append([]PlanItem(nil), items...)
	return nil
}

func (m *memPlanStore) counts() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loads, m.saves
}

// The whole point: a plan written in one process is there in the next one.
func TestPlanSurvivesANewScratchpad(t *testing.T) {
	store := newMemPlanStore()

	first := newScratchpadManager(store)
	first.set("task-7", []string{"read the config", "dial the gateway", "write the report"})
	if _, err := first.check("task-7", 0, "config lives in /etc/app.toml"); err != nil {
		t.Fatalf("check: %v", err)
	}

	// A new manager is a new process as far as the plan is concerned.
	second := newScratchpadManager(store)
	items := second.get("task-7")
	if len(items) != 3 {
		t.Fatalf("recovered %d items, want 3: %+v", len(items), items)
	}
	if !items[0].Done {
		t.Error("the finished step came back unfinished")
	}
	if items[1].Done || items[2].Done {
		t.Error("an unfinished step came back finished")
	}
}

// "Step 3 is done" without what step 3 produced means doing it again. The note
// is the context a resumed run needs, and it is the reason this is not just a
// list of booleans.
func TestPlanCarriesWhatEachStepProduced(t *testing.T) {
	store := newMemPlanStore()
	first := newScratchpadManager(store)
	first.set("task-7", []string{"find the port", "connect"})
	if _, err := first.check("task-7", 0, "it is 47821, from settings.json"); err != nil {
		t.Fatalf("check: %v", err)
	}

	items := newScratchpadManager(store).get("task-7")
	if items[0].Note != "it is 47821, from settings.json" {
		t.Errorf("note came back as %q — a resumed run would have to redo the step", items[0].Note)
	}
}

// A key nobody has touched must not be a blank plan silently replacing a stored
// one; it must be loaded before it is read.
func TestPlanIsLoadedOnFirstTouchOfEachKey(t *testing.T) {
	store := newMemPlanStore()
	seed := newScratchpadManager(store)
	seed.set("a", []string{"one"})
	seed.set("b", []string{"two"})

	m := newScratchpadManager(store)
	if got := len(m.get("a")); got != 1 {
		t.Errorf("key a came back with %d items, want 1", got)
	}
	if got := len(m.get("b")); got != 1 {
		t.Errorf("key b came back with %d items, want 1", got)
	}
	loadsBefore, _ := store.counts()
	m.get("a")
	m.get("a")
	loadsAfter, _ := store.counts()
	if loadsAfter != loadsBefore {
		t.Errorf("re-read the store %d extra times for a key already loaded", loadsAfter-loadsBefore)
	}
}

// A store that cannot write must not take the agent down with it. Losing a
// checkpoint is recoverable; failing the tool call the agent is in the middle
// of is not.
func TestAFailingStoreDoesNotBreakThePlan(t *testing.T) {
	store := newMemPlanStore()
	store.saveErr = errors.New("disk full")
	m := newScratchpadManager(store)

	items := m.set("task-7", []string{"one", "two"})
	if len(items) != 2 {
		t.Fatalf("set returned %d items despite a write failure, want 2", len(items))
	}
	if _, err := m.check("task-7", 1, "done anyway"); err != nil {
		t.Errorf("check failed because the store did: %v", err)
	}
	if got := m.get("task-7"); !got[1].Done {
		t.Error("the in-memory plan lost a completion because the store could not record it")
	}
}

// Same for reads: an unreadable store leaves an empty plan, not a broken tool.
func TestAFailingLoadLeavesAnEmptyPlan(t *testing.T) {
	store := newMemPlanStore()
	store.loadErr = errors.New("no such file")
	m := newScratchpadManager(store)
	if got := m.get("task-7"); len(got) != 0 {
		t.Errorf("get returned %+v after a failed load, want empty", got)
	}
	if items := m.set("task-7", []string{"one"}); len(items) != 1 {
		t.Errorf("the plan was unusable after a failed load: %+v", items)
	}
}

// No store at all is the default, and it must behave exactly as before.
func TestNoStoreStillWorks(t *testing.T) {
	m := newScratchpadManager(nil)
	m.set("k", []string{"one", "two"})
	if _, err := m.check("k", 0, "note"); err != nil {
		t.Fatalf("check: %v", err)
	}
	items := m.get("k")
	if len(items) != 2 || !items[0].Done || items[0].Note != "note" {
		t.Errorf("in-memory behaviour changed: %+v", items)
	}
}

// Two tasks in one process used to share one global map, so their plans
// overwrote each other. Each service gets its own now.
func TestTwoServicesDoNotShareOnePlan(t *testing.T) {
	a := newScratchpadManager(nil)
	b := newScratchpadManager(nil)
	a.set("default", []string{"a's plan"})
	b.set("default", []string{"b's plan", "and more"})

	if got := a.get("default"); len(got) != 1 || got[0].Text != "a's plan" {
		t.Errorf("one service's plan was overwritten by another's: %+v", got)
	}
}

func TestCheckRejectsAnOutOfRangeIndex(t *testing.T) {
	m := newScratchpadManager(nil)
	m.set("k", []string{"one"})
	if _, err := m.check("k", 5, ""); err == nil {
		t.Error("checking item 5 of a 1-item list was accepted")
	}
	if _, err := m.check("k", -1, ""); err == nil {
		t.Error("checking item -1 was accepted")
	}
}

// The whole feature end to end, through the tools the model actually calls:
// plan a task, finish two steps recording what they produced, lose the process,
// and come back with something worth putting in a prompt.
func TestAResumedRunIsToldWhereItGotTo(t *testing.T) {
	store := newMemPlanStore()

	before := &Service{planStore: store}
	pad := before.scratchpadStore()
	pad.set("build-the-thing", []string{
		"find the gateway port",
		"write the client",
		"run the tests",
	})
	if _, err := pad.check("build-the-thing", 0, "port 47821, from settings.json"); err != nil {
		t.Fatalf("check 0: %v", err)
	}
	if _, err := pad.check("build-the-thing", 1, "client.go, uses grpc.NewClient"); err != nil {
		t.Fatalf("check 1: %v", err)
	}

	// A different Service is a different process as far as the plan is concerned.
	after := &Service{planStore: store}
	summary := after.PlanSummary("build-the-thing")

	for _, want := range []string{
		"2 of 3 steps done",
		"port 47821, from settings.json", // what step 0 concluded
		"client.go, uses grpc.NewClient", // what step 1 produced
		"run the tests",                  // what is left
		"Carry on from the first unchecked step",
	} {
		if !contains(summary, want) {
			t.Errorf("the resumed run was not told %q.\nSummary was:\n%s", want, summary)
		}
	}
}

// A plan nobody has started is not worth spending context on: the model is
// about to write one for itself.
func TestPlanSummaryIsEmptyBeforeAnythingIsDone(t *testing.T) {
	store := newMemPlanStore()
	s := &Service{planStore: store}
	s.scratchpadStore().set("k", []string{"one", "two"})
	if got := s.PlanSummary("k"); got != "" {
		t.Errorf("summary for an untouched plan = %q, want empty", got)
	}
}

func TestPlanSummaryIsEmptyWithNoPlan(t *testing.T) {
	s := &Service{}
	if got := s.PlanSummary("nothing-here"); got != "" {
		t.Errorf("summary = %q, want empty", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
