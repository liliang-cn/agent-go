package agent

import (
	"testing"
	"time"
)

func TestResolveDateTime(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	// Anchor: Saturday 2026-06-13 12:00 — the date the LLM kept miscomputing.
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, loc)
	bTrue := true
	bFalse := false

	cases := []struct {
		name    string
		args    ResolveDateTimeArgs
		wantRFC string
	}{
		{
			name:    "下下周一上午十点 (the off-by-one case)",
			args:    ResolveDateTimeArgs{Weekday: "monday", WeekOffset: 2, Time: "10:00"},
			wantRFC: "2026-06-22T10:00:00+08:00", // Monday
		},
		{
			name:    "大后天下午三点",
			args:    ResolveDateTimeArgs{DayOffset: 3, Time: "15:00"},
			wantRFC: "2026-06-16T15:00:00+08:00",
		},
		{
			name:    "下个月3号上午九点",
			args:    ResolveDateTimeArgs{MonthOffset: 1, DayOfMonth: 3, Time: "09:00"},
			wantRFC: "2026-07-03T09:00:00+08:00",
		},
		{
			name:    "这周五已过去 → 默认取下一个 (avoid_past)",
			args:    ResolveDateTimeArgs{Weekday: "friday", WeekOffset: 0, Time: "15:00"},
			wantRFC: "2026-06-19T15:00:00+08:00", // next Friday, not the past 06-12
		},
		{
			name:    "本周五允许过去 (avoid_past=false)",
			args:    ResolveDateTimeArgs{Weekday: "friday", WeekOffset: 0, Time: "15:00", AvoidPast: &bFalse},
			wantRFC: "2026-06-12T15:00:00+08:00", // this ISO week's Friday (already past)
		},
		{
			name:    "上周五 (week_offset -1, not rolled forward)",
			args:    ResolveDateTimeArgs{Weekday: "friday", WeekOffset: -1, Time: "15:00", AvoidPast: &bTrue},
			wantRFC: "2026-06-05T15:00:00+08:00",
		},
		{
			name:    "明天默认09:00",
			args:    ResolveDateTimeArgs{DayOffset: 1},
			wantRFC: "2026-06-14T09:00:00+08:00",
		},
		{
			name:    "中文 weekday + explicit base",
			args:    ResolveDateTimeArgs{Base: "2026-06-13", Weekday: "周三", WeekOffset: 1, Time: "08:30"},
			wantRFC: "2026-06-17T08:30:00+08:00", // next week's Wednesday (this-week Mon 06-08 +1wk +Wed)
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveDateTime(now, tc.args)
			if err != nil {
				t.Fatalf("ResolveDateTime error: %v", err)
			}
			if got.RFC3339 != tc.wantRFC {
				t.Fatalf("got %s, want %s (weekday=%s)", got.RFC3339, tc.wantRFC, got.Weekday)
			}
		})
	}
}

func TestResolveDateTimeErrors(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	if _, err := ResolveDateTime(now, ResolveDateTimeArgs{Weekday: "funday"}); err == nil {
		t.Fatal("expected error for bad weekday")
	}
	if _, err := ResolveDateTime(now, ResolveDateTimeArgs{Base: "not-a-date"}); err == nil {
		t.Fatal("expected error for bad base")
	}
	if _, err := ResolveDateTime(now, ResolveDateTimeArgs{DayOffset: 1, Time: "25-99"}); err == nil {
		t.Fatal("expected error for bad time")
	}
}
