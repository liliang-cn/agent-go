package planstoretest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	_ "modernc.org/sqlite"
)

// The built-in store runs the suite too, so the suite cannot drift into
// passing anything.
func TestSQLitePlanStoreConforms(t *testing.T) {
	Run(t, func(t *testing.T) agent.PlanStore {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "plans.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		s, err := agent.NewSQLitePlanStore(db)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

// A store that drops ID and After — the shape of the bug this suite exists
// for — must fail it.
type lossyStore struct{ plans map[string][]agent.PlanItem }

func (l *lossyStore) LoadPlan(_ context.Context, key string) ([]agent.PlanItem, error) {
	return l.plans[key], nil
}
func (l *lossyStore) SavePlan(_ context.Context, key string, items []agent.PlanItem) error {
	kept := make([]agent.PlanItem, len(items))
	for i, it := range items {
		kept[i] = agent.PlanItem{Text: it.Text, Done: it.Done, Note: it.Note}
	}
	l.plans[key] = kept
	return nil
}

func TestSuiteCatchesALossyStore(t *testing.T) {
	s := &lossyStore{plans: map[string][]agent.PlanItem{}}
	want := []agent.PlanItem{{ID: "a", Text: "a"}, {ID: "b", Text: "b", After: []string{"a"}}}
	save(t, s, "k", want)
	if diff(load(t, s, "k"), want) == nil {
		t.Fatal("a store that drops ID and After passed the comparison")
	}
}
