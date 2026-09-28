package agent

import (
	"context"
	"strings"
	"time"

	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// Task and progress persistence.
//
// The scratchpad tools give a long-horizon agent somewhere to keep a multi-step
// plan. Until now that somewhere was a package-level map: it died with the
// process, and every task in the process shared it. Both are fine for a run
// that finishes in one sitting and wrong for one that does not — a two-hour
// task that is interrupted loses the plan at exactly the moment the plan is the
// only thing worth keeping.
//
// A PlanStore fixes the first half. The second half — one plan per task rather
// than one per process — is fixed by giving each Service its own scratchpad.
//
// The interface is deliberately tiny and deliberately not CortexDB: agent-go
// should not grow a database dependency because one of its tools wants to
// remember something. It mirrors RunMemory, which took the same shape for the
// same reason.

// PlanItem is one step of a plan, and what it produced.
type PlanItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
	// Note is what the step turned out to be, or produced: the port it found,
	// the file it wrote, the approach it ruled out. It exists because "step 3
	// is done" is not enough to carry on from — a run resumed knowing only
	// which steps finished has to redo them to find out what they concluded.
	Note string `json:"note,omitempty"`
	// ID names the step so another can come After it. Optional: a plan
	// written before steps had IDs, or one with no ordering to state, leaves
	// it empty and behaves as the flat checklist it always was.
	ID string `json:"id,omitempty"`
	// After lists the IDs of steps that must be done before this one can be
	// checked. The scratchpad tools refuse a cycle or an unknown ID.
	After []string `json:"after,omitempty"`
}

// PlanStore persists a plan so a task interrupted partway can be picked up.
//
// Implementations should treat SavePlan as a full replacement of the list under
// that key, and LoadPlan on an unknown key as an empty plan and no error — a
// task that has never been planned is not an error condition.
type PlanStore interface {
	// LoadPlan returns the stored plan for a key, or nil if there is none.
	LoadPlan(ctx context.Context, key string) ([]PlanItem, error)
	// SavePlan writes the whole plan for a key.
	SavePlan(ctx context.Context, key string, items []PlanItem) error
}

// planStoreTimeout bounds a single load or save.
//
// The scratchpad is called from inside a tool call, so a store that hangs would
// hang the agent's turn. A plan is small; if writing it takes longer than this,
// something is wrong and the in-memory copy is the better answer.
const planStoreTimeout = 5 * time.Second

// loadPlan reads a key, reporting an empty plan when there is nothing to read
// or the store cannot be reached.
//
// A failure is logged and swallowed rather than returned. The alternative is a
// tool call that errors because a database was busy, which trades a lost
// checkpoint for a broken turn — the worse of the two.
func (m *scratchpadManager) loadPlan(key string) []scratchpadItem {
	if m.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), planStoreTimeout)
	defer cancel()
	items, err := m.store.LoadPlan(ctx, key)
	if err != nil {
		agentgolog.WithModule("agent.scratchpad").Warn("load plan", "key", key, "error", err)
		return nil
	}
	return scratchpadItemsFrom(items)
}

// savePlan writes a key through to the store, best effort.
//
// Called with m.mu held, and deliberately synchronous: the point of persisting
// is to survive the process ending, and a write queued on a goroutine is a
// write that loses the race with a kill. It is one small row.
func (m *scratchpadManager) savePlan(key string, list []scratchpadItem) {
	if m.store == nil {
		return
	}
	items := planItemsFrom(list)
	ctx, cancel := context.WithTimeout(context.Background(), planStoreTimeout)
	defer cancel()
	if err := m.store.SavePlan(ctx, key, items); err != nil {
		agentgolog.WithModule("agent.scratchpad").Warn("save plan", "key", key, "error", err)
	}
}

// ensureLoaded pulls a key in from the store the first time it is touched.
//
// Tracked per key rather than by "is the list empty", because an agent that
// deliberately clears its plan must not have the old one reappear underneath
// it on the next read.
//
// Called with m.mu held.
func (m *scratchpadManager) ensureLoaded(key string) {
	if m.loaded == nil {
		m.loaded = map[string]bool{}
	}
	if m.loaded[key] {
		return
	}
	m.loaded[key] = true
	if list := m.loadPlan(key); len(list) > 0 {
		m.lists[key] = list
	}
}

// PlanSummary renders a service's stored plan as text a resumed run can be
// given, or "" when there is nothing worth saying.
//
// This is the half that makes persistence useful rather than merely durable: a
// process that comes back holding a plan nobody tells the model about is a
// process that starts over. Callers inject it — through RunMemory.RecallForRun,
// a system-prompt section, or a first message — because where context belongs
// is the caller's decision, not this package's.
func (s *Service) PlanSummary(key string) string {
	if s == nil {
		return ""
	}
	// Through scratchpadStore, not s.scratchpad directly: this is normally the
	// first thing a resumed process asks, before any tool has run, so the
	// scratchpad does not exist yet and reading the field would report "no
	// plan" for every plan there is.
	items := s.scratchpadStore().get(key)
	if len(items) == 0 {
		return ""
	}
	done, annotated := 0, 0
	for _, it := range items {
		if it.Done {
			done++
			continue
		}
		if it.Note != "" {
			// An unfinished step someone left a note on is progress too — it
			// is the half-done work a previous attempt got through.
			annotated++
		}
	}
	if done == 0 && annotated == 0 {
		// Nothing has been attempted, so there is no progress to report and the
		// plan is one the model is about to write for itself anyway.
		return ""
	}

	// Only a plan that states an order gets the ready/waiting marks; a flat
	// one renders exactly as it always did, so a stored plan from before
	// steps had IDs is handed over unchanged.
	graph := planHasDependencies(items)
	ready := map[int]bool{}
	if graph {
		for _, i := range planReadySteps(items) {
			ready[i] = true
		}
	}

	out := "Plan in progress (" + itoa(done) + " of " + itoa(len(items)) + " steps done):\n"
	for i, it := range items {
		mark := "[ ]"
		if it.Done {
			mark = "[x]"
		}
		out += mark + " " + itoa(i) + ". " + it.Text
		if graph {
			out += planStepGraphLabel(items, i, ready[i])
		}
		if it.Note != "" {
			// The note is the reason to read this at all; give it its own line
			// so a long one does not bury the step it belongs to. On an
			// unfinished step it is worth more still — it is where the last
			// attempt got to, and without it the next one starts over.
			arrow := "      → "
			if !it.Done {
				arrow = "      … in progress: "
			}
			out += "\n" + arrow + it.Note
		}
		out += "\n"
	}
	if graph {
		out += "Carry on with a step marked ready; ready steps do not depend on each other and can be done in any order. " +
			"A waiting step cannot be checked until the steps it comes after are done. Do not repeat finished ones, " +
			"and pick up an in-progress one where it was left rather than starting it again."
		return out
	}
	out += "Carry on from the first unchecked step. Do not repeat finished ones, " +
		"and pick up an in-progress one where it was left rather than starting it again."
	return out
}

// planStepGraphLabel is the id/after/ready suffix a step carries in the
// summary of a plan that states an order.
func planStepGraphLabel(items []scratchpadItem, i int, ready bool) string {
	it := items[i]
	var parts []string
	if it.ID != "" {
		parts = append(parts, "id: "+it.ID)
	}
	if len(it.After) > 0 {
		parts = append(parts, "after: "+strings.Join(it.After, ", "))
	}
	label := ""
	if len(parts) > 0 {
		label = " (" + strings.Join(parts, "; ") + ")"
	}
	if it.Done {
		return label
	}
	if ready {
		return label + " — ready"
	}
	open := planOpenPredecessors(items, i)
	waits := make([]string, 0, len(open))
	for _, j := range open {
		waits = append(waits, itoa(j))
	}
	return label + " — waiting on step " + strings.Join(waits, ", ")
}

// scratchpadStore returns this service's plan lists, building them on first
// use so a Service constructed without a store still has somewhere to write.
func (s *Service) scratchpadStore() *scratchpadManager {
	s.scratchpadMu.Lock()
	defer s.scratchpadMu.Unlock()
	if s.scratchpad == nil {
		s.scratchpad = newScratchpadManager(s.planStore)
	}
	return s.scratchpad
}

// SetPlanStore attaches persistence to this service's scratchpad. Setting it
// after the scratchpad has been used rebuilds it, so an already-written plan is
// re-read from the store rather than shadowed by the in-memory one.
func (s *Service) SetPlanStore(store PlanStore) {
	s.scratchpadMu.Lock()
	defer s.scratchpadMu.Unlock()
	s.planStore = store
	s.scratchpad = newScratchpadManager(store)
}

// planSummaryForRun renders the plan a previous run left for this task, or ""
// when there is nothing to hand over.
//
// Which key to read is the awkward part. The scratchpad tools key their lists
// by an argument the model supplies, defaulting to "default" — so a plan
// written by an earlier run of this task is under the task id only if that run
// chose to say so, and under "default" otherwise. Both are checked, task first,
// because a task-scoped plan is the more specific statement of the same thing.
//
// It is read once per run rather than once per round on purpose. The summary
// rides at the end of the system prompt, and a section that changed as steps
// were ticked off would invalidate the provider's cache of the entire
// conversation after it, every turn — paying for the hand-off over and over.
// What an earlier run got through is a fact about the start of this one.
func (s *Service) planSummaryForRun(planKey, taskID string) string {
	if s == nil {
		return ""
	}
	// The run's own key is the most specific statement of where its plan is;
	// RunSegments sets it to the task-scoped list its tools write to.
	if planKey != "" {
		if summary := s.PlanSummary(planKey); summary != "" {
			return summary
		}
	}
	if taskID != "" {
		// The task's own list — what an unnamed run writes to (Runtime.planKey)
		// and what RunSegments derives. Never the shared "default" list from
		// here: a task that has no plan of its own must not be handed another
		// task's, which is what the old fallback did on every fresh session.
		if summary := s.PlanSummary(taskScopedPlanKey(taskID)); summary != "" {
			return summary
		}
		return s.PlanSummary(taskID)
	}
	return s.PlanSummary(scratchpadDefaultKey)
}

// taskScopedPlanKey is the scratchpad list a task's plan lives under when
// nobody named one. RunSegments and Runtime.planKey both derive it, so a plan
// written by one is found by the other.
func taskScopedPlanKey(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return scratchpadDefaultKey
	}
	return scratchpadDefaultKey + ":" + taskID
}
