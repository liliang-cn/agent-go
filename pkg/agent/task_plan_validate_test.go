package agent

import (
	"strings"
	"testing"
)

func item(id string, blockedBy ...string) TaskPlanItem {
	return TaskPlanItem{
		ID:        id,
		Subject:   "subject " + id,
		Status:    PlanItemStatusPending,
		BlockedBy: blockedBy,
	}
}

func TestTaskPlanValidate(t *testing.T) {
	tests := []struct {
		name        string
		items       []TaskPlanItem
		wantErr     bool
		wantSubstrs []string
	}{
		{
			name:    "valid linear plan",
			items:   []TaskPlanItem{item("a"), item("b", "a"), item("c", "b")},
			wantErr: false,
		},
		{
			name:    "valid diamond plan",
			items:   []TaskPlanItem{item("a"), item("b", "a"), item("c", "a"), item("d", "b", "c")},
			wantErr: false,
		},
		{
			name:        "dangling dependency",
			items:       []TaskPlanItem{item("a"), item("b", "ghost")},
			wantErr:     true,
			wantSubstrs: []string{"unknown item", "ghost"},
		},
		{
			name:        "self dependency",
			items:       []TaskPlanItem{item("a", "a")},
			wantErr:     true,
			wantSubstrs: []string{"depends on itself", "a"},
		},
		{
			name:        "two cycle",
			items:       []TaskPlanItem{item("a", "b"), item("b", "a")},
			wantErr:     true,
			wantSubstrs: []string{"cycle"},
		},
		{
			name:        "three cycle",
			items:       []TaskPlanItem{item("a", "c"), item("b", "a"), item("c", "b")},
			wantErr:     true,
			wantSubstrs: []string{"cycle"},
		},
		{
			name:        "duplicate id",
			items:       []TaskPlanItem{item("a"), item("a"), item("b", "a")},
			wantErr:     true,
			wantSubstrs: []string{"duplicate item id", "a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := &TaskPlan{Goal: "g", Items: tc.items}
			err := plan.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if err != nil {
				msg := err.Error()
				for _, want := range tc.wantSubstrs {
					if !strings.Contains(msg, want) {
						t.Fatalf("error %q missing expected substring %q", msg, want)
					}
				}
			}
		})
	}
}

func TestTaskPlanValidateAggregatesMultipleProblems(t *testing.T) {
	plan := &TaskPlan{
		Goal: "g",
		Items: []TaskPlanItem{
			item("a", "a"),       // self-dep
			item("b", "missing"), // dangling
			item("b"),            // duplicate id b
		},
	}
	err := plan.Validate()
	if err == nil {
		t.Fatal("expected aggregated validation error")
	}
	ve, ok := err.(*TaskPlanValidationError)
	if !ok {
		t.Fatalf("expected *TaskPlanValidationError, got %T", err)
	}
	if len(ve.Problems) < 3 {
		t.Fatalf("expected at least 3 aggregated problems, got %d: %v", len(ve.Problems), ve.Problems)
	}
}
