package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A slot missed by too long is skipped and next_run moves to the next future
// slot; a slot only slightly late still fires once.
func TestMissedSlotGrace(t *testing.T) {
	s := newTestScheduler(t)
	s.RegisterExecutor(&countingExecutor{taskType: "missed-test"})
	loc := time.UTC
	// Sunday 2026-11-01 16:04.
	now := time.Date(2026, 11, 1, 16, 4, 0, 0, loc)
	cases := []struct {
		name     string
		schedule string
		slot     time.Time
		fires    bool
		wantNext time.Time // checked only when skipped
	}{
		{"weekly missed by 3 days", "20 20 * * 4", time.Date(2026, 10, 29, 20, 20, 0, 0, loc), false, time.Date(2026, 11, 5, 20, 20, 0, 0, loc)},
		{"midnight job missed by 16h", "0 0 * * *", time.Date(2026, 11, 1, 0, 0, 0, 0, loc), false, time.Date(2026, 11, 2, 0, 0, 0, 0, loc)},
		{"daily missed by 2 minutes", "0 16 * * *", time.Date(2026, 11, 1, 16, 2, 0, 0, loc), true, time.Time{}},
		{"daily missed by 59 minutes", "5 15 * * *", time.Date(2026, 11, 1, 15, 5, 0, 0, loc), true, time.Time{}},
		{"daily missed by 90 minutes", "34 14 * * *", time.Date(2026, 11, 1, 14, 34, 0, 0, loc), false, time.Date(2026, 11, 2, 14, 34, 0, 0, loc)},
		{"every 10m missed by 3 hours", "@every 10m", now.Add(-3 * time.Hour), false, now.Add(10 * time.Minute)},
		{"every 10m missed by 1 minute", "@every 10m", now.Add(-time.Minute), true, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := s.CreateTask(&Task{Type: "missed-test", Schedule: tc.schedule, Enabled: true})
			require.NoError(t, err)
			slot := tc.slot
			require.NoError(t, s.storage.UpdateTaskNextRun(id, &slot))
			task, err := s.GetTask(id)
			require.NoError(t, err)

			got := s.claimSlot(task, now)
			require.Equal(t, tc.fires, got)

			after, err := s.GetTask(id)
			require.NoError(t, err)
			require.NotNil(t, after.NextRun)
			if tc.fires {
				require.True(t, after.NextRun.After(now))
				return
			}
			if tc.schedule == "@every 10m" {
				require.True(t, after.NextRun.After(now))
				return
			}
			require.True(t, after.NextRun.Equal(tc.wantNext), "next_run %s, want %s", after.NextRun, tc.wantNext)
		})
	}
}
