package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Built-in deterministic date/time resolution tool.
//
// LLMs are unreliable at date arithmetic (weekday counting, week boundaries,
// month carry) even when given the current time — they routinely land a day
// off on things like "下下周一 / the Monday after next". The fix is to keep the
// *understanding* in the model (it maps any phrasing or language into structured
// fields) and move the *arithmetic* into Go. This tool never parses natural
// language: the model fills in offsets/weekday, Go computes the exact instant.

// ResolveDateTimeArgs is the structured intent the model produces. It contains
// no natural language — only offsets the model has already reasoned out.
type ResolveDateTimeArgs struct {
	Base        string `json:"base,omitempty"`         // anchor: "" / "today", or "YYYY-MM-DD"
	DayOffset   int    `json:"day_offset,omitempty"`   // today 0 / tomorrow 1 / day after 2 / N days out N
	Weekday     string `json:"weekday,omitempty"`      // monday..sunday (Chinese names such as "周五" also accepted)
	WeekOffset  int    `json:"week_offset,omitempty"`  // this week 0 / next week 1 / the week after 2 (negative for past weeks: -1)
	MonthOffset int    `json:"month_offset,omitempty"` // this month 0 / next month 1
	DayOfMonth  int    `json:"day_of_month,omitempty"` // day of month (1-31)
	AvoidPast   *bool  `json:"avoid_past,omitempty"`   // default true: a weekday already past this week rolls to the next one
	Time        string `json:"time,omitempty"`         // "HH:MM", defaults to 09:00
}

// DateTimeResult is the resolved absolute instant plus human-readable parts.
type DateTimeResult struct {
	RFC3339 string `json:"rfc3339"`
	Date    string `json:"date"`
	Weekday string `json:"weekday"`
	Time    string `json:"time"`
}

// weekdayMap maps weekday names (en + zh) to the ISO index (Monday=0…Sunday=6).
var dateToolWeekdayMap = map[string]int{
	"monday": 0, "mon": 0, "周一": 0, "星期一": 0, "礼拜一": 0,
	"tuesday": 1, "tue": 1, "周二": 1, "星期二": 1, "礼拜二": 1,
	"wednesday": 2, "wed": 2, "周三": 2, "星期三": 2, "礼拜三": 2,
	"thursday": 3, "thu": 3, "周四": 3, "星期四": 3, "礼拜四": 3,
	"friday": 4, "fri": 4, "周五": 4, "星期五": 4, "礼拜五": 4,
	"saturday": 5, "sat": 5, "周六": 5, "星期六": 5, "礼拜六": 5,
	"sunday": 6, "sun": 6, "周日": 6, "星期日": 6, "星期天": 6, "礼拜天": 6, "礼拜日": 6,
}

func dateToolWeekdayCN(t time.Time) string {
	return "周" + []string{"日", "一", "二", "三", "四", "五", "六"}[int(t.Weekday())]
}

// ResolveDateTime does the deterministic arithmetic. `now` is injectable for
// testing. It never parses natural language — only the structured args.
func ResolveDateTime(now time.Time, a ResolveDateTimeArgs) (DateTimeResult, error) {
	loc := now.Location()
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if s := strings.TrimSpace(a.Base); s != "" && !strings.EqualFold(s, "today") {
		t, err := time.ParseInLocation("2006-01-02", s, loc)
		if err != nil {
			return DateTimeResult{}, fmt.Errorf("bad base %q (want today or YYYY-MM-DD)", s)
		}
		base = t
	}

	avoidPast := true
	if a.AvoidPast != nil {
		avoidPast = *a.AvoidPast
	}

	var target time.Time
	switch {
	case a.DayOfMonth > 0:
		// "the Nth of (next) month": anchor on the 1st of that month and add the
		// offset, leaving month carry to the time package.
		first := time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, loc)
		target = first.AddDate(0, a.MonthOffset, a.DayOfMonth-1)
	case strings.TrimSpace(a.Weekday) != "":
		wd, ok := dateToolWeekdayMap[strings.ToLower(strings.TrimSpace(a.Weekday))]
		if !ok {
			return DateTimeResult{}, fmt.Errorf("unknown weekday %q", a.Weekday)
		}
		isoIdx := (int(base.Weekday()) + 6) % 7 // Sun=0..Sat=6 → Mon=0..Sun=6
		monday := base.AddDate(0, 0, -isoIdx)
		target = monday.AddDate(0, 0, a.WeekOffset*7+wd)
		// A weekday in the current week that has already passed rolls forward to
		// the next one (assistant semantics; use week_offset<0 for a past week).
		if avoidPast && a.WeekOffset >= 0 && target.Before(base) {
			target = target.AddDate(0, 0, 7)
		}
	default:
		target = base.AddDate(0, a.MonthOffset, a.WeekOffset*7+a.DayOffset)
	}

	hh, mm := 9, 0
	if ts := strings.TrimSpace(a.Time); ts != "" {
		if _, err := fmt.Sscanf(ts, "%d:%d", &hh, &mm); err != nil {
			return DateTimeResult{}, fmt.Errorf("bad time %q (want HH:MM)", ts)
		}
	}
	target = time.Date(target.Year(), target.Month(), target.Day(), hh, mm, 0, 0, loc)

	return DateTimeResult{
		RFC3339: target.Format(time.RFC3339),
		Date:    target.Format("2006-01-02"),
		Weekday: dateToolWeekdayCN(target),
		Time:    target.Format("15:04"),
	}, nil
}

// dateTimeToolSchema is the JSON Schema advertised to the model.
func dateTimeToolSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"base":         map[string]interface{}{"type": "string", "description": "Anchor date: today (default) or YYYY-MM-DD"},
			"day_offset":   map[string]interface{}{"type": "integer", "description": "Days to add to the anchor: today 0, tomorrow 1, the day after 2, three days out 3, N days out N"},
			"weekday":      map[string]interface{}{"type": "string", "description": "Target weekday: monday..sunday (Chinese names such as '周五' are also accepted)"},
			"week_offset":  map[string]interface{}{"type": "integer", "description": "Week offset: this week 0, next week 1, the week after 2; use -1 for last week"},
			"month_offset": map[string]interface{}{"type": "integer", "description": "Month offset: this month 0, next month 1"},
			"day_of_month": map[string]interface{}{"type": "integer", "description": "Day of month (1-31), for phrases like 'the 3rd of next month'"},
			"avoid_past":   map[string]interface{}{"type": "boolean", "description": "Default true: a weekday already past in the current week rolls to the next one; for a past weekday use week_offset:-1"},
			"time":         map[string]interface{}{"type": "string", "description": "Time of day HH:MM, e.g. 10:00; defaults to 09:00"},
		},
	}
}

const dateTimeToolDescription = "Turn a relative time into an absolute one (RFC3339). Do not pass natural language: work out the meaning first and fill in the fields. today=day_offset:0, tomorrow:1, the day after:2, three days out:3; this week=week_offset:0, next week:1, the week after:2 (combine with weekday; use -1 for last week); next month=month_offset:1 (combine with day_of_month). Returns rfc3339 and the matching weekday. Understand the phrasing, but never do the date arithmetic yourself."

// resolveDateTimeFromMap adapts the map-based tool args to ResolveDateTime.
func resolveDateTimeFromMap(now time.Time, args map[string]interface{}) (DateTimeResult, error) {
	a := ResolveDateTimeArgs{
		Base:        toolArgString(args, "base"),
		DayOffset:   toolArgInt(args, "day_offset"),
		Weekday:     toolArgString(args, "weekday"),
		WeekOffset:  toolArgInt(args, "week_offset"),
		MonthOffset: toolArgInt(args, "month_offset"),
		DayOfMonth:  toolArgInt(args, "day_of_month"),
		Time:        toolArgString(args, "time"),
	}
	if v, ok := args["avoid_past"].(bool); ok {
		a.AvoidPast = &v
	}
	return ResolveDateTime(now, a)
}

// RegisterDateTimeTool registers the built-in `resolve_datetime` tool on a
// service. It is read-only and concurrency-safe. Use it on any agent that
// handles scheduling/reminders so the model never does date math itself.
//
//	svc, _ := agent.New("assistant").Build()
//	agent.RegisterDateTimeTool(svc)
func RegisterDateTimeTool(svc *Service) {
	if svc == nil {
		return
	}
	if svc.toolRegistry != nil && svc.toolRegistry.Has("resolve_datetime") {
		return
	}
	svc.AddToolWithMetadata(
		"resolve_datetime",
		dateTimeToolDescription,
		dateTimeToolSchema(),
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			res, err := resolveDateTimeFromMap(time.Now(), args)
			if err != nil {
				return map[string]interface{}{"ok": false, "error": err.Error()}, nil
			}
			return map[string]interface{}{"ok": true, "data": res}, nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
}

func toolArgString(args map[string]interface{}, k string) string {
	if v, ok := args[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func toolArgInt(args map[string]interface{}, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		var n int
		fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		return n
	}
	return 0
}
