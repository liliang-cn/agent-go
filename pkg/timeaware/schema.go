package timeaware

import (
	"fmt"
	"strings"
	"time"
)

// Riding along instead of asking again.
//
// Resolve() is the standalone route: text in, dates out, one call. But a
// caller that is ALREADY asking the model about the same text — the memory
// writer extracting what is worth remembering, an ingestion pass, a task
// planner — should not ask twice. Two background calls where one would do is
// how a background feature becomes a bill.
//
// So the contract is available in two pieces a caller can graft onto its own
// structured call: the fields to add to its schema, the sentences to add to
// its prompt, and the parser for what comes back. One model call, both
// answers, and the same definition of a resolved time in every path.

// SchemaFields returns the properties to merge into a caller's own
// per-item schema, so its existing structured call also resolves time.
//
//	props := map[string]interface{}{"content": …, "importance": …}
//	for k, v := range timeaware.SchemaFields() { props[k] = v }
func SchemaFields() map[string]interface{} {
	return map[string]interface{}{
		"time_text":  map[string]interface{}{"type": "string", "description": "ONLY the words from the input that name the time, quoted exactly and nothing else — not an explanation, not the resolved date. Empty when the item names no time."},
		"time_kind":  map[string]interface{}{"type": "string", "enum": []string{"none", "point", "range", "recurring"}, "description": "what shape of time the item names"},
		"occurs_on":  map[string]interface{}{"type": "string", "description": "REQUIRED whenever time_kind is not \"none\": the date those words resolve to, exactly YYYY-MM-DD, in the stated timezone. Put the resolved date HERE, never in time_text. Empty string only when time_kind is \"none\"."},
		"occurs_at":  map[string]interface{}{"type": "string", "description": "the clock time, HH:MM 24-hour; empty when only a day was named"},
		"ends_on":    map[string]interface{}{"type": "string", "description": "end date for a range, YYYY-MM-DD; empty otherwise"},
		"ends_at":    map[string]interface{}{"type": "string", "description": "end time for a range, HH:MM; empty otherwise"},
		"recurrence": map[string]interface{}{"type": "string", "description": "plain description of the repeat when time_kind is recurring; empty otherwise"},
	}
}

// RequiredFields returns the field names a caller must add to its own
// `required` list.
//
// Both are required for the same reason, found twice against a real model.
// Left optional, a model working through a long extraction prompt does not
// fill an optional field — it omitted the time fields entirely on the first
// attempt, and on the second, with time_kind required, it answered
// `time_kind: "point"` and wrote the resolved date into the free-text
// time_text as prose ("明天下午三点特别指2026年9月3日下午15:00") while leaving
// occurs_on empty. Both answers are indistinguishable from "this text named
// no time" to anything downstream.
//
// So the two fields that carry the verdict are required, and an item with no
// time answers "none" and an empty string rather than being allowed to say
// nothing at all. occurs_at stays optional: a day named with no clock time
// is a normal answer, not a skipped one.
func RequiredFields() []string { return []string{"time_kind", "occurs_on"} }

// PromptRules returns the instructions to append to a caller's own prompt so
// the fields above are filled correctly.
//
// The anchor is stated with its weekday, offset and zone name. All three are
// load-bearing: "next Friday" cannot be counted from a date alone, and a
// daylight-saving boundary cannot be applied from an offset alone.
func PromptRules(anchor time.Time) string {
	zone, abbrev := anchor.Zone()
	_ = abbrev
	var sb strings.Builder
	sb.WriteString("\nTime:\n")
	fmt.Fprintf(&sb, "- This interaction is happening at %s (%s, timezone %s). Every relative expression in it is relative to that moment.\n",
		anchor.Format("2006-01-02 15:04:05 -07:00"), anchor.Format("Monday"), zoneName(anchor, zone))
	sb.WriteString("- When an item names a time — in any language, however phrased — resolve it against that moment. Put the resolved date in occurs_on as YYYY-MM-DD, the clock time in occurs_at as HH:MM, and ONLY the original words in time_text.\n")
	sb.WriteString("- Never write a date, an explanation or a resolved time into time_text. It holds the input's own words and nothing else.\n")
	sb.WriteString("- Dates are YYYY-MM-DD and times are HH:MM on a 24-hour clock, in that timezone: the person's local wall clock, never UTC.\n")
	sb.WriteString("- An item that names no time gets time_kind \"none\" and empty date fields. Never invent a time the text does not name.\n")
	return sb.String()
}

// ReferenceFromFields builds a Reference from the values a caller pulled out
// of its own structured answer, so both routes produce the same thing.
//
// An unresolvable or half-given answer becomes a KindNone reference rather
// than a half-truth: "the model said point and gave no date" is nothing, and
// a caller that stores it as something will show a reader a date that was
// never resolved.
func ReferenceFromFields(index int, anchor time.Time, text, kind, occursOn, occursAt, endsOn, endsAt, recurrence string) Reference {
	k := Kind(strings.TrimSpace(strings.ToLower(kind)))
	switch k {
	case KindPoint, KindRange, KindRecurring, KindNone:
	default:
		k = KindNone
	}
	ref := Reference{
		Index:      index,
		Text:       strings.TrimSpace(text),
		Kind:       k,
		Date:       strings.TrimSpace(occursOn),
		Time:       strings.TrimSpace(occursAt),
		EndDate:    strings.TrimSpace(endsOn),
		EndTime:    strings.TrimSpace(endsAt),
		Recurrence: strings.TrimSpace(recurrence),
		AnchoredAt: anchor,
	}
	ref.AllDay = ref.Time == "" && ref.Date != ""
	if ref.Kind != KindNone {
		if _, ok := ref.Start(); !ok {
			ref.Kind = KindNone
			ref.Date, ref.Time = "", ""
		}
	}
	// A date with no kind is still a date; the kind is a label, the date is
	// the answer.
	if ref.Kind == KindNone && ref.Date != "" {
		if _, ok := ref.Start(); ok {
			ref.Kind = KindPoint
		}
	}
	return ref
}
