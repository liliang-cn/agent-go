package agent

import (
	"fmt"
	"sort"
	"strings"
)

// A plan is a graph, not a list.
//
// It was a flat checklist, and a flat checklist says nothing about order: "check
// the summary" was accepted with the three steps it summarises still open, and
// a model that stopped after two of four steps finished the run cleanly. Each
// step can now name an ID and the steps it comes After. The scratchpad tools
// refuse a plan with a cycle or a dangling reference, and refuse to check a
// step whose predecessors are open; the summary a resumed run is handed marks
// which steps are ready; and a lint rejects finishing while a ready step is
// unchecked.
//
// A plan with no After edges behaves exactly as the flat list always did —
// every open step is ready, the summary reads as it did byte for byte, and a
// stored plan written before IDs existed loads unchanged.

// planStepRef names one step in structured tool feedback.
type planStepRef struct {
	Index int    `json:"index"`
	ID    string `json:"id,omitempty"`
	Text  string `json:"text"`
}

// planGraphError is a plan the tools refused, with enough structure for the
// model to fix it without re-reading the whole list.
type planGraphError struct {
	msg string
	// fields is merged into the tool's error payload.
	fields map[string]interface{}
}

func (e *planGraphError) Error() string { return e.msg }

// payload renders the refusal as the tool result the model sees.
func (e *planGraphError) payload() map[string]interface{} {
	out := toolErr(e.msg)
	for k, v := range e.fields {
		out[k] = v
	}
	return out
}

// planHasDependencies reports whether any step names a predecessor. Only then
// does anything about the plan's rendering change.
func planHasDependencies(items []scratchpadItem) bool {
	for _, it := range items {
		if len(it.After) > 0 {
			return true
		}
	}
	return false
}

// planIndexByID maps each non-empty step ID to its position.
func planIndexByID(items []scratchpadItem) map[string]int {
	byID := make(map[string]int, len(items))
	for i, it := range items {
		if it.ID != "" {
			byID[it.ID] = i
		}
	}
	return byID
}

func planRef(items []scratchpadItem, i int) planStepRef {
	return planStepRef{Index: i, ID: items[i].ID, Text: items[i].Text}
}

// validatePlanGraph refuses duplicate IDs, references to steps that do not
// exist, and cycles. A nil return means the plan is a DAG.
func validatePlanGraph(items []scratchpadItem) *planGraphError {
	seen := map[string]int{}
	for i, it := range items {
		if it.ID == "" {
			continue
		}
		if prev, dup := seen[it.ID]; dup {
			return &planGraphError{
				msg: fmt.Sprintf("plan refused: step id %q is used by steps %d and %d; ids must be unique", it.ID, prev, i),
				fields: map[string]interface{}{
					"duplicate_id": it.ID,
					"steps":        []planStepRef{planRef(items, prev), planRef(items, i)},
				},
			}
		}
		seen[it.ID] = i
	}
	for i, it := range items {
		for _, dep := range it.After {
			if _, ok := seen[dep]; !ok {
				return &planGraphError{
					msg: fmt.Sprintf("plan refused: step %d comes after %q, but no step has that id", i, dep),
					fields: map[string]interface{}{
						"step":       planRef(items, i),
						"unknown_id": dep,
					},
				}
			}
		}
	}
	if cycle := planCycle(items, seen); len(cycle) > 0 {
		names := make([]string, 0, len(cycle))
		refs := make([]planStepRef, 0, len(cycle))
		for _, i := range cycle {
			names = append(names, items[i].ID)
			refs = append(refs, planRef(items, i))
		}
		return &planGraphError{
			msg: fmt.Sprintf("plan refused: its steps depend on each other in a circle (%s), so none of them could ever start",
				strings.Join(append(names, names[0]), " → ")),
			fields: map[string]interface{}{"cycle": refs},
		}
	}
	return nil
}

// planCycle returns the indices of one cycle, in order, or nil.
func planCycle(items []scratchpadItem, byID map[string]int) []int {
	const (
		white = iota
		grey
		black
	)
	color := make([]int, len(items))
	stack := []int{}
	var found []int
	var visit func(i int) bool
	visit = func(i int) bool {
		color[i] = grey
		stack = append(stack, i)
		for _, dep := range items[i].After {
			j, ok := byID[dep]
			if !ok {
				continue
			}
			switch color[j] {
			case grey:
				for k := len(stack) - 1; k >= 0; k-- {
					if stack[k] == j {
						found = append([]int(nil), stack[k:]...)
						break
					}
				}
				return true
			case white:
				if visit(j) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[i] = black
		return false
	}
	for i := range items {
		if color[i] == white && visit(i) {
			// The walk follows After edges, i.e. from a step to what it waits
			// for; reverse so the cycle reads in the order work would flow.
			for l, r := 0, len(found)-1; l < r; l, r = l+1, r-1 {
				found[l], found[r] = found[r], found[l]
			}
			return found
		}
	}
	return nil
}

// planOpenPredecessors lists the unfinished steps step i waits for.
func planOpenPredecessors(items []scratchpadItem, i int) []int {
	if i < 0 || i >= len(items) || len(items[i].After) == 0 {
		return nil
	}
	byID := planIndexByID(items)
	var open []int
	for _, dep := range items[i].After {
		if j, ok := byID[dep]; ok && !items[j].Done {
			open = append(open, j)
		}
	}
	sort.Ints(open)
	return open
}

// planReadySteps lists the unchecked steps whose predecessors are all done —
// what can be worked on now. On a plan with no After edges that is every open
// step.
func planReadySteps(items []scratchpadItem) []int {
	var ready []int
	for i, it := range items {
		if it.Done {
			continue
		}
		if len(planOpenPredecessors(items, i)) == 0 {
			ready = append(ready, i)
		}
	}
	return ready
}

// planCheckRefusal is the refusal for checking a step whose predecessors are
// open, or nil when the check may go ahead.
func planCheckRefusal(items []scratchpadItem, i int) *planGraphError {
	open := planOpenPredecessors(items, i)
	if len(open) == 0 {
		return nil
	}
	refs := make([]planStepRef, 0, len(open))
	labels := make([]string, 0, len(open))
	for _, j := range open {
		refs = append(refs, planRef(items, j))
		labels = append(labels, fmt.Sprintf("%d (%s)", j, items[j].Text))
	}
	return &planGraphError{
		msg: fmt.Sprintf("step %d cannot be checked yet: it comes after step(s) %s, which are not done. Finish those first.",
			i, strings.Join(labels, ", ")),
		fields: map[string]interface{}{
			"step":       planRef(items, i),
			"blocked_by": refs,
			"ready":      planReadySteps(items),
		},
	}
}

// PlanView is the plan a lint sees: the run's stored plan and which of its
// open steps are ready. Empty when the run has no plan.
type PlanView struct {
	Key   string
	Items []PlanItem
	// Ready holds the indices of unchecked steps whose predecessors are all
	// done. On a plan with no dependencies that is every unchecked step.
	Ready []int
}

// Empty reports whether there is no plan at all.
func (p PlanView) Empty() bool { return len(p.Items) == 0 }

func newPlanView(key string, list []scratchpadItem) PlanView {
	if len(list) == 0 {
		return PlanView{Key: key}
	}
	return PlanView{Key: key, Items: planItemsFrom(list), Ready: planReadySteps(list)}
}

func planItemsFrom(list []scratchpadItem) []PlanItem {
	items := make([]PlanItem, 0, len(list))
	for _, it := range list {
		items = append(items, PlanItem{Text: it.Text, Done: it.Done, Note: it.Note, ID: it.ID, After: cloneStrings(it.After)})
	}
	return items
}

func scratchpadItemsFrom(items []PlanItem) []scratchpadItem {
	out := make([]scratchpadItem, 0, len(items))
	for _, it := range items {
		out = append(out, scratchpadItem{Text: it.Text, Done: it.Done, Note: it.Note, ID: it.ID, After: cloneStrings(it.After)})
	}
	return out
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

// cloneScratchpadItems deep-copies a list, After slices included, so a caller
// holding the copy cannot race a later write.
func cloneScratchpadItems(list []scratchpadItem) []scratchpadItem {
	out := make([]scratchpadItem, len(list))
	for i, it := range list {
		it.After = cloneStrings(it.After)
		out[i] = it
	}
	return out
}
