// Package planstoretest is the conformance suite a PlanStore has to pass
// before a long run should rely on it.
//
// It exists because a plan store can drop a field and nothing notices. superai's
// graph-backed store kept Text, Done and Note and silently lost ID and After
// when plans became dependency graphs: its own tests compared the fields they
// knew about and stayed green, and a resumed run came back with a flat
// checklist whose ordering was gone. The suite compares whole items, so a
// field added to PlanItem later is covered the day it is added.
package planstoretest

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// Factory builds the store under test. It is called once per case; return a
// fresh database, file or namespace each time.
type Factory func(t *testing.T) agent.PlanStore

// Run executes the whole suite against one store.
//
//	planstoretest.Run(t, func(t *testing.T) agent.PlanStore { return newMyStore(t) })
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("UnknownKeyIsAnEmptyPlan", func(t *testing.T) {
		got, err := newStore(t).LoadPlan(ctx, "never-planned")
		if err != nil {
			t.Fatalf("LoadPlan on an unknown key returned %v; a task never planned is not an error", err)
		}
		if len(got) != 0 {
			t.Fatalf("LoadPlan on an unknown key returned %d items", len(got))
		}
	})

	t.Run("EveryFieldSurvives", func(t *testing.T) {
		s := newStore(t)
		want := []agent.PlanItem{
			{ID: "build", Text: "build the binary", Done: true, Note: "go build ./... ok"},
			{ID: "test", Text: "run the tests", After: []string{"build"}},
			{ID: "ship", Text: "tag and push", After: []string{"build", "test"}, Note: "waiting on CI"},
			{Text: "a step with no id"},
		}
		roundTrip(t, s, "task-fields", want)
	})

	t.Run("OrderIsKept", func(t *testing.T) {
		s := newStore(t)
		var want []agent.PlanItem
		for i := 0; i < 25; i++ {
			want = append(want, agent.PlanItem{ID: fmt.Sprintf("s%02d", 24-i), Text: fmt.Sprintf("step %d", i)})
		}
		roundTrip(t, s, "task-order", want)
	})

	t.Run("SaveReplacesTheWholePlan", func(t *testing.T) {
		s := newStore(t)
		save(t, s, "task-replace", []agent.PlanItem{{ID: "a", Text: "a"}, {ID: "b", Text: "b", After: []string{"a"}}, {ID: "c", Text: "c"}})
		roundTrip(t, s, "task-replace", []agent.PlanItem{{ID: "c", Text: "c", Done: true}})
	})

	t.Run("SavingNothingClearsThePlan", func(t *testing.T) {
		s := newStore(t)
		save(t, s, "task-clear", []agent.PlanItem{{Text: "x"}})
		save(t, s, "task-clear", nil)
		got := load(t, s, "task-clear")
		if len(got) != 0 {
			t.Fatalf("after saving an empty plan, LoadPlan returned %d items", len(got))
		}
	})

	t.Run("KeysAreIndependent", func(t *testing.T) {
		s := newStore(t)
		a := []agent.PlanItem{{ID: "a1", Text: "only in a"}}
		b := []agent.PlanItem{{ID: "b1", Text: "only in b"}, {ID: "b2", Text: "second", After: []string{"b1"}}}
		save(t, s, "default:task-a", a)
		save(t, s, "default:task-b", b)
		save(t, s, "default:task-a", a) // a rewrite of one key must not touch the other
		equal(t, "default:task-a", load(t, s, "default:task-a"), a)
		equal(t, "default:task-b", load(t, s, "default:task-b"), b)
	})

	t.Run("TextIsNotMangled", func(t *testing.T) {
		s := newStore(t)
		want := []agent.PlanItem{
			{ID: "读", Text: "读取配置文件 config.toml", Note: "端口是 43510；含 \"引号\"、换行\n和 emoji 🚀"},
			{ID: "q", Text: "it's a 'quoted' step; DROP TABLE plans; --", After: []string{"读"}},
		}
		roundTrip(t, s, "task-text", want)
	})

	t.Run("ConcurrentSavesToDifferentKeys", func(t *testing.T) {
		s := newStore(t)
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				key := fmt.Sprintf("task-par-%d", i)
				if err := s.SavePlan(ctx, key, []agent.PlanItem{{ID: key, Text: key}}); err != nil {
					errs <- fmt.Errorf("%s: %w", key, err)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent SavePlan: %v", err)
		}
		for i := 0; i < 16; i++ {
			key := fmt.Sprintf("task-par-%d", i)
			equal(t, key, load(t, s, key), []agent.PlanItem{{ID: key, Text: key}})
		}
	})
}

func save(t *testing.T, s agent.PlanStore, key string, items []agent.PlanItem) {
	t.Helper()
	if err := s.SavePlan(context.Background(), key, items); err != nil {
		t.Fatalf("SavePlan(%q): %v", key, err)
	}
}

func load(t *testing.T, s agent.PlanStore, key string) []agent.PlanItem {
	t.Helper()
	got, err := s.LoadPlan(context.Background(), key)
	if err != nil {
		t.Fatalf("LoadPlan(%q): %v", key, err)
	}
	return got
}

func roundTrip(t *testing.T, s agent.PlanStore, key string, want []agent.PlanItem) {
	t.Helper()
	save(t, s, key, want)
	equal(t, key, load(t, s, key), want)
}

func equal(t *testing.T, key string, got, want []agent.PlanItem) {
	t.Helper()
	if err := diff(got, want); err != nil {
		t.Fatalf("plan %q did not come back as saved\n%v", key, err)
	}
}

// diff compares whole items, treating a nil and an empty After alike: a
// store may not distinguish them, and nothing reads the difference.
func diff(got, want []agent.PlanItem) error {
	norm := func(items []agent.PlanItem) []agent.PlanItem {
		out := make([]agent.PlanItem, len(items))
		for i, it := range items {
			if len(it.After) == 0 {
				it.After = nil
			}
			out[i] = it
		}
		return out
	}
	if g, w := norm(got), norm(want); !reflect.DeepEqual(g, w) {
		return fmt.Errorf(" got: %+v\nwant: %+v", g, w)
	}
	return nil
}
