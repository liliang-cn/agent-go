package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
)

// PlanItemStatus describes the lifecycle of a planned work item.
type PlanItemStatus string

const (
	PlanItemStatusPending    PlanItemStatus = "pending"
	PlanItemStatusInProgress PlanItemStatus = "in_progress"
	PlanItemStatusCompleted  PlanItemStatus = "completed"
	PlanItemStatusBlocked    PlanItemStatus = "blocked"
	PlanItemStatusFailed     PlanItemStatus = "failed"
)

// TaskPlan is a lightweight work plan. Plan items are coordination records;
// execution remains a first-class task and is linked through ExecutionTaskID.
type TaskPlan struct {
	ID           string         `json:"id"`
	SessionID    string         `json:"session_id,omitempty"`
	ParentTaskID string         `json:"parent_task_id,omitempty"`
	Goal         string         `json:"goal"`
	Items        []TaskPlanItem `json:"items"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// TaskPlanItem is one planned unit of work.
type TaskPlanItem struct {
	ID              string         `json:"id"`
	Subject         string         `json:"subject"`
	Description     string         `json:"description,omitempty"`
	ActiveForm      string         `json:"active_form,omitempty"`
	OwnerAgent      string         `json:"owner_agent,omitempty"`
	Status          PlanItemStatus `json:"status"`
	Blocks          []string       `json:"blocks,omitempty"`
	BlockedBy       []string       `json:"blocked_by,omitempty"`
	ExecutionTaskID string         `json:"execution_task_id,omitempty"`
	ResultText      string         `json:"result_text,omitempty"`
	Error           string         `json:"error,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type TaskPlanCreateOptions struct {
	SessionID    string
	ParentTaskID string
	Goal         string
	Items        []TaskPlanItem
}

type TaskPlanItemUpdateOptions struct {
	Subject         *string
	Description     *string
	ActiveForm      *string
	OwnerAgent      *string
	Status          *PlanItemStatus
	AddBlocks       []string
	AddBlockedBy    []string
	ExecutionTaskID *string
	ResultText      *string
	Error           *string
	Metadata        map[string]any
}

type TaskPlanSubmitItemOptions struct {
	SessionID string
	AgentName string
	Input     string
}

// TaskPlanService manages lightweight plan items for a team manager.
type TaskPlanService struct {
	manager *TeamManager
}

func (m *TeamManager) Plans() *TaskPlanService {
	return &TaskPlanService{manager: m}
}

func (s *TaskPlanService) Create(_ context.Context, opts TaskPlanCreateOptions) (*TaskPlan, error) {
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("task plan service is not configured")
	}
	goal := strings.TrimSpace(opts.Goal)
	if goal == "" {
		return nil, fmt.Errorf("goal is required")
	}
	if len(opts.Items) == 0 {
		return nil, fmt.Errorf("plan requires at least one item")
	}

	now := time.Now()
	plan := &TaskPlan{
		ID:           uuid.NewString(),
		SessionID:    strings.TrimSpace(opts.SessionID),
		ParentTaskID: strings.TrimSpace(opts.ParentTaskID),
		Goal:         goal,
		Items:        make([]TaskPlanItem, 0, len(opts.Items)),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	for idx, item := range opts.Items {
		normalized, err := normalizeTaskPlanItem(item, idx, now)
		if err != nil {
			return nil, err
		}
		plan.Items = append(plan.Items, normalized)
	}
	normalizeTaskPlanDependencies(plan.Items)

	// Reject structurally invalid plans (dangling deps, self-deps, cycles,
	// duplicate IDs) after normalization so callers get one aggregated error.
	if err := plan.Validate(); err != nil {
		return nil, err
	}

	s.manager.planMu.Lock()
	s.manager.ensureTaskPlansLocked()
	s.manager.taskPlans[plan.ID] = cloneTaskPlan(plan)
	s.manager.planMu.Unlock()
	if s.manager.store != nil {
		if err := s.manager.store.SaveTaskPlan(plan); err != nil {
			return nil, err
		}
	}

	return cloneTaskPlan(plan), nil
}

func (s *TaskPlanService) Get(_ context.Context, planID string) (*TaskPlan, error) {
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("task plan service is not configured")
	}
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return nil, fmt.Errorf("plan id is required")
	}
	s.manager.planMu.RLock()
	defer s.manager.planMu.RUnlock()
	plan := s.manager.taskPlans[planID]
	if plan == nil {
		return nil, fmt.Errorf("task plan %s not found", planID)
	}
	return cloneTaskPlan(plan), nil
}

func (s *TaskPlanService) List(_ context.Context) ([]*TaskPlan, error) {
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("task plan service is not configured")
	}
	s.manager.planMu.RLock()
	defer s.manager.planMu.RUnlock()
	out := make([]*TaskPlan, 0, len(s.manager.taskPlans))
	for _, plan := range s.manager.taskPlans {
		out = append(out, cloneTaskPlan(plan))
	}
	slices.SortFunc(out, func(a, b *TaskPlan) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return out, nil
}

func (s *TaskPlanService) ReadyItems(_ context.Context, planID string) ([]TaskPlanItem, error) {
	plan, err := s.Get(context.Background(), planID)
	if err != nil {
		return nil, err
	}
	completed := make(map[string]struct{}, len(plan.Items))
	for _, item := range plan.Items {
		if item.Status == PlanItemStatusCompleted {
			completed[item.ID] = struct{}{}
		}
	}
	out := make([]TaskPlanItem, 0)
	for _, item := range plan.Items {
		if item.Status != PlanItemStatusPending {
			continue
		}
		ready := true
		for _, blocker := range item.BlockedBy {
			if _, ok := completed[blocker]; !ok {
				ready = false
				break
			}
		}
		if ready {
			out = append(out, cloneTaskPlanItem(item))
		}
	}
	return out, nil
}

func (s *TaskPlanService) UpdateItem(_ context.Context, planID, itemID string, opts TaskPlanItemUpdateOptions) (*TaskPlanItem, error) {
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("task plan service is not configured")
	}
	planID = strings.TrimSpace(planID)
	itemID = strings.TrimSpace(itemID)
	if planID == "" || itemID == "" {
		return nil, fmt.Errorf("plan id and item id are required")
	}

	s.manager.planMu.Lock()
	defer s.manager.planMu.Unlock()
	plan := s.manager.taskPlans[planID]
	if plan == nil {
		return nil, fmt.Errorf("task plan %s not found", planID)
	}
	idx := taskPlanItemIndex(plan.Items, itemID)
	if idx < 0 {
		return nil, fmt.Errorf("task plan item %s not found", itemID)
	}
	now := time.Now()
	item := &plan.Items[idx]
	applyTaskPlanItemUpdate(item, opts, now)
	normalizeTaskPlanDependencies(plan.Items)
	plan.UpdatedAt = now
	persisted := cloneTaskPlan(plan)
	returnItem := cloneTaskPlanItem(*item)
	if s.manager.store != nil {
		if err := s.manager.store.SaveTaskPlan(persisted); err != nil {
			return nil, err
		}
	}
	return &returnItem, nil
}

func (s *TaskPlanService) SubmitItem(ctx context.Context, planID, itemID string, opts TaskPlanSubmitItemOptions) (*taskpkg.Task, error) {
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("task plan service is not configured")
	}
	plan, err := s.Get(ctx, planID)
	if err != nil {
		return nil, err
	}
	idx := taskPlanItemIndex(plan.Items, strings.TrimSpace(itemID))
	if idx < 0 {
		return nil, fmt.Errorf("task plan item %s not found", itemID)
	}
	item := plan.Items[idx]
	if item.Status == PlanItemStatusCompleted {
		return nil, fmt.Errorf("task plan item %s is already completed", item.ID)
	}

	ready, err := s.ReadyItems(ctx, planID)
	if err != nil {
		return nil, err
	}
	isReady := false
	for _, candidate := range ready {
		if candidate.ID == item.ID {
			isReady = true
			break
		}
	}
	if !isReady {
		return nil, fmt.Errorf("task plan item %s is blocked by unfinished dependencies", item.ID)
	}

	agentName := strings.TrimSpace(opts.AgentName)
	if agentName == "" {
		agentName = strings.TrimSpace(item.OwnerAgent)
	}
	if agentName == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	input := strings.TrimSpace(opts.Input)
	if input == "" {
		input = strings.TrimSpace(item.Description)
	}
	if input == "" {
		input = strings.TrimSpace(item.Subject)
	}

	submitted, err := s.manager.Tasks().Submit(ctx, TaskSubmitOptions{
		SessionID: strings.TrimSpace(firstNonEmptyTaskString(opts.SessionID, plan.SessionID)),
		AgentName: agentName,
		Input:     input,
	})
	if err != nil {
		return nil, err
	}
	status := PlanItemStatusInProgress
	taskID := submitted.ID
	owner := agentName
	_, _ = s.UpdateItem(ctx, planID, itemID, TaskPlanItemUpdateOptions{
		Status:          &status,
		ExecutionTaskID: &taskID,
		OwnerAgent:      &owner,
	})
	return submitted, nil
}

func (m *TeamManager) updateTaskPlanItemForExecutionTask(taskID string, status PlanItemStatus, resultText, errText string) {
	if m == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}

	m.planMu.Lock()
	defer m.planMu.Unlock()

	m.ensureTaskPlansLocked()
	now := time.Now()
	for _, plan := range m.taskPlans {
		if plan == nil {
			continue
		}
		changed := false
		for idx := range plan.Items {
			item := &plan.Items[idx]
			if strings.TrimSpace(item.ExecutionTaskID) != taskID {
				continue
			}
			item.Status = status
			item.ResultText = strings.TrimSpace(resultText)
			item.Error = strings.TrimSpace(errText)
			item.UpdatedAt = now
			plan.UpdatedAt = now
			changed = true
		}
		if changed {
			normalizeTaskPlanDependencies(plan.Items)
			if m.store != nil {
				_ = m.store.SaveTaskPlan(cloneTaskPlan(plan))
			}
		}
	}
}

func (m *TeamManager) ensureTaskPlansLocked() {
	if m.taskPlans == nil {
		m.taskPlans = make(map[string]*TaskPlan)
	}
}

func (m *TeamManager) restoreTaskPlans() {
	if m == nil || m.store == nil {
		return
	}
	plans, err := m.store.ListTaskPlans("", 1000)
	if err != nil || len(plans) == 0 {
		return
	}
	m.planMu.Lock()
	defer m.planMu.Unlock()
	m.ensureTaskPlansLocked()
	for _, plan := range plans {
		if plan == nil || strings.TrimSpace(plan.ID) == "" {
			continue
		}
		m.taskPlans[plan.ID] = cloneTaskPlan(plan)
	}
}

// TaskPlanValidationError aggregates every structural problem found in a plan
// so callers see all issues at once instead of fixing them one at a time.
type TaskPlanValidationError struct {
	Problems []string
}

func (e *TaskPlanValidationError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "task plan is invalid"
	}
	return "task plan is invalid: " + strings.Join(e.Problems, "; ")
}

// Validate checks a plan's dependency graph for structural problems and
// returns a *TaskPlanValidationError listing ALL of them (or nil when clean).
// Detected problems: duplicate item IDs, self-dependencies, dangling
// dependency references, and dependency cycles (with the offending path).
//
// Safe to call standalone for pre-flight before Create.
func (p *TaskPlan) Validate() error {
	if p == nil {
		return &TaskPlanValidationError{Problems: []string{"plan is nil"}}
	}

	var problems []string

	// Index items by ID and detect duplicates.
	idToIndex := make(map[string]int, len(p.Items))
	seen := make(map[string]int, len(p.Items))
	for i := range p.Items {
		id := strings.TrimSpace(p.Items[i].ID)
		seen[id]++
		if _, ok := idToIndex[id]; !ok {
			idToIndex[id] = i
		}
	}
	// Report duplicates deterministically (in first-seen order).
	reported := make(map[string]bool)
	for i := range p.Items {
		id := strings.TrimSpace(p.Items[i].ID)
		if seen[id] > 1 && !reported[id] {
			reported[id] = true
			problems = append(problems, fmt.Sprintf("duplicate item id %q (appears %d times)", id, seen[id]))
		}
	}

	// Per-item checks: self-dependency and dangling references.
	for i := range p.Items {
		item := &p.Items[i]
		id := strings.TrimSpace(item.ID)
		checkRefs := func(field string, refs []string) {
			for _, ref := range refs {
				ref = strings.TrimSpace(ref)
				if ref == "" {
					continue
				}
				if ref == id {
					problems = append(problems, fmt.Sprintf("item %q depends on itself via %s", id, field))
					continue
				}
				if _, ok := idToIndex[ref]; !ok {
					problems = append(problems, fmt.Sprintf("item %q %s references unknown item %q", id, field, ref))
				}
			}
		}
		checkRefs("blocks", item.Blocks)
		checkRefs("blocked_by", item.BlockedBy)
	}

	// Cycle detection over the dependency graph. Edge item -> blocker means
	// "item is blocked_by blocker" (blocker must finish first). A cycle here is
	// a circular dependency. Only edges between existing items are considered
	// (dangling refs are reported separately above).
	if cycle := detectTaskPlanCycle(p.Items, idToIndex); len(cycle) > 0 {
		problems = append(problems, "dependency cycle detected: "+strings.Join(cycle, " -> "))
	}

	if len(problems) == 0 {
		return nil
	}
	return &TaskPlanValidationError{Problems: problems}
}

// detectTaskPlanCycle runs a DFS over the blocked_by dependency graph and
// returns the first cycle path found (e.g. ["a","b","a"]), or nil if the graph
// is acyclic.
func detectTaskPlanCycle(items []TaskPlanItem, idToIndex map[string]int) []string {
	const (
		white = 0 // unvisited
		gray  = 1 // on current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(items))
	var stack []string

	var dfs func(id string) []string
	dfs = func(id string) []string {
		color[id] = gray
		stack = append(stack, id)
		idx, ok := idToIndex[id]
		if ok {
			for _, dep := range items[idx].BlockedBy {
				dep = strings.TrimSpace(dep)
				if dep == "" || dep == id {
					continue
				}
				if _, exists := idToIndex[dep]; !exists {
					continue // dangling ref, reported elsewhere
				}
				switch color[dep] {
				case gray:
					// Found a back-edge: build the cycle path from dep.
					cycle := []string{}
					for i := len(stack) - 1; i >= 0; i-- {
						cycle = append([]string{stack[i]}, cycle...)
						if stack[i] == dep {
							break
						}
					}
					return append(cycle, dep)
				case white:
					if found := dfs(dep); found != nil {
						return found
					}
				}
			}
		}
		color[id] = black
		stack = stack[:len(stack)-1]
		return nil
	}

	for i := range items {
		id := strings.TrimSpace(items[i].ID)
		if color[id] == white {
			if cycle := dfs(id); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

func normalizeTaskPlanItem(item TaskPlanItem, index int, now time.Time) (TaskPlanItem, error) {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		item.ID = fmt.Sprintf("%d", index+1)
	}
	item.Subject = strings.TrimSpace(item.Subject)
	if item.Subject == "" {
		return TaskPlanItem{}, fmt.Errorf("plan item %d subject is required", index+1)
	}
	item.Description = strings.TrimSpace(item.Description)
	item.ActiveForm = strings.TrimSpace(item.ActiveForm)
	item.OwnerAgent = strings.TrimSpace(item.OwnerAgent)
	if item.Status == "" {
		item.Status = PlanItemStatusPending
	}
	item.Blocks = uniqueTrimmedStrings(item.Blocks)
	item.BlockedBy = uniqueTrimmedStrings(item.BlockedBy)
	item.ExecutionTaskID = strings.TrimSpace(item.ExecutionTaskID)
	item.ResultText = strings.TrimSpace(item.ResultText)
	item.Error = strings.TrimSpace(item.Error)
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	return item, nil
}

func applyTaskPlanItemUpdate(item *TaskPlanItem, opts TaskPlanItemUpdateOptions, now time.Time) {
	if opts.Subject != nil {
		item.Subject = strings.TrimSpace(*opts.Subject)
	}
	if opts.Description != nil {
		item.Description = strings.TrimSpace(*opts.Description)
	}
	if opts.ActiveForm != nil {
		item.ActiveForm = strings.TrimSpace(*opts.ActiveForm)
	}
	if opts.OwnerAgent != nil {
		item.OwnerAgent = strings.TrimSpace(*opts.OwnerAgent)
	}
	if opts.Status != nil {
		item.Status = *opts.Status
	}
	if opts.ExecutionTaskID != nil {
		item.ExecutionTaskID = strings.TrimSpace(*opts.ExecutionTaskID)
	}
	if opts.ResultText != nil {
		item.ResultText = strings.TrimSpace(*opts.ResultText)
	}
	if opts.Error != nil {
		item.Error = strings.TrimSpace(*opts.Error)
	}
	item.Blocks = uniqueTrimmedStrings(append(item.Blocks, opts.AddBlocks...))
	item.BlockedBy = uniqueTrimmedStrings(append(item.BlockedBy, opts.AddBlockedBy...))
	if opts.Metadata != nil {
		merged := cloneTaskPlanMetadata(item.Metadata)
		for key, value := range opts.Metadata {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if value == nil {
				delete(merged, key)
			} else {
				merged[key] = value
			}
		}
		item.Metadata = merged
	}
	item.UpdatedAt = now
}

func normalizeTaskPlanDependencies(items []TaskPlanItem) {
	index := make(map[string]int, len(items))
	for i, item := range items {
		index[item.ID] = i
	}
	for i := range items {
		for _, blocked := range items[i].Blocks {
			if j, ok := index[blocked]; ok {
				items[j].BlockedBy = uniqueTrimmedStrings(append(items[j].BlockedBy, items[i].ID))
			}
		}
		for _, blocker := range items[i].BlockedBy {
			if j, ok := index[blocker]; ok {
				items[j].Blocks = uniqueTrimmedStrings(append(items[j].Blocks, items[i].ID))
			}
		}
	}
}

func taskPlanItemIndex(items []TaskPlanItem, itemID string) int {
	itemID = strings.TrimSpace(itemID)
	for i, item := range items {
		if item.ID == itemID {
			return i
		}
	}
	return -1
}

func cloneTaskPlan(plan *TaskPlan) *TaskPlan {
	if plan == nil {
		return nil
	}
	cloned := *plan
	cloned.Items = make([]TaskPlanItem, len(plan.Items))
	for i, item := range plan.Items {
		cloned.Items[i] = cloneTaskPlanItem(item)
	}
	return &cloned
}

func cloneTaskPlanItem(item TaskPlanItem) TaskPlanItem {
	item.Blocks = append([]string(nil), item.Blocks...)
	item.BlockedBy = append([]string(nil), item.BlockedBy...)
	item.Metadata = cloneTaskPlanMetadata(item.Metadata)
	return item
}

func cloneTaskPlanMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func uniqueTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
