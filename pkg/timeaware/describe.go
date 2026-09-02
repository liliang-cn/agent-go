package timeaware

import (
	"fmt"
	"strings"
	"time"
)

// Reading back costs nothing.
//
// Everything in this file is arithmetic on two timestamps. No model, no
// language, no table — which is what makes it safe to run on the path of an
// agent's turn, where the resolver itself must never be. A memory recalled
// on the hot path gets its "which is today" from here.

// Describe says where an instant sits relative to now, in days: the unit
// every relative expression a person uses is about.
//
// Empty for a zero time. Deliberately coarse — "3 days ago" rather than "68
// hours ago" — because it goes into a prompt beside a date, and the date is
// the precise half.
func Describe(when, now time.Time) string {
	if when.IsZero() || now.IsZero() {
		return ""
	}
	days := DaysBetween(now, when)
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days == -1:
		return "yesterday"
	case days > 1:
		return fmt.Sprintf("in %d days", days)
	default:
		return fmt.Sprintf("%d days ago", -days)
	}
}

// DaysBetween counts calendar days from `from` to `to`, positive when `to`
// is later, in the reader's timezone — `from`'s location, because `from` is
// "now" at every call site here.
//
// Two things it deliberately gets right. Calendar days, not 24-hour periods:
// 23:00 and 01:00 the next morning are two hours apart and one day, and this
// package exists to agree with the person, not the clock. And BOTH endpoints
// are converted into that one location first: a memory written at 23:30 in
// Tokyo is 16:30 the same afternoon in Vienna, so comparing the two as they
// were stored puts "yesterday" and "today" a day apart from each other.
func DaysBetween(from, to time.Time) int {
	loc := from.Location()
	if loc == nil {
		loc = time.Local
	}
	f := from.In(loc)
	t := to.In(loc)
	fd := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc)
	td := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return int(td.Sub(fd).Hours() / 24)
}

// Note renders the parenthetical a stored text carries when it is read back:
// when it was written, what day it named, and where that day sits now.
//
// Everything is rendered in `now`'s location — the reader's local time. A
// memory written in one timezone and read in another is described by the
// calendar the reader is living in, which is the only calendar that makes
// "today" mean anything to them.
//
// This is the whole feature in one line of prompt. A memory written on the
// 1st saying "明天要去医院", recalled on the 2nd, reads:
//
//	(written 2026-09-01, yesterday; "明天" = 2026-09-02, today)
//
// The model no longer has to guess which "tomorrow" was meant, and — this is
// the part a phrase table could never do — it works identically whatever
// language the person wrote in, because nothing here reads the words.
//
// ref may be a zero Reference: an unresolved text still gets its write time,
// which is the honest degraded answer and is enough to reckon from.
func Note(writtenAt time.Time, ref Reference, now time.Time) string {
	loc := now.Location()
	if loc == nil {
		loc = time.Local
	}
	var parts []string
	if !writtenAt.IsZero() {
		writtenAt = writtenAt.In(loc)
		part := "written " + writtenAt.Format("2006-01-02")
		if rel := Describe(writtenAt, now); rel != "" {
			part += ", " + rel
		}
		parts = append(parts, part)
	}
	if start, ok := ref.Start(); ok && ref.Kind != KindNone {
		start = start.In(loc)
		part := ""
		if ref.Text != "" {
			part = fmt.Sprintf("%q = ", ref.Text)
		}
		part += start.Format("2006-01-02")
		if !ref.AllDay && ref.Time != "" {
			part += " " + ref.Time
		}
		if rel := Describe(start, now); rel != "" {
			part += ", " + rel
		}
		if end, ok := ref.End(); ok {
			part += " until " + end.In(loc).Format("2006-01-02")
		}
		if ref.Kind == KindRecurring && ref.Recurrence != "" {
			part += " (" + ref.Recurrence + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, "; ") + ")"
}
