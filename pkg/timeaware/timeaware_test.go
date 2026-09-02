package timeaware

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// fakeModel answers with a canned payload and records what it was asked.
type fakeModel struct {
	domain.Generator
	reply  string
	err    error
	prompt string
	calls  int
}

func (f *fakeModel) GenerateStructured(_ context.Context, prompt string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	f.calls++
	f.prompt = prompt
	if f.err != nil {
		return nil, f.err
	}
	return &domain.StructuredResult{Valid: true, Raw: f.reply}, nil
}

func anchorAt(t *testing.T, s string) time.Time {
	t.Helper()
	loc := time.FixedZone("CST", 8*3600)
	when, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return when
}

// The point of the package: many texts, one call.
func TestResolveIsOneCallForTheWholeBatch(t *testing.T) {
	m := &fakeModel{reply: `{"items":[
		{"index":0,"text":"明天","kind":"point","date":"2026-09-02","all_day":true},
		{"index":1,"text":"next Friday at 3pm","kind":"point","date":"2026-09-11","time":"15:00"},
		{"index":2,"kind":"none"}
	]}`}
	r := New(m)
	anchor := anchorAt(t, "2026-09-01 20:00")

	res, err := r.Resolve(context.Background(), anchor,
		"用户说明天要去医院", "standup moved to next Friday at 3pm", "the build is green")
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 {
		t.Fatalf("made %d model calls for 3 texts, want 1", m.calls)
	}
	if len(res.References) != 3 {
		t.Fatalf("got %d references, want one per input text", len(res.References))
	}

	// The anchor has to reach the model — without it "明天" is unanswerable.
	if !strings.Contains(m.prompt, "2026-09-01") || !strings.Contains(m.prompt, "Tuesday") {
		t.Errorf("the prompt did not state the anchor and its weekday:\n%s", m.prompt)
	}

	first, _ := res.For(0)
	start, ok := first.Start()
	if !ok || start.Format("2006-01-02") != "2026-09-02" {
		t.Errorf("first reference = %v (%v), want 2026-09-02", start, ok)
	}
	if !first.AllDay || !first.Resolved() {
		t.Errorf("a day with no clock time should be all-day and resolved: %+v", first)
	}

	second, _ := res.For(1)
	if s, _ := second.Start(); s.Format("2006-01-02 15:04") != "2026-09-11 15:00" {
		t.Errorf("second reference = %v, want 2026-09-11 15:00", s)
	}

	// "There is no time here" is an answer, and must be distinguishable from
	// "not asked yet" — otherwise a caller re-asks forever.
	third, ok := res.For(2)
	if !ok || third.Kind != KindNone || third.Resolved() {
		t.Errorf("third reference = %+v, want an explicit none", third)
	}
}

// A model that claims a resolution without giving a date has told us
// nothing, and must not be recorded as if it had.
func TestHalfAnswersAreRecordedAsNone(t *testing.T) {
	m := &fakeModel{reply: `{"items":[{"index":0,"text":"sometime","kind":"point","date":""}]}`}
	res, err := New(m).Resolve(context.Background(), anchorAt(t, "2026-09-01 20:00"), "let us meet sometime")
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := res.For(0)
	if ref.Kind != KindNone || ref.Resolved() {
		t.Fatalf("reference = %+v, want none", ref)
	}
}

// No model, or a failing one, is "no resolution" — never a guess, and never
// a fallback that only works in the languages someone enumerated.
func TestFailureNeverGuesses(t *testing.T) {
	if _, err := New(nil).Resolve(context.Background(), time.Now(), "明天"); err != ErrNoModel {
		t.Errorf("err = %v, want ErrNoModel", err)
	}
	m := &fakeModel{err: context.DeadlineExceeded}
	if _, err := New(m).Resolve(context.Background(), time.Now(), "明天"); err == nil {
		t.Error("a failed call must report an error, not a resolution")
	}
	bad := &fakeModel{reply: "not json"}
	if _, err := New(bad).Resolve(context.Background(), time.Now(), "明天"); err == nil {
		t.Error("unparsable output must report an error")
	}
}

// Any language, any phrasing — the package never looks at the words, so a
// language nobody enumerated works exactly as well as the ones they did.
func TestResolutionIsLanguageAgnostic(t *testing.T) {
	m := &fakeModel{reply: `{"items":[
		{"index":0,"text":"내일","kind":"point","date":"2026-09-02","all_day":true},
		{"index":1,"text":"depois de amanhã","kind":"point","date":"2026-09-03","all_day":true},
		{"index":2,"text":"الأسبوع القادم","kind":"range","date":"2026-09-07","end_date":"2026-09-13"}
	]}`}
	res, err := New(m).Resolve(context.Background(), anchorAt(t, "2026-09-01 20:00"),
		"내일 병원에 갑니다", "reunião depois de amanhã", "الاجتماع الأسبوع القادم")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"2026-09-02", "2026-09-03", "2026-09-07"} {
		ref, ok := res.For(i)
		if !ok {
			t.Fatalf("no reference for text %d", i)
		}
		if s, _ := ref.Start(); s.Format("2006-01-02") != want {
			t.Errorf("text %d resolved to %v, want %s", i, s, want)
		}
	}
	rng, _ := res.For(2)
	if end, ok := rng.End(); !ok || end.Format("2006-01-02") != "2026-09-13" {
		t.Errorf("range end = %v, want 2026-09-13", end)
	}
}

// This file's whole reason to exist: what the reader sees on the day.
func TestNoteReanchorsWithoutAModel(t *testing.T) {
	written := anchorAt(t, "2026-09-01 20:00")
	ref := Reference{
		Text: "明天", Kind: KindPoint, Date: "2026-09-02", AllDay: true, AnchoredAt: written,
	}

	// Read the next day: the appointment is today.
	note := Note(written, ref, anchorAt(t, "2026-09-02 09:00"))
	if !strings.Contains(note, "written 2026-09-01, yesterday") {
		t.Errorf("note does not date the memory: %s", note)
	}
	if !strings.Contains(note, `"明天" = 2026-09-02, today`) {
		t.Errorf("note does not say yesterday's tomorrow is today: %s", note)
	}

	// Read the same day it was written: still tomorrow.
	if note := Note(written, ref, anchorAt(t, "2026-09-01 21:00")); !strings.Contains(note, "2026-09-02, tomorrow") {
		t.Errorf("same-day note = %s", note)
	}

	// Read a week later: it is in the past.
	if note := Note(written, ref, anchorAt(t, "2026-09-09 09:00")); !strings.Contains(note, "7 days ago") {
		t.Errorf("later note = %s", note)
	}

	// Nothing resolved: the write time alone, which is still enough.
	bare := Note(written, Reference{}, anchorAt(t, "2026-09-02 09:00"))
	if !strings.Contains(bare, "written 2026-09-01, yesterday") || strings.Contains(bare, "=") {
		t.Errorf("unresolved note = %s", bare)
	}
	if Note(time.Time{}, Reference{}, time.Now()) != "" {
		t.Error("a note with nothing to say should be empty")
	}
}

func TestDaysBetweenCountsCalendarDaysNotHours(t *testing.T) {
	// 23:00 and 01:00 the next morning are two hours apart and one day.
	late := anchorAt(t, "2026-09-01 23:00")
	early := anchorAt(t, "2026-09-02 01:00")
	if got := DaysBetween(late, early); got != 1 {
		t.Errorf("DaysBetween = %d, want 1", got)
	}
	if got := Describe(early, late); got != "tomorrow" {
		t.Errorf("Describe = %q, want tomorrow", got)
	}
}

// The schema is part of the contract with the model; a change that drops a
// field silently stops resolving something.
func TestSchemaAsksForEverythingAReferenceCarries(t *testing.T) {
	raw, err := json.Marshal(resolveSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"index", "kind", "date", "time", "end_date", "end_time", "recurrence", "all_day", "text"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Errorf("schema does not ask for %q", field)
		}
	}
}

// Local time is the setting most likely to be wrong and least likely to look
// wrong. A server in UTC must resolve for the person, not for itself.
func TestAnchorIsExpressedInThePersonsTimezone(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	m := &fakeModel{reply: `{"items":[{"index":0,"kind":"none"}]}`}
	r := New(m, WithLocation(shanghai))

	// A UTC anchor at 16:30 is 00:30 the NEXT day in Shanghai. The model
	// must be told the person's wall clock, or "tonight" and "tomorrow" are
	// both answered for the wrong day.
	utcAnchor := time.Date(2026, 9, 1, 16, 30, 0, 0, time.UTC)
	if _, err := r.Resolve(context.Background(), utcAnchor, "明天开会"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.prompt, "2026-09-02 00:30") {
		t.Errorf("the prompt did not state the person's local time:\n%s", m.prompt)
	}
	if !strings.Contains(m.prompt, "+08:00") || !strings.Contains(m.prompt, "Asia/Shanghai") {
		t.Errorf("the prompt did not state the offset and zone:\n%s", m.prompt)
	}
	// The weekday must be the local one too: 16:30 UTC Tuesday is already
	// Wednesday in Shanghai, and "next Friday" counts from the local day.
	if !strings.Contains(m.prompt, "Wednesday") {
		t.Errorf("the prompt used the server's weekday, not the person's:\n%s", m.prompt)
	}
	if r.Location() != shanghai {
		t.Error("Location() should report the configured zone")
	}
}

// Resolved dates come back as local wall-clock times in that same zone.
func TestResolvedInstantsAreLocal(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	m := &fakeModel{reply: `{"items":[{"index":0,"text":"明天下午三点","kind":"point","date":"2026-09-02","time":"15:00"}]}`}
	res, err := New(m, WithLocation(shanghai)).
		Resolve(context.Background(), time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), "明天下午三点开会")
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := res.For(0)
	start, ok := ref.Start()
	if !ok {
		t.Fatal("nothing resolved")
	}
	if got := start.Format("2006-01-02 15:04 -07:00"); got != "2026-09-02 15:00 +08:00" {
		t.Errorf("start = %s, want 3pm Shanghai time", got)
	}
	// The same instant in UTC is the morning — which is what a naive
	// implementation would have stored as the appointment.
	if got := start.UTC().Format("15:04"); got != "07:00" {
		t.Errorf("start in UTC = %s, want 07:00", got)
	}
}

// A memory written in one timezone and read in another is described by the
// reader's calendar, because that is the only calendar in which "today"
// means anything to them.
func TestNoteUsesTheReadersCalendar(t *testing.T) {
	tokyo := time.FixedZone("Asia/Tokyo", 9*3600)
	vienna := time.FixedZone("Europe/Vienna", 2*3600)

	// 2026-09-01 23:30 in Tokyo is 2026-09-01 16:30 in Vienna: the same
	// instant, and in this case the same date. Two hours later in Tokyo it
	// is the 2nd there and still the 1st in Vienna.
	writtenTokyo := time.Date(2026, 9, 2, 1, 0, 0, 0, tokyo) // 2026-09-01 18:00 Vienna
	ref := Reference{Text: "明日", Kind: KindPoint, Date: "2026-09-03", AllDay: true, AnchoredAt: writtenTokyo}

	// A Vienna reader on the evening of the 1st: the memory was written
	// today by their calendar, even though it is already the 2nd in Tokyo.
	nowVienna := time.Date(2026, 9, 1, 20, 0, 0, 0, vienna)
	note := Note(writtenTokyo, ref, nowVienna)
	if !strings.Contains(note, "written 2026-09-01, today") {
		t.Errorf("note = %s, want the reader's own date for the write time", note)
	}

	// The same call from Tokyo, at the same instant, says the 2nd.
	nowTokyo := nowVienna.In(tokyo)
	if note := Note(writtenTokyo, ref, nowTokyo); !strings.Contains(note, "written 2026-09-02, today") {
		t.Errorf("note from Tokyo = %s, want 2026-09-02", note)
	}

	// And DaysBetween agrees with whichever reader is asking.
	if got := DaysBetween(nowVienna, writtenTokyo); got != 0 {
		t.Errorf("DaysBetween in Vienna = %d, want 0", got)
	}
	if got := DaysBetween(nowTokyo, writtenTokyo); got != 0 {
		t.Errorf("DaysBetween in Tokyo = %d, want 0", got)
	}
}

// Across a real DST boundary the zone name matters, not the offset: the
// resolver must hand the model something it can apply the rules from.
func TestPromptCarriesTheIANAZoneForDaylightSaving(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Skip("no tzdata on this machine")
	}
	m := &fakeModel{reply: `{"items":[{"index":0,"kind":"none"}]}`}
	// The Sunday Austria puts its clocks back.
	anchor := time.Date(2026, 10, 24, 12, 0, 0, 0, vienna)
	if _, err := New(m, WithLocation(vienna)).Resolve(context.Background(), anchor, "next Sunday at 2am"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.prompt, "Europe/Vienna") {
		t.Errorf("the prompt named no IANA zone, so DST rules cannot be applied:\n%s", m.prompt)
	}
}
