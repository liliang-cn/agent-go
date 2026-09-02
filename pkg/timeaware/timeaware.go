// Package timeaware turns what a person said about time into absolute
// instants, using the model and nothing else.
//
// # The problem
//
// Everything a person says about time is relative to the moment they said
// it. "明天要去医院", stored on the 1st and recalled on the 2nd, describes
// today — but the stored text still says "明天", and a reader with no anchor
// places the appointment on the 3rd. The sentence is correct and it is
// correct about a day that has passed. The same is true of "next Friday",
// "in two weeks", "月末", "after the holiday", and every other way a person
// has of naming a day without naming it.
//
// # Why there is no pattern in this package
//
// The obvious implementation is a table: 明天 → +1, tomorrow → +1, and so
// on. It is also wrong, for the reason every phrase table in this framework
// was deleted: a table serves exactly the languages and phrasings somebody
// thought to enumerate, and for everyone else it silently does nothing —
// which reads identically to "this text mentioned no date". A user writing
// Korean, or Portuguese, or simply "the Tuesday after next" gets no error
// and no resolution.
//
// Understanding what a person meant by a day is understanding. It belongs to
// the model. This package contains no regular expression, no keyword list
// and no phrase matching of any kind: it hands the text and the anchor to
// the model in one structured call and stores what comes back.
//
// # It must never block a run
//
// Resolution is a model call, and a model call on the path of an agent's
// turn is latency the user pays for. So the contract is:
//
//   - Resolve() is for background work — a durable-memory writer, an
//     ingestion job, a scheduler filling in a task. Callers on the agent's
//     own path must not wait for it.
//   - Reading back costs nothing. Describe() re-anchors an already-resolved
//     instant against now with plain arithmetic and no model, which is what
//     a recall path uses.
//   - A text nobody managed to resolve degrades to "written at <time>",
//     which is still enough for a model to reckon with, and is what the
//     recall path shows when this package has not answered yet.
//
// # One call, however many texts
//
// Resolve takes a batch. A memory write extracting five items resolves all
// five in a single request, because five requests to answer one background
// question is how a background feature becomes a bill.
package timeaware

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Kind says what shape of time a text named.
type Kind string

const (
	// KindNone means the text named no time at all.
	KindNone Kind = "none"
	// KindPoint is a single moment or day.
	KindPoint Kind = "point"
	// KindRange is a span with a start and an end.
	KindRange Kind = "range"
	// KindRecurring is a repeating time ("every Monday"). Start is the next
	// occurrence after the anchor; Recurrence is the model's own description.
	KindRecurring Kind = "recurring"
)

// Reference is one resolved mention of time.
type Reference struct {
	// Index is the position of the text this came from in the request.
	Index int `json:"index"`
	// Text is the words the model resolved, quoted from the input, so a
	// reader can see what was interpreted rather than trust the answer.
	Text string `json:"text,omitempty"`
	// Kind says whether this is a point, a range, a recurrence or nothing.
	Kind Kind `json:"kind"`
	// Date and Time are the resolved start, "2006-01-02" and "15:04". Time
	// is empty when the text named a day but no clock time.
	Date string `json:"date,omitempty"`
	Time string `json:"time,omitempty"`
	// EndDate and EndTime are the end of a range, empty otherwise.
	EndDate string `json:"end_date,omitempty"`
	EndTime string `json:"end_time,omitempty"`
	// Recurrence is how the model described a repeat, in its own words.
	Recurrence string `json:"recurrence,omitempty"`
	// AllDay marks a day with no clock time attached.
	AllDay bool `json:"all_day,omitempty"`
	// AnchoredAt is the instant this was resolved against, kept so a later
	// reader can check the arithmetic instead of trusting it.
	AnchoredAt time.Time `json:"anchored_at"`
}

// Start returns the resolved start instant, and whether there was one.
func (r Reference) Start() (time.Time, bool) { return parseDateTime(r.Date, r.Time, r.AnchoredAt) }

// End returns the resolved end instant, and whether there was one.
func (r Reference) End() (time.Time, bool) { return parseDateTime(r.EndDate, r.EndTime, r.AnchoredAt) }

// Resolved reports whether this reference names an actual instant.
func (r Reference) Resolved() bool {
	_, ok := r.Start()
	return ok && r.Kind != KindNone
}

// Result is what one Resolve call produced: one entry per input text, in the
// order they were given. A text that named no time still gets an entry, with
// Kind KindNone — "asked and there was nothing" is an answer, and a caller
// that cannot tell it from "not asked yet" will ask again forever.
type Result struct {
	Anchor     time.Time
	References []Reference
}

// For returns the reference for the nth input text.
func (r *Result) For(index int) (Reference, bool) {
	if r == nil {
		return Reference{}, false
	}
	for _, ref := range r.References {
		if ref.Index == index {
			return ref, true
		}
	}
	return Reference{}, false
}

// Resolver resolves time expressions with one model call per batch.
type Resolver struct {
	llm     domain.Generator
	timeout time.Duration
	now     func() time.Time
	loc     *time.Location
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithTimeout bounds a single resolution call. Background work still has to
// end: a request that hangs holds a worker that has other memories to write.
func WithTimeout(d time.Duration) Option {
	return func(r *Resolver) {
		if d > 0 {
			r.timeout = d
		}
	}
}

// WithClock replaces the source of "now", for tests and for a host whose
// notion of the current time is not the machine's.
func WithClock(now func() time.Time) Option {
	return func(r *Resolver) {
		if now != nil {
			r.now = now
		}
	}
}

// WithLocation sets the timezone every anchor and every answer is expressed
// in — the person's timezone, not the server's.
//
// This is the setting most likely to be wrong and least likely to look
// wrong. A server in UTC resolving "tonight at 8" for someone in +08:00
// answers with a moment four hours after their bedtime, and every date near
// midnight lands on the wrong day: 2026-09-01 23:30 in Tokyo is still
// 2026-09-01 16:30 in Vienna, so "today" is a different day depending on who
// is asking. The resolver therefore converts the anchor into this location
// before asking, states the offset and the zone name in the prompt, and
// reads every answer back in it.
//
// Unset, the anchor's own location is used, and a zero anchor falls back to
// the machine's. Neither is a substitute for telling it where the user is.
func WithLocation(loc *time.Location) Option {
	return func(r *Resolver) {
		if loc != nil {
			r.loc = loc
		}
	}
}

// Location returns the timezone this resolver expresses times in.
func (r *Resolver) Location() *time.Location {
	if r != nil && r.loc != nil {
		return r.loc
	}
	return time.Local
}

// DefaultTimeout bounds one resolution call.
//
// Generous on purpose. This runs in the background, where a slow answer
// costs nothing anyone is waiting for, and a timeout costs the resolution
// entirely — measured: a reasoning model behind a gateway took longer than
// twenty seconds for a six-text batch, and every one of them came back
// unresolved.
const DefaultTimeout = 60 * time.Second

// New returns a Resolver over a model. A nil model yields a Resolver whose
// Resolve reports ErrNoModel, which callers treat as "no resolution
// available" and degrade past — never as a failure of the work they were
// doing.
func New(llm domain.Generator, opts ...Option) *Resolver {
	r := &Resolver{llm: llm, timeout: DefaultTimeout, now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// ErrNoModel is returned when a Resolver has no model to ask.
var ErrNoModel = fmt.Errorf("timeaware: no model configured")

// Available reports whether this resolver can answer at all.
func (r *Resolver) Available() bool { return r != nil && r.llm != nil }

// Resolve resolves every text against one anchor, in a single model call.
//
// A zero anchor means "now". The call is bounded by the resolver's timeout,
// and every failure — no model, a refusal, unparsable output — returns an
// error rather than a guess. There is no fallback path that matches words,
// because a fallback that only works in one language is worse than an
// honest "could not resolve": the caller degrades to the write timestamp,
// which is true in every language.
//
// Do not call this on an agent's turn. It is a model call; run it on a
// background worker, an ingestion job or a scheduler.
func (r *Resolver) Resolve(ctx context.Context, anchor time.Time, texts ...string) (*Result, error) {
	if !r.Available() {
		return nil, ErrNoModel
	}
	if len(texts) == 0 {
		return &Result{Anchor: anchor}, nil
	}
	if anchor.IsZero() {
		anchor = r.now()
	}
	// Everything downstream — the prompt, the dates that come back, the
	// calendar-day arithmetic a reader does later — happens in the person's
	// timezone. Converting here is the one place that has to be right.
	if r.loc != nil {
		anchor = anchor.In(r.loc)
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	raw, err := r.llm.GenerateStructured(ctx, r.prompt(anchor, texts), resolveSchema, &domain.GenerationOptions{Temperature: 0})
	if err != nil {
		return nil, fmt.Errorf("timeaware: resolve: %w", err)
	}
	if raw == nil || strings.TrimSpace(raw.Raw) == "" {
		return nil, fmt.Errorf("timeaware: model returned nothing")
	}

	var parsed struct {
		Items []struct {
			Index      int    `json:"index"`
			Text       string `json:"text"`
			Kind       string `json:"kind"`
			Date       string `json:"date"`
			Time       string `json:"time"`
			EndDate    string `json:"end_date"`
			EndTime    string `json:"end_time"`
			Recurrence string `json:"recurrence"`
			AllDay     bool   `json:"all_day"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw.Raw), &parsed); err != nil {
		return nil, fmt.Errorf("timeaware: decode: %w", err)
	}

	out := &Result{Anchor: anchor, References: make([]Reference, 0, len(parsed.Items))}
	for _, item := range parsed.Items {
		if item.Index < 0 || item.Index >= len(texts) {
			continue
		}
		kind := Kind(strings.TrimSpace(strings.ToLower(item.Kind)))
		switch kind {
		case KindPoint, KindRange, KindRecurring, KindNone:
		default:
			kind = KindNone
		}
		ref := Reference{
			Index:      item.Index,
			Text:       strings.TrimSpace(item.Text),
			Kind:       kind,
			Date:       strings.TrimSpace(item.Date),
			Time:       strings.TrimSpace(item.Time),
			EndDate:    strings.TrimSpace(item.EndDate),
			EndTime:    strings.TrimSpace(item.EndTime),
			Recurrence: strings.TrimSpace(item.Recurrence),
			AllDay:     item.AllDay,
			AnchoredAt: anchor,
		}
		// A model that says "point" and gives no date has told us nothing;
		// record that rather than a half-answer a caller would trust.
		if ref.Kind != KindNone {
			if _, ok := ref.Start(); !ok {
				ref.Kind = KindNone
				ref.Date, ref.Time = "", ""
			}
		}
		out.References = append(out.References, ref)
	}
	return out, nil
}

// resolveSchema is the structured output the model is asked for.
var resolveSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"items": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"index":      map[string]interface{}{"type": "integer", "description": "which input text this answers, zero-based"},
					"text":       map[string]interface{}{"type": "string", "description": "ONLY the words in the input that name the time, quoted exactly — never a date, never an explanation. Empty when none."},
					"kind":       map[string]interface{}{"type": "string", "enum": []string{"none", "point", "range", "recurring"}},
					"date":       map[string]interface{}{"type": "string", "description": "REQUIRED whenever kind is not \"none\": the resolved start date, exactly YYYY-MM-DD. The resolved date goes HERE, never in text. Empty string only when kind is \"none\"."},
					"time":       map[string]interface{}{"type": "string", "description": "resolved start time, HH:MM 24-hour; empty when no clock time was named"},
					"end_date":   map[string]interface{}{"type": "string", "description": "end date for a range, YYYY-MM-DD; empty otherwise"},
					"end_time":   map[string]interface{}{"type": "string", "description": "end time for a range, HH:MM; empty otherwise"},
					"recurrence": map[string]interface{}{"type": "string", "description": "plain description of the repeat when kind is recurring; empty otherwise"},
					"all_day":    map[string]interface{}{"type": "boolean", "description": "true when a day was named with no clock time"},
				},
				"required": []string{"index", "kind", "date"},
			},
		},
	},
	"required": []string{"items"},
}

// prompt states the anchor and asks for one answer per text.
//
// The anchor is given with its weekday and offset because "next Friday"
// cannot be resolved from a date alone, and a model told only "2026-09-02"
// has to work out the weekday itself and sometimes gets it wrong.
func (r *Resolver) prompt(anchor time.Time, texts []string) string {
	var sb strings.Builder
	sb.WriteString("Resolve every expression of time in the texts below into absolute dates.\n\n")
	fmt.Fprintf(&sb, "The texts were written at this moment, and every relative expression in them is relative to it:\n")
	zone, _ := anchor.Zone()
	fmt.Fprintf(&sb, "  %s (%s, timezone %s)\n\n",
		anchor.Format("2006-01-02 15:04:05 -07:00"), anchor.Format("Monday"), zoneName(anchor, zone))
	sb.WriteString("Rules:\n")
	sb.WriteString("- Resolve relative expressions against that moment, in any language, however they are phrased.\n")
	sb.WriteString("- Return one item per input text, with its zero-based index, even when the text names no time: use kind \"none\" for those.\n")
	sb.WriteString("- Quote the words you resolved in \"text\", exactly as they appear in the input.\n")
	sb.WriteString("- Dates are YYYY-MM-DD and times are HH:MM on a 24-hour clock, in the anchor's timezone — local wall-clock time for the person who wrote the text, never UTC.\n")
	sb.WriteString("- When a day is named with no clock time, give the date, leave time empty and set all_day.\n")
	sb.WriteString("- Do not invent a time that the text does not name. An unclear expression is kind \"none\".\n\n")
	sb.WriteString("Texts:\n")
	for i, t := range texts {
		fmt.Fprintf(&sb, "[%d] %s\n", i, strings.TrimSpace(t))
	}
	return sb.String()
}

// parseDateTime turns a resolved date and time into an instant in the
// anchor's location.
func parseDateTime(date, clock string, anchor time.Time) (time.Time, bool) {
	date = strings.TrimSpace(date)
	if date == "" {
		return time.Time{}, false
	}
	loc := anchor.Location()
	if loc == nil {
		loc = time.Local
	}
	clock = strings.TrimSpace(clock)
	if clock == "" {
		t, err := time.ParseInLocation("2006-01-02", date, loc)
		return t, err == nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+clock, loc)
	if err != nil {
		// A time we cannot read is not a reason to lose the date.
		t, err2 := time.ParseInLocation("2006-01-02", date, loc)
		return t, err2 == nil
	}
	return t, true
}

// zoneName prefers the IANA name over the abbreviation: "CST" is three
// different zones, and a daylight-saving boundary resolved against the wrong
// one moves an appointment by an hour.
func zoneName(t time.Time, abbrev string) string {
	if loc := t.Location(); loc != nil && loc.String() != "" && loc.String() != "Local" {
		return loc.String()
	}
	if abbrev != "" {
		return abbrev
	}
	return "local"
}
